package mgcontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any of them resolves onto the developer's live tree.
//
// MG_ROOT is the load-bearing one here. This package's entire job is to run the
// real `mg`, and mg resolves its store as `--root` > `$MG_ROOT` >
// `$HOME/.macguffin`. The probes address every command with an explicit --root
// of their own, so the envelope is belt to that braces — but a package that
// shells out to the fleet's live work-item store dozens of times per run is the
// last place to rely on one mechanism.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("mgcontract")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestProbeStoresAreOutsideTheDevelopersTree is the positive control for the
// isolation above, and it asserts the property that actually matters for this
// package: the store a probe runs against is a throwaway of its own, reachable
// from neither the developer's ~/.macguffin nor the fleet's live one.
//
// Without it the isolation is an unverified claim — a later edit could drop the
// --root and leave every clause green while the suite filed probe fixtures into
// the real work-item store, which is a strictly worse outcome than the gate
// outage this package exists to prevent.
func TestProbeStoresAreOutsideTheDevelopersTree(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve the home directory: %v", err)
	}
	live, err := filepath.EvalSymlinks(filepath.Join(home, ".macguffin"))
	if err != nil {
		// No live store on this machine is not a pass by luck: the check below
		// still has to hold against the path it WOULD be.
		live = filepath.Join(home, ".macguffin")
	}

	// A recording probe: it captures the root it is addressed with and nothing
	// else, so what is measured is where a real clause would have run.
	var addressed string
	err = probe(Clause{
		Name: "probe-store-location",
		probe: func(s *store) error {
			addressed = s.root
			return nil
		},
	})
	if err != nil {
		if strings.Contains(err.Error(), ErrMgNotInstalled.Error()) {
			t.Skip("mg is not on PATH")
		}
		t.Fatalf("probe: %v", err)
	}
	if addressed == "" {
		t.Fatal("the probe never received a store root, so this test observed nothing")
	}

	resolved, err := filepath.EvalSymlinks(addressed)
	if err != nil {
		resolved = addressed
	}
	// Resolved through symlinks, because the symlinked root is the only shape in
	// which this goes wrong quietly: every string reads as a private path right
	// up until something follows the link.
	if resolved == live || strings.HasPrefix(resolved+string(filepath.Separator), live+string(filepath.Separator)) {
		t.Fatalf("a probe store resolves onto the live macguffin tree: %s -> %s (live %s)", addressed, resolved, live)
	}
	if _, err := os.Stat(addressed); !os.IsNotExist(err) {
		t.Errorf("the probe store at %s outlived the probe; each one is torn down so a "+
			"clause cannot see what an earlier clause left behind", addressed)
	}
}
