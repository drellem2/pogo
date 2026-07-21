package freshen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run executes a command in dir and fails the test on error.
func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes a file and commits it.
func commit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", file)
	run(t, dir, "git", "commit", "-m", msg)
}

// fixture builds a bare origin, a "publisher" clone used to push new commits,
// and the checkout under test (standing in for ~/.pogo/agents/<name>/repo).
type fixture struct {
	origin    string
	publisher string
	checkout  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()

	origin := filepath.Join(root, "origin.git")
	run(t, root, "git", "init", "--bare", "--initial-branch=main", origin)

	publisher := filepath.Join(root, "publisher")
	run(t, root, "git", "clone", origin, publisher)
	commit(t, publisher, "README.md", "v1\n", "initial")
	run(t, publisher, "git", "push", "-u", "origin", "main")

	checkout := filepath.Join(root, "checkout")
	run(t, root, "git", "clone", origin, checkout)

	return &fixture{origin: origin, publisher: publisher, checkout: checkout}
}

// advanceOrigin pushes n new commits to origin/main, leaving the checkout
// behind by exactly n. It does NOT fetch in the checkout — the whole point is
// that the checkout's local origin/main ref stays stale, exactly as a
// long-lived workspace's does.
func (f *fixture) advanceOrigin(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		commit(t, f.publisher, fmt.Sprintf("file%d.txt", i), fmt.Sprintf("c%d\n", i), fmt.Sprintf("commit %d", i))
	}
	run(t, f.publisher, "git", "push", "origin", "main")
}

func head(t *testing.T, dir string) string {
	t.Helper()
	return run(t, dir, "git", "rev-parse", "HEAD")
}

// --- Positive control -------------------------------------------------------

// TestPositiveControl_DetectorCanReportStale is the control the ticket
// requires: before trusting any "fresh" verdict, prove the detector is capable
// of returning a stale one. A check that structurally cannot report staleness
// would pass every fresh-case test in this file while being worthless.
//
// The dirty variant is used because it is the one case where the detector must
// report staleness WITHOUT resolving it — so the stale finding survives in the
// Result rather than being erased by the fix.
func TestPositiveControl_DetectorCanReportStale(t *testing.T) {
	f := newFixture(t)
	f.advanceOrigin(t, 7)

	// Make it dirty so the detector reports rather than repairs.
	if err := os.WriteFile(filepath.Join(f.checkout, "README.md"), []byte("local edit\n"), 0644); err != nil {
		t.Fatal(err)
	}

	res := Checkout(f.checkout)

	if !res.Stale() {
		t.Fatalf("positive control FAILED: detector did not report a 7-commit-behind checkout as stale: %+v", res)
	}
	if res.Behind != 7 {
		t.Errorf("Behind = %d, want 7 (an OID-derived count, not a guess): %+v", res.Behind, res)
	}
	if res.Status != StatusDeclinedDirty {
		t.Errorf("Status = %q, want %q", res.Status, StatusDeclinedDirty)
	}
}

// TestPositiveControl_CurrentCheckoutIsNotReportedStale is the negative half:
// the detector must not report staleness that isn't there, or the loud channel
// becomes noise and gets ignored — which is how the original bug survived.
func TestPositiveControl_CurrentCheckoutIsNotReportedStale(t *testing.T) {
	f := newFixture(t)

	res := Checkout(f.checkout)

	if res.Status != StatusAlreadyCurrent {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, StatusAlreadyCurrent, res)
	}
	if res.Stale() {
		t.Errorf("a current checkout reported Stale(): %+v", res)
	}
	if res.Behind != 0 {
		t.Errorf("Behind = %d, want 0", res.Behind)
	}
}

// --- The hard constraint: never clobber ------------------------------------

