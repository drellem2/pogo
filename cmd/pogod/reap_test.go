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
	reapMergedPolecat(reg, mr, complete, nil)

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
	}, nil)
	if called || len(reg.stopped) != 0 {
		t.Errorf("expected no action for authorless MR (complete=%v, stopped=%v)", called, reg.stopped)
	}
}

func TestReapMergedPolecat_IgnoresUnknownAuthor(t *testing.T) {
	// The polecat already exited (or the author was never an agent) — the
	// mayor's backstop owns any leftover work-item state; pogod must not
	// mg done items it can't tie to a live polecat.
	reg := &fakeReaper{agents: map[string]*agent.Agent{}}
	called := false
	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-gone"}, func(string, string) error {
		called = true
		return nil
	}, nil)
	if called || len(reg.stopped) != 0 {
		t.Errorf("expected no action for unknown author (complete=%v, stopped=%v)", called, reg.stopped)
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
	}, nil)
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

	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-1234"}, complete, nil)

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
	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-1234"}, func(string, string) error { return nil }, nil)
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
// own — the returned func fires the most recently armed one on demand. escalate
// increments *escalations. This lets each test drive both directions of the
// acceptance control without real wall-clock time.
func newTestBackstop(reg polecatReaper, escalations *[]string) (*deferredBackstop, func()) {
	var last *fakeBackstopTimer
	b := newDeferredBackstop(15*time.Minute, reg, func(mr *refinery.MergeRequest) {
		*escalations = append(*escalations, mr.Author)
	})
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
	reapMergedPolecat(reg, mr, complete, backstop)

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
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, backstop)

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
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, backstop)

	// The polecat finishes its post-merge flow and its process exits — the
	// OnExit hook disarms the backstop.
	backstop.cancel("1234")
	// A late timer fire (already-disarmed) must do nothing.
	fire()

	if len(reg.stopped) != 0 {
		t.Errorf("clean completion: polecat must NOT be reaped, got stopped=%v", reg.stopped)
	}
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
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, backstop)

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

// TestDeferredBackstop_NilIsSafe guards the nil-backstop path used by the
// non-defer reap tests and any pogod build where the backstop is not wired.
func TestDeferredBackstop_NilIsSafe(t *testing.T) {
	var b *deferredBackstop
	// None of these must panic.
	b.arm("1234", &refinery.MergeRequest{Author: "mg-1234"})
	b.cancel("1234")
}
