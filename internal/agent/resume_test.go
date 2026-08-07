package agent

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// TestResumeClearsShutdownLatch covers the half of gh #108 that hides in plain
// sight. StopAll's latch was one-way: nothing in the tree cleared it, and
// Spawn never read it, so after a single stop-orchestration a daemon kept
// accepting `pogo agent start` while restart_on_crash was inert for every
// agent, for the life of the process. The fleet looked hand-recoverable and
// healthy until something crashed.
func TestResumeClearsShutdownLatch(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	reg.StopAll(2 * time.Second)

	// Spawn still works with the latch set — that asymmetry is why the defect
	// was easy to miss, so assert it rather than assume it.
	a, err := reg.Spawn(SpawnRequest{Name: "latch", Type: TypeCrew, Command: []string{"true"}})
	if err != nil {
		t.Fatalf("Spawn with the latch set: %v", err)
	}
	<-a.Done()

	if _, err := reg.Respawn("latch"); err == nil || !strings.Contains(err.Error(), "shut down") {
		t.Fatalf("Respawn before Resume: err = %v, want the shutdown latch to refuse it", err)
	}

	reg.Resume()

	if _, err := reg.Respawn("latch"); err != nil {
		t.Fatalf("Respawn after Resume: %v — the latch was not cleared", err)
	}
}

// TestRespawnFromGenerationLosesToARoundTrip is the race test.
//
// The interval is measured, not hypothetical: pogod's OnExit hook schedules a
// respawn that SLEEPS 2s before firing, while StopAll returns synchronously.
// A goroutine scheduled during teardown therefore fires after the drain
// returned — and if a start-orchestration lands inside that window, the
// shutdown latch has already been cleared by the time it calls. Clearing the
// latch cannot close that window; the window is on the far side of the clear.
// The generation is what closes it: a respawn belonging to the pre-stop fleet
// is refused however late it fires.
func TestRespawnFromGenerationLosesToARoundTrip(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{Name: "stale", Type: TypeCrew, Command: []string{"true"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	<-a.Done()

	// What pogod's OnExit hook captures at SCHEDULING time.
	gen := reg.Generation()

	// The stop/start round-trip the deferred respawn sleeps through.
	reg.StopAll(2 * time.Second)
	reg.Resume()

	// The goroutine finally fires. The latch is clear, so the latch alone
	// would let it through.
	if _, err := reg.RespawnFromGeneration("stale", gen); err == nil {
		t.Fatal("a respawn scheduled before a stop/start round-trip was admitted afterwards — " +
			"it would land in a fleet it does not belong to, alongside the auto-start sweep")
	} else if !strings.Contains(err.Error(), "generation") {
		t.Errorf("err = %v, want the refusal to name the generation mismatch", err)
	}

	// Sanity: the barrier is a barrier, not a wall. A respawn scheduled in the
	// CURRENT generation still works, which is the whole point of clearing the
	// latch before the sweep rather than after it.
	b, err := reg.Spawn(SpawnRequest{Name: "fresh", Type: TypeCrew, Command: []string{"true"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	<-b.Done()
	if _, err := reg.RespawnFromGeneration("fresh", reg.Generation()); err != nil {
		t.Fatalf("RespawnFromGeneration in the current generation: %v", err)
	}
}

// TestLateRespawnGoroutineDoesNotResurrectAcrossARoundTrip drives the same
// race through the real supervisor shape — an OnExit hook that captures the
// generation and fires on a delay, exactly as cmd/pogod/main.go does — rather
// than calling the guarded method directly. The assertion is on the registry:
// the agent must not be back.
//
// Note what this test does and does not isolate. It asserts the end-to-end
// property, and that property is defended twice: StopAll also drops each
// stopped agent from the map, so a stale respawn of an agent present at drain
// time hits "not found" even with the generation check removed (measured).
// TestRespawnFromGenerationLosesToARoundTrip above is the test that isolates
// the barrier itself. The barrier is kept because it makes the invariant local
// and explicit — one check, at the point of respawn — instead of emergent from
// StopAll's cleanup loop and Respawn's still-running check agreeing across two
// functions.
func TestLateRespawnGoroutineDoesNotResurrectAcrossARoundTrip(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	var wg sync.WaitGroup
	respawnErr := make(chan error, 4)
	reg.SetOnExit(func(a *Agent, _ error) {
		if !a.RestartOnCrash {
			return
		}
		gen := reg.Generation() // captured at scheduling time, as pogod does
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Stands in for pogod's 2s backoff: long enough that the
			// stop/start round-trip below completes first.
			time.Sleep(150 * time.Millisecond)
			_, rerr := reg.RespawnFromGeneration(a.Name, gen)
			respawnErr <- rerr
		}()
	})

	if _, err := reg.Spawn(SpawnRequest{
		Name:           "ghost",
		Type:           TypeCrew,
		Command:        []string{"cat"},
		RestartOnCrash: true,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	reg.StopAll(2 * time.Second) // schedules the late respawn, then returns
	reg.Resume()                 // the start-orchestration that clears the latch

	wg.Wait()

	select {
	case rerr := <-respawnErr:
		if rerr == nil {
			t.Error("the late respawn succeeded after the round-trip — a pre-stop agent " +
				"came back into the post-restart fleet")
		}
	default:
		t.Fatal("the respawn goroutine never fired; the test proved nothing")
	}
	if a := reg.Get("ghost"); a != nil {
		t.Errorf("agent %q is back in the registry after a stop/start round-trip: %+v", "ghost", a)
	}
}

// TestGenerationBumpsOnBothEdges pins the invariant the barrier rests on: the
// generation moves when the latch is SET and when it is CLEARED, so no
// scheduled respawn can straddle either edge unnoticed.
func TestGenerationBumpsOnBothEdges(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	start := reg.Generation()
	reg.StopAll(2 * time.Second)
	afterStop := reg.Generation()
	reg.Resume()
	afterResume := reg.Generation()

	if afterStop == start {
		t.Errorf("generation did not move on StopAll (%d -> %d)", start, afterStop)
	}
	if afterResume == afterStop {
		t.Errorf("generation did not move on Resume (%d -> %d)", afterStop, afterResume)
	}
}
