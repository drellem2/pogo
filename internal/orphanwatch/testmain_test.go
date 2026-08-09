package orphanwatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope. HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root before a single test
// runs, and refused if any resolves onto the developer's live tree.
//
// POGO_HOME is the one that matters here: DefaultRoot() derives from it, and
// this package's subject is a scan over the polecats tree. A suite that could
// pick up the live ~/.pogo/polecats would be reading a population that changes
// under it — and the live probe starts CPU burners, which must never land in a
// directory pogod is reaping.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("orphanwatch")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestDefaultRootIsSandboxed is the positive control for the isolation above,
// and it also pins the POGO_HOME normalization this detector depends on.
//
// The normalization is not academic on this fleet: an old shell integration
// exports POGO_HOME=$HOME, and a resolver that honored that literally would
// return $HOME/polecats — a directory that does not exist. Every process would
// then be unattributable and a box full of orphans would report clean. This
// asserts DefaultRoot() lands where pogod actually puts worktrees.
func TestDefaultRootIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	got := DefaultRoot()
	if got == "" {
		t.Fatal("DefaultRoot() = \"\"; the envelope is not pinning POGO_HOME")
	}
	if !sandbox.Contains(got) {
		t.Errorf("DefaultRoot() = %s, want a path under the sandbox root %s; a scan would read the live polecats tree",
			got, sandbox.Root)
	}
}

// TestDefaultRootNormalizesLegacyPogoHome pins the $HOME-equals-POGO_HOME case
// directly, because it is the one that fails silently rather than loudly.
func TestDefaultRootNormalizesLegacyPogoHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", home)

	got := DefaultRoot()
	if want := home + "/.pogo/polecats"; got != want {
		t.Errorf("DefaultRoot() with the legacy POGO_HOME=$HOME = %q, want %q; "+
			"a literal join yields %q, which does not exist and attributes nothing",
			got, want, home+"/polecats")
	}
}
