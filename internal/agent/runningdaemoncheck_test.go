package agent

// A presence-and-uniqueness guard on the running-daemon check in the polecat
// template (mg-e785).
//
// WHAT IT GUARDS. mg-96ad reported itself BLOCKED at submit by a gate that was
// never in force: it read the gate's source on `origin/main`, concluded a
// submit would be refused, and waited for another work item instead. The gate
// was merged at 47b5d48; the pogod actually running was 023fab5, which does not
// contain it. A submit would have gone through at any point that day. The
// polecat reasoned from what the source SAYS to what the daemon DOES, and the
// two disagreed.
//
// WHY A TEST. The line was not missing from the fleet's knowledge — the same
// sentence had already been written into mg-91cc's dispatch body, and mg-91cc
// behaved correctly. The difference between the two polecats was whether the
// dispatcher remembered to type it. A template line reaches every future
// polecat and depends on nobody; a test is what keeps it there.
//
// WHY UNIQUENESS IS PART OF THE GUARD. Prompts are written by copying prompts.
// A rule restated in two templates goes stale in one of them, and a polecat
// reading the stale copy is back to reasoning from text instead of from the
// daemon. So the allowed count is exactly one, and this test names which file.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// theHome is the single shipped prompt that carries the rule.
const theHome = "templates/polecat.md"

// claimMarker identifies the rule itself. Deliberately the method's own words
// and not any control's name: the rule has to outlive every gate it is about —
// a sentence naming the mg-96ad gate would have been stale within six hours,
// because that gate was reverted the same day (mg-8cfe).
const claimMarker = "merged control will refuse you"

// The instrument, checked separately from the claim because it is the half
// that stays true while the prose around it drifts. A polecat that is told to
// "check the daemon" and not given the two commands has been told to go look,
// which is not the same as being able to.
var instrumentMarkers = []string{
	"/version",
	"jq -r .revision",
	"merge-base --is-ancestor",
}

// homesFor returns the prompt files carrying the rule, in walk order.
func homesFor(root fs.FS, marker string) ([]string, error) {
	var found []string
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), marker) {
			found = append(found, path)
		}
		return nil
	})
	return found, err
}

// TestRunningDaemonCheck_HasExactlyOneHome is the standing guard: the rule is
// in the polecat template, and only there.
func TestRunningDaemonCheck_HasExactlyOneHome(t *testing.T) {
	homes, err := homesFor(os.DirFS("prompts"), claimMarker)
	if err != nil {
		t.Fatalf("walking prompts: %v", err)
	}
	switch {
	case len(homes) == 0:
		t.Fatalf("no shipped prompt tells a polecat to check the running daemon before "+
			"predicting a merged control will refuse you.\n"+
			"  It belongs in prompts/%s, beside the other \"verify the claim before acting on it\" guidance:\n"+
			"    Before predicting that a merged control will refuse you, check whether the running\n"+
			"    daemon carries it. If it is merged but not running, say so in your report and do not\n"+
			"    reason as though it were live.\n"+
			"  With the two commands beside it: %s", theHome, strings.Join(instrumentMarkers, ", "))
	case len(homes) > 1:
		t.Fatalf("the running-daemon rule is stated in %d prompts (%v), want exactly 1 (prompts/%s).\n"+
			"  Two statements of one rule go stale one at a time; delete all but the home.",
			len(homes), homes, theHome)
	case homes[0] != theHome:
		t.Fatalf("the running-daemon rule lives in prompts/%s, want prompts/%s", homes[0], theHome)
	}
}

// TestRunningDaemonCheck_CarriesTheInstrument pins the commands to the same
// file as the claim. Splitting them is the failure this ticket was filed
// against in miniature: mg-96ad did not fail to find the daemon, it never
// occurred to it to ask.
func TestRunningDaemonCheck_CarriesTheInstrument(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("prompts", theHome))
	if err != nil {
		t.Fatalf("reading prompts/%s: %v", theHome, err)
	}
	for _, m := range instrumentMarkers {
		if !strings.Contains(string(data), m) {
			t.Errorf("prompts/%s states the rule but omits %q — the polecat is told to look "+
				"without being told how", theHome, m)
		}
	}
}

// TestRunningDaemonCheck_PredicateCanFire is the refutation control. A guard
// that has only ever been observed passing is not evidence; both failure
// directions are exercised on a copy of the real prompt tree rather than on a
// hand-built fixture, so the check is shown to guard the corpus it names.
func TestRunningDaemonCheck_PredicateCanFire(t *testing.T) {
	t.Run("a second home is caught", func(t *testing.T) {
		dir := copyPromptTree(t)
		appendToTemplate(t, dir, "templates/polecat-qa.md",
			"\nBefore predicting that a merged control will refuse you, check the daemon.\n")
		homes, err := homesFor(os.DirFS(dir), claimMarker)
		if err != nil {
			t.Fatalf("walking poisoned tree: %v", err)
		}
		if len(homes) != 2 {
			t.Fatalf("duplicated rule produced %d homes, want 2: %v", len(homes), homes)
		}
	})

	t.Run("a deleted home is caught", func(t *testing.T) {
		dir := copyPromptTree(t)
		path := filepath.Join(dir, theHome) // copyPromptTree roots the copy at prompts/
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		stripped := strings.ReplaceAll(string(data), claimMarker, "")
		if err := os.WriteFile(path, []byte(stripped), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		homes, err := homesFor(os.DirFS(dir), claimMarker)
		if err != nil {
			t.Fatalf("walking stripped tree: %v", err)
		}
		if len(homes) != 0 {
			t.Fatalf("stripped tree still reports homes: %v", homes)
		}
	})
}
