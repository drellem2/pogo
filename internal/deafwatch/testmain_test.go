package deafwatch

import (
	"os"
	"testing"
)

// TestMain clears any ambient POGO_HOME (e.g. exported by the developer's
// shell) before this package's tests run, matching internal/ackwatch and
// internal/scheduler. No test here is supposed to touch live state at all —
// every one builds its Snapshot from a fixture, and the only live reader in the
// package (source.go) takes an explicit registry — but mg-6092, mg-e8e7 and
// mg-5336 are three tickets for tests that nevertheless reached the developer's
// real ~/.pogo, so the guard is cheap insurance against the fourth.
func TestMain(m *testing.M) {
	os.Unsetenv("POGO_HOME")
	os.Exit(m.Run())
}
