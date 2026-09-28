package crewreset

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope. The package touches no files and
// every test injects its own Mail/Emit, but pinning HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT means a future test that forgets an injection mails a
// throwaway store, not the developer's live crew.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("crewreset")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

// TestPackageStateIsSandboxed is the positive control for that envelope.
func TestPackageStateIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
