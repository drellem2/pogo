package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/vcs"
)

// enth-vcs slice 8 (§7): every pogod consumer that named a merge by its
// merged_sha names an enth merge by mr.Landed(), and renders a git merge
// exactly as before. Each test below pairs the enth case with its git control.

var enthNew = strings.Repeat("ab", 32)

func enthLandedMR() *refinery.MergeRequest {
	return &refinery.MergeRequest{
		ID: "mr-e1", RepoPath: "/r", TargetRef: "main", Author: "mg-e1", Item: "mg-e1",
		Status:        refinery.StatusMerged,
		EnthSubmitted: []string{"c1"},
		Enth: &vcs.LandRecord{Ref: "refs/heads/main", Entry: 7, Old: "o", New: enthNew,
			Changes: []vcs.LandedChange{{Submitted: "c1", Landed: enthNew}}, RequestID: "mr-e1/1"},
	}
}

// The reap sidecar (`.result`): an enth MR has no merged_sha key — so the
// release-cut recipe's `jq .merged_sha` reads null and refuses to tag — and
// names the landing under `landed`. A git MR's sidecar has exactly the keys
// it always had.
func TestReapSidecarNamesAnEnthLandingAndLeavesGitAlone(t *testing.T) {
	sidecar := func(mr *refinery.MergeRequest) map[string]any {
		bare := strings.TrimPrefix(mr.Author, "mg-")
		reg := &fakeReaper{agents: map[string]*agent.Agent{bare: {Name: bare, WorkItemID: mr.Author, Type: agent.TypePolecat}}}
		var got string
		reapMergedPolecat(reg, mr, func(_, r string) error { got = r; return nil }, postMergeVerdict{}, nil, nil)
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Fatalf("sidecar %q: %v", got, err)
		}
		return m
	}

	e := sidecar(enthLandedMR())
	if _, ok := e["merged_sha"]; ok {
		t.Errorf("enth sidecar has merged_sha: %v", e)
	}
	if !reflect.DeepEqual(e["landed"], map[string]any{"vcs": "enth", "id": enthNew}) {
		t.Errorf("enth sidecar landed = %v", e["landed"])
	}

	g := sidecar(&refinery.MergeRequest{ID: "mr-g1", Branch: "polecat-g1", TargetRef: "main", Author: "mg-g1", MergedSHA: "abc123def4567890"})
	want := map[string]any{"branch": "polecat-g1", "mr": "mr-g1", "completed_by": "refinery", "target": "main", "merged_sha": "abc123def4567890"}
	if !reflect.DeepEqual(g, want) {
		t.Errorf("git sidecar changed:\n got %v\nwant %v", g, want)
	}
}

func TestMergedWorkForCarriesTheLanding(t *testing.T) {
	got, ok := mergedWorkFor([]refinery.MergeRequest{*enthLandedMR()}, "mg-e1")
	if !ok || got.MergedSHA != "" || got.LandedVCS != vcs.Enth || got.LandedID != enthNew {
		t.Errorf("enth merged work = %+v (%v)", got, ok)
	}
	got, ok = mergedWorkFor([]refinery.MergeRequest{mergedMR("mr-1", "mg-g1", refinery.StatusMerged)}, "mg-g1")
	if !ok || got.MergedSHA != "abc123def4567890" || got.LandedVCS != vcs.Git || got.LandedID != got.MergedSHA {
		t.Errorf("git merged work = %+v (%v)", got, ok)
	}
}

func TestMergedOpenAlertNamesAnEnthLanding(t *testing.T) {
	got := captureMergedOpenAlerts(t, func(string) (bool, error) { return false, nil })
	reportMergedButOpen(enthLandedMR(), "e1", errors.New("refused"))
	reportMergedButOpen(landedMR(), "ac0c", errors.New("refused"))
	if len(*got) != 2 {
		t.Fatalf("alerts = %d", len(*got))
	}
	_, eb := (*got)[0].Message()
	if !strings.Contains(eb, "Merge:      mr-e1 -> main as enth "+enthNew+"\n") {
		t.Errorf("enth alert does not name the landing:\n%s", eb)
	}
	if (*got)[0].EnthLanded["id"] != enthNew {
		t.Errorf("enth alert detail = %v", (*got)[0].EnthLanded)
	}
	_, gb := (*got)[1].Message()
	if !strings.Contains(gb, "Merge:      mr-d9ugdoitjv1ohvj2fd20 -> main as abc123def4567890\n") || strings.Contains(gb, "enth") {
		t.Errorf("git alert changed:\n%s", gb)
	}
	if (*got)[1].EnthLanded != nil {
		t.Errorf("git alert grew an enth detail: %v", (*got)[1].EnthLanded)
	}
}

func TestReopenGuardNamesAnEnthLanding(t *testing.T) {
	e := *enthLandedMR()
	failed := guardFailedMR("mr-2", "mg-e1", "")
	res := reopenResult{Outcome: reopenDeclinedLanded, Landed: e}
	if line := reopenMailLine(failed, res); !strings.Contains(line, " as enth abababababab,") {
		t.Errorf("enth reopen line: %s", line)
	}
	ev, ok := reopenEvent(failed, res)
	if !ok || ev.Details["landed_sha"] != nil || !reflect.DeepEqual(ev.Details["landed"], map[string]string{"vcs": "enth", "id": enthNew}) {
		t.Errorf("enth reopen event details = %v", ev.Details)
	}

	g := guardMergedMR("mr-1", "mg-g1", "polecat-g1", "main", "45b4421abc", false)
	res = reopenResult{Outcome: reopenDeclinedLanded, Landed: g}
	if line := reopenMailLine(failed, res); !strings.Contains(line, "main as 45b4421,") || strings.Contains(line, "enth") {
		t.Errorf("git reopen line changed: %s", line)
	}
	ev, _ = reopenEvent(failed, res)
	if ev.Details["landed_sha"] != "45b4421abc" || ev.Details["landed"] != nil {
		t.Errorf("git reopen event details changed: %v", ev.Details)
	}
}
