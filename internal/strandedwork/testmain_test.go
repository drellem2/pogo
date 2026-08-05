package strandedwork

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any of them resolves onto the developer's live tree.
//
// This package's own code touches none of those — it shells out to git against
// an explicitly named repository and reads nothing from the environment. What it
// DOES do is run git, and git is the tool most eager to consult per-user state:
// ~/.gitconfig supplies user.name, commit.gpgsign, init.defaultBranch, and any
// number of aliases and hooks paths that would make these fixtures behave
// differently on one developer's machine than on another's. The helpers here pin
// GIT_CONFIG_GLOBAL and GIT_CONFIG_SYSTEM per invocation for that reason; the
// sandbox pins HOME behind them, so a git that ignores those variables (an older
// build, a wrapper) still cannot reach the operator's real config.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("strandedwork")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestPackageIsolationIsEstablished is the positive control for the isolation
// above: it re-proves the sandbox from inside a test, so dropping TestMain's
// call cannot leave the rest of the package green while the suite goes back to
// reading the machine's real state.
func TestPackageIsolationIsEstablished(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	// And the package-specific half: HOME must be inside the sandbox, because it
	// is what git falls back to for ~/.gitconfig.
	if home, err := os.UserHomeDir(); err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	} else if !sandbox.Contains(home) {
		t.Errorf("home = %s, want a path under the sandbox root %s; git fixtures would "+
			"read the developer's real ~/.gitconfig", home, sandbox.Root)
	}
}
