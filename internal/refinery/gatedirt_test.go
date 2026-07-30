package refinery

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The class under test (mg-393f): a quality gate that writes a tracked file
// dirties the refinery's own checkout, and every git step after it — rebase on
// the next attempt, the target checkout, the ff-merge — refuses. The failure
// used to surface as git's "You have unstaged changes / Please commit or stash
// them", which names a worktree the author cannot see and prescribes a fix they
// cannot reach.
//
// Two integration arms below reproduce the two ways it bites, and both must
// merge. The unit arms cover the reporting path for a dirty tree that survives
// the cleanup anyway.

// TestGateWritingTrackedFileMergesAcrossRetry is the measured instance
// (mg-48dd): the gate writes a tracked record on every run AND races a commit
// onto origin/main, which forces a retry. Attempt 2's `git rebase` then meets
// the tree the gate dirtied.
//
// Before the fix this failed with `cannot rebase: You have unstaged changes`,
// three seconds after the gate finished, blaming the author.
func TestGateWritingTrackedFileMergesAcrossRetry(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	// Sidecar clone the gate pushes the racing commit from — outside the
	// refinery's worktree, so the refinery cannot reset it.
	sidecarDir := t.TempDir()
	run(t, sidecarDir, "git", "clone", originDir, ".")
	run(t, sidecarDir, "git", "config", "user.email", "ci@test.com")
	run(t, sidecarDir, "git", "config", "user.name", "CI")

	stateDir := t.TempDir()
	raceFlag := filepath.Join(stateDir, "race_done")
	gateRuns := filepath.Join(stateDir, "gate_runs")

	// The gate rewrites record.json — a COMMITTED file — with per-run content,
	// exactly as the width_three probes rewrite their JSON records. It also
	// injects a one-shot race so the ff-only merge loses and a retry happens.
	buildSh := fmt.Sprintf(`#!/bin/sh
set -e
RUNS=$(cat %s 2>/dev/null || echo 0)
RUNS=$((RUNS+1))
echo $RUNS > %s
echo "{\"run\": $RUNS}" > record.json
if [ ! -f %s ]; then
    touch %s
    (cd %s && git fetch origin main >/dev/null 2>&1 && git reset --hard origin/main >/dev/null 2>&1 && git commit --allow-empty -m "ci: version bump" >/dev/null && git push origin main >/dev/null 2>&1)
fi
exit 0
`, gateRuns, gateRuns, raceFlag, raceFlag, sidecarDir)

	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte(buildSh), 0755)
	os.WriteFile(filepath.Join(workDir, "record.json"), []byte("{\"run\": 0}\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial with record-writing gate")
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "feature-dirty-gate")
	os.WriteFile(filepath.Join(workDir, "feature.txt"), []byte("feature"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add feature")
	run(t, workDir, "git", "push", "origin", "feature-dirty-gate")

	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-dirty-gate",
		TargetRef: "main",
		Author:    "test-cat",
	})
	if err != nil {
		t.Fatal(err)
	}

	r.processNext()

	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR not found")
	}
	if mr.Status != StatusMerged {
		t.Fatalf("gate that writes a tracked file must not fail the merge; got %s\nerror: %s\ngate_output: %s",
			mr.Status, mr.Error, mr.GateOutput)
	}
	// The record must say the gate writes, so the next reader does not have to
	// derive it from timestamps the way mg-48dd did.
	if !strings.Contains(mr.GateOutput, "record.json") {
		t.Errorf("gate output does not name the file the gate wrote:\n%s", mr.GateOutput)
	}
	if !strings.Contains(mr.GateOutput, "discarded") {
		t.Errorf("gate output does not record that the writes were discarded:\n%s", mr.GateOutput)
	}

	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	if _, err := os.Stat(filepath.Join(verifyDir, "feature.txt")); os.IsNotExist(err) {
		t.Error("feature.txt missing on main after merge")
	}
	// The gate's output must NOT have landed: it is not part of the branch.
	got, _ := os.ReadFile(filepath.Join(verifyDir, "record.json"))
	if !strings.Contains(string(got), "\"run\": 0") {
		t.Errorf("gate output landed on main; record.json = %q, want the committed value", string(got))
	}
}

