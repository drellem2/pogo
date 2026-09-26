package codex

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
)

// TestMain takes this package's test binary off the PRODUCTION sentinel-drift
// alert sink and the production events.log before a single test runs. It
// mirrors internal/cursor/testmain_test.go (mg-54f8) for the same reason.
//
// trust_hook_race_test.go and trust_hook_label_test.go drive the REAL hook loop
// against a real Agent on a real PTY, and several of their fixtures end on the
// deadline arm or the refused arm on purpose. The deadline arm feeds the
// process-global drift detector, whose default sink emits sentinel_drift and
// mails the fleet coordinator; the refused arm emits trust_dialog_refused.
// Neither belongs to a `go test` run.
//
// Deliberately narrow, like cursor's: it does NOT establish a testsandbox
// envelope — this package is still on the adoption ledger.
func TestMain(m *testing.M) {
	restore := agent.StubDriftSinkForTesting()
	emitEvent = func(events.Event) {}
	code := m.Run()
	restore()
	os.Exit(code)
}

// TestDriftSinkIsStubbed is the control for TestMain above; see cursor's
// identically-named test for why it asks rather than drives misses.
func TestDriftSinkIsStubbed(t *testing.T) {
	if agent.DriftSinkIsProductionForTesting() {
		t.Fatal("the process-global sentinel-drift sink is still the production " +
			"one: this package's tests drive the real trust-dialog hook loop to its " +
			"deadline arm, which can cross the drift threshold and mail the fleet " +
			"coordinator from a unit test. TestMain must call " +
			"agent.StubDriftSinkForTesting.")
	}
}
