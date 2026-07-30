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
	// status picks the directory: "available" (default), "claimed", "done",
	// "pending" or "shelved".
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

// TestSpawnPolecatAllowedWhenPairIsPending is the arm the first cut of this gate
// got wrong, and it is the one that matters most in practice.
//
// A pair is filed with `depends: [<target>]`. mg parks an item whose depends are
// unmet in pending/ until `mg schedule` promotes it. The target of a pairing
// obligation is by definition not done — it has not even been dispatched yet —
// so a CORRECTLY pre-filed pair is in pending/ for exactly the window this gate
// runs in. Measured on the live store the morning this was written: three of the
// five items in pending/ were pre-filed onethird audits, and every one of them
// would have read as never filed.
//
// The consequence is the expensive direction. A missed case is one unaudited
// item; a gate that refuses every correctly paired item refuses every item in
// the repo, and the lesson operators learn from that is to disarm the gate.
func TestSpawnPolecatAllowedWhenPairIsPending(t *testing.T) {
	root := pairingStore(t,
		pairingItem{id: "mg-41aa", repo: onethirdRepo, tags: []string{"onethird", "audit-repair"}},
		// mg-5800 as it actually sits on disk: pre-filed, depends on its target,
		// and parked in pending/ because that target is not done.
		pairingItem{
			id: "mg-5800", repo: onethirdRepo, status: "pending",
			tags:    []string{"onethird", "audit", "independent-audit", "mg-41aa-followup"},
			depends: []string{"mg-41aa"},
		},
	)
	reg := newDrainTestRegistry(t)
	reg.SetDispatchPairingGate(MGDispatchPairingGate{Root: root, Cfg: onethirdPolicy()})

	if rr := spawnPolecatFor(t, reg, "mg-41aa"); rr.Code == http.StatusConflict {
		t.Fatalf("pairing gate refused an item whose pair is pre-filed and parked in pending/: %s\n"+
			"pending/ is where a correctly pre-filed pair LIVES — refusing here refuses "+
			"every properly paired item in the repo", rr.Body.String())
	}
}

// TestSpawnPolecatPairingShelvedPairDoesNotSatisfy is the boundary on the fix
// above, and it must be asserted separately or "scan pending/" slides into "scan
// everything". Pending is a pair waiting its turn; shelved is a pair somebody
// dropped. If shelving counted, an obligation could be discharged by abandoning
// it — which is the failure this gate exists to prevent, reachable by one
// command.
func TestSpawnPolecatPairingShelvedPairDoesNotSatisfy(t *testing.T) {
	root := pairingStore(t,
		pairingItem{id: "mg-a3d4", repo: onethirdRepo, tags: []string{"onethird", "research"}},
		pairingItem{
			id: "mg-5630", repo: onethirdRepo, status: "shelved",
			tags:    []string{"onethird", "independent-audit"},
			depends: []string{"mg-a3d4"},
		},
	)
	reg := newDrainTestRegistry(t)
	reg.SetDispatchPairingGate(MGDispatchPairingGate{Root: root, Cfg: onethirdPolicy()})

	if rr := spawnPolecatFor(t, reg, "mg-a3d4"); rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a SHELVED pair is a dropped obligation, "+
			"not a discharged one", rr.Code)
	}
}