// TestDirtyStaleCheckoutIsDeclinedNotClobbered encodes the hard constraint. An
// automatic refresh that clobbers a dirty tree turns a silent staleness bug
// into silent DATA LOSS, which is strictly worse. Both the working-tree
// content and HEAD must be exactly as they were.
func TestDirtyStaleCheckoutIsDeclinedNotClobbered(t *testing.T) {
	f := newFixture(t)
	f.advanceOrigin(t, 3)

	precious := "work in progress nobody wants to lose\n"
	if err := os.WriteFile(filepath.Join(f.checkout, "README.md"), []byte(precious), 0644); err != nil {
		t.Fatal(err)
	}
	before := head(t, f.checkout)

	res := Checkout(f.checkout)

	if res.Status != StatusDeclinedDirty {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, StatusDeclinedDirty, res)
	}
	got, err := os.ReadFile(filepath.Join(f.checkout, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != precious {
		t.Errorf("CLOBBERED uncommitted work: README.md = %q, want %q", got, precious)
	}
	if after := head(t, f.checkout); after != before {
		t.Errorf("HEAD moved on a dirty tree: %s -> %s", before, after)
	}
	if !res.Stale() {
		t.Errorf("declined-dirty must still report Stale(): %+v", res)
	}
}

// TestStagedAddsAreDeclined is the shape of the actual incident: a checkout 99
// commits behind on an abandoned branch with 83 staged adds. Staged-but-
// uncommitted files are invisible to a naive `git status` eyeball and are
// exactly what a blind fast-forward would destroy.
func TestStagedAddsAreDeclined(t *testing.T) {
	f := newFixture(t)
	f.advanceOrigin(t, 4)

	for i := 0; i < 83; i++ {
		name := fmt.Sprintf("staged%02d.txt", i)
		if err := os.WriteFile(filepath.Join(f.checkout, name), []byte("x\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, f.checkout, "git", "add", ".")
	before := head(t, f.checkout)

	res := Checkout(f.checkout)

	if res.Status != StatusDeclinedDirty {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, StatusDeclinedDirty, res)
	}
	if after := head(t, f.checkout); after != before {
		t.Errorf("HEAD moved with 83 staged adds present: %s -> %s", before, after)
	}
	staged := run(t, f.checkout, "git", "diff", "--cached", "--name-only")
	if n := len(strings.Split(staged, "\n")); n != 83 {
		t.Errorf("staged file count = %d, want 83 — the index was disturbed", n)
	}
}

// --- The fix path -----------------------------------------------------------

func TestCleanStaleCheckoutIsFastForwarded(t *testing.T) {
	f := newFixture(t)
	f.advanceOrigin(t, 5)
	before := head(t, f.checkout)

	res := Checkout(f.checkout)

	if res.Status != StatusUpdated {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, StatusUpdated, res)
	}
	if res.Behind != 5 {
		t.Errorf("Behind = %d, want 5", res.Behind)
	}
	if after := head(t, f.checkout); after == before {
		t.Errorf("HEAD did not move: still %s", before)
	}
	// The refresh must actually land the upstream content, not merely claim to.
	if _, err := os.Stat(filepath.Join(f.checkout, "file4.txt")); err != nil {
		t.Errorf("upstream content missing after reported fast-forward: %v", err)
	}
	if res.Stale() {
		t.Errorf("an updated checkout must not report Stale(): %+v", res)
	}
}

// TestUntrackedFilesDoNotBlock: untracked files are tolerated because git's
// own ff-only merge aborts rather than overwriting one — the boundary is
// enforced by git, not by us. Same posture as internal/refinery/fastforward.go.
func TestUntrackedFilesDoNotBlock(t *testing.T) {
	f := newFixture(t)
	f.advanceOrigin(t, 2)

	scratch := filepath.Join(f.checkout, "scratch-notes.txt")
	if err := os.WriteFile(scratch, []byte("notes\n"), 0644); err != nil {
		t.Fatal(err)
	}

	res := Checkout(f.checkout)

	if res.Status != StatusUpdated {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, StatusUpdated, res)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("untracked file was removed: %v", err)
	}
}

// --- Methodology: existence is not identity ---------------------------------

// TestExistenceIsNotIdentity encodes the finding that motivated this check's
// design. A sweep once verified 83 stale paths with `git cat-file -e
// origin/main:<path>` and reported a clean 83/83 — while one of the 83 blobs
// actually differed in substance. Presence of a path at the upstream ref says
// nothing about whether the local content matches it.
//
// Here every tracked path in the checkout exists at origin/main, and the naive
// existence check therefore passes for all of them — yet the checkout holds a
// superseded revision. The verdict must still be "stale".
func TestExistenceIsNotIdentity(t *testing.T) {
	f := newFixture(t)

	// Upstream CORRECTS the content of an existing file. No path is added or
	// removed, so every local path still exists at origin/main.
	commit(t, f.publisher, "README.md", "v2-corrected\n", "correct README")
	run(t, f.publisher, "git", "push", "origin", "main")

	// The naive check the ticket warns about: does each tracked path exist at
	// the upstream ref? Confirm it reports a clean bill of health.
	run(t, f.checkout, "git", "fetch", "origin", "main")
	tracked := strings.Split(run(t, f.checkout, "git", "ls-files"), "\n")
	for _, p := range tracked {
		if p == "" {
			continue
		}
		cmd := exec.Command("git", "cat-file", "-e", "FETCH_HEAD:"+p)
		cmd.Dir = f.checkout
		if err := cmd.Run(); err != nil {
			t.Fatalf("test setup: %q must exist upstream for this control to mean anything", p)
		}
	}
	// ^ The existence check just passed on every path. If freshness were
	//   decided that way, the verdict here would be "clean".

	res := Checkout(f.checkout)

	if res.Status == StatusAlreadyCurrent {
		t.Fatal("verdict was AlreadyCurrent — freshness is being decided by existence, not identity")
	}
	if res.Status != StatusUpdated || res.Behind != 1 {
		t.Fatalf("Status = %q Behind = %d, want %q / 1: %+v", res.Status, res.Behind, StatusUpdated, res)
	}
	got, err := os.ReadFile(filepath.Join(f.checkout, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2-corrected\n" {
		t.Errorf("content not corrected: %q", got)
	}
}

// TestBehindIsMeasuredAfterFetchNotAgainstStaleTrackingRef encodes the other
// half of the incident: freshness of a REF and freshness of a CHECKOUT are
// different things. A long-lived checkout's local origin/main is exactly as
// old as its last fetch. Measuring against it asks a stale question and gets a
// reassuring answer.
func TestBehindIsMeasuredAfterFetchNotAgainstStaleTrackingRef(t *testing.T) {
	f := newFixture(t)
	f.advanceOrigin(t, 6)

	// Establish that the local tracking ref IS stale — otherwise this test
	// would pass for the wrong reason.
	localTracking := run(t, f.checkout, "git", "rev-parse", "origin/main")
	localHead := head(t, f.checkout)
	if localTracking != localHead {
		t.Fatalf("test setup: expected local origin/main (%s) to still equal HEAD (%s)", localTracking, localHead)
	}
	// Against that stale ref, the checkout looks perfectly current: 0 behind.

	res := Checkout(f.checkout)

	if res.Behind != 6 {
		t.Fatalf("Behind = %d, want 6 — measured against the stale local tracking ref rather than a fresh fetch: %+v",
			res.Behind, res)
	}
}

// --- Declines that are not faults -------------------------------------------

func TestDetachedHeadIsDeclined(t *testing.T) {
	f := newFixture(t)
	f.advanceOrigin(t, 2)
	run(t, f.checkout, "git", "checkout", "--detach", "HEAD")

	res := Checkout(f.checkout)

	if res.Status != StatusDeclinedDetached {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, StatusDeclinedDetached, res)
	}
	if res.Behind != -1 {
		t.Errorf("Behind = %d, want -1 (undetermined, distinct from 0/current)", res.Behind)
	}
}

func TestNoUpstreamIsDeclined(t *testing.T) {
	f := newFixture(t)
	run(t, f.checkout, "git", "checkout", "-b", "parked-local-branch")

	res := Checkout(f.checkout)

	if res.Status != StatusDeclinedNoUpstream {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, StatusDeclinedNoUpstream, res)
	}
	if res.Behind != -1 {
		t.Errorf("Behind = %d, want -1 (undetermined)", res.Behind)
	}
}

