package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/events"
)

// investigationsCorpus writes a two-file corpus with a README that indexes only
// one of them — the shape of the real directory, and the reason this command
// searches files.
func investigationsCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("README.md", "# Investigations\n\n| Doc | Covers |\n|---|---|\n| [indexed.md](indexed.md) | drains |\n")
	write("indexed.md", "# The drain predicate\n\nThe drain waits on unmerged branches.\n")
	write("unindexed.md", "# The registry is absent while alive\n\nA dark polecat holds a PTY.\n")
	return dir
}

// captureEvents points the events writer at a private log for the duration of
// fn and returns everything of this command's type that landed in it.
func captureEvents(t *testing.T, fn func()) []events.Event {
	t.Helper()
	log := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(log)
	t.Cleanup(func() { events.SetLogPathForTesting("") })

	fn()

	if _, err := os.Stat(log); os.IsNotExist(err) {
		return nil
	}
	got, err := events.ReadFiltered(log, events.Filter{Type: EventTypeInvestigationSearch})
	if err != nil {
		t.Fatalf("read %s: %v", log, err)
	}
	return got
}

// TestInvestigations_EmitsAnEventWhenNothingMatched is the load-bearing test of
// mg-22c7, and it guards the MEASUREMENT rather than the feature.
//
// This command is phase 1 of a gated decision: phase 2 (wiring the search into
// `mg new` and the dispatch template) is justified only if phase 1 is built and
// goes unused, because that is what would show the gap is recall rather than
// friction. Nothing on this box records a CLI invocation — every event type in
// the log is daemon-side — so without this emission the branch that JUSTIFIES
// phase 2 produces no artifact at all, and silence is indistinguishable from
// "nobody has needed it yet". A measurement whose negative result is
// unobservable is the exact defect this ticket is about; it was caught once
// already, in the instrument designed to catch it.
//
// The zero-match case is singled out because it is the one most likely to be
// dropped as uninteresting, and it is the most informative record the command
// can leave: somebody had a question and the corpus did not answer it.
func TestInvestigations_EmitsAnEventWhenNothingMatched(t *testing.T) {
	dir := investigationsCorpus(t)

	got := captureEvents(t, func() {
		captureStdout(t, func() {
			runInvestigations([]string{"nothing-matches-this"}, dir, 0, 3, false, false)
		})
	})

	if len(got) != 1 {
		t.Fatalf("a search that matched nothing emitted %d events, want 1 — "+
			"the branch that justifies phase 2 must not be silent", len(got))
	}
	d := got[0].Details
	if d["outcome"] != "no_match" {
		t.Errorf("outcome = %v, want no_match", d["outcome"])
	}
	if d["query"] != "nothing-matches-this" {
		t.Errorf("query = %v, want the terms searched", d["query"])
	}
	// files_searched is what makes a zero legible: a no_match over 46 files and
	// a no_match over an empty directory are different facts.
	if n, ok := d["files_searched"].(float64); !ok || int(n) != 2 {
		t.Errorf("files_searched = %v, want 2 — a zero result without its denominator is unreadable", d["files_searched"])
	}
	if got[0].Agent == "" {
		t.Error("event carries no agent; the gate needs to know who searched")
	}
}

func TestInvestigations_EmitsAnEventOnAMatch(t *testing.T) {
	dir := investigationsCorpus(t)

	got := captureEvents(t, func() {
		captureStdout(t, func() {
			runInvestigations([]string{"registry"}, dir, 0, 3, false, false)
		})
	})

	if len(got) != 1 {
		t.Fatalf("emitted %d events, want exactly 1 per invocation", len(got))
	}
	d := got[0].Details
	if d["outcome"] != "matched" {
		t.Errorf("outcome = %v, want matched", d["outcome"])
	}
	if n, ok := d["matches"].(float64); !ok || int(n) != 1 {
		t.Errorf("matches = %v, want 1", d["matches"])
	}
	// The corpus a search ran against is part of the record: an invocation from
	// a polecat worktree during this command's own construction is not evidence
	// that anyone reached for it in anger, and the gate has to be able to tell.
	if d["corpus_dir"] == "" || d["corpus_dir"] == nil {
		t.Error("corpus_dir missing; build-time invocations become indistinguishable from use")
	}
}

