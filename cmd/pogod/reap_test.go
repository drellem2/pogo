package main

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// fakeReaper is a polecatReaper backed by a static agent map, recording
// Stop calls. Its GetByWorkItemOrName mirrors the real registry: a direct
// key (registry name) lookup first, then a scan by WorkItemID — so a polecat
// registered under its bare id resolves from the full work-item id an MR
// carries as its author.
type fakeReaper struct {
	agents  map[string]*agent.Agent
	stopped []string
	stopErr error

	// releaseHeld models the store: the ids whose claim is still held at exit.
	// ReleaseClaimAfterExit reports (false, nil) for anything not in here,
	// exactly as MGClaimReleaser does when claimed/ has no file for the id.
	releaseHeld map[string]bool
	released    []string
	releaseErr  error
}

func (f *fakeReaper) GetByWorkItemOrName(id string) *agent.Agent {
	if id == "" {
		return nil
	}
	if a := f.agents[id]; a != nil {
		return a
	}
	for _, a := range f.agents {
		if a.WorkItemID == id {
			return a
		}
	}
	return nil
}

func (f *fakeReaper) Stop(name string, timeout time.Duration) error {
	f.stopped = append(f.stopped, name)
	return f.stopErr
}

func (f *fakeReaper) ReleaseClaimAfterExit(a *agent.Agent) (bool, error) {
	if a == nil || a.WorkItemID == "" {
		return false, nil
	}
	f.released = append(f.released, a.WorkItemID)
	if f.releaseErr != nil {
		return false, f.releaseErr
	}
	if !f.releaseHeld[a.WorkItemID] {
		return false, nil
	}
	delete(f.releaseHeld, a.WorkItemID)
	return true, nil
}

// TestReapMergedPolecat_StopsPolecatAndMarksDone is the gh #48 regression: the
// polecat is registered under its BARE id ("1234") but authors its MR with the
// FULL work-item id ("mg-1234"). Reap must (a) resolve it via WorkItemID, (b)
// mg done the FULL id (mr.Author), and (c) Stop the BARE id (registry name).
func TestReapMergedPolecat_StopsPolecatAndMarksDone(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var completedID, completedResult string
	complete := func(id, resultJSON string) error {
		completedID = id
		completedResult = resultJSON
		return nil
	}

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234"}
	reapMergedPolecat(reg, mr, complete, postMergeVerdict{}, nil)

	if completedID != "mg-1234" {
		t.Errorf("expected mg done for work-item id mg-1234, got %q", completedID)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(completedResult), &result); err != nil {
		t.Fatalf("result sidecar is not valid JSON: %v (%q)", err, completedResult)
	}
	if result["branch"] != "polecat-mg-1234" || result["mr"] != "mr-42" || result["completed_by"] != "refinery" {
		t.Errorf("unexpected result sidecar: %q", completedResult)
	}
	// Stop must key on the registry name (bare id), not mr.Author — otherwise
	// the lookup succeeds but the stop silently misses and the polecat lingers.
	if len(reg.stopped) != 1 || reg.stopped[0] != "1234" {
		t.Errorf("expected exactly one Stop(1234) keyed on bare name, got %v", reg.stopped)
	}
}

func TestReapMergedPolecat_IgnoresEmptyAuthor(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{}}
	called := false
	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b"}, func(string, string) error {
		called = true
		return nil
	}, postMergeVerdict{}, nil)
	if called || len(reg.stopped) != 0 {
		t.Errorf("expected no action for authorless MR (complete=%v, stopped=%v)", called, reg.stopped)
	}
}

