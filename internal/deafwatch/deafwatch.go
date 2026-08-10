// Package deafwatch ANNOUNCES an agent that can be mailed but never woken.
//
// # The gap this closes
//
// mg-de08 taught `pogo agent diagnose` to notice that an agent pogod expects to
// be running has no `mail-check-<name>` schedule, and to call that RED
// (health="no_mail_loop"). mg-738f widened the population to the deaf survivor:
// an `auto_start=false` agent that is CONFIGURED and actually RUNNING with its
// mail loop dead underneath it. Both were correct. Neither was loud.
//
// mg-738f's own closing section said so, and shipped anyway:
//
//	"Loud" means observable from OUTSIDE the thing that failed; a diagnose
//	field only helps someone already running diagnose. The only consumer of
//	MailCheckMissing is `pogo agent diagnose <name>` — an operator who already
//	suspects this agent. This fix makes the fault detectable, not announced.
//
// A CLI subcommand that takes the agent's NAME as an argument is the wrong
// shape for this fault, because not knowing which name to type IS the fault.
// An agent with no mail loop is unreachable by every coordination path the
// fleet has; nothing it fails to do will look like anything, because it is
// alive, its process is healthy, and its PTY is still producing output. The
// only thing that can report it is something outside it, on a clock. That is
// this package.
//
// # Why not ackwatch
//
// internal/ackwatch is the closest existing detector and it is genuinely
// adjacent: it reads the scheduler's ack counters, compares a schedule to its
// same-kind peers plus an absolute floor, and mails. But it reads SCHEDULES
// THAT EXIST. An agent with no mail-check schedule at all has no counter row to
// compare, contributes to no cohort, and disappears from that detector
// silently. ackwatch catches "registered and not completing"; this catches
// "never registered, or reaped". Different faults, disjoint detectors.
//
// # Not crying wolf is the whole design
//
// mg-738f's reasoning is binding here: "a wrong 'yes' costs a false RED, and a
// health signal that cries wolf gets ignored — which is how the fleet ends up
// back where mg-de08 started." Three things keep this quiet:
//
//   - The judgement is not re-derived. The source is
//     agent.Registry.MailLoopReport, which runs diagnose's OWN mailLoopFor over
//     the registry. Every exclusion mailLoopJudgeable draws — polecats, a
//     configured agent that is not running, an agent that is not configured at
//     all, an agent whose prompt tree cannot be READ (a separate reason since
//     mg-7b3f, because "I could not look" is not the same exclusion as "nothing
//     is owed here") — is inherited rather than reimplemented, so it cannot
//     drift and this package cannot invent a RED that diagnose would not
//     report.
//   - A HOLD-DOWN. A missing loop must persist unbroken for HoldDown before it
//     is a finding. Registration is not instantaneous: pogod spawns an agent and
//     registers its mail-check moments later, and every restart re-runs that
//     race for the whole fleet. mg-4904 shipped the same mechanism for the same
//     reason on usage-limit hits — a sub-second flap must page nobody.
//   - "Could not look" is an error, never a clean scan. A nil provider or a
//     failing source emits EventError and evaluates nothing, because a blind
//     detector that renders as a quiet one is this lineage's founding bug.
//
// # Announcing to an agent that cannot hear
//
// One routing rule is specific to this detector and does not appear in any of
// its siblings: the default recipient is the mayor, and THE MAYOR CAN BE THE
// PATIENT. Mailing a deaf agent about its own deafness is not a degraded
// alert, it is provably no alert at all — the mail lands in a mailbox nothing
// will ever wake the agent to read. So a finding that names the notify mailbox
// escalates to EscalateTo IMMEDIATELY, not after EscalateAfter. See
// escalateNow.
//
// # Report-only
//
// It mails and it emits. It never registers a schedule, nudges, or restarts.
// Re-registering a mail-check on an agent's behalf would paper over the reason
// the loop vanished — a reap, a failed registration, a manual `pogo schedule
// rm` — and this fleet has been burned by remediation that hides its own cause.
package deafwatch

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Default cadences for the standing runner.
const (
	// DefaultInterval is how often the runner samples. Finer than ackwatch's 30
	// minutes because the condition here is a BOOLEAN state rather than a rate
	// over hundreds of fires: there is no averaging to wait for, and the
	// hold-down — not the sampling interval — is what keeps it quiet.
	DefaultInterval = 5 * time.Minute
	// DefaultHoldDown is how long a missing mail loop must persist, unbroken,
	// before it becomes a finding. Comfortably longer than the spawn ->
	// registration gap and than a redeploy's re-registration sweep, and short
	// enough that a genuinely deaf agent is announced inside a coffee break
	// rather than a working day.
	DefaultHoldDown = 15 * time.Minute
	// DefaultRenotifyAfter is how long an UNCHANGED roster stays quiet before
	// it is raised again.
	DefaultRenotifyAfter = 6 * time.Hour
	// DefaultEscalateAfter is how long a finding may persist, unbroken, before
	// the notice ALSO goes to EscalateTo.
	DefaultEscalateAfter = 24 * time.Hour
)

