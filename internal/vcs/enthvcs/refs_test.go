package enthvcs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
)

// enth-vcs §8, slice 12 (mg-ae4ac): tags and release refs against a REAL
// sandboxed enthd — a tag, a retry, a conflicting tag, and a release branch
// landed onto.

func TestDefaultIsTheTrunk(t *testing.T) {
	for trunk, want := range map[string]string{"": "main", "refs/heads/main": "main", "refs/heads/trunk": "trunk"} {
		got, err := Backend{Repo: vcs.EnthRepo{Trunk: trunk}}.Default(context.Background(), vcs.Repo{})
		if err != nil || got != want {
			t.Errorf("trunk %q: Default = %q %v; want %q", trunk, got, err, want)
		}
	}
}

func TestTagAgainstEnthd(t *testing.T) {
	s := enthdtest.Start(t)
	s.RequireRef(t)
	b := Backend{Repo: s.EnthRepo(), Bin: s.Enth()}
	ctx := t.Context()

	first := s.Tip(t)
	res, err := b.Tag(ctx, vcs.Repo{}, "v1.0.0", first, "ignored")
	if err != nil || res != vcs.TagCreated {
		t.Fatalf("Tag = %v %v; want TagCreated", res, err)
	}
	if got := showChange(t, s, "refs/tags/v1.0.0"); got != first {
		t.Fatalf("refs/tags/v1.0.0 = %s; want %s", got, first)
	}

	// The retry: enth refuses tag_exists even at the same Change, and the
	// backend reads that as the tag already being where it was asked.
	res, err = b.Tag(ctx, vcs.Repo{}, "v1.0.0", first, "ignored")
	if err != nil || res != vcs.TagAlreadyPresent {
		t.Fatalf("retry = %v %v; want TagAlreadyPresent", res, err)
	}
	// A prefix names the same Change: still the retry, not a conflict.
	if res, err := b.Tag(ctx, vcs.Repo{}, "v1.0.0", first[:12], ""); err != nil || res != vcs.TagAlreadyPresent {
		t.Fatalf("retry by prefix = %v %v; want TagAlreadyPresent", res, err)
	}

	// The conflict: the same tag asked at another Change is refused, and the
	// tag is not moved.
	id, _ := s.Work(t, "mg-1", "p1", "next", map[string]string{"n.txt": "n\n"})
	s.Land(t, id)
	second := s.Tip(t)
	_, err = b.Tag(ctx, vcs.Repo{}, "v1.0.0", second, "")
	var re *vcs.RefusedError
	if err == nil || !errors.As(err, &re) || re.Code != "tag_exists" || re.Current != first || !strings.Contains(err.Error(), "refusing to move a published tag") {
		t.Fatalf("conflicting tag = %v; want tag_exists at %s, refused", err, first)
	}
	if got := showChange(t, s, "refs/tags/v1.0.0"); got != first {
		t.Fatalf("a conflicting tag moved refs/tags/v1.0.0 to %s", got)
	}

	// No Change, or one that does not exist: refused before anything moves.
	if _, err := b.Tag(ctx, vcs.Repo{}, "v2", "", ""); err == nil {
		t.Error("Tag with no Change succeeded")
	}
	if _, err := b.Tag(ctx, vcs.Repo{}, "v2", strings.Repeat("e", 64), ""); err == nil {
		t.Error("Tag of a Change that does not exist succeeded")
	}
}

