package prtracking

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope, established before any test runs.
// HOME, XDG_CONFIG_HOME, POGO_HOME and MG_ROOT are pinned under a throwaway
// root and read back out of the process; see internal/testsandbox.
//
// This package shells out to `mg`, which reads the macguffin store under
// MG_ROOT/HOME. TestLocalResolverRefusesWithoutAWorkingControl runs `mg show`
// for real, so without this envelope the suite would consult the developer's
// live ~/.macguffin — and a resolver test whose answer depends on which work
// items happen to exist on the machine is exactly the instrument this package
// warns about, inside the package that warns about it.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("prtracking")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestStoreIsSandboxed is the positive control for the envelope: the resolver's
// `mg show` must not be able to reach the live store. It is asserted the only
// way that is true regardless of whether `mg` is installed on the runner —
// against the environment `mg` would read.
func TestStoreIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	for _, key := range []string{"HOME", "MG_ROOT"} {
		got := os.Getenv(key)
		if got == "" {
			t.Errorf("%s is unset, so `mg show` would fall back to the real store", key)
			continue
		}
		if !sandbox.Contains(got) {
			t.Errorf("%s = %s, want a path under the sandbox root %s", key, got, sandbox.Root)
		}
	}
}
