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
