package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// readSourceFile reads a file from this package's own directory so a test can
// assert a structural property of the source itself.
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// stripGoComments removes // line comments and /* */ block comments so a
// structural assertion about code is not defeated (or satisfied) by prose. It
// is deliberately naive — it does not understand string literals — which is
// fine for the files it is pointed at.
func stripGoComments(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				return b.String()
			}
			i += j
		case strings.HasPrefix(src[i:], "/*"):
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return b.String()
			}
			i += 2 + j + 2
		default:
			b.WriteByte(src[i])
			i++
		}
	}
	return b.String()
}

// fakeDoneReg is a doneReapRegistry backed by a static snapshot, recording Stop
// calls. It models the two facts the reaper reads and nothing else — which is
// the point of the narrow interface.
type fakeDoneReg struct {
	mu       sync.Mutex
	live     []agent.PolecatActivity
	stopped  []string
	stopErr  map[string]error
	snapshot int // how many times PolecatActivityAt was called
	// beforeSnapshot, when set, runs at the top of PolecatActivityAt. Used to
	// drive the reentrancy control from inside a Check.
	beforeSnapshot func()
}

func (f *fakeDoneReg) PolecatActivityAt(now time.Time) []agent.PolecatActivity {
	if f.beforeSnapshot != nil {
		f.beforeSnapshot()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot++
	out := make([]agent.PolecatActivity, len(f.live))
	copy(out, f.live)
	return out
}

func (f *fakeDoneReg) Stop(name string, timeout time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, name)
	if err, ok := f.stopErr[name]; ok {
		return err
	}
	// Model the real registry: a stopped polecat leaves the live set, so a
	// later snapshot does not see it again.
	kept := f.live[:0]
	for _, p := range f.live {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	f.live = kept
	return nil
}

func (f *fakeDoneReg) stops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.stopped))
	copy(out, f.stopped)
	return out
}

// doneStore models `mg show --json` over a fixed status map. Anything not in
// the map errors, as `mg show` does for an unknown id.
func doneStore(status map[string]string) func(string) (bool, error) {
	return func(id string) (bool, error) {
		s, ok := status[id]
		if !ok {
			return false, fmt.Errorf("mg show %s failed: no such work item", id)
		}
		return s == "done" || s == "archived", nil
	}
}

// TestDoneReapBothArms is the acceptance control for mg-56d1, and it must pass
// in BOTH directions or the mechanism is worse than the manual sweep it
// replaces.
//
// POSITIVE ARM: a non-merge polecat that called `mg done` and went quiet is
// stopped, so its slot is freed. This is the d764 case measured on 2026-07-30 —
// triage delivered, packet mailed, successor filed, item done, 7m16s idle, one
// of five slots held with high-priority work queued.
//
// NEGATIVE ARM: a polecat that is merely IDLE with its item still `claimed` is
// NOT stopped, no matter how long it has been quiet. This is the e9ee case the
// same night — healthy, mid-work, 42 minutes in. A mechanism that cannot tell
// these apart kills working agents, so the two are asserted from a single
// Check, against a single snapshot, to make the discrimination structural
// rather than a property of how the test was set up.
func TestDoneReapBothArms(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		// d764: item done, long idle — must be reaped.
		{Name: "d764", WorkItemID: "mg-d764", IdleFor: 7*time.Minute + 16*time.Second, HasOutput: true},
		// e9ee: item claimed, MUCH longer idle — must survive.
		{Name: "e9ee", WorkItemID: "mg-e9ee", IdleFor: 42 * time.Minute, HasOutput: true},
	}}
	r := newDoneReaper(reg, doneStore(map[string]string{
		"mg-d764": "done",
		"mg-e9ee": "claimed",
	}), 2*time.Minute)

	stopped := r.Check(time.Now())

	if len(stopped) != 1 || stopped[0] != "d764" {
		t.Fatalf("positive arm: want exactly [d764] stopped, got %v", stopped)
	}
	for _, name := range reg.stops() {
		if name == "e9ee" {
			t.Fatalf("negative arm: e9ee was stopped — a 42-minute idle polecat with a CLAIMED item is healthy mid-work, "+
				"and reaping it is the failure this control exists to catch (stops=%v)", reg.stops())
		}
	}
	// The freed slot is the deliverable, not the Stop call: assert d764 is gone
	// from the live set the dispatcher's concurrency count is derived from.
	for _, p := range reg.PolecatActivityAt(time.Now()) {
		if p.Name == "d764" {
			t.Fatalf("d764 still live after being stopped — its slot was not freed")
		}
	}
}