// TestInvestigations_EmitsOnceForTheListingMode — no query is still a use, and
// it is the mode most likely to be treated as "not a real search".
func TestInvestigations_EmitsOnceForTheListingMode(t *testing.T) {
	dir := investigationsCorpus(t)

	got := captureEvents(t, func() {
		captureStdout(t, func() { runInvestigations(nil, dir, 0, 3, false, false) })
	})

	if len(got) != 1 {
		t.Fatalf("listing emitted %d events, want 1", len(got))
	}
	if got[0].Details["query"] != "" {
		t.Errorf("query = %v, want empty for a listing", got[0].Details["query"])
	}
}

// exitCallRe finds direct process-exit calls in the command's source.
var exitCallRe = regexp.MustCompile(`cli\.ExitWithError\(|os\.Exit\(`)

// TestInvestigations_NoExitPathSkipsTheEvent pins the single funnel. Every
// non-zero exit goes through failInvestigations, which emits first — so a later
// edit cannot reintroduce a quiet failure by adding one more error branch, and
// the reader of the invocation count never has to wonder whether the attempts
// that failed were counted.
//
// This is a source-level guard because the exit paths call os.Exit and cannot
// be driven in-process. It asserts the SHAPE that makes the runtime guarantee
// true, which is the part an edit would break.
func TestInvestigations_NoExitPathSkipsTheEvent(t *testing.T) {
	src, err := os.ReadFile("investigations.go")
	if err != nil {
		t.Fatalf("read investigations.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	inFunnel := false
	var offenders []string
	for i, ln := range lines {
		if strings.HasPrefix(ln, "func failInvestigations(") {
			inFunnel = true
		} else if strings.HasPrefix(ln, "func ") {
			inFunnel = false
		}
		if inFunnel || !exitCallRe.MatchString(ln) {
			continue
		}
		offenders = append(offenders, "investigations.go:"+strconv.Itoa(i+1)+": "+strings.TrimSpace(ln))
	}
	if len(offenders) > 0 {
		t.Fatalf("exit paths outside failInvestigations, which would leave a failed "+
			"invocation unrecorded:\n  %s", strings.Join(offenders, "\n  "))
	}
	if !strings.Contains(string(src), "func failInvestigations(") {
		t.Fatal("failInvestigations is gone; there is no longer a funnel that emits before exiting")
	}
}

// TestInvestigations_EventTypeIsDocumented. The catalog in docs/event-log.md is
// how the reader at the 30-day gate learns this event exists and what its
// details mean. An event type nobody can look up is half an artifact.
func TestInvestigations_EventTypeIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "event-log.md"))
	if err != nil {
		t.Skipf("event-log.md not readable from here: %v", err)
	}
	if !strings.Contains(string(doc), EventTypeInvestigationSearch) {
		t.Fatalf("%s is emitted but absent from docs/event-log.md", EventTypeInvestigationSearch)
	}
}

// TestInvestigations_ZeroResultStatesItsDenominator: the printed output of an
// empty search must say what it searched and that a zero is a fact about the
// files. An unqualified "0 results" from an instrument is how a discoverability
// gap becomes a confident "no investigation exists".
func TestInvestigations_ZeroResultStatesItsDenominator(t *testing.T) {
	dir := investigationsCorpus(t)

	var out string
	captureEvents(t, func() {
		out = captureStdout(t, func() {
			runInvestigations([]string{"nothing-matches-this"}, dir, 0, 3, false, false)
		})
	})

	if !strings.Contains(out, "Searched 2 files") {
		t.Errorf("empty result does not state its denominator:\n%s", out)
	}
	if !strings.Contains(out, "not README.md") {
		t.Errorf("output does not say the search domain was the files:\n%s", out)
	}
	if !strings.Contains(out, "not about whether an") {
		t.Errorf("empty result does not warn that a zero is a statement about the text:\n%s", out)
	}
}

// TestInvestigations_ReportsIndexCoverageWithoutFilteringOnIt. Coverage is a
// diagnostic; the search domain is every file.
func TestInvestigations_ReportsIndexCoverageWithoutFilteringOnIt(t *testing.T) {
	dir := investigationsCorpus(t)

	var out string
	captureEvents(t, func() {
		out = captureStdout(t, func() { runInvestigations([]string{"registry"}, dir, 0, 3, false, false) })
	})

	if !strings.Contains(out, "unindexed.md") {
		t.Errorf("a file absent from README.md was not returned:\n%s", out)
	}
	if !strings.Contains(out, "absent from the index") {
		t.Errorf("index coverage is not reported:\n%s", out)
	}
}
