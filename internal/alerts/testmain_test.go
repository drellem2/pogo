package alerts

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's checked test envelope (internal/testsandbox).
// Nothing here should touch ~/.pogo or a live mailbox — the transport tests
// stub execCommand — and this is what makes that a checked claim rather than
// a hope: a send that slipped past the stub would land in a throwaway MG_ROOT.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("alerts")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

func TestStateRootIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
