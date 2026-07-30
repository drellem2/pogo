package agent

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// pairingItem is one work item in a fixture store.
type pairingItem struct {
	id      string
	repo    string
	tags    []string
	depends []string
	// status picks the directory: "available" (default), "claimed", or "done".
	status string
	// claimed writes the file under the `<id>.md.<pid>` name a claim leaves
	// behind, which is the name workitem.ListFrom cannot see.
	claimed bool
}

// pairingStore builds a macguffin store fixture and returns its root.
func pairingStore(t *testing.T, items ...pairingItem) string {
	t.Helper()
	root := t.TempDir()
	for _, it := range items {
		status := it.status
		if status == "" {
			status = "available"
		}
		dir := filepath.Join(root, "work", status)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("---\nid: %s\ntype: task\ncreated: 2026-07-30T01:00:00Z\ncreator: daniel\n"+
			"depends: [%s]\ntags: [%s]\nrepo: %s\npriority: high\n---\n\n# %s\n",
			it.id, strings.Join(it.depends, ", "), strings.Join(it.tags, ", "), it.repo, it.id)
		name := it.id + ".md"
		if it.claimed {
			name += ".12345"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// onethirdPolicy is the configuration half of mg-0e24, written out here exactly
// as a deployment would write it in config.toml. It lives in the TEST because
// that is the whole point of the platform/configuration split: no shipped source
// file names this program, this repository, or this tag vocabulary.
const onethirdRepo = "/Users/daniel/research/onethird_program"

func onethirdPolicy() config.DispatchPairingConfig {
	return config.DispatchPairingConfig{
		Repos:      []string{onethirdRepo},
		PairTags:   []string{"independent-audit"},
		WaiverTags: []string{"audit-waived"},
	}
}

// TestSpawnPolecatRefusedWhenPairIsMissing IS THE POSITIVE CONTROL mg-0e24
// demanded, and it is built to the shape of the actual miss rather than to a
// convenient one. mg-78c0 was a onethird research ticket with no pre-filed
// audit; it was dispatched without complaint and merged thirteen minutes later.
// Same item, same store, through the same handler: the dispatch must now be
// refused.
//
// "A check that has only ever been seen to pass is not a check" — so this
// asserts the refusal, and TestSpawnPolecatAllowedWhenPairExists below asserts
// the gate is not simply refusing everything.
func TestSpawnPolecatRefusedWhenPairIsMissing(t *testing.T) {
	root := pairingStore(t, pairingItem{
		id:   "mg-78c0",
		repo: onethirdRepo,
		tags: []string{"onethird", "audit", "followup"},
	})
	reg := newDrainTestRegistry(t)
	reg.SetDispatchPairingGate(MGDispatchPairingGate{Root: root, Cfg: onethirdPolicy()})

	rr := spawnPolecatFor(t, reg, "mg-78c0")
	if rr.Code != http.StatusConflict {
		t.Fatalf("spawn on an unpaired onethird item: status = %d, want 409 — "+
			"the pairing gate did not refuse, which is the mg-78c0 miss reproduced", rr.Code)
	}
	body := rr.Body.String()
	// The refusal has to be actionable without reading source: the item, the
	// repo that put it in scope, the marker a pair must carry, and the visible
	// opt-out.
	for _, want := range []string{"mg-78c0", onethirdRepo, "independent-audit", "audit-waived"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal does not mention %q; got: %s", want, body)
		}
	}
	// Nothing may be left behind — the gate sits above every side effect.
	if a := reg.Get("cat-gate"); a != nil {
		t.Error("a refused dispatch registered an agent anyway")
	}
}

// TestSpawnPolecatAllowedWhenPairExists is the other half of the control: an
// item WITH a pre-filed pair dispatches untouched. Both reference channels the
// store actually uses are exercised, because a gate that recognised only one
// would refuse dispatches whose audit is sitting right there in available/.
func TestSpawnPolecatAllowedWhenPairExists(t *testing.T) {
	tests := []struct {
		name string
		pair pairingItem
	}{
		{
			// The mg-a3d4 / mg-86a3 shape: the audit declares `depends:`.
			name: "pair references the target by depends",
			pair: pairingItem{
				id: "mg-86a3", repo: onethirdRepo,
				tags:    []string{"onethird", "audit", "research", "independent-audit"},
				depends: []string{"mg-a3d4"},
			},
		},
		{
			// The mg-78c0 / mg-5630 shape: the audit declares a followup TAG.
			name: "pair references the target by a followup tag",
			pair: pairingItem{
				id: "mg-5630", repo: onethirdRepo,
				tags: []string{"onethird", "independent-audit", "mg-a3d4-followup"},
			},
		},
		{
			// A pair that is already being worked on. workitem.ListFrom cannot
			// see claimed files at all, so without ListAllFrom this arm refuses
			// an item whose audit exists — a false refusal that would look
			// exactly like the gate working.
			name: "pair is claimed and mid-flight",
			pair: pairingItem{
				id: "mg-5630", repo: onethirdRepo, status: "claimed", claimed: true,
				tags: []string{"onethird", "independent-audit"}, depends: []string{"mg-a3d4"},
			},
		},
		{
			// An audit that already ran still discharges the obligation.
			name: "pair is already done",
			pair: pairingItem{
				id: "mg-5630", repo: onethirdRepo, status: "done",
				tags: []string{"onethird", "independent-audit"}, depends: []string{"mg-a3d4"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := pairingStore(t,
				pairingItem{id: "mg-a3d4", repo: onethirdRepo, tags: []string{"onethird", "research"}},
				tt.pair,
			)
			reg := newDrainTestRegistry(t)
			reg.SetDispatchPairingGate(MGDispatchPairingGate{Root: root, Cfg: onethirdPolicy()})

			if rr := spawnPolecatFor(t, reg, "mg-a3d4"); rr.Code == http.StatusConflict {
				t.Errorf("pairing gate refused an item whose pair IS filed: %s", rr.Body.String())
			}
		})
	}
}

// TestSpawnPolecatPairingExemptions — the three ways a covered item legitimately
// carries no obligation. Each is asserted separately because each is a different
// claim: an audit is not its own audit, a waiver is a deliberate act, and a
// narrowing require_tags is an operator's choice.
func TestSpawnPolecatPairingExemptions(t *testing.T) {
	tests := []struct {
		name string
		item pairingItem
		cfg  config.DispatchPairingConfig
	}{
		{
			name: "the pair itself does not owe a pair",
			item: pairingItem{id: "mg-5630", repo: onethirdRepo,
				tags: []string{"onethird", "independent-audit"}},
			cfg: onethirdPolicy(),
		},
		{
			name: "a visibly waived item dispatches",
			item: pairingItem{id: "mg-aaaa", repo: onethirdRepo,
				tags: []string{"onethird", "research", "audit-waived"}},
			cfg: onethirdPolicy(),
		},
		{
			name: "an item in another repo is not covered",
			item: pairingItem{id: "mg-bbbb", repo: "/Users/daniel/dev/pogo",
				tags: []string{"pogo", "research"}},
			cfg: onethirdPolicy(),
		},
		{
			name: "require_tags narrows the obligation",
			item: pairingItem{id: "mg-cccc", repo: onethirdRepo, tags: []string{"onethird", "ops"}},
			cfg: config.DispatchPairingConfig{
				Repos: []string{onethirdRepo}, RequireTags: []string{"research"},
				PairTags: []string{"independent-audit"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetDispatchPairingGate(MGDispatchPairingGate{
				Root: pairingStore(t, tt.item), Cfg: tt.cfg,
			})
			if rr := spawnPolecatFor(t, reg, tt.item.id); rr.Code == http.StatusConflict {
				t.Errorf("pairing gate refused: %s", rr.Body.String())
			}
		})
	}
}

// TestSpawnPolecatPairingFailsOpenBeforeCoverage pins the documented fail-open
// half. Until the gate has positively read an item and seen a covered `repo:`,
// it must not refuse anything — `--id` is optional by design (mg-2437) and a
// gate that halted the fleet over one repository's policy would be worse than
// the miss it prevents.
func TestSpawnPolecatPairingFailsOpenBeforeCoverage(t *testing.T) {
	tests := []struct {
		name string
		gate MGDispatchPairingGate
		id   string
	}{
		{"no --id supplied", MGDispatchPairingGate{Root: pairingStore(t), Cfg: onethirdPolicy()}, ""},
		{"id not in the store", MGDispatchPairingGate{Root: pairingStore(t), Cfg: onethirdPolicy()}, "mg-ghost"},
		{"store does not exist", MGDispatchPairingGate{
			Root: filepath.Join(t.TempDir(), "absent"), Cfg: onethirdPolicy()}, "mg-ghost"},
		{"no repos configured", MGDispatchPairingGate{
			Root: pairingStore(t, pairingItem{id: "mg-dddd", repo: onethirdRepo, tags: []string{"research"}}),
		}, "mg-dddd"},
		{"repos configured but no pair_tags — unsatisfiable, so not enforced", MGDispatchPairingGate{
			Root: pairingStore(t, pairingItem{id: "mg-dddd", repo: onethirdRepo, tags: []string{"research"}}),
			Cfg:  config.DispatchPairingConfig{Repos: []string{onethirdRepo}},
		}, "mg-dddd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetDispatchPairingGate(tt.gate)
			if rr := spawnPolecatFor(t, reg, tt.id); rr.Code == http.StatusConflict {
				t.Errorf("gate refused (%s) when it must fail open: %s", tt.name, rr.Body.String())
			}
		})
	}
}

// TestSpawnPolecatPairingFailsClosedAfterCoverage is the other direction, and it
// is the one that is easy to get backwards. Once the item is KNOWN to be covered,
// an unreadable store is an unverifiable obligation, and an unverifiable
// obligation is an undischarged one. The blast radius is one repository, which is
// what makes the asymmetry affordable.
func TestSpawnPolecatPairingFailsClosedAfterCoverage(t *testing.T) {
	root := pairingStore(t, pairingItem{
		id: "mg-a3d4", repo: onethirdRepo, tags: []string{"onethird", "research"},
	})
	// Make the scan of available/ fail while leaving the by-id lookup working:
	// FindFrom opens `<id>.md` directly, ListAllFrom must ReadDir the directory.
	avail := filepath.Join(root, "work", "available")
	if err := os.Chmod(avail, 0o300); err != nil {
		t.Skipf("cannot make the directory unreadable: %v", err)
	}
	t.Cleanup(func() { os.Chmod(avail, 0o755) })
	if _, err := os.ReadDir(avail); err == nil {
		t.Skip("directory is still readable (running as root?); cannot exercise the scan failure")
	}

	reg := newDrainTestRegistry(t)
	reg.SetDispatchPairingGate(MGDispatchPairingGate{Root: root, Cfg: onethirdPolicy()})
	rr := spawnPolecatFor(t, reg, "mg-a3d4")
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a covered item whose store could not be scanned "+
			"must be refused, not waved through", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "REFUSING") {
		t.Errorf("refusal does not say it is refusing because it could not check: %s", body)
	}
}

// TestDispatchPairingDefaultShipsNoPolicy — the default must be functional but
// INERT, the opposite of SetDispatchGate's default. This gate carries one
// deployment's configuration, so a daemon that never reaches the wiring line
// must apply no program's rules to anyone.
func TestDispatchPairingDefaultShipsNoPolicy(t *testing.T) {
	reg := newDrainTestRegistry(t)
	g := reg.getDispatchPairingGate()
	if g == nil {
		t.Fatal("getDispatchPairingGate() returned nil on a fresh registry")
	}
	mg, ok := g.(MGDispatchPairingGate)
	if !ok {
		t.Fatalf("default gate is %T, want MGDispatchPairingGate", g)
	}
	if len(mg.Cfg.Repos) != 0 {
		t.Errorf("the default gate carries policy: %v", mg.Cfg.Repos)
	}
	if refusal, unmet := mg.PairingUnmet("mg-anything"); unmet {
		t.Errorf("the unconfigured default refused a dispatch: %s", refusal)
	}

	// Explicit nil restores the default rather than leaving a nil interface.
	reg.SetDispatchPairingGate(nil)
	if reg.getDispatchPairingGate() == nil {
		t.Error("SetDispatchPairingGate(nil) left the gate nil")
	}
}

// TestPairingGateIsIndependentOfAssigneeGate — the two gates answer different
// questions at the same chokepoint, and neither may mask the other. An item that
// clears the assignee gate must still face the pairing gate; that is the
// composition the spawn handler relies on.
func TestPairingGateIsIndependentOfAssigneeGate(t *testing.T) {
	root := pairingStore(t, pairingItem{
		id: "mg-a3d4", repo: onethirdRepo, tags: []string{"onethird", "research"},
	})
	reg := newDrainTestRegistry(t)
	// An unassigned item: the assignee gate passes it, as it did for mg-78c0.
	reg.SetDispatchGate(MGDispatchGate{Root: root})
	if _, gated := (MGDispatchGate{Root: root}).DispatchGated("mg-a3d4"); gated {
		t.Fatal("fixture is wrong: the assignee gate already refuses this item")
	}
	reg.SetDispatchPairingGate(MGDispatchPairingGate{Root: root, Cfg: onethirdPolicy()})
	if rr := spawnPolecatFor(t, reg, "mg-a3d4"); rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — an item the assignee gate passes must still "+
			"face the pairing gate", rr.Code)
	}
}
