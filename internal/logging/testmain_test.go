package logging

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox.
//
// This package's own surface is one environment variable and a pure parse, so
// nothing here reaches for HOME or ~/.pogo today. The envelope is not for what
// these tests do — it is for what the next test written here would otherwise
// be free to do. TestEveryTestSuiteRoutesThroughTheIsolation is a ratchet, and
// a package that opts out on the grounds that it "doesn't need it yet" is the
// one that quietly starts needing it.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("logging")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestEnvIsSandboxed is the positive control for the envelope above: the
// process HOME these tests run under must resolve inside the throwaway root,
// not onto the developer's live tree. It also pins the property the level
// tests actually depend on — that this package reads its answer from the
// process environment, which t.Setenv is therefore able to control.
func TestEnvIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home := os.Getenv("HOME")
	if !sandbox.Contains(home) {
		t.Errorf("HOME = %s, want a path under the sandbox root %s", home, sandbox.Root)
	}
}
