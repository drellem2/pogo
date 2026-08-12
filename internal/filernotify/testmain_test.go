package filernotify

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
// Every seam this package has is injected — mail, the creator probe, the result
// probe, the registry probe — so nothing in the tests here touches disk or the
// environment today. The envelope is established anyway, and this package is a
// sharper case for it than most: its production wiring is client.SendMGMail,
// which shells out to `mg mail send` against $HOME/.macguffin/mail. A test that
// reached the real function by accident — a nil seam falling through to a
// default, a live-store test added later — would deliver mail into the
// operator's fleet, addressed to agents that are actually running and would
// actually read it. Adopting the isolation at the package's first commit costs
// nothing and means the next test written here inherits it.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("filernotify")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsInEffect is the positive control for the envelope above. Without
// it the isolation is an unverified claim: TestMain could stop pinning HOME and
// every other test here would stay green, because none of them touch the
// filesystem — which is exactly the state in which somebody adds the first one
// that does.
func TestSandboxIsInEffect(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if !sandbox.Contains(home) {
		t.Errorf("HOME resolves to %s, outside the sandbox root %s; a test that reached the real "+
			"mail sender would write into the operator's live maildir", home, sandbox.Root)
	}
}
