package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/workitem"
)

// The completion notifier rests on two claims about macguffin that this file
// establishes against a REAL store rather than asserting in prose:
//
//  1. `mg show --json` carries the item's CREATOR — the agent that filed it.
//     Everything mg-f120 does depends on there being a recorded filer to
//     address, and `mg show --json` was measured on 2026-08-12 to carry no
//     `result` key at all, so which fields it does carry is not a safe guess.
//  2. The result sidecar mg writes at `mg done` survives being archived, and is
//     reachable from its new home. The refinery runs `mg archive --days=0`
//     immediately after a merge, so a verdict reader that only knew done/ would
//     work or not depending on which side of that call it ran.
//
// The unit tests establish that the code takes the right branch. Only this one
// establishes that the branch is pointed at something that exists.

// mgActorItem files an item in the sandbox store AS a named creator, by pinning
// MG_ACTOR — which is how mg resolves a creator (MG_ACTOR, else
// POGO_AGENT_NAME, else the OS user), per `mg new --help`.
func mgActorItem(t *testing.T, root, actor, title string) string {
	t.Helper()
	cmd := exec.Command("mg", "--root", root, "new", "--no-repo", title)
	cmd.Env = append(cmd.Environ(), "MG_ACTOR="+actor)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mg new: %v: %s", err, out)
	}
	m := mgNewID.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("could not parse a work item id out of %q", out)
	}
	return m[1]
}

func TestMGShowCarriesTheCreatorTheNotifierAddresses(t *testing.T) {
	root := mgSandboxStore(t)
	// client.MGWorkItemFiling shells out to `mg show` with no --root, so pin the
	// probe at the sandbox through the environment or it asks the developer's
	// live ~/.macguffin about a fixture id.
	t.Setenv("MG_ROOT", root)

	id := mgActorItem(t, root, "pm-onethird", "enumerate the consumers for the energy identity")

	creator, title, err := client.MGWorkItemFiling(id)
	if err != nil {
		t.Fatalf("MGWorkItemFiling(%s): %v", id, err)
	}
	if creator != "pm-onethird" {
		t.Errorf("creator = %q, want pm-onethird — the notifier has nobody to address without it", creator)
	}
	if !strings.Contains(title, "energy identity") {
		t.Errorf("title = %q, want the filed title", title)
	}
}

// An item that names no creator must come back empty AND without an error:
// "nobody is recorded as waiting" and "the store could not be read" point
// opposite ways, and the notifier escalates only the second.
func TestMGWorkItemFilingOnAnItemThatDoesNotExist(t *testing.T) {
	root := mgSandboxStore(t)
	t.Setenv("MG_ROOT", root)

	if _, _, err := client.MGWorkItemFiling("mg-nosuchitem"); err == nil {
		t.Error("an unreadable item must report an error, not an empty creator")
	}
}

// The verdict the filer is sent survives the archive that follows a merge.
func TestTheVerdictIsReachableBeforeAndAfterTheArchive(t *testing.T) {
	root := mgSandboxStore(t)
	work := filepath.Join(root, "work")

	id := mgClaimedItem(t, root, "an item whose worker records a verdict")
	verdict := `{"verdict":"pass","summary":"the thing was done"}`
	if out, err := exec.Command("mg", "--root", root, "done", id, "--result="+verdict).CombinedOutput(); err != nil {
		t.Fatalf("mg done: %v: %s", err, out)
	}

	got, err := workitem.ReadResultFrom(work, id)
	if err != nil {
		t.Fatalf("ReadResultFrom (done): %v", err)
	}
	if !strings.Contains(got, `"summary":"the thing was done"`) {
		t.Fatalf("verdict not readable while the item is in done/: %q", got)
	}

	if out, err := exec.Command("mg", "--root", root, "archive", "--days=0").CombinedOutput(); err != nil {
		t.Fatalf("mg archive: %v: %s", err, out)
	}
	// Confirm the archive actually moved it — otherwise the second read below
	// proves nothing about the archive at all. Globbed one level deeper than
	// mgItemStatus scans: mg files an archived item under archive/<YYYY-MM>/,
	// and that month partition is exactly the thing ReadResultFrom has to walk.
	moved, _ := filepath.Glob(filepath.Join(work, "archive", "*", id+".result.json"))
	if len(moved) == 0 {
		t.Fatalf("mg archive --days=0 did not move %s's sidecar under archive/<month>/; "+
			"this test cannot say anything about the archived case", id)
	}
	if still, _ := filepath.Glob(filepath.Join(work, "done", id+".result.json")); len(still) != 0 {
		t.Fatalf("%s's sidecar is still in done/ after the archive; the archived read below would be "+
			"satisfied by the done/ copy and would prove nothing", id)
	}

	got, err = workitem.ReadResultFrom(work, id)
	if err != nil {
		t.Fatalf("ReadResultFrom (archived): %v", err)
	}
	if !strings.Contains(got, `"summary":"the thing was done"`) {
		t.Errorf("verdict became unreachable once the item was archived: %q — the refinery archives "+
			"immediately after a merge, so this is the ordinary case, not an edge one", got)
	}
}
