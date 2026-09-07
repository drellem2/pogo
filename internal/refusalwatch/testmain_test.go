package refusalwatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope. Two reasons it is not optional
// here: the default events.Emit writes to the real ~/.pogo/events.log, where
// refusal_streak_* fixture events would be indistinguishable from findings about
// the real fleet; and MailSink resolves the LIVE ~/.macguffin when no Root is
// given, so a test that forgot an override would post fixture alarms into
// Daniel's actual `human` mailbox and raise a real notification.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("refusalwatch")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

func TestPackageStateIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !sandbox.Contains(home) {
		t.Errorf("HOME = %s, want a path under the sandbox root %s", home, sandbox.Root)
	}
	// The live-store guard, stated as an assertion rather than a convention: if
	// this ever resolves outside the sandbox, an unrooted MailSink in any test
	// below mails the real human.
	if root := DefaultMGRoot(); !sandbox.Contains(root) {
		t.Errorf("DefaultMGRoot() = %s, want a path under the sandbox root %s", root, sandbox.Root)
	}
	if led := DefaultLedgerPath(); !sandbox.Contains(led) {
		t.Errorf("DefaultLedgerPath() = %s, want a path under the sandbox root %s", led, sandbox.Root)
	}
}