func TestDivergedIsDeclined(t *testing.T) {
	f := newFixture(t)
	f.advanceOrigin(t, 3)
	commit(t, f.checkout, "local.txt", "local\n", "local commit")
	before := head(t, f.checkout)

	res := Checkout(f.checkout)

	if res.Status != StatusDeclinedDiverged {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, StatusDeclinedDiverged, res)
	}
	if res.Behind != 3 || res.Ahead != 1 {
		t.Errorf("Behind/Ahead = %d/%d, want 3/1", res.Behind, res.Ahead)
	}
	if after := head(t, f.checkout); after != before {
		t.Errorf("HEAD moved on a diverged checkout: %s -> %s", before, after)
	}
	if !res.Stale() {
		t.Errorf("diverged-and-behind must report Stale(): %+v", res)
	}
}

// --- Absence and failure ----------------------------------------------------

// TestMissingDirIsSkippedNotFailed: most agents keep no repo/ at all. Absence
// must be quiet, or the loud channel fires on every agent start and gets muted.
func TestMissingDirIsSkipped(t *testing.T) {
	res := Checkout(filepath.Join(t.TempDir(), "nope"))
	if res.Status != StatusSkipped {
		t.Fatalf("Status = %q, want %q", res.Status, StatusSkipped)
	}
	if res.Stale() {
		t.Error("a missing checkout must not report Stale()")
	}
}

