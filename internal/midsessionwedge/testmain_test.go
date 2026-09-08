package midsessionwedge

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope. HOME, XDG_CONFIG_HOME, POGO_HOME
// and MG_ROOT are pinned under a throwaway root before any test runs.
//
// Every dependency of the Watcher is injected and every test here supplies its
// own, so nothing reaches the live tree on purpose. That is precisely the
// situation the isolation exists for: New's default Emit writes to the real
// event log, so one future test that forgets an Emit would put
// midsession_wedge_fired fixtures into the live spine, where they are
// indistinguishable from findings about the real fleet — and this detector's
// findings name an agent and claim it is wedged.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("midsessionwedge")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

// TestWatcherStateIsSandboxed is the positive control for that envelope.
func TestWatcherStateIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !sandbox.Contains(home) {
		t.Errorf("HOME = %s, want a path under the sandbox root %s; a Watcher built "+
			"without an Emit would write midsession_wedge_* events into the live event log",
			home, sandbox.Root)
	}
}
