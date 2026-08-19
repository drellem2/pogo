package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dependsStore builds a macguffin store fixture in which items can be placed in
// any lifecycle directory and can declare `depends:`. gateStore next door writes
// only available/ items with an assignee, which is the whole vocabulary the
// assignee gate needs and none of what this one does.
//
// It writes `depends:` as the inline YAML sequence mg itself writes, so the
// parser under test is fed the shape it will actually meet.
type depItem struct {
	status  string   // available | claimed | done | pending
	depends []string // parent ids
	pidSfx  bool     // claimed items carry a `.<pid>` filename suffix
}

func dependsStore(t *testing.T, items map[string]depItem) string {
	t.Helper()
	root := t.TempDir()
	for id, it := range items {
		dir := filepath.Join(root, "work", it.status)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nid: " + id + "\ntype: task\n"
		if len(it.depends) > 0 {
			body += "depends: [" + strings.Join(it.depends, ", ") + "]\n"
		}
		body += "---\n# " + id + "\n"
		name := id + ".md"
		if it.pidSfx {
			name += ".4242"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Every directory the gate reads must exist even when empty, or an absent
	// done/ would be indistinguishable from a store the gate could not read.
	for _, d := range []string{"available", "claimed", "done", "pending"} {
		if err := os.MkdirAll(filepath.Join(root, "work", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestSpawnPolecatRefusedForUnmetDepends is the mg-e7ff positive control, and it
// is the case the measured incident produced: an item in available/ whose parent
// is still claimed. Before this gate, `spawn-polecat` put a worker on it without
// complaint — the dispatch gate was keyed on assignee and on `stage: gated`, and
// unmet depends was the one "deliberately not ready" condition with no check at
// the spawn point.
func TestSpawnPolecatRefusedForUnmetDepends(t *testing.T) {
	for _, parentStatus := range []string{"available", "claimed", "pending"} {
		t.Run("parent "+parentStatus, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetDispatchGate(MGDispatchGate{
				Root: dependsStore(t, map[string]depItem{
					"mg-child":  {status: "available", depends: []string{"mg-parent"}},
					"mg-parent": {status: parentStatus, pidSfx: parentStatus == "claimed"},
				}),
			})

			rr := spawnPolecatFor(t, reg, "mg-child")
			if rr.Code != http.StatusConflict {
				t.Fatalf("spawn on an item depending on a %s parent: status = %d, want 409 — "+
					"the dispatch gate did not refuse", parentStatus, rr.Code)
			}
			body := rr.Body.String()
			// The refusal must name the parent to chase and what it is doing.
			// "blocked" without an id sends the reader to go and look.
			if !strings.Contains(body, "mg-parent") {
				t.Errorf("refusal does not name the outstanding parent: %q", body)
			}
			if !strings.Contains(body, parentStatus) {
				t.Errorf("refusal does not say what the parent is doing (%s): %q", parentStatus, body)
			}
			if !strings.Contains(body, "mg-child") {
				t.Errorf("refusal does not name the item: %q", body)
			}
			// The store inconsistency is the half a reader can act on beyond
			// this one dispatch: mg parks a gated dependent in pending/, so a
			// gated item in available/ means some path placed it wrongly.
			if !strings.Contains(body, "available/") {
				t.Errorf("refusal does not report that the item is misplaced in available/: %q", body)
			}
			// Nothing may be left behind: the gate sits above every side effect.
			if a := reg.Get("cat-gate"); a != nil {
				t.Error("a refused dispatch registered an agent anyway")
			}
		})
	}
}

// TestSpawnPolecatAllowedWhenDependsAreSatisfied is the negative half, and it is
// the half that decides whether the gate is usable at all. It does not assert the
// spawn succeeds — with no template on disk it will not — only that the depends
// gate is not what stopped it.
//
// The "archived or nonexistent parent" case is the load-bearing one. pogo's store
// reader does not scan the archive, and mg archives completed work within minutes
// of `mg done`, so a gate that treated "absent" as "unfinished" would refuse
// nearly every dispatch whose item has any dependency at all.
func TestSpawnPolecatAllowedWhenDependsAreSatisfied(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items map[string]depItem
	}{
		{
			name: "no depends at all",
			items: map[string]depItem{
				"mg-child": {status: "available"},
			},
		},
		{
			name: "parent is done",
			items: map[string]depItem{
				"mg-child":  {status: "available", depends: []string{"mg-parent"}},
				"mg-parent": {status: "done"},
			},
		},
		{
			name: "parent is archived or was never filed — absent from the store",
			items: map[string]depItem{
				"mg-child": {status: "available", depends: []string{"mg-gone"}},
			},
		},
		{
			name: "every parent is done",
			items: map[string]depItem{
				"mg-child": {status: "available", depends: []string{"mg-p1", "mg-p2"}},
				"mg-p1":    {status: "done"},
				"mg-p2":    {status: "done"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetDispatchGate(MGDispatchGate{Root: dependsStore(t, tc.items)})

			rr := spawnPolecatFor(t, reg, "mg-child")
			if rr.Code == http.StatusConflict {
				t.Errorf("depends gate refused a dispatchable item (%s): %s", tc.name, rr.Body.String())
			}
		})
	}
}

// One outstanding parent among several satisfied ones still refuses, and the
// refusal names ONLY the one that is holding it. Naming a met parent would bury
// the one the reader has to chase.
func TestSpawnPolecatDependsRefusalNamesOnlyTheUnmetParents(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetDispatchGate(MGDispatchGate{
		Root: dependsStore(t, map[string]depItem{
			"mg-child": {status: "available", depends: []string{"mg-done", "mg-live"}},
			"mg-done":  {status: "done"},
			"mg-live":  {status: "claimed", pidSfx: true},
		}),
	})

	rr := spawnPolecatFor(t, reg, "mg-child")
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "mg-live") {
		t.Errorf("refusal does not name the outstanding parent: %q", body)
	}
	if strings.Contains(body, "mg-done") {
		t.Errorf("refusal names a SATISFIED parent, burying the one that holds it: %q", body)
	}
}

// The assignee gate stays first. An item that is both human-assigned and
// dependency-gated must refuse with the assignee's message: that value was set by
// hand and states an intent the dependency cannot, and its way out differs.
func TestSpawnPolecatAssigneeGateStillWinsOverDepends(t *testing.T) {
	root := dependsStore(t, map[string]depItem{
		"mg-parent": {status: "claimed", pidSfx: true},
	})
	body := "---\nid: mg-both\ntype: task\nassignee: human\ndepends: [mg-parent]\n---\n# mg-both\n"
	if err := os.WriteFile(filepath.Join(root, "work", "available", "mg-both.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newDrainTestRegistry(t)
	reg.SetDispatchGate(MGDispatchGate{Root: root})

	rr := spawnPolecatFor(t, reg, "mg-both")
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	if out := rr.Body.String(); !strings.Contains(out, "human") {
		t.Errorf("refusal did not come from the assignee gate: %q", out)
	}
}

// The gate must fail OPEN on a store it cannot read, like every other branch of
// MGDispatchGate. A guard that halts the fleet over one bad path gets disarmed
// rather than fixed.
func TestSpawnPolecatDependsGateFailsOpenOnAnUnreadableStore(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetDispatchGate(MGDispatchGate{Root: filepath.Join(t.TempDir(), "no-such-store")})

	if rr := spawnPolecatFor(t, reg, "mg-child"); rr.Code == http.StatusConflict {
		t.Errorf("depends gate refused against an absent store: %s", rr.Body.String())
	}
}
