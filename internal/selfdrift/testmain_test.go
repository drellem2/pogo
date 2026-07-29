package selfdrift

import (
	"os"
	"testing"
)

// TestMain clears the environment variables this package reads before any test
// runs. POGO_REPO and POGO_GOBIN are the same variables scripts/pogo-self-deploy
// honors, so a developer running these tests on the box that operates the fleet
// is exactly the person likely to have them exported — and an inherited value
// would silently redirect ResolveRepo/InstalledBin at their live checkout and
// live ~/go/bin, turning a hermetic test into a reading of the host. Tests that
// exercise these variables set them explicitly with t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv("POGO_REPO")
	os.Unsetenv("POGO_GOBIN")
	os.Exit(m.Run())
}
