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

// This file answers one question the rest of the package could not: WHICH
// CONFIGURED AGENTS ARE NOT HERE.
//
// Everything else that reports on the fleet iterates the REGISTRY — `pogo agent
// list`, Registry.MailLoopReport and therefore internal/deafwatch, the stall
// watch, ackwatch's cohorts. That set contains exactly the agents pogod is
// currently running, so an agent that was stopped is not a row with a bad value
// in it; it is no row at all. A reader cannot tell "this agent is down" from
// "this agent was never configured on this machine", and no amount of care
// reading a roster fixes a member that is not in it.
//
// mg-7d20 is the bill for that. `crew-doctor` was stopped on 2026-08-10 during
// an auth-incident cleanup and stayed down 2 days 21 hours. It is on-demand by
// design (auto_start = false and restart_on_crash = false, shipped deliberately
// by mg-b2cc for gh #18), so nothing was going to restart it — and nothing
// reported it either:
//
//   - the heartbeat stall-watch reads a sweep log's mtime, and a stopped agent
//     writes no sweep log; a file that stops being written is indistinguishable
//     from one nobody watched;
//   - ackwatch measures completion against a schedule, and pogod REMOVED the
//     mail-check at stop time (reason=agent_gone), so there was no schedule left
//     to under-complete;
//   - deafwatch iterates the registry, and this population is registry-absent by
//     construction — cmd/pogod's registryLiveness doc says so in as many words:
//     "A detector's existence is not its coverage; the question is which set it
//     iterates."
//
// The set this file iterates is the CONFIGURED one: every crew/mayor prompt on
// disk. Presence in the registry, a park flag, or neither, is then a property of
// each member rather than the precondition for being looked at.
//
// It is deliberately NOT a judgement about whether an absence is wrong.
// RosterReport reports state and class; deciding that an absence has gone on too
// long is internal/absentwatch's job, and how long is too long depends on the
// class. Keeping the two apart is what lets `pogo agent roster` show an
// on-demand agent as absent without that being an alarm.

// ErrNoRosterJudgement is returned when the roster cannot be computed at all:
// no registry to compare against. As with ErrNoMailCheckJudgement, callers must
// not render it as a clean roster — "I could not look" and "everybody is here"
// are the two readings this whole file exists to keep apart.
var ErrNoRosterJudgement = errors.New("no agent registry: cannot judge roster presence")

// RosterState is where one configured agent stands relative to the running
// fleet.
type RosterState string

const (
	// RosterPresent means the registry holds an entry for this agent. It says
	// nothing about health — an entry with status=exited is still present, and
	// visibly so, which is the whole difference from RosterAbsent.
	RosterPresent RosterState = "present"
	// RosterParked means the agent has a park flag on disk: dormant BY
	// DECLARATION. Park is the supported way to be down, it already shows in
	// `pogo agent list` as status=parked, and it is never a finding here.
	RosterParked RosterState = "parked"
	// RosterAbsent means the agent is configured on this machine, is not
	// parked, and has no registry entry. Nothing in the fleet's existing
	// instruments can express this state, which is why it has a name.
	RosterAbsent RosterState = "absent"
)

// RosterClass is what the agent's own frontmatter says should happen to it, and
// it is the input that decides whether an absence is a fault or a Tuesday.
type RosterClass string

const (
	// RosterSupervised is auto_start = true: pogod brings this agent up at
	// boot, so its absence contradicts the machine's own desired state.
	RosterSupervised RosterClass = "supervised"
	// RosterOnDemand is auto_start = false: nothing brings this agent up but a
	// person or another agent asking. Its absence is normal in the short run
	// and worth saying out loud in the long run — mg-7d20's doctor is exactly
	// this class, and 2d21h of it was nobody's decision.
	RosterOnDemand RosterClass = "on_demand"
	// RosterUnclassifiable is a prompt that EXISTS and could not be read or
	// parsed. It is its own class rather than being folded into on-demand: we
	// know the agent was configured and we do not know what was wanted for it,
	// and reporting that as the quieter of the two answers would be guessing in
	// the direction of silence (mg-de08, mg-7b3f).
	RosterUnclassifiable RosterClass = "unclassifiable"
)

// RosterMember is one configured crew/mayor agent and where it stands.
type RosterMember struct {
	// Name is the bare agent name — what an operator types after
	// `pogo agent start`.
	Name string `json:"name"`
	// Identity is the event-log identity ("crew-<name>"), the shape schedules
	// and the event log address agents by.
	Identity string `json:"identity"`
	// Category is "mayor" or "crew", as ListPrompts reports it.
	Category string `json:"category"`
	// State is presence: present, parked, or absent.
	State RosterState `json:"state"`
	// Class is the lifecycle the prompt declares.
	Class RosterClass `json:"class"`
	// RestartOnCrash is the RESOLVED flag (frontmatter over the crew default),
	// carried because a reader deciding what to do about an absence needs to
	// know whether a restart would stick.
	RestartOnCrash bool `json:"restart_on_crash"`
	// Status is the registry status when State is present; empty otherwise.
	Status AgentStatus `json:"status,omitempty"`
	// Alive is the registry's liveness when State is present.
	Alive bool `json:"alive,omitempty"`
	// Error is why the prompt could not be classified, set only for
	// RosterUnclassifiable.
	Error string `json:"error,omitempty"`
	// LifecycleWarning is set when this member's two lifecycle flags are in the
	// one combination no prompt in this fleet should carry: auto_start = true
	// with restart_on_crash = false. See CoupledFlagWarning.
	LifecycleWarning string `json:"lifecycle_warning,omitempty"`
}

