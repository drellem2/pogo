package homevcs

import (
	"context"
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any of them resolves onto the developer's live tree.
//
// It matters more here than in most packages. This one's whole subject is the
// git repository that may be versioning $POGO_HOME — on the fleet host, a live
// working tree holding the mail spool, schedules and events.log. A test that
// resolved the real POGO_HOME would run git against it, and any future test
// that seeded a fixture would seed it THERE.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("homevcs")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestAuditReadsTheSandboxedHome is the positive control for the isolation
// above. Audit() takes no arguments — it resolves config.PogoHome() — so this
// is the assertion that the exported entry point, the one a future test will
// reach for first, cannot reach the developer's live ~/.pogo.
func TestAuditReadsTheSandboxedHome(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	rep := Audit(context.Background())
	if !sandbox.Contains(rep.Home) {
		t.Fatalf("Audit().Home = %s, want a path under the sandbox root %s; this suite would be running git against the fleet's live home",
			rep.Home, sandbox.Root)
	}
	if rep.Examined == 0 {
		t.Error("Examined = 0; a sandboxed home still has the same set of pogo-written paths to look for")
	}
}
