package strandedmail

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any resolves onto the developer's live tree.
//
// MG_ROOT is the one that matters here. This package shells out to the REAL `mg`
// binary — that is deliberate, since the failure it guards against is mg's JSON
// contract drifting under us — and `mg mail send` CREATES the mailbox it is
// given. A suite running against the live root would deliver fixture mail into
// the fleet's actual maildir, inventing mailboxes named after this file's
// fixtures and, worse, marking real messages read: `mg mail read` on a live
// message is not reversible, and this package's whole subject is messages that
// go unread because nobody knows to look.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("strandedmail")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// mailRoot returns a fresh, per-test mail root INSIDE the sandbox. Per-test
// rather than per-package because each mg-backed test constructs a whole fleet
// state and asserts on the total set of findings — a shared root would let one
// test's fixture mail become another's stranded message.
func mailRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(sandbox.Root, "mail-"+t.Name())
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", root, err)
	}
	return root
}

// TestMgRootIsSandboxed is the positive control for the envelope above, and it
// is not ceremony: without it the isolation is an unverified claim, and every
// other test in this package would stay green while the suite went back to
// sending fixture mail into the operator's real ~/.macguffin.
func TestMgRootIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	root := os.Getenv("MG_ROOT")
	if root == "" {
		t.Fatal("MG_ROOT is unset under test — mg would fall back to ~/.macguffin, the live mail store")
	}
	if !sandbox.Contains(root) {
		t.Fatalf("MG_ROOT = %s, want a path under the sandbox root %s", root, sandbox.Root)
	}
	// And the per-test roots the mg-backed tests use must land inside it too.
	if got := mailRoot(t); !sandbox.Contains(got) {
		t.Fatalf("per-test mail root %s is outside the sandbox root %s", got, sandbox.Root)
	}
}
