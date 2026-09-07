// Package refusalwatch is pogod's alarm for the consecutive-refusal class:
// the periodic scan that turns internal/refusalstreak from a thing you can ask
// into a thing that tells somebody.
//
// # Why this is not another detector
//
// The fleet is not short of detection. On 2026-09-07 the wedge detector fired
// correctly in 14m30s, named all six agents and the exact cause, and then
// emitted sixteen more findings over 3h55m, EVERY ONE carrying
// "routed_to": "nobody". The outage ran 5h30m and ended when a human noticed a
// dead fleet. That is the shape this package exists against: a correct detection
// with no path to a person, at exactly the moment when the agents that would
// normally carry a message are the thing that has stopped.
//
// So the load-bearing part of this package is not [Watcher.Check]. It is
// [Deliver], the [Receipt] it returns, and the rule that an unconfirmed receipt
// is not a delivery. "Did it alarm?" and "did anyone learn?" are separate
// questions and only the second one matters.
//
// # What it does on a hit
//
// It ALARMS, out of band, to `human`. It never restarts and never nudges: no
// member of this class is fixable by restarting — a new session inherits the
// same credential, the same limit, the same disabled entitlement — and every
// nudge path runs through an agent, which is the population that has stopped.
//
// Alarms are coalesced fleet-wide, following synthwatch's precedent: one dead
// credential is shared by every agent, and per-agent alarms would turn one fact
// into an N-message storm at the moment a human needs to read one clear thing.
//
// # It RE-ALARMS while undelivered, and that is the whole point
//
// A conventional pager fires once per episode and stops. That is correct when
// the channel works and catastrophic when it does not: it is precisely how one
// accurate detection becomes sixteen accurate detections nobody received. Here,
// an episode that has never been CONFIRMED delivered is retried every tick, and
// the failure to deliver is its own event type ([EventUndelivered]) rather than
// a log line inside the success path.
//
// # Verified with the fleet DOWN, not up
//
// A notification mechanism verified against a healthy fleet is verified in the
// one condition where it is not needed. [Probe] therefore constructs the alarm
// against a throwaway macguffin store with NO agents at all — no registry, no
// running process, nothing that could carry a message — and asserts the bytes
// are in the mailbox the out-of-process notifier polls. Its matched control
// asserts that a store with no such mailbox produces a REFUSAL and not a
// confirmed receipt, so the positive arm cannot be green for the reason a
// scan-of-nothing is green.
package refusalwatch

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/refusalstreak"
)

// Event types emitted to ~/.pogo/events.log.
const (
	// EventDetected is emitted when an alarm is raised AND confirmed delivered.
	// Its routed_to names the sinks that took it — a list that is never empty,
	// because an empty one is EventUndelivered instead.
	EventDetected = "refusal_streak_alarm"

	// EventUndelivered is emitted when an alarm was raised and NOTHING confirmed
	// it. It is a separate type rather than a field on the success event so that
	// it cannot be read past: on 2026-09-07 sixteen findings carried
	// routed_to=nobody inside a normal-looking fired event and nobody noticed.
	EventUndelivered = "refusal_streak_undelivered"

	// EventCleared is emitted when every streaking agent has gone back to work.
	EventCleared = "refusal_streak_cleared"

	// eventAgent is the envelope identity on all three types. The daemon, not
	// any one agent: these findings are about a SET, carried in details.agents.
	eventAgent = "pogod"
)

// DefaultInterval is the minimum gap between scans of one agent. The fleet's
// nudge cadence is */10, so a failing agent writes a fresh turn every ten
// minutes at worst; scanning every five notices a run within one nudge cycle
// without re-reading transcripts on every ~30s heartbeat tick.
const DefaultInterval = 5 * time.Minute

// DefaultFreshness is how recent the most recent failing turn must be for a run
// to count as live. A run has no window by construction (that is the point of
// counting a run rather than a rate), so this is the ONE time bound in the
// package and it is on the alarm, not on the detector.
//
// 90m is measured, not chosen. Across the 11,936 adjacent failing-turn pairs in
// this fleet's crew transcripts (114 files, read 2026-09-07) the gap between
// consecutive failing turns distributes p50=10m0s, p90=30m0s, p99=76m30s, and
// the LARGEST observed gap is 83m4s. 90m clears every gap ever observed inside a
// real outage, so a live run cannot age out between two of its own turns; and it
// is short enough that a run whose last failure is two hours old reads as
// history, which it is.
const DefaultFreshness = 90 * time.Minute

