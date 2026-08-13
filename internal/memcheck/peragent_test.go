package memcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// slugFor is a stand-in for a harness's path encoding. The real one lives in the
// provider; these tests must not depend on which harness is installed, so they
// supply their own and assert the SHAPE of the answer rather than any dotdir.
func slugFor(root string) func(string) []string {
	return func(workdir string) []string {
		if workdir == "" {
			return nil
		}
		var b strings.Builder
		for _, r := range workdir {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
				b.WriteRune(r)
			default:
				b.WriteByte('-')
			}
		}
		return []string{filepath.Join(root, "projects", b.String(), "memory", "MEMORY.md")}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// storeFor builds the store directory a harness would key on workdir, so a test
// stages fixtures through the same transform the code under test uses. Doing it
// by hand would let a test pass against a path the code never looks at.
func storeFor(t *testing.T, home, harnessRoot, workdir string) string {
	t.Helper()
	rel := slugFor(harnessRoot)(workdir)
	if len(rel) != 1 {
		t.Fatalf("fixture transform returned %d paths, want 1", len(rel))
	}
	return filepath.Dir(filepath.Join(home, rel[0]))
}

// TestSurveyFindsAPopulatedPerAgentStore is the POSITIVE CONTROL: it proves the
// check CAN report a finding before any test asserts that it doesn't. A detector
// whose only green tests are clean populations cannot be distinguished from one
// that has silently stopped looking — which is the exact defect this check
// exists to catch one level down.
func TestSurveyFindsAPopulatedPerAgentStore(t *testing.T) {
	home := t.TempDir()
	agentRoot := filepath.Join(home, ".pogo", "agents")
	workdir := filepath.Join(agentRoot, "architect")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}

	store := storeFor(t, home, ".harness", workdir)
	write(t, filepath.Join(store, "MEMORY.md"), "- [a](a.md) — hook\n")
	write(t, filepath.Join(store, "a.md"), "note a\n")
	write(t, filepath.Join(store, "b.md"), "note b\n")

	sv, err := SurveyPerAgentStores(home, agentRoot, slugFor(".harness"))
	if err != nil {
		t.Fatal(err)
	}
	if !sv.Measured() {
		t.Fatalf("survey reported unmeasured with a store present: %+v", sv)
	}
	stranded := sv.Stranded()
	if len(stranded) != 1 {
		t.Fatalf("stranded = %d, want 1: %+v", len(stranded), sv.Stores)
	}
	if stranded[0].Agent != "architect" {
		t.Errorf("agent = %q, want architect", stranded[0].Agent)
	}
	if got := len(stranded[0].Notes); got != 2 {
		t.Errorf("notes = %d, want 2 (%v)", got, stranded[0].Notes)
	}
	if stranded[0].Newest.IsZero() {
		t.Error("newest note time is zero with two notes present")
	}
}

// TestAnEmptiedStoreIsNotStranded pins the retirement shape. A store left
// holding only its index is how a tombstone survives, so reporting it would make
// the correct remedy permanently noisy — and a permanently noisy check gets
// tuned out along with its real findings.
func TestAnEmptiedStoreIsNotStranded(t *testing.T) {
	home := t.TempDir()
	agentRoot := filepath.Join(home, ".pogo", "agents")
	workdir := filepath.Join(agentRoot, "mayor")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}

	store := storeFor(t, home, ".harness", workdir)
	write(t, filepath.Join(store, "MEMORY.md"), "# TOMBSTONE — retired, do not write here\n")

	sv, err := SurveyPerAgentStores(home, agentRoot, slugFor(".harness"))
	if err != nil {
		t.Fatal(err)
	}
	if !sv.Measured() {
		t.Fatal("survey reported unmeasured with a store present")
	}
	if len(sv.Stores) != 1 {
		t.Fatalf("stores = %d, want 1", len(sv.Stores))
	}
	if got := sv.Stranded(); len(got) != 0 {
		t.Errorf("stranded = %d, want 0 for a tombstoned store: %+v", len(got), got)
	}
}

// TestIndexAndArchiveFilesAreNotNotes covers the two exclusions that decide
// whether the retirement shape is reachable at all. MEMORY.md is the index; a
// `_`-prefixed file is this corpus's convention for a secondary index. Counting
// either would mean a correctly-emptied store never goes quiet.
func TestIndexAndArchiveFilesAreNotNotes(t *testing.T) {
	home := t.TempDir()
	agentRoot := filepath.Join(home, ".pogo", "agents")
	workdir := filepath.Join(agentRoot, "doctor")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}

	store := storeFor(t, home, ".harness", workdir)
	write(t, filepath.Join(store, "MEMORY.md"), "index\n")
	write(t, filepath.Join(store, "_index-archive-2026-08-12.md"), "archive\n")
	write(t, filepath.Join(store, "notes.txt"), "not markdown\n")

	sv, err := SurveyPerAgentStores(home, agentRoot, slugFor(".harness"))
	if err != nil {
		t.Fatal(err)
	}
	if got := sv.Stranded(); len(got) != 0 {
		t.Fatalf("stranded = %d, want 0: %+v", len(got), got)
	}
}

