// Package absentwatch ANNOUNCES a configured agent that is not there.
//
// # The gap this closes
//
// Every standing detector this fleet has iterates the REGISTRY: `pogo agent
// list`, the stall watch, ackwatch's schedule cohorts, and internal/deafwatch
// via agent.Registry.MailLoopReport. The registry holds the agents pogod is
// running. So an agent that was stopped does not become a row with a bad value
// in it — it becomes no row at all, and no reader can tell "this agent is down"
// from "this agent was never configured on this machine".
//
// cmd/pogod's registryLiveness names the shape exactly, about a neighbouring
// population: "A detector's existence is not its coverage; the question is
// which set it iterates."
//
// mg-7d20 is the bill. `crew-doctor` was stopped on 2026-08-10T17:14:23Z as part
// of an auth-incident cleanup and stayed down 2 days 21 hours, until a person
// asked for it by hand. It is on-demand by design — auto_start = false and
// restart_on_crash = false, shipped deliberately by mg-b2cc for gh #18 so that
// `pogo agent stop doctor` STAYS stopped — so nothing was going to restart it.
// The defect is not that it was down. The defect is that nothing said so:
//
//   - the heartbeat stall-watch reads a sweep log's mtime, and a stopped agent
//     writes no sweep log; a file that stops being written is not
//     distinguishable from one nobody was watching;
//   - ackwatch measures completion against a schedule, and pogod had REMOVED the
//     mail-check at stop time (reason=agent_gone), so no schedule was left to
//     under-complete;
//   - deafwatch judges an agent's mail loop, and its population is the registry,
//     which this agent had left.
//
// Every instrument read GREEN over a fleet with its auditor missing. And the one
// surface that reports on the fleet's checks — `pogo doctor --check` — is read
// on a cadence by nobody but doctor, so the thing that would have reported
// doctor's absence WAS doctor. That circularity is why this detector is not a
// check inside doctor, and must never become one: the distinguishing question
// for any gap-filler is whether the check still reads green when the thing it
// names fails.
//
// # Why not extend deafwatch
//
// deafwatch is the closest sibling and shares this one's whole structure, but
// the two are disjoint by population and by fault:
//
//	deafwatch    — the agent is RUNNING and nothing can wake it.
//	absentwatch  — the agent is not running at all, and nothing says so.
//
// deafwatch's source is Registry.MailLoopReport, which explicitly declines to
// judge "a configured agent that is not running" — that exclusion is correct for
// it (a stopped agent is owed no mail loop) and is precisely the hole here.
// Widening deafwatch would have required breaking that exclusion and would have
// produced one detector answering two questions with one hold-down. This one
// iterates agent.Registry.RosterReport instead: the CONFIGURED set, with
// presence as a property of each member rather than the precondition for being
// looked at.
//
// # Class, because absence is not always a fault
//
// A detector that mails about an on-demand agent being off is a detector that
// gets filtered, and a filtered detector is worse than none — it is the reason
// nobody builds the one that would work. So a finding's hold-down comes from the
// agent's OWN declaration:
//
//   - SUPERVISED (auto_start = true): pogod brings this up at boot. Its absence
//     contradicts the machine's desired state and is wrong within minutes, so it
//     uses HoldDown (default 15m) — long enough to clear a restart's spawn gap.
//   - ON-DEMAND (auto_start = false): nothing brings this up but somebody asking.
//     Absence is normal for an afternoon and notable for a day, so it uses
//     DormantAfter (default 24h). Against mg-7d20's timeline that is a notice at
//     2026-08-11T17:14Z — 21 hours before the hand-restart actually happened.
//   - UNCLASSIFIABLE (the prompt exists and cannot be parsed): treated as
//     SUPERVISED. We know the agent was configured and we cannot read what was
//     wanted for it, and rounding an unknown toward the quieter answer is how
//     this lineage's founding bug is spelled.
//
// A PARKED agent is never a finding. Park is the supported way to be down: it is
// declared, it persists, and `pogo agent list` already shows it as
// status=parked. agent.RosterReport draws that line, not this package.
//
// # Report-only
//
// It mails and it emits. It never starts an agent. Starting an absent agent
// would paper over WHY it left — a requested stop, a crash with no respawn, an
// auto-start sweep that failed — and the reason is the part worth knowing. It is
// also the half of mg-7d20 that stands whatever doctor's lifecycle should be:
// this ticket deliberately did not change doctor's flags, because a supervised
// doctor that dies between bounces is invisible by the same three mechanisms.
package absentwatch

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Default cadences for the standing runner.
const (
	// DefaultInterval is how often the runner samples. It matches deafwatch's
	// 5 minutes for the same reason: the condition is a BOOLEAN state, not a
	// rate over hundreds of fires, so there is no averaging to wait for and the
	// hold-down rather than the sampling interval is what keeps it quiet.
	DefaultInterval = 5 * time.Minute
	// DefaultHoldDown is how long a SUPERVISED agent must be observed absent,
	// unbroken, before it is announced. It must comfortably clear the window in
	// which pogod's auto-start sweep is still working through the crew after a
	// bounce, and a restart_on_crash respawn's ~2s registry gap.
	DefaultHoldDown = 15 * time.Minute
	// DefaultDormantAfter is the same threshold for an ON-DEMAND agent, whose
	// absence is its ordinary state. A day is chosen against mg-7d20's own
	// timeline: doctor went down at 17:14Z and was restored by hand 2d21h
	// later, so a 24h threshold announces it once, on the second day, with the
	// remaining two days still ahead of the fleet.
	DefaultDormantAfter = 24 * time.Hour
	// DefaultRenotifyAfter is how long an UNCHANGED roster stays quiet before
	// it is raised again.
	DefaultRenotifyAfter = 12 * time.Hour
	// DefaultEscalateAfter is how long a finding may persist, unbroken, before
	// the notice ALSO goes to EscalateTo.
	DefaultEscalateAfter = 48 * time.Hour
)