// DefaultMinAlarmInterval is the floor between CONFIRMED alarms for a standing
// episode. It applies only after a delivery has been confirmed: an episode that
// has never reached anybody is retried on every scan, with no floor at all,
// because a floor on an undelivered alarm is the defect this package is for.
//
// A roster CHANGE bypasses it: a second agent joining is new information about
// the scope of the outage.
const DefaultMinAlarmInterval = 60 * time.Minute

// Target is one agent to scan. pogod builds these from its registry.
type Target struct {
	// Name is the bare agent name.
	Name string
	// Identity is the event-log identity ("crew-mayor" / "cat-8cdb").
	Identity string
	// Workdir is the agent process's working directory, from which the harness's
	// transcript path is derived.
	Workdir string
	// WorkItemID is the polecat's work item, for the alarm. Optional.
	WorkItemID string
}

// Options carries the watcher's dependencies so the package is testable with no
// filesystem and no daemon of its own.
type Options struct {
	// Home is the root the provider-declared globs are joined under.
	Home string
	// Targets enumerates the agents to scan. Required; without it Check is inert.
	Targets func() []Target
	// Globs returns the home-relative transcript globs for a workdir. Required.
	Globs func(workdir string) []string
	// Sinks are the out-of-band delivery channels, tried in order and ALL tried.
	// Empty means no alarm can be delivered, which Check reports as
	// EventUndelivered rather than passing over in silence.
	Sinks []Sink
	// Emit writes events. Defaults to events.Emit.
	Emit func(events.Event)
	// Scan overrides the reader. Defaults to refusalstreak.Scan.
	Scan func(home string, globs []string, opts refusalstreak.Options) refusalstreak.Report
	// Interval is the minimum gap between scans of one agent. Zero means
	// DefaultInterval.
	Interval time.Duration
	// Freshness bounds how old a run's last failing turn may be. Zero means
	// DefaultFreshness; NEGATIVE disables the bound.
	Freshness time.Duration
	// MinAlarmInterval floors repeat alarms for a standing, ALREADY-DELIVERED
	// episode. Zero means DefaultMinAlarmInterval; NEGATIVE disables it.
	MinAlarmInterval time.Duration
	// ScanOptions tunes the detector (MinStreak, Markers). Zero means defaults.
	ScanOptions refusalstreak.Options
	// Logf receives the daemon-log line for an alarm. Defaults to log.Printf.
	Logf func(format string, args ...any)
}

// Watcher scans agent transcripts on pogod's heartbeat and alarms on a run of
// consecutive failing turns.
type Watcher struct {
	opts Options

	mu sync.Mutex
	// streaking holds the current reading for every agent whose transcript shows
	// a live run — the episode roster.
	streaking map[string]refusalstreak.Report
	// lastScan rate-limits per-agent scans to Interval.
	lastScan map[string]time.Time
	// alarmedRoster is the roster of the last alarm RAISED, and delivered says
	// whether that alarm was ever confirmed. They are separate because an alarm
	// that was raised and not delivered must not suppress the next one, which is
	// the property the 2026-09-07 sixteen did not have.
	alarmedRoster []string
	delivered     bool
	// lastConfirmedAt is when an alarm for the standing episode was last
	// confirmed. The floor is measured from this, never from when one was sent.
	lastConfirmedAt time.Time
	// undeliveredSince is when the standing episode first failed to reach
	// anybody, and undeliveredCount how many attempts have failed. Both are
	// carried into every EventUndelivered so the duration of a silent alarm is a
	// number somebody can read rather than something they reconstruct from
	// repeated log lines.
	undeliveredSince time.Time
	undeliveredCount int
	// delivering serialises the delivery itself. pogod calls Check in a
	// goroutine on every ~30s heartbeat tick and a sink shells out to `mg`, so
	// two ticks can be inside Deliver at once — and the undelivered branch has
	// NO floor by design, so nothing else would stop them. Without this, a
	// channel that is merely slow produces duplicate pages, and the fix for that
	// must not be a floor on the undelivered path.
	delivering bool
}