// TestDoneReapNegativeArmIsItemState pins WHY the negative arm holds: the gate
// is the item's state, and no amount of idling substitutes for it. Ratchets
// against a future "reap anything idle long enough" simplification.
func TestDoneReapNegativeArmIsItemState(t *testing.T) {
	for _, idle := range []time.Duration{3 * time.Minute, time.Hour, 24 * time.Hour} {
		reg := &fakeDoneReg{live: []agent.PolecatActivity{
			{Name: "e9ee", WorkItemID: "mg-e9ee", IdleFor: idle, HasOutput: true},
		}}
		r := newDoneReaper(reg, doneStore(map[string]string{"mg-e9ee": "claimed"}), time.Minute)
		if got := r.Check(time.Now()); len(got) != 0 {
			t.Fatalf("idle=%s: a claimed item must never be reaped, got %v", idle, got)
		}
	}
}

// TestDoneReapGraceProtectsPostDoneWork is the other half of the safety
// argument. A polecat legitimately works AFTER `mg done` — mailing its verdict
// packet, filing a successor — and the polecat protocol explicitly tells it to
// stay alive. Stopping on the `done` transition alone kills it mid-sentence, so
// a done polecat still producing PTY output must survive.
func TestDoneReapGraceProtectsPostDoneWork(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		// Done, but wrote 5 seconds ago — mid-mail.
		{Name: "busy", WorkItemID: "mg-busy", IdleFor: 5 * time.Second, HasOutput: true},
		// Done and quiet past the grace.
		{Name: "quiet", WorkItemID: "mg-quiet", IdleFor: 3 * time.Minute, HasOutput: true},
	}}
	r := newDoneReaper(reg, doneStore(map[string]string{
		"mg-busy":  "done",
		"mg-quiet": "done",
	}), 2*time.Minute)

	stopped := r.Check(time.Now())
	if len(stopped) != 1 || stopped[0] != "quiet" {
		t.Fatalf("want only the quiet polecat reaped, got %v", stopped)
	}
}

// TestDoneReapArchivedCountsAsTerminal — the coordinator archives an item right
// after it goes done, and the polecat is just as finished either way. Guards
// against a reaper that stops working the moment the mayor tidies up.
func TestDoneReapArchivedCountsAsTerminal(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "arch", WorkItemID: "mg-arch", IdleFor: 10 * time.Minute, HasOutput: true},
	}}
	r := newDoneReaper(reg, doneStore(map[string]string{"mg-arch": "archived"}), time.Minute)
	if got := r.Check(time.Now()); len(got) != 1 || got[0] != "arch" {
		t.Fatalf("want [arch] reaped, got %v", got)
	}
}

// TestDoneReapUnreadableItemLeavesPolecatRunning: a store we could not read is
// not evidence of completion. Same direction as resolvePostMergeWork's
// probe-failure handling, and for the same reason — the expensive mistake is
// asserting a completion we have no standing to assert.
func TestDoneReapUnreadableItemLeavesPolecatRunning(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "unk", WorkItemID: "mg-unk", IdleFor: time.Hour, HasOutput: true},
	}}
	r := newDoneReaper(reg, func(string) (bool, error) {
		return false, errors.New("store unreadable")
	}, time.Minute)
	if got := r.Check(time.Now()); len(got) != 0 {
		t.Fatalf("an unreadable item must not license a stop, got %v", got)
	}
	if len(reg.stops()) != 0 {
		t.Fatalf("Stop was called despite an unreadable item: %v", reg.stops())
	}
}

// TestDoneReapSkipsPolecatWithNoOutput: a polecat that has never written to its
// PTY has an UNMEASURABLE idle time, not a zero one — it may be seconds into
// spawn, or wedged before its first turn (mg-ce61). It must not be reaped on the
// strength of a zero, and the store must not even be consulted for it (the
// cheap local gate runs first).
func TestDoneReapSkipsPolecatWithNoOutput(t *testing.T) {
	probed := 0
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "fresh", WorkItemID: "mg-fresh", HasOutput: false},
	}}
	r := newDoneReaper(reg, func(string) (bool, error) { probed++; return true, nil }, time.Minute)
	if got := r.Check(time.Now()); len(got) != 0 {
		t.Fatalf("a polecat with no PTY output must not be reaped, got %v", got)
	}
	if probed != 0 {
		t.Fatalf("the store was probed %d times for a polecat the local gate already refused", probed)
	}
}

// TestDoneReapSkipsPolecatWithNoWorkItem: nothing to ask, so nothing to
// conclude.
func TestDoneReapSkipsPolecatWithNoWorkItem(t *testing.T) {
	probed := 0
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "loose", IdleFor: time.Hour, HasOutput: true},
	}}
	r := newDoneReaper(reg, func(string) (bool, error) { probed++; return true, nil }, time.Minute)
	if got := r.Check(time.Now()); len(got) != 0 {
		t.Fatalf("a polecat with no work item must not be reaped, got %v", got)
	}
	if probed != 0 {
		t.Fatalf("the store was probed %d times with an empty id", probed)
	}
}