// CoupledFlagWarning is the text reported for a member declaring
// auto_start = true with restart_on_crash = false.
//
// THE COUPLING. Those two flags are not independent. Together in that shape they
// are the only configuration that can reach cmd/pogod's desired-state
// fall-through with expected=true over an agent that is durably dead — registry
// entry gone, witness gone, and auto_start still saying "should be running" —
// which leaves a mail-check firing at nobody (mg-8677). The registry arm of
// registryLiveness.AgentState already refuses to let auto_start override a
// corpse it can SEE; this is the case where it cannot see one. With
// restart_on_crash = true that arm returns AgentAlive and never reaches the
// fall-through, so both-true is the safe form and is what every healthy crew
// agent already does.
//
// It is reported HERE, and deliberately not added to `pogo doctor --check`:
// mg-10e3 records that nothing on this host reads that checklist on a cadence,
// and an instrument that cannot go red for the failure it names is worse than
// none, because its presence is the reason nobody builds the one that would
// work. internal/agent's embedded-prompt test enforces the same rule over the
// prompts pogo SHIPS; this is the reader for a deployment's own tree, which no
// repo test can reach (mg-7d20).
const CoupledFlagWarning = "auto_start = true with restart_on_crash = false — " +
	"pogod will start this at boot and never bring it back after an exit; set " +
	"restart_on_crash = true alongside auto_start (mg-8677)"

// Absent reports whether this member is a finding — configured, unparked, and
// not in the registry.
func (m RosterMember) Absent() bool { return m.State == RosterAbsent }

// RosterReport is one reading of the configured roster against the running
// fleet.
type RosterReport struct {
	// Now is the sample time.
	Now time.Time `json:"now"`
	// Configured is how many crew/mayor prompts this machine has. Zero means
	// there was nothing to compare, which is NOT a complete roster — callers
	// must say so rather than printing the same line a healthy fleet prints.
	Configured int `json:"configured"`
	// Present, Parked and the length of Absent partition Configured.
	Present int `json:"present"`
	Parked  int `json:"parked"`
	// Absent is the finding set, sorted by name.
	Absent []RosterMember `json:"absent"`
	// Coupled is every member whose lifecycle flags are in the forbidden
	// combination, sorted by name. It is orthogonal to presence — a RUNNING
	// agent can carry it, and that is when it matters most, because the fault it
	// predicts only lands after the agent next exits. See CoupledFlagWarning.
	Coupled []RosterMember `json:"coupled"`
	// Members is every configured agent, sorted by name, whatever its state.
	// This is the roster proper: the thing that could not previously be
	// printed, because a member that is not running has nowhere to appear.
	Members []RosterMember `json:"members"`
}

// Complete reports whether every configured agent is accounted for as running
// or deliberately parked. It is the one-line check mg-7d20 asked for, and it is
// false on an empty roster because "nothing configured" is not completeness.
func (rep RosterReport) Complete() bool {
	return rep.Configured > 0 && len(rep.Absent) == 0
}

// RosterReport compares the CONFIGURED crew/mayor set against the registry.
//
// The comparison order is registry, then park flag, then absent — the same
// precedence handleAgents already uses when it merges parked agents into the
// listing, so a mid-wake agent that is both registered and flagged reads as
// present in both places rather than one each.
//
// An unreadable prompt TREE is an error: we could not enumerate the configured
// set, so we cannot say anybody is missing and must not say nobody is. An
// unreadable prompt FILE is not — the tree told us the agent exists, so it is
// still a roster member, classified RosterUnclassifiable with the parse error
// attached.
func (r *Registry) RosterReport() (RosterReport, error) {
	if r == nil {
		return RosterReport{}, ErrNoRosterJudgement
	}
	cands, err := autoStartCandidates()
	if err != nil {
		return RosterReport{}, fmt.Errorf("roster: %w", err)
	}

	// Non-nil from the first line, so a clean report still serialises
	// `"absent": []` and `"coupled": []` — the wire signal that this daemon DOES
	// report the sets and found them empty, rather than that it does not know
	// about them.
	rep := RosterReport{
		Now:     time.Now(),
		Members: []RosterMember{},
		Absent:  []RosterMember{},
		Coupled: []RosterMember{},
	}
	for _, c := range cands {
		m := RosterMember{
			Name:     c.name,
			Identity: crewIdentity(c.name),
			Category: c.category,
		}
		meta, _, perr := ParsePromptFrontmatter(c.path)
		switch {
		case perr != nil:
			m.Class = RosterUnclassifiable
			m.Error = perr.Error()
		case meta != nil && meta.AutoStart:
			m.Class = RosterSupervised
		default:
			m.Class = RosterOnDemand
		}
		// The RESOLVED flag, not the declared one: an auto_start prompt that
		// omits restart_on_crash inherits the crew always-on default and is
		// already both-true, so it is not a finding.
		m.RestartOnCrash = ResolveRestartOnCrash(c.path, TypeCrew)
		if m.Class == RosterSupervised && !m.RestartOnCrash {
			m.LifecycleWarning = CoupledFlagWarning
		}

		switch a := r.Get(c.name); {
		case a != nil:
			m.State = RosterPresent
			m.Status = a.Status
			m.Alive = a.Alive()
			rep.Present++
		case IsParked(c.name):
			m.State = RosterParked
			rep.Parked++
		default:
			m.State = RosterAbsent
		}

		rep.Configured++
		rep.Members = append(rep.Members, m)
		if m.Absent() {
			rep.Absent = append(rep.Absent, m)
		}
		if m.LifecycleWarning != "" {
			rep.Coupled = append(rep.Coupled, m)
		}
	}
	sort.Slice(rep.Members, func(i, j int) bool { return rep.Members[i].Name < rep.Members[j].Name })
	sort.Slice(rep.Absent, func(i, j int) bool { return rep.Absent[i].Name < rep.Absent[j].Name })
	sort.Slice(rep.Coupled, func(i, j int) bool { return rep.Coupled[i].Name < rep.Coupled[j].Name })
	return rep, nil
}

