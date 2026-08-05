package wedgewatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's private, CHECKED envelope, established before a
// single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME, POGO_HOME
// and MG_ROOT are pinned under a throwaway root, read back out of the process,
// and refused if any of them resolves onto the developer's live tree.
//
// No test here is supposed to touch live state — every one builds its
// Observations, its CredentialView and its PTY fixtures by hand, and the only
// live readers in the package (source.go) take an explicit registry and an
// injectable credential func. But mg-6092, mg-e8e7, mg-5336 and mg-3412 are
// four tickets for tests that nevertheless reached the developer's real state,
// and "this suite does not need it" is what each of those suites believed too.
//
// It matters more than usual here for one specific reason: SystemCredential
// shells out to macOS `security` against the developer's live Keychain, and a
// test that reached it could block on an authorization prompt — which
// internal/credexpiry gives a 10s timeout precisely because it happens. Nothing
// in this package may call it, and TestSandboxIsEstablished is the standing
// check that the envelope is real rather than remembered.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("wedgewatch")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsEstablished is the positive control for the isolation above. It
// is not a formality: without it the envelope is an unverified claim, and this
// package would go on passing while a future test read the developer's live
// ~/.pogo or their real credential.
func TestSandboxIsEstablished(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	if got := config.PogoHome(); !sandbox.Contains(got) {
		t.Errorf("config.PogoHome() = %s, want a path under the sandbox root %s", got, sandbox.Root)
	}
}
