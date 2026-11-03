package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

func TestEnthWorkerEnv(t *testing.T) {
	e := vcs.EnthRepo{Remote: "https://enth.example", Repo: "o/r", Trunk: "refs/heads/main"}
	t.Setenv("ENTH_TOKEN", "")
	got := strings.Join(enthWorkerEnv(e), " ")
	if got != "ENTH_REMOTE=https://enth.example ENTH_REPO=o/r ENTH_TRUNK=refs/heads/main" {
		t.Errorf("env without a token = %q", got)
	}
	t.Setenv("ENTH_TOKEN", "enth_test_value")
	env := enthWorkerEnv(e)
	if env[len(env)-1] != "ENTH_TOKEN=enth_test_value" {
		t.Errorf("pogod's ENTH_TOKEN is not passed through (last entry %q)", env[len(env)-1])
	}
}

func TestEnthSelectionPrelude(t *testing.T) {
	if p := enthSelectionPrelude(vcs.EnthRepo{}, "/w"); p != "" {
		t.Errorf("whole-repo workspace got a prelude: %q", p)
	}
	p := enthSelectionPrelude(vcs.EnthRepo{WorkspacePaths: []string{"src", "docs"}}, "/w")
	for _, want := range []string{"`src`, `docs`", "outside_selection", "enth checkout --add <PATH> /w"} {
		if !strings.Contains(p, want) {
			t.Errorf("prelude lacks %q:\n%s", want, p)
		}
	}
}

func TestEnthRepoForFollowsTheProjectRecord(t *testing.T) {
	t.Cleanup(func() { SetRepoVCS(nil) })
	if enthRepoFor("/r") != nil {
		t.Fatal("no lookup installed: must be git")
	}
	SetRepoVCS(func(p string) (vcs.Kind, *vcs.EnthRepo) {
		if p == "/enth" {
			return vcs.Enth, &vcs.EnthRepo{Remote: "https://e", Repo: "o/r"}
		}
		return vcs.Git, nil
	})
	if enthRepoFor("/git") != nil {
		t.Error("a git repo was routed to enth")
	}
	if e := enthRepoFor("/enth"); e == nil || e.Repo != "o/r" {
		t.Errorf("enth repo = %+v", e)
	}
}

// The persona exclude follows the tree's marker: on an enth checkout it is
// written to .enth/info/exclude and .git is never consulted.
func TestPersonaExcludeOnAnEnthCheckout(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".enth"), 0o755)
	os.WriteFile(filepath.Join(dir, enthvcs.StateRelPath),
		[]byte(`{"format":"enth-checkout","version":1,"base":"b","origin":{"remote":"https://e","repo":"o/r"},"paths":[]}`+"\n"), 0o644)
	if err := ensureWorktreeGitExcluded(dir, "AGENTS.override.md"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, enthvcs.ExcludeRelPath))
	if err != nil || !strings.Contains(string(data), "/AGENTS.override.md") {
		t.Errorf("exclude = %q, %v", data, err)
	}
	if ws, err := workspacesAt(dir); err != nil {
		t.Errorf("workspacesAt(enth checkout) = %v", err)
	} else if _, ok := ws.(enthvcs.Backend); !ok {
		t.Errorf("workspacesAt(enth checkout) = %T %v", ws, err)
	}
	// Both markers: refused, nothing written to either.
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	if err := ensureWorktreeGitExcluded(dir, "other.md"); err == nil {
		t.Error("a tree holding both .git and .enth/checkout was not refused")
	}
}
