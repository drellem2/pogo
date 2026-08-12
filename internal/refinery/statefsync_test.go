package refinery

import (
	"path/filepath"
	"testing"
	"time"
)

// probeQueueReadable reports whether the reader `pogo refinery queue` serves
// (Refinery.QueueWithProcessing, via internal/refinery/api.go handleQueue)
// answers within `within`. It is the whole measuring instrument for the tests
// below, used in BOTH polarities: it must say "readable" while a state write
// is in flight, and "blocked" while Refinery.mu is genuinely held.
func probeQueueReadable(r *Refinery, within time.Duration) bool {
	answered := make(chan struct{})
	go func() {
		_ = r.QueueWithProcessing()
		close(answered)
	}()
	select {
	case <-answered:
		return true
	case <-time.After(within):
		return false
	}
}

// TestProbeDetectsAHeldRefineryMutex is the negative control for
// TestStateWriteDoesNotHoldRefineryMutex, and it exists because the positive
// test alone proves nothing: a probe that could never report "blocked" would
// pass whether or not the fsync moved.
//
// It holds Refinery.mu directly — the exact condition the repair claims to
// have removed from the persist path — and requires the probe to notice.
func TestProbeDetectsAHeldRefineryMutex(t *testing.T) {
	r := newPersistent(t, filepath.Join(t.TempDir(), "refinery-state.json"))

	r.mu.Lock()
	blocked := !probeQueueReadable(r, 750*time.Millisecond)
	r.mu.Unlock()

	if !blocked {
		t.Fatal("the probe answered while Refinery.mu was held: it cannot detect the condition " +
			"TestStateWriteDoesNotHoldRefineryMutex claims to rule out, so that test is vacuous")
	}
	// And it recovers, so a "blocked" reading is about the lock and not about
	// the probe being broken.
	if !probeQueueReadable(r, 5*time.Second) {
		t.Fatal("the probe did not answer with Refinery.mu free")
	}
}

// TestStateWriteDoesNotHoldRefineryMutex is the mg-538e regression.
//
// The defect: store.save ran json.MarshalIndent + write + tmp.Sync() (fsync) +
// rename, and every caller invoked it as saveStateLocked() with Refinery.mu
// held. An fsync inside a mutex is an unbounded hold — its duration is set by
// disk contention, not by the refinery — and `pogo refinery queue` needs that
// same mutex. That is why it could hang while `pogo agent list`, a different
// mutex on a different object, answered in 0s.
//
// This asserts the PROPERTY, not the implementation: while a state write is
// parked mid-write, the reader handleQueue uses must still answer. Any repair
// that fsyncs under some other lock handleQueue needs fails this the same way
// the original did — which is the trap mg-6ea3 named ("a remedy is subject to
// the defect it repairs").
func TestStateWriteDoesNotHoldRefineryMutex(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	seedBranch(t, originDir, "feature-1")
	r := newPersistent(t, filepath.Join(t.TempDir(), "refinery-state.json"))

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	// Stands in for a slow fsync. A disk that takes seconds to sync is a state
	// a test cannot create; a write that parks on a channel is the same shape
	// with a bound the test controls.
	r.store.beforeWrite = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}

	submitErr := make(chan error, 1)
	go func() {
		_, err := r.Submit(MergeRequest{RepoPath: originDir, Branch: "feature-1", Author: "cat-a"})
		submitErr <- err
	}()

	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		close(release)
		t.Fatal("no state write was ever attempted — the test never reached the condition it measures")
	}

	// The write is now parked, holding whatever the persist path holds.
	readable := probeQueueReadable(r, 10*time.Second)
	close(release)

	if !readable {
		t.Fatal("QueueWithProcessing blocked while a state write was in flight: the state write still " +
			"holds a lock `pogo refinery queue` needs (mg-538e). A fix that moved the fsync under a " +
			"different lock handleQueue reaches has not fixed anything.")
	}
	if err := <-submitErr; err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

