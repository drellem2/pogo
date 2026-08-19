package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubMergedGate answers from a fixed map, so the handler-side behaviour is
// testable without a refinery (which this package may not even import).
type stubMergedGate struct {
	merged map[string]MergedWork
	asked  []string
}

func (s *stubMergedGate) MergedWork(id string) (MergedWork, bool) {
	s.asked = append(s.asked, id)
	m, ok := s.merged[id]
	return m, ok
}

func mergedFixture() MergedWork {
	return MergedWork{
		MR:        "mr-d9ugdoitjv1ohvj2fd20",
		Branch:    "polecat-ac0c",
		Target:    "main",
		MergedSHA: "0123456789abcdef0123",
		MergedAt:  time.Now().Add(-4 * time.Minute),
	}
}

// A merged item is refused, and the refusal names everything a reader needs to
// CHECK the claim without trusting it: the branch, the target, the sha and the
// merge request. A guard whose claim cannot be checked is one that gets
// overridden on reflex.
func TestMergedWorkRefusalNamesTheMergeAndBothWaysOut(t *testing.T) {
	r := &Registry{}
	r.SetMergedWorkGate(&stubMergedGate{merged: map[string]MergedWork{"mg-ac0c": mergedFixture()}})

	refusal := r.mergedWorkRefusal("mg-ac0c")
	if refusal == "" {
		t.Fatal("a work item whose branch has merged was not refused")
	}
	for _, want := range []string{
		"mg-ac0c",
		"polecat-ac0c",
		"main",
		"0123456789ab",
		"mr-d9ugdoitjv1ohvj2fd20",
		"mg done mg-ac0c --successor=",
		"--merged-override=",
	} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal does not mention %q, so a reader cannot check it or act on it:\n%s", want, refusal)
		}
	}

	// The ITEM's remedy has to come before the override. The usual cause is an
	// unfiled remainder, and a reader who reaches for the override first leaves
	// the item in exactly the state that produced the refusal.
	if strings.Index(refusal, "mg done") > strings.Index(refusal, "--merged-override") {
		t.Errorf("the override is offered before the remedy; a refusal ordered that way teaches "+
			"the reader to bypass it:\n%s", refusal)
	}
}

// The age is rendered, because "merged 4 minutes ago" and "merged last week"
// call for different reactions from a coordinator holding a queue.
func TestMergedWorkRefusalRendersHowLongAgo(t *testing.T) {
	r := &Registry{}
	r.SetMergedWorkGate(&stubMergedGate{merged: map[string]MergedWork{"mg-ac0c": mergedFixture()}})
	if refusal := r.mergedWorkRefusal("mg-ac0c"); !strings.Contains(refusal, "ago") {
		t.Errorf("refusal does not say how long ago the merge landed:\n%s", refusal)
	}

	// A record with no timestamp says nothing rather than rendering a bogus age.
	m := mergedFixture()
	m.MergedAt = time.Time{}
	r.SetMergedWorkGate(&stubMergedGate{merged: map[string]MergedWork{"mg-ac0c": m}})
	if refusal := r.mergedWorkRefusal("mg-ac0c"); strings.Contains(refusal, "ago") {
		t.Errorf("a merge with no recorded time produced an age anyway:\n%s", refusal)
	}
}

// THE FAILURE DIRECTION IS OPEN, and it has three separate doors. Each is a case
// where refusing would break legitimate spawns rather than prevent a duplicated
// one.
func TestMergedWorkRefusalFailsOpen(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*Registry)
		id    string
	}{
		{
			name:  "no gate installed at all",
			setup: func(r *Registry) { r.SetMergedWorkGate(nil) },
			id:    "mg-ac0c",
		},
		{
			name: "no work item id on the spawn",
			setup: func(r *Registry) {
				r.SetMergedWorkGate(&stubMergedGate{merged: map[string]MergedWork{"mg-ac0c": mergedFixture()}})
			},
			id: "",
		},
		{
			name: "the gate knows of no merge for this item",
			setup: func(r *Registry) {
				r.SetMergedWorkGate(&stubMergedGate{merged: map[string]MergedWork{"mg-0e8c": mergedFixture()}})
			},
			id: "mg-ac0c",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Registry{}
			tc.setup(r)
			if refusal := r.mergedWorkRefusal(tc.id); refusal != "" {
				t.Errorf("dispatch was refused when the gate could not answer, which halts the fleet "+
					"on a question it was never able to ask: %s", refusal)
			}
		})
	}
}