const (
	mailFrom = "absent-watch"
	// DefaultNotifyTo is the mayor: deciding whether an absent agent should be
	// started, parked, or left alone is coordination work. See escalateNow for
	// what happens when the mayor is itself the finding — which for THIS
	// detector is not a degraded alert but no alert at all, since the recipient
	// is not merely deaf, it is not running.
	DefaultNotifyTo = "mayor"
	// DefaultEscalateTo receives a finding the fleet has not cleared, and any
	// finding that names the notify mailbox itself.
	DefaultEscalateTo = "human"
)

// Event types this package writes to the shared log.
const (
	// EventFired records a mailed announcement.
	EventFired = "absent_watch_fired"
	// EventPending records an agent entering its hold-down window: observed
	// absent, not yet long enough to announce. Emitted once per entry, so the
	// event log distinguishes "we saw it and waited" from "we never saw it" —
	// the two are identical from the mail alone.
	EventPending = "absent_watch_pending"
	// EventError records a sample that could not be taken: no registry, or a
	// prompt tree that could not be enumerated. A detector that cannot read its
	// source has NOT found a complete roster.
	EventError = "absent_watch_error"
)

// EpisodeKind is this detector's value for details.kind on the generic
// incident_episode_cleared event (the mg-55b2 contract), which is what lets the
// pogo-reminders notifier coalesce a fleet-wide clear into one notification
// instead of a swarm (mg-e0f6).
const EpisodeKind = "absent_agent"

// IncidentEpisodeClearedEvent is the generic episode-close event type, spelled
// here rather than imported from internal/claude so this package does not take a
// dependency on the harness layer for one string constant. It must stay
// byte-identical to claude.IncidentEpisodeClearedEvent;
// TestEpisodeKindMatchesContract pins that.
const IncidentEpisodeClearedEvent = "incident_episode_cleared"

// Class is the lifecycle an absent agent's own frontmatter declared. It decides
// how patient this detector is, and it is carried into the mail because it is
// the first thing a reader needs in order to know whether the absence is a fault
// or a Tuesday.
type Class string

const (
	// ClassSupervised is auto_start = true.
	ClassSupervised Class = "supervised"
	// ClassOnDemand is auto_start = false.
	ClassOnDemand Class = "on_demand"
	// ClassUnclassifiable is a prompt that exists and could not be read.
	ClassUnclassifiable Class = "unclassifiable"
)

