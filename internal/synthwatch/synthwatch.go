// Package synthwatch is pogod's pager for the synthetic-failure-turn class:
// the periodic scan that turns internal/synthfail from a thing you can ask into
// a thing that tells you.
//
// # Why a watcher and not just `pogo agent diagnose`
//
// Every member of this class leaves the agent ALIVE and RESPONSIVE. It exits
// nothing, crashes nothing, and wedges nothing — mg-18d0 measured six agents
// consuming 143 nudges each at their due second and failing every one in ~10ms,
// for 23h30m, while `scheduler_fire_delivered` logged 647 successful
// deliveries. Nothing in pogo's exit-driven or idle-driven machinery can fire
// on that, because from the outside the fleet looks busy. So the only way this
// gets noticed without a human happening to run diagnose is a watcher on an
// independent cadence — pogod's heartbeat — that reads the transcripts.
//
// # What it does on a hit
//
// It PAGES and it SUPPRESSES. It never restarts, and it holds no remediation of
// its own: no member of this class is fixable by restarting, and mg-18d0
// costed the alternative at ~66 restarts against a dead credential, each one
// discarding a live session's context (pm-pogo held 2339 messages) and
// destroying the transcripts the diagnosis rested on. See
// synthfail.Report.SuppressRestart.
//
// Pages are coalesced into EPISODES, following the usage-limit coordinator's
// precedent (gh #45): this class is characteristically fleet-wide — one expired
// credential is shared by every agent — so per-agent mail would turn one fact
// into an N-agent notification storm at the exact moment a human needs to read
// one clear thing. One mail when the episode opens, one when it closes, and
// agents joining a live episode are added to the roster silently.
package synthwatch

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/claude"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/synthfail"
)

// Event types emitted to ~/.pogo/events.log.
const (
	// EventDetected is emitted once per agent when it enters the failing state.
	EventDetected = "synthetic_failure_detected"
	// EventCleared is emitted once per agent when it leaves the failing state.
	EventCleared = "synthetic_failure_cleared"
	// EventRestartSuppressed is emitted when a restart was withheld because the
	// agent is in this class. It is the audit trail for the suppression half of
	// the fix — a suppression that only ever happened silently would be
	// indistinguishable from a suppression that never happened.
	EventRestartSuppressed = "synthetic_failure_restart_suppressed"
)

// AuthEpisodeKind is the details.kind value this watcher stamps on the generic
// claude.IncidentEpisodeClearedEvent it emits at every episode close. The
// synthetic-failure-turn class is dominated by, and named for, the expired-auth
// case (mg-18d0/mg-ed45), so "auth" is this source's discriminator on the shared
// incident_episode_cleared type. It is the AUTH half of the notification arc: the
// pogo-reminders notifier (mg-e0f6) binds one event type and coalesces every
// incident class by kind, so this stamp lets it group the fleet's auth
// self-reports without any reader-side change (mg-55b2 contract). Minting a new
// KIND value is expected; minting a new EVENT TYPE is not — reuse the const.
const AuthEpisodeKind = "auth"

// DefaultInterval is the minimum gap between scans of one agent. The fleet's
// nudge cadence is */10, so a failing agent produces a fresh turn every ten
// minutes; scanning every five means a hit is noticed within one nudge cycle
// without re-reading transcripts on every ~30s heartbeat tick.
const DefaultInterval = 5 * time.Minute

// mailFrom / mailTo follow driftwatch: the detector mails, a human acts. `human`
// is the identity the apple-side notifier surfaces; the mayor's inbox is for
// coordination and, in the fleet-wide case, the mayor is one of the casualties.
const (
	mailFrom = "pogod"
	mailTo   = "human"
)

// Target is one agent to scan. pogod builds these from its registry.
type Target struct {
	// Name is the bare agent name (`pogo agent diagnose <name>`).
	Name string
	// Identity is the event-log identity ("crew-mayor" / "cat-8cdb").
	Identity string
	// Workdir is the agent process's working directory, from which the
	// harness's transcript path is derived.
	Workdir string
	// WorkItemID is the polecat's work item, for the page. Optional.
	WorkItemID string
}

