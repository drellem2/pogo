package revcheck

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope: HOME, XDG_CONFIG_HOME, POGO_HOME
// and MG_ROOT pinned under a throwaway root, read back out of the process, and
// refused if any of them resolves onto the developer's live tree.
//
// This package's own tests do not touch ~/.pogo directly — they compare strings
// and probe httptest servers. But BinaryRevision delegates to selfdrift.BinaryRev,
// whose path resolution reads POGO_GOBIN / PATH / GOBIN / GOPATH and can land on
// the developer's real ~/go/bin, and a caller added here later would inherit
// whatever envelope this file establishes. The isolation is not asked for
// per-test, it is the package's floor — which is the whole reason
// adoption-ledger.txt is a ratchet rather than a checklist.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("revcheck")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsInForce is the positive control: without it the isolation above
// is an unverified claim, and every other test in the package would stay green
// while the envelope silently stopped taking.
func TestSandboxIsInForce(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
