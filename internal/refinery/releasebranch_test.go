package refinery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustAncestor wraps isAncestor so a failed probe is fatal rather than a
// "no": a mangled revspec must not read as "absent".
func mustAncestor(t *testing.T, dir, a, b string) bool {
	t.Helper()
	ok, err := isAncestor(dir, a, b)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// TestReleaseBranchCut_TagsTheSoakedSHANotMainsTip is the mg-d734 dry run of
// the soak-before-release cut (mg-8382, docs/release-process.md).
//
// The candidate is the SHA this box ran for 24h. By the time it has soaked,
// main has moved on, so the cut must not tag main or main+bump. Instead:
//
//	release/vX.Y.Z is created on origin AT the candidate,
//	a polecat based on it commits the version bump,
//	`pogo refinery submit <branch> --target release/vX.Y.Z --post-merge-tag vX.Y.Z`.
//
// This pins what that path does today: the existing release branch is used as
// is (never re-carved from the default branch, even with auto-create on), the
// tag lands on the commit the merge produced on the release branch, that
// commit's first parent is the candidate, and main's later commits are absent
// from the tag. The origin is a bare temp repo, so no real tag goes anywhere.
func TestReleaseBranchCut_TagsTheSoakedSHANotMainsTip(t *testing.T) {
	useTempEventLog(t)
	originDir := initBareOrigin(t, "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	// The candidate: what the nightly installed and the box soaked.
	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	os.WriteFile(filepath.Join(workDir, "version.go"), []byte("package main\n\nconst Version = \"9.9.8\"\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: the candidate (mg-cand)")
	run(t, workDir, "git", "push", "origin", "main")
	candidate := gitOutput(t, workDir, "rev-parse", "HEAD")

	// Main moves on during the 24h soak. None of this may ship.
	os.WriteFile(filepath.Join(workDir, "unsoaked.txt"), []byte("merged during the soak\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: landed during the soak (mg-late)")
	run(t, workDir, "git", "push", "origin", "main")
	later := gitOutput(t, workDir, "rev-parse", "HEAD")

	// Step 1: create release/v9.9.9 on origin AT the candidate SHA.
	const release = "release/v9.9.9"
	const tag = "v9.9.9"
	run(t, workDir, "git", "push", "origin", candidate+":refs/heads/"+release)

	// Steps 2-3: the release-cut polecat's worktree is based on
	// origin/release/v9.9.9 (spawn-polecat --branch), and it commits the bump.
	run(t, workDir, "git", "fetch", "origin")
	run(t, workDir, "git", "checkout", "-b", "polecat-cut", "origin/"+release)
	os.WriteFile(filepath.Join(workDir, "version.go"), []byte("package main\n\nconst Version = \"9.9.9\"\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "chore: Bump version to 9.9.9")
	run(t, workDir, "git", "push", "origin", "polecat-cut")

	// Step 4: submit to the release branch with a post-merge tag. Auto-create
	// is ON deliberately: it must not fire for a target that already exists.
	r, _ := newPostMergeRefinery(t)
	id, err := r.Submit(MergeRequest{
		RepoPath:            originDir,
		Branch:              "polecat-cut",
		TargetRef:           release,
		Author:              "mg-cut0",
		PostMergeTag:        tag,
		AutoCreateTargetRef: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, originDir, "rev-parse", "refs/heads/"+release); got != candidate {
		t.Fatalf("Submit moved %s to %s; it must stay at the candidate %s — an existing target is never re-carved from the default branch", release, got, candidate)
	}
	r.processNext()

	mr := r.Get(id)
	if mr == nil || mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %+v", mr)
	}
	if mr.PostMergeError != "" {
		t.Fatalf("post-merge tag failed: %s", mr.PostMergeError)
	}
	// A non-default target is an integration branch to the refinery, so the
	// cutting polecat is NOT auto-completed: it must `mg done` itself.
	if !mr.PRFlow {
		t.Errorf("PRFlow = false for target %s; docs/release-process.md tells the cutting polecat to mg done itself because this is true", release)
	}

	releaseTip := gitOutput(t, originDir, "rev-parse", "refs/heads/"+release)
	tagSHA := originTagSHA(t, originDir, tag)
	if tagSHA == "" {
		t.Fatalf("%s not on origin", tag)
	}
	if tagSHA != mr.MergedSHA || tagSHA != releaseTip {
		t.Fatalf("tag %s at %s, MergedSHA %s, %s tip %s — the tag must be on the release-branch merge", tag, tagSHA, mr.MergedSHA, release, releaseTip)
	}
	if parent := gitOutput(t, originDir, "rev-parse", tagSHA+"^1"); parent != candidate {
		t.Errorf("tag's first parent is %s, want the candidate %s", parent, candidate)
	}
	// Positive control first: `later` IS on main, so the instrument can say yes.
	if !mustAncestor(t, originDir, later, "refs/heads/main") {
		t.Fatal("control failed: the soak-time commit is not on main")
	}
	if mustAncestor(t, originDir, later, tagSHA) {
		t.Errorf("main's soak-time commit %s is in %s — the release shipped unsoaked code", later, tag)
	}
	if got := gitOutput(t, originDir, "rev-parse", "refs/heads/main"); got != later {
		t.Errorf("main moved to %s during the cut; want it untouched at %s", got, later)
	}
	if got := gitOutput(t, originDir, "show", tagSHA+":version.go"); !strings.Contains(got, `"9.9.9"`) {
		t.Errorf("tagged tree's version.go = %q, want 9.9.9", got)
	}

	// Step 5: carry the bump back to main. The refinery rebases the release
	// branch onto main, which replays only the bump (the rest is already
	// there). The tag stays where it is, and the release branch survives.
	id2, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    release,
		TargetRef: "main",
		Author:    "mg-cut0",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()
	if mr2 := r.Get(id2); mr2 == nil || mr2.Status != StatusMerged {
		t.Fatalf("back-port to main: expected merged, got %+v", mr2)
	}
	if got := gitOutput(t, originDir, "show", "refs/heads/main:version.go"); !strings.Contains(got, `"9.9.9"`) {
		t.Errorf("main's version.go after the back-port = %q, want 9.9.9", got)
	}
	if !mustAncestor(t, originDir, later, "refs/heads/main") {
		t.Error("the back-port dropped main's soak-time commit")
	}
	if got := originTagSHA(t, originDir, tag); got != tagSHA {
		t.Errorf("tag moved from %s to %s during the back-port", tagSHA, got)
	}
	if got := gitOutput(t, originDir, "rev-parse", "refs/heads/"+release); got != releaseTip {
		t.Errorf("%s moved to %s during the back-port; want %s", release, got, releaseTip)
	}
}
