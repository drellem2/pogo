package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
)

// The acceptance test for drellem2/pogo#158, driven through the REAL `pogo gc
// --list-preserved` binary.
//
// WHY THE FULL BINARY AND NOT THE RENDERERS. The defect was never in what the
// renderers can produce — it was in WHEN the command hands anything over, and
// on which stream. internal/gitgc's tests assert that a scan delivers each tree
// as it resolves; only running the command asserts that the command is WIRED to
// that, that the listing lands on stdout and the trace on stderr, and that
// `--json` still parses. Those last two are the whole reason a human can watch
// a slow scan without breaking a machine consumer, and none of them is visible
// from inside the package.

// retainedFixture is newGCFixture's world with the polecat's tree made dirty,
// which is what makes it RETAINED — the population this listing exists for.
func retainedFixture(t *testing.T, name string) *gcFixture {
	t.Helper()
	f := newGCFixture(t, name)
	// Untracked, deliberately: it is on no branch, in no stash and on no
	// remote, so this tree is the only copy of its git objects — the row the
	// listing most has to reach the operator with.
	if err := os.WriteFile(filepath.Join(f.worktree, "only-copy.go"),
		[]byte("package p // exists nowhere else\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestGCListPreservedStreamsToStdoutAndTracesToStderr is the bar.
//
// #158 reports `pogo gc --list-preserved` producing no output and never
// returning. The scan was fully buffered — first byte only after the last tree
// — so a slow scan and a hung one were the same experience, and neither named
// the tree responsible. This pins the three properties that make the difference:
// the listing streams, the trace names each tree before it is read, and a
// finished listing says so.
func TestGCListPreservedStreamsToStdoutAndTracesToStderr(t *testing.T) {
	f := retainedFixture(t, "beef")

	stdout, stderr, code := f.runGC("--list-preserved")
	if code != 0 {
		t.Fatalf("gc --list-preserved exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	// --- the listing, on stdout, in order -------------------------------
	head := strings.Index(stdout, "retained polecat worktrees under")
	row := strings.Index(stdout, f.worktree)
	// The tail form, not the bare sentinel: the header NAMES the sentinel in
	// its warning, which is the point of it, so a bare search finds the
	// warning rather than the claim of completeness.
	tail := strings.Index(stdout, "scan complete — ")
	if head < 0 || row < 0 || tail < 0 {
		t.Fatalf("stdout is missing header(%d)/tree(%d)/completion(%d):\n%s", head, row, tail, stdout)
	}
	if !(head < row && row < tail) {
		t.Errorf("stdout must read header -> trees -> counts (got %d, %d, %d). The counts cannot "+
			"come first: they are not known until the scan ends, and holding the trees back for "+
			"them is the defect.\n%s", head, row, tail, stdout)
	}
	if !strings.Contains(stdout, "only-copy.go") {
		t.Errorf("the streamed row must carry the untracked file — that file is the reason the "+
			"tree is retained:\n%s", stdout)
	}
	if !strings.Contains(stdout, "IF THIS OUTPUT ENDS WITHOUT") {
		t.Errorf("the header must warn, before there is anything to misread, that a truncated "+
			"listing is partial:\n%s", stdout)
	}

	// --- the trace, on stderr, naming the tree --------------------------
	if !strings.Contains(stderr, "[1/1] beef") {
		t.Errorf("stderr must name each tree as the scan reaches it — that line is what tells an "+
			"operator WHICH tree a stalled scan is stuck on:\n%s", stderr)
	}
	if !strings.Contains(stderr, "retained") {
		t.Errorf("stderr must say what each directory resolved to:\n%s", stderr)
	}

	// --- and the two streams stay separate ------------------------------
	// Not tidiness: it is what keeps `> inventory.txt` a listing rather than a
	// listing with a progress bar sewn through it, and what lets --json below
	// stay parseable while a human still watches the scan.
	if strings.Contains(stdout, "[1/1]") {
		t.Errorf("the progress trace leaked into stdout:\n%s", stdout)
	}
	if strings.Contains(stderr, "NOTHING BELOW IS A VERDICT") {
		t.Errorf("the listing leaked into stderr:\n%s", stderr)
	}
}

// TestGCListPreservedJSONStaysOneDocumentWhileStderrTraces is the constraint
// that decided where the trace goes.
//
// `--json` has a named consumer that parses it with sed and no jq
// (scripts/pogo-self-deploy), so a progress line on stdout would break it — and
// the version of this fix that streams progress into stdout would have had to
// choose between showing a human where the scan is and keeping the machine
// output readable. Putting the trace on stderr means neither has to lose.
func TestGCListPreservedJSONStaysOneDocumentWhileStderrTraces(t *testing.T) {
	f := retainedFixture(t, "cafe")

	stdout, stderr, code := f.runGC("--list-preserved", "--json")
	if code != 0 {
		t.Fatalf("gc --list-preserved --json exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	var rep gitgc.PreservedReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("--json stdout is not one parseable document (%v):\n%s", err, stdout)
	}
	if rep.RetainedCount != 1 || len(rep.Retained) != 1 || rep.Retained[0].Owner != "cafe" {
		t.Fatalf("--json payload = %+v, want the one retained tree", rep)
	}
	if rep.Retained[0].Untracked != 1 {
		t.Errorf("--json payload lost the untracked count: %+v", rep.Retained[0])
	}
	if !strings.Contains(stderr, "[1/1] cafe") {
		t.Errorf("a --json run must still trace to stderr — a machine consumer redirects it away, "+
			"and a human running --json interactively is the one who most needs it:\n%s", stderr)
	}
}