const (
	mailFrom = "deaf-watch"
	// DefaultNotifyTo is the mayor: re-registering a mail-check and deciding
	// whether the agent also needs a restart is coordination work, not a
	// judgement only a person can make. See escalateNow for what happens when
	// the mayor is itself the finding.
	DefaultNotifyTo = "mayor"
	// DefaultEscalateTo receives a finding the fleet has not cleared, and any
	// finding that names the notify mailbox itself.
	DefaultEscalateTo = "human"
)

// Event types this package writes to the shared log.
const (
	// EventFired records a mailed announcement.
	EventFired = "deaf_watch_fired"
	// EventPending records an agent entering the hold-down window: observed
	// with no mail loop, not yet old enough to announce. Emitted once per
	// entry, so the event log distinguishes "we saw it and waited" from "we
	// never saw it" — the two look identical from the mail alone.
	EventPending = "deaf_watch_pending"
	// EventError records a sample that could not be taken. A detector that
	// cannot read its source has NOT found a clean fleet.
	EventError = "deaf_watch_error"
)

// EpisodeKind is this detector's value for details.kind on the generic
// incident_episode_cleared event (the mg-55b2 contract). Sharing that event
// type is what lets the pogo-reminders notifier (mg-e0f6) coalesce a fleet-wide
// clear into ONE notification instead of a swarm — the reason mg-55b2 made the
// event generic in the first place.
const EpisodeKind = "deaf_agent"

// IncidentEpisodeClearedEvent is the generic episode-close event type, spelled
// here rather than imported from internal/claude so this package does not take
// a dependency on the harness layer for one string constant. It must stay
// byte-identical to claude.IncidentEpisodeClearedEvent; TestEpisodeKindMatchesContract
// pins that.
const IncidentEpisodeClearedEvent = "incident_episode_cleared"

// Finding is one agent judged to have no mail-delivery path. It mirrors
// agent.MailLoopFinding without importing it, so this package stays a pure
// detector over a plain struct and its tests build fixtures by hand — the same
// separation internal/ackwatch draws in source.go, and for the same reason
// (mg-6092, mg-e8e7 and mg-5336 are three tickets for tests that read the
// developer's live ~/.pogo).
type Finding struct {
	// Name is the bare agent name — what an operator types after
	// `pogo agent diagnose`.
	Name string `json:"name"`
	// Identity is the event-log identity ("crew-<name>" / "cat-<name>"), the
	// shape the notifier matches senders against.
	Identity string `json:"identity"`
	// Type is the agent type as a string ("crew", "polecat").
	Type string `json:"type"`
	// Alive is observed liveness, not a status field.
	Alive bool `json:"alive"`
}

func (f Finding) identity() string {
	if f.Identity != "" {
		return f.Identity
	}
	return f.Name
}

// Snapshot is one reading of the fleet's mail-delivery paths.
type Snapshot struct {
	// Now is the sample time.
	Now time.Time
	// Scanned is how many agents the registry held.
	Scanned int
	// Judged is how many of them diagnose had standing to judge. Zero means
	// nothing was evaluated, which is NOT a clean fleet — the runner reports it
	// in every event's details so a reader can tell the two apart.
	Judged int
	// Missing is the RED set: judged, and with no mail-check schedule.
	Missing []Finding
}

// SourceFunc yields one reading. Production binds a closure over the live agent
// registry; tests substitute a fixture.
//
// It returns an error rather than an empty Snapshot when the fleet cannot be
// judged (no mail-check provider installed, registry unavailable), because
// "nothing is deaf" and "I could not look" must never collapse into the same
// quiet result.
type SourceFunc func(now time.Time) (Snapshot, error)

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. As in internal/ackwatch it is the ONLY side-effect channel this
// package has: there is deliberately no seam through which the watcher could
// register a schedule, nudge, or restart the agent it names.
type MailFunc func(to, from, subject, body string) error

// renderFindings formats the roster for a mail body, newest information first:
// what is wrong, then what to type.
func renderFindings(findings []Finding, since map[string]time.Time, now time.Time) string {
	var b strings.Builder
	for _, f := range findings {
		age := ""
		if at, ok := since[f.Name]; ok && !at.IsZero() {
			age = fmt.Sprintf(", deaf for %s", now.Sub(at).Round(time.Minute))
		}
		live := "not running"
		if f.Alive {
			live = "ALIVE"
		}
		fmt.Fprintf(&b, "  %s (%s, %s%s)\n", f.Name, f.Type, live, age)
	}
	return b.String()
}

// mailSubject names the roster in the subject line so the notification is
// actionable without opening it — the agent name IS the missing argument that
// made this fault undiagnosable.
func mailSubject(findings []Finding) string {
	names := make([]string, 0, len(findings))
	for _, f := range findings {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if len(names) == 1 {
		return "agent " + names[0] + " has NO mail loop — it cannot be reached"
	}
	return fmt.Sprintf("%d agents have NO mail loop — they cannot be reached: %s",
		len(names), strings.Join(names, ", "))
}

// fingerprint identifies a roster by IDENTITY, not by age. An unchanged set
// stays quiet until RenotifyAfter; ages advance every tick, so fingerprinting
// them would mail every interval and get the sender filtered — which is how a
// detector becomes an alert nobody consumes (ackwatch, same reasoning).
func fingerprint(findings []Finding) string {
	names := make([]string, 0, len(findings))
	for _, f := range findings {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
