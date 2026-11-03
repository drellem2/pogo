package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
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
		// Slice 12 (mg-ae4ac): a release branch is landed onto, not PR flow.
		{name: "release branch", tmpl: "polecat", branch: "release/v1.2.0"},
		{name: "release branch, architect", tmpl: "polecat-architect", branch: "release/v1.2.0"},
		{name: "release branch as a full ref", tmpl: "polecat", branch: "refs/heads/release/v1.2.0", want: "pass --branch release/v1.2.0"},
		{name: "release prefix with no name", tmpl: "polecat", branch: "release/", want: "not this enth repo's trunk"},
		{name: "release-ish feature branch", tmpl: "polecat", branch: "releases/x", want: "not this enth repo's trunk"},
		{name: "feature branch under release", tmpl: "polecat", branch: "feature/release/x", want: "not this enth repo's trunk"},
		{name: "build-pr onto a release branch", tmpl: "polecat-build-pr", branch: "release/v1.2.0", want: "has no PR flow"},
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

// TestSpawnAcceptsAReleaseBranchOnAnEnthRepo drives the real spawn path
// against a sandboxed enthd (slice 12, mg-ae4ac): a --branch naming a
// release branch made with `enth ref create` is dispatched, and its
// checkout stands on the release branch's tip, not the trunk's. A feature
// branch on the same repo is still the slice-9 409. The git control: the
// same release-branch spawn on a git repo is dispatched as before.
func TestSpawnAcceptsAReleaseBranchOnAnEnthRepo(t *testing.T) {
	s := enthdtest.Start(t)
	s.RequireRef(t)
	// The spawn's checkout runs the enth on PATH.
	t.Setenv("PATH", s.BinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	src := filepath.Join(t.TempDir(), "src")
	s.Checkout(t, vcs.DefaultEnthTrunk, src)
	cand := s.Tip(t)
	s.Cmd(t, "enth", append(s.Remote(), "ref", "create", "refs/heads/release/v1.0.0", cand)...)
	later, _ := s.Work(t, "mg-12a00", "p0", "unsoaked", map[string]string{"u.txt": "u\n"})
	s.Land(t, later)
	writeTemplate(t, "relvcs", "+++\nworktree = true\n+++\n{{if eq .VCS \"enth\"}}enth land --ref refs/heads/{{.Branch}}{{else}}git {{.Branch}}{{end}}\n")
	polecats, _ := gitgc.DefaultPolecatsDir()

	t.Run("enth", func(t *testing.T) {
		useEnthRecord(t, src, s.EnthRepo())
		reg := adoptRegistry(t)
		rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: "e12a01", Id: "mg-12a01", Repo: src, Template: "relvcs", Branch: "release/v1.0.0"})
		if rr.Code != http.StatusCreated {
			t.Fatalf("release-branch spawn on enth = %d, want 201: %s", rr.Code, rr.Body.String())
		}
		dir := filepath.Join(polecats, "e12a01")
		t.Cleanup(func() { os.RemoveAll(dir) })
		st, err := enthvcs.ReadState(dir)
		if err != nil || st.Base != cand {
			t.Fatalf("the release worker's checkout base = %q (%v); want the release branch's tip %s, not the trunk's", st.Base, err, cand)
		}

		rr = spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: "e12a02", Id: "mg-12a02", Repo: src, Template: "relvcs", Branch: "feature-x"})
		if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "not this enth repo's trunk") {
			t.Fatalf("feature-branch spawn on enth = %d %s; want the slice-9 409", rr.Code, rr.Body.String())
		}
		if _, err := os.Stat(filepath.Join(polecats, "e12a02")); !os.IsNotExist(err) {
			t.Errorf("a refused spawn left a workspace behind (%v)", err)
		}
	})

	t.Run("git control", func(t *testing.T) {
		repo := strandedRepo(t)
		pushBranch(t, repo, "release/v1.0.0", "VERSION", "chore: release branch")
		reg := adoptRegistry(t)
		for _, b := range []string{"release/v1.0.0", "integ"} {
			if b == "integ" {
				pushBranch(t, repo, "integ", "integ.md", "feat: integration-branch work")
			}
			name := "g12" + strings.ReplaceAll(strings.ReplaceAll(b, "/", ""), ".", "")
			rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: name, Id: "mg-" + name, Repo: repo, Template: adoptTemplate(t), Branch: b})
			if rr.Code != http.StatusCreated {
				t.Errorf("--branch %s on a git repo = %d, want 201: %s", b, rr.Code, rr.Body.String())
			}
		}
	})
}
