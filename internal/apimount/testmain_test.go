package apimount

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
// This package's own tests touch nothing outside an in-memory http.ServeMux, so
// the envelope is inherited rather than needed. It is adopted anyway, and the
// ledger is right to insist: "this suite does not read live state" is a claim
// about today's tests, and the next test written here — one that stands up a
// listener, or reaches for a config to find a server URL — would silently be
// the exception. That is the same mistake this package exists to prevent, one
// layer down: a property that holds by inspection and is checked by nothing.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("apimount")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsInEffect is the positive control for the isolation above.
func TestSandboxIsInEffect(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
