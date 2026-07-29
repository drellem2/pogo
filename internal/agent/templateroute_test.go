package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// typeStore builds a macguffin store fixture whose items carry a `type` marker
// and returns its root (the store root, so MGWorkItemTyper's own
// filepath.Join(root, "work") is exercised rather than bypassed).
//
// A "-" value writes an item with NO `type:` line at all, which is a distinct
// case from an unrecognized one: an item that predates the marker vocabulary.
func typeStore(t *testing.T, items map[string]string) string {
	t.Helper()
	root := t.TempDir()
	avail := filepath.Join(root, "work", "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, itemType := range items {
		body := "---\nid: " + id + "\n"
		if itemType != "-" {
			body += "type: " + itemType + "\n"
		}
		body += "---\n# " + id + "\n"
		if err := os.WriteFile(filepath.Join(avail, id+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// routeTestRegistry builds a registry that can actually spawn, inside an
// isolated sandbox, with the given store wired as the type source.
func routeTestRegistry(t *testing.T, storeRoot string) *Registry {
	t.Helper()
	testsandbox.Isolate(t)
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	reg.SetCommandConfig(catCommandConfig{})
	reg.SetWorkItemTyper(MGWorkItemTyper{Root: storeRoot})
	return reg
}

// TestTemplateForTypeIsClosed pins the map itself. Two properties, and the
// second is the load-bearing one: the routed types resolve, and EVERY other
// type — including the ones the fleet actually files most of — resolves to
// nothing at all rather than to the build worker.
func TestTemplateForTypeIsClosed(t *testing.T) {
	for _, tc := range []struct{ itemType, want string }{
		{"design", "polecat-architect"},
		{"qa", "polecat-qa"},
		// Normalization, mirroring config.IsDispatchGated: a marker typed with
		// a capital or a stray space still routes rather than silently falling
		// off the map into a refusal.
		{"Design", "polecat-architect"},
		{"  qa  ", "polecat-qa"},
	} {
		got, ok := TemplateForType(tc.itemType)
		if !ok || got != tc.want {
			t.Errorf("TemplateForType(%q) = (%q, %v), want (%q, true)", tc.itemType, got, ok, tc.want)
		}
	}

	// The closed half. `task` is here on purpose: it is the default type and
	// the overwhelming majority of filed work, and it is precisely the case
	// that used to silently receive the build worker.
	for _, itemType := range []string{"task", "scoping", "audit", "bug", "", "   ", "architect", "polecat"} {
		if got, ok := TemplateForType(itemType); ok {
			t.Errorf("TemplateForType(%q) = (%q, true) — the map must be CLOSED; "+
				"an unrouted type produces no template", itemType, got)
		}
	}

	// Nothing may map to the build worker. If it ever does, the "unrouted types
	// are refused" property is dead for that type without anything saying so.
	for _, tmpl := range workerTemplateForType {
		if tmpl == BuildWorkerTemplate {
			t.Errorf("a type routes to %q: the build worker is reachable only via an "+
				"explicit --template, never by routing", BuildWorkerTemplate)
		}
	}
}

// TestSpawnPolecatRefusesUnroutedType IS THE POSITIVE CONTROL, and the whole
// point of the ticket: before this change a spawn with no --template on any of
// these items dispatched the build worker without complaint. It must now refuse,
// and the refusal must NAME THE TYPE — a bare 409 sends the caller to read
// source to find out whether to fix the item or pass a flag.
func TestSpawnPolecatRefusesUnroutedType(t *testing.T) {
	for _, itemType := range []string{"task", "scoping", "audit", "bug"} {
		t.Run(itemType, func(t *testing.T) {
			reg := routeTestRegistry(t, typeStore(t, map[string]string{"mg-unrouted": itemType}))
			writeTemplate(t, BuildWorkerTemplate, "# build worker\n")

			rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: "cat-unrouted", Id: "mg-unrouted"})
			if rr.Code != http.StatusConflict {
				t.Fatalf("spawn on a %q item with no --template: status = %d, want 409 — "+
					"the template router did not refuse", itemType, rr.Code)
			}
			body := rr.Body.String()
			if !strings.Contains(body, itemType) || !strings.Contains(body, "mg-unrouted") {
				t.Errorf("refusal must name the item and the unrouted type, got: %q", body)
			}
			// It must also say the way out, or the caller's only recovery is to
			// guess — which is the behaviour being removed.
			if !strings.Contains(body, "--template") {
				t.Errorf("refusal must name the override that gets past it, got: %q", body)
			}
			// And nothing may be left behind: the router sits above the
			// worktree, agent dir, and expanded prompt file (mg-ef80).
			if a := reg.Get("cat-unrouted"); a != nil {
				t.Error("a refused dispatch registered an agent anyway")
			}
			if _, err := os.Stat(filepath.Join(PromptDir(), "cat-unrouted")); !os.IsNotExist(err) {
				t.Errorf("a refused dispatch left an agent dir behind (stat err = %v)", err)
			}
		})
	}
}

// TestSpawnPolecatRefusesWhenTypeCannotBeRead covers the three ways the router
// can fail to learn a type. All of them refuse, which is the opposite direction
// from MGDispatchGate — see the rationale at the top of templateroute.go.
func TestSpawnPolecatRefusesWhenTypeCannotBeRead(t *testing.T) {
	for _, tc := range []struct{ name, id, store string }{
		{"no --id supplied", "", "present"},
		{"id not in the store", "mg-ghost", "present"},
		{"store does not exist", "mg-ghost", "absent"},
		{"item carries no type field", "mg-untyped", "present"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := typeStore(t, map[string]string{"mg-untyped": "-"})
			if tc.store == "absent" {
				root = filepath.Join(t.TempDir(), "absent")
			}
			reg := routeTestRegistry(t, root)
			writeTemplate(t, BuildWorkerTemplate, "# build worker\n")

			rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: "cat-noread", Id: tc.id})
			if rr.Code != http.StatusConflict {
				t.Fatalf("spawn with %s: status = %d, want 409 — an unanswerable "+
					"routing question must refuse, not default to the build worker",
					tc.name, rr.Code)
			}
			if a := reg.Get("cat-noread"); a != nil {
				t.Error("a refused dispatch registered an agent anyway")
			}
		})
	}
}

