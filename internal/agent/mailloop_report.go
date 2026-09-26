package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ErrNoMailCheckJudgement is returned by MailLoopReport when the registry has
// no MailCheckProvider installed. It is an ERROR rather than an empty report on
// purpose: "nothing is missing a mail loop" and "I could not look" render
// identically to a caller that flattens them, and a detector that reports the
// second as the first is the exact failure mode this whole lineage is about
// (mg-de08 -> mg-738f -> mg-032b).
var ErrNoMailCheckJudgement = errors.New("agent: no mail-check provider installed; diagnose has no basis to judge mail loops")

// MailLoopFinding names one agent that diagnose judges to have NO mail-check
// schedule: it can be mailed, and nothing will ever wake it to read the mail.
//
// It carries both the bare Name (what an operator types after `pogo agent
// diagnose`) and the event-log Identity ("crew-<name>" / "cat-<name>"), because
// the two are consumed by different readers and collapsing them has bitten this
// tree before — see synthwatch's episode roster, which carries both for the
// same reason.
type MailLoopFinding struct {
	Name     string      `json:"name"`
	Identity string      `json:"identity"`
	Type     AgentType   `json:"type"`
	Status   AgentStatus `json:"status"`
	// Alive is the observed liveness (pidAlive), not the Status field. The
	// mg-738f disjunct that this population turns on is "CONFIGURED and actually
	// RUNNING", and that disjunct rests on evidence rather than on a status
	// string; a reader of the finding is owed the same evidence.
	Alive bool `json:"alive"`
}

// MailLoopExclusionReason names WHY an agent was not judged.
//
// The set MATCHES the categories `pogo check-mailloops --help` lists. It did
// not until mg-7b3f: IsConfiguredAgent returned false both for an unreadable
// prompt tree and for a genuinely unconfigured agent, so the set was
// deliberately kept coarser than the disclosure rather than emit a reason the
// code could not back. ConfiguredStateFor now keeps the error, so the fourth
// value is computable — see mailLoopExclusionFor, still the one place a reason
// is decided.
//
// Three of the four are decisions and one is a fault. ExclusionPolecat,
// ExclusionNotRunning and ExclusionNotConfigured all say "nothing is owed
// here"; ExclusionUnreadablePrompts says "I could not look", which is not a
// verdict about the agent at all. Keeping them in one set is fine — keeping
// them in one VALUE was the defect.
type MailLoopExclusionReason string

const (
	// ExclusionPolecat: polecats register their own loop at spawn (mg-e633)
	// with their own escalation path on failure (mg-6fe0).
	ExclusionPolecat MailLoopExclusionReason = "polecat"
	// ExclusionNotRunning: a configured agent that is not running is owed
	// nothing — "not there" is not a fault.
	ExclusionNotRunning MailLoopExclusionReason = "not_running"
	// ExclusionNotConfigured: no prompt on this machine's prompt tree, which
	// WAS read. This agent is not one of ours — a fact about intent, needing no
	// action.
	ExclusionNotConfigured MailLoopExclusionReason = "not_configured"
	// ExclusionUnreadablePrompts: the prompt tree could not be read, so this
	// agent could not be classified at all. UNKNOWN, not a clean exclusion —
	// the instrument is broken, and an operator should act on that even though
	// Actionable deliberately does not (see its doc comment).
	ExclusionUnreadablePrompts MailLoopExclusionReason = "unreadable_prompts"
)

// Describe renders the reason as the phrase an operator reads. An unrecognised
// reason renders verbatim rather than as a blank: a newer pogod naming a
// category this binary does not know must not read as no reason at all.
func (r MailLoopExclusionReason) Describe() string {
	switch r {
	case ExclusionPolecat:
		return "registers its own mail loop at spawn"
	case ExclusionNotRunning:
		return "not running"
	case ExclusionNotConfigured:
		return "not configured — no prompt on this machine"
	case ExclusionUnreadablePrompts:
		return "UNREADABLE prompt tree — could not be classified at all"
	case "":
		return "reason not reported"
	}
	return string(r)
}

