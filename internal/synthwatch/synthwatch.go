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
//
// # Why the episode does not close the moment the transcripts go quiet (mg-70f3)
//
// Coalescing by agent was not enough: coalescing by AGENT still pages once per
// RECURRENCE, and this class recurs. Measured over seven days of
// `~/.pogo/reminders/deadman.log`: 45 open pages and 40 clear notices, six
// open/clear pairs in the five hours of 2026-08-10 07:26Z–12:22Z alone, and on
// 2026-08-14 a `cleared — 9 agent(s)` at 03:22:09Z followed by a re-alarm at
// 03:24:38Z — 2m29s later. Four cycles that night: 2m29s, 2m07s, 14m04s, 6m37s.
//
// The mechanism is that the episode closed on a QUIET reading, and quiet is what
// an intermittent fault looks like between recurrences — the same reading an
// idle agent produces. So the watcher held a fault of one shape (intermittent,
// hours long) and reported it as a series of short faults, each with its own
// page and its own all-clear. The clear was the more misleading half: it
// asserted recovery that the next page contradicted minutes later.
//
// So the CLOSE is damped and the OPEN is not. An episode enters a quiet hold
// (ClearHold, default 60m) instead of closing; a re-failure inside the hold
// resumes the SAME episode with no new page, and the clear mail — when it
// finally goes — states how many recurrences the hold absorbed, so damping the
// mail never becomes under-reporting the fault.
//
// # The bound: never "wait and see whether it clears before paging"
//
// That was proposed and ruled out on evidence (mg-c058). The 2026-08-14 fault
// was not transient — github.com intermittently unreachable from at least 01:18Z
// to 03:16Z over SSH/22 as well as HTTPS/DNS, on four independent instruments —
// and on 2026-07-22 a genuinely dead fleet went 23h30m unnoticed. Delaying the
// FIRST page would have delayed a real multi-hour outage, which is the whole
// case this channel exists for. Nothing here delays an episode-open page: the
// hold applies to the close, and the paging floor (MinPageInterval) has an
// unconditional escape when the reason changes.
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
	// EventEpisodeHeld is emitted when the class recurs during an episode's quiet
	// hold, so the episode is extended rather than closed-and-re-opened. Each one
	// is a page and a clear notice that were NOT sent — it is the measurement of
	// what the hysteresis absorbed, and the only way to answer "is this still
	// flapping" without re-deriving it from mail. Same reasoning as
	// EventRestartSuppressed: damping that happens silently is indistinguishable
	// from damping that never happened.
	EventEpisodeHeld = "synthetic_failure_episode_held"
	// EventPageSuppressed is emitted when the paging floor withheld an
	// episode-open page. Details carry how long since the last page and what the
	// floor is, so a suppression can always be checked against the bound.
	EventPageSuppressed = "synthetic_failure_page_suppressed"
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

// DefaultClearHold is how long every agent must read quiet, CONTINUOUSLY, before
// the episode closes and the clear mail goes out. A re-failure inside the hold
// resumes the same episode silently.
//
// # 60m is where the measured data breaks, not a round number
//
// Every clear→re-alarm gap in `~/.pogo/reminders/deadman.log` was extracted for
// mg-70f3 — 43 of them, across 93 mails — and taken from each mail's MAILDIR
// SEND STAMP, never from the log line's timestamp. The log records when the
// delivering daemon NOTICED a mail, and that lag was 16m26s on the anchor page
// of 2026-08-14; every gap computed from log lines is wrong by the difference of
// two such lags. In minutes, sorted:
//
//	0.5 2.5 2.5 2.8 3.5 5.0 5.0 5.2 5.4 6.5 6.7 6.9 7.0 7.0 8.0 8.8 9.5
//	10.0 10.0 10.0 10.1 10.2 10.4 11.0 12.8 15.2 16.5 16.5 | 31.5 |
//	42.6 50.5 51.5 54.0 58.3 60.5 | 106.5 134.1 383.6 414.7 1358.1 1623.6
//	2404.2 4276.3
//
// The distribution has one wide break, between 60.5m and 106.5m. Below it the
// gaps are one fault recurring; above it they are separate incidents, running out
// to three days. 60m is the largest value that stays inside that break, and it
// absorbs 34 of 43 (79%) against 28 of 43 (65%) for 30m — including the 31.5m
// gap of 2026-08-14 06:26:37Z→06:58:09Z, which a 30m hold would have missed by
// 92 seconds. It is also 2x synthfail.DefaultWindow, so the reading that closes
// an episode is taken over a trailing window whose whole span is inside the hold.
//
// The cost of the hold is bounded and one-sided: it delays the ALL-CLEAR, never
// the alarm. Restart suppression is lifted per agent on its own quiet reading
// and does not wait for the hold either.
const DefaultClearHold = 60 * time.Minute