// TestGateWritingBranchOwnedFileMerges is the second arm: the gate rewrites a
// tracked file the BRANCH also modifies. No retry is needed — the post-gate
// `checkout -B <target> origin/<target>` refuses on its own, because the file
// differs between the branch tip and the target and is locally modified.
func TestGateWritingBranchOwnedFileMerges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	os.WriteFile(filepath.Join(workDir, "build.sh"),
		[]byte("#!/bin/sh\nset -e\necho regenerated-by-gate > record.json\nexit 0\n"), 0755)
	os.WriteFile(filepath.Join(workDir, "record.json"), []byte("base\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial")
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "feature-overlap")
	os.WriteFile(filepath.Join(workDir, "record.json"), []byte("authored-by-branch\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "update record")
	run(t, workDir, "git", "push", "origin", "feature-overlap")

	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-overlap",
		TargetRef: "main",
		Author:    "test-cat",
	})
	if err != nil {
		t.Fatal(err)
	}

	r.processNext()

	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR not found")
	}
	if mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %s\nerror: %s\ngate_output: %s", mr.Status, mr.Error, mr.GateOutput)
	}

	// The branch's version of the contested file must land, not the gate's.
	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	got, err := os.ReadFile(filepath.Join(verifyDir, "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "authored-by-branch" {
		t.Errorf("record.json on main = %q, want the branch's value — the gate's write must not land", string(got))
	}
}

// TestDiscardGateSideEffects covers the cleanup primitive: tracked
// modifications go, untracked files stay (they are where gates keep build
// caches, and they never block git), and a clean tree reports nothing.
func TestDiscardGateSideEffects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("committed\n"), 0644)
	os.WriteFile(filepath.Join(dir, "also.txt"), []byte("committed\n"), 0644)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "initial")

	// Clean tree: nothing discarded, no error.
	discarded, err := discardGateSideEffects(dir)
	if err != nil {
		t.Fatalf("clean tree: %v", err)
	}
	if len(discarded) != 0 {
		t.Errorf("clean tree reported %v discarded, want none", discarded)
	}

	// Gate writes: one tracked file modified, one staged, one untracked cache.
	os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("written by gate\n"), 0644)
	os.WriteFile(filepath.Join(dir, "also.txt"), []byte("staged by gate\n"), 0644)
	run(t, dir, "git", "add", "also.txt")
	os.WriteFile(filepath.Join(dir, "cache.bin"), []byte("build cache\n"), 0644)

	discarded, err = discardGateSideEffects(dir)
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	want := []string{"also.txt", "tracked.txt"}
	if strings.Join(discarded, ",") != strings.Join(want, ",") {
		t.Errorf("discarded = %v, want %v", discarded, want)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "tracked.txt")); strings.TrimSpace(string(got)) != "committed" {
		t.Errorf("tracked.txt = %q, want the committed content restored", string(got))
	}
	if _, err := os.Stat(filepath.Join(dir, "cache.bin")); err != nil {
		t.Errorf("untracked build cache was removed: %v", err)
	}
	if leftover, err := dirtyTrackedPaths(dir); err != nil || len(leftover) != 0 {
		t.Errorf("tree still dirty after discard: %v (err %v)", leftover, err)
	}
}

// TestClassifyGateDirtIgnoresCleanTree guards the narrowness of the new
// reporting path: an ordinary git failure on a clean tree (a real rebase
// conflict, a bad upstream) must keep its own error and its own retry
// classification, not be relabelled as gate dirt.
func TestClassifyGateDirtIgnoresCleanTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0644)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "initial")

	r := &Refinery{}
	if got := r.classifyGateDirt(dir, nil, "rebase onto main", "fatal: invalid upstream 'origin/main'"); got != nil {
		t.Errorf("clean tree classified as gate dirt: %v", got)
	}
}

