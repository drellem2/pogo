// Modal-dismissal watcher (mg-4421 — combined impl of mg-ef6b and mg-5a3d).
//
// Lives in pogod's PTY-managing goroutine for each agent. Subscribes to the
// tee'd PTY-output stream, byte-scans for any of the configured modal markers,
// and on a confirmed wedge writes the modal's dismissal keystroke directly to
// the agent's PTY stdin. No `pogo schedule` involvement; the watcher survives
// schedule-substrate failures (mg-8e5d class).
//
// Why one watcher with a matcher table rather than one watcher per modal:
// the SessionHook lifecycle is the load-bearing primitive; the matcher table
// is the cheap policy layer. Future Claude Code modals are one struct entry,
// not a new goroutine.
//
// Idle-gate dispatch — there are two modes, picked per matcher:
//
//   - ModeScannerIdle: fire when marker has been observed AND no further PTY
//     chunks have arrived for IdleAfterMarker. Used by modals that genuinely
//     freeze the tee stream (rating dialog).
//
//   - ModeEventsStale: fire when marker has been observed recently AND the
//     agent's identity has not produced an event-log line for EventStaleness.
//     Used by modals whose auto-update animation keeps the tee stream live
//     even when the reasoning loop is wedged (rate-limit-options modal — see
//     mg-5a3d §3 for why scanner-idle alone is brittle here).
package claude

import (
	"bytes"
	"context"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/events"
)

// RatingDialogMarker matches Claude Code's mid-session session-rating prompt.
// The string is stable across current Claude Code versions; if it drifts, set
// POGO_RATING_DIALOG_MARKER to override per mg-ef6b §3.
const RatingDialogMarker = "1:Bad 2:Fine 3:Good 0:Dismiss"

// RateLimitMarker matches the first menu option of Claude Code's API
// rate-limit-options modal. The phrase is more specific than the modal's
// header ("What do you want to do?"), which would false-positive on legitimate
// agent output; see mg-5a3d §2 for the marker-choice rationale.
const RateLimitMarker = "Stop and wait for limit to reset"

// IdleMode selects how a matcher's idle gate confirms that the marker text
// represents an actually-wedged dialog rather than an in-flight transcript
// mention or user-invoked interaction.
type IdleMode int

const (
	// ModeScannerIdle requires the marker to be visible AND no further
	// PTY chunk to arrive for IdleAfterMarker.
	ModeScannerIdle IdleMode = iota
	// ModeEventsStale requires the marker to be recently seen AND the
	// agent's event log to be silent for EventStaleness.
	ModeEventsStale
)

// IdleGatePolicy is the per-matcher decision rule.
type IdleGatePolicy struct {
	Mode            IdleMode
	IdleAfterMarker time.Duration // for ModeScannerIdle
	EventStaleness  time.Duration // for ModeEventsStale

	// UsageLimitStaleness, when > 0, enables a second, earlier stage on a
	// ModeEventsStale matcher: while the marker is recently visible and the
	// agent's event log has been stale for longer than this (shorter than
	// EventStaleness), the watcher declares a *suspected usage-limit hit* —
	// emits usage_limit_hit, flags the agent RateLimited, and notifies the
	// fleet usage-limit coordinator. It clears (usage_limit_cleared) when the
	// event log advances again. This is diagnostic only; it does not dismiss
	// the modal — the EventStaleness gate still owns the 20m auto-dismissal.
	// Zero disables the stage (used by matchers with no usage-limit semantics).
	UsageLimitStaleness time.Duration // for ModeEventsStale (gh #45)
}

// ModalMatcher is one entry in the watcher table. Adding a matcher = adding
// a struct literal to DefaultModalMatchers; no other changes required.
type ModalMatcher struct {
	Name       string
	LineMarker string
	Dismissal  []byte
	IdleGate   IdleGatePolicy
}