// DefaultMinPageInterval is the floor between episode-open pages that carry the
// same reason — the backstop half of mg-70f3, independent of the episode
// machinery. With ClearHold >= MinPageInterval it can never bite, because two
// opens cannot be closer together than a full hold; it exists so the guarantee
// ("this alarm pages at most once per floor for one standing cause") holds even
// if the hold is shortened or disabled. It is deliberately left at 30m rather
// than tracking the hold, so that shortening the hold does not silently take the
// backstop with it.
//
// A reason CHANGE bypasses it unconditionally: rate_limit decaying into
// auth_failed is new information about a different fix, and the bound this whole
// item works under is that no true page is delayed.
const DefaultMinPageInterval = 30 * time.Minute

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
	// ClearHold is how long every agent must read quiet before the episode
	// closes. Zero means DefaultClearHold; NEGATIVE disables the hold, restoring
	// the close-on-first-quiet-reading behaviour that flapped.
	ClearHold time.Duration
	// MinPageInterval is the floor between episode-open pages carrying the same
	// reason. Zero means DefaultMinPageInterval; NEGATIVE disables the floor.
	MinPageInterval time.Duration
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
	// quietSince is when the LAST failing agent of an open episode went quiet —
	// the instant the quiet hold started running. Zero while any agent is
	// failing, and zero when no episode is open. openedAt is the marker for "an
	// episode is open"; quietSince is the marker for "it is draining".
	quietSince time.Time
	// recurrences counts re-entries into the class during the current episode's
	// quiet hold. Each one is a page/clear pair the hysteresis absorbed, and the
	// clear mail states the total — damping the mail must not under-report the
	// fault.
	recurrences int
	// suppressedPages counts episode-open pages the floor withheld during the
	// current episode.
	suppressedPages int
	// lastPageAt / lastPageReason are the paging floor's state. They deliberately
	// SURVIVE an episode close: the floor is a property of the channel, not of an
	// episode, and resetting it at close would make a flap that closes and
	// re-opens exempt from the very thing it needs.
	lastPageAt     time.Time
	lastPageReason synthfail.Reason
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
	// Zero means "use the default"; negative means "off". Normalising negatives
	// to 0 here keeps every comparison downstream a plain `elapsed >= hold`.
	if opts.ClearHold == 0 {
		opts.ClearHold = DefaultClearHold
	} else if opts.ClearHold < 0 {
		opts.ClearHold = 0
	}
	if opts.MinPageInterval == 0 {
		opts.MinPageInterval = DefaultMinPageInterval
	} else if opts.MinPageInterval < 0 {
		opts.MinPageInterval = 0
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
	// The hold expires on the clock, not on a reading, so it has to be evaluated
	// every tick — including ticks where no agent was scanned at all (every
	// target is inside its Interval) and ticks where the target set is empty
	// because the last failing agent departed.
	w.drain(now)
}

