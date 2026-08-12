// This file is package testtmp_test — the EXTERNAL test package — and that is
// forced, not stylistic. testsandbox imports testtmp (Main takes its sandbox
// root from it), so a testtmp-internal file importing testsandbox would be an
// import cycle and would not build. An external test package may import a
// package that depends on the package under test, so the isolation is adopted
// here and the assertions about Reap stay in-package next door.
package testtmp_test

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox.
//
// Nothing in this package reads HOME or ~/.pogo — it resolves $TMPDIR and
// nothing else. The envelope is not for what these tests do; it is for what the
// next test written here would be free to do, and adopting it is the ratchet in
// adoption_test.go rather than a judgement about this suite.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("testtmp")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestEnvIsSandboxed is the positive control for the envelope above.
func TestEnvIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home := os.Getenv("HOME")
	if !sandbox.Contains(home) {
		t.Errorf("HOME = %s, want a path under the sandbox root %s", home, sandbox.Root)
	}
}
