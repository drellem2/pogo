package ghintake

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any of them resolves onto the developer's live tree.
//
// This package needs the envelope more than most, because it reads two live
// stores and the blast radius of each is different in kind:
//
//   - The WORK-ITEM store. Unlike internal/ghteardown, which scans `status=done`
//     only, this scan covers every status — ~2 000 items on the live store, one
//     `mg show` each. MGSource.resolveRoot has its own testing.Testing() default
//     for exactly that reason, and TestResolveRootNeverResolvesToTheLiveStoreUnderTest
//     asserts it, but a defaulted --root is one mechanism and MG_ROOT is another.
//     Both should hold, and the sandbox is what makes the second one true.
//
//   - The POLLER STATE directory. DiscoverRepos reads
//     $POGO_HOME/gh-issues/seen-<owner>-<repo>.json, so a suite running against
//     the developer's POGO_HOME would silently derive its watch list from live
//     host state — a test whose fixture is whatever repos this machine happens to
//     poll today. That is not a hypothetical class: it is mg-6092 / mg-e8e7 /
//     mg-5336 / mg-3412 with a different directory name.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("ghintake")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestPollerStateDirIsSandboxed is the positive control for the envelope above.
// The watch list is derived from POGO_HOME, so with no configured repos the
// discovery path must look inside the throwaway tree and find nothing there —
// never at the operator's real ~/.pogo/gh-issues, where it would pick up whatever
// this machine polls today and quietly make that the fixture.
//
// Without this assertion the isolation is an unverified claim: every other test
// in the package would stay green while the suite went back to reading live host
// state.
func TestPollerStateDirIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	stateDir := filepath.Join(sandbox.PogoHome, PollerStateDirName)
	if !sandbox.Contains(stateDir) {
		t.Fatalf("poller state dir %s is outside the sandbox root %s", stateDir, sandbox.Root)
	}

	// Nothing has written a seen file there, so discovery must come up empty —
	// and with no configured list either, the watch list is empty. It is NOT a
	// built-in repo list: that fallback named pogo's own upstream repos until
	// mg-f04b, which meant an unconfigured install reconciled a stranger's
	// issue tracker. The source string is what distinguishes "examined nothing"
	// from "found nothing", so assert it, not just the emptiness.
	if got := DiscoverRepos(stateDir); len(got) != 0 {
		t.Errorf("DiscoverRepos(%s) = %v, want nothing — the sandbox tree has no poller state", stateDir, got)
	}
	repos, src := ResolveRepos(nil, stateDir)
	if src != "no repos configured" {
		t.Errorf("watch-list source = %q, want %q", src, "no repos configured")
	}
	if len(repos) != 0 {
		t.Errorf("repos = %v, want none — an unconfigured install watches nothing", repos)
	}
}

// TestDefaultReposIsEmpty is the guard proper: the fallback must name no repo.
//
// It is a separate test from the sandbox check above because the failure it
// catches is not a test-isolation bug. A default repo list is a working feature
// on the machine whose repos it names and a silent misfeature everywhere else:
// pogod polls a tracker its operator has nothing to do with, finds every issue
// uncarried (they all are — they're strangers' issues), and mails the
// coordinator a wall of findings that cannot be actioned. Nothing about that
// reads as misconfiguration from the inside.
func TestDefaultReposIsEmpty(t *testing.T) {
	if len(DefaultRepos) != 0 {
		t.Errorf("DefaultRepos = %v, want empty — a built-in watch list names "+
			"repos that belong to whoever wrote it, not to whoever installed pogo", DefaultRepos)
	}
}

// And the store side of the same envelope: MG_ROOT must not point at the live
// macguffin tree, so even an `mg` invocation that somehow escaped MGSource's
// explicit --root would land in the throwaway root.
func TestMGRootIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	root := os.Getenv("MG_ROOT")
	if root == "" {
		t.Fatal("MG_ROOT is unset under test — mg would fall back to ~/.macguffin, the live store")
	}
	// Contains is the whole assertion. Comparing against os.UserHomeDir() would be
	// worse than redundant: HOME is itself pinned inside the envelope, so
	// $HOME/.macguffin is the CORRECT answer under test and a prefix check against
	// it fails on a properly sandboxed root. testsandbox.Verify already refuses any
	// value that resolves onto the developer's real tree.
	if !sandbox.Contains(root) {
		t.Fatalf("MG_ROOT = %s, want a path under the sandbox root %s", root, sandbox.Root)
	}
}
