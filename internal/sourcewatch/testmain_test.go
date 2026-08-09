package sourcewatch

import (
	"os"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established before a
// single test runs. See internal/testsandbox.
//
// This package needs it more than most. Everything here is a reader of the live
// machine by construction: Discover lists ~/Library/LaunchAgents, sampleDir
// stats and lists whatever directories those plists name, and
// DefaultLaunchAgentsDir resolves off the user's home. Every test below passes
// an explicit t.TempDir() and none of them calls the live default — but that is
// isolation-by-remembering, which is precisely what mg-6092, mg-e8e7, mg-5336
// and mg-3412 each were, all four written by authors who did not intend to read
// live state at all. The next test added to this file inherits the envelope
// instead of having to remember.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("sourcewatch")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestDefaultLaunchAgentsDirIsSandboxed is the positive control for the
// isolation above, and it is not a formality here: DefaultLaunchAgentsDir is
// the single function in this package that reaches the real machine, and it is
// what the doctor row calls in production. Without this test the envelope is an
// unverified claim — dropping it would leave every other test in the package
// green while a future one audited the operator's live notifier and reported on
// their real mailboxes.
func TestDefaultLaunchAgentsDirIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	got := DefaultLaunchAgentsDir()
	if got == "" {
		t.Fatal("DefaultLaunchAgentsDir() = \"\"; the sandbox should still provide a home")
	}
	if !sandbox.Contains(got) {
		t.Errorf("DefaultLaunchAgentsDir() = %s, want a path under the sandbox root %s; "+
			"a test that called Audit with the default would read the operator's live LaunchAgents", got, sandbox.Root)
	}
	if !strings.HasSuffix(got, "LaunchAgents") {
		t.Errorf("DefaultLaunchAgentsDir() = %s, want it to still name the LaunchAgents directory", got)
	}
}
