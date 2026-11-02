package gitvcs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture builds a bare origin with main, a feature branch pushed to it, and a
// separate clone (the lander's working dir). main is advanced after the branch
// forks so Prepare has a real rebase to do. conflict makes the branch and main
// edit the same line.
func fixture(t *testing.T, conflict bool) (origin, clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	git(t, root, "init", "--bare", "-b", "main", origin)

	author := filepath.Join(root, "author")
	git(t, root, "clone", origin, author)
	git(t, author, "checkout", "-B", "main")
	write(t, author, "f", "base\n")
	git(t, author, "add", "f")
	git(t, author, "commit", "-m", "base")
	git(t, author, "push", "origin", "main")

	git(t, author, "checkout", "-b", "feature")
	if conflict {
		write(t, author, "f", "feature\n")
	} else {
		write(t, author, "g", "feature\n")
	}
	git(t, author, "add", ".")
	git(t, author, "commit", "-m", "feature work")
	git(t, author, "push", "origin", "feature")

	git(t, author, "checkout", "main")
	write(t, author, "f", "main moved\n")
	git(t, author, "commit", "-am", "main moves")
	git(t, author, "push", "origin", "main")

	clone = filepath.Join(root, "clone")
	git(t, root, "clone", "--no-local", origin, clone)
	return origin, clone
}

func testMR(clone string, h vcs.LandHooks) vcs.MR {
	return vcs.MR{ID: "mr-test", Dir: clone, Branch: "feature", Target: "main", Hooks: h}
}

func TestLanderPrepareAndLand(t *testing.T) {
	origin, clone := fixture(t, false)
	l := Lander{Runner: Runner{LogPrefix: "test"}}

	var steps []vcs.Step
	mr := testMR(clone, vcs.LandHooks{BeforeStep: func(s vcs.Step) { steps = append(steps, s) }})
	p, err := l.Prepare(context.Background(), mr)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if p.GateDir != clone {
		t.Errorf("GateDir = %q, want %q", p.GateDir, clone)
	}
	landed, err := l.Land(context.Background(), p)
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	want := []vcs.Step{vcs.StepFetch, vcs.StepCheckoutBranch, vcs.StepRebase,
		vcs.StepFetchTarget, vcs.StepResetTarget, vcs.StepMerge, vcs.StepPush}
	if strings.Join(stepNames(steps), ",") != strings.Join(stepNames(want), ",") {
		t.Errorf("steps = %v, want %v", steps, want)
	}
	tip := git(t, origin, "rev-parse", "refs/heads/main")
	if landed.SHA != tip {
		t.Errorf("Landed.SHA = %q, origin main = %q", landed.SHA, tip)
	}
	// The landed tip carries both the branch's file and main's move.
	if got := git(t, origin, "show", "main:g"); got != "feature" {
		t.Errorf("main:g = %q", got)
	}
	if got := git(t, origin, "show", "main:f"); got != "main moved" {
		t.Errorf("main:f = %q", got)
	}

	if err := l.Cleanup(context.Background(), mr); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if out := git(t, origin, "branch", "--list", "feature"); out != "" {
		t.Errorf("feature still on origin after Cleanup: %q", out)
	}
}

func stepNames(s []vcs.Step) []string {
	var out []string
	for _, x := range s {
		out = append(out, string(x))
	}
	return out
}

