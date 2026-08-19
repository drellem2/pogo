// This file is in the EXTERNAL test package on purpose. internal/claude imports
// internal/client, and internal/client imports this package for the
// /health/progress reading, so an in-package test that reached for
// claude.IncidentEpisodeClearedEvent would close an import cycle. An external
// test package sees only the exported surface, which is all this assertion
// needs — and pinning the contract is worth the extra file, because the string
// it pins is the one a notifier binds and a drift in it is silent.
package progresswatch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/claude"
	"github.com/drellem2/pogo/internal/progresswatch"
)

// TestEpisodeKindMatchesContract pins the locally-spelled event type against
// the one the notifier actually binds (mg-55b2 / mg-e0f6).
func TestEpisodeKindMatchesContract(t *testing.T) {
	if progresswatch.IncidentEpisodeClearedEvent != claude.IncidentEpisodeClearedEvent {
		t.Fatalf("local copy %q has drifted from the contract %q",
			progresswatch.IncidentEpisodeClearedEvent, claude.IncidentEpisodeClearedEvent)
	}
}

// TestEventTypesAreDocumented. The catalog in docs/event-log.md is the only
// place a reader of the log can learn what a type means, and an emitted type
// that is not in it is a line nobody can interpret. Cheap to keep, and the
// failure it prevents is silent.
func TestEventTypesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "event-log.md"))
	if err != nil {
		t.Skipf("event-log.md not readable from here: %v", err)
	}
	for _, ty := range []string{
		progresswatch.EventStalled,
		progresswatch.EventPending,
		progresswatch.EventCleared,
		progresswatch.EventError,
	} {
		if !strings.Contains(string(doc), ty) {
			t.Errorf("%s is emitted but absent from docs/event-log.md", ty)
		}
	}
}
