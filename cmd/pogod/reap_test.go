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
// Stop calls.
type fakeReaper struct {
	agents  map[string]*agent.Agent
	stopped []string
	stopErr error
}

func (f *fakeReaper) Get(name string) *agent.Agent { return f.agents[name] }

func (f *fakeReaper) Stop(name string, timeout time.Duration) error {
	f.stopped = append(f.stopped, name)
	return f.stopErr
}

func TestReapMergedPolecat_StopsPolecatAndMarksDone(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"mg-1234": {Name: "mg-1234", Type: agent.TypePolecat},
	}}
	var completedID, completedResult string
	complete := func(id, resultJSON string) error {
		completedID = id
		completedResult = resultJSON
		return nil
	}

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234"}
	reapMergedPolecat(reg, mr, complete)

	if completedID != "mg-1234" {
		t.Errorf("expected mg done for mg-1234, got %q", completedID)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(completedResult), &result); err != nil {
		t.Fatalf("result sidecar is not valid JSON: %v (%q)", err, completedResult)
	}
	if result["branch"] != "polecat-mg-1234" || result["mr"] != "mr-42" || result["completed_by"] != "refinery" {
		t.Errorf("unexpected result sidecar: %q", completedResult)
	}
	if len(reg.stopped) != 1 || reg.stopped[0] != "mg-1234" {
		t.Errorf("expected exactly one Stop(mg-1234), got %v", reg.stopped)
	}
}

func TestReapMergedPolecat_IgnoresEmptyAuthor(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{}}
	called := false
	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b"}, func(string, string) error {
		called = true
		return nil
	})
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
	})
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
	})
	if called || len(reg.stopped) != 0 {
		t.Errorf("expected no action for crew author (complete=%v, stopped=%v)", called, reg.stopped)
	}
}

func TestReapMergedPolecat_StopsEvenWhenDoneFails(t *testing.T) {
	// "Already done" (the polecat won the race) must not leave the polecat
	// running.
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"mg-1234": {Name: "mg-1234", Type: agent.TypePolecat},
	}}
	complete := func(string, string) error { return errors.New("mg done failed: already done") }

	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-1234"}, complete)

	if len(reg.stopped) != 1 || reg.stopped[0] != "mg-1234" {
		t.Errorf("expected Stop(mg-1234) despite mg done failure, got %v", reg.stopped)
	}
}

func TestReapMergedPolecat_StopFailureIsNonFatal(t *testing.T) {
	reg := &fakeReaper{
		agents: map[string]*agent.Agent{
			"mg-1234": {Name: "mg-1234", Type: agent.TypePolecat},
		},
		stopErr: errors.New("agent wedged"),
	}
	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-1234"}, func(string, string) error { return nil })
	// Must not panic; the mayor's backstop picks up the still-running polecat.
	if len(reg.stopped) != 1 {
		t.Errorf("expected one Stop attempt, got %v", reg.stopped)
	}
}