// TestReapMergedPolecat_ClosesItemWithNoLivePolecat is mg-be37, and it REPLACES
// a test that asserted the opposite.
//
// The old TestReapMergedPolecat_IgnoresUnknownAuthor pinned "pogod must not
// mg done items it can't tie to a live polecat" on the reasoning that the
// mayor's backstop owns leftover work-item state. That reasoning was measured
// and found false on 2026-08-09: no such backstop closes the item, and four
// merges (mg-51f4, mg-00b3, mg-6c90, mg-56ac) landed with their items left in
// available/. priority-wake then advertised mg-6c90 as unclaimed high-priority
// work four minutes after its branch merged as b9e1d1b with 1116 insertions on
// main — and the recommended action, dispatch, re-derives it.
//
// The window is worse than the unmerged one it grew out of: while the branch is
// unmerged the spawn-time stranded-work guard refuses the dispatch, and the
// moment it merges that guard correctly stops refusing. Nothing else was
// looking.
//
// A hand-submitted branch has no polecat by construction — that is what makes it
// hand-submitted — so requiring one was requiring the exact condition the case
// cannot satisfy.
func TestReapMergedPolecat_ClosesItemWithNoLivePolecat(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{}}
	var completedID, completedResult string
	complete := func(id, resultJSON string) error {
		completedID, completedResult = id, resultJSON
		return nil
	}

	mr := &refinery.MergeRequest{
		ID: "mr-1", Branch: "polecat-q6c90", Author: "mg-6c90",
		TargetRef: "main", MergedSHA: "b9e1d1b",
	}
	reapMergedPolecat(reg, mr, complete, postMergeVerdict{}, nil)

	if completedID != "mg-6c90" {
		t.Fatalf("completed %q, want mg-6c90: the branch merged and nothing else will ever close "+
			"this item, so it sits in available/ where priority-wake advertises it", completedID)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(completedResult), &result); err != nil {
		t.Fatalf("result sidecar is not valid JSON: %v (%q)", err, completedResult)
	}
	if result["branch"] != "polecat-q6c90" || result["merged_sha"] != "b9e1d1b" {
		t.Errorf("sidecar does not record what landed: %q", completedResult)
	}
	// There is nothing to stop, and reaching for the registry would panic on the
	// nil agent this path no longer has.
	if len(reg.stopped) != 0 {
		t.Errorf("Stop called with no polecat running: %v", reg.stopped)
	}
}

// TestReapMergedPolecat_IgnoresAuthorThatIsNotAWorkItemID is the other polarity
// of the same change, and without it the fix above is a `mg done mayor` on every
// crew merge. The completion is now gated on the author's SHAPE rather than on a
// registry hit, so the shape test has to actually exclude something.
func TestReapMergedPolecat_IgnoresAuthorThatIsNotAWorkItemID(t *testing.T) {
	for _, author := range []string{"mayor", "pm-pogo", "daniel", "architect"} {
		reg := &fakeReaper{agents: map[string]*agent.Agent{}}
		called := false
		reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: author},
			func(string, string) error { called = true; return nil }, postMergeVerdict{}, nil)
		if called {
			t.Errorf("author %q produced an `mg done %s`, which is not a completion but an error "+
				"logged on every crew merge forever", author, author)
		}
	}
}

// TestReapMergedPolecat_AuthorlessDeferredMergeDoesNotClose. The declared-work
// guards outrank the mg-be37 close: an item that says merging is a STEP must not
// be completed just because nobody is running to say so. This is the direction
// where being wrong truncates a ticket silently.
func TestReapMergedPolecat_AuthorlessDeferredMergeDoesNotClose(t *testing.T) {
	for name, mr := range map[string]*refinery.MergeRequest{
		"pr flow":    {ID: "mr-1", Branch: "b", Author: "mg-7746", PRFlow: true, TargetRef: "integration"},
		"defer done": {ID: "mr-2", Branch: "b", Author: "mg-0081", DeferDone: true},
		"post-merge step failed": {ID: "mr-3", Branch: "b", Author: "mg-6879",
			PostMergeError: "tag push rejected"},
	} {
		reg := &fakeReaper{agents: map[string]*agent.Agent{}}
		called := false
		verdict := postMergeVerdict{}
		if mr.PostMergeError != "" {
			verdict = resolvePostMergeWork(reg, mr, nil)
		}
		reapMergedPolecat(reg, mr, func(string, string) error { called = true; return nil }, verdict, nil)
		if called {
			t.Errorf("%s: the item was closed although this merge is not completion", name)
		}
	}
}

// TestResolvePostMergeWork_ProbesAuthorlessMerges. The probe used to return the
// zero verdict whenever the registry had nobody, which was harmless while
// completion also required a live polecat. It stopped being harmless the moment
// completion did not: an authorless merge would have skipped the declaration
// check entirely and closed an item that declares its own remainder.
func TestResolvePostMergeWork_ProbesAuthorlessMerges(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{}}
	probed := ""
	v := resolvePostMergeWork(reg, &refinery.MergeRequest{ID: "mr-1", Author: "mg-ca3c"},
		func(id string) (bool, error) { probed = id; return true, nil })
	if probed != "mg-ca3c" {
		t.Fatalf("the work item was not probed (probed=%q); an authorless merge would close an "+
			"item tagged post-merge-work", probed)
	}
	if !v.Declared {
		t.Errorf("Declared = false although the item declares post-merge work")
	}

	// And a crew author is still not probed: its name is not an id, and the
	// probe's failure direction (a failed lookup DECLARES work) would turn every
	// crew merge into a false declaration.
	probed = ""
	if v := resolvePostMergeWork(reg, &refinery.MergeRequest{ID: "mr-2", Author: "mayor"},
		func(id string) (bool, error) { probed = id; return false, nil }); v.Declared || probed != "" {
		t.Errorf("crew author was probed (probed=%q, declared=%v)", probed, v.Declared)
	}
}