// New builds a Watcher. It is inert (Check is a no-op) without Targets and
// Globs, so a daemon that could not wire them degrades to pre-alarm behaviour
// rather than panicking.
func New(opts Options) *Watcher {
	if opts.Emit == nil {
		opts.Emit = func(e events.Event) { events.Emit(context.Background(), e) }
	}
	if opts.Scan == nil {
		opts.Scan = refusalstreak.Scan
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	// Zero means "use the default"; negative means "off". Normalising negatives
	// to 0 here keeps every comparison downstream a plain `elapsed >= bound`.
	if opts.Freshness == 0 {
		opts.Freshness = DefaultFreshness
	} else if opts.Freshness < 0 {
		opts.Freshness = 0
	}
	if opts.MinAlarmInterval == 0 {
		opts.MinAlarmInterval = DefaultMinAlarmInterval
	} else if opts.MinAlarmInterval < 0 {
		opts.MinAlarmInterval = 0
	}
	return &Watcher{
		opts:      opts,
		streaking: map[string]refusalstreak.Report{},
		lastScan:  map[string]time.Time{},
	}
}

// Report returns the last known reading for an agent and whether one exists.
func (w *Watcher) Report(name string) (refusalstreak.Report, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.streaking[name]
	return r, ok
}

// Check runs one scan pass. It is the heartbeat OnTick integration point and a
// no-op when the watcher was built without Targets or Globs.
func (w *Watcher) Check(now time.Time) {
	if w.opts.Targets == nil || w.opts.Globs == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}

	present := map[string]bool{}
	for _, t := range w.opts.Targets() {
		if t.Name == "" {
			continue
		}
		present[t.Name] = true

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
		w.record(t.Name, rep, now)
	}

	// An agent that left the registry leaves the roster. Its transcript is no
	// longer evidence about a live process, and holding it would keep an episode
	// open on a machine with nothing running.
	w.mu.Lock()
	for name := range w.streaking {
		if !present[name] {
			delete(w.streaking, name)
		}
	}
	w.mu.Unlock()

	w.raise(now)
}

