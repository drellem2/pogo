package claude

// Positive control for mg-d578.
//
// mg-d578 diagnosed TestModalHook_Case3_RateLimitFiresOnEventsStale's 2-in-3
// failure under `go test -race` as a race in the TEST FIXTURES — a package-level
// poll-interval knob written by one test while a leaked watcher goroutine from
// the previous test was still reading it — and not a race in modal_hook.go's
// production path. The whole risk of that verdict is that it is the comfortable
// one: "the fixture was at fault" is exactly what someone would conclude if a
// genuine race in the shipped watcher were being masked by the noisier fixture
// report.
//
// A green suite is not evidence for the verdict. The fixture race was reported
// against Case3 no matter which goroutine actually raced, so removing it would
// make the package quiet whether or not the production path is clean. This file
// is the control that closes that gap: it drives the production watcher — the
// real DefaultModalMatchers table, the real eventsActivityTracker, the real
// usageLimitCoordinator, several agents at once — hard enough that a race in any
// of them has to surface, and asserts that the dismissal and usage-limit paths
// were actually executed rather than idled through.
//
// The control was verified to have teeth, not assumed to: with
// modalScanner.MarkerVisible's `s.mu.Lock()` deleted, this test fails under
// -race with a WARNING: DATA RACE naming modalScanner.Write / MarkerVisible.
// See the mg-d578 changelog entry for the recorded output. Restoring the lock
// makes it green again.

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// productionMatchersFastPoll returns DefaultModalMatchers with one field
// changed: the events-stale poll interval, shrunk so a ~1s test covers hundreds
// of gate evaluations instead of none.
//
// Every other value — marker text, dismissal payload, idle mode, the 500ms
// scanner-idle window, the 20m EventStaleness, the 5m UsageLimitStaleness — is
// production's, because the point of a positive control is to exercise shipped
// configuration rather than a convenient stand-in. Cadence is the one dimension
// that cannot be left alone (a 30s poll under a short test evaluates the gate
// zero times), and it is also the one dimension that cannot hide a race: it
// changes how often the shared state is touched, never which state or under
// which lock.
//
// The copy is deliberate. DefaultModalMatchers is a package-level slice that
// production reads; mutating its elements in place would make this control the
// very thing it exists to rule out.
func productionMatchersFastPoll(poll time.Duration) []ModalMatcher {
	out := make([]ModalMatcher, len(DefaultModalMatchers))
	copy(out, DefaultModalMatchers)
	for i := range out {
		if out[i].IdleGate.Mode == ModeEventsStale {
			out[i].IdleGate.PollInterval = poll
		}
	}
	return out
}

// productionRateLimitModal is the PTY shape the rate-limit-options modal
// arrives in: SGR-coloured menu rows whose columns are placed with
// cursor-forward escapes, the same rendering that defeated the pre-mg-f36b
// literal matcher.
const productionRateLimitModal = "\x1b[2K\x1b[1mWhat do you want to do?\x1b[0m\r\n" +
	"\x1b[38;5;244m1:\x1b[2C" + RateLimitMarker + "\x1b[0m\r\n" +
	"\x1b[38;5;244m2:\x1b[2CTry a different model\x1b[0m\r\n"

