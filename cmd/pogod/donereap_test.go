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
	mu      sync.Mutex
	live    []agent.PolecatActivity
	stopped []string
	// stopCauses is the cause argument of each StopWithCause call, positionally
	// aligned with stopped (mg-a95f).
	stopCauses []string
	stopErr    map[string]error
	snapshot   int // how many times PolecatActivityAt was called
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

func (f *fakeDoneReg) StopWithCause(name string, timeout time.Duration, cause string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, name)
	f.stopCauses = append(f.stopCauses, cause)
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

// noReviews is the `reviews:` probe for every test that predates the review
// exemption: no item declares a review, so the mg-aaf6 gate never applies and
// each of those tests still asserts the original conjunction on its own.
func noReviews(string) (string, error) { return "", nil }

// reviewsStore models `mg show --json` + workitem.ParseCarrier over a fixed map
// of item id -> the build item its `reviews:` carrier line names. An item absent
// from the map declares nothing, which is the ordinary case for every item that
// is not a gh-issue review ticket.
func reviewsStore(declared map[string]string) func(string) (string, error) {
	return func(id string) (string, error) { return declared[id], nil }
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
	}), noReviews, 2*time.Minute)

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
		r := newDoneReaper(reg, doneStore(map[string]string{"mg-e9ee": "claimed"}), noReviews, time.Minute)
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
	}), noReviews, 2*time.Minute)

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
	r := newDoneReaper(reg, doneStore(map[string]string{"mg-arch": "archived"}), noReviews, time.Minute)
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
	}, noReviews, time.Minute)
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
	r := newDoneReaper(reg, func(string) (bool, error) { probed++; return true, nil }, noReviews, time.Minute)
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
	r := newDoneReaper(reg, func(string) (bool, error) { probed++; return true, nil }, noReviews, time.Minute)
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
	r := newDoneReaper(reg, doneStore(map[string]string{"mg-gone": "done"}), noReviews, time.Minute)
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
	r = newDoneReaper(reg, doneStore(map[string]string{"mg-dup": "done"}), noReviews, time.Minute)

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
	r := newDoneReaper(&fakeDoneReg{}, func(string) (bool, error) { return true, nil }, noReviews, 0)
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

// TestDoneReapReviewExemptionFiresAndExpires is the acceptance control for
// mg-aaf6 (drellem2/pogo#131), and it is deliberately ONE test rather than two.
//
// The bar the ticket sets is that the guard be OBSERVED doing both things: a
// builder whose item is done must SURVIVE while its reviewer runs, and must be
// REAPED once the reviewer is gone. Split across two tests, each half can pass
// for the wrong reason — a guard wired to "never reap" passes the first, a guard
// that does nothing at all passes the second — and neither test can tell. Run in
// sequence against the same reaper, the pair is only satisfiable by a guard that
// actually keys on the reviewer's liveness.
//
// The shape is the real one: mg-aaf6 is the build item, mg-1c60 the review item
// filed against it, and mg-1c60's body carries `reviews: mg-aaf6` as a leading
// carrier line written once when the coordinator filed it.
func TestDoneReapReviewExemptionFiresAndExpires(t *testing.T) {
	const (
		buildItem  = "mg-aaf6"
		reviewItem = "mg-1c60"
	)
	builder := agent.PolecatActivity{Name: "paaf6", WorkItemID: buildItem, IdleFor: 9 * time.Minute, HasOutput: true}
	reviewer := agent.PolecatActivity{Name: "p1c60", WorkItemID: reviewItem, IdleFor: 30 * time.Second, HasOutput: true}

	reg := &fakeDoneReg{live: []agent.PolecatActivity{builder, reviewer}}
	r := newDoneReaper(reg,
		// The builder self-closed at PR-open — the gh#131 mistake. Its item reads
		// terminal and it has been quiet for 9 minutes, which is the MEDIAN real
		// between-round wait (8.3m) and far past the 2m grace. Everything the
		// pre-mg-aaf6 reaper looks at says "reap this".
		doneStore(map[string]string{buildItem: "done", reviewItem: "claimed"}),
		reviewsStore(map[string]string{reviewItem: buildItem}),
		2*time.Minute)

	// ── FIRES ──────────────────────────────────────────────────────────────────
	if stopped := r.Check(time.Now()); len(stopped) != 0 {
		t.Fatalf("the exemption did not fire: builder %s was reaped while review item %s was still being worked "+
			"by a live polecat — this is gh#131 reproduced, and the reviewer now has no counterparty (stopped=%v)",
			builder.Name, reviewItem, stopped)
	}
	if got := r.exempt[builder.Name]; got != reviewItem {
		t.Fatalf("the exemption fired without a positive record: want exempt[%s] == %q, got %q. "+
			"A guard whose only evidence is an absence cannot be told from a guard that never ran (mg-aaf6)",
			builder.Name, reviewItem, got)
	}
	// Repeat ticks must not change the answer — a review runs for many heartbeats.
	for i := 0; i < 3; i++ {
		if stopped := r.Check(time.Now()); len(stopped) != 0 {
			t.Fatalf("tick %d reaped the builder despite an unchanged live set: %v", i+2, stopped)
		}
	}

	// ── EXPIRES ────────────────────────────────────────────────────────────────
	// The verdict lands, the coordinator stops the review polecat. Nothing is
	// cleared, edited, or remembered by anyone: the reviewer simply is not in the
	// live set any more, and that is the whole of the expiry mechanism.
	reg.mu.Lock()
	reg.live = []agent.PolecatActivity{builder}
	reg.mu.Unlock()

	stopped := r.Check(time.Now())
	if len(stopped) != 1 || stopped[0] != builder.Name {
		t.Fatalf("the exemption did not expire: want [%s] reaped once its reviewer is gone, got %v. "+
			"An exemption that outlives its reviewer is mg-56d1's slot leak returning through the fix for it",
			builder.Name, stopped)
	}
	if _, held := r.exempt[builder.Name]; held {
		t.Fatalf("the reaped builder is still recorded as exempt — the record must not outlive the polecat it names")
	}
}

