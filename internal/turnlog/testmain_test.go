package turnlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope. HOME, XDG_CONFIG_HOME, POGO_HOME
// and MG_ROOT are pinned under a throwaway root before any test runs.
//
// This package computes Dir() from config.PogoHome(), and Append() creates that
// directory and writes to it. Unisolated, a single careless test would append
// synthetic turn-completion lines into the developer's LIVE turnlog — which is
// worse here than the usual case: the live tree is read by a detector whose
// entire premise is that nothing but a completed turn produces those lines.
// Fixture lines in it would be indistinguishable from evidence.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("turnlog")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

// TestTurnLogDirIsSandboxed is the positive control for that envelope: the
// live-path accessors must resolve inside the throwaway root. Without it the
// isolation is an unverified claim, and the failure it prevents is silent —
// tests pass either way, and the damage lands in the operator's real evidence.
func TestTurnLogDirIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	if got := Dir(); !sandbox.Contains(got) {
		t.Errorf("Dir() = %s, want a path under the sandbox root %s", got, sandbox.Root)
	}
	if got := Path("mayor"); !sandbox.Contains(got) {
		t.Errorf("Path(mayor) = %s, want a path under the sandbox root %s", got, sandbox.Root)
	}
	// And a real write lands there rather than in ~/.pogo.
	if err := Append("probe-sandbox", "isolation check", time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(Dir(), "probe-sandbox.log")); err != nil {
		t.Errorf("Append did not write under the sandbox: %v", err)
	}
}