// TestModalHook_ProductionPathRaceControl runs several production watchers
// concurrently against shared production collaborators and asserts that the
// dismissal and usage-limit paths both executed. Under -race, any unsynchronised
// access in modal_hook.go's shipped path fails this test.
func TestModalHook_ProductionPathRaceControl(t *testing.T) {
	const (
		agents   = 4
		duration = 750 * time.Millisecond
	)

	// The real activity tracker pogod runs: its ingest side is driven by the
	// events.Follow goroutine and its LastSeen side by every matcher's gate, so
	// the two are genuinely concurrent in production. Seeded 21m stale so the
	// production 20m EventStaleness gate trips and the fire path executes.
	tracker := &eventsActivityTracker{lastSeen: make(map[string]time.Time)}
	wedgedAt := time.Now().Add(-21 * time.Minute)

	// The real fleet coordinator: one instance, every agent's watcher calling
	// into it. Mail and structured events go to counting sinks so the control
	// touches no real inbox or events log; the coordinator's own locking — the
	// part under test — is untouched by that substitution.
	mails := &mailSink{}
	esink := &eventSink{}
	coord := newUsageLimitCoordinatorWithHoldDown(
		mails.send, time.Now, 10*time.Millisecond, nil, esink.emit)

	matchers := productionMatchersFastPoll(time.Millisecond)

	var (
		dismissals atomic.Int64
		hits       atomic.Int64
		emitted    atomic.Int64
		readerHits atomic.Int64
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	for a := 0; a < agents; a++ {
		agentID := "cat-race-control-" + string(rune('a'+a))
		tracker.Touch(agentID, wedgedAt)

		// scannerCh hands this agent's live *modalScanner — the exact instance
		// the watcher's dispatchers are reading — to the hammer goroutines
		// below, so the control races production readers against production
		// writers rather than against a private copy.
		scannerCh := make(chan *modalScanner, 1)

		deps := ModalHookDeps{
			AgentName:  agentID,
			AgentID:    agentID,
			WorkItemID: "mg-d578",
			Now:        time.Now, // production clock, not an injected one
			Subscribe: func(w io.Writer) func() {
				if s, ok := w.(*modalScanner); ok {
					scannerCh <- s
				}
				return func() {}
			},
			Dismiss: func([]byte) error {
				dismissals.Add(1)
				return nil
			},
			Tracker:   tracker,
			EmitEvent: func(events.Event) { emitted.Add(1) },
			NotifyPM:  func(string, string) {},
			SetRateLimited: func(bool) {
				// Production binds this to agent.Agent's locked setter; the
				// control only needs a call site that several goroutines reach.
			},
			OnUsageLimitHit: func(id, item string, when time.Time) {
				hits.Add(1)
				coord.OnHit(id, item, when)
			},
			OnUsageLimitClear: func(id string, when time.Time) {
				coord.OnClear(id, when)
			},
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			RunModalHook(ctx, deps, matchers)
		}()

		var scanner *modalScanner
		select {
		case scanner = <-scannerCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("agent %s: watcher never subscribed a scanner", agentID)
		}

		// The PTY reader side: one goroutine per agent writing production-shaped
		// modal output, exactly as pogod's tee does.
		wg.Add(1)
		go func() {
			defer wg.Done()
			filler := make([]byte, 512)
			for i := range filler {
				filler[i] = 'y'
			}
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				_, _ = scanner.Write([]byte(productionRateLimitModal))
				_, _ = scanner.Write([]byte(columnMoveRatingFooter))
				_, _ = scanner.Write(filler)
			}
		}()

		// The gate-reader side: MarkerVisible / MarkerLastSeen / LastChunk are
		// called by every dispatcher while Write is in flight. Hammering them
		// from extra goroutines widens the window a real unsynchronised read
		// would need.
		for r := 0; r < 2; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					default:
					}
					for idx := range matchers {
						if scanner.MarkerVisible(idx) {
							readerHits.Add(1)
						}
						_ = scanner.MarkerLastSeen(idx)
						_ = scanner.LastChunk()
					}
				}
			}()
		}

		// The events-log side: ingest parses appended lines on pogod's Follow
		// goroutine while the gates call LastSeen. Keep the agent wedged (all
		// ingested timestamps stay at the 21m-stale point) so the fire path
		// stays reachable for the whole run.
		wg.Add(1)
		go func() {
			defer wg.Done()
			line, err := json.Marshal(events.Event{
				SchemaVersion: 1,
				Timestamp:     wedgedAt.UTC().Format(time.RFC3339Nano),
				EventType:     "agent_output",
				Agent:         agentID,
				Details:       map[string]any{},
			})
			if err != nil {
				return
			}
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				tracker.ingest(line)
				_ = tracker.LastSeen(agentID)
				tracker.Touch(agentID, wedgedAt)
			}
		}()
	}

	time.Sleep(duration)
	cancel()
	wg.Wait()

	// Prove the control drove the paths it claims to cover. Without these, a
	// watcher that silently idled — never firing, never evaluating a gate —
	// would report "no race found" while having tested nothing.
	if got := dismissals.Load(); got == 0 {
		t.Errorf("control executed no dismissals: the production fire path was never reached")
	}
	if got := hits.Load(); got == 0 {
		t.Errorf("control executed no usage-limit hits: the suspected-hit stage was never reached")
	}
	if got := emitted.Load(); got == 0 {
		t.Errorf("control emitted no events: fireDismissal/emitUsageLimitHit were never reached")
	}
	if got := readerHits.Load(); got == 0 {
		t.Errorf("control never observed a visible marker: scanner readers raced nothing")
	}
	if got := esink.count(); got == 0 {
		t.Errorf("control never reached the fleet coordinator's episode path")
	}
}
