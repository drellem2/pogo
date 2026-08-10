package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initOriginAndClone builds a bare origin carrying `main` and a working clone
// of it, and returns the clone. The clone is what a polecat spawn treats as
// the source repo.
func initOriginAndClone(t *testing.T) (clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	origin := t.TempDir()
	gitRun(t, origin, "init", "--bare", "-b", "main", ".")

	seed := t.TempDir()
	gitRun(t, seed, "clone", origin, ".")
	gitRun(t, seed, "config", "user.email", "test@test.com")
	gitRun(t, seed, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "-m", "init")
	gitRun(t, seed, "push", "origin", "main")

	clone = t.TempDir()
	gitRun(t, clone, "clone", origin, ".")
	return clone
}

// hangingGitOrigin starts a TCP listener that accepts git-protocol connections
// and then says nothing, ever. A `git fetch` against it connects, sends its
// request, and blocks in read — the unbounded-network-call shape mg-538e is
// about, reproduced without depending on the real network.
func hangingGitOrigin(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		var held []net.Conn
		for {
			c, err := ln.Accept()
			if err != nil {
				for _, h := range held {
					h.Close()
				}
				close(done)
				return
			}
			held = append(held, c) // accepted, never answered
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-done
	})
	return fmt.Sprintf("git://%s/hang.git", ln.Addr().String())
}

// TestControlTheFakeOriginActuallyHangs is the negative control for
// TestPolecatFetchIsBoundedByItsTimeout. Without it, that test would pass just
// as happily against an origin that refused the connection instantly — which
// would prove nothing about a bound, because there would be nothing to bound.
func TestControlTheFakeOriginActuallyHangs(t *testing.T) {
	repo := initOriginAndClone(t)
	gitRun(t, repo, "remote", "set-url", "origin", hangingGitOrigin(t))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_ = exec.CommandContext(ctx, "git", "-C", repo, "fetch", "origin").Run()
	elapsed := time.Since(start)

	if ctx.Err() == nil {
		t.Fatalf("the fake origin did not hang git fetch — it returned in %s. "+
			"TestPolecatFetchIsBoundedByItsTimeout is vacuous against this fixture.", elapsed)
	}
}

// TestPolecatFetchIsBoundedByItsTimeout is the mg-538e item-3 regression.
//
// `resolvePolecatBaseRef` ran `git fetch origin` with no CommandContext, no
// timeout and no retry bound — the only networked operation on the polecat
// spawn path, 104 lines before the work-item claim, and every spawn blocks on
// it. An origin that never answers turned a spawn into a permanent hang and
// left the work item `available` with a worktree already on disk.
//
// The bound reuses the failure semantics that already existed: the function
// logs and returns "", which means "base the worktree on local HEAD". So the
// property under test is *bounded, and falls back* — not *fails*.
func TestPolecatFetchIsBoundedByItsTimeout(t *testing.T) {
	repo := initOriginAndClone(t)
	gitRun(t, repo, "remote", "set-url", "origin", hangingGitOrigin(t))

	const bound = 750 * time.Millisecond
	done := make(chan string, 1)
	start := time.Now()
	go func() { done <- resolvePolecatBaseRefWithin(repo, "main", bound) }()

	var ref string
	select {
	case ref = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("resolvePolecatBaseRef never returned against an origin that never answers — " +
			"the fetch is still unbounded (mg-538e)")
	}
	elapsed := time.Since(start)

	// Two attempts (narrow, then wide) share ONE deadline, so the whole call is
	// bounded by the timeout and not by a multiple of it.
	if elapsed > 8*bound {
		t.Errorf("call took %s against a %s bound: the bound is not what stopped it", elapsed, bound)
	}
	if ref != "" {
		t.Errorf("a timed-out fetch must fall back to local HEAD (\"\"), got %q", ref)
	}
}

// TestPolecatFetchNarrowsToTheRefsItReads covers change (2) of item 3: the
// fetch asks for the branch it is about to resolve and the default branch,
// instead of all 707 refs (635 of them `polecat-*`, with prune unset).
func TestPolecatFetchNarrowsToTheRefsItReads(t *testing.T) {
	repo := initOriginAndClone(t)

	specs := polecatFetchRefspecs(context.Background(), repo, "some-target")
	joined := strings.Join(specs, " ")
	if !strings.Contains(joined, "+refs/heads/some-target:refs/remotes/origin/some-target") {
		t.Errorf("refspecs do not name the target branch: %v", specs)
	}
	if !strings.Contains(joined, "+refs/heads/main:refs/remotes/origin/main") {
		t.Errorf("refspecs do not name the default branch (needed for the origin/HEAD fallback): %v", specs)
	}
	if len(specs) != 2 {
		t.Errorf("expected exactly the two refs the resolver reads, got %v", specs)
	}
	// No duplicate when the target IS the default branch.
	if got := polecatFetchRefspecs(context.Background(), repo, "main"); len(got) != 1 {
		t.Errorf("target == default should produce one refspec, got %v", got)
	}
}

// TestPolecatBaseRefStillResolvesWhenTheTargetBranchIsNotOnOrigin is the
// no-regression half of the narrowing. git fails a fetch WHOLE when a refspec
// matches nothing on the remote, and a polecat targeting a branch that does not
// exist on origin yet is an ordinary case — the origin/HEAD fallback below
// exists for it. A narrow-only fetch would silently downgrade that case to
// "base on local HEAD", which is the exact invisibility this resolver was
// written to prevent.
func TestPolecatBaseRefStillResolvesWhenTheTargetBranchIsNotOnOrigin(t *testing.T) {
	repo := initOriginAndClone(t)

	if got := resolvePolecatBaseRefWithin(repo, "branch-that-is-not-on-origin", 30*time.Second); got != "origin/main" {
		t.Errorf("base ref = %q, want origin/main: the narrow fetch failed the whole resolution "+
			"instead of falling back", got)
	}
	// And the ordinary case still resolves to the target branch.
	if got := resolvePolecatBaseRefWithin(repo, "main", 30*time.Second); got != "origin/main" {
		t.Errorf("base ref for an existing branch = %q, want origin/main", got)
	}
}

// TestPolecatBaseRefWithNoOriginIsUnchanged: a repo without an origin never
// reaches the network at all, before or after this change.
func TestPolecatBaseRefWithNoOriginIsUnchanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main", ".")

	start := time.Now()
	if got := resolvePolecatBaseRefWithin(repo, "main", 30*time.Second); got != "" {
		t.Errorf("base ref = %q, want \"\" for a repo with no origin", got)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("no-origin path took %s: it should not be touching the network", elapsed)
	}
}