// TestDoneReapStopFailureIsNotReported: losing the race with a clean exit
// surfaces as a Stop error ("agent not found"). It must not be counted as a
// reap, because the caller's count is what a test — and a future metric —
// reads as "slots freed by this mechanism".
func TestDoneReapStopFailureIsNotReported(t *testing.T) {
	reg := &fakeDoneReg{
		live:    []agent.PolecatActivity{{Name: "gone", WorkItemID: "mg-gone", IdleFor: time.Hour, HasOutput: true}},
		stopErr: map[string]error{"gone": errors.New("agent \"gone\" not found")},
	}
	r := newDoneReaper(reg, doneStore(map[string]string{"mg-gone": "done"}), time.Minute)
	if got := r.Check(time.Now()); len(got) != 0 {
		t.Fatalf("a failed Stop must not be reported as a reap, got %v", got)
	}
	if len(reg.stops()) != 1 {
		t.Fatalf("Stop should still have been attempted once, got %v", reg.stops())
	}
}

// TestDoneReapCheckIsSerialised: the heartbeat dispatches Check in a goroutine
// every ~30s while a Check can block on a slow store, so two can overlap.
// Without the guard the second re-decides against a polecat the first is
// already stopping and issues a duplicate Stop.
func TestDoneReapCheckIsSerialised(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "dup", WorkItemID: "mg-dup", IdleFor: time.Hour, HasOutput: true},
	}}
	var r *doneReaper
	var inner []string
	// Re-enter Check from inside the first Check's snapshot — the tightest
	// possible overlap, and deterministic.
	reg.beforeSnapshot = func() {
		if reg.snapshot == 0 {
			inner = r.Check(time.Now())
		}
	}
	r = newDoneReaper(reg, doneStore(map[string]string{"mg-dup": "done"}), time.Minute)

	outer := r.Check(time.Now())
	if len(inner) != 0 {
		t.Fatalf("the reentrant Check should have stood down, got %v", inner)
	}
	if len(outer) != 1 || outer[0] != "dup" {
		t.Fatalf("the outer Check should have reaped dup, got %v", outer)
	}
	if got := reg.stops(); len(got) != 1 {
		t.Fatalf("want exactly one Stop across overlapping Checks, got %v", got)
	}
	// And a later, non-overlapping Check still works — the guard released.
	reg.beforeSnapshot = nil
	if got := r.Check(time.Now()); len(got) != 0 {
		t.Fatalf("nothing left to reap, got %v", got)
	}
}

// TestDoneReapNilSafety: a daemon whose registry failed to load wires no
// reaper, and the tick must not panic on it.
func TestDoneReapNilSafety(t *testing.T) {
	var r *doneReaper
	if got := r.Check(time.Now()); got != nil {
		t.Fatalf("nil reaper should be inert, got %v", got)
	}
	if got := (&doneReaper{}).Check(time.Now()); got != nil {
		t.Fatalf("reaper with no registry should be inert, got %v", got)
	}
}

// TestDoneReapDefaultGrace: a zero grace means the compiled default, never
// "stop immediately". Getting this backwards would stop a done polecat the
// instant it is seen — mid-mail if that is where it happens to be.
func TestDoneReapDefaultGrace(t *testing.T) {
	r := newDoneReaper(&fakeDoneReg{}, func(string) (bool, error) { return true, nil }, 0)
	if r.grace != doneReapIdleGrace {
		t.Fatalf("zero grace should fall back to %s, got %s", doneReapIdleGrace, r.grace)
	}
	if r.grace <= 0 {
		t.Fatalf("the default grace must be positive, got %s", r.grace)
	}
}

// TestDoneReapKeysOnDoneNotMerge is a documentation ratchet. The whole point of
// this ticket is that `done` is the general condition and merge is one path to
// it, so the reaper must not have acquired any dependency on the refinery. If
// this file ever imports it, the extension it was told not to make has been
// made.
func TestDoneReapKeysOnDoneNotMerge(t *testing.T) {
	// Comments are stripped first: this file's own rationale NAMES the merge hook
	// at length, and it should — the ratchet is about what the code depends on,
	// not about what the prose is allowed to mention.
	src := stripGoComments(readSourceFile(t, "donereap.go"))
	if strings.Contains(src, "internal/refinery") {
		t.Fatal("donereap.go imports internal/refinery — the reap must key on the work item reaching done, " +
			"not on a merge; merge already has its own hook in reap.go (mg-56d1)")
	}
	if strings.Contains(src, "refinery.") {
		t.Fatal("donereap.go uses a refinery type — it must decide from the work item's state alone (mg-56d1)")
	}
}