// DefaultModalMatchers ships with both currently-known wedge dialogs:
// rating-dialog (mg-ef6b) and rate-limit-options (mg-5a3d).
var DefaultModalMatchers = []ModalMatcher{
	{
		Name:       "rating-dialog",
		LineMarker: RatingDialogMarker,
		Dismissal:  []byte("0\n"),
		IdleGate: IdleGatePolicy{
			Mode:            ModeScannerIdle,
			IdleAfterMarker: 500 * time.Millisecond,
		},
	},
	{
		Name:       "rate-limit-options",
		LineMarker: RateLimitMarker,
		Dismissal:  []byte("1\n"),
		IdleGate: IdleGatePolicy{
			Mode:                ModeEventsStale,
			EventStaleness:      20 * time.Minute,
			UsageLimitStaleness: UsageLimitSuspectStaleness,
		},
	},
}

// UsageLimitSuspectStaleness is how long the agent's event log must be stale —
// with the rate-limit modal still recently visible — before the watcher
// declares a suspected usage-limit hit (gh #45). It is deliberately much
// longer than a scanner-idle window: the rate-limit marker text also appears in
// ordinary transcripts, so a seconds-level trigger would false-positive on an
// agent that merely quotes the phrase. Requiring ~5m of event-log silence means
// only a genuinely wedged agent — one that has stopped producing events while
// the modal stays on screen — trips the gate. It is shorter than
// EventStaleness (20m) so operators learn of the wedge well before the
// auto-dismissal fires.
const UsageLimitSuspectStaleness = 5 * time.Minute

// dismissalCooldown is the per-matcher no-re-fire window after a successful
// dismissal write. Prevents a modal that briefly redraws its marker line from
// triggering a second dismissal.
const dismissalCooldown = 5 * time.Minute

// markerRecency bounds how long after the most recent marker observation
// the events-stale gate stays armed. Beyond this we assume the modal has
// resolved by other means (user picked an option, agent restarted, etc.).
const markerRecency = 60 * time.Second

// eventsStalePollInterval is how often the events-stale matcher re-checks
// its gate while armed. Mutable (via setEventsStalePollIntervalForTest) so
// tests can run the dispatcher fast without sleeping 30s per case.
var eventsStalePollInterval = 30 * time.Second

// setEventsStalePollIntervalForTest swaps the poll interval. Returns the
// previous value so callers can defer-restore it. Tests only.
func setEventsStalePollIntervalForTest(d time.Duration) time.Duration {
	prev := eventsStalePollInterval
	eventsStalePollInterval = d
	return prev
}

// scanBufBytes is the size of the per-watcher sliding output buffer. Must
// comfortably exceed the longest marker; 8 KiB is plenty.
const scanBufBytes = 8 * 1024

// ActivityTracker reports the last time we observed an event-log line for
// a given agent identity. Exported so tests can inject deterministic values.
type ActivityTracker interface {
	LastSeen(agentID string) time.Time
}

// ModalHookDeps is the injectable surface RunModalHook closes over. Production
// code wires it from a real Agent via ModalHook; tests construct it directly.
type ModalHookDeps struct {
	AgentName string                     // for logs (e.g. "alice")
	AgentID   string                     // for event envelopes (e.g. "cat-alice")
	Subscribe func(w io.Writer) func()   // returns unsubscribe
	Dismiss   func(payload []byte) error // write dismissal to PTY stdin
	Tracker   ActivityTracker            // event-log staleness lookups
	Now       func() time.Time           // overridable clock for tests
	EmitEvent func(ev events.Event)      // dismissal observability
	NotifyPM  func(agentID, matcherName string)

	// WorkItemID is the agent's mg work item (e.g. "mg-7ffa"), stamped into the
	// usage_limit_hit / usage_limit_cleared events and the coordinator roster.
	// Empty for agents not tied to an item.
	WorkItemID string
	// SetRateLimited flags/unflags the agent's RateLimited condition (surfaced
	// in `pogo status` and `pogo agent diagnose`). Nil in tests that don't care.
	SetRateLimited func(bool)
	// OnUsageLimitHit / OnUsageLimitClear notify the fleet usage-limit
	// coordinator so it can coalesce one operator mail per fleet-wide episode
	// (gh #45). Nil disables the coalesced-mail path.
	OnUsageLimitHit   func(agentID, workItemID string, when time.Time)
	OnUsageLimitClear func(agentID string, when time.Time)
}

