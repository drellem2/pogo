package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// gateStore builds a macguffin store fixture and returns its root (the path that
// goes in MGDispatchGate.Root — the store root, so the gate's own
// filepath.Join(root, "work") is exercised rather than bypassed).
func gateStore(t *testing.T, items map[string]string) string {
	t.Helper()
	root := t.TempDir()
	avail := filepath.Join(root, "work", "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, assignee := range items {
		body := "---\nid: " + id + "\ntype: task\nassignee: " + assignee + "\n---\n# " + id + "\n"
		if err := os.WriteFile(filepath.Join(avail, id+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func spawnPolecatFor(t *testing.T, reg *Registry, id string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(SpawnPolecatAPIRequest{Name: "cat-gate", Id: id})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/agents/spawn-polecat", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr, req)
	return rr
}

// TestSpawnPolecatRefusedForGatedAssignee is THE POSITIVE CONTROL for mg-4798,
// and the reason the ticket demanded one: before this change `pogo agent
// spawn-polecat --id <a human-assigned item>` spawned a worker without complaint,
// and a guard only ever observed on unassigned items has not been observed at all.
// So this asserts the guard CAN fail — that dispatch is refused — for each gate
// value, not merely that ordinary dispatch still works.
func TestSpawnPolecatRefusedForGatedAssignee(t *testing.T) {
	for _, gate := range config.DefaultNonDispatchableAssignees {
		t.Run(gate, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetDispatchGate(MGDispatchGate{
				Root: gateStore(t, map[string]string{"mg-gate": gate}),
			})

			rr := spawnPolecatFor(t, reg, "mg-gate")
			if rr.Code != http.StatusConflict {
				t.Fatalf("spawn on a %q-assigned item: status = %d, want 409 — "+
					"the dispatch gate did not refuse", gate, rr.Code)
			}
			// The refusal has to name what gated it. A bare 409 sends the caller
			// to read source to find out whether to reassign or retry.
			if body := rr.Body.String(); !strings.Contains(body, gate) || !strings.Contains(body, "mg-gate") {
				t.Errorf("refusal must name the item and the gating assignee, got: %q", body)
			}
			// And nothing may be left behind: the gate sits above every side
			// effect, so no agent was registered.
			if a := reg.Get("cat-gate"); a != nil {
				t.Error("a refused dispatch registered an agent anyway")
			}
		})
	}
}

// TestSpawnPolecatAllowedForDispatchableAssignee is the negative half of the
// control. It does not assert the spawn SUCCEEDS — with no template on disk it
// will not — only that the dispatch gate is not what stopped it. Without this, a
// gate that refused unconditionally would pass the test above.
func TestSpawnPolecatAllowedForDispatchableAssignee(t *testing.T) {
	for _, tc := range []struct{ name, assignee string }{
		{"unassigned", ""},
		{"owned by an agent", "pm-pogo"},
		{"owned by the coordinator", "mayor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetDispatchGate(MGDispatchGate{
				Root: gateStore(t, map[string]string{"mg-open": tc.assignee}),
			})

			rr := spawnPolecatFor(t, reg, "mg-open")
			if rr.Code == http.StatusConflict {
				t.Errorf("dispatch gate refused a %s item (assignee=%q): %s",
					tc.name, tc.assignee, rr.Body.String())
			}
		})
	}
}

// TestSpawnPolecatGateHonoursConfiguredVocabulary proves the gate reads the
// operator's non_dispatchable_assignees rather than a hard-coded pair — the
// property mg-a3a2 built and this enforcement point has to inherit, not re-fix.
func TestSpawnPolecatGateHonoursConfiguredVocabulary(t *testing.T) {
	root := gateStore(t, map[string]string{
		"mg-legal":  "legal-review",
		"mg-parked": "parked",
	})
	reg := newDrainTestRegistry(t)
	reg.SetDispatchGate(MGDispatchGate{Root: root, Gates: []string{"human", "legal-review"}})

	if rr := spawnPolecatFor(t, reg, "mg-legal"); rr.Code != http.StatusConflict {
		t.Errorf("configured gate %q did not refuse: status = %d", "legal-review", rr.Code)
	}
	// "parked" is not in this deployment's vocabulary, so it must not gate —
	// a configured list REPLACES the default, matching config.go.
	if rr := spawnPolecatFor(t, reg, "mg-parked"); rr.Code == http.StatusConflict {
		t.Error(`"parked" gated despite being absent from the configured vocabulary`)
	}
}

// TestSpawnPolecatGateFailsOpen pins the documented fail-open direction. These
// are not aspirations — they are the cases that would break legitimate dispatch
// if the gate failed closed, and `--id` is optional by design (mg-2437).
func TestSpawnPolecatGateFailsOpen(t *testing.T) {
	tests := []struct {
		name string
		gate MGDispatchGate
		id   string
	}{
		{"no --id supplied", MGDispatchGate{Root: gateStore(t, nil)}, ""},
		{"id not in the store", MGDispatchGate{Root: gateStore(t, nil)}, "mg-ghost"},
		{"store does not exist", MGDispatchGate{Root: filepath.Join(t.TempDir(), "absent")}, "mg-ghost"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetDispatchGate(tt.gate)
			if rr := spawnPolecatFor(t, reg, tt.id); rr.Code == http.StatusConflict {
				t.Errorf("gate refused (%s) when it must fail open: %s", tt.name, rr.Body.String())
			}
		})
	}
}

// TestDispatchGateDefaultIsFunctional — an unwired Registry must still gate. A
// guard that engages only after someone remembers to call SetDispatchGate is
// absent in every deployment where they didn't, which is the gap shape mg-da48
// and mg-6c4b were both about.
func TestDispatchGateDefaultIsFunctional(t *testing.T) {
	reg := newDrainTestRegistry(t)
	g := reg.getDispatchGate()
	if g == nil {
		t.Fatal("getDispatchGate() returned nil on a fresh registry")
	}
	mg, ok := g.(MGDispatchGate)
	if !ok {
		t.Fatalf("default gate is %T, want MGDispatchGate", g)
	}
	if len(mg.gates()) == 0 {
		t.Fatal("default gate has an empty vocabulary; it would never refuse")
	}
	for _, want := range config.DefaultNonDispatchableAssignees {
		if _, gated := (MGDispatchGate{Root: gateStore(t, map[string]string{"mg-d": want})}).DispatchGated("mg-d"); !gated {
			t.Errorf("default vocabulary does not gate %q", want)
		}
	}

	// Explicit nil restores the default rather than disabling the gate.
	reg.SetDispatchGate(nil)
	if reg.getDispatchGate() == nil {
		t.Error("SetDispatchGate(nil) disabled the gate; it must restore the default")
	}
}

// TestDispatchGateDefaultRootIsTestSafe is a guard on the tests, not the gate.
// The default root must never resolve to the live ~/.macguffin from a test
// binary: that store holds real items assigned to "human" and "parked" today, so
// a leak would make every existing spawn test's outcome depend on Daniel's queue.
// This is the mg-da48 lesson applied to the gate — a safe DEFAULT, not an opt-in.
func TestDispatchGateDefaultRootIsTestSafe(t *testing.T) {
	root := macguffinStoreRoot("")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if live := filepath.Join(home, ".macguffin"); root == live {
		t.Fatalf("macguffinStoreRoot() = %q under a test binary — that is the LIVE store", root)
	}
}