func TestNonRepoDirIsSkipped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if res := Checkout(dir); res.Status != StatusSkipped {
		t.Fatalf("Status = %q, want %q", res.Status, StatusSkipped)
	}
}

// TestFetchFailureIsNotACleanBillOfHealth: when git cannot answer, the verdict
// must be UNKNOWN, never "current". A check that reports success when it could
// not run is worse than no check — it actively certifies the thing it failed
// to inspect.
func TestFetchFailureIsNotACleanBillOfHealth(t *testing.T) {
	f := newFixture(t)
	run(t, f.checkout, "git", "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	res := Checkout(f.checkout)

	if res.Status == StatusAlreadyCurrent || res.Status == StatusUpdated {
		t.Fatalf("an unreachable remote yielded a clean verdict %q: %+v", res.Status, res)
	}
	if res.Status != StatusFailed {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, StatusFailed, res)
	}
	if res.Behind != -1 {
		t.Errorf("Behind = %d, want -1 (undetermined)", res.Behind)
	}
	if !strings.Contains(res.String(), "FRESHNESS UNKNOWN") {
		t.Errorf("failure summary must not read as reassuring: %q", res.String())
	}
}

// TestStringIsNeverReassuringOnDecline guards the mg-f86c shape: a DECLINED
// operation that logs in the vocabulary of success is the defect.
func TestStringIsNeverReassuringOnDecline(t *testing.T) {
	for _, st := range []Status{
		StatusDeclinedDirty, StatusDeclinedDetached,
		StatusDeclinedNoUpstream, StatusDeclinedDiverged,
	} {
		r := Result{Path: "/p", Status: st, Behind: 12, Upstream: "origin/main", Detail: "d"}
		s := r.String()
		if !strings.Contains(s, "DECLINED") {
			t.Errorf("%s summary does not say DECLINED: %q", st, s)
		}
		for _, bad := range []string{"refreshed", "fast-forwarded", "current"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s summary contains reassuring word %q: %q", st, bad, s)
			}
		}
		if !r.Declined() {
			t.Errorf("%s: Declined() = false", st)
		}
	}
}