// observe folds one agent's verdict into the episode state, sending the
// episode-open page when this hit opens an episode.
func (w *Watcher) observe(t Target, rep synthfail.Report, now time.Time) {
	if rep.State == synthfail.StateFailing {
		w.mu.Lock()
		_, already := w.failing[t.Name]
		// An episode is open iff openedAt is set — NOT iff failing is non-empty.
		// During the quiet hold the failing map is empty while the episode is
		// still very much open, and reading emptiness as "new episode" here would
		// re-page on every recurrence: the exact defect (mg-70f3).
		newEpisode := w.openedAt.IsZero()
		// A recurrence inside the hold: the episode was draining and the class
		// came back. Cancel the drain, keep the episode, do not page.
		held := !newEpisode && !w.quietSince.IsZero()
		quietFor := time.Duration(0)
		if held {
			quietFor = now.Sub(w.quietSince)
			w.quietSince = time.Time{}
			w.recurrences++
		}
		recurrence := w.recurrences
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
		episodeID := w.episodeID
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
				// window_seconds is what makes failing_turns a reading rather
				// than a number: it is the trailing window the count was taken
				// over, and without it a later reader supplies their own (mg-c058).
				"window_seconds": rep.WindowSeconds,
				"detail":         rep.Detail,
				"remediation":    "page a human; restart is suppressed and cannot help",
			},
		})
		if held {
			// The episode was draining and the class came back. Before mg-70f3
			// this was a clear mail plus a fresh page — the flap. Record what was
			// absorbed, in the one place that can be counted later.
			w.opts.Emit(events.Event{
				EventType:  EventEpisodeHeld,
				Agent:      identityOr(t.Identity, t.Name),
				WorkItemID: t.WorkItemID,
				Details: map[string]any{
					"target":        t.Name,
					"reason":        string(rep.Reason),
					"episode_id":    episodeID,
					"quiet_seconds": int(quietFor / time.Second),
					"hold_seconds":  int(w.opts.ClearHold / time.Second),
					"recurrence":    recurrence,
					"why":           "the class recurred inside the episode's quiet hold; the episode was extended instead of closed and re-opened (mg-70f3)",
					"withheld":      "one clear mail and one re-open page",
				},
			})
		}
		if newEpisode {
			w.page(t, rep, now)
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

// clear removes one agent from the live episode. When it was the last one the
// episode does NOT close — it starts draining, and closes only if the whole
// ClearHold passes with nothing failing (see drain).
//
// The per-agent record is unchanged and immediate: EventCleared still fires the
// moment a transcript reads quiet, and restart suppression is still lifted for
// that agent right then. What the hold damps is the MAIL and the episode
// boundary, which are the parts that were flapping (mg-70f3). An observer who
// wants per-agent transitions has always had them in events.log and still does.
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
	// Last one out: start the quiet hold, unless one is already running (an
	// agent reaped after the drain began must not restart the clock — that
	// would let a departing fleet postpone the all-clear indefinitely).
	if w.quietSince.IsZero() {
		w.quietSince = now
	}
	w.mu.Unlock()
}

