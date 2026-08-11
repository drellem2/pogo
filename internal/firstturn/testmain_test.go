package firstturn

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox.
//
// This package's subject is the operator's live tree — the agent registry and
// ~/.pogo/events.log — and its whole point is to alarm when nothing in that tree
// has completed a turn. Every exported entry point takes its paths and its
// population as arguments precisely so the judgement is constructible against a
// fixture, and every test here does exactly that. Nothing in the type system
// stops the next one from reaching for config.PogoHome() instead, and on this
// machine that would be a detector suite whose verdicts change with whether the
// fleet happened to be healthy while the tests ran.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("firstturn")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestFixturesAreSandboxed is the positive control for the isolation above: the
// home this process resolves must be the throwaway one, not the developer's.
// Without it the isolation is an unverified claim, and the failure it prevents
// is silent — a suite that reached for the live ~/.pogo would still be green,
// and would still be judging the wrong tree.
func TestFixturesAreSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home, err := os.UserHomeDir()
	if err != nil || !sandbox.Contains(home) {
		t.Errorf("os.UserHomeDir() = %s (err %v), want a path under the sandbox root %s; "+
			"a test that reached for ~/.pogo would be judging the live box", home, err, sandbox.Root)
	}
}
