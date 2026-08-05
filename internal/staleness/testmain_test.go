package staleness

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox.
//
// This package is a detector whose whole subject is the operator's live tree —
// ~/.pogo/deploy-attempt.stamp, ~/.pogo/agents, ~/.pogo/deploy-src. Every
// exported entry point takes its paths as arguments precisely so the judgement
// is constructible against a fixture, but nothing in the type system stops a
// test from reaching for config.PogoHome() and reading the real box. On this
// machine that would be a suite whose verdicts change with whether last night's
// deploy happened to succeed.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("staleness")
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