func TestReleaseBranchAgainstEnthd(t *testing.T) {
	s := enthdtest.Start(t)
	s.RequireRef(t)
	b := Backend{Repo: s.EnthRepo(), Bin: s.Enth()}
	ctx := t.Context()

	cand := s.Tip(t)
	// Main moves on past the candidate.
	later, _ := s.Work(t, "mg-1", "p1", "unsoaked", map[string]string{"u.txt": "u\n"})
	s.Land(t, later)

	m, err := b.CreateBranch(ctx, "release/v1.0.0", cand)
	if err != nil || m.Ref != "refs/heads/release/v1.0.0" || m.Change != cand || m.Existed || m.Entry == 0 {
		t.Fatalf("CreateBranch = %+v %v; want refs/heads/release/v1.0.0 at the candidate %s", m, err, cand)
	}
	if tip, err := b.RefTip(ctx, "release/v1.0.0"); err != nil || tip != cand {
		t.Fatalf("release tip = %s %v; want the candidate", tip, err)
	}
	// The retry converges; a different value is refused and not re-carved.
	if m, err := b.CreateBranch(ctx, "refs/heads/release/v1.0.0", cand); err != nil || !m.Existed {
		t.Fatalf("retry = %+v %v; want Existed", m, err)
	}
	var re *vcs.RefusedError
	if err := b.CreateRef(ctx, vcs.Repo{}, "release/v1.0.0", "main"); err == nil || !errors.As(err, &re) || re.Code != "ref_exists" {
		t.Fatalf("re-carve from main = %v; want ref_exists", err)
	}
	if tip, _ := b.RefTip(ctx, "release/v1.0.0"); tip != cand {
		t.Fatalf("a refused re-carve moved the release branch to %s", tip)
	}
	// --auto-create-target's shape: no source is the trunk's tip.
	if m, err := b.CreateBranch(ctx, "integ", ""); err != nil || m.Change != s.Tip(t) {
		t.Fatalf("CreateBranch from the trunk = %+v %v; want the trunk tip", m, err)
	}
	// Neither the trunk nor a tag is a branch to create.
	if _, err := b.CreateBranch(ctx, "main", cand); err == nil {
		t.Error("CreateBranch of the trunk succeeded")
	}
	if _, err := b.CreateBranch(ctx, "refs/tags/v1", cand); err == nil {
		t.Error("CreateBranch of a tag succeeded")
	}

	// Landed onto: a worker's Change on the release branch moves it, and not
	// the trunk. That is self_land's `enth land --ref refs/heads/<branch>`.
	dir := filepath.Join(t.TempDir(), "rel")
	s.Checkout(t, "refs/heads/release/v1.0.0", dir)
	writeFile(t, dir, "VERSION", "1.0.0\n")
	bump := s.Change(t, dir, "chore: bump to 1.0.0\n\nPogo-Item: mg-rel\nPogo-Worker: prel\n")
	trunk := s.Tip(t)
	out, err := s.Try("enth", append(s.Remote(), "land", "--ref", b.LandRef("release/v1.0.0"), bump, "--request-id", "mg-rel/1")...)
	if err != nil {
		t.Fatalf("land on the release branch: %v\n%s", err, out)
	}
	rec, err := ParseLanded([]byte(out))
	if err != nil || rec.Ref != "refs/heads/release/v1.0.0" || rec.Old != cand {
		t.Fatalf("release land = %+v %v; want refs/heads/release/v1.0.0 from %s", rec, err, cand)
	}
	if s.Tip(t) != trunk {
		t.Fatal("a land on the release branch moved the trunk")
	}
	// And the release tag on what that land landed as.
	if res, err := b.Tag(ctx, vcs.Repo{}, "v1.0.0", rec.New, ""); err != nil || res != vcs.TagCreated {
		t.Fatalf("tag the release land = %v %v", res, err)
	}
	if got := showChange(t, s, "refs/tags/v1.0.0"); got != rec.New {
		t.Fatalf("v1.0.0 = %s; want the release land %s", got, rec.New)
	}
}

func showChange(t *testing.T, s *enthdtest.Server, rev string) string {
	t.Helper()
	b := Backend{Repo: s.EnthRepo(), Bin: s.Enth()}
	l, err := b.show(t.Context(), rev)
	if err != nil {
		t.Fatalf("enth query show %s: %v", rev, err)
	}
	return l.Change
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