func TestReapMergedPolecat_IgnoresCrewAuthor(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"mayor": {Name: "mayor", Type: agent.TypeCrew},
	}}
	called := false
	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mayor"}, func(string, string) error {
		called = true
		return nil
	}, postMergeVerdict{}, nil)
	if called || len(reg.stopped) != 0 {
		t.Errorf("expected no action for crew author (complete=%v, stopped=%v)", called, reg.stopped)
	}
}

func TestReapMergedPolecat_StopsEvenWhenDoneFails(t *testing.T) {
	// "Already done" (the polecat won the race) must not leave the polecat
	// running.
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	complete := func(string, string) error { return errors.New("mg done failed: already done") }

	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-1234"}, complete, postMergeVerdict{}, nil)

	if len(reg.stopped) != 1 || reg.stopped[0] != "1234" {
		t.Errorf("expected Stop(1234) despite mg done failure, got %v", reg.stopped)
	}
}

func TestReapMergedPolecat_StopFailureIsNonFatal(t *testing.T) {
	reg := &fakeReaper{
		agents: map[string]*agent.Agent{
			"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
		},
		stopErr: errors.New("agent wedged"),
	}
	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-1234"}, func(string, string) error { return nil }, postMergeVerdict{}, nil)
	// Must not panic; the mayor's backstop picks up the still-running polecat.
	if len(reg.stopped) != 1 {
		t.Errorf("expected one Stop attempt, got %v", reg.stopped)
	}
}

// --- defer-done (gh #81) ---

// fakeBackstopTimer is a hand-fired stand-in for *time.Timer. It captures the
// scheduled func so a test can invoke it deterministically (fire the deadline)
// and records whether Stop was called (the timer was disarmed).
type fakeBackstopTimer struct {
	fn      func()
	stopped bool
}

func (t *fakeBackstopTimer) Stop() bool {
	already := t.stopped
	t.stopped = true
	return !already
}

// newTestBackstop builds a deferredBackstop whose timers never fire on their
// own — the returned func fires the most recently armed one on demand. This
// lets each test drive every direction of the acceptance control without real
// wall-clock time.
//
// BOTH escalation kinds land in *escalations: the linger escalation as the bare
// work-item id, the deferred-death escalation (mg-c8d5) prefixed "death:". They
// share a slice so that a test asserting "nothing was escalated" keeps meaning
// that after a second escalation path was added — a separate recorder would
// have let the new path fire unnoticed through every existing negative control.
func newTestBackstop(reg polecatReaper, escalations *[]string) (*deferredBackstop, func()) {
	var last *fakeBackstopTimer
	b := newDeferredBackstop(15*time.Minute, reg, func(mr *refinery.MergeRequest) {
		*escalations = append(*escalations, mr.Author)
	})
	b.escalateDeath = func(mr *refinery.MergeRequest, a *agent.Agent, released bool, releaseErr error) {
		id := a.WorkItemID
		if mr != nil && mr.Author != "" {
			id = mr.Author
		}
		*escalations = append(*escalations, "death:"+id)
	}
	b.afterFunc = func(d time.Duration, f func()) backstopTimer {
		last = &fakeBackstopTimer{fn: f}
		return last
	}
	fire := func() {
		if last != nil {
			last.fn()
		}
	}
	return b, fire
}

// TestReapMergedPolecat_DeferDoneArmsBackstopAndSkipsAutoStop is the core gh
// #81 behavior: a --defer-done polecat must NOT be auto-done'd or auto-stopped
// at merge — it owns its own lifecycle — but the backstop must be armed so it
// cannot linger forever.
func TestReapMergedPolecat_DeferDoneArmsBackstopAndSkipsAutoStop(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var escalations []string
	backstop, _ := newTestBackstop(reg, &escalations)

	completeCalled := false
	complete := func(string, string) error { completeCalled = true; return nil }

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", DeferDone: true}
	reapMergedPolecat(reg, mr, complete, postMergeVerdict{}, backstop)

	if completeCalled {
		t.Error("defer-done: mg done must NOT be called at merge — the polecat calls it itself")
	}
	if len(reg.stopped) != 0 {
		t.Errorf("defer-done: polecat must NOT be auto-stopped at merge, got stopped=%v", reg.stopped)
	}
	backstop.mu.Lock()
	_, armed := backstop.timers["1234"]
	backstop.mu.Unlock()
	if !armed {
		t.Error("defer-done: backstop must be armed for the merged polecat")
	}
}

