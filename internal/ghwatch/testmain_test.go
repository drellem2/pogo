package ghwatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// Every test here injects its store, its `mg` and its mail, but a regression
// that reached a real one must land in a sandbox, not in ~/.pogo or the live
// work-item store.
func TestMain(m *testing.M) {
	_, down := testsandbox.Main("ghwatch")
	code := m.Run()
	down()
	os.Exit(code)
}