// ModalHook is the SessionHook entrypoint. Wires production defaults and runs
// the watcher for the agent's lifetime.
func ModalHook(ctx context.Context, a *agent.Agent) {
	deps := ModalHookDeps{
		AgentName: a.Name,
		AgentID:   a.EventAgent(),
		Subscribe: a.Subscribe,
		Dismiss:   defaultDismisser(a),
		Tracker:   defaultActivityTracker(),
		Now:       time.Now,
		EmitEvent: func(ev events.Event) { events.Emit(context.Background(), ev) },
		NotifyPM:  defaultNotifyPM,

		WorkItemID:     a.WorkItemID,
		SetRateLimited: a.SetRateLimited,
		OnUsageLimitHit: func(agentID, workItemID string, when time.Time) {
			defaultUsageLimitCoordinator().OnHit(agentID, workItemID, when)
		},
		OnUsageLimitClear: func(agentID string, when time.Time) {
			defaultUsageLimitCoordinator().OnClear(agentID, when)
		},
	}
	RunModalHook(ctx, deps, DefaultModalMatchers)
}

// RunModalHook is the testable form of ModalHook. Production callers go
// through ModalHook; tests inject ModalHookDeps + a custom matcher table.
func RunModalHook(ctx context.Context, deps ModalHookDeps, matchers []ModalMatcher) {
	if len(matchers) == 0 {
		return
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.EmitEvent == nil {
		deps.EmitEvent = func(events.Event) {}
	}
	if deps.NotifyPM == nil {
		deps.NotifyPM = func(string, string) {}
	}
	if deps.Tracker == nil {
		deps.Tracker = nullTracker{}
	}

	scanner := newModalScanner(matchers, deps.Now)
	if deps.Subscribe != nil {
		unsubscribe := deps.Subscribe(scanner)
		defer unsubscribe()
	}

	var wg sync.WaitGroup
	for i, m := range matchers {
		wg.Add(1)
		go func(idx int, mm ModalMatcher) {
			defer wg.Done()
			dispatchMatcher(ctx, idx, mm, scanner, deps)
		}(i, m)
	}
	wg.Wait()
}

// modalScanner accumulates a sliding window of PTY output (ANSI-stripped) and
// signals per-matcher channels when each matcher's marker is currently visible
// or when any chunk has arrived (the latter resets ModeScannerIdle's idle
// window). All state is protected by mu; observed/output channels are
// buffered (1) and dropped if full — dispatchMatcher re-reads scanner state
// on wake, so dropping a signal can only delay a fire by one chunk.
type modalScanner struct {
	mu       sync.Mutex
	rawBuf   []byte
	matchers []ModalMatcher
	now      func() time.Time

	// markerLastSeen[i] records the most recent time matcher i's marker was
	// observed in the cleaned buffer. Read by dispatchMatcher; updated by
	// Write under mu.
	markerLastSeen []time.Time

	// observed[i] signals a matcher i observation; output signals any chunk.
	observed []chan struct{}
	output   chan struct{}
}

func newModalScanner(matchers []ModalMatcher, now func() time.Time) *modalScanner {
	s := &modalScanner{
		matchers:       matchers,
		now:            now,
		markerLastSeen: make([]time.Time, len(matchers)),
		observed:       make([]chan struct{}, len(matchers)),
		output:         make(chan struct{}, 1),
	}
	for i := range s.observed {
		s.observed[i] = make(chan struct{}, 1)
	}
	return s
}

// Write is the io.Writer surface a.Subscribe consumes. Runs on pogod's PTY
// reader goroutine; must not block or call into a.mu (the agent's master fd
// is held under that lock).
func (s *modalScanner) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	cp := make([]byte, len(p))
	copy(cp, p)

	s.mu.Lock()
	s.rawBuf = append(s.rawBuf, cp...)
	if len(s.rawBuf) > scanBufBytes {
		s.rawBuf = s.rawBuf[len(s.rawBuf)-scanBufBytes:]
	}
	clean := agent.StripANSI(s.rawBuf)
	now := s.now()
	for i, m := range s.matchers {
		if bytes.Contains(clean, []byte(m.LineMarker)) {
			s.markerLastSeen[i] = now
			nbSend(s.observed[i])
		}
	}
	s.mu.Unlock()

	nbSend(s.output)
	return len(p), nil
}