// TestGateDirtErrorMessage is the acceptance-(b) arm: when a dirty tree does
// reach a git step anyway, the message must name the gate as the writer and
// must not tell the author to commit or stash — including inside the quoted
// git output, which is where the bad advice originally came from.
func TestGateDirtErrorMessage(t *testing.T) {
	e := &gateDirtError{
		Stage:       "rebase onto main",
		DirtyPaths:  []string{"records/probe.json", "records/mutation.json"},
		Gates:       []string{"./scripts/refinery_gate.sh"},
		WorktreeDir: "/Users/daniel/.pogo/refinery/worktrees/pogo",
		GitOutput: "error: cannot rebase: You have unstaged changes.\n" +
			"error: Please commit or stash them.\n" +
			"hint: run `git stash` first",
	}
	msg := e.Error()

	for _, want := range []string{
		"records/probe.json",
		"records/mutation.json",
		"./scripts/refinery_gate.sh",
		"quality gate modified",
		"NOT your change",
		"/Users/daniel/.pogo/refinery/worktrees/pogo",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	// The instruction that cost mg-48dd a debugging cycle must be gone, in
	// every form — including git's own relayed wording.
	for _, banned := range []string{"commit or stash", "git stash", "git add"} {
		if strings.Contains(msg, banned) {
			t.Errorf("message still instructs %q:\n%s", banned, msg)
		}
	}
	// Keeping the non-advice half of git's complaint is the point of redacting
	// rather than dropping.
	if !strings.Contains(msg, "cannot rebase") {
		t.Errorf("message dropped git's evidence entirely:\n%s", msg)
	}
}

// TestRedactGitDirtAdvice pins the redaction against every wording git uses for
// this complaint. A rewording that slipped through would put "commit or stash"
// back in front of an author who cannot act on it, which is the whole defect.
func TestRedactGitDirtAdvice(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"rebase", "error: cannot rebase: You have unstaged changes.\nerror: Please commit or stash them."},
		{"checkout", "error: Your local changes to the following files would be overwritten by checkout:\n\trecord.json\nPlease commit your changes or stash them before you switch branches.\nAborting"},
		{"merge", "error: Your local changes to the following files would be overwritten by merge:\n\trecord.json\nPlease commit your changes or stash them before you merge.\nAborting"},
		{"older-comma-form", "error: Your local changes would be overwritten.\nPlease, commit your changes or stash them before you can merge."},
		{"hint-lines", "error: cannot pull with rebase: You have unstaged changes.\nhint: run `git stash` first\nhint: see gitfaq(7)"},
	}
	for _, tc := range cases {
		got := redactGitDirtAdvice(tc.in)
		for _, banned := range []string{"commit or stash", "commit your changes", "stash them", "git stash", "hint:"} {
			if strings.Contains(strings.ToLower(got), banned) {
				t.Errorf("%s: advice %q survived redaction:\n%s", tc.name, banned, got)
			}
		}
		// Redaction must not silently swallow the whole complaint — the
		// evidence half is why it is quoted at all.
		if !strings.Contains(got, "error:") {
			t.Errorf("%s: redaction dropped git's evidence entirely:\n%s", tc.name, got)
		}
	}
}

// TestGateDirtErrorNamesBranchOverlap covers the other branch of the blame
// question: when the branch does modify a dirty path, the message says the gate
// may be rewriting output the change alters instead of flatly denying it.
func TestGateDirtErrorNamesBranchOverlap(t *testing.T) {
	e := &gateDirtError{
		Stage:       "checkout target main",
		DirtyPaths:  []string{"record.json"},
		BranchPaths: []string{"record.json"},
		Gates:       []string{"./build.sh"},
		WorktreeDir: "/tmp/wt",
	}
	msg := e.Error()
	if strings.Contains(msg, "NOT your change") {
		t.Errorf("claimed the author is uninvolved for a path the branch modifies:\n%s", msg)
	}
	if !strings.Contains(msg, "also modifies record.json") {
		t.Errorf("message does not report the overlap:\n%s", msg)
	}
	if strings.Contains(msg, "commit or stash") {
		t.Errorf("overlap case still instructs commit-or-stash:\n%s", msg)
	}
}

// TestDirtyTrackedPathsParsesRenames guards the porcelain parse: a rename entry
// carries two paths and only the new one exists in the tree.
func TestDirtyTrackedPathsParsesRenames(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "old.txt"), []byte("body\n"), 0644)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "initial")
	run(t, dir, "git", "mv", "old.txt", "new.txt")

	paths, err := dirtyTrackedPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if strings.Contains(p, "->") {
			t.Errorf("rename arrow leaked into path %q (all: %v)", p, paths)
		}
	}
	if len(paths) == 0 {
		t.Fatalf("rename not reported as dirty")
	}
}
