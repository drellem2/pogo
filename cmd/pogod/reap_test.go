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
	reapMergedPolecat(reg, mr, complete)

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
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	complete := func(string, string) error { return errors.New("mg done failed: already done") }

	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-1234"}, complete)

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
	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-1234"}, func(string, string) error { return nil })
	// Must not panic; the mayor's backstop picks up the still-running polecat.
	if len(reg.stopped) != 1 {
		t.Errorf("expected one Stop attempt, got %v", reg.stopped)
	}
}

// fakeUnlinker is a worktreeUnlinker backed by a static agent map, using the
// same name-or-work-item resolution as the real registry.
type fakeUnlinker struct {
	agents map[string]*agent.Agent
}

func (f *fakeUnlinker) GetByWorkItemOrName(id string) *agent.Agent {
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

// TestUnlinkSubmittedPolecatWorktree_ResolvesByWorkItemID is the OnSubmit twin
// of the gh #48 regression: a polecat registered under its BARE id must have
// its worktree unlinked when the MR it submits carries the FULL work-item id.
func TestUnlinkSubmittedPolecatWorktree_ResolvesByWorkItemID(t *testing.T) {
	reg := &fakeUnlinker{agents: map[string]*agent.Agent{
		"1234": {
			Name:        "1234",
			WorkItemID:  "mg-1234",
			Type:        agent.TypePolecat,
			WorktreeDir: "/wt/1234",
			SourceRepo:  "/src/pogo",
		},
	}}
	var gotSource, gotWorktree string
	unlink := func(sourceRepo, worktreeDir string) error {
		gotSource, gotWorktree = sourceRepo, worktreeDir
		return nil
	}

	mr := &refinery.MergeRequest{ID: "mr-7", Branch: "polecat-mg-1234", Author: "mg-1234"}
	unlinkSubmittedPolecatWorktree(reg, mr, unlink)

	if gotSource != "/src/pogo" || gotWorktree != "/wt/1234" {
		t.Errorf("expected unlink(/src/pogo, /wt/1234), got unlink(%q, %q)", gotSource, gotWorktree)
	}
}

func TestUnlinkSubmittedPolecatWorktree_IgnoresEmptyAuthor(t *testing.T) {
	reg := &fakeUnlinker{agents: map[string]*agent.Agent{}}
	called := false
	unlinkSubmittedPolecatWorktree(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b"}, func(string, string) error {
		called = true
		return nil
	})
	if called {
		t.Error("expected no unlink for authorless MR")
	}
}

func TestUnlinkSubmittedPolecatWorktree_IgnoresUnknownAndWorktreeless(t *testing.T) {
	// Unknown author, and a known agent without worktree metadata, are both
	// no-ops.
	reg := &fakeUnlinker{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	for _, author := range []string{"mg-gone", "mg-1234"} {
		called := false
		unlinkSubmittedPolecatWorktree(reg, &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: author}, func(string, string) error {
			called = true
			return nil
		})
		if called {
			t.Errorf("expected no unlink for author %q (missing worktree metadata)", author)
		}
	}
}