// record folds one agent's reading into the roster.
//
// The freshness bound is applied HERE, not in the detector: a run is positional
// and has no window, so the only question time can answer about it is whether it
// is still going.
func (w *Watcher) record(name string, rep refusalstreak.Report, now time.Time) {
	live := rep.Alarming()
	if live && w.opts.Freshness > 0 && !rep.Last.IsZero() && now.Sub(rep.Last) > w.opts.Freshness {
		live = false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if live {
		w.streaking[name] = rep
		return
	}
	delete(w.streaking, name)
}

// raise decides whether to alarm and does it.
func (w *Watcher) raise(now time.Time) {
	w.mu.Lock()
	roster := make([]string, 0, len(w.streaking))
	reports := map[string]refusalstreak.Report{}
	for name, rep := range w.streaking {
		roster = append(roster, name)
		reports[name] = rep
	}
	sort.Strings(roster)

	if len(roster) == 0 {
		cleared := len(w.alarmedRoster) > 0
		prev := w.alarmedRoster
		wasDelivered := w.delivered
		w.alarmedRoster = nil
		w.delivered = false
		w.lastConfirmedAt = time.Time{}
		w.undeliveredSince = time.Time{}
		w.undeliveredCount = 0
		w.mu.Unlock()
		if cleared {
			// AN EPISODE THAT CLEARS HAVING NEVER REACHED ANYBODY is this item's
			// own defect closing quietly: the outage happened, the alarm was
			// correct, and the record of it is a line nobody looked at. There is
			// nothing left to escalate to by definition — the channels are what
			// failed — so the two things available are said as loudly as
			// possible: a distinct daemon-log line, and was_delivered on the
			// event, which is what makes the case COUNTABLE rather than a thing
			// somebody has to notice.
			if !wasDelivered {
				w.opts.Logf("pogod: refusal-streak episode CLEARED WITHOUT EVER REACHING ANYBODY — "+
					"agents=%v. The outage happened and no channel took the alarm; see refusal_streak_undelivered.", prev)
			}
			w.opts.Emit(events.Event{
				EventType: EventCleared,
				// The DAEMON is the agent on all three of this watcher's event
				// types, following turn_watch_error: the finding is about a SET
				// of agents (details.agents), and stamping one of them here would
				// make a fleet-wide fact read as a per-agent one.
				Agent: eventAgent,
				Details: map[string]any{
					"agents":        prev,
					"was_delivered": wasDelivered,
					"why": "every agent in the episode has completed a turn again; nothing here restarted or " +
						"nudged anything, so this cleared on its own or on a human",
				},
			})
		}
		return
	}

	if w.delivering {
		// A delivery for this episode is already in flight. Skipping is right
		// rather than queueing: the next tick is 30 seconds away and the alarm
		// it would send is the same one.
		w.mu.Unlock()
		return
	}

	same := sameRoster(roster, w.alarmedRoster)
	switch {
	case !same:
		// A new or grown roster is new information about the scope. Always alarm.
	case !w.delivered:
		// The standing episode has NEVER reached anybody. Retry with no floor:
		// this is the branch that stops sixteen accurate detections from being
		// sixteen accurate silences.
	case w.opts.MinAlarmInterval > 0 && now.Sub(w.lastConfirmedAt) < w.opts.MinAlarmInterval:
		w.mu.Unlock()
		return
	}
	w.alarmedRoster = roster
	worst := roster[0]
	for _, name := range roster {
		if reports[name].Streak > reports[worst].Streak {
			worst = name
		}
	}
	undeliveredSince := w.undeliveredSince
	w.delivering = true
	w.mu.Unlock()

	alarm := Alarm{Agents: roster, Reports: reports, Worst: worst, Now: now}
	receipts, err := Deliver(w.opts.Sinks, alarm)
	routed := Confirmed(receipts)

	w.mu.Lock()
	w.delivering = false
	if err == nil {
		w.delivered = true
		w.lastConfirmedAt = now
		w.undeliveredSince = time.Time{}
		w.undeliveredCount = 0
	} else {
		w.delivered = false
		if w.undeliveredSince.IsZero() {
			w.undeliveredSince = now
			undeliveredSince = now
		}
		w.undeliveredCount++
	}
	count := w.undeliveredCount
	w.mu.Unlock()

	rep := reports[worst]
	if err != nil {
		silent := time.Duration(0)
		if !undeliveredSince.IsZero() {
			silent = now.Sub(undeliveredSince)
		}
		w.opts.Logf("pogod: REFUSAL-STREAK ALARM COULD NOT BE DELIVERED (attempt %d, silent for %s): %s: %v",
			count, silent.Round(time.Second), alarm.Subject(), err)
		w.opts.Emit(events.Event{
			EventType: EventUndelivered,
			Agent:     eventAgent,
			Details: map[string]any{
				"agents":         roster,
				"streak":         rep.Streak,
				"reason":         string(rep.Reason),
				"brief":          rep.Brief(),
				"routed_to":      "nobody",
				"attempts":       count,
				"silent_seconds": int(silent / time.Second),
				"sinks":          receipts,
				"why": "the alarm was RAISED and nothing confirmed it. This is the 2026-09-07 state: a correct " +
					"detection with no path to a person. It will be retried on every scan until something takes it.",
			},
		})
		return
	}

	w.opts.Logf("pogod: refusal-streak alarm delivered to %v — %s", routed, alarm.Subject())
	w.opts.Emit(events.Event{
		EventType: EventDetected,
		Agent:     eventAgent,
		Details: map[string]any{
			"agents":    roster,
			"streak":    rep.Streak,
			"reason":    string(rep.Reason),
			"brief":     rep.Brief(),
			"detail":    rep.Detail,
			"routed_to": routed,
			"sinks":     receipts,
			"why": "N consecutive assistant turns were answered locally and failed, with no established work " +
				"between them. Presence instruments read green throughout; nothing restarts this.",
		},
	})
}

func sameRoster(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pogoHome is config.PogoHome, indirected so this package states its one
// dependency on pogo state in a single place.
func pogoHome() string { return config.PogoHome() }

// Describe renders the watcher's configuration for the daemon log. The bounds
// are stated because they are the only knobs that can make this channel say LESS
// than it did, and a reader asking "why was I not alarmed" must be able to read
// their values off the daemon rather than off the source of whatever revision
// they think is running.
func (w *Watcher) Describe() string {
	names := make([]string, 0, len(w.opts.Sinks))
	for _, s := range w.opts.Sinks {
		if s != nil {
			names = append(names, s.Name())
		}
	}
	return fmt.Sprintf("min-streak=%d, interval=%s, freshness=%s, alarm-floor=%s, sinks=%v "+
		"(an UNDELIVERED alarm ignores the floor and retries every scan)",
		minStreakOf(w.opts.ScanOptions), w.opts.Interval, w.opts.Freshness, w.opts.MinAlarmInterval, names)
}

func minStreakOf(o refusalstreak.Options) int {
	if o.MinStreak <= 0 {
		return refusalstreak.DefaultMinStreak
	}
	return o.MinStreak
}