// handleRoster serves GET /agents/roster.
//
// It is a SEPARATE endpoint rather than extra rows on GET /agents on purpose.
// Eight callers consume that array — `pogo gc`, checkturns, checkorphans,
// checkstranded among them — and every one of them assumes a listed agent has a
// process behind it. Synthesising absent rows into it would have made this
// ticket's fix a change to the meaning of a contract those readers already rely
// on. The absence is new information and gets its own surface; `pogo agent list`
// then reads BOTH and prints the absences as a footer, which is the smallest
// change that makes the roster complete where a person actually looks.
func (r *Registry) handleRoster(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	rep, err := r.RosterReport()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rep)
}

// crewIdentity builds the event-log identity for a configured crew/mayor agent.
// It mirrors Agent.eventAgent's crew branch; an absent agent has no *Agent to
// ask, which is the entire problem this file addresses.
func crewIdentity(name string) string {
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "crew-") {
		return name
	}
	return "crew-" + name
}

// Render formats the roster for a terminal, absent members first.
//
// Every branch reports the DENOMINATOR. A roster reader's mistake is not
// misreading a row, it is not knowing how many rows there should have been —
// the same defect drellem2/pogo#127 reported against MailLoopReport.Render, and
// the same fix: one renderer, no green-only shortcut.
func (rep RosterReport) Render() string {
	var b strings.Builder
	if rep.Configured == 0 {
		b.WriteString("NOTHING CONFIGURED: no crew or mayor prompt on this machine.\n")
		b.WriteString("This is not a complete roster — there was nothing to compare the\n")
		b.WriteString("registry against. Check the prompt tree before reading it as clean.\n")
		return b.String()
	}
	if len(rep.Absent) == 0 {
		fmt.Fprintf(&b, "Roster complete: %d configured agent(s), %d running, %d parked.\n",
			rep.Configured, rep.Present, rep.Parked)
		b.WriteString(rep.renderCoupled())
		return b.String()
	}
	fmt.Fprintf(&b, "%d configured agent(s) are NOT in the registry — they are neither running\n"+
		"nor parked, so they appear in no roster this fleet prints:\n\n", len(rep.Absent))
	for _, m := range rep.Absent {
		fmt.Fprintf(&b, "  %-20s %s\n", m.Name, describeClass(m))
	}
	fmt.Fprintf(&b, "\n%d configured, %d running, %d parked, %d absent.\n",
		rep.Configured, rep.Present, rep.Parked, len(rep.Absent))
	b.WriteString(rep.renderCoupled())
	return b.String()
}

// renderCoupled appends the lifecycle-flag warnings. It is called from BOTH
// branches of Render on purpose: a coupled-flag agent is most often a RUNNING
// one, so hanging this off the absent branch would hide it in exactly the case
// where the fault has not landed yet and is still cheap to fix.
func (rep RosterReport) renderCoupled() string {
	if len(rep.Coupled) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%d agent(s) carry COUPLED lifecycle flags in the forbidden combination:\n",
		len(rep.Coupled))
	for _, m := range rep.Coupled {
		fmt.Fprintf(&b, "  %-20s %s (currently %s)\n", m.Name, m.LifecycleWarning, m.State)
	}
	return b.String()
}

// describeClass renders the one thing a reader needs in order to know whether an
// absence is a fault: what the agent's own frontmatter asked for.
func describeClass(m RosterMember) string {
	switch m.Class {
	case RosterSupervised:
		return "auto_start = true — pogod should have started this at boot and did not"
	case RosterOnDemand:
		return "auto_start = false — on-demand; nothing will bring it back"
	case RosterUnclassifiable:
		return "prompt unreadable (" + m.Error + ") — cannot say what was wanted"
	default:
		return string(m.Class)
	}
}
