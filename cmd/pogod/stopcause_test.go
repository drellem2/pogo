package main

import (
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// pogod's own polecat stops name themselves (mg-a95f).
//
// Three of the six paths that reach Registry.Stop are pogod reaping a polecat,
// and they are the ones that dominate ~/.pogo/events.log — 34,380 agent_stopped
// records at the time of writing, almost all of them these. Before this they were
// indistinguishable from each other AND from a fleet-wide drain, which is what
// made the 2026-08-08 crew stop a two-day question.
//
// Each assertion names a DIFFERENT constant, so a fix that threaded one cause
// through every call site fails here rather than passing as "attributed".

// TestStopCause_MergeReapNamesItself covers the event-driven merge reap (gh #35).
func TestStopCause_MergeReapNamesItself(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234"}
	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, postMergeVerdict{}, nil, nil)

	if len(reg.stopCauses) != 1 || reg.stopCauses[0] != agent.StopCauseMergeReap {
		t.Errorf("stop_cause: want [%s], got %v", agent.StopCauseMergeReap, reg.stopCauses)
	}
}

// TestStopCause_DeferDoneBackstopNamesItself covers the 15-minute backstop, which
// reaps a polecat that merged and then never ended its own lifecycle (gh #81).
// It must NOT read as an ordinary merge reap: the two mean different things about
// whether the polecat finished.
func TestStopCause_DeferDoneBackstopNamesItself(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"q77f": {Name: "q77f", WorkItemID: "mg-77f0", Type: agent.TypePolecat},
	}}
	b := newDeferredBackstop(time.Hour, reg, func(*refinery.MergeRequest) {})
	mr := &refinery.MergeRequest{ID: "mr-9", Branch: "polecat-q77f", Author: "mg-77f0"}
	b.arm("q77f", mr)
	b.fire("q77f", mr)

	if len(reg.stopCauses) != 1 || reg.stopCauses[0] != agent.StopCauseMergeBackstop {
		t.Errorf("stop_cause: want [%s], got %v", agent.StopCauseMergeBackstop, reg.stopCauses)
	}
}

// TestStopCause_DoneReapNamesItself covers the done-and-idle reaper (mg-56d1) —
// the path that ends every non-merge polecat, and the one an operator is most
// likely to meet in the log.
func TestStopCause_DoneReapNamesItself(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "d764", WorkItemID: "mg-d764", IdleFor: 7 * time.Minute, HasOutput: true},
	}}
	r := newDoneReaper(reg, doneStore(map[string]string{"mg-d764": "done"}), noReviews, 2*time.Minute)

	if stopped := r.Check(time.Now()); len(stopped) != 1 {
		t.Fatalf("test premise: want d764 reaped, got %v", stopped)
	}
	if len(reg.stopCauses) != 1 || reg.stopCauses[0] != agent.StopCauseDoneReap {
		t.Errorf("stop_cause: want [%s], got %v", agent.StopCauseDoneReap, reg.stopCauses)
	}
}