// MarkerVisible reports whether matcher idx's marker is currently visible in
// the cleaned buffer. Used by dispatchMatcher to re-verify before firing.
func (s *modalScanner) MarkerVisible(idx int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := agent.StripANSI(s.rawBuf)
	return bytes.Contains(clean, []byte(s.matchers[idx].LineMarker))
}

// MarkerLastSeen returns the most recent time matcher idx's marker was seen.
func (s *modalScanner) MarkerLastSeen(idx int) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markerLastSeen[idx]
}

func nbSend(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// dispatchMatcher is one goroutine per matcher. It owns the idle-gate state
// machine for its mode and calls fireDismissal exactly once when the gate
// passes (then waits dismissalCooldown before considering re-fire).
func dispatchMatcher(ctx context.Context, idx int, m ModalMatcher, scanner *modalScanner, deps ModalHookDeps) {
	switch m.IdleGate.Mode {
	case ModeScannerIdle:
		dispatchScannerIdle(ctx, idx, m, scanner, deps)
	case ModeEventsStale:
		dispatchEventsStale(ctx, idx, m, scanner, deps)
	default:
		log.Printf("modal_hook: agent %s matcher %q: unknown idle mode %d, skipping",
			deps.AgentName, m.Name, m.IdleGate.Mode)
		<-ctx.Done()
	}
}

func dispatchScannerIdle(ctx context.Context, idx int, m ModalMatcher, scanner *modalScanner, deps ModalHookDeps) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false
	var lastDismissed time.Time

	resetIdle := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(m.IdleGate.IdleAfterMarker)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-scanner.observed[idx]:
			armed = true
			resetIdle()
		case <-scanner.output:
			if armed {
				resetIdle()
			}
		case <-timer.C:
			if !armed {
				continue
			}
			armed = false
			if !lastDismissed.IsZero() && deps.Now().Sub(lastDismissed) < dismissalCooldown {
				continue
			}
			// Re-verify marker is still visible before firing — guards
			// against stale-observation false positives where the marker
			// scrolled off but the idle window otherwise looks clear.
			if !scanner.MarkerVisible(idx) {
				continue
			}
			if fireDismissal(m, deps) {
				lastDismissed = deps.Now()
			}
		}
	}
}

