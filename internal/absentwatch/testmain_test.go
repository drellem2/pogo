package absentwatch

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
// No test here is SUPPOSED to touch live state — every one builds its Snapshot
// from a fixture, and the only live reader in the package (source.go) takes an
// explicit registry — but that is exactly the reasoning under which mg-6092,
// mg-e8e7, mg-5336 and mg-3412 each shipped a suite that reached the real
// ~/.pogo anyway. This package's own subject matter is the difference between "I
// looked and found nothing" and "I never looked", so it is a poor place to rest
// on a claim nobody checks.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("absentwatch")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsPinned is the positive control for the isolation above: without
// it, the envelope is an unverified claim and dropping it would leave every
// other test in the package green.
func TestSandboxIsPinned(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
