package mailbox

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any of them resolves onto the developer's live tree.
//
// This package is pure string handling today — Canonical and ListInvocations
// touch no disk and read no environment — so nothing here needs the envelope
// yet. It is established anyway, because the adoption ratchet is right about
// where this package is heading: it holds the tree's one answer to "which
// mailbox is this?", and the obvious next assertion is one that resolves a name
// against the real maildir under $HOME/.macguffin/mail. A test written that way
// in an unisolated package would read the developer's live mail — 1146 boxes on
// this machine — and pass or fail on whatever happened to be sitting in them.
//
// Adopting the isolation at the package's first commit costs nothing and means
// the next test written here inherits it rather than having to notice.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("mailbox")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsInEffect is the positive control for the envelope above. Without
// it the isolation is an unverified claim: TestMain could stop pinning HOME
// entirely and every other test in this package would stay green, because none
// of them touch the filesystem — which is exactly the state in which somebody
// adds the first test that does.
func TestSandboxIsInEffect(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if !sandbox.Contains(home) {
		t.Errorf("HOME resolves to %s, outside the sandbox root %s; a test that read the "+
			"maildir would be reading the developer's live mail", home, sandbox.Root)
	}
}