// MailFunc sends operator mail. pogod wires client.SendMGMail.
type MailFunc func(to, from, subject, body string) error

// Options carries the watcher's dependencies so the package is testable with no
// filesystem or daemon of its own.
type Options struct {
	// Home is the root the provider-declared globs are joined under. Empty
	// means os.UserHomeDir at call time via the Globs func's own resolution;
	// pogod passes it explicitly.
	Home string
	// Targets enumerates the agents to scan. Required.
	Targets func() []Target
	// Globs returns the home-relative transcript globs for a workdir. pogod
	// wires providers.SessionTranscriptGlobs. Required.
	Globs func(workdir string) []string
	// Mail sends the page. nil disables paging (the scan still records state
	// and still suppresses restarts).
	Mail MailFunc
	// Emit writes events. Defaults to events.Emit.
	Emit func(events.Event)
	// Scan overrides the reader. Defaults to synthfail.Scan; tests substitute.
	Scan func(home string, globs []string, opts synthfail.Options) synthfail.Report
	// Interval is the minimum gap between scans of one agent. Zero means
	// DefaultInterval.
	Interval time.Duration
	// ScanOptions tunes the reader (window, threshold). Zero means defaults.
	ScanOptions synthfail.Options
}

// Watcher scans agent transcripts on pogod's heartbeat and pages on the
// synthetic-failure-turn class.
type Watcher struct {
	opts Options

	mu sync.Mutex
	// failing holds the current verdict for every agent currently in the class
	// — the live episode roster.
	failing map[string]synthfail.Report
	// roster accumulates every agent (keyed by bare name) that was failing during
	// the open episode, so the clear mail can name them all AND the episode-close
	// incident event can carry their event-log identities.
	roster map[string]Target
	// episodeID is a stable per-episode id, stamped from the first agent to open
	// the episode; openedAt is its open time. Both are captured at close into the
	// incident_episode_cleared event's window (the roster+window the notifier
	// coalesces on) and reset when the episode closes. This is the coordinator
	// state usagelimit.go holds; without it the emit would have to reconstruct the
	// window from per-agent atoms — the reconstruction mg-e0f6 warned against.
	episodeID string
	openedAt  time.Time
	// lastScan rate-limits per-agent scans to Interval.
	lastScan map[string]time.Time
}

// New builds a Watcher. It is inert (Check is a no-op) without Targets and
// Globs, so a daemon that could not wire them degrades to pre-detector
// behaviour rather than panicking.
func New(opts Options) *Watcher {
	if opts.Emit == nil {
		opts.Emit = func(e events.Event) { events.Emit(context.Background(), e) }
	}
	if opts.Scan == nil {
		opts.Scan = synthfail.Scan
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	return &Watcher{
		opts:     opts,
		failing:  map[string]synthfail.Report{},
		roster:   map[string]Target{},
		lastScan: map[string]time.Time{},
	}
}

// Report returns the last known verdict for an agent and whether one exists.
// It is the read side used to suppress a restart without re-reading the
// transcript at exit time.
func (w *Watcher) Report(name string) (synthfail.Report, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.failing[name]
	return r, ok
}

// SuppressRestart reports whether restart-based remediation must be withheld
// for the named agent, and emits the audit event when it withholds one.
//
// It answers from the last completed scan. An agent never scanned, or scanned
// to StateUnavailable, is NOT suppressed — absence of evidence must leave
// today's recovery behaviour intact.
func (w *Watcher) SuppressRestart(name, identity string) bool {
	w.mu.Lock()
	rep, ok := w.failing[name]
	w.mu.Unlock()
	if !ok || !rep.SuppressRestart() {
		return false
	}
	w.opts.Emit(events.Event{
		EventType: EventRestartSuppressed,
		Agent:     identityOr(identity, name),
		Details: map[string]any{
			"target":            name,
			"reason":            string(rep.Reason),
			"failing_turns":     rep.Count,
			"detail":            rep.Detail,
			"suppressed_action": "respawn",
			"why":               "a restart cannot fix a synthetic zero-token failure turn; it discards the session's context and recovers nothing (mg-18d0)",
		},
	})
	return true
}

// Check runs one scan pass. It is the heartbeat OnTick integration point and is
// a no-op when the watcher was built without Targets or Globs.
func (w *Watcher) Check(now time.Time) {
	if w.opts.Targets == nil || w.opts.Globs == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}

	for _, t := range w.opts.Targets() {
		if t.Name == "" {
			continue
		}
		w.mu.Lock()
		last, seen := w.lastScan[t.Name]
		if seen && now.Sub(last) < w.opts.Interval {
			w.mu.Unlock()
			continue
		}
		w.lastScan[t.Name] = now
		w.mu.Unlock()

		scanOpts := w.opts.ScanOptions
		scanOpts.Now = now
		rep := w.opts.Scan(w.opts.Home, w.opts.Globs(t.Workdir), scanOpts)
		w.observe(t, rep, now)
	}

	w.reapMissing(now)
}