// A blank id must not even reach the gate: the gate's production implementation
// scans the refinery's whole history, and doing that for a spawn that named no
// item is work in service of an answer that cannot be used.
func TestMergedWorkRefusalDoesNotAskAboutABlankID(t *testing.T) {
	stub := &stubMergedGate{merged: map[string]MergedWork{}}
	r := &Registry{}
	r.SetMergedWorkGate(stub)
	r.mergedWorkRefusal("   ")
	if len(stub.asked) != 0 {
		t.Errorf("the gate was consulted for a blank work item id: %v", stub.asked)
	}
}

// spawnPolecatWithMergedOverride drives the real handler, which is the only
// place the gate is actually consulted. A unit test of mergedWorkRefusal alone
// would pass identically if the handler never called it — and a gate that is
// never reached is precisely the failure mg-4798 was filed for.
func spawnPolecatWithMergedOverride(t *testing.T, reg *Registry, id, override string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(SpawnPolecatAPIRequest{
		Name: "cat-merged", Id: id, Template: BuildWorkerTemplate, MergedOverride: override,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/agents/spawn-polecat", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr, req)
	return rr
}

// THE POSITIVE CONTROL for mg-9d4e: before this change `pogo agent spawn-polecat
// --id <an item whose branch merged>` was accepted without complaint, because the
// item is genuinely unclaimed. priority-wake offered mg-0e8c and mg-ac0c that way
// within minutes of each merging.
func TestSpawnPolecatRefusedForAlreadyMergedWork(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetMergedWorkGate(&stubMergedGate{merged: map[string]MergedWork{"mg-ac0c": mergedFixture()}})

	rr := spawnPolecatWithMergedOverride(t, reg, "mg-ac0c", "")
	if rr.Code != http.StatusConflict {
		t.Fatalf("spawn on an already-merged item: status = %d, want 409 — the merged-work gate "+
			"did not refuse (body: %s)", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "mg-ac0c") || !strings.Contains(body, "polecat-ac0c") {
		t.Errorf("refusal must name the item and the branch that merged, got: %q", body)
	}
	// The gate sits above every side effect, so a refused dispatch leaves no
	// agent, worktree or prompt file behind (mg-ef80).
	if a := reg.Get("cat-merged"); a != nil {
		t.Error("a refused dispatch registered an agent anyway")
	}
}

// The negative half. It does not assert the spawn SUCCEEDS — with no template on
// disk it will not — only that the merged-work gate is not what stopped it.
// Without this, a gate that refused unconditionally would pass the test above.
func TestSpawnPolecatNotRefusedWhenNothingMerged(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetMergedWorkGate(&stubMergedGate{merged: map[string]MergedWork{"mg-0e8c": mergedFixture()}})

	rr := spawnPolecatWithMergedOverride(t, reg, "mg-ac0c", "")
	if body := rr.Body.String(); strings.Contains(body, "HAS ALREADY MERGED") {
		t.Errorf("the merged-work gate refused an item with no merge on record: %q", body)
	}
}

// The override is a written reason, never a boolean, and it must actually get
// past the gate — a gate that can be wrong with no way past it gets disarmed
// rather than overridden. Here it has a use that is not even a false positive:
// an item can genuinely owe work after its merge.
func TestSpawnPolecatMergedOverrideDispatchesAnyway(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetMergedWorkGate(&stubMergedGate{merged: map[string]MergedWork{"mg-ac0c": mergedFixture()}})

	rr := spawnPolecatWithMergedOverride(t, reg, "mg-ac0c", "release item: the tag step follows the merge")
	if body := rr.Body.String(); strings.Contains(body, "HAS ALREADY MERGED") {
		t.Errorf("--merged-override did not get past the gate: %q", body)
	}

	// And a blank override is not an override: an empty string must not silently
	// bypass the refusal.
	rr = spawnPolecatWithMergedOverride(t, reg, "mg-ac0c", "   ")
	if rr.Code != http.StatusConflict {
		t.Errorf("a whitespace-only --merged-override bypassed the gate: status = %d", rr.Code)
	}
}