// TestDoneReapExemptionRequiresTheDeclarationToNameThisItem pins WHAT the
// exemption keys on. A live review polecat is not by itself a reason to spare
// every done builder in the fleet: two gh-issue tracks run concurrently all the
// time, and a guard that exempted on "some reviewer is alive" would hold every
// finished polecat on the box.
func TestDoneReapExemptionRequiresTheDeclarationToNameThisItem(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		// Done builder on track A.
		{Name: "pa", WorkItemID: "mg-aaaa", IdleFor: 10 * time.Minute, HasOutput: true},
		// Live reviewer on track B — it reviews mg-bbbb, not mg-aaaa.
		{Name: "pb", WorkItemID: "mg-bbb1", IdleFor: time.Second, HasOutput: true},
	}}
	r := newDoneReaper(reg,
		doneStore(map[string]string{"mg-aaaa": "done", "mg-bbb1": "claimed"}),
		reviewsStore(map[string]string{"mg-bbb1": "mg-bbbb"}),
		time.Minute)

	if got := r.Check(time.Now()); len(got) != 1 || got[0] != "pa" {
		t.Fatalf("a reviewer of a DIFFERENT build item must not exempt this one: want [pa] reaped, got %v", got)
	}
}

// TestDoneReapExemptionIsNotGrantedByADeadReviewer is the same discrimination
// from the other side: the declaration exists and names this builder, but no
// polecat is running the review item. That is the state five minutes after the
// coordinator stops the reviewer, and it must reap — the `reviews:` line is
// NEVER cleared, so if the line alone were sufficient the builder would be
// exempt forever and the slot would leak permanently.
func TestDoneReapExemptionIsNotGrantedByADeadReviewer(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "paaf6", WorkItemID: "mg-aaf6", IdleFor: 10 * time.Minute, HasOutput: true},
	}}
	r := newDoneReaper(reg,
		doneStore(map[string]string{"mg-aaf6": "done"}),
		// The review item still declares `reviews: mg-aaf6` — it always will — but
		// it has no live polecat, so it contributes no exemption.
		reviewsStore(map[string]string{"mg-1c60": "mg-aaf6"}),
		time.Minute)

	if got := r.Check(time.Now()); len(got) != 1 || got[0] != "paaf6" {
		t.Fatalf("a declaration with no LIVE reviewer must not exempt: want [paaf6] reaped, got %v. "+
			"The line is never cleared, so liveness is the only thing bounding this guard (mg-aaf6)", got)
	}
}

