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

// MailLoopReport is one reading of the fleet's mail-delivery paths: every agent
// diagnose has standing to judge, and which of them have no way to be woken.
//
// Scanned counts every agent in the registry; Judged counts the subset
// mailLoopJudgeable admits (see its doc comment for who is deliberately NOT
// judged — polecats, configured-but-not-running agents, and anything with an
// unreadable prompt tree). Missing is the RED set, sorted by name.
//
// Judged is load-bearing for any consumer that wants to alert: an empty Missing
// with Judged == 0 means nothing was judged, which is not the same statement as
// a clean fleet.
type MailLoopReport struct {
	Now     time.Time         `json:"now"`
	Scanned int               `json:"scanned"`
	Judged  int               `json:"judged"`
	Missing []MailLoopFinding `json:"missing,omitempty"`
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
		}
	}
	sort.Slice(rep.Missing, func(i, j int) bool { return rep.Missing[i].Name < rep.Missing[j].Name })
	return rep, nil
}

// Render formats the report for a terminal. It leads with the agent NAMES,
// because not knowing which name to type is the entire reason this fault went
// two tickets without an announcement.
//
// A report that judged nothing says so in as many words rather than printing
// the same reassuring line a clean fleet prints.
func (rep MailLoopReport) Render() string {
	var b strings.Builder
	if len(rep.Missing) == 0 {
		if rep.Judged == 0 {
			fmt.Fprintf(&b, "NOTHING JUDGED: %d agent(s) in the registry, none of them judgeable.\n", rep.Scanned)
			b.WriteString("This is not an all-clear. Polecats, configured-but-stopped agents, and\n")
			b.WriteString("agents whose prompt tree could not be read are deliberately not judged.\n")
			return b.String()
		}
		fmt.Fprintf(&b, "All %d judged agent(s) have a mail-check schedule. (%d in the registry.)\n",
			rep.Judged, rep.Scanned)
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

// Actionable reports whether the report found anything worth acting on. It is
// the CLI's exit-status predicate.
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
