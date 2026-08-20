package ineffect

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's CHECKED envelope. This package's whole subject is
// the operator's live tree — the running daemon, ~/go/bin, ~/.pogo/bin,
// ~/.pogo/agents, ~/.pogo/deploy-src. Every judgement is constructible against
// a fake Deps, but nothing in the type system stops a test reaching for
// config.PogoHome() and reading the real box, and on this machine that would be
// a suite whose verdicts change with whether last night's deploy happened.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("ineffect")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestFixturesAreSandboxed is the positive control for the isolation above.
// Without it the isolation is an unverified claim and its failure is silent: a
// test that reached for the live ~/.pogo would still be green, and would still
// be judging the wrong tree.
func TestFixturesAreSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home, err := os.UserHomeDir()
	if err != nil || !sandbox.Contains(home) {
		t.Errorf("os.UserHomeDir() = %s (err %v), want a path under the sandbox root %s", home, err, sandbox.Root)
	}
}
