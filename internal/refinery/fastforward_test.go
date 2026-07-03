package refinery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ffFixture builds the gh #30 scenario: a bare origin, a local source
// checkout on main, and origin/main advanced one commit past the checkout.
// Returns (originDir, checkoutDir, newTipSHA).
func ffFixture(t *testing.T) (string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	// The user's local checkout — the repo the MR was submitted from.
	checkoutDir := t.TempDir()
	run(t, checkoutDir, "git", "clone", originDir, ".")
	run(t, checkoutDir, "git", "config", "user.email", "test@test.com")
	run(t, checkoutDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(checkoutDir, "README.md"), []byte("# Test"), 0644)
	run(t, checkoutDir, "git", "add", ".")
	run(t, checkoutDir, "git", "commit", "-m", "initial commit")
	run(t, checkoutDir, "git", "push", "origin", "main")

	// Advance origin/main from a second clone (simulates the refinery's
	// worktree pushing a merged branch). The local checkout stays behind.
	otherDir := t.TempDir()
	run(t, otherDir, "git", "clone", originDir, ".")
	run(t, otherDir, "git", "config", "user.email", "test@test.com")
	run(t, otherDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(otherDir, "merged.txt"), []byte("merged"), 0644)
	run(t, otherDir, "git", "add", ".")
	run(t, otherDir, "git", "commit", "-m", "merged commit")
	run(t, otherDir, "git", "push", "origin", "main")
	newTip := strings.TrimSpace(runOut(t, otherDir, "git", "rev-parse", "HEAD"))

	return originDir, checkoutDir, newTip
}

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runOut(t, dir, "git", "rev-parse", "HEAD"))
}

func TestFastForwardSourceCheckoutCleanAdvances(t *testing.T) {
	_, checkoutDir, newTip := ffFixture(t)

	fastForwardSourceCheckout(checkoutDir, "main")

	if got := headSHA(t, checkoutDir); got != newTip {
		t.Errorf("expected checkout fast-forwarded to %s, got %s", newTip, got)
	}
	if _, err := os.Stat(filepath.Join(checkoutDir, "merged.txt")); err != nil {
		t.Error("merged.txt missing from working tree after fast-forward")
	}
}

func TestFastForwardSourceCheckoutUntrackedFilesDoNotBlock(t *testing.T) {
	_, checkoutDir, newTip := ffFixture(t)
	os.WriteFile(filepath.Join(checkoutDir, "scratch.txt"), []byte("wip"), 0644)

	fastForwardSourceCheckout(checkoutDir, "main")

	if got := headSHA(t, checkoutDir); got != newTip {
		t.Errorf("expected fast-forward with untracked file present, got %s (want %s)", got, newTip)
	}
	data, err := os.ReadFile(filepath.Join(checkoutDir, "scratch.txt"))
	if err != nil || string(data) != "wip" {
		t.Errorf("untracked file clobbered: %q, %v", data, err)
	}
}

func TestFastForwardSourceCheckoutSkipsDirtyTree(t *testing.T) {
	_, checkoutDir, _ := ffFixture(t)
	before := headSHA(t, checkoutDir)
	os.WriteFile(filepath.Join(checkoutDir, "README.md"), []byte("# local edit"), 0644)

	fastForwardSourceCheckout(checkoutDir, "main")

	if got := headSHA(t, checkoutDir); got != before {
		t.Errorf("dirty tree must not be touched: HEAD moved %s -> %s", before, got)
	}
	data, _ := os.ReadFile(filepath.Join(checkoutDir, "README.md"))
	if string(data) != "# local edit" {
		t.Errorf("local modification clobbered: %q", data)
	}
}

func TestFastForwardSourceCheckoutSkipsStagedChanges(t *testing.T) {
	_, checkoutDir, _ := ffFixture(t)
	before := headSHA(t, checkoutDir)
	os.WriteFile(filepath.Join(checkoutDir, "README.md"), []byte("# staged edit"), 0644)
	run(t, checkoutDir, "git", "add", "README.md")

	fastForwardSourceCheckout(checkoutDir, "main")

	if got := headSHA(t, checkoutDir); got != before {
		t.Errorf("tree with staged changes must not be touched: HEAD moved %s -> %s", before, got)
	}
}