// drain closes the episode once the whole ClearHold has passed with no agent
// failing. It is a no-op when no episode is open, when one is open but something
// is still failing, and when the hold has not yet elapsed.
func (w *Watcher) drain(now time.Time) {
	w.mu.Lock()
	if w.openedAt.IsZero() || w.quietSince.IsZero() || len(w.failing) > 0 {
		w.mu.Unlock()
		return
	}
	if now.Sub(w.quietSince) < w.opts.ClearHold {
		w.mu.Unlock()
		return
	}
	// Episode closed: nothing has failed for a continuous ClearHold. Capture the
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
	quietSince := w.quietSince
	recurrences := w.recurrences
	suppressed := w.suppressedPages
	w.roster = map[string]Target{}
	w.episodeID = ""
	w.openedAt = time.Time{}
	w.quietSince = time.Time{}
	w.recurrences = 0
	w.suppressedPages = 0
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
	//
	// closed_at is the end of the HOLD, not the last quiet reading: the episode
	// really was open until then, and the notifier's coalescing window is safer
	// wide than narrow. The details KEY SET is fixed by the cross-repo contract —
	// do not add one here for the hold.
	w.opts.Emit(episodeClearedEvent(episodeID, identities, openedAt, now))
	w.sendMail(clearMail(names, quietSince, now, w.window(), w.opts.ClearHold, recurrences, suppressed))
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

// page sends the episode-open page, subject to the paging floor.
//
// The floor is the backstop half of mg-70f3 and it is deliberately narrow: it
// withholds a page only when an identical CAUSE paged less than MinPageInterval
// ago. A different reason pages immediately, however recently the last page
// went, because the bound this item works under is that no true page is delayed
// — the 2026-08-14 fault ran for five hours and a page held back on the
// assumption that it would settle would have been a page about a five-hour
// outage.
//
// Two consecutive identical opens — the 2026-08-10 20:17:18Z pair, minted 41.8s
// apart — are the degenerate case of this rule and are covered a fortiori: same
// agent and same reason means the same subject, and 30m is 43x the gap that
// pair needed.
func (w *Watcher) page(t Target, rep synthfail.Report, now time.Time) {
	subject, body := hitMail(t, rep, now, w.opts.ClearHold)

	w.mu.Lock()
	since := now.Sub(w.lastPageAt)
	floored := !w.lastPageAt.IsZero() &&
		since < w.opts.MinPageInterval &&
		rep.Reason == w.lastPageReason
	if floored {
		w.suppressedPages++
	} else {
		w.lastPageAt = now
		w.lastPageReason = rep.Reason
	}
	w.mu.Unlock()

	if floored {
		w.opts.Emit(events.Event{
			EventType:  EventPageSuppressed,
			Agent:      identityOr(t.Identity, t.Name),
			WorkItemID: t.WorkItemID,
			Details: map[string]any{
				"target":              t.Name,
				"reason":              string(rep.Reason),
				"subject":             subject,
				"since_last_page_sec": int(since / time.Second),
				"floor_sec":           int(w.opts.MinPageInterval / time.Second),
				"why":                 "an episode-open page for the same reason went out less than the paging floor ago (mg-70f3); a DIFFERENT reason is never floored",
			},
		})
		return
	}
	w.sendMail(subject, body)
}

// window is the trailing window the reports were counted over, for the mails
// that have to state it. It reads the resolved value the same way synthfail
// does, so the page can never claim a window the scan did not use.
func (w *Watcher) window() time.Duration {
	if w.opts.ScanOptions.Window > 0 {
		return w.opts.ScanOptions.Window
	}
	return synthfail.DefaultWindow
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
//
// # Why the subject states a count and a window rather than a rate
//
// It used to read `AGENTS ARE FAILING EVERY TURN — <agent> (<reason>)`. Sent at
// 02:28:12Z on 2026-08-14, it woke Daniel, and two of its three parts were
// false: the count was 2 in a 30-minute trailing window, not "every turn", and
// the named agent (mayor) was completing turns — it had itself run the query
// that found them. The third part, `server_error`, was true.
//
// So this page was NOT a false alarm. The fault was real and ongoing: an
// intermittent network fault, not the persistent per-agent cause the phrasing
// points a reader at. What was wrong was the scope and the attribution, and
// they sent the reader at the wrong subsystem for nine days via a credential
// ticket parked on `human` (mg-c058, mg-fb29). That is why nothing here delays
// or suppresses the page — only the words change.
//
// So the subject now carries what was measured: N errors, over how long, ending
// when. The founding 23h30m case renders as "143 errors in 30m,
// 2026-07-21T23:10:26Z–2026-07-22T12:00:00Z" — strictly more alarming than the
// old wording, and true.
//
// Every time in this mail is ABSOLUTE. A page is read minutes or hours after it
// is sent — the 2026-08-14 one was noticed by the delivering daemon 16m26s
// later — so a relative age ("last 14m ago") would be wrong by the time anyone
// saw it. That is the same defect one layer out, and it is why the send time is
// stated too: without it, a reader dates the page to when they read it.
func hitMail(t Target, rep synthfail.Report, sentAt time.Time, hold time.Duration) (subject, body string) {
	brief := rep.Brief()
	subject = fmt.Sprintf("AGENTS FAILING TURNS — %s (%s)", t.Name, rep.Reason)
	if brief != "" {
		subject += ": " + brief
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Sent %s. Every time below is UTC and absolute — date this page\n", sentAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "from that line, not from when you are reading it.\n\n")
	fmt.Fprintf(&b, "pogod read %s's session transcript and found it answering turns\n", t.Name)
	fmt.Fprintf(&b, "LOCALLY and failing them: %d zero-token failure turns between %s\n",
		rep.Count, rep.First.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "and %s.\n\n", rep.Last.UTC().Format(time.RFC3339))
	if w := rep.WindowString(); w != "" {
		fmt.Fprintf(&b, "THAT IS A COUNT OVER A TRAILING %s WINDOW, NOT A RATE. It does not say\n", strings.ToUpper(w))
		fmt.Fprintf(&b, "every turn failed, and it does not say %s is failing now — an agent with\n", t.Name)
		fmt.Fprintf(&b, "a handful of failures in this window can be completing turns throughout.\n")
		fmt.Fprintf(&b, "Nor is the window the size of the fault: it can be narrower at either end.\n\n")
	}
	fmt.Fprintf(&b, "Reason: %s — %s\n", rep.Reason, rep.Reason.Human())
	if rep.Reason == synthfail.ReasonServerError {
		// The mayor prompt's enumeration for this state names only persistent
		// causes, so a reader arrives expecting one. server_error is not.
		fmt.Fprintf(&b, "  NOTE: server_error is a PROVIDER/NETWORK fault, not a credential, a rate\n")
		fmt.Fprintf(&b, "  limit or a spend cap. It needs no action from you and it can be\n")
		fmt.Fprintf(&b, "  intermittent — recurring for hours while most turns succeed. Do not go\n")
		fmt.Fprintf(&b, "  looking for an expired credential on the strength of this page.\n")
	}
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
	// Why it is fleet-wide differs by reason, and saying "one shared
	// credential" under a server_error page contradicts the note above it —
	// which is the mistake this whole page is being corrected for.
	if rep.Reason == synthfail.ReasonServerError {
		fmt.Fprintf(&b, "This will look fleet-wide, because a network or provider fault IS\n")
		fmt.Fprintf(&b, "fleet-wide. That is not evidence of a shared credential; do not read it\n")
		fmt.Fprintf(&b, "as one (mg-c058).\n")
	} else {
		fmt.Fprintf(&b, "This class is characteristically fleet-wide (one shared credential).\n")
	}
	fmt.Fprintf(&b, "Other agents joining this episode are added silently; you will get ONE\n")
	fmt.Fprintf(&b, "follow-up mail naming all of them when it clears.\n\n")
	// A reader who knows the all-clear is held cannot mistake its absence for
	// "the fault is still live this minute", and cannot mistake its arrival for
	// a fault that only lasted as long as the gap between the two mails.
	if hold > 0 {
		fmt.Fprintf(&b, "THAT ALL-CLEAR IS HELD: it goes only after %s with nothing failing\n", compact(hold))
		fmt.Fprintf(&b, "anywhere, and it will say how many times the fault recurred in between.\n")
		fmt.Fprintf(&b, "So this page is NOT repeated per recurrence — silence from here is not\n")
		fmt.Fprintf(&b, "evidence the fault stopped, and the all-clear is not evidence it lasted\n")
		fmt.Fprintf(&b, "only until then (mg-70f3).\n\n")
	}
	fmt.Fprintf(&b, "Verify:  pogo agent diagnose %s --json   (health, transcript_check)\n", t.Name)
	fmt.Fprintf(&b, "See docs/operations.md → \"Agents that fail every turn\".\n")
	return subject, b.String()
}

// clearMail builds the episode-close page, naming every agent that was in the
// class so a human can confirm each one resumed.
//
// It states what was OBSERVED — no failing turns from any agent for a continuous
// hold — and not what a reader would like it to mean. "producing real turns
// again" was an over-claim: the close fires on QUIET transcripts, and a quiet
// transcript is equally consistent with an agent that has gone idle, and with a
// fault that is merely between recurrences. On 2026-08-14 this alarm announced a
// clear at 03:22Z and re-opened at 03:24Z, against a github.com reachability
// fault that ran intermittently from at least 01:18Z to 03:16Z; the clear was
// not wrong about its window, it was wrong about what its window proved
// (mg-c058).
//
// # Why the recurrence count is in the SUBJECT (mg-70f3)
//
// The hold means one mail can now stand for many recurrences, and a mail that
// said only "cleared" would have traded a flapping alarm for one that
// under-reports — the same defect pointed the other way. An intermittent
// five-hour fault must not read like a two-minute one just because pogod stopped
// sending a mail per recurrence. The count goes in the subject rather than the
// body because the subject is the part that travels: it is what the notifier
// surfaces and what a human skims.
func clearMail(roster []string, quietSince, when time.Time, window, hold time.Duration, recurrences, suppressedPages int) (subject, body string) {
	subject = fmt.Sprintf("turn failures cleared — %d agent(s), quiet %s", len(roster), compact(hold))
	if recurrences > 0 {
		subject += fmt.Sprintf(" after %d recurrence(s)", recurrences)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "As of %s, no agent has shown a failing turn since %s — a continuous %s\n",
		when.UTC().Format(time.RFC3339), quietSince.UTC().Format(time.RFC3339), compact(hold))
	fmt.Fprintf(&b, "with nothing failing, read over a trailing %s window.\n\n", compact(window))
	fmt.Fprintf(&b, "THAT IS WHAT WAS MEASURED, AND IT IS LESS THAN \"THE FAULT IS OVER\". A\n")
	fmt.Fprintf(&b, "quiet transcript is also what an idle agent writes, and an intermittent\n")
	fmt.Fprintf(&b, "fault is quiet between recurrences — a clean reading lands in a good\n")
	fmt.Fprintf(&b, "minute just as reliably as a clean probe does. If you need to establish\n")
	fmt.Fprintf(&b, "recovery, look for a PERIOD with no instrument failures anywhere (refinery\n")
	fmt.Fprintf(&b, "fetch retries, gh-intake, gh-teardown-watch), not one successful check.\n\n")
	if recurrences > 0 {
		fmt.Fprintf(&b, "THIS EPISODE RECURRED %d TIME(S) after reading quiet. Each recurrence was\n", recurrences)
		fmt.Fprintf(&b, "held inside this one episode rather than closed and re-opened, so it cost\n")
		fmt.Fprintf(&b, "you no page and no all-clear (mg-70f3). Read it as ONE intermittent fault\n")
		fmt.Fprintf(&b, "spanning the whole episode, not as %d short ones: the gaps between\n", recurrences+1)
		fmt.Fprintf(&b, "recurrences were quiet, and quiet is not absence. Every hold is in\n")
		fmt.Fprintf(&b, "~/.pogo/events.log as %s.\n\n", EventEpisodeHeld)
	}
	if suppressedPages > 0 {
		fmt.Fprintf(&b, "%d further open page(s) for the SAME reason were withheld by the paging\n", suppressedPages)
		fmt.Fprintf(&b, "floor and are in events.log as %s. A page for a\n", EventPageSuppressed)
		fmt.Fprintf(&b, "DIFFERENT reason is never withheld.\n\n")
	}
	fmt.Fprintf(&b, "%d agent(s) were in this class during the episode. Restart suppression\n", len(roster))
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

// compact renders a whole-minute duration as "30m" rather than Go's "30m0s",
// for the parts of a subject line a human skims. Anything not a whole number of
// minutes falls back to the exact String().
func compact(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return d.String()
}

func identityOr(identity, name string) string {
	if identity != "" {
		return identity
	}
	return name
}