// observe folds one agent's verdict into the episode state, sending the
// episode-open page when this hit opens an episode.
func (w *Watcher) observe(t Target, rep synthfail.Report, now time.Time) {
	if rep.State == synthfail.StateFailing {
		w.mu.Lock()
		_, already := w.failing[t.Name]
		newEpisode := len(w.failing) == 0
		w.failing[t.Name] = rep
		w.roster[t.Name] = t
		if newEpisode {
			// Stamp the episode window from the first agent to open it, exactly as
			// usagelimit's OnHit does. (firstAgent, openedAt) is unique because
			// episodes are sequential — a new one opens only after the prior fully
			// closed — so this id is stable and deterministic under the test clock.
			w.episodeID = makeEpisodeID(identityOr(t.Identity, t.Name), now)
			w.openedAt = now
		}
		w.mu.Unlock()

		if already {
			// Still failing. Re-recording the verdict keeps the reason fresh
			// (a rate limit can decay into an auth failure) without re-paging:
			// 124 identical fires is a detector with no escalation path, and
			// mg-18d0 named that as its own defect.
			return
		}
		w.opts.Emit(events.Event{
			EventType:  EventDetected,
			Agent:      identityOr(t.Identity, t.Name),
			WorkItemID: t.WorkItemID,
			Details: map[string]any{
				"target":        t.Name,
				"reason":        string(rep.Reason),
				"failing_turns": rep.Count,
				"first":         rep.First.UTC().Format(time.RFC3339),
				"last":          rep.Last.UTC().Format(time.RFC3339),
				"detail":        rep.Detail,
				"remediation":   "page a human; restart is suppressed and cannot help",
			},
		})
		if newEpisode {
			w.page(t, rep)
		}
		return
	}

	// Not failing. Only a POSITIVE reading clears an agent: StateUnavailable
	// means we could not look, and treating "could not look" as recovery is the
	// absence-as-evidence error that let the original incident run for a day.
	if rep.State == synthfail.StateQuiet {
		w.clear(t.Name, now)
	}
}

// reapMissing clears agents that have left the target set entirely (stopped,
// unregistered). An agent pogod no longer runs cannot still be failing, and
// leaving it on the roster would hold an episode open forever.
func (w *Watcher) reapMissing(now time.Time) {
	live := map[string]bool{}
	for _, t := range w.opts.Targets() {
		live[t.Name] = true
	}
	w.mu.Lock()
	var gone []string
	for name := range w.failing {
		if !live[name] {
			gone = append(gone, name)
		}
	}
	w.mu.Unlock()
	sort.Strings(gone)
	for _, name := range gone {
		w.clear(name, now)
	}
}