// MailLoopExclusion names one agent the report did NOT judge, and why. It is
// not a finding: nothing here is wrong, and nothing here is an all-clear
// either. It is the census line that turns "judged 2 of 6" from a number into
// a statement (mg-0db1).
//
// MailLoop is what the scheduler says about this agent's mail-check schedule —
// an OBSERVATION, not a judgement (mg-b70c). It turns "3 not judged" into "3
// not judged; all 3 have a loop anyway", which the report could not say while
// it only disclosed the set. It is informational and nothing else: it does not
// move the agent into Judged, it cannot put the agent in Missing, and
// Actionable never reads it. The eligibility boundary is mg-738f's cry-wolf
// guarantee, and widening what the check JUDGES is a different decision from
// widening what it REPORTS.
type MailLoopExclusion struct {
	Name     string                  `json:"name"`
	Type     AgentType               `json:"type"`
	Reason   MailLoopExclusionReason `json:"reason"`
	MailLoop MailLoopObservation     `json:"mail_loop"`
}

// MailLoopObservation is whether a mail-check schedule was FOUND for an agent
// the report did not judge.
//
// It has three states, and the empty one is not "absent". A pogod older than
// mg-b70c does not send the field, and a plain decode turns that into "" — so
// "" must read as "not reported", never as "no loop". The field carries no
// omitempty for the same reason Unjudged is a pointer: a daemon that does
// observe always puts a non-empty value on the wire, and only an old one
// leaves it blank.
type MailLoopObservation string

const (
	// MailLoopObservedPresent: the scheduler holds a mail-check for this agent,
	// by the same alias rules diagnose uses. Something will wake it.
	MailLoopObservedPresent MailLoopObservation = "present"
	// MailLoopObservedAbsent: no mail-check is registered. For an unjudged
	// agent this is NOT a finding — a stopped, unexpected agent has its loop
	// reaped by design, and a polecat between spawn and registration has none
	// yet. It is a fact stated beside the exclusion, not a vote against it.
	MailLoopObservedAbsent MailLoopObservation = "absent"
)

// Describe renders the observation as the phrase an operator reads.
func (o MailLoopObservation) Describe() string {
	switch o {
	case MailLoopObservedPresent:
		return "has a mail loop"
	case MailLoopObservedAbsent:
		return "no mail loop registered"
	case "":
		return "mail loop not reported"
	}
	return string(o)
}

// observeMailLoop reads whether a has a mail-check schedule WITHOUT judging it.
//
// It deliberately does not go through mailLoopFor: that function's whole job
// is to refuse an answer for an agent outside the eligibility boundary, and
// routing an excluded agent through it would either return mailLoopUnknown
// (and observe nothing) or tempt a caller to loosen mailLoopJudgeable (and
// judge it). This asks the provider the same question with the same identity
// mailLoopFor would pass, and returns a fact with no verdict attached.
func observeMailLoop(a *Agent, p MailCheckProvider) MailLoopObservation {
	if p.HasMailCheck(a.EventAgent()) {
		return MailLoopObservedPresent
	}
	return MailLoopObservedAbsent
}

