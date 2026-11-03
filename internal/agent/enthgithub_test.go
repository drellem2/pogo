package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/vcs"
)

// enth-vcs slice 9 (mg-2e9fc): the GitHub boundary for an enth repo. Every
// test here has a git control beside it: the same request on a git repo is
// exactly what it was.

var sliceNineEnth = vcs.EnthRepo{Remote: "https://enth.example", Repo: "o/r"}

func TestEnthPRFlowRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, tmpl, branch, want string
		trunk                    string
	}{
		{name: "build-pr template", tmpl: "polecat-build-pr", want: "has no PR flow"},
		{name: "build-pr template by path", tmpl: "/x/templates/polecat-build-pr.md", want: "has no PR flow"},
		{name: "integration branch", tmpl: "polecat", branch: "integ", want: "targets refs/heads/integ, which is not this enth repo's trunk refs/heads/main"},
		{name: "integration ref", tmpl: "polecat", branch: "refs/heads/release-1", want: "not this enth repo's trunk"},
		{name: "main against a custom trunk", tmpl: "polecat", branch: "main", trunk: "refs/heads/trunk", want: "trunk refs/heads/trunk"},
		{name: "ordinary build", tmpl: "polecat"},
		{name: "architect", tmpl: "polecat-architect"},
		{name: "branch is the trunk by name", tmpl: "polecat", branch: "main"},
		{name: "branch is the trunk by ref", tmpl: "polecat", branch: "refs/heads/main"},
		{name: "custom trunk by name", tmpl: "polecat", branch: "trunk", trunk: "refs/heads/trunk"},
	} {
		e := sliceNineEnth
		e.Trunk = tc.trunk
		got := enthPRFlowRefusal(e, tc.tmpl, tc.branch)
		if tc.want == "" {
			if got != "" {
				t.Errorf("%s: refused: %s", tc.name, got)
			}
			continue
		}
		if !strings.Contains(got, tc.want) || !strings.Contains(got, "Nothing was dispatched") {
			t.Errorf("%s: refusal = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// useEnthRepo makes the project record say repo is enth for one test.
func useEnthRepo(t *testing.T, repo string) {
	t.Helper()
	SetRepoVCS(func(p string) (vcs.Kind, *vcs.EnthRepo) {
		if p == repo {
			e := sliceNineEnth
			return vcs.Enth, &e
		}
		return vcs.Git, nil
	})
	t.Cleanup(func() { SetRepoVCS(nil) })
}

// TestSpawnRefusesPRFlowOnAnEnthRepo drives the real spawn path: both PR-flow
// shapes are a 409 on an enth repo with nothing created, and the identical
// requests on the same repo as git are dispatched (the control).
func TestSpawnRefusesPRFlowOnAnEnthRepo(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "integ", "integ.md", "feat: integration-branch work")
	writeTemplate(t, "polecat-build-pr", "+++\nworktree = true\n+++\n# PR-flow build for {{.Id}}\n")
	polecats, _ := gitgc.DefaultPolecatsDir()

	cases := []struct {
		name string
		req  SpawnPolecatAPIRequest
		want string
	}{
		{"build-pr", SpawnPolecatAPIRequest{Name: "e9a01", Id: "mg-9a01", Repo: repo, Template: "polecat-build-pr"}, "has no PR flow"},
		{"integration branch", SpawnPolecatAPIRequest{Name: "e9a02", Id: "mg-9a02", Repo: repo, Template: adoptTemplate(t), Branch: "integ"}, "not this enth repo's trunk"},
	}

	t.Run("enth refuses", func(t *testing.T) {
		useEnthRepo(t, repo)
		reg := adoptRegistry(t)
		for _, tc := range cases {
			rr := spawnPolecat(t, reg, tc.req)
			if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), tc.want) {
				t.Errorf("%s: status %d, body %s; want 409 %q", tc.name, rr.Code, rr.Body.String(), tc.want)
			}
			if reg.Get(tc.req.Name) != nil {
				t.Errorf("%s: a refused spawn left an agent behind", tc.name)
			}
			if _, err := os.Stat(filepath.Join(polecats, tc.req.Name)); !os.IsNotExist(err) {
				t.Errorf("%s: a refused spawn left a workspace behind (%v)", tc.name, err)
			}
		}
	})

	t.Run("git control", func(t *testing.T) {
		reg := adoptRegistry(t)
		for _, tc := range cases {
			rr := spawnPolecat(t, reg, tc.req)
			if rr.Code != http.StatusCreated {
				t.Errorf("%s: the same spawn on a git repo = %d, want 201: %s", tc.name, rr.Code, rr.Body.String())
			}
		}
	})
}