// TestDeferredBackstop_FiresReapsAndEscalates is the FIRST direction of the
// acceptance control: a deferred polecat that never completes gets reaped +
// escalated once the bounded window elapses.
func TestDeferredBackstop_FiresReapsAndEscalates(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var escalations []string
	backstop, fire := newTestBackstop(reg, &escalations)

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", DeferDone: true}
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, postMergeVerdict{}, backstop)

	// The polecat never exits: the deadline elapses.
	fire()

	if len(reg.stopped) != 1 || reg.stopped[0] != "1234" {
		t.Errorf("backstop fire: expected Stop(1234) to reap the lingering polecat, got %v", reg.stopped)
	}
	if len(escalations) != 1 || escalations[0] != "mg-1234" {
		t.Errorf("backstop fire: expected one escalation for mg-1234, got %v", escalations)
	}
	// The fired timer must be cleared so a later cancel/exit is a clean no-op.
	backstop.mu.Lock()
	_, stillArmed := backstop.timers["1234"]
	backstop.mu.Unlock()
	if stillArmed {
		t.Error("backstop fire: timer entry must be removed after firing")
	}
}

// TestDeferredBackstop_CleanCompletionDisarms is the SECOND direction of the
// acceptance control: a normal defer-done that completes cleanly (its process
// exits → OnExit calls cancel) is never reaped or escalated.
func TestDeferredBackstop_CleanCompletionDisarms(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var escalations []string
	backstop, fire := newTestBackstop(reg, &escalations)

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", DeferDone: true}
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, postMergeVerdict{}, backstop)

	// The polecat finishes its post-merge flow and its process exits — the
	// OnExit hook disarms the backstop. Its `mg done` already moved the item out
	// of claimed/, which is why reg holds no claim for it.
	backstop.cancel(reg.agents["1234"])
	// A late timer fire (already-disarmed) must do nothing.
	fire()

	if len(reg.stopped) != 0 {
		t.Errorf("clean completion: polecat must NOT be reaped, got stopped=%v", reg.stopped)
	}
	// Covers the death escalation too: newTestBackstop funnels both into
	// `escalations`, prefixed, so "no escalation" means neither kind fired.
	if len(escalations) != 0 {
		t.Errorf("clean completion: no escalation expected, got %v", escalations)
	}
}

// TestDeferredBackstop_FireAfterExitIsNoop covers the race where the polecat
// exits (leaving the registry) but the timer fires before cancel ran: with no
// live agent to reap, the backstop must not escalate a phantom linger.
func TestDeferredBackstop_FireAfterExitIsNoop(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var escalations []string
	backstop, fire := newTestBackstop(reg, &escalations)

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", DeferDone: true}
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, postMergeVerdict{}, backstop)

	// Process is gone from the registry, but cancel has not run yet.
	delete(reg.agents, "1234")
	fire()

	if len(reg.stopped) != 0 {
		t.Errorf("fire-after-exit: nothing to reap, got stopped=%v", reg.stopped)
	}
	if len(escalations) != 0 {
		t.Errorf("fire-after-exit: no escalation for an already-gone polecat, got %v", escalations)
	}
}

// TestDeferredBackstop_CompletedItemIsReapedWithoutEscalation covers mg-7746's
// consequence for PR-flow polecats: they finish their flow, call `mg done`, and
// then STAY ALIVE because their protocol tells them to wait for the mayor. At
// the deadline the process is still there, but the work is finished — reap it
// to free the slot, and do NOT report it as a polecat that never completed.
func TestDeferredBackstop_CompletedItemIsReapedWithoutEscalation(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var escalations []string
	backstop, fire := newTestBackstop(reg, &escalations)
	var probed string
	backstop.workItemDone = func(id string) (bool, error) { probed = id; return true, nil }

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "integ", PRFlow: true}
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, postMergeVerdict{}, backstop)
	fire()

	if probed != "mg-1234" {
		t.Errorf("expected the backstop to probe work item mg-1234, got %q", probed)
	}
	if len(reg.stopped) != 1 || reg.stopped[0] != "1234" {
		t.Errorf("a completed-but-lingering polecat must still be reaped to free its slot, got %v", reg.stopped)
	}
	if len(escalations) != 0 {
		t.Errorf("no escalation for a polecat whose work item is done, got %v", escalations)
	}
}