// clear removes one agent from the live episode, sending the episode-close page
// when it was the last one.
func (w *Watcher) clear(name string, now time.Time) {
	w.mu.Lock()
	if _, ok := w.failing[name]; !ok {
		w.mu.Unlock()
		return
	}
	delete(w.failing, name)
	w.opts.Emit(events.Event{
		EventType: EventCleared,
		Agent:     name,
		Details:   map[string]any{"target": name},
	})
	if len(w.failing) > 0 {
		w.mu.Unlock()
		return
	}
	// Episode closed: the last failing agent recovered or departed. Capture the
	// roster and window under the lock — this is the ONE close point where they
	// are already in hand (the mg-e0f6 bound: emit from the coordinator's real
	// close, never reconstruct the window from per-agent atoms). names feed the
	// clear mail (bare, for `pogo agent diagnose`); identities feed the incident
	// event (event-log identity, the shape the notifier matches senders against).
	names := make([]string, 0, len(w.roster))
	identities := make([]string, 0, len(w.roster))
	for n, t := range w.roster {
		names = append(names, n)
		identities = append(identities, identityOr(t.Identity, t.Name))
	}
	episodeID := w.episodeID
	openedAt := w.openedAt
	w.roster = map[string]Target{}
	w.episodeID = ""
	w.openedAt = time.Time{}
	w.mu.Unlock()

	sort.Strings(names)
	// The generic incident_episode_cleared event (mg-55b2 contract), emitted at
	// EVERY auth-episode close. It carries the structured roster+window the
	// pogo-reminders notifier (mg-e0f6) coalesces on, so the fleet's auth
	// self-reports collapse to ONE notification instead of swarming — the exact
	// 2026-07-22 founding case. Same event TYPE and details SHAPE as
	// usagelimit.go's emitter; only details.kind differs ("auth"). Emitted after
	// the per-agent EventCleared and after the lock is dropped, mirroring the
	// usage-limit coordinator.
	w.opts.Emit(episodeClearedEvent(episodeID, identities, openedAt, now))
	w.sendMail(clearMail(names, now))
}

// makeEpisodeID builds a stable per-episode id from the opening agent and the
// episode's open time, byte-identical in shape to usagelimit.go's. Episodes are
// sequential, so (firstAgent, openedAt) is unique; deriving it from the injected
// clock keeps it deterministic under test.
func makeEpisodeID(firstAgent string, openedAt time.Time) string {
	return fmt.Sprintf("ep-%d-%s", openedAt.UTC().UnixNano(), firstAgent)
}

// episodeClearedEvent builds the structured incident_episode_cleared event for an
// auth-episode close. It mirrors usagelimit.go's episodeClearedEvent exactly —
// same event type (claude.IncidentEpisodeClearedEvent, reused, not re-minted),
// same Agent ("pogod"), same RFC3339Nano timestamps, same details field names and
// nesting — changing only details.kind to AuthEpisodeKind. The roster is emitted
// sorted so the on-disk record is deterministic. Do not diverge this shape from
// usagelimit.go's or from mg-e0f6's reader without updating both.
func episodeClearedEvent(episodeID string, roster []string, openedAt, closedAt time.Time) events.Event {
	ids := append([]string(nil), roster...)
	sort.Strings(ids)
	return events.Event{
		EventType: claude.IncidentEpisodeClearedEvent,
		Agent:     "pogod",
		Timestamp: closedAt.UTC().Format(time.RFC3339Nano),
		Details: map[string]any{
			"kind":       AuthEpisodeKind,
			"episode_id": episodeID,
			"roster":     ids,
			"opened_at":  openedAt.UTC().Format(time.RFC3339Nano),
			"closed_at":  closedAt.UTC().Format(time.RFC3339Nano),
		},
	}
}

func (w *Watcher) page(t Target, rep synthfail.Report) {
	w.sendMail(hitMail(t, rep))
}

func (w *Watcher) sendMail(subject, body string) {
	if w.opts.Mail == nil {
		return
	}
	if err := w.opts.Mail(mailTo, mailFrom, subject, body); err != nil {
		log.Printf("synthwatch: failed to page %s: %v", mailTo, err)
	}
}

