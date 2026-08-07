package main

import (
	"encoding/json"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// mg-dfea. The reap path used to be the SOLE author of the result sidecar, and
// that is how it destroyed the worker's verdict: not by clobbering one (mg
// refuses a second `mg done` rather than overwriting the first — see
// TestReapMergedPolecat_AlreadyDoneLeavesTheWorkersResultStanding) but by
// preempting it. pogod closed the item at merge and stopped the polecat ~0.5s
// later, so the polecat's own `mg done --result` arrived at a closed item and
// was turned away. 139 of 149 landed items on 2026-08-06 carried a
// refinery-authored sidecar; 10 carried any field beyond branch/mr/target.
//
// The verdict now rides in on the merge request from submit time — the one
// moment the author is both alive and not yet preempted — and this writer
// merges it in.
func TestReapMergedPolecat_CarriesTheAuthorsVerdictIntoTheSidecar(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var completedResult string
	complete := func(_, resultJSON string) error {
		completedResult = resultJSON
		return nil
	}

	worker := `{"verdict":"partial","summary":"landed the parser, left the writer","unverified":["throughput"]}`
	mr := &refinery.MergeRequest{
		ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "main",
		Verdict: json.RawMessage(worker),
	}
	reapMergedPolecat(reg, mr, complete, postMergeVerdict{}, nil)

	var side map[string]json.RawMessage
	if err := json.Unmarshal([]byte(completedResult), &side); err != nil {
		t.Fatalf("result sidecar is not valid JSON: %v (%q)", err, completedResult)
	}
	raw, ok := side["verdict"]
	if !ok {
		t.Fatalf("the author's verdict is absent from the sidecar this reap wrote — the destruction this fix closes: %q", completedResult)
	}
	// Verbatim, field for field. A verdict that survives as a summary of itself
	// is the same loss one step later.
	var got, want map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("verdict is not an object in the sidecar: %v (%q)", err, raw)
	}
	if err := json.Unmarshal([]byte(worker), &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("verdict lost fields in transit: wrote %v, sidecar has %v", want, got)
	}
	for k, v := range want {
		if gv, ok := got[k]; !ok {
			t.Errorf("verdict field %q dropped", k)
		} else if a, b := mustJSON(t, gv), mustJSON(t, v); a != b {
			t.Errorf("verdict field %q altered: got %s, want %s", k, a, b)
		}
	}

	// The refinery's own measurements are untouched and un-shadowed. This is
	// why the verdict is nested rather than flattened: an author writing
	// "branch" must not be able to overwrite the branch that actually merged,
	// and must not be silently dropped in favour of it either.
	var flat map[string]any
	if err := json.Unmarshal([]byte(completedResult), &flat); err != nil {
		t.Fatal(err)
	}
	if flat["branch"] != "polecat-mg-1234" || flat["mr"] != "mr-42" || flat["completed_by"] != "refinery" {
		t.Errorf("refinery bookkeeping disturbed by the verdict merge: %q", completedResult)
	}
}

// The negative arm, and it is the more important one: an author that recorded
// NO verdict must still produce a verdict-free sidecar. A fix that made every
// sidecar look answered would have removed the instrument rather than the
// defect — verdictwatch's result-channel predicate ("carries any field beyond
// branch/mr/target") has to keep firing on a real drop.
func TestReapMergedPolecat_NoVerdictStaysDetectableAsADrop(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var completedResult string
	complete := func(_, resultJSON string) error {
		completedResult = resultJSON
		return nil
	}

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "main"}
	reapMergedPolecat(reg, mr, complete, postMergeVerdict{}, nil)

	var side map[string]json.RawMessage
	if err := json.Unmarshal([]byte(completedResult), &side); err != nil {
		t.Fatalf("result sidecar is not valid JSON: %v (%q)", err, completedResult)
	}
	if _, ok := side["verdict"]; ok {
		t.Errorf("a sidecar with no author verdict must not invent one: %q", completedResult)
	}
	// The detector's predicate, run here so the regression is pinned to the
	// measurement and not to a key name this test happens to know.
	for k := range side {
		switch k {
		case "branch", "mr", "completed_by", "target", "merged_sha", "post_merge_tag":
		default:
			t.Errorf("unexpected field %q would make a verdict-free close read as answered: %q", k, completedResult)
		}
	}
}

// An author verdict on a DEFERRED merge must not cause a close. Passing
// --verdict-file on a PR-flow or --defer-done submit is explicitly documented
// as harmless, and "harmless" has to mean it does not drag the item into the
// auto-done lane the deferral exists to avoid.
func TestReapMergedPolecat_VerdictDoesNotOverrideADeferral(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	called := false
	complete := func(string, string) error {
		called = true
		return nil
	}

	mr := &refinery.MergeRequest{
		ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "feat-x",
		PRFlow: true, Verdict: json.RawMessage(`{"verdict":"pass"}`),
	}
	reapMergedPolecat(reg, mr, complete, postMergeVerdict{}, nil)

	if called {
		t.Error("a verdict on a PR-flow MR must not trigger the auto-done the deferral exists to skip")
	}
	if len(reg.stopped) != 0 {
		t.Errorf("a verdict on a PR-flow MR must not trigger the auto-stop, got %v", reg.stopped)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
