package refinery

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/events"
)

// testEventLogPath is the package-wide default events.log path used by every
// refinery test. TestMain redirects events.Emit here so any test that exercises
// a flow which calls events.Emit cannot accidentally append to the user's
// production ~/.pogo/events.log. Per-test helpers (e.g. useTempEventLog)
// restore to this value on cleanup rather than to "" so redirection persists
// across the whole test run.
var testEventLogPath string

// TestMain redirects the events log to a package-scoped temp directory before
// any test runs. This is belt-and-suspenders for test isolation: even tests
// that don't explicitly call useTempEventLog cannot leak refinery_* events
// into ~/.pogo/events.log. Without this, integration_test.go, api_test.go,
// refinery_test.go, etc. — all of which exercise Submit/processNext — would
// emit events to the user's production log on every `go test ./...` run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "refinery-events-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "refinery TestMain: mkdir temp: %v\n", err)
		os.Exit(1)
	}
	testEventLogPath = filepath.Join(dir, "events.log")
	events.SetLogPathForTesting(testEventLogPath)

	code := m.Run()

	events.SetLogPathForTesting("")
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