// TestNoStateMutationHoldsTheMutexAcrossItsWrite extends the property above to
// every operation that persists, not just Submit.
//
// The repair keeps r.mu off the fsync by calling flushState AFTER the unlock —
// which for the `defer` sites means registering `defer r.flushState()` BEFORE
// `r.mu.Lock()`, because defers are LIFO. Get that order wrong at one call
// site and that operation alone reinstates the exact defect, invisibly. An
// audit by inspection is what rots; this asserts it.
func TestNoStateMutationHoldsTheMutexAcrossItsWrite(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	for _, b := range []string{"feature-1", "feature-2", "feature-3", "feature-4"} {
		seedBranch(t, originDir, b)
	}

	cases := []struct {
		name string
		// setup runs BEFORE the write is armed, so the write this test parks
		// is the one `run` makes and not one its setup made.
		setup func(t *testing.T, r *Refinery) string
		run   func(t *testing.T, r *Refinery, id string)
	}{
		{
			name:  "Submit",
			setup: func(*testing.T, *Refinery) string { return "" },
			run: func(t *testing.T, r *Refinery, _ string) {
				if _, err := r.Submit(MergeRequest{RepoPath: originDir, Branch: "feature-1", Author: "cat-a"}); err != nil {
					t.Error(err)
				}
			},
		},
		{
			name:  "Cancel",
			setup: submitOne(originDir, "feature-2", "cat-b"),
			run: func(t *testing.T, r *Refinery, id string) {
				if _, err := r.Cancel(id); err != nil {
					t.Error(err)
				}
			},
		},
		{
			name:  "claimLane",
			setup: submitOne(originDir, "feature-3", "cat-c"),
			run: func(t *testing.T, r *Refinery, _ string) {
				if ln, _ := r.claimLane(nil); ln == nil {
					t.Error("claimLane took nothing")
				}
			},
		},
		{
			name:  "Stop",
			setup: submitOne(originDir, "feature-4", "cat-d"),
			run:   func(_ *testing.T, r *Refinery, _ string) { r.Stop() },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPersistent(t, filepath.Join(t.TempDir(), "refinery-state.json"))
			id := tc.setup(t, r)
			// Setup's own write is already durable, so the next write to park
			// belongs to the operation under test.
			r.flushState()

			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			r.store.beforeWrite = func() {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				tc.run(t, r, id)
			}()

			select {
			case <-entered:
			case <-time.After(30 * time.Second):
				close(release)
				<-done
				t.Fatalf("%s never persisted anything — this case measures nothing", tc.name)
			}

			readable := probeQueueReadable(r, 10*time.Second)
			close(release)
			<-done

			if !readable {
				t.Fatalf("%s held Refinery.mu across its state write — `pogo refinery queue` blocks "+
					"for the duration of the fsync. Check that flushState is called AFTER the unlock "+
					"(a `defer r.flushState()` must be registered BEFORE `defer r.mu.Unlock()`). (mg-538e)",
					tc.name)
			}
		})
	}
}

// submitOne returns a setup that queues one MR and hands back its ID.
func submitOne(repo, branch, author string) func(*testing.T, *Refinery) string {
	return func(t *testing.T, r *Refinery) string {
		t.Helper()
		id, err := r.Submit(MergeRequest{RepoPath: repo, Branch: branch, Author: author})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
}

// TestSubmitStaysWriteThroughAcrossTheAsyncWrite guards the property the
// repair could plausibly have broken. Moving the disk work off the caller's
// goroutine makes it easy to also make it asynchronous to the API — and
// TestStateSurvivesRestart's premise ("no Stop() — simulate an unclean death.
// Write-through persistence must have already captured both submits") depends
// on it not being.
//
// This asserts the same thing at the file, immediately, with no restart in
// between: when Submit returns, the bytes are on disk.
func TestSubmitStaysWriteThroughAcrossTheAsyncWrite(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	seedBranch(t, originDir, "feature-1")
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	r := newPersistent(t, statePath)

	id, err := r.Submit(MergeRequest{RepoPath: originDir, Branch: "feature-1", Author: "cat-a"})
	if err != nil {
		t.Fatal(err)
	}

	// No sleep, no poll, no flush: read the file the instant Submit returns.
	st, err := (&store{path: statePath}).load()
	if err != nil {
		t.Fatalf("load state written by Submit: %v", err)
	}
	if st == nil {
		t.Fatal("Submit returned before the state file existed — persistence is no longer write-through")
	}
	found := false
	for _, mr := range st.Queue {
		if mr != nil && mr.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("MR %s not in the state file when Submit returned — persistence is no longer write-through", id)
	}
}

// TestSavesCoalesceIntoOneWrite records a deliberate consequence of the
// hand-off: a burst of saves collapses into the newest snapshot rather than
// queueing one write each. The file holds whole state, so writing an
// intermediate version of it is pure cost — and the burst is real, the gate
// heartbeat saves on every beat.
//
// It is written as a bound (fewer writes than saves), not an exact count: the
// number that actually lands depends on how fast the writer drains.
func TestSavesCoalesceIntoOneWrite(t *testing.T) {
	s := &store{path: filepath.Join(t.TempDir(), "state.json")}

	writes := make(chan struct{}, 256)
	gate := make(chan struct{})
	s.beforeWrite = func() {
		writes <- struct{}{}
		<-gate
	}

	const saves = 20
	for i := 0; i < saves; i++ {
		s.enqueue([]byte("{}\n"))
	}
	// Let the first write park, then release everything and settle.
	select {
	case <-writes:
	case <-time.After(30 * time.Second):
		t.Fatal("no write started")
	}
	close(gate)
	if err := s.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got := len(writes) + 1 // the one already drained from the channel
	if got >= saves {
		t.Errorf("%d saves produced %d writes: they are not coalescing", saves, got)
	}
}
