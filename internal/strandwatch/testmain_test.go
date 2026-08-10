package strandwatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. Same reasoning as internal/strandedwork's: this
// package's own code reads nothing from the environment, but it runs git, and
// git reaches for ~/.gitconfig for user.name, commit.gpgsign,
// init.defaultBranch, and hooks paths. The helpers pin GIT_CONFIG_GLOBAL and
// GIT_CONFIG_SYSTEM per invocation; the sandbox pins HOME behind them so a git
// that ignores those still cannot reach the operator's real config.
//
// Nothing here shells out to `mg`: the work-item board arrives through
// Options.Items, which every test supplies. That is not only for isolation — a
// detector whose board is injectable can be shown the exact three-row store the
// live one had on 2026-08-09, which no sandbox could otherwise reproduce.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("strandwatch")
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

	if home, err := os.UserHomeDir(); err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	} else if !sandbox.Contains(home) {
		t.Errorf("home = %s, want a path under the sandbox root %s; git fixtures would "+
			"read the developer's real ~/.gitconfig", home, sandbox.Root)
	}
}