// hitMail builds the episode-open page. It leads with the fact that this is not
// a wedge and not restartable, because the operator's first instinct — and the
// mayor's documented 120-minute rule — is to restart.
func hitMail(t Target, rep synthfail.Report) (subject, body string) {
	subject = fmt.Sprintf("AGENTS ARE FAILING EVERY TURN — %s (%s)", t.Name, rep.Reason)

	var b strings.Builder
	fmt.Fprintf(&b, "pogod read %s's session transcript and found it answering turns\n", t.Name)
	fmt.Fprintf(&b, "LOCALLY and failing them: %d zero-token failure turns between %s\n",
		rep.Count, rep.First.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "and %s.\n\n", rep.Last.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Reason: %s — %s\n", rep.Reason, rep.Reason.Human())
	if rep.Detail != "" {
		fmt.Fprintf(&b, "Harness said: %q\n", rep.Detail)
	}
	if t.WorkItemID != "" {
		fmt.Fprintf(&b, "Work item: %s\n", t.WorkItemID)
	}
	fmt.Fprintf(&b, "\nWHAT THIS IS NOT: it is not a wedge. The agent is alive and consuming\n")
	fmt.Fprintf(&b, "every nudge on time — it just accomplishes nothing with them. Delivery\n")
	fmt.Fprintf(&b, "counters (nudge_sent, scheduler_fire_delivered) will look perfectly\n")
	fmt.Fprintf(&b, "healthy throughout, which is how this went unnoticed for 23h30m on\n")
	fmt.Fprintf(&b, "2026-07-22 (mg-18d0).\n\n")
	fmt.Fprintf(&b, "DO NOT RESTART. A new session inherits the same credential, the same\n")
	fmt.Fprintf(&b, "limit, the same cap — and the restart discards the live session's whole\n")
	fmt.Fprintf(&b, "context. pogod has already suppressed restart-based remediation for\n")
	fmt.Fprintf(&b, "affected agents; do not work around it.\n\n")
	fmt.Fprintf(&b, "This class is characteristically fleet-wide (one shared credential).\n")
	fmt.Fprintf(&b, "Other agents joining this episode are added silently; you will get ONE\n")
	fmt.Fprintf(&b, "follow-up mail naming all of them when it clears.\n\n")
	fmt.Fprintf(&b, "Verify:  pogo agent diagnose %s --json   (health, transcript_check)\n", t.Name)
	fmt.Fprintf(&b, "See docs/operations.md → \"Agents that fail every turn\".\n")
	return subject, b.String()
}

// clearMail builds the episode-close page, naming every agent that was in the
// class so a human can confirm each one resumed.
func clearMail(roster []string, when time.Time) (subject, body string) {
	subject = fmt.Sprintf("turn failures cleared — %d agent(s) producing real turns again", len(roster))

	var b strings.Builder
	fmt.Fprintf(&b, "The synthetic-failure-turn episode cleared as of %s.\n\n", when.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "%d agent(s) were failing every turn during this episode. Each now shows\n", len(roster))
	fmt.Fprintf(&b, "real model turns in its transcript, or has stopped. Restart suppression\n")
	fmt.Fprintf(&b, "is lifted. Confirm each resumed real work — the nudges consumed during\n")
	fmt.Fprintf(&b, "the episode were destroyed, not queued, so the scheduled work of that\n")
	fmt.Fprintf(&b, "window is GONE rather than late (mg-18d0):\n\n")
	for _, name := range roster {
		fmt.Fprintf(&b, "- %s\n", name)
		fmt.Fprintf(&b, "    verify: pogo agent diagnose %s\n", name)
		fmt.Fprintf(&b, "    if idle: pogo nudge %s \"turn failures cleared — resume your task\"\n", name)
	}
	fmt.Fprintf(&b, "\nSee docs/operations.md → \"Agents that fail every turn\".\n")
	return subject, b.String()
}

func identityOr(identity, name string) string {
	if identity != "" {
		return identity
	}
	return name
}
