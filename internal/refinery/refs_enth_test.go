package refinery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// enth-vcs §8, slice 12 (mg-ae4ac): the release cut on an enth repo, against
// a REAL sandboxed enthd — a release branch made through the seam, a worker's
// land onto it recorded with a post-merge tag, the retry, and a conflicting
// tag. The git controls are the existing post-merge-tag and auto-create
// tests (postmerge_test.go, releasebranch_test.go), which this slice leaves
// untouched, plus TestRefCreateGitControl below.

func TestReleaseCutOnEnth(t *testing.T) {
	fx := newSelfLandFixture(t)
	fx.s.RequireRef(t)
	logPath := useTempEventLog(t)
	r, cl := newCallbackRefinery(t)

	// 1. The release branch, AT the candidate, through the seam.
	cand := fx.s.Tip(t)
	later, _ := fx.s.Work(t, "mg-0", "p0", "unsoaked", map[string]string{"u.txt": "u\n"})
	fx.land(t, "mg-0/1", later)
	res, err := r.CreateRef(RefCreateRequest{RepoPath: fx.repo, Ref: "release/v1.0.0", At: cand})
	if err != nil || res.VCS != vcs.Enth || res.Ref != "refs/heads/release/v1.0.0" || res.Change != cand || res.Existed {
		t.Fatalf("CreateRef = %+v %v; want refs/heads/release/v1.0.0 at the candidate", res, err)
	}
	// The retry converges; a re-carve from the trunk is refused.
	if res, err := r.CreateRef(RefCreateRequest{RepoPath: fx.repo, Ref: "release/v1.0.0", At: cand}); err != nil || !res.Existed {
		t.Fatalf("CreateRef retry = %+v %v; want Existed", res, err)
	}
	if _, err := r.CreateRef(RefCreateRequest{RepoPath: fx.repo, Ref: "release/v1.0.0"}); err == nil || !strings.Contains(err.Error(), "refusing to re-carve") {
		t.Fatalf("re-carve from the default branch = %v; want refused", err)
	}
	// No --at is the default branch: --auto-create-target's source.
	if res, err := r.CreateRef(RefCreateRequest{RepoPath: fx.repo, Ref: "integ"}); err != nil || res.At != "main" || res.Change != fx.s.Tip(t) {
		t.Fatalf("CreateRef from the default branch = %+v %v; want main's tip", res, err)
	}

	// 2. The worker's bump, landed onto the release branch, recorded with the
	// tag. The tag names what the land landed as.
	dir := filepath.Join(t.TempDir(), "rel")
	fx.s.Checkout(t, "refs/heads/release/v1.0.0", dir)
	writeFileIn(t, dir, "VERSION", "1.0.0\n")
	bump := fx.s.Change(t, dir, "chore: bump to 1.0.0\n\nPogo-Item: mg-rel\nPogo-Worker: prel\n")
	out := fx.land(t, "mg-rel/1", "--ref", "refs/heads/release/v1.0.0", bump)
	req := fx.request(t, "mg-rel", out)
	req.TargetRef, req.PostMergeTag, req.RequestID = "release/v1.0.0", "v1.0.0", "mg-rel/1"
	got, err := r.RecordSelfLand(req)
	if err != nil {
		t.Fatal(err)
	}
	mr := got.MR
	if mr.PostMergeError != "" || mr.PostMergeTag != "v1.0.0" || mr.Enth.Ref != "refs/heads/release/v1.0.0" {
		t.Fatalf("recorded MR = %+v; want the release land, tagged", mr)
	}
	if tip, _ := enthWork(fx.s.EnthRepo()).RefTip(t.Context(), "refs/tags/v1.0.0"); tip != mr.Enth.New {
		t.Fatalf("refs/tags/v1.0.0 = %s; want the release land %s", tip, mr.Enth.New)
	}
	if len(cl.merged) != 1 {
		t.Fatalf("OnMerged = %v; want once", cl.merged)
	}
	// The event names the Change under landed, never as merged_sha (§7).
	tagged := filterEvents(readEvents(t, logPath), "refinery_post_merge_tagged")
	if len(tagged) != 1 {
		t.Fatalf("refinery_post_merge_tagged events = %d; want 1", len(tagged))
	}
	if _, ok := tagged[0].Details["merged_sha"]; ok {
		t.Errorf("an enth tag event carries merged_sha: %v", tagged[0].Details)
	}
	if l, _ := tagged[0].Details["landed"].(map[string]any); l == nil || l["id"] != mr.Enth.New || l["vcs"] != "enth" {
		t.Errorf("the enth tag event's landed = %v; want {enth, %s}", tagged[0].Details["landed"], mr.Enth.New)
	}

	// 3. The retry: the tag is already where this land put it. A second
	// release item landing elsewhere and asking for the SAME tag is refused,
	// recorded with PostMergeError, and the tag does not move.
	if msg := r.tagSelfLanded(mr, mr.Enth.New); msg != "" {
		t.Fatalf("tag retry = %q; want success (already there)", msg)
	}
	fix, _ := fx.s.Work(t, "mg-rel2", "prel2", "fix", map[string]string{"f.txt": "f\n"})
	out2 := fx.land(t, "mg-rel2/1", fix)
	req2 := fx.request(t, "mg-rel2", out2)
	req2.PostMergeTag = "v1.0.0"
	got2, err := r.RecordSelfLand(req2)
	if err != nil {
		t.Fatalf("a land whose tag fails is still recorded: %v", err)
	}
	if !strings.Contains(got2.MR.PostMergeError, "refusing to move a published tag") {
		t.Fatalf("conflicting tag: PostMergeError = %q; want the refusal", got2.MR.PostMergeError)
	}
	if tip, _ := enthWork(fx.s.EnthRepo()).RefTip(t.Context(), "refs/tags/v1.0.0"); tip != mr.Enth.New {
		t.Fatalf("a conflicting tag moved refs/tags/v1.0.0 to %s", tip)
	}
	failed := filterEvents(readEvents(t, logPath), "refinery_post_merge_tag_failed")
	if len(failed) != 1 {
		t.Fatalf("refinery_post_merge_tag_failed events = %d; want 1", len(failed))
	}
	if _, ok := failed[0].Details["merged_sha"]; ok {
		t.Errorf("an enth tag-failed event carries merged_sha: %v", failed[0].Details)
	}

	// A malformed tag is refused before anything is recorded.
	bad := fx.request(t, "mg-rel2", out2)
	bad.PostMergeTag = "v1..0"
	if _, err := r.RecordSelfLand(bad); err == nil || !strings.Contains(err.Error(), "not a valid tag name") {
		t.Fatalf("malformed tag = %v; want refused", err)
	}
}

// TestRefCreateGitControl: the same verb on a git repo is the
// --auto-create-target step, unchanged — a branch off the default branch, and
// an existing ref refused.
func TestRefCreateGitControl(t *testing.T) {
	origin := initBareOrigin(t, "main")
	r, _ := newCallbackRefinery(t)
	res, err := r.CreateRef(RefCreateRequest{RepoPath: origin, Ref: "release/v9"})
	if err != nil || res.VCS != vcs.Git || res.Ref != "release/v9" || res.At != "main" {
		t.Fatalf("git CreateRef = %+v %v", res, err)
	}
	if err := validateTargetRef(origin, "release/v9"); err != nil {
		t.Fatalf("git release/v9 not created: %v", err)
	}
	if _, err := r.CreateRef(RefCreateRequest{RepoPath: origin, Ref: "release/v9"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("git re-create = %v; want refused", err)
	}
}

func writeFileIn(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
