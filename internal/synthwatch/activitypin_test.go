package synthwatch

import (
	"testing"

	"github.com/drellem2/pogo/internal/events"
)

// TestDetectedIsExcludedFromAgentActivity pins this package's event name
// against the liveness exclusion list that has to know it.
//
// internal/events deliberately holds "synthetic_failure_detected" as a LITERAL
// rather than importing this package: a general-purpose log writer should not
// depend on a detector, and the import would be backwards. The cost of that
// correct decision is that the two strings are joined by nothing, so renaming
// EventDetected here would silently re-open drellem2/pogo#138 for this event
// type — the reading would go back to getting fresher exactly when the agent's
// turns are failing.
//
// The pin lives HERE, in the package that owns the name, because this is where
// a rename happens and this is the suite that would run. It costs internal/events
// no dependency at all.
func TestDetectedIsExcludedFromAgentActivity(t *testing.T) {
	if events.CountsAsAgentActivity(EventDetected) {
		t.Fatalf("%q counts as agent activity in the liveness indexes. It is recorded under the "+
			"FAILING agent's identity and fires BECAUSE its turns are failing, so counting it "+
			"reports the agent as freshly alive precisely when it is worst off. If this name was "+
			"just renamed, update the exclusion set in internal/events/activity.go to match",
			EventDetected)
	}
}

// TestClearedStillCountsAsAgentActivity is the other half, and it is a real
// distinction rather than symmetry for its own sake.
//
// `synthetic_failure_cleared` is emitted only on a POSITIVE reading — clear()
// runs for StateQuiet and never for StateUnavailable, precisely so that "could
// not look" is not mistaken for recovery. That makes it evidence drawn from the
// agent's own turns, which is exactly what a liveness index wants, and it must
// not be swept up by a future tidy-up that assumes everything synthwatch writes
// belongs on the exclusion list.
func TestClearedStillCountsAsAgentActivity(t *testing.T) {
	if !events.CountsAsAgentActivity(EventCleared) {
		t.Errorf("%q stopped counting as agent activity. Unlike EventDetected it is emitted only on a "+
			"positive reading of the agent's own turns, so it IS evidence of life", EventCleared)
	}
}