func dispatchEventsStale(ctx context.Context, idx int, m ModalMatcher, scanner *modalScanner, deps ModalHookDeps) {
	ticker := time.NewTicker(eventsStalePollInterval)
	defer ticker.Stop()
	var lastDismissed time.Time

	// Usage-limit hit/clear state (gh #45). hitActive is true once we've
	// declared a suspected hit that has not yet cleared; hitEventsSeen is the
	// event-log LastSeen value at the moment we declared it, so the clear gate
	// can detect the log advancing (agent producing events again).
	var (
		hitActive     bool
		hitEventsSeen time.Time
	)

	check := func() {
		now := deps.Now()
		seen := scanner.MarkerLastSeen(idx)
		eventsLastSeen := deps.Tracker.LastSeen(deps.AgentID)

		// --- usage-limit suspected-hit / clear stage -----------------------
		// Independent of the dismissal gate below: emitting the hit is
		// observability, not intervention. Runs only for matchers that opt in
		// via UsageLimitStaleness > 0 (the rate-limit-options matcher).
		if m.IdleGate.UsageLimitStaleness > 0 {
			if !hitActive {
				markerFresh := !seen.IsZero() && now.Sub(seen) <= markerRecency
				// A fresh-spawned agent with no events yet is treated as fresh
				// (never a hit); the same guard the dismissal gate uses.
				eventsStale := !eventsLastSeen.IsZero() &&
					now.Sub(eventsLastSeen) > m.IdleGate.UsageLimitStaleness
				if markerFresh && eventsStale {
					emitUsageLimitHit(deps, now)
					hitActive = true
					hitEventsSeen = eventsLastSeen
				}
			} else if !eventsLastSeen.IsZero() && eventsLastSeen.After(hitEventsSeen) {
				// Event log advanced past the wedge point — the agent is
				// producing events again, so the limit has cleared.
				emitUsageLimitCleared(deps, now)
				hitActive = false
				hitEventsSeen = time.Time{}
			}
		}

		// --- 20m auto-dismissal gate (unchanged) ---------------------------
		if seen.IsZero() {
			return
		}
		if now.Sub(seen) > markerRecency {
			return
		}
		if !lastDismissed.IsZero() && now.Sub(lastDismissed) < dismissalCooldown {
			return
		}
		// A fresh-spawned agent without any events yet is treated as fresh;
		// the dispatcher will get another tick once events show up. Without
		// this guard, a missing tracker entry would fire dismissal immediately
		// even though the agent just started.
		if eventsLastSeen.IsZero() {
			return
		}
		if now.Sub(eventsLastSeen) <= m.IdleGate.EventStaleness {
			return
		}
		if fireDismissal(m, deps) {
			lastDismissed = now
			deps.NotifyPM(deps.AgentID, m.Name)
		}
	}

	for {
		select {
		case <-ctx.Done():
			// If the agent exits while still flagged, release the flag and let
			// the coordinator drop it from the current episode so a stuck agent
			// can't hold the episode open forever. This is a release, not a
			// recovery — no usage_limit_cleared event is emitted (the agent's
			// lifecycle events already record its death).
			if hitActive {
				if deps.SetRateLimited != nil {
					deps.SetRateLimited(false)
				}
				if deps.OnUsageLimitClear != nil {
					deps.OnUsageLimitClear(deps.AgentID, deps.Now())
				}
			}
			return
		case <-scanner.observed[idx]:
			// Just an arming signal; the ticker drives evaluation.
		case <-ticker.C:
			check()
		}
	}
}

// emitUsageLimitHit records a suspected usage-limit hit: it flags the agent
// RateLimited, emits the usage_limit_hit event, and notifies the fleet
// coordinator. Called at most once per wedge (guarded by hitActive).
func emitUsageLimitHit(deps ModalHookDeps, when time.Time) {
	if deps.SetRateLimited != nil {
		deps.SetRateLimited(true)
	}
	log.Printf("modal_hook: agent %s suspected usage-limit hit (rate-limit modal visible, event log stale)",
		deps.AgentName)
	deps.EmitEvent(events.Event{
		EventType:  "usage_limit_hit",
		Agent:      deps.AgentID,
		WorkItemID: deps.WorkItemID,
		Timestamp:  when.UTC().Format(time.RFC3339Nano),
		Details: map[string]any{
			"matcher": "rate-limit-options",
		},
	})
	if deps.OnUsageLimitHit != nil {
		deps.OnUsageLimitHit(deps.AgentID, deps.WorkItemID, when)
	}
}