// TestDeferredBackstop_ProbeErrorEscalatesConservatively: an unreadable work
// item is "unknown", not "finished". The escalation must still go out.
func TestDeferredBackstop_ProbeErrorEscalatesConservatively(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var escalations []string
	backstop, fire := newTestBackstop(reg, &escalations)
	backstop.workItemDone = func(string) (bool, error) { return false, errors.New("mg unavailable") }

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "integ", PRFlow: true}
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, postMergeVerdict{}, backstop)
	fire()

	if len(escalations) != 1 || escalations[0] != "mg-1234" {
		t.Errorf("expected a conservative escalation when the item state is unknown, got %v", escalations)
	}
}

// TestReapMergedPolecat_PRFlowSkipsAutoDone is the mg-7746 unit-level guard:
// PRFlow alone (no --defer-done) must be enough to keep the polecat alive and
// the work item claimed.
func TestReapMergedPolecat_PRFlowSkipsAutoDone(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var escalations []string
	backstop, _ := newTestBackstop(reg, &escalations)

	completeCalled := false
	mr := &refinery.MergeRequest{
		ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234",
		TargetRef: "daed-101-integration", PRFlow: true,
	}
	reapMergedPolecat(reg, mr, func(string, string) error { completeCalled = true; return nil }, postMergeVerdict{}, backstop)

	if completeCalled {
		t.Error("PR flow: mg done must NOT be called — completion is the PR, not the integration merge")
	}
	if len(reg.stopped) != 0 {
		t.Errorf("PR flow: polecat must NOT be stopped before it opens the PR, got %v", reg.stopped)
	}
	backstop.mu.Lock()
	_, armed := backstop.timers["1234"]
	backstop.mu.Unlock()
	if !armed {
		t.Error("PR flow: backstop must be armed so a stalled polecat cannot hold its slot forever")
	}
}

// TestReapMergedPolecat_ResultRecordsTargetAndOmitsPRFlow pins the sidecar
// shape the mayor's classification reads.
func TestReapMergedPolecat_ResultRecordsTargetAndOmitsPRFlow(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var got string
	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "main"}
	reapMergedPolecat(reg, mr, func(_, resultJSON string) error { got = resultJSON; return nil }, postMergeVerdict{}, nil)

	var result map[string]any
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("result sidecar is not valid JSON: %v (%q)", err, got)
	}
	if result["target"] != "main" {
		t.Errorf("expected target=main in the result sidecar, got %q", got)
	}
	if _, ok := result["pr_flow"]; ok {
		t.Errorf("pr_flow must be absent on a default-branch merge, got %q", got)
	}
}

// TestDeferredBackstop_NilIsSafe guards the nil-backstop path used by the
// non-defer reap tests and any pogod build where the backstop is not wired.
func TestDeferredBackstop_NilIsSafe(t *testing.T) {
	var b *deferredBackstop
	// None of these must panic.
	b.arm("1234", &refinery.MergeRequest{Author: "mg-1234"})
	b.cancel(&agent.Agent{Name: "1234", WorkItemID: "mg-1234"})
	b.cancel(nil)
}