// MailLoopReport is one reading of the fleet's mail-delivery paths: every agent
// diagnose has standing to judge, and which of them have no way to be woken.
//
// Scanned counts every agent in the registry; Judged counts the subset
// mailLoopJudgeable admits (see its doc comment for who is deliberately NOT
// judged — polecats, configured-but-not-running agents, and anything with an
// unreadable prompt tree). Missing is the RED set, sorted by name; Unjudged is
// the complement of Judged, also sorted by name.
//
// Judged is load-bearing for any consumer that wants to alert: an empty Missing
// with Judged == 0 means nothing was judged, which is not the same statement as
// a clean fleet.
//
// # Why Unjudged is a POINTER
//
// ABSENT and EMPTY must be distinguishable on the wire, and they mean opposite
// things: an empty list is "every agent was judged", an absent one is "this
// daemon does not report the set at all". internal/client plain-decodes this
// struct with no version negotiation, so a plain slice would flatten a pogod
// too old to send the field into a confident "0 not judged" — this issue's
// exact defect, reproduced inside its own fix, green, on the fleet that filed
// it. That is not hypothetical: when this shipped the running pogod was ~93
// commits behind main. The pointer makes "unknown" unsayable as zero, and the
// tag carries no `omitempty` so a report that judged everything still puts
// `"unjudged": []` on the wire (the shape internal/staleness already ships).
//
// # And why the null does not explain itself
//
// The null stays BARE: no `unjudged_reason`, no coverage object beside it. The
// asymmetry drellem2/pogo#127's review named — the text render prints a
// sentence, the JSON prints a null — is prose and advice, not data. Scanned and
// Judged are sent by every daemon version, so the COUNT is already derivable on
// the wire by the same arithmetic renderCoverage uses; what `null` withholds is
// exactly what `null` means. The ruling (mg-4692) is that this is paid for in
// DOCUMENTATION, and the place it is paid is `pogo check-mailloops --help` —
// where a machine reader looks — not here, where no consumer does. See
// cmd/pogo/checkmailloops.go for the shipped text and the reasoning; a test
// holds that text against the bytes this struct marshals.
//
// The ruling is priced against there being exactly TWO wire states. A daemon
// that reported the set PARTIALLY would be a third, `null` vs `[]` would stop
// carrying the distinction, and the shape question would be live again.
type MailLoopReport struct {
	Now      time.Time            `json:"now"`
	Scanned  int                  `json:"scanned"`
	Judged   int                  `json:"judged"`
	Missing  []MailLoopFinding    `json:"missing,omitempty"`
	Unjudged *[]MailLoopExclusion `json:"unjudged"`
}

// MailLoopReport enumerates the registry and applies diagnose's OWN mail-loop
// judgement to every agent in it, returning the ones with no delivery path.
//
// # Why this exists as a separate reader
//
// The judgement has been correct since mg-de08 and complete since mg-738f, and
// until now its ONLY consumer was `pogo agent diagnose <name>` — a CLI
// subcommand that takes the agent's name as an argument. That makes a missing
// mail loop DETECTABLE but never ANNOUNCED: it helps exactly one person, the
// one who already suspects that one agent, and two years of this fleet's
// incidents say the operator does not know which name to type. This method is
// the fleet-wide read that lets something OUTSIDE the failed agent say so out
// loud (mg-032b; see internal/deafwatch for the announcer).
//
// # It must not re-derive the judgement
//
// Every classification here goes through mailLoopFor, the same function
// Registry.diagnose calls. That is deliberate and load-bearing: a second
// implementation of "who is owed a mail loop" would drift from the first, and
// the boundaries in mailLoopJudgeable — polecats excluded, an unreadable prompt
// tree staying silent — are the cry-wolf guarantees mg-738f argued for. A
// detector that inherits them cannot lose them.
func (r *Registry) MailLoopReport() (MailLoopReport, error) {
	r.mu.RLock()
	provider := r.mailChecks
	r.mu.RUnlock()
	if provider == nil {
		return MailLoopReport{}, ErrNoMailCheckJudgement
	}

	rep := MailLoopReport{Now: time.Now()}
	// Non-nil from the first line, so a report that judged everything still
	// serialises `"unjudged": []` — the wire signal that this daemon DOES
	// report the set and found it empty. See the struct's doc comment.
	unjudged := []MailLoopExclusion{}
	for _, a := range r.List() {
		rep.Scanned++
		switch mailLoopFor(a, provider) {
		case mailLoopPresent:
			rep.Judged++
		case mailLoopMissing:
			rep.Judged++
			rep.Missing = append(rep.Missing, MailLoopFinding{
				Name:     a.Name,
				Identity: a.EventAgent(),
				Type:     a.Type,
				Status:   a.Status,
				Alive:    a.Alive(),
			})
		default:
			// mailLoopUnknown. The reason comes from mailLoopExclusionFor —
			// the same function mailLoopJudgeable is defined in terms of — so
			// the roster cannot name a reason the predicate would not give.
			// MailLoop is observed, not judged (mg-b70c): it never
			// touches Judged or Missing.
			unjudged = append(unjudged, MailLoopExclusion{
				Name:     a.Name,
				Type:     a.Type,
				Reason:   mailLoopExclusionFor(a),
				MailLoop: observeMailLoop(a, provider),
			})
		}
	}
	sort.Slice(rep.Missing, func(i, j int) bool { return rep.Missing[i].Name < rep.Missing[j].Name })
	sort.Slice(unjudged, func(i, j int) bool { return unjudged[i].Name < unjudged[j].Name })
	rep.Unjudged = &unjudged
	return rep, nil
}

