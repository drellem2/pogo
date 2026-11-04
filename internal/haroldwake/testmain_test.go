package haroldwake

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox pins HOME, XDG_CONFIG_HOME, POGO_HOME and MG_ROOT for the package.
// KnownAgents reads the prompt tree under POGO_HOME and MGBinary looks under
// HOME, so an unpinned run would answer from the developer's live fleet.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("haroldwake")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

func TestStateRootIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)
	if got := config.PogoHome(); !sandbox.Contains(got) {
		t.Errorf("PogoHome() = %s, want a path under the sandbox root %s", got, sandbox.Root)
	}
}