func TestFastForwardSourceCheckoutSkipsWhenOnOtherBranch(t *testing.T) {
	_, checkoutDir, _ := ffFixture(t)
	run(t, checkoutDir, "git", "checkout", "-b", "side")
	before := headSHA(t, checkoutDir)

	fastForwardSourceCheckout(checkoutDir, "main")

	if got := headSHA(t, checkoutDir); got != before {
		t.Errorf("HEAD moved while on another branch: %s -> %s", before, got)
	}
	// The main ref itself must also be untouched (no branch -f behind HEAD).
	mainSHA := strings.TrimSpace(runOut(t, checkoutDir, "git", "rev-parse", "main"))
	if mainSHA != before {
		t.Errorf("main ref moved while checked out elsewhere: %s -> %s", before, mainSHA)
	}
}

func TestFastForwardSourceCheckoutSkipsDetachedHead(t *testing.T) {
	_, checkoutDir, _ := ffFixture(t)
	before := headSHA(t, checkoutDir)
	run(t, checkoutDir, "git", "checkout", "--detach", "HEAD")

	fastForwardSourceCheckout(checkoutDir, "main")

	if got := headSHA(t, checkoutDir); got != before {
		t.Errorf("HEAD moved while detached: %s -> %s", before, got)
	}
}

func TestFastForwardSourceCheckoutSkipsDivergedBranch(t *testing.T) {
	_, checkoutDir, _ := ffFixture(t)
	// Local commit not on origin — an ff is impossible; must never merge.
	os.WriteFile(filepath.Join(checkoutDir, "local.txt"), []byte("local"), 0644)
	run(t, checkoutDir, "git", "add", "local.txt")
	run(t, checkoutDir, "git", "commit", "-m", "local-only commit")
	before := headSHA(t, checkoutDir)

	fastForwardSourceCheckout(checkoutDir, "main")

	if got := headSHA(t, checkoutDir); got != before {
		t.Errorf("diverged branch must not be touched: HEAD moved %s -> %s", before, got)
	}
}

func TestFastForwardSourceCheckoutIgnoresBareRepo(t *testing.T) {
	originDir, _, _ := ffFixture(t)
	// Must be a silent no-op — bare origins are the RepoPath in many tests.
	fastForwardSourceCheckout(originDir, "main")
}

func TestFastForwardSourceCheckoutIgnoresMissingDir(t *testing.T) {
	fastForwardSourceCheckout(filepath.Join(t.TempDir(), "nope"), "main")
}

// TestProcessMergeFastForwardsSourceCheckout runs the full merge pipeline
// with RepoPath set to a real (non-bare) source checkout — the production
// shape of gh #30 — and verifies the checkout is advanced after the merge.
func TestProcessMergeFastForwardsSourceCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	// The user's checkout: clean, on main. MRs are submitted with this
	// path; ensureWorktree reads its origin URL for the refinery clone.
	checkoutDir := t.TempDir()
	run(t, checkoutDir, "git", "clone", originDir, ".")
	run(t, checkoutDir, "git", "config", "user.email", "test@test.com")
	run(t, checkoutDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(checkoutDir, "README.md"), []byte("# Test"), 0644)
	run(t, checkoutDir, "git", "add", ".")
	run(t, checkoutDir, "git", "commit", "-m", "initial commit")
	run(t, checkoutDir, "git", "push", "origin", "main")

	// Feature branch pushed from a separate clone (a polecat's worktree).
	polecatDir := t.TempDir()
	run(t, polecatDir, "git", "clone", originDir, ".")
	run(t, polecatDir, "git", "config", "user.email", "test@test.com")
	run(t, polecatDir, "git", "config", "user.name", "Test")
	run(t, polecatDir, "git", "checkout", "-b", "feature-ff")
	os.WriteFile(filepath.Join(polecatDir, "feature.txt"), []byte("new feature"), 0644)
	run(t, polecatDir, "git", "add", ".")
	run(t, polecatDir, "git", "commit", "-m", "add feature")
	run(t, polecatDir, "git", "push", "origin", "feature-ff")

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	id, err := r.Submit(MergeRequest{
		RepoPath:  checkoutDir,
		Branch:    "feature-ff",
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
		t.Fatalf("expected merged, got %s (error: %s)", mr.Status, mr.Error)
	}

	// The local checkout must now contain the merged commit.
	if _, err := os.Stat(filepath.Join(checkoutDir, "feature.txt")); err != nil {
		t.Error("source checkout not fast-forwarded: feature.txt missing")
	}
	onBranch := strings.TrimSpace(runOut(t, checkoutDir, "git", "symbolic-ref", "--short", "HEAD"))
	if onBranch != "main" {
		t.Errorf("checkout left on %q, want main", onBranch)
	}
	status := strings.TrimSpace(runOut(t, checkoutDir, "git", "status", "--porcelain"))
	if status != "" {
		t.Errorf("checkout left dirty after fast-forward:\n%s", status)
	}
}
