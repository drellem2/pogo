package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// polecatWorktrees is the half of mg-ded2 that cannot be exercised in
// internal/strandwatch, because it is the only place the sweep learns about a
// repository the BOARD does not name. Everything else in this command is
// downstream of the open work items; if this function's answer is wrong or
// empty, the sweep silently reverts to the population that could not contain
// /Users/daniel/dev/macguffin.

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// TestPolecatWorktreesFindsTheRepoTheBoardDoesNotName builds the measured shape:
// a polecat worktree on disk whose repository no work item mentions. It also
// carries the REAPED SHELL control — 19 of the 58 entries under the polecats dir
// on 2026-08-19 were directories whose git registration was gone — because a
// reader that counted directory entries instead of worktrees is a mistake this
// fleet has already made once (mg-f4f7 counted 67 entries as 67 worktrees).
func TestPolecatWorktreesFindsTheRepoTheBoardDoesNotName(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	root := t.TempDir()
	pogoHome := filepath.Join(root, "pogohome")
	polecats := filepath.Join(pogoHome, "polecats")
	if err := os.MkdirAll(polecats, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POGO_HOME", pogoHome)

	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "init", "--initial-branch=main", src)
	gitRun(t, src, "config", "user.email", "test@example.com")
	gitRun(t, src, "config", "user.name", "Test")
	gitRun(t, src, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, src, "add", "README.md")
	gitRun(t, src, "commit", "-q", "-m", "chore: initial")

	live := filepath.Join(polecats, "gt-ffbd")
	gitRun(t, src, "worktree", "add", "-q", live, "-b", "polecat-gt-ffbd")

	// The control: a directory under the polecats dir that is NOT a worktree.
	shell := filepath.Join(polecats, "reaped-shell")
	if err := os.MkdirAll(shell, 0755); err != nil {
		t.Fatal(err)
	}
	// And a plain file, which must not be mistaken for one either.
	if err := os.WriteFile(filepath.Join(polecats, "notes.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := polecatWorktrees()
	if err != nil {
		t.Fatalf("polecatWorktrees: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d worktrees, want 1 (the reaped shell and the plain file are not "+
			"worktrees; counting directory entries as worktrees is a mistake this fleet has "+
			"already made): %+v", len(got), got)
	}
	w := got[0]
	// EvalSymlinks on both sides: macOS resolves the per-test TMPDIR through
	// /private, and git answers with the resolved form. Nothing on the real box
	// aliases that way — the repositories live under /Users — but comparing raw
	// strings here would fail for a reason that has nothing to do with the code.
	if resolve(t, w.Repo) != resolve(t, src) {
		t.Errorf("Repo = %q, want %q — the repository has to come from git's own answer, because "+
			"the whole point is finding one the board does not name", w.Repo, src)
	}
	if w.Branch != "polecat-gt-ffbd" {
		t.Errorf("Branch = %q, want polecat-gt-ffbd", w.Branch)
	}
	if resolve(t, w.Path) != resolve(t, live) {
		t.Errorf("Path = %q, want %q", w.Path, live)
	}
}

func resolve(t *testing.T, path string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return out
}

// TestPolecatWorktreesTreatsAMissingDirAsZeroAndNotAsFailure. "There are no
// polecats" and "the question could not be asked" render differently in the
// frame, and only a real error may claim the second.
func TestPolecatWorktreesTreatsAMissingDirAsZeroAndNotAsFailure(t *testing.T) {
	t.Setenv("POGO_HOME", filepath.Join(t.TempDir(), "nothing-here"))
	got, err := polecatWorktrees()
	if err != nil {
		t.Fatalf("polecatWorktrees on a missing dir: %v, want a clean zero", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d worktrees from a nonexistent dir", len(got))
	}
}

// TestCheckStrandedHelpNamesItsOwnPopulationBound. The next person to extend this
// command reads --help before the source, and the thing this ticket is about is
// precisely a bound nobody could see. A help text that describes the item join
// and not its complement is how a bounded answer gets read as a census.
func TestCheckStrandedHelpNamesItsOwnPopulationBound(t *testing.T) {
	bin := checkVerdictsBinary(t)
	out, err := exec.Command(bin, "check-stranded", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("check-stranded --help: %v\n%s", err, out)
	}
	for _, want := range []string{
		"orphan_branch",   // the row the item join cannot produce
		"NO REMOTE REF",   // the predicate that keeps it bounded
		"MATCHED NOTHING", // the --repo repair
		"boundary",        // the frame, which is the cheapest and widest of the three
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("--help does not mention %q:\n%s", want, out)
		}
	}
}
