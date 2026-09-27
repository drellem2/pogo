package wakewatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// Every seam here is injected, but production wiring shells out to `mg mail
// send` and types into live terminals; the sandbox makes sure a test that
// reached a real default by accident writes nowhere that matters.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("wakewatch")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

func TestSandboxIsInEffect(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