// Finding is one configured agent that is not in the registry and is not
// parked. It mirrors agent.RosterMember without importing it, so this package
// stays a pure detector over a plain struct and its tests build fixtures by hand
// — the same separation internal/deafwatch and internal/ackwatch draw, and for
// the same reason (mg-6092, mg-e8e7 and mg-5336 are three tickets for tests that
// read the developer's live ~/.pogo).
type Finding struct {
	// Name is the bare agent name — what an operator types after
	// `pogo agent start`.
	Name string `json:"name"`
	// Identity is the event-log identity ("crew-<name>"), the shape the
	// notifier matches senders against.
	Identity string `json:"identity"`
	// Class is what the prompt asked for.
	Class Class `json:"class"`
	// RestartOnCrash is the resolved flag, carried because a reader deciding
	// what to do about the absence needs to know whether a start would stick.
	RestartOnCrash bool `json:"restart_on_crash"`
	// Reason is why the prompt could not be classified, set only for
	// ClassUnclassifiable.
	Reason string `json:"reason,omitempty"`
}

func (f Finding) identity() string {
	if f.Identity != "" {
		return f.Identity
	}
	return f.Name
}

// patience returns the hold-down this finding's class earns. An unclassifiable
// prompt gets the SUPERVISED (short) threshold deliberately: rounding an unknown
// toward the quieter answer is the mistake this whole lineage exists to stop.
func (f Finding) patience(holdDown, dormantAfter time.Duration) time.Duration {
	if f.Class == ClassOnDemand {
		return dormantAfter
	}
	return holdDown
}

// Snapshot is one reading of the configured roster against the running fleet.
type Snapshot struct {
	// Now is the sample time.
	Now time.Time
	// Configured is how many crew/mayor prompts this machine has — the
	// DENOMINATOR. Zero means nothing was compared, which is not a complete
	// roster; the runner carries it in every event's details so a reader can
	// tell an empty machine from a healthy one.
	Configured int
	// Present and Parked are the accounted-for members.
	Present int
	Parked  int
	// Absent is the candidate set: configured, unparked, not in the registry.
	// Whether any of them is old enough to announce is the Watcher's decision.
	Absent []Finding
}

// SourceFunc yields one reading. Production binds a closure over the live agent
// registry; tests substitute a fixture.
//
// It returns an error rather than an empty Snapshot when the roster cannot be
// computed (no registry, an unreadable prompt tree), because "everybody is here"
// and "I could not look" must never collapse into the same quiet result.
type SourceFunc func(now time.Time) (Snapshot, error)

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. As in internal/deafwatch it is the ONLY side-effect channel this
// package has: there is deliberately no seam through which the watcher could
// start the agent it names.
type MailFunc func(to, from, subject, body string) error

// renderFindings formats the roster for a mail body: who, what they asked for,
// and how long they have been gone.
func renderFindings(findings []Finding, since map[string]time.Time, now time.Time) string {
	var b strings.Builder
	for _, f := range findings {
		age := ""
		if at, ok := since[f.Name]; ok && !at.IsZero() {
			age = fmt.Sprintf(", absent for %s", now.Sub(at).Round(time.Minute))
		}
		fmt.Fprintf(&b, "  %s (%s%s)\n", f.Name, describeClass(f), age)
	}
	return b.String()
}

// describeClass renders the frontmatter's own intent, which is the difference
// between a fault and a state somebody chose.
func describeClass(f Finding) string {
	switch f.Class {
	case ClassSupervised:
		return "auto_start = true — pogod should be running this and is not"
	case ClassOnDemand:
		return "auto_start = false — on-demand; nothing will bring it back"
	case ClassUnclassifiable:
		if f.Reason != "" {
			return "prompt unreadable: " + f.Reason
		}
		return "prompt unreadable — cannot say what was wanted"
	default:
		return string(f.Class)
	}
}

// mailSubject names the roster in the subject line so the notification is
// actionable without opening it — the agent name IS the argument an operator
// cannot guess when the fault is that nothing mentions the agent.
func mailSubject(findings []Finding) string {
	names := names(findings)
	if len(names) == 1 {
		return "configured agent " + names[0] + " is NOT RUNNING and nothing else reports it"
	}
	return fmt.Sprintf("%d configured agents are NOT RUNNING and nothing else reports them: %s",
		len(names), strings.Join(names, ", "))
}

// fingerprint identifies a roster by IDENTITY, not by age. An unchanged set
// stays quiet until RenotifyAfter; ages advance every tick, so fingerprinting
// them would mail every interval and get the sender filtered — which is how a
// detector becomes an alert nobody consumes (ackwatch and deafwatch, same
// reasoning).
func fingerprint(findings []Finding) string {
	return strings.Join(names(findings), ",")
}

func names(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func identities(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.identity())
	}
	sort.Strings(out)
	return out
}
