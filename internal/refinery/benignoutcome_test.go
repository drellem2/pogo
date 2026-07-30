package refinery

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The class under test (mg-5d3f): the refinery calls `git rebase --abort`
// unconditionally to clear crash debris, so the abort fails on every clean
// clone. 245 of those landed in one 50,603-line log, every one of them reading
// `git [rebase --abort] failed` and every one of them an expected outcome.
// Benign lines at error level teach a reader to skip error lines.
//
// The fix demotes on the FULL MESSAGE, never on the command. The two arms below
// are the pair that tells those two implementations apart — they behave
// identically on the benign line, and differ only on a DIFFERENT
// `rebase --abort` failure.

// captureRefineryLog redirects the standard logger to a buffer for the duration
// of the test and returns a closure yielding everything written so far.
// gitCmdOutput reports through log.Printf, so reading the logger's own output
// is the only way to assert how a git failure was classified.
func captureRefineryLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return buf.String
}

// runTolerant runs a command and returns its combined output, tolerating a
// non-zero exit. The positive-control fixture needs a rebase that CONFLICTS,
// which run() would treat as a fatal test error.
func runTolerant(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// TestRebaseAbortWithNoRebaseInProgressIsNotLoggedAsFailure is the measured
// benign case: a clean clone with nothing to abort.
func TestRebaseAbortWithNoRebaseInProgressIsNotLoggedAsFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	wtDir := t.TempDir()
	run(t, wtDir, "git", "init", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(wtDir, "f"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wtDir, "git", "add", "f")
	run(t, wtDir, "git", "commit", "-m", "base")

	logged := captureRefineryLog(t)
	out, err := gitCmdOutput(wtDir, "rebase", "--abort")

	// The error still reaches the caller — this ticket changes how the outcome
	// is REPORTED, not whether callers can see it. probeAlreadyMerged ignores
	// it deliberately; nothing else may be silently rerouted.
	if err == nil {
		t.Fatalf("expected rebase --abort to fail with no rebase in progress, got nil error (output %q)", out)
	}
	if out != "fatal: no rebase in progress" {
		t.Skipf("git wording changed: got %q — the demotion is keyed on the exact text, so this is the safe form working, not a test failure", out)
	}

	got := logged()
	if strings.Contains(got, "failed") {
		t.Errorf("benign outcome still logged as a failure:\n%s", got)
	}
	if !strings.Contains(got, "expected outcome") {
		t.Errorf("benign outcome not logged as expected outcome:\n%s", got)
	}
}

// TestDifferentRebaseAbortFailureStillLogsAsError is the acceptance criterion
// for mg-5d3f, and the reason the ticket asked for it specifically: a
// command-level match and a full-message match are indistinguishable on the 245
// benign lines, and differ only here.
//
// The failure is not contrived. A concurrent git process holding
// .git/index.lock is something the refinery can really hit — it runs git
// against worktrees that other agents and gitgc also touch — so this is a
// rehearsal of a real failure mode that a command-level match would swallow.
func TestDifferentRebaseAbortFailureStillLogsAsError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	wtDir := t.TempDir()
	run(t, wtDir, "git", "init", "-b", "main", ".")
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(wtDir, "f"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("base\n")
	run(t, wtDir, "git", "add", "f")
	run(t, wtDir, "git", "commit", "-m", "base")
	run(t, wtDir, "git", "checkout", "-b", "side")
	write("side\n")
	run(t, wtDir, "git", "commit", "-am", "side")
	run(t, wtDir, "git", "checkout", "main")
	write("main\n")
	run(t, wtDir, "git", "commit", "-am", "main")
	run(t, wtDir, "git", "checkout", "side")

	// Conflicts on purpose: a rebase is now IN PROGRESS, so the abort has real
	// work to do and cannot report "no rebase in progress".
	rebaseOut := runTolerant(t, wtDir, "git", "rebase", "main")
	if _, statErr := os.Stat(filepath.Join(wtDir, ".git", "rebase-merge")); statErr != nil {
		t.Skipf("fixture did not leave a rebase in progress (git output: %s)", rebaseOut)
	}

	// A concurrent git process holding the index lock.
	lock := filepath.Join(wtDir, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	logged := captureRefineryLog(t)
	out, err := gitCmdOutput(wtDir, "rebase", "--abort")
	if err == nil {
		t.Fatalf("fixture failed to break rebase --abort: got nil error (output %q)", out)
	}
	if out == "fatal: no rebase in progress" {
		t.Fatalf("fixture produced the benign message, so it cannot test the distinction: %q", out)
	}

	if note, benign := benignGitOutcome(out); benign {
		t.Fatalf("a DIFFERENT rebase --abort failure was classified benign as %q — the demotion is matching the command, not the message.\ngit output: %s", note, out)
	}

	got := logged()
	if !strings.Contains(got, "failed") {
		t.Errorf("a different rebase --abort failure no longer logs as a failure — a new variant would be swallowed:\n%s\ngit output: %s", got, out)
	}
	if !strings.Contains(got, out) {
		t.Errorf("log line dropped the git output that identifies the new variant:\n%s\ngit output: %s", got, out)
	}
}

// TestBenignGitOutcomeMatchesFullMessageOnly pins the matching rule itself: a
// near-miss must NOT be demoted. Each case below would be swallowed by a
// prefix, substring, or command match, and the ticket names that fallback as
// the defect.
func TestBenignGitOutcomeMatchesFullMessageOnly(t *testing.T) {
	benign := "fatal: no rebase in progress"
	if _, ok := benignGitOutcome(benign); !ok {
		t.Fatalf("the measured benign outcome %q is not demoted", benign)
	}

	notBenign := []struct {
		name string
		out  string
	}{
		{"index lock held by another git process",
			"error: Unable to create '/tmp/wt/.git/index.lock': File exists.\n\nAnother git process seems to be running in this repository."},
		{"unstaged changes block the abort",
			"error: cannot rebase: You have unstaged changes."},
		{"benign text as a prefix of a longer failure",
			"fatal: no rebase in progress\nfatal: could not move back to HEAD"},
		{"benign text embedded in a longer failure",
			"error: something else went wrong: fatal: no rebase in progress"},
		{"same wording, different severity word",
			"error: no rebase in progress"},
		{"not a rebase failure at all",
			"fatal: could not read from remote repository"},
	}
	for _, tc := range notBenign {
		t.Run(tc.name, func(t *testing.T) {
			if note, ok := benignGitOutcome(tc.out); ok {
				t.Errorf("classified benign as %q, so it would be demoted:\n%s", note, tc.out)
			}
		})
	}
}
