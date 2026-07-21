package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// wtRepo builds a real git repo with a polecat worktree, standing in for the
// state a live polecat is in at exit.
func wtRepo(t *testing.T) (repo string, worktree string) {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "t")
	run("config", "user.email", "t@t")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "seed.txt")
	run("commit", "-qm", "seed")
	run("branch", "polecat-cat1")
	wt := filepath.Join(filepath.Dir(dir), "wt-cat1")
	run("worktree", "add", "-q", wt, "polecat-cat1")
	return dir, wt
}

// TestCleanupAgentWorktreePreservesDirty is the end-to-end control for
// mg-ee02 at the exit-hook layer — the layer `pogo agent stop` actually
// reaches. `stop` SIGTERMs the process; the registry's onExit hook then fires
// and calls exactly this function. Before the fix it force-removed, and a
// stopped mid-flight polecat lost its working tree.
func TestCleanupAgentWorktreePreservesDirty(t *testing.T) {
	repo, wt := wtRepo(t)
	racetest := filepath.Join(wt, "trust_hook_race_test.go")
	if err := os.WriteFile(racetest, []byte("package x // 201 irreplaceable lines\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var gotTo, gotSubject, gotBody string
	mail := func(to, from, subject, body string) error {
		gotTo, gotSubject, gotBody = to, subject, body
		return nil
	}

	outcome := cleanupAgentWorktree("cat1", repo, wt, "mayor", mail)
	if outcome != worktreePreserved {
		t.Fatalf("outcome = %v, want worktreePreserved", outcome)
	}
	if _, err := os.Stat(racetest); err != nil {
		t.Fatalf("THE WORK WAS DESTROYED: %v", err)
	}

	// Preservation must be loud, or it trades data loss for silent
	// accumulation — the exact cost of choosing preserve over refuse.
	if gotTo != "mayor" {
		t.Errorf("notice should go to the coordinator, got %q", gotTo)
	}
	if !strings.Contains(gotSubject, "cat1") {
		t.Errorf("subject should name the agent, got %q", gotSubject)
	}
	// The operator needs three things to act: what was kept, where it is, and
	// how to reclaim it once they are done.
	if !strings.Contains(gotBody, "trust_hook_race_test.go") {
		t.Errorf("body must name the uncommitted file, got:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, wt) {
		t.Errorf("body must give the worktree path, got:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "--force") {
		t.Errorf("body must state how to reclaim it, got:\n%s", gotBody)
	}
}

// TestCleanupAgentWorktreeReapsClean is the negative control: the common case
// — stopping a polecat that has committed and merged — must still reap. A fix
// that leaks worktrees is a different defect, not a fix.
func TestCleanupAgentWorktreeReapsClean(t *testing.T) {
	repo, wt := wtRepo(t)

	mailed := false
	mail := func(to, from, subject, body string) error { mailed = true; return nil }

	outcome := cleanupAgentWorktree("cat1", repo, wt, "mayor", mail)
	if outcome != worktreeReaped {
		t.Fatalf("outcome = %v, want worktreeReaped", outcome)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("clean worktree must still be reaped, stat err = %v", err)
	}
	if mailed {
		t.Error("reaping a clean worktree must not mail anyone; that would be noise on the common path")
	}
	// The registration must go too, or the branch stays pinned and the
	// gitgc branch phase leaks.
	out, _ := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if strings.Contains(string(out), wt) {
		t.Errorf("registration should be gone:\n%s", out)
	}
}

// TestCleanupAgentWorktreeNoWorktree: a --no-worktree polecat has nothing to
// clean up and must not be treated as an error.
func TestCleanupAgentWorktreeNoWorktree(t *testing.T) {
	if got := cleanupAgentWorktree("cat1", "/nonexistent", "", "mayor", nil); got != worktreeNone {
		t.Errorf("outcome = %v, want worktreeNone", got)
	}
}

// TestCleanupAgentWorktreeSurvivesMailFailure: an unreachable coordinator must
// not change the preservation decision. The tree stays either way — losing the
// work because the mail failed would reintroduce the bug through the back door.
func TestCleanupAgentWorktreeSurvivesMailFailure(t *testing.T) {
	repo, wt := wtRepo(t)
	keep := filepath.Join(wt, "wip.go")
	if err := os.WriteFile(keep, []byte("package wip\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mail := func(to, from, subject, body string) error { return os.ErrDeadlineExceeded }

	if got := cleanupAgentWorktree("cat1", repo, wt, "mayor", mail); got != worktreePreserved {
		t.Fatalf("outcome = %v, want worktreePreserved even when mail fails", got)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("work must survive a failed notification: %v", err)
	}
}

// TestCleanupAgentWorktreeKeepsUndeterminable is the mg-4d45 control at the
// exit-hook layer — the layer `pogo agent stop` actually reaches.
//
// The hook fires AFTER the process has exited, so this is precisely the site
// where a naive reading of "liveness decides" would answer GONE and reap. It
// must not: the tree belonged to an agent that was running until moments ago,
// and its files are that agent's in-flight work.
func TestCleanupAgentWorktreeKeepsUndeterminable(t *testing.T) {
	repo, wt := wtRepo(t)
	precious := filepath.Join(wt, "irreplaceable.go")
	if err := os.WriteFile(precious, []byte("package x // the only copy\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// A genuine `git status` failure: a present but corrupt .git pointer.
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /nonexistent/garbage\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var gotSubject, gotBody string
	mail := func(to, from, subject, body string) error {
		gotSubject, gotBody = subject, body
		return nil
	}

	outcome := cleanupAgentWorktree("cat1", repo, wt, "mayor", mail)
	if outcome != worktreeUndetermined {
		t.Fatalf("outcome = %v, want worktreeUndetermined", outcome)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatalf("THE WORK WAS DESTROYED — a tree we could not read must survive: %v", err)
	}

	// The notice must report what actually happened. Claiming "uncommitted
	// work" would send an operator hunting for files we never established are
	// there; cannot-tell has to stay distinguishable from dirty.
	if !strings.Contains(gotBody, "could NOT be checked") {
		t.Errorf("body must say the check failed, got:\n%s", gotBody)
	}
	if strings.Contains(gotSubject, "preserved uncommitted work") {
		t.Errorf("subject must not claim uncommitted work was found, got %q", gotSubject)
	}
	if !strings.Contains(gotBody, "pogo gc") {
		t.Errorf("body must say how to reclaim the tree, got:\n%s", gotBody)
	}
}