// TestDeferredBackstop_DeathBetweenMergeAndDoneReleasesClaim is the mg-c8d5
// regression, and the THIRD direction of the acceptance control the first two
// left out: the deferred polecat neither completes nor lingers — it DIES, after
// its branch merged and before `mg done`.
//
// Before the fix, cancel() disarmed the timer on any process exit and returned.
// A self-exit never goes through Registry.Stop, which is the only place that
// released a claim, so the work item stayed in claimed/ under a dead pid:
// undispatchable, and invisible to stall-watch, which scans available/ only.
// The window is small in the --defer-done lane and unremarkable in the PR-flow
// lane, which #92 made the DEFAULT for a merged polecat.
func TestDeferredBackstop_DeathBetweenMergeAndDoneReleasesClaim(t *testing.T) {
	reg := &fakeReaper{
		agents:      map[string]*agent.Agent{"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat}},
		releaseHeld: map[string]bool{"mg-1234": true}, // never reached mg done
	}
	var escalations []string
	backstop, fire := newTestBackstop(reg, &escalations)

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "integration", PRFlow: true}
	reapMergedPolecat(reg, mr, func(string, string) error {
		t.Error("PR flow: mg done must not be called at merge")
		return nil
	}, postMergeVerdict{}, backstop)

	// The death: the process ends on its own, mid-flow. OnExit calls cancel.
	backstop.cancel(reg.agents["1234"])

	if len(reg.released) != 1 || reg.released[0] != "mg-1234" {
		t.Fatalf("deferred death: expected the claim on mg-1234 to be released, got %v", reg.released)
	}
	if reg.releaseHeld["mg-1234"] {
		t.Error("deferred death: mg-1234 is still claimed — it is stranded under a dead pid")
	}
	if len(escalations) != 1 || escalations[0] != "death:mg-1234" {
		t.Errorf("deferred death: expected one death escalation for mg-1234, got %v", escalations)
	}
	// The process is already gone: reaping it would be a second stop of a dead
	// agent, not slot protection.
	if len(reg.stopped) != 0 {
		t.Errorf("deferred death: nothing to stop, got stopped=%v", reg.stopped)
	}
	// And the disarmed timer must not resurrect any of this later.
	fire()
	if len(escalations) != 1 {
		t.Errorf("deferred death: a late timer fire must be inert, got %v", escalations)
	}
}

// TestDeferredBackstop_ExitWithNoClaimHeldIsQuiet is the negative control that
// keeps the fix above from becoming a pager. Two ordinary endings leave no claim
// held: the polecat ran `mg done` itself, or the mayor stopped it and
// Registry.Stop released on the way down (mg-fb13). Neither is a death, and
// neither may escalate.
func TestDeferredBackstop_ExitWithNoClaimHeldIsQuiet(t *testing.T) {
	reg := &fakeReaper{
		agents:      map[string]*agent.Agent{"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat}},
		releaseHeld: map[string]bool{}, // already out of claimed/
	}
	var escalations []string
	backstop, _ := newTestBackstop(reg, &escalations)

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "integration", PRFlow: true}
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, postMergeVerdict{}, backstop)
	backstop.cancel(reg.agents["1234"])

	if len(escalations) != 0 {
		t.Errorf("an exit with no claim held is a completion, not a death; got escalations=%v", escalations)
	}
}

// TestDeferredBackstop_FailedReleaseStillEscalates: a release that ERRORS is the
// one case worth paging hardest about — the item is stranded and pogod could not
// unstrand it, so a human has to run `mg unclaim`. Silence here would recreate
// the invisible failure mg-fb13 was filed for.
func TestDeferredBackstop_FailedReleaseStillEscalates(t *testing.T) {
	reg := &fakeReaper{
		agents:      map[string]*agent.Agent{"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat}},
		releaseHeld: map[string]bool{"mg-1234": true},
		releaseErr:  errors.New("mg unclaim: store is locked"),
	}
	var escalations []string
	backstop, _ := newTestBackstop(reg, &escalations)

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", DeferDone: true}
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, postMergeVerdict{}, backstop)
	backstop.cancel(reg.agents["1234"])

	if len(escalations) != 1 || escalations[0] != "death:mg-1234" {
		t.Errorf("a failed claim release must escalate, got %v", escalations)
	}
}

// TestDeferredBackstop_UnarmedExitTouchesNothing: cancel runs from the OnExit
// hook for EVERY agent in the fleet, the overwhelming majority of which never
// merged anything. An agent that was never armed must not have its claim probed
// or released — that would turn a lifecycle hook into a fleet-wide unclaim
// sweep, which is precisely the blast radius mg-fb13 refused.
func TestDeferredBackstop_UnarmedExitTouchesNothing(t *testing.T) {
	reg := &fakeReaper{
		agents:      map[string]*agent.Agent{"9999": {Name: "9999", WorkItemID: "mg-9999", Type: agent.TypePolecat}},
		releaseHeld: map[string]bool{"mg-9999": true},
	}
	var escalations []string
	backstop, _ := newTestBackstop(reg, &escalations)

	backstop.cancel(reg.agents["9999"])

	if len(reg.released) != 0 {
		t.Errorf("an unarmed agent's exit must not touch any claim, got released=%v", reg.released)
	}
	if !reg.releaseHeld["mg-9999"] {
		t.Error("a mid-flight polecat that never merged must keep its claim on exit")
	}
	if len(escalations) != 0 {
		t.Errorf("an unarmed exit must not escalate, got %v", escalations)
	}
}
