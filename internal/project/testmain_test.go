package project

import (
	"os"
	"testing"
)

// TestMain clears any ambient POGO_HOME (e.g. exported by the developer's
// shell) before the package's tests run. Since mg-3dc3 every pogo state path
// derives from $POGO_HOME (falling back to $HOME/.pogo), and most tests
// isolate themselves by re-pointing HOME at a temp dir — an inherited
// POGO_HOME would defeat that isolation and leak test writes into the real
// state dir. Tests that exercise POGO_HOME semantics set it explicitly via
// t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv("POGO_HOME")
	os.Exit(m.Run())
}
