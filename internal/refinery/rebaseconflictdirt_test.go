package refinery

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The class under test (mg-eac0): the gate-dirt detector runs DOWNSTREAM of the
// rebase, and a conflicted rebase leaves the tree in exactly the state a
// writing gate leaves it in — modified tracked files, in the refinery's clone,
// at a step that just failed. Read without asking who wrote them, that dirt was
// attributed to the gate, and the resulting report managed to say three wrong
// things at once about a real conflict:
//
//	Status: failed(infrastructure) — establishes nothing about the branch. Resubmit.
//	Error:  the quality gate modified 6 tracked files ...
//	        THIS IS NOT YOUR CHANGE: none of those paths are touched by the submitted branch.
//	        ... or add the paths to .gitignore.
//
// while quoting its own "CONFLICT (content)" underneath. The branch's sole
// commit touched those six paths and no others; two of them were production
// deploy scripts the .gitignore advice would have untracked.
//
// The arms below are the three separable claims: the conflict must not be
// called gate dirt, the classification must come out a branch DEFECT, and the
// "not your change" sentence must be computed rather than asserted.

// conflictingRepo builds a bare origin, a branch that modifies a file, and a
// main that modifies the SAME file differently, then returns a clone parked
// exactly where the refinery parks it: branch checked out, mid-conflict from
// `git rebase origin/main`, with the rebase state still on disk. It returns the
// clone dir and git's verbatim output.
func conflictingRepo(t *testing.T) (wtDir, rebaseOut string, mr *MergeRequest) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	seed := t.TempDir()
	run(t, seed, "git", "clone", originDir, ".")
	run(t, seed, "git", "config", "user.email", "test@test.com")
	run(t, seed, "git", "config", "user.name", "Test")
	writeFile(t, seed, "scripts/pogo-self-deploy", "#!/bin/sh\necho base\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "initial")
	run(t, seed, "git", "push", "origin", "main")

	// The branch: rewrites the deploy script and adds a changelog entry. These
	// are the ONLY paths it touches — the fact the old message denied.
	run(t, seed, "git", "checkout", "-b", "polecat-p6d2f")
	writeFile(t, seed, "scripts/pogo-self-deploy", "#!/bin/sh\necho branch\n")
	writeFile(t, seed, "changelog.d/mg-6d2f.fixed.md", "branch entry\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "feat: deploy script (mg-6d2f)")
	run(t, seed, "git", "push", "origin", "polecat-p6d2f")

	// main moves under it, touching the same file — a genuine, deterministic
	// conflict that is exactly as true on every resubmit.
	run(t, seed, "git", "checkout", "main")
	writeFile(t, seed, "scripts/pogo-self-deploy", "#!/bin/sh\necho main-moved\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "fix: deploy script (mg-0155)")
	run(t, seed, "git", "push", "origin", "main")

	wtDir = t.TempDir()
	run(t, wtDir, "git", "clone", originDir, ".")
	run(t, wtDir, "git", "config", "user.email", "refinery@test.com")
	run(t, wtDir, "git", "config", "user.name", "Refinery")
	run(t, wtDir, "git", "checkout", "-B", "polecat-p6d2f", "origin/polecat-p6d2f")

	out, err := gitCmdOutput(wtDir, "rebase", "origin/main")
	if err == nil {
		t.Fatalf("setup did not produce a conflict; rebase succeeded:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "conflict") {
		t.Fatalf("setup produced a non-conflict rebase failure:\n%s", out)
	}
	return wtDir, out, &MergeRequest{
		ID:        "mr-test-eac0",
		RepoPath:  wtDir,
		Branch:    "polecat-p6d2f",
		TargetRef: "main",
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestConflictedRebaseIsNotGateDirt is the first arm. The tree IS dirty at this
// point and dirtyTrackedPaths says so — the detector must decline anyway,
// because the step that failed is the step that dirtied it.
func TestConflictedRebaseIsNotGateDirt(t *testing.T) {
	wtDir, out, mr := conflictingRepo(t)

	// The precondition that made the misdiagnosis possible: without the fix,
	// this dirt is all the detector looked at.
	dirty, err := dirtyTrackedPaths(wtDir)
	if err != nil {
		t.Fatalf("dirtyTrackedPaths: %v", err)
	}
	if len(dirty) == 0 {
		t.Fatal("a conflicted rebase left a clean tree — the test no longer exercises the confusion it was written for")
	}

	r := &Refinery{}
	if got := r.classifyGateDirt(wtDir, mr, "rebase onto main", out); got != nil {
		t.Errorf("a conflicted rebase was reported as gate dirt over %d dirty path(s) %v:\n%s",
			len(dirty), dirty, got.Error())
	}
}

// TestConflictedRebaseClassifiesAsDefect is the arm that inverts the outcome.
// failed(infrastructure) carries "establishes nothing about the branch;
// resubmit; do NOT dispatch a fix" — and every resubmit re-runs the same
// deterministic conflict, so the label turns a branch failure into an infinite
// retry loop.
func TestConflictedRebaseClassifiesAsDefect(t *testing.T) {
	wtDir, out, mr := conflictingRepo(t)

	r := &Refinery{}
	var err error
	if dirtErr := r.classifyGateDirt(wtDir, mr, "rebase onto main", out); dirtErr != nil {
		err = dirtErr
	} else {
		err = gitStepFail("rebase", "rebase onto main: "+out, []string{"rebase", "origin/main"}, out,
			errors.New("exit status 1"))
	}

	d := classifyFailure("rebase", out, err)
	if d.Class != ClassDefect {
		t.Errorf("a conflicted rebase classified %q, want %q (signal=%q reason=%q)",
			d.Class, ClassDefect, d.Signal, d.Reason)
	}
	if d.Retryable {
		t.Error("a deterministic conflict was marked retryable — every retry re-runs the same conflict")
	}
	// The two instruments must now agree: no report may say CONFLICT and
	// infrastructure at once.
	if strings.Contains(strings.ToLower(out), "conflict") && d.Class == ClassInfrastructure {
		t.Error("one report contains both 'CONFLICT' and infrastructure — the classification is not reading the git output it captured")
	}
}

// TestGateDirtIsStillReportedWithoutAConflict is the guard on the guard. The
// suppression is deliberately one-directional: a dirty tree with no conflict to
// explain it is still the gate, and mg-393f's message must survive this fix.
func TestGateDirtIsStillReportedWithoutAConflict(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	writeFile(t, dir, "records/probe.json", "{}\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "initial")
	writeFile(t, dir, "records/probe.json", "{\"run\": 2}\n")

	r := &Refinery{}
	got := r.classifyGateDirt(dir, nil, "rebase onto main", "error: cannot rebase: You have unstaged changes.")
	if got == nil {
		t.Fatal("a gate-dirtied tree with no conflict was NOT reported as gate dirt — the mg-393f message is gone")
	}
	if !strings.Contains(got.Error(), "records/probe.json") {
		t.Errorf("gate-dirt message lost the path it exists to name:\n%s", got.Error())
	}
}

// TestBranchTouchedPathsReadsTheBranchNotHEAD is the arm behind the false
// sentence. Mid-conflict, HEAD is detached at the target with the branch's
// commits not yet applied, so the old `origin/main...HEAD` probe returned an
// empty list — which the message printed as "none of those paths are touched by
// the submitted branch" about a branch that touched every one of them.
func TestBranchTouchedPathsReadsTheBranchNotHEAD(t *testing.T) {
	wtDir, _, mr := conflictingRepo(t)

	paths, known := branchTouchedPaths(wtDir, mr)
	if !known {
		t.Fatal("branchTouchedPaths could not answer against a reachable origin")
	}
	want := map[string]bool{
		"scripts/pogo-self-deploy":     false,
		"changelog.d/mg-6d2f.fixed.md": false,
	}
	for _, p := range paths {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("branch path %q missing from %v — the probe is reading the worktree HEAD, not the submitted branch", p, paths)
		}
	}

	// The old probe, for contrast: it is what produced the empty answer.
	old, err := gitCmdOutput(wtDir, "diff", "--name-only", "origin/main...HEAD")
	if err == nil && strings.TrimSpace(old) == "" {
		t.Logf("confirmed: the HEAD-relative probe returns nothing mid-conflict (this is what asserted 'not your change')")
	}
}

// TestGateDirtMessageNeverGitignoresBranchPaths is the destructive-remedy arm.
// "add the paths to .gitignore" aimed at a path the branch modifies is an
// instruction to untrack production source — and the paths it named in mg-eac0
// were the deploy scripts the author was trying to land.
func TestGateDirtMessageNeverGitignoresBranchPaths(t *testing.T) {
	cases := []struct {
		name        string
		branchPaths []string
		known       bool
		wantIgnore  bool
	}{
		{"branch owns the dirty paths", []string{"scripts/pogo-self-deploy"}, true, false},
		{"ownership unknown", nil, false, false},
		{"probe answered: genuinely not the branch", nil, true, true},
	}
	for _, tc := range cases {
		e := &gateDirtError{
			Stage:            "rebase onto main",
			DirtyPaths:       []string{"scripts/pogo-self-deploy"},
			BranchPaths:      tc.branchPaths,
			BranchPathsKnown: tc.known,
			Gates:            []string{"./build.sh"},
			WorktreeDir:      "/Users/daniel/.pogo/refinery/worktrees/pogo",
			GitOutput:        "error: cannot rebase: You have unstaged changes.",
		}
		msg := e.Error()
		if got := strings.Contains(msg, ".gitignore"); got != tc.wantIgnore {
			t.Errorf("%s: .gitignore advice present=%v, want %v:\n%s", tc.name, got, tc.wantIgnore, msg)
		}
		// The negative claim is only sayable when the probe answered it.
		if !tc.known && strings.Contains(msg, "NOT your change") {
			t.Errorf("%s: message asserts 'NOT your change' from a probe that could not answer:\n%s", tc.name, msg)
		}
		if len(tc.branchPaths) > 0 && strings.Contains(msg, "NOT your change") {
			t.Errorf("%s: message denies authorship of paths the branch modifies:\n%s", tc.name, msg)
		}
	}
}

// TestClassifyFailureRefusesInfrastructureForConflictOutput pins the second
// lock. classifyGateDirt no longer builds one of these for a conflicted step,
// but the classifier must not depend on that: the error TYPE alone was what
// decided infrastructure, without reading the output the same error carried.
func TestClassifyFailureRefusesInfrastructureForConflictOutput(t *testing.T) {
	out := "Rebasing (1/1)\nAuto-merging scripts/pogo-self-deploy\nCONFLICT (content): Merge conflict in scripts/pogo-self-deploy\n"
	err := &gateDirtError{
		Stage:       "rebase onto main",
		DirtyPaths:  []string{"scripts/pogo-self-deploy"},
		WorktreeDir: "/tmp/wt",
		GitOutput:   out,
	}
	d := classifyFailure("rebase", out, err)
	if d.Class == ClassInfrastructure {
		t.Errorf("a gateDirtError carrying CONFLICT output still classified infrastructure (signal=%q)", d.Signal)
	}
	if d.Class != ClassDefect {
		t.Errorf("got class %q, want %q", d.Class, ClassDefect)
	}
}
