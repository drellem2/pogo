package cursor

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// TestMain takes this package's test binary off the PRODUCTION sentinel-drift
// alert sink before a single test runs (mg-54f8).
//
// trust_hook_race_test.go drives the REAL hook loop (watchForTrustDialog) against
// a real Agent on a real PTY, and every arm of that loop calls
// agent.RecordTrustDialogReady. That feeds the process-global drift detector, and
// when the `cursor/trust-dialog` key's windowed miss fraction crosses the
// threshold, the default sink emits a durable sentinel_drift event AND mails the
// fleet coordinator — deliberately, because nobody reads our logs. Neither of
// those belongs to a `go test` run, and unlike an event-log write the mail lands
// in a human's inbox: the blast radius is the fleet rather than the package.
//
// This package was safe only by arithmetic. The suite records one miss (the
// too-short-budget positive control) against three confirmations — 0.25 against a
// 0.5 threshold — so it sat, in the mg-d631 reviewer's words, "a couple of
// deadline-arm cases wide" of mailing the coordinator. A second deadline-arm
// fixture takes it to 2/5, a third to 3/6, and the detector is a process-global
// so `-count=N` accumulates in the same process rather than starting over. Adding
// a test here should not require doing that arithmetic.
//
// Deliberately narrow: this stubs the drift sink and nothing else. It does NOT
// establish a testsandbox envelope — the e2e tests here read the developer's real
// Cursor config, and this package's event-log isolation is a separate question
// from the mail.
func TestMain(m *testing.M) {
	restore := agent.StubDriftSinkForTesting()
	code := m.Run()
	restore()
	os.Exit(code)
}

// TestDriftSinkIsStubbed is the control for TestMain above: it re-proves the
// isolation from inside a test, so deleting the install cannot leave the package
// green while the suite goes back to being able to mail the coordinator.
//
// It asks the detector which sink it holds rather than driving misses until
// something fires. A control of the latter shape would, on the day the install
// got dropped, send the very mail it exists to prevent — see
// agent.DriftSinkIsProductionForTesting.
func TestDriftSinkIsStubbed(t *testing.T) {
	if agent.DriftSinkIsProductionForTesting() {
		t.Fatal("the process-global sentinel-drift sink is still the production " +
			"one: this package's tests drive the real trust-dialog hook loop, so a " +
			"run of deadline-arm cases can cross the drift threshold and mail the " +
			"fleet coordinator from a unit test. TestMain must call " +
			"agent.StubDriftSinkForTesting.")
	}
}