// emitUsageLimitCleared records recovery from a usage-limit hit: it clears the
// RateLimited flag, emits usage_limit_cleared, and notifies the coordinator.
func emitUsageLimitCleared(deps ModalHookDeps, when time.Time) {
	if deps.SetRateLimited != nil {
		deps.SetRateLimited(false)
	}
	log.Printf("modal_hook: agent %s usage limit cleared (event log advancing again)", deps.AgentName)
	deps.EmitEvent(events.Event{
		EventType:  "usage_limit_cleared",
		Agent:      deps.AgentID,
		WorkItemID: deps.WorkItemID,
		Timestamp:  when.UTC().Format(time.RFC3339Nano),
		Details: map[string]any{
			"matcher": "rate-limit-options",
		},
	})
	if deps.OnUsageLimitClear != nil {
		deps.OnUsageLimitClear(deps.AgentID, when)
	}
}

// fireDismissal writes the dismissal sequence via the two-write pattern (body,
// 50ms submit-delay gap, submit byte) per mg-09b6's paste-detection bug class. Even though
// the trust-hook precedent (commit 782080b) writes "0\n" as one shot for the
// rating dialog and works, the rate-limit modal is the higher-risk case and
// the same split costs nothing on the rating path. Returns true if the write
// succeeded; false otherwise (the dispatcher then leaves the cooldown unset
// and may retry on the next gate evaluation).
func fireDismissal(m ModalMatcher, deps ModalHookDeps) bool {
	if len(m.Dismissal) == 0 || deps.Dismiss == nil {
		return false
	}
	if err := deps.Dismiss(m.Dismissal); err != nil {
		log.Printf("modal_hook: agent %s dismissal %q write failed: %v",
			deps.AgentName, m.Name, err)
		return false
	}
	log.Printf("modal_hook: agent %s dismissed %q modal", deps.AgentName, m.Name)
	deps.EmitEvent(events.Event{
		EventType: "modal_dismissed",
		Agent:     deps.AgentID,
		Details: map[string]any{
			"matcher": m.Name,
		},
	})
	// Back-compat alias events so subscribers that grep for the historical
	// names from the design docs (rating_dialog_dismissed / rate_limit_modal_dismissed)
	// don't have to migrate.
	switch m.Name {
	case "rating-dialog":
		deps.EmitEvent(events.Event{
			EventType: "rating_dialog_dismissed",
			Agent:     deps.AgentID,
			Details:   map[string]any{},
		})
	case "rate-limit-options":
		deps.EmitEvent(events.Event{
			EventType: "rate_limit_modal_dismissed",
			Agent:     deps.AgentID,
			Details:   map[string]any{},
		})
	}
	return true
}

// defaultDismisser produces the production Dismiss func bound to an agent.
// Uses the two-write pattern (body + submit-delay + submit byte) to dodge
// Claude Code's paste-detection collapsing "1\n" into a literal input-box
// entry rather than a menu selection (mg-09b6 / mg-5a3d Dismissal-write). The
// inter-write delay is the Claude nudge dialect's SubmitDelay — read from
// agent.DefaultNudgeProfile (which claude.Provider.Nudge adopts verbatim)
// rather than claude.Provider to avoid a package-init cycle.
func defaultDismisser(a *agent.Agent) func([]byte) error {
	return func(payload []byte) error {
		if len(payload) == 0 {
			return nil
		}
		body, submit := splitDismissal(payload)
		if len(body) > 0 {
			if err := a.SendRaw(string(body)); err != nil {
				return err
			}
			time.Sleep(agent.DefaultNudgeProfile.SubmitDelay)
		}
		if len(submit) > 0 {
			if err := a.SendRaw(string(submit)); err != nil {
				return err
			}
		}
		return nil
	}
}

// splitDismissal returns the digit/letter body and the trailing submit byte
// (typically "\n" or "\r"). If the payload has no trailing terminator the
// whole thing is treated as body and submit is empty.
func splitDismissal(payload []byte) (body, submit []byte) {
	if len(payload) == 0 {
		return nil, nil
	}
	last := payload[len(payload)-1]
	if last == '\n' || last == '\r' {
		return payload[:len(payload)-1], payload[len(payload)-1:]
	}
	return payload, nil
}

