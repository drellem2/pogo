package gitvcs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
)

// The behaviour of every Workspaces method is pinned by internal/agent's and
// internal/freshen's existing tests, which drive it through the polecat spawn
// handler and freshenWorkspace and were not edited by slice 2 (mg-cc7ae). This
// test pins the backend's own contract, which those callers rely on but cannot
// see: a failed command is a *vcs.CommandError carrying git's verbatim output,
// and each method answers against a real worktree.
func TestWorkspacesLifecycleAgainstARealOrigin(t *testing.T) {
	_, clone := fixture(t, false)
	ctx := context.Background()
	ws := Workspaces{FetchTimeout: time.Minute}
	repo := vcs.Repo{Path: clone}

	base := ws.ResolveBase(ctx, repo, "feature")
	if base != "origin/feature" {
		t.Fatalf("ResolveBase(feature) = %q, want origin/feature", base)
	}
	if got := ws.ResolveBase(ctx, repo, "not-on-origin"); got != "origin/main" {
		t.Fatalf("ResolveBase(not-on-origin) = %q, want the origin/HEAD fallback origin/main", got)
	}

	dir := filepath.Join(t.TempDir(), "w1")
	const branch = "polecat-w1"
	if ws.BranchExists(ctx, repo, branch) {
		t.Fatal("BranchExists before Create: the fixture already has the branch, so nothing below is measured")
	}
	got, err := ws.Create(ctx, vcs.WorkspaceSpec{Repo: repo, Dir: dir, Branch: branch, Base: base})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Dir != dir || got.Branch != branch || got.Base != base {
		t.Fatalf("Create returned %+v", got)
	}
	if !ws.BranchExists(ctx, repo, branch) {
		t.Fatal("BranchExists after Create = false")
	}
	if !ws.BranchSpent(ctx, repo, branch, base) {
		t.Fatal("BranchSpent = false for a branch with no commits of its own")
	}
	// The positive control for BranchSpent: main does not contain feature.
	if ws.BranchSpent(ctx, repo, "origin/feature", "origin/main") {
		t.Fatal("BranchSpent(origin/feature, origin/main) = true; feature carries a commit main lacks")
	}
	if err := ws.Contains(ctx, repo, base, branch); err != nil {
		t.Fatalf("Contains(%s, %s): %v", base, branch, err)
	}
	if err := ws.Contains(ctx, repo, "origin/feature", "origin/main"); err == nil {
		t.Fatal("Contains(origin/feature, origin/main) = nil; main does not contain feature")
	}

	// Exclude: written once under its group header, idempotent on repeat.
	for i := 0; i < 2; i++ {
		if err := ws.Exclude(ctx, dir, "# group", ".x/y.md"); err != nil {
			t.Fatalf("Exclude #%d: %v", i, err)
		}
	}
	excl, err := excludePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(excl)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "/.x/y.md\n") != 1 || strings.Count(string(data), "# group\n") != 1 {
		t.Fatalf("exclude file after two Excludes:\n%s", data)
	}

	// A second Create on the occupied path fails with git's own words.
	_, err = ws.Create(ctx, vcs.WorkspaceSpec{Repo: repo, Dir: dir, Branch: "polecat-w2", Base: base})
	var ce *vcs.CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("Create on an occupied path: %T %v, want *vcs.CommandError", err, err)
	}
	if !strings.Contains(string(ce.Output), "already exists") || ce.Err == nil {
		t.Fatalf("CommandError does not carry git's output: %+v", ce)
	}

	if err := ws.Rollback(ctx, repo, dir); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Rollback left %s behind (%v)", dir, err)
	}
	for _, b := range []string{branch, "polecat-w2"} {
		if ws.BranchExists(ctx, repo, b) {
			if err := ws.DeleteBranch(ctx, repo, b); err != nil {
				t.Fatalf("DeleteBranch(%s): %v", b, err)
			}
		}
	}
	if ws.BranchExists(ctx, repo, branch) {
		t.Fatal("BranchExists after DeleteBranch = true")
	}
	if err := ws.DeleteBranch(ctx, repo, branch); !errors.As(err, &ce) {
		t.Fatalf("DeleteBranch of a missing branch: %T %v, want *vcs.CommandError", err, err)
	}

	if r := ws.Refresh(ctx, filepath.Join(t.TempDir(), "absent")); r.Status != vcs.RefreshSkipped {
		t.Fatalf("Refresh of a missing dir = %+v, want skipped", r)
	}
}

// ResolveBase with no origin answers "" (base on local HEAD), never an error.
func TestWorkspacesResolveBaseWithoutOrigin(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	if got := (Workspaces{FetchTimeout: time.Minute}).ResolveBase(context.Background(), vcs.Repo{Path: dir}, "main"); got != "" {
		t.Fatalf("ResolveBase with no origin = %q, want \"\"", got)
	}
}