// Render formats the report for a terminal. It leads with the agent NAMES,
// because not knowing which name to type is the entire reason this fault went
// two tickets without an announcement.
//
// A report that judged nothing says so in as many words rather than printing
// the same reassuring line a clean fleet prints.
//
// ALL THREE branches end in renderCoverage. The green branch used to be the
// only one that omitted who-was-not-judged, which is the defect
// drellem2/pogo#127 reported; routing every branch through one renderer is what
// makes them structurally incapable of diverging again (mg-0db1).
func (rep MailLoopReport) Render() string {
	var b strings.Builder
	if len(rep.Missing) == 0 {
		if rep.Judged == 0 {
			fmt.Fprintf(&b, "NOTHING JUDGED: %d agent(s) in the registry, none of them judgeable.\n", rep.Scanned)
			b.WriteString("This is not an all-clear. Polecats, configured-but-stopped agents, and\n")
			b.WriteString("agents whose prompt tree could not be read are deliberately not judged.\n")
			rep.renderCoverage(&b)
			return b.String()
		}
		fmt.Fprintf(&b, "All %d judged agent(s) have a mail-check schedule. (%d in the registry.)\n",
			rep.Judged, rep.Scanned)
		rep.renderCoverage(&b)
		return b.String()
	}
	fmt.Fprintf(&b, "%d agent(s) have NO mail-check schedule — they can be mailed, and nothing\n"+
		"will ever wake them to read it:\n\n", len(rep.Missing))
	for _, m := range rep.Missing {
		live := "not running"
		if m.Alive {
			live = "ALIVE"
		}
		fmt.Fprintf(&b, "  %-20s %-8s %s\n", m.Name, m.Type, live)
	}
	fmt.Fprintf(&b, "\nJudged %d of %d agents in the registry.\n", rep.Judged, rep.Scanned)
	rep.renderCoverage(&b)
	b.WriteString("\nConfirm one:\n")
	for _, m := range rep.Missing {
		fmt.Fprintf(&b, "  pogo agent diagnose %s\n", m.Name)
	}
	b.WriteString("\nRestore one:\n")
	for _, m := range rep.Missing {
		fmt.Fprintf(&b, "  pogo schedule %s --cron \"*/10 * * * *\" --id mail-check-%s \\\n"+
			"      --replay once --message \"Check your mail with mg mail list %s.\"\n",
			m.Name, m.Name, m.Name)
	}
	return b.String()
}