// TestDoneReapExemptionSurvivesTheReviewersOwnCompletion. The reviewer calls
// `mg done` on its review ticket and then stays alive to mail its verdict — the
// polecat protocol tells it to. The builder must still be exempt in that window,
// because the round is not over until the reviewer's process is: a reviewer
// mid-verdict can still need its counterparty. The exemption is keyed on the
// polecat being ALIVE, not on its item being open, and this pins that.
func TestDoneReapExemptionSurvivesTheReviewersOwnCompletion(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "paaf6", WorkItemID: "mg-aaf6", IdleFor: 10 * time.Minute, HasOutput: true},
		// Reviewer's own item is done, but it is still typing its verdict mail.
		{Name: "p1c60", WorkItemID: "mg-1c60", IdleFor: 5 * time.Second, HasOutput: true},
	}}
	r := newDoneReaper(reg,
		doneStore(map[string]string{"mg-aaf6": "done", "mg-1c60": "done"}),
		reviewsStore(map[string]string{"mg-1c60": "mg-aaf6"}),
		time.Minute)

	if got := r.Check(time.Now()); len(got) != 0 {
		t.Fatalf("want nothing reaped: the builder is exempt while its reviewer lives, and the reviewer itself "+
			"is inside the grace window mid-verdict; got %v", got)
	}
}

// TestDoneReapExemptionProbeErrorIsFailOpenAndLogged. An unreadable `reviews:`
// probe — a store that will not answer, or a carrier block out of the parser's
// reach — cannot manufacture an exemption: we will not hold a builder against a
// declaration nobody has seen. The direction is fail-OPEN and it is asserted
// here rather than left to be discovered, because it is the one case where this
// guard's absence is invisible in the outcome and visible only in the log.
func TestDoneReapExemptionProbeErrorIsFailOpenAndLogged(t *testing.T) {
	logged := captureLog(t)

	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "paaf6", WorkItemID: "mg-aaf6", IdleFor: 10 * time.Minute, HasOutput: true},
		{Name: "p1c60", WorkItemID: "mg-1c60", IdleFor: time.Second, HasOutput: true},
	}}
	r := newDoneReaper(reg,
		doneStore(map[string]string{"mg-aaf6": "done", "mg-1c60": "claimed"}),
		func(id string) (string, error) {
			return "", errors.New("carrier block is out of the parser's reach")
		},
		time.Minute)

	if got := r.Check(time.Now()); len(got) != 1 || got[0] != "paaf6" {
		t.Fatalf("an unreadable declaration must not be read as an exemption: want [paaf6] reaped, got %v", got)
	}
	if !strings.Contains(logged(), "mg-1c60") {
		t.Fatalf("the probe failure was not logged — a guard that could not read its input must SAY so, "+
			"or it is indistinguishable from a guard correctly not firing (log=%q)", logged())
	}
}

// TestDoneReapExemptionCostsNoProbeWhenNothingIsTerminal. The `reviews:` lookup
// is an `mg show` per live polecat, and on the overwhelming majority of ticks
// nothing is done, so there is nobody it could exempt. Resolving it anyway would
// multiply this reaper's store traffic by the fleet size for no decision.
func TestDoneReapExemptionCostsNoProbeWhenNothingIsTerminal(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "a", WorkItemID: "mg-a", IdleFor: time.Hour, HasOutput: true},
		{Name: "b", WorkItemID: "mg-b", IdleFor: time.Hour, HasOutput: true},
	}}
	probes := 0
	r := newDoneReaper(reg,
		doneStore(map[string]string{"mg-a": "claimed", "mg-b": "claimed"}),
		func(string) (string, error) { probes++; return "", nil },
		time.Minute)

	if got := r.Check(time.Now()); len(got) != 0 {
		t.Fatalf("claimed items must not be reaped, got %v", got)
	}
	if probes != 0 {
		t.Fatalf("the review probe ran %d times with nothing terminal to exempt — it must be resolved lazily", probes)
	}
}

// TestDoneReapExemptionResolvedOncePerCheck. Once a candidate appears the map is
// built, and it must be built ONCE for the whole tick rather than per candidate:
// a fleet finishing together would otherwise issue a quadratic number of `mg
// show` calls against a store this reaper already polls on every heartbeat.
func TestDoneReapExemptionResolvedOncePerCheck(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "a", WorkItemID: "mg-a", IdleFor: time.Hour, HasOutput: true},
		{Name: "b", WorkItemID: "mg-b", IdleFor: time.Hour, HasOutput: true},
		{Name: "c", WorkItemID: "mg-c", IdleFor: time.Hour, HasOutput: true},
	}}
	probes := 0
	r := newDoneReaper(reg,
		doneStore(map[string]string{"mg-a": "done", "mg-b": "done", "mg-c": "done"}),
		func(string) (string, error) { probes++; return "", nil },
		time.Minute)

	r.Check(time.Now())
	if probes != 3 {
		t.Fatalf("want one review probe per live polecat per Check (3), got %d — the map is being rebuilt per candidate", probes)
	}
}