// TestSpawnPolecatPairingOverrideDispatches is the escape hatch mg-2530 required,
// and the reason it is required is not that the obligation is optional. It is
// that a refusal with no override becomes a wedge the first time the marker is
// wrong — a repo named too broadly, a pair filed under a tag the config does not
// list — and a wedge under time pressure gets resolved by disarming the gate. A
// cheap, loud override is what keeps the gate armed.
func TestSpawnPolecatPairingOverrideDispatches(t *testing.T) {
	logPath := useTempEventLog(t)
	root := pairingStore(t, pairingItem{
		id: "mg-a3d4", repo: onethirdRepo, tags: []string{"onethird", "research"},
	})
	reg := newDrainTestRegistry(t)
	reg.SetDispatchPairingGate(MGDispatchPairingGate{Root: root, Cfg: onethirdPolicy()})

	// Same item, same store, same gate as TestSpawnPolecatRefusedWhenPairIsMissing
	// — which asserts this exact spawn is refused. The only difference is the
	// override, so a pass here cannot be the gate having gone quiet.
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-gate", Id: "mg-a3d4", Template: BuildWorkerTemplate,
		PairingOverride: "audit is mg-9999, filed under the wrong tag; fixing the config separately",
	})
	if rr.Code == http.StatusConflict {
		t.Fatalf("--pairing-override did not get past the pairing gate: %s", rr.Body.String())
	}

	// Permitted is only half of it. An override nobody can find afterwards is
	// indistinguishable from a gate that did not fire.
	ev := findEvent(readEventLines(t, logPath), "dispatch_pairing_overridden", "cat-cat-gate")
	if ev == nil {
		t.Fatal("the override left no dispatch_pairing_overridden event: " +
			"an unrecorded override is the silent bypass this gate exists to end")
	}
	if ev["work_item_id"] != "mg-a3d4" {
		t.Errorf("event work_item_id = %v, want mg-a3d4", ev["work_item_id"])
	}
	details, _ := ev["details"].(map[string]any)
	if reason, _ := details["reason"].(string); !strings.Contains(reason, "wrong tag") {
		t.Errorf("event details.reason = %q, want the operator's stated reason", reason)
	}
	// The refusal too, verbatim: the reason is what the operator believed, the
	// refusal is what the gate actually objected to, and a reader needs both to
	// tell a config bug from an unaudited deliverable.
	if refusal, _ := details["refusal"].(string); !strings.Contains(refusal, "PAIRED") {
		t.Errorf("event details.refusal = %q, want the bypassed refusal verbatim", refusal)
	}
}

// TestSpawnPolecatPairingOverrideMustCarryAReason — whitespace is not a reason,
// and neither is an absent flag. If an empty value overrode the gate, then every
// caller that does not set the field overrides it, which is every caller.
func TestSpawnPolecatPairingOverrideMustCarryAReason(t *testing.T) {
	for _, override := range []string{"", "   ", "\t\n"} {
		t.Run(fmt.Sprintf("%q", override), func(t *testing.T) {
			root := pairingStore(t, pairingItem{
				id: "mg-a3d4", repo: onethirdRepo, tags: []string{"onethird", "research"},
			})
			reg := newDrainTestRegistry(t)
			reg.SetDispatchPairingGate(MGDispatchPairingGate{Root: root, Cfg: onethirdPolicy()})
			rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
				Name: "cat-gate", Id: "mg-a3d4", Template: BuildWorkerTemplate,
				PairingOverride: override,
			})
			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409 — %q is not a stated reason", rr.Code, override)
			}
		})
	}
}

// TestSpawnPolecatPairingOverrideDoesNotWidenToOtherGates. The override names one
// gate and must move one gate. An override that quietly became "dispatch this
// item no matter what any check says" is a much larger flag than the one anybody
// asked for, and the assignee gate is the one it would most plausibly swallow —
// it sits immediately above, refuses with the same 409, and answers a question
// nobody delegated to this flag.
func TestSpawnPolecatPairingOverrideDoesNotWidenToOtherGates(t *testing.T) {
	root := pairingStore(t, pairingItem{
		id: "mg-a3d4", repo: onethirdRepo, tags: []string{"onethird", "research"},
	})
	// Assign the item to `human`, which the assignee gate refuses.
	item := filepath.Join(root, "work", "available", "mg-a3d4.md")
	b, err := os.ReadFile(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(item, []byte(strings.Replace(string(b),
		"priority: high", "assignee: human\npriority: high", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := newDrainTestRegistry(t)
	reg.SetDispatchGate(MGDispatchGate{Root: root})
	reg.SetDispatchPairingGate(MGDispatchPairingGate{Root: root, Cfg: onethirdPolicy()})

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-gate", Id: "mg-a3d4", Template: BuildWorkerTemplate,
		PairingOverride: "pair obligation waived deliberately",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — --pairing-override overrode the ASSIGNEE gate, "+
			"which it does not name and was never given", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "human") {
		t.Errorf("refusal is not the assignee gate's: %s", body)
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