// TestSpawnPolecatRoutesMappedTypes is the negative half of the control: the
// router must not over-fire. Every currently-used routed type still dispatches
// exactly as it does today, and dispatches to the RIGHT template — a router that
// refused everything would pass the test above.
func TestSpawnPolecatRoutesMappedTypes(t *testing.T) {
	for _, tc := range []struct{ itemType, wantTemplate string }{
		{"design", "polecat-architect"},
		{"qa", "polecat-qa"},
	} {
		t.Run(tc.itemType, func(t *testing.T) {
			reg := routeTestRegistry(t, typeStore(t, map[string]string{"mg-routed": tc.itemType}))
			// Distinctive nudges make the assertion about WHICH template was
			// expanded, not merely that some spawn succeeded.
			writeTemplate(t, BuildWorkerTemplate, "+++\nnudge_on_start = \"BUILD\"\n+++\nbody\n")
			writeTemplate(t, tc.wantTemplate, "+++\nnudge_on_start = \"ROUTED-"+tc.itemType+"\"\n+++\nbody\n")

			a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
				Name: "cat-routed", Id: "mg-routed", NoWorktree: true,
			})
			if want := "ROUTED-" + tc.itemType; a.InitialNudge != want {
				t.Errorf("type %q dispatched the wrong template: InitialNudge = %q, want %q",
					tc.itemType, a.InitialNudge, want)
			}
		})
	}
}

// TestSpawnPolecatExplicitTemplateOverridesRouting proves the hand-dispatch
// override survives: a person may put any template on any item, including the
// build worker on an unrouted type, and including the gh-issue stage templates
// which route on a body marker and a stage rather than on `type` and so must
// pass through this path untouched.
func TestSpawnPolecatExplicitTemplateOverridesRouting(t *testing.T) {
	for _, tc := range []struct{ name, itemType, template string }{
		{"build worker on a bare task", "task", BuildWorkerTemplate},
		{"build worker on an unrouted type", "scoping", BuildWorkerTemplate},
		{"gh-issue triage stage", "task", "polecat-triage"},
		{"gh-issue build-pr stage", "task", "polecat-build-pr"},
		{"gh-issue review stage", "task", "polecat-review"},
		// An override may even contradict the map. Markers route; a human
		// decides.
		{"override against the map", "design", BuildWorkerTemplate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := routeTestRegistry(t, typeStore(t, map[string]string{"mg-override": tc.itemType}))
			writeTemplate(t, tc.template, "+++\nnudge_on_start = \"OVERRIDE\"\n+++\nbody\n")

			a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
				Name: "cat-override", Id: "mg-override", Template: tc.template, NoWorktree: true,
			})
			if a.InitialNudge != "OVERRIDE" {
				t.Errorf("explicit --template=%s was not honoured: InitialNudge = %q",
					tc.template, a.InitialNudge)
			}
		})
	}
}