// TestDoneReapSelfReferenceIsNotAnExemption. `reviews: <its own id>` would
// exempt a polecat from its own completion forever, and forever is the one
// duration this guard's design rules out. The coordinator never writes it; it is
// refused here so that a typo cannot.
func TestDoneReapSelfReferenceIsNotAnExemption(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "self", WorkItemID: "mg-self", IdleFor: time.Hour, HasOutput: true},
	}}
	r := newDoneReaper(reg,
		doneStore(map[string]string{"mg-self": "done"}),
		reviewsStore(map[string]string{"mg-self": "mg-self"}),
		time.Minute)

	if got := r.Check(time.Now()); len(got) != 1 || got[0] != "self" {
		t.Fatalf("a self-referential declaration must not exempt: want [self] reaped, got %v", got)
	}
}

// TestDoneReapNilReviewProbeKeepsThePreMgAaf6Behaviour. A nil probe disables the
// exemption rather than panicking — the honest degraded mode for a daemon whose
// probe did not wire up. It is fail-OPEN, which is why the wiring test pins the
// real probe into main.go separately.
func TestDoneReapNilReviewProbeKeepsThePreMgAaf6Behaviour(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "paaf6", WorkItemID: "mg-aaf6", IdleFor: time.Hour, HasOutput: true},
	}}
	r := newDoneReaper(reg, doneStore(map[string]string{"mg-aaf6": "done"}), nil, time.Minute)
	if got := r.Check(time.Now()); len(got) != 1 || got[0] != "paaf6" {
		t.Fatalf("a nil review probe must fall back to the pre-mg-aaf6 conjunction, got %v", got)
	}
}

// TestDoneReapLapseIsLoggedExactlyOnceWhenStopKeepsFailing pins round-1
// advisory 3 on PR #133. The expiry line is half of this guard's positive
// record, and a record that repeats every 30 seconds is one nobody reads.
//
// A polecat whose Stop keeps failing stays in the live set, so it is re-decided
// on every tick. The lapse must be announced on the first of those and not
// again: the exemption ended when the reviewer did, which is a fact about the
// reviewer rather than about the stop succeeding.
func TestDoneReapLapseIsLoggedExactlyOnceWhenStopKeepsFailing(t *testing.T) {
	logged := captureLog(t)

	builder := agent.PolecatActivity{Name: "paaf6", WorkItemID: "mg-aaf6", IdleFor: 10 * time.Minute, HasOutput: true}
	reviewer := agent.PolecatActivity{Name: "p1c60", WorkItemID: "mg-1c60", IdleFor: time.Second, HasOutput: true}
	reg := &fakeDoneReg{
		live:    []agent.PolecatActivity{builder, reviewer},
		stopErr: map[string]error{"paaf6": errors.New("stop keeps failing")},
	}
	r := newDoneReaper(reg,
		doneStore(map[string]string{"mg-aaf6": "done", "mg-1c60": "claimed"}),
		reviewsStore(map[string]string{"mg-1c60": "mg-aaf6"}),
		time.Minute)

	if got := r.Check(time.Now()); len(got) != 0 {
		t.Fatalf("precondition: the exemption should hold while the reviewer runs, got %v", got)
	}

	// Reviewer gone; the builder's Stop will fail on every attempt from here.
	reg.mu.Lock()
	reg.live = []agent.PolecatActivity{builder}
	reg.mu.Unlock()

	for i := 0; i < 4; i++ {
		r.Check(time.Now())
	}
	if n := strings.Count(logged(), "has LAPSED"); n != 1 {
		t.Errorf("the expiry was announced %d times across 4 ticks with a failing Stop, want exactly 1 — "+
			"a positive record that repeats is one nobody reads (log=%q)", n, logged())
	}
	// And the reaper must keep TRYING; suppressing the log must not suppress the stop.
	if n := len(reg.stops()); n < 4 {
		t.Errorf("Stop was attempted %d times across 4 ticks, want one per tick — clearing the exemption "+
			"record must not stop the reaper retrying", n)
	}
}