// renderCoverage states WHO was not judged and WHY. Every Render branch calls
// it, including the green one — a verdict delivered over a subset of the fleet
// has to name the subset, or "judged 2 of 6, all fine" reads as "the fleet is
// fine" (drellem2/pogo#127).
//
// It has two modes, and the difference between them is the whole point:
//
//   - Unjudged SUPPLIED — name every excluded agent and its reason. An empty
//     supplied list means genuinely nobody was excluded, so there is nothing to
//     print.
//   - Unjudged ABSENT — the daemon is older than this binary and does not
//     report the set. The COUNT is still honest, because Scanned and Judged are
//     fields every version sends and the count falls out of arithmetic; WHO and
//     WHY are UNKNOWN and are printed as unknown. This branch must never print
//     zero: "the daemon did not tell me" and "nobody was excluded" are opposite
//     statements, and collapsing them is the defect this command was filed for.
func (rep MailLoopReport) renderCoverage(b *strings.Builder) {
	if rep.Unjudged == nil {
		n := rep.Scanned - rep.Judged
		if n <= 0 {
			// Every scanned agent was judged — derivable without the field, so
			// this is a real all-clear on coverage rather than an assumed one.
			return
		}
		fmt.Fprintf(b, "\nNot judged: %d of %d — WHO and WHY are UNKNOWN. This pogod is older than the\n"+
			"client and does not report the unjudged set, so the verdict above covers %d of\n"+
			"%d agents and cannot say which. Upgrade pogod, or ask one at a time with\n"+
			"`pogo agent diagnose <name>`.\n", n, rep.Scanned, rep.Judged, rep.Scanned)
		return
	}
	if len(*rep.Unjudged) == 0 {
		return
	}
	fmt.Fprintf(b, "\nNot judged: %d of %d. This is not a verdict on them — it is who the verdict\n"+
		"above does NOT cover:\n", len(*rep.Unjudged), rep.Scanned)
	for _, u := range *rep.Unjudged {
		fmt.Fprintf(b, "  %-20s %-8s %s [%s]\n", u.Name, u.Type, u.Reason.Describe(), u.MailLoop.Describe())
	}
	renderUnjudgedLoops(b, *rep.Unjudged)
}

// renderUnjudgedLoops summarises the mail-loop OBSERVATION over the unjudged
// set (mg-b70c): "3 not judged; all 3 have a loop anyway".
//
// It is informational only, and the sentence says so, because the same line
// sits under a RED verdict and a green one: an unjudged agent with no loop is
// not a finding, and an unjudged agent with one does not make the verdict
// cover it. Unreported observations (a pogod older than mg-b70c) are counted
// as unknown and never folded into either side — "the daemon did not say" is
// not "no loop", the same distinction renderCoverage draws one field up.
func renderUnjudgedLoops(b *strings.Builder, unjudged []MailLoopExclusion) {
	n := len(unjudged)
	var present, absent, unknown int
	for _, u := range unjudged {
		switch u.MailLoop {
		case MailLoopObservedPresent:
			present++
		case MailLoopObservedAbsent:
			absent++
		default:
			unknown++
		}
	}
	switch {
	case unknown == n:
		fmt.Fprintf(b, "Mail loops of the %d not judged: NOT REPORTED — this pogod is older than the\n"+
			"client and does not observe them.\n", n)
		return
	case present == n:
		fmt.Fprintf(b, "All %d not judged have a mail-check schedule anyway.\n", n)
	default:
		fmt.Fprintf(b, "Of the %d not judged: %d have a mail-check schedule, %d have none registered", n, present, absent)
		if unknown > 0 {
			fmt.Fprintf(b, ", %d not reported", unknown)
		}
		b.WriteString(".\n")
	}
	b.WriteString("(Observed, not judged: this does not move the verdict or the exit status.)\n")
}

// Actionable reports whether the report found anything worth acting on. It is
// the CLI's exit-status predicate.
//
// The unjudged set deliberately does NOT move it. Everything in that set is
// excluded on purpose (mg-738f drew the boundary and the cry-wolf guarantee
// rests on it), so exiting non-zero because a polecat exists would make the
// command's exit status useless. mg-0db1 changed what the command DISCLOSES,
// not what it judges — recorded here rather than left as a silence. mg-b70c
// then OBSERVED each unjudged agent's mail loop, and that observation does not
// move it either: an excluded agent gets a sentence in the report, not a vote
// in the exit status.
func (rep MailLoopReport) Actionable() bool { return len(rep.Missing) > 0 }

// handleMailLoops serves GET /agents/mail-loops: the FLEET-WIDE read of the
// judgement /agents/{name}/diagnose has only ever answered one agent at a time.
//
// It answers 503 rather than 200-with-an-empty-body when the registry has no
// basis to judge. A 200 with `{"missing":[]}` would tell the caller the fleet is
// reachable when in fact nothing was evaluated, and that substitution — silence
// standing in for an all-clear — is the failure this endpoint's whole lineage
// exists to stop.
func (r *Registry) handleMailLoops(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	rep, err := r.MailLoopReport()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rep)
}