// TestSpawnPolecatRoutingWithoutAnIDStillHonoursExplicitTemplate keeps the
// id-less hand dispatch working. --id is optional by design (mg-2437); what the
// router removes is the id-less spawn that silently got a build worker, not the
// id-less spawn that says which worker it wants.
func TestSpawnPolecatRoutingWithoutAnIDStillHonoursExplicitTemplate(t *testing.T) {
	reg := routeTestRegistry(t, typeStore(t, nil))
	writeTemplate(t, BuildWorkerTemplate, "+++\nnudge_on_start = \"NOID\"\n+++\nbody\n")

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-noid", Template: BuildWorkerTemplate, NoWorktree: true,
	})
	if a.InitialNudge != "NOID" {
		t.Errorf("id-less spawn with an explicit template: InitialNudge = %q, want %q", a.InitialNudge, "NOID")
	}
}

// TestDefaultWorkItemTyperIsTheEnforcingOne mirrors the dispatch gate's twin
// assertion. A router that only engages after someone remembers to call
// SetWorkItemTyper is a router that is off in every deployment where they
// didn't, so the zero value must be the real thing.
func TestDefaultWorkItemTyperIsTheEnforcingOne(t *testing.T) {
	reg := newDrainTestRegistry(t)
	if _, ok := reg.getWorkItemTyper().(MGWorkItemTyper); !ok {
		t.Errorf("an unwired registry's typer is %T, want MGWorkItemTyper — "+
			"routing must hold without wiring", reg.getWorkItemTyper())
	}
	reg.SetWorkItemTyper(nil)
	if _, ok := reg.getWorkItemTyper().(MGWorkItemTyper); !ok {
		t.Error("SetWorkItemTyper(nil) disabled routing; it must restore the default")
	}
}

// TestMGWorkItemTyperReadsTheTypeMarker exercises the production reader against
// a store directly, so the handler tests above are not the only thing standing
// between a store-format change and silent misrouting.
func TestMGWorkItemTyperReadsTheTypeMarker(t *testing.T) {
	root := typeStore(t, map[string]string{
		"mg-des":     "design",
		"mg-untyped": "-",
	})
	typer := MGWorkItemTyper{Root: root}

	if got, found := typer.WorkItemType("mg-des"); !found || got != "design" {
		t.Errorf("WorkItemType(mg-des) = (%q, %v), want (design, true)", got, found)
	}
	if got, found := typer.WorkItemType("mg-untyped"); !found || got != "" {
		t.Errorf("WorkItemType(mg-untyped) = (%q, %v), want (\"\", true) — "+
			"an item present but untyped is found-with-no-type, which is unrouted", got, found)
	}
	if _, found := typer.WorkItemType("mg-ghost"); found {
		t.Error("WorkItemType reported an absent item as found")
	}
	if _, found := typer.WorkItemType(""); found {
		t.Error("WorkItemType reported an empty id as found")
	}
}

// TestMappedTypesNamesTheLiveMap keeps refusal messages honest: they list the
// routed vocabulary, and a message that names a route the router does not
// implement is worse than no message.
func TestMappedTypesNamesTheLiveMap(t *testing.T) {
	got := MappedTypes()
	if len(got) != len(workerTemplateForType) {
		t.Fatalf("MappedTypes() = %v, want one entry per map key (%d)", got, len(workerTemplateForType))
	}
	for _, ty := range got {
		if _, ok := workerTemplateForType[ty]; !ok {
			t.Errorf("MappedTypes() names %q, which is not in the map", ty)
		}
	}
	for _, ty := range got {
		if !strings.Contains(mappedRoutes(), ty+"→"+workerTemplateForType[ty]) {
			t.Errorf("mappedRoutes() omits the %q route", ty)
		}
	}
}
