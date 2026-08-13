package version

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope, established before a single test
// runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME, POGO_HOME and MG_ROOT
// are pinned under a throwaway root, read back out of the process, and refused
// if any resolves onto the developer's live tree.
//
// THIS PACKAGE'S TESTS DO NOT READ $HOME TODAY — resolve() is pure and the one
// test that is not (TestGetNeverReportsEmptyFields) reads only this binary's own
// build stamp. The envelope is here because the adoption ledger is a ratchet
// (mg-78a5): "this suite does not need it" is how every one of mg-6092 /
// mg-e8e7 / mg-5336 / mg-3412 started, and a package that is honest today is
// exactly the one where the next test gets written without a second thought.
// The cost is one TestMain; the thing it buys is that the question never has to
// be re-asked here.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("version")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsEstablished is the positive control for the envelope above:
// without it, deleting TestMain would leave every other test in this package
// green and the isolation silently gone.
func TestSandboxIsEstablished(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
