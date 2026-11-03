package vcs

import (
	"strings"
	"testing"
)

func TestNormalizeSelectionWorkspacePaths(t *testing.T) {
	_, e, err := NormalizeSelection(Enth, &EnthRepo{Remote: "https://e", Repo: "o/r",
		WorkspacePaths: []string{"src/", " docs ", "src", ""}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(e.WorkspacePaths, ",") != "src,docs" {
		t.Errorf("paths = %q, want cleaned, deduplicated, in order", e.WorkspacePaths)
	}
	for _, bad := range []string{"/abs", "..", "../x", "."} {
		if _, _, err := NormalizeSelection(Enth, &EnthRepo{Remote: "https://e", Repo: "o/r", WorkspacePaths: []string{bad}}); err == nil {
			t.Errorf("workspace path %q accepted", bad)
		}
	}
	_, e, _ = NormalizeSelection(Enth, &EnthRepo{Remote: "https://e", Repo: "o/r", WorkspacePaths: []string{}})
	if e.WorkspacePaths != nil {
		t.Errorf("an empty list must be stored as no key (nil), got %#v", e.WorkspacePaths)
	}
	if _, _, err := NormalizeSelection(Git, &EnthRepo{WorkspacePaths: []string{"src"}}); err == nil {
		t.Error("enth workspace paths accepted for a git repo")
	}
	if !(EnthRepo{}).IsZero() || (EnthRepo{WorkspacePaths: []string{"x"}}).IsZero() {
		t.Error("IsZero")
	}
	if !(EnthRepo{Repo: "a"}).Equal(EnthRepo{Repo: "a", WorkspacePaths: []string{}}) {
		t.Error("nil and empty paths must be equal")
	}
}

// The git backend never reports an undetermined Behind on an update, so its
// rendering is unchanged; enth's Refresh does, and must not print "-1".
func TestRefreshUpdatedRendersUnknownBehind(t *testing.T) {
	git := RefreshResult{Path: "/p", Status: RefreshUpdated, Branch: "main", Upstream: "origin/main", Behind: 3, From: "a", To: "b"}
	if got := git.String(); got != "/p: fast-forwarded main 3 commit(s) to origin/main (a..b)" {
		t.Errorf("git rendering changed: %q", got)
	}
	enth := git
	enth.Behind = -1
	if got := enth.String(); strings.Contains(got, "-1") {
		t.Errorf("enth rendering = %q", got)
	}
}

func ids(st []Change) string {
	var s []string
	for _, c := range st {
		s = append(s, c.ID)
	}
	return strings.Join(s, ">")
}

func TestStacks(t *testing.T) {
	cases := []struct {
		name string
		in   []Change
		want []string
	}{
		{"none", nil, nil},
		{"one change", []Change{{ID: "a", Base: "T"}}, []string{"a"}},
		{"a stack of three, listed oldest first", []Change{{ID: "a", Base: "T"}, {ID: "b", Base: "a"}, {ID: "c", Base: "b"}}, []string{"a>b>c"}},
		{"a stack listed out of order", []Change{{ID: "c", Base: "b"}, {ID: "a", Base: "T"}, {ID: "b", Base: "a"}}, []string{"a>b>c"}},
		{"two workers, two stacks", []Change{{ID: "a", Base: "T"}, {ID: "x", Base: "T"}, {ID: "b", Base: "a"}}, []string{"a>b", "x"}},
		{"a fork is two stacks sharing a prefix", []Change{{ID: "a", Base: "T"}, {ID: "b", Base: "a"}, {ID: "c", Base: "a"}}, []string{"a>b", "a>c"}},
		{"a gap (middle Change missing) is two stacks", []Change{{ID: "a", Base: "T"}, {ID: "c", Base: "b"}}, []string{"a", "c"}},
		{"a duplicate line is one Change", []Change{{ID: "a", Base: "T"}, {ID: "a", Base: "T"}}, []string{"a"}},
		{"a root Change (no base)", []Change{{ID: "r"}}, []string{"r"}},
	}
	for _, c := range cases {
		var got []string
		for _, st := range Stacks(c.in) {
			got = append(got, ids(st))
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: Stacks = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestChangeTrailerIsCaseInsensitive(t *testing.T) {
	c := Change{Trailers: []Trailer{{Key: "pogo-item", Value: "mg-1"}, {Key: "Pogo-Item", Value: "mg-2"}}}
	if c.Trailer(TrailerItem) != "mg-1" || c.Trailer(TrailerWorker) != "" {
		t.Errorf("Trailer = %q/%q", c.Trailer(TrailerItem), c.Trailer(TrailerWorker))
	}
}

func TestAmbiguousWorkErrorNamesEachStacksWorker(t *testing.T) {
	e := &AmbiguousWorkError{Item: "mg-1", Stacks: []Submission{
		{Worker: "p1", Changes: []Change{{ID: "a"}, {ID: "b"}}},
		{Changes: []Change{{ID: "x"}}},
	}}
	msg := e.Error()
	for _, want := range []string{"mg-1", "2 stacks", "worker p1, 2 Change(s), top b", "worker (no Pogo-Worker trailer), 1 Change(s), top x"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}
