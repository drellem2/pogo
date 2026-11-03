package agent

import (
	"fmt"
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
	// An identity already in the environment: none is added.
	t.Setenv("ENTH_AUTHOR_NAME", "a")
	t.Setenv("ENTH_AUTHOR_EMAIL", "a@x")
	t.Setenv("ENTH_COMMITTER_NAME", "a")
	t.Setenv("ENTH_COMMITTER_EMAIL", "a@x")
	got := strings.Join(enthWorkerEnv(e, t.TempDir(), "p1"), " ")
	if got != "ENTH_REMOTE=https://enth.example ENTH_REPO=o/r ENTH_TRUNK=refs/heads/main" {
		t.Errorf("env without a token = %q", got)
	}
	t.Setenv("ENTH_TOKEN", "enth_test_value")
	env := enthWorkerEnv(e, t.TempDir(), "p1")
	if env[len(env)-1] != "ENTH_TOKEN=enth_test_value" {
		t.Errorf("pogod's ENTH_TOKEN is not passed through (last entry %q)", env[len(env)-1])
	}
}

// TestEnthWorkerEnvSuppliesAnIdentity is pm-pogo's carry-over 1 (mg-30da0):
// an environment with NO identity — the measured state of this fleet — gets
// git's, so the worker's `enth change` is not refused no_identity.
func TestEnthWorkerEnvSuppliesAnIdentity(t *testing.T) {
	for _, k := range []string{"ENTH_TOKEN", "ENTH_AUTHOR_NAME", "ENTH_AUTHOR_EMAIL", "ENTH_COMMITTER_NAME", "ENTH_COMMITTER_EMAIL",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	old := gitIdentity
	t.Cleanup(func() { gitIdentity = old })
	var asked string
	gitIdentity = func(dir string) (string, string) { asked = dir; return "Dan", "dan@example.com" }
	got := strings.Join(enthWorkerEnv(vcs.EnthRepo{Remote: "r", Repo: "o/r"}, "/src/proj", "p1"), " ")
	if !strings.HasSuffix(got, "ENTH_AUTHOR_NAME=Dan ENTH_AUTHOR_EMAIL=dan@example.com ENTH_COMMITTER_NAME=Dan ENTH_COMMITTER_EMAIL=dan@example.com") || asked != "/src/proj" {
		t.Errorf("env with no identity = %q (git asked in %q)", got, asked)
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

func TestResolveEnthAdoption(t *testing.T) {
	old := enthStacks
	t.Cleanup(func() { enthStacks = old })
	e := vcs.EnthRepo{Remote: "http://e", Repo: "o/r"}
	stack := vcs.Submission{Item: "mg-1", Worker: "p1", Ref: "c2", Revs: []string{"c1", "c2"},
		Changes: []vcs.Change{{ID: "c1"}, {ID: "c2", Base: "c1"}}}

	for _, tc := range []struct {
		name string
		ret  vcs.Submission
		err  error
		want string
	}{
		{"none", vcs.Submission{}, fmt.Errorf("x: %w", vcs.ErrNoWork), "nothing to adopt"},
		{"ambiguous", vcs.Submission{}, &vcs.AmbiguousWorkError{Item: "mg-1", Stacks: []vcs.Submission{{}, {}}}, "form 2 stacks"},
		{"unreachable", vcs.Submission{}, fmt.Errorf("connection refused"), "listing mg-1's Changes on http://e failed"},
	} {
		enthStacks = func(vcs.EnthRepo, string) (vcs.Submission, error) { return tc.ret, tc.err }
		if _, refusal := resolveEnthAdoption(e, "mg-1", "why"); !strings.Contains(refusal, tc.want) || !strings.Contains(refusal, "Nothing was dispatched") {
			t.Errorf("%s: refusal = %q, want %q", tc.name, refusal, tc.want)
		}
	}

	var asked string
	enthStacks = func(_ vcs.EnthRepo, item string) (vcs.Submission, error) { asked = item; return stack, nil }
	// No item: refused before any listing (an empty filter lists every Change).
	if _, refusal := resolveEnthAdoption(e, "", "why"); !strings.Contains(refusal, "needs the work item (--id)") || asked != "" {
		t.Errorf("no item: refusal %q, listed %q", refusal, asked)
	}
	a, refusal := resolveEnthAdoption(e, "mg-1", "the work is good")
	if refusal != "" || a.Stack.Ref != "c2" || asked != "mg-1" {
		t.Fatalf("one stack: %+v %q (asked %q)", a, refusal, asked)
	}
	p := enthAdoptPrelude("mg-1", a)
	for _, want := range []string{"based on c2, NOT on the trunk", "2 unlanded Change(s) by p1", "the work is good", "--trailer Pogo-Item=mg-1"} {
		if !strings.Contains(p, want) {
			t.Errorf("prelude lacks %q:\n%s", want, p)
		}
	}
	rec := a.record("refs/heads/main")
	if rec.Finding.Ref != "c2" || rec.BaseRef() != "c2" || rec.Finding.Target != "refs/heads/main" || rec.Reason != "the work is good" {
		t.Errorf("event record = %+v", rec)
	}
}