// defaultNotifyPM mails the responsible PM that a rate-limit dismissal fired
// for an agent. Heuristic per mg-5a3d §6: pm-pogo for pogo-crew agents,
// pm-onethird for onethird agents. The team field doesn't exist on Agent
// today, so we route on name substring — best-effort, a wrong route is just
// an unread inbox item.
func defaultNotifyPM(agentID, matcherName string) {
	if matcherName != "rate-limit-options" {
		return
	}
	pm := "pm-pogo"
	if strings.Contains(strings.ToLower(agentID), "onethird") {
		pm = "pm-onethird"
	}
	subject := "rate-limit modal auto-dismissed for " + agentID
	body := "pogod's modal watcher (mg-4421) dismissed a rate-limit-options modal " +
		"on agent " + agentID + " after 20m of event-log silence. " +
		"The agent should now be responsive — confirm and chase any in-flight work."
	if err := client.SendMGMail(pm, "pogod", subject, body); err != nil {
		log.Printf("modal_hook: failed to notify %s for %s: %v", pm, agentID, err)
	}
}

// --- activity tracker -------------------------------------------------------

// nullTracker reports zero LastSeen for every agent. Used when DefaultLogPath
// is unavailable (e.g. some test setups); also returned by dispatchEventsStale
// to short-circuit firing on a missing tracker entry.
type nullTracker struct{}

func (nullTracker) LastSeen(string) time.Time { return time.Time{} }

// eventsActivityTracker tails ~/.pogo/events.log and maintains an in-memory
// map of last-seen-event-timestamp keyed by canonical agent identity. The
// goroutine lives for pogod's lifetime; no shutdown hook (the process exits
// will reap it).
type eventsActivityTracker struct {
	mu       sync.RWMutex
	lastSeen map[string]time.Time
}

var (
	defaultTrackerOnce sync.Once
	defaultTracker     ActivityTracker
)

// defaultActivityTracker returns a process-wide singleton that follows
// ~/.pogo/events.log. Lazy-started so it doesn't spin up a goroutine + open
// the log in pure-library callers (tests, mg).
func defaultActivityTracker() ActivityTracker {
	defaultTrackerOnce.Do(func() {
		path, err := events.DefaultLogPath()
		if err != nil {
			log.Printf("modal_hook: cannot resolve events log path: %v (events-stale gate disabled)", err)
			defaultTracker = nullTracker{}
			return
		}
		defaultTracker = startEventsTracker(path)
	})
	return defaultTracker
}

func startEventsTracker(path string) *eventsActivityTracker {
	t := &eventsActivityTracker{lastSeen: make(map[string]time.Time)}
	stop := make(chan struct{}) // never closed; tracker lives until process exit
	go func() {
		if err := events.Follow(path, time.Second, stop, func(line []byte) {
			t.ingest(line)
		}); err != nil {
			log.Printf("modal_hook: events tracker stopped: %v", err)
		}
	}()
	return t
}

func (t *eventsActivityTracker) ingest(line []byte) {
	ev, err := events.ParseLine(line)
	if err != nil || ev.Agent == "" || ev.Timestamp == "" {
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
	if err != nil {
		return
	}
	t.mu.Lock()
	if existing, ok := t.lastSeen[ev.Agent]; !ok || ts.After(existing) {
		t.lastSeen[ev.Agent] = ts
	}
	t.mu.Unlock()
}

// LastSeen returns the most recent observed event timestamp for agent, or
// the zero time if no event has been seen yet.
func (t *eventsActivityTracker) LastSeen(agentID string) time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lastSeen[agentID]
}

// Touch records an observation for agentID at when. Exported for adapters
// that want to seed activity (e.g. mark an agent as freshly-spawned).
func (t *eventsActivityTracker) Touch(agentID string, when time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.lastSeen[agentID]; !ok || when.After(existing) {
		t.lastSeen[agentID] = when
	}
}
