package promptcli

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT pinned under a throwaway root, read back out of the
// process, and refused if any of them resolves onto the developer's live tree.
//
// This package's own tests are pure — hand-built surfaces and string fixtures,
// no process started, no file read. The envelope is here for what the package
// DOES rather than what it currently tests: DiscoverBinary executes a real
// `mg`/`pogo`, and both of those resolve their store from the environment. `mg`
// reads MG_ROOT and otherwise ~/.macguffin; `pogo` reads POGO_HOME. A future
// test in this package that reaches for DiscoverBinary would run them against
// the developer's live work-item store unless something had already moved the
// floor — and this control's whole subject is a class of defect that only shows
// up at the moment of use, which is the worst possible time to discover that a
// test harness was pointed at production.
//
// The `--help` invocations DiscoverBinary makes are inert (cobra answers the
// help flag before any command body runs), so the envelope is belt to that
// braces rather than the only thing standing between this suite and the store.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("promptcli")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxEnvelope is the positive control for the isolation above. Without
// it the envelope is an unverified claim, and dropping TestMain would leave
// every other test in the package green.
func TestSandboxEnvelope(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