// TestRetiredOriginalsInASubdirectoryDoNotCount is the regression that makes the
// remedy converge. The triage that motivated this check moved 153 originals into
// a `_retired-<date>/<agent>/` subdirectory of the SHARED store — but the same
// shape could land under a per-agent store, and a recursive walk would then
// report a store as populated forever after it was correctly emptied.
func TestRetiredOriginalsInASubdirectoryDoNotCount(t *testing.T) {
	home := t.TempDir()
	agentRoot := filepath.Join(home, ".pogo", "agents")
	workdir := filepath.Join(agentRoot, "pm-pogo")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}

	store := storeFor(t, home, ".harness", workdir)
	write(t, filepath.Join(store, "MEMORY.md"), "# TOMBSTONE\n")
	write(t, filepath.Join(store, "_retired-2026-08-13", "pm-pogo", "old.md"), "archived original\n")

	sv, err := SurveyPerAgentStores(home, agentRoot, slugFor(".harness"))
	if err != nil {
		t.Fatal(err)
	}
	if got := sv.Stranded(); len(got) != 0 {
		t.Fatalf("stranded = %d, want 0 — archived originals are not live notes: %+v", len(got), got)
	}
}

// TestUnmeasuredIsNotClean is the clause that keeps a blind check from reporting
// health. A provider that names no store, or an agent root with no agent
// directories, must produce Measured()==false — because zero findings from a
// probe that never ran looks exactly like zero findings from a healthy fleet.
func TestUnmeasuredIsNotClean(t *testing.T) {
	home := t.TempDir()
	agentRoot := filepath.Join(home, ".pogo", "agents")
	if err := os.MkdirAll(filepath.Join(agentRoot, "architect"), 0o755); err != nil {
		t.Fatal(err)
	}

	none := func(string) []string { return nil }
	sv, err := SurveyPerAgentStores(home, agentRoot, none)
	if err != nil {
		t.Fatal(err)
	}
	if sv.Measured() {
		t.Error("Measured() true when no provider named a store")
	}
	if len(sv.Stranded()) != 0 {
		t.Error("stranded findings from a survey that probed nothing")
	}

	// And the same answer when the root itself is absent: nothing to say about a
	// machine that has never run an agent, reported as unmeasured rather than as
	// an error or a clean bill.
	sv, err = SurveyPerAgentStores(home, filepath.Join(home, "nope"), slugFor(".harness"))
	if err != nil {
		t.Fatalf("missing agent root should not error: %v", err)
	}
	if sv.Measured() {
		t.Error("Measured() true for a nonexistent agent root")
	}
}

// TestAConstructedPathThatDoesNotExistStillCounts pins the direction the check
// fails in. A wrong path model yields probes that match nothing — those still
// increment Candidates, so the run reports "checked and clean" rather than
// "measured nothing", and a reader can tell a blind check from an idle one only
// because the denominator is printed.
func TestAConstructedPathThatDoesNotExistStillCounts(t *testing.T) {
	home := t.TempDir()
	agentRoot := filepath.Join(home, ".pogo", "agents")
	for _, name := range []string{"architect", "mayor"} {
		if err := os.MkdirAll(filepath.Join(agentRoot, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	sv, err := SurveyPerAgentStores(home, agentRoot, slugFor(".harness"))
	if err != nil {
		t.Fatal(err)
	}
	if sv.Candidates != 2 {
		t.Errorf("candidates = %d, want 2 (one per agent directory)", sv.Candidates)
	}
	if !sv.Measured() {
		t.Error("Measured() false after probing two candidate paths")
	}
	if len(sv.Stores) != 0 {
		t.Errorf("stores = %d, want 0 — none of the constructed paths exist", len(sv.Stores))
	}
}

// TestNonAgentDirectoriesAreHarmless documents why the enumeration is
// deliberately unfiltered. pogo's agent root also holds templates/, sockets/ and
// similar; filtering to the configured roster would blind the check to its most
// likely finding — the store of an agent that has since been removed from the
// config, which is precisely the store nobody is going to notice.
func TestNonAgentDirectoriesAreHarmless(t *testing.T) {
	home := t.TempDir()
	agentRoot := filepath.Join(home, ".pogo", "agents")
	for _, name := range []string{"templates", "sockets", "retired-agent"} {
		if err := os.MkdirAll(filepath.Join(agentRoot, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(agentRoot, "stray-file.md"), "not a directory\n")

	// Only the removed agent has a store, and it is exactly the one to report.
	store := storeFor(t, home, ".harness", filepath.Join(agentRoot, "retired-agent"))
	write(t, filepath.Join(store, "MEMORY.md"), "index\n")
	write(t, filepath.Join(store, "kept.md"), "a note nobody owns any more\n")

	sv, err := SurveyPerAgentStores(home, agentRoot, slugFor(".harness"))
	if err != nil {
		t.Fatal(err)
	}
	if sv.Candidates != 3 {
		t.Errorf("candidates = %d, want 3 (directories only, not the stray file)", sv.Candidates)
	}
	stranded := sv.Stranded()
	if len(stranded) != 1 || stranded[0].Agent != "retired-agent" {
		t.Fatalf("stranded = %+v, want exactly retired-agent", stranded)
	}
}

// TestNilIndexerIsUnmeasured guards the composition point: memcheck must not
// name a harness, so the caller supplies the transform — and a caller that
// supplies nothing gets "unmeasured", never a panic and never a clean result.
func TestNilIndexerIsUnmeasured(t *testing.T) {
	sv, err := SurveyPerAgentStores(t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if sv.Measured() || len(sv.Stores) != 0 {
		t.Errorf("nil indexer produced %+v, want an unmeasured empty survey", sv)
	}
}
