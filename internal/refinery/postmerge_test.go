package refinery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// releaseCutFixture builds the shape of the merge that lost the v0.8.0 tag: a
// bare origin on main, and a polecat branch carrying a version bump pushed to
// it. Nothing here declares post-merge work and nothing targets an integration
// branch — that is the point. The defect was invisible precisely because a
// release cut looks like every other default-branch merge.
func releaseCutFixture(t *testing.T) (originDir, branch string) {
	t.Helper()
	originDir = initBareOrigin(t, "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "init build")
	run(t, workDir, "git", "push", "origin", "main")

	branch = "polecat-e084"
	run(t, workDir, "git", "checkout", "-b", branch)
	os.WriteFile(filepath.Join(workDir, "version.go"), []byte(`package main

const Version = "0.8.0"
`), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "chore: Bump version to 0.8.0")
	run(t, workDir, "git", "push", "origin", branch)
	return originDir, branch
}

func newPostMergeRefinery(t *testing.T) (*Refinery, string) {
	t.Helper()
	wtDir := t.TempDir()
	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: wtDir})
	if err != nil {
		t.Fatal(err)
	}
	return r, wtDir
}

// originTagSHA reads what origin's copy of a tag points at, peeling annotated
// tags through to their commit, and returns "" when origin has no such tag.
// Asked of the ORIGIN rather than the refinery's clone: a tag that exists only
// locally is exactly the failure this feature exists to prevent, so the
// assertion has to be about what was pushed.
//
// A missing tag is a normal, expected answer here — several tests assert
// absence, and one asks from inside an OnMerged callback on another goroutine —
// so this must not be built on a helper that calls t.Fatal.
func originTagSHA(t *testing.T, originDir, tag string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-list", "-n", "1", tag)
	cmd.Dir = originDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestPostMergeTag_ReleaseCutTagsTheMergedSHA is the mg-6879 acceptance
// fixture, and it is written to fail against the old design in the specific way
// the v0.8.0 cut failed.
//
// The release cut's protocol is: merge the version bump, then tag the commit it
// landed as. Under the old design the polecat performed step two and was reaped
// three seconds after step one, so the tag never existed while the work item
// read done. Here the REFINERY performs step two, inside the merge pipeline,
// before OnMerged fires at all.
//
// The assertion is not "a tag exists" but "the tag is on the commit the
// refinery actually committed". That distinction is the whole design: a tag
// created before the merge would point at the pre-rebase SHA, which the
// refinery rewrites (mg-cef7's dangling-tag defect), so tagging early and
// tagging late are each other's failure mode.
func TestPostMergeTag_ReleaseCutTagsTheMergedSHA(t *testing.T) {
	logPath := useTempEventLog(t)
	originDir, branch := releaseCutFixture(t)
	r, _ := newPostMergeRefinery(t)

	id, err := r.Submit(MergeRequest{
		RepoPath:     originDir,
		Branch:       branch,
		TargetRef:    "main",
		Author:       "mg-e084",
		PostMergeTag: "v0.8.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	if mr == nil || mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %+v", mr)
	}
	if mr.PostMergeError != "" {
		t.Fatalf("post-merge step failed: %s", mr.PostMergeError)
	}

	// The MR must be able to name the commit. Before mg-6879 the refinery
	// computed this and discarded it, which is why no actor downstream of the
	// merge could tag the right SHA.
	if mr.MergedSHA == "" {
		t.Fatal("MergedSHA is empty — no actor downstream of the merge can name the commit that landed")
	}

	// main's tip on origin is what the refinery pushed.
	mainSHA := strings.TrimSpace(gitOutput(t, originDir, "rev-parse", "main"))
	if mr.MergedSHA != mainSHA {
		t.Errorf("MergedSHA %s is not origin/main's tip %s — the recorded SHA is not what landed", mr.MergedSHA, mainSHA)
	}

	// The tag must be ON ORIGIN (not merely local) and on the merged commit.
	tagSHA := originTagSHA(t, originDir, "v0.8.0")
	if tagSHA == "" {
		t.Fatal("v0.8.0 does not exist on origin — the release is half-cut, which is the mg-6879 defect")
	}
	if tagSHA != mr.MergedSHA {
		t.Errorf("v0.8.0 points at %s but the merge landed as %s — the tag is dangling off the wrong commit (the mg-cef7 shape)", tagSHA, mr.MergedSHA)
	}

	// git describe is how a release tag is normally read back, and it only
	// finds annotated tags by default. A lightweight tag here would leave
	// `git describe --tags` on main reporting the previous release.
	if got := strings.TrimSpace(gitOutput(t, originDir, "describe", "--tags", "main")); !strings.HasPrefix(got, "v0.8.0") {
		t.Errorf("git describe on main reads %q, want it to find v0.8.0", got)
	}

	tagged := filterEvents(readEvents(t, logPath), "refinery_post_merge_tagged")
	if len(tagged) != 1 {
		t.Fatalf("expected 1 refinery_post_merge_tagged event, got %d", len(tagged))
	}
	if got := tagged[0].Details["merged_sha"]; got != mr.MergedSHA {
		t.Errorf("event records merged_sha %v, want %s", got, mr.MergedSHA)
	}
}

// TestPostMergeTag_RunsBeforeOnMergedFires pins the ordering that makes the
// race structurally impossible rather than merely unlikely.
//
// Every prior fix in this area (--defer-done, PRFlow/mg-7746, the post-merge-
// work tag/mg-d86e) kept the worker as the acting party and bought it time
// against the reap. This one finishes the work before the reap can observe the
// merge: OnMerged is fired by processNext only after processMerge returns, and
// processMerge does not return until the tag is pushed. If the tag step were
// ever moved after the callback, this test fails.
func TestPostMergeTag_RunsBeforeOnMergedFires(t *testing.T) {
	originDir, branch := releaseCutFixture(t)
	r, _ := newPostMergeRefinery(t)

	tagVisibleAtCallback := make(chan string, 1)
	r.SetOnMerged(func(mr *MergeRequest) {
		tagVisibleAtCallback <- originTagSHA(t, originDir, "v0.8.0")
	})

	id, err := r.Submit(MergeRequest{
		RepoPath: originDir, Branch: branch, TargetRef: "main",
		Author: "mg-e084", PostMergeTag: "v0.8.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	var seen string
	select {
	case seen = <-tagVisibleAtCallback:
	case <-time.After(30 * time.Second):
		t.Fatal("OnMerged never fired")
	}

	mr := r.Get(id)
	if seen == "" {
		t.Fatal("the tag did not exist on origin when OnMerged fired — the reap can still land between the merge and the tag (mg-6879)")
	}
	if seen != mr.MergedSHA {
		t.Errorf("tag visible at callback time is %s, want the merged SHA %s", seen, mr.MergedSHA)
	}
}

// TestPostMergeTag_ExistingTagOnSameSHAIsIdempotent covers the resubmit path.
// A polecat that loses track of its MR resubmits the branch (gh #34); the
// refinery resolves it as already-merged. Re-running the tag step must converge
// on its own prior work rather than failing, or a benign resubmit would report
// a broken release.
func TestPostMergeTag_ExistingTagOnSameSHAIsIdempotent(t *testing.T) {
	originDir, branch := releaseCutFixture(t)
	r, _ := newPostMergeRefinery(t)

	first, err := r.Submit(MergeRequest{
		RepoPath: originDir, Branch: branch, TargetRef: "main",
		Author: "mg-e084", PostMergeTag: "v0.8.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()
	landed := r.Get(first).MergedSHA

	// Resubmit the same branch — now an ancestor of main.
	second, err := r.Submit(MergeRequest{
		RepoPath: originDir, Branch: branch, TargetRef: "main",
		Author: "mg-e084", PostMergeTag: "v0.8.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(second)
	if mr.Status != StatusMerged {
		t.Fatalf("resubmit should resolve as merged, got %s", mr.Status)
	}
	if !mr.AlreadyMerged {
		t.Error("resubmit should take the already-merged no-op path")
	}
	if mr.PostMergeError != "" {
		t.Errorf("re-running the tag step on an unchanged tag must succeed, got: %s", mr.PostMergeError)
	}
	if got := originTagSHA(t, originDir, "v0.8.0"); got != landed {
		t.Errorf("tag moved to %s across a resubmit, want it stable at %s", got, landed)
	}
}

// TestPostMergeTag_AlreadyMergedBranchStillGetsItsMissingTag is the recovery
// path, and it is the state v0.8.0 was actually found in: the branch had landed
// on main and the tag did not exist.
//
// The already-merged path skips gates, push and deploy because repeating them
// is a no-op. The tag step is NOT a no-op there — a branch can be merged with
// its tag never pushed, which is the defect itself — so skipping it would mean
// the refinery could not repair the very failure it exists to prevent. A
// resubmit is then the supported way to finish a half-cut release.
func TestPostMergeTag_AlreadyMergedBranchStillGetsItsMissingTag(t *testing.T) {
	originDir, branch := releaseCutFixture(t)
	r, _ := newPostMergeRefinery(t)

	// Land the branch with NO tag declared — reproducing the half-cut state.
	first, err := r.Submit(MergeRequest{
		RepoPath: originDir, Branch: branch, TargetRef: "main", Author: "mg-e084",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()
	landed := r.Get(first).MergedSHA
	if originTagSHA(t, originDir, "v0.8.0") != "" {
		t.Fatal("fixture must start with no tag on origin")
	}

	// Resubmit the merged branch, this time declaring the tag.
	second, err := r.Submit(MergeRequest{
		RepoPath: originDir, Branch: branch, TargetRef: "main",
		Author: "mg-e084", PostMergeTag: "v0.8.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(second)
	if !mr.AlreadyMerged {
		t.Fatalf("expected the already-merged no-op path, got %+v", mr)
	}
	if mr.PostMergeError != "" {
		t.Fatalf("the missing tag should have been created on resubmit, got: %s", mr.PostMergeError)
	}
	if got := originTagSHA(t, originDir, "v0.8.0"); got != landed {
		t.Errorf("tag on origin is %q, want the already-landed commit %s — an already-merged MR must still complete its declared post-merge step", got, landed)
	}
}

// TestPostMergeTag_ExistingTagOnDifferentSHAFailsLoudly is the other direction,
// and it must NOT be idempotent. A release tag that silently relocates is worse
// than a missing one: consumers have already resolved it. The merge stands (it
// landed remotely and is not unwound), but the step reports failure so the work
// item cannot be completed.
func TestPostMergeTag_ExistingTagOnDifferentSHAFailsLoudly(t *testing.T) {
	logPath := useTempEventLog(t)
	originDir, branch := releaseCutFixture(t)

	// Pre-existing v0.8.0 on origin, pointing at main's tip BEFORE the merge —
	// i.e. exactly what `bump-version.sh --tag` would have left behind.
	preMerge := strings.TrimSpace(gitOutput(t, originDir, "rev-parse", "main"))
	tagWork := t.TempDir()
	run(t, tagWork, "git", "clone", originDir, ".")
	run(t, tagWork, "git", "config", "user.email", "test@test.com")
	run(t, tagWork, "git", "config", "user.name", "Test")
	run(t, tagWork, "git", "tag", "v0.8.0", preMerge)
	run(t, tagWork, "git", "push", "origin", "v0.8.0")

	r, _ := newPostMergeRefinery(t)
	id, err := r.Submit(MergeRequest{
		RepoPath: originDir, Branch: branch, TargetRef: "main",
		Author: "mg-e084", PostMergeTag: "v0.8.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	// The merge itself is not unwound — it already landed on origin.
	if mr.Status != StatusMerged {
		t.Errorf("the merge landed remotely and must stay merged, got %s", mr.Status)
	}
	if mr.PostMergeError == "" {
		t.Fatal("a tag that already exists on a DIFFERENT commit must fail loudly, not be treated as done")
	}
	if !strings.Contains(mr.PostMergeError, preMerge) || !strings.Contains(mr.PostMergeError, mr.MergedSHA) {
		t.Errorf("the error must name both commits so a human can adjudicate, got: %s", mr.PostMergeError)
	}

	// The published tag must be untouched.
	if got := originTagSHA(t, originDir, "v0.8.0"); got != preMerge {
		t.Errorf("the existing tag was MOVED to %s — a published tag must never be relocated (was %s)", got, preMerge)
	}

	failed := filterEvents(readEvents(t, logPath), "refinery_post_merge_tag_failed")
	if len(failed) != 1 {
		t.Errorf("expected 1 refinery_post_merge_tag_failed event, got %d", len(failed))
	}
}

// TestPostMergeTag_AbsentByDefault keeps the feature opt-in: a merge that
// declares no tag must not gain a step, and must record its SHA regardless.
func TestPostMergeTag_AbsentByDefault(t *testing.T) {
	logPath := useTempEventLog(t)
	originDir, branch := releaseCutFixture(t)
	r, _ := newPostMergeRefinery(t)

	id, err := r.Submit(MergeRequest{
		RepoPath: originDir, Branch: branch, TargetRef: "main", Author: "mg-e084",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	if mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %+v", mr)
	}
	if mr.PostMergeError != "" {
		t.Errorf("no tag was declared, so no step should have run or failed, got: %s", mr.PostMergeError)
	}
	// MergedSHA is unconditional — it is the record of what landed, useful to
	// every reader whether or not a post-merge step was declared.
	if mr.MergedSHA == "" {
		t.Error("MergedSHA must be recorded on every merge, not only tagged ones")
	}
	if n := len(filterEvents(readEvents(t, logPath), "refinery_post_merge_tagged")); n != 0 {
		t.Errorf("expected no post-merge-tag events when none declared, got %d", n)
	}
}

// TestSubmitRejectsMalformedPostMergeTag keeps a typo cheap. Caught at submit
// the author retries; caught after the merge it costs a half-finished release,
// because the merge is not unwound.
func TestSubmitRejectsMalformedPostMergeTag(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	r, _ := newPostMergeRefinery(t)

	for _, bad := range []string{"v 0.8.0", "-v0.8.0", "v0.8.0..1", "refs/tags/v1^", "v1.lock"} {
		if _, err := r.Submit(MergeRequest{
			RepoPath: originDir, Branch: "polecat-x", TargetRef: "main",
			Author: "mg-e084", PostMergeTag: bad,
		}); err == nil {
			t.Errorf("Submit accepted malformed post_merge_tag %q", bad)
		}
	}
}

func TestValidTagName(t *testing.T) {
	valid := []string{"v0.8.0", "v1.0.0-rc.1", "release/2026-07-30", "v0.8.0+build.1"}
	for _, name := range valid {
		if ok, why := validTagName(name); !ok {
			t.Errorf("validTagName(%q) = false (%s), want true", name, why)
		}
	}
	invalid := []string{"", "-x", "/v1", "v1/", "v1.", "v1.lock", "a..b", "a//b", "a@{b", "a b", "v1~1", "v1^", "v1:2", "v1?", "v1*", "v1[2]", "a\\b", "a\tb"}
	for _, name := range invalid {
		if ok, _ := validTagName(name); ok {
			t.Errorf("validTagName(%q) = true, want false", name)
		}
	}
}