// The StepFailed hook must see the failed rebase BEFORE the backend aborts it:
// the refinery's gate-dirt classifier reads the in-progress state (mg-393f).
func TestLanderRebaseConflictHookRunsBeforeAbort(t *testing.T) {
	_, clone := fixture(t, true)
	l := Lander{Runner: Runner{LogPrefix: "test"}}

	sentinel := errors.New("classified")
	var sawRebaseInProgress bool
	mr := testMR(clone, vcs.LandHooks{StepFailed: func(s vcs.Step, out string) error {
		if s != vcs.StepRebase {
			t.Errorf("StepFailed for %s, want rebase", s)
		}
		gitDir := filepath.Join(clone, ".git")
		for _, d := range []string{"rebase-merge", "rebase-apply"} {
			if _, err := os.Stat(filepath.Join(gitDir, d)); err == nil {
				sawRebaseInProgress = true
			}
		}
		return sentinel
	}})
	_, err := l.Prepare(context.Background(), mr)
	var se *vcs.StepError
	if !errors.As(err, &se) {
		t.Fatalf("Prepare err = %v, want *vcs.StepError", err)
	}
	if se.Step != vcs.StepRebase || se.Override != sentinel {
		t.Errorf("StepError = {Step:%s Override:%v}, want rebase/sentinel", se.Step, se.Override)
	}
	if strings.Join(se.Args, " ") != "rebase origin/main" {
		t.Errorf("Args = %v", se.Args)
	}
	if !sawRebaseInProgress {
		t.Error("hook ran after the rebase was aborted; it must see the in-progress state")
	}
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(clone, ".git", d)); err == nil {
			t.Errorf("rebase left in progress (%s) after Prepare returned", d)
		}
	}
}

func TestLanderFetchFailureIsAStepError(t *testing.T) {
	dir := t.TempDir()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	git(t, dir, "init", "-b", "main", ".")
	l := Lander{Runner: Runner{LogPrefix: "test"}}
	_, err := l.Prepare(context.Background(), testMR(dir, vcs.LandHooks{}))
	var se *vcs.StepError
	if !errors.As(err, &se) || se.Step != vcs.StepFetch || se.Output == "" {
		t.Fatalf("err = %#v, want a fetch StepError carrying git's output", err)
	}
}

func TestRefsTagIsIdempotentAndNeverMoves(t *testing.T) {
	_, clone := fixture(t, false)
	refs := Refs{Runner: Runner{LogPrefix: "test"}}
	repo := vcs.Repo{Path: clone}
	head := git(t, clone, "rev-parse", "origin/main")
	base := git(t, clone, "rev-parse", "origin/main~1")

	res, err := refs.Tag(context.Background(), repo, "v1.0.0", head, "v1.0.0\n\nrelease")
	if err != nil || res != vcs.TagCreated {
		t.Fatalf("first Tag = %v, %v; want TagCreated", res, err)
	}
	res, err = refs.Tag(context.Background(), repo, "v1.0.0", head, "v1.0.0\n\nrelease")
	if err != nil || res != vcs.TagAlreadyPresent {
		t.Fatalf("repeat Tag = %v, %v; want TagAlreadyPresent", res, err)
	}
	if _, err = refs.Tag(context.Background(), repo, "v1.0.0", base, "x"); err == nil ||
		!strings.Contains(err.Error(), "refusing to move a published tag") {
		t.Fatalf("Tag at another commit err = %v, want refusal", err)
	}
}

func TestRefsDefaultAndCreateRefOnBareRepo(t *testing.T) {
	origin, _ := fixture(t, false)
	refs := Refs{}
	got, err := refs.Default(context.Background(), vcs.Repo{Path: origin})
	if err != nil || got != "main" {
		t.Fatalf("Default = %q, %v; want main", got, err)
	}
	if err := refs.CreateRef(context.Background(), vcs.Repo{Path: origin}, "release/1", "main"); err != nil {
		t.Fatalf("CreateRef: %v", err)
	}
	if a, b := git(t, origin, "rev-parse", "refs/heads/release/1"), git(t, origin, "rev-parse", "refs/heads/main"); a != b {
		t.Errorf("release/1 = %s, main = %s", a, b)
	}
}

func TestRunSuppliesIdentityAndDemotesBenignOutcome(t *testing.T) {
	if _, ok := BenignOutcome("fatal: no rebase in progress"); !ok {
		t.Error("the measured benign outcome is not recognised")
	}
	if _, ok := BenignOutcome("fatal: Unable to create '.git/index.lock': File exists."); ok {
		t.Error("an unmeasured failure was demoted")
	}
	for _, kv := range IdentityEnv() {
		k, v, _ := strings.Cut(kv, "=")
		if os.Getenv(k) != "" {
			t.Errorf("IdentityEnv overrides a set %s", k)
		}
		if v != CommitterName && v != CommitterEmail {
			t.Errorf("IdentityEnv %s = %q", k, v)
		}
	}
}
