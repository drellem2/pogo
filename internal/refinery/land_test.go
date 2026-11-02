package refinery

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// These tests pin the wiring between attemptMerge and the vcs backend (enth-vcs
// slice 1, mg-a18a3). The refactor moved the git steps into gitvcs.Lander and
// left the refinery to translate each failed step back into the stage, error
// text, step name and retry class it had before. A mutation run against the
// pre-existing suite found that five of those translations and hooks could be
// broken with every existing test still green — the entry discard, the
// gate-dirt classification hook, the ff-merge stage label, the invalid-upstream
// retry, and the push auth check — so they are pinned here.

func stepErr(step vcs.Step, out string, args ...string) *vcs.StepError {
	return &vcs.StepError{Step: step, Args: args, Output: out, Err: errors.New("exit status 1")}
}

func TestPrepareAndLandFailureTranslation(t *testing.T) {
	mr := &MergeRequest{ID: "mr-x", Branch: "feat", TargetRef: "main"}
	cases := []struct {
		name      string
		land      bool
		se        *vcs.StepError
		stage     string
		retryable bool
		gitStep   string
		msgPrefix string
	}{
		{"fetch", false, stepErr(vcs.StepFetch, "fatal: unable to access", "fetch", "origin"),
			"fetch", false, "fetch", "fetch: fatal: unable to access: exit status 1"},
		{"checkout-branch", false, stepErr(vcs.StepCheckoutBranch, "error: pathspec", "checkout", "-B", "feat", "origin/feat"),
			"fetch", false, "checkout-branch", "checkout branch: error: pathspec"},
		{"rebase-conflict", false, stepErr(vcs.StepRebase, "CONFLICT (content)", "rebase", "origin/main"),
			"rebase", false, "rebase", "rebase onto main: CONFLICT (content)"},
		{"rebase-invalid-upstream", false, stepErr(vcs.StepRebase, "fatal: invalid upstream 'origin/main'", "rebase", "origin/main"),
			"rebase", true, "rebase", "rebase onto main: fatal: invalid upstream"},
		{"fetch-target", true, stepErr(vcs.StepFetchTarget, "fatal: timeout", "fetch", "origin", "main"),
			"fetch", true, "fetch-target", "fetch target main: fatal: timeout"},
		{"reset-target", true, stepErr(vcs.StepResetTarget, "error: x", "checkout", "-B", "main", "origin/main"),
			"fetch", true, "reset-target", "reset target main to origin: error: x"},
		{"merge-ff-only", true, stepErr(vcs.StepMerge, "fatal: Not possible to fast-forward", "merge", "--ff-only", "feat"),
			"rebase", true, "merge-ff-only", "merge (ff-only): fatal: Not possible"},
		{"push-rejected", true, stepErr(vcs.StepPush, "! [rejected] main -> main (fetch first)", "push", "origin", "main"),
			"push", true, "push", "push: ! [rejected]"},
		{"push-auth", true, stepErr(vcs.StepPush, "fatal: could not read Username for 'https://github.com'", "push", "origin", "main"),
			"push", false, "push", "refinery push failed: git could not authenticate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stage string
			var err error
			if tc.land {
				stage, err = landFailure(mr, tc.se)
			} else {
				stage, err = prepareFailure(mr, tc.se)
			}
			if stage != tc.stage {
				t.Errorf("stage = %q, want %q", stage, tc.stage)
			}
			if isRetryable(err) != tc.retryable {
				t.Errorf("retryable = %v, want %v", isRetryable(err), tc.retryable)
			}
			var ge *gitStepError
			if !errors.As(err, &ge) {
				t.Fatalf("err %T is not a *gitStepError", err)
			}
			if ge.step != tc.gitStep {
				t.Errorf("gitStepError.step = %q, want %q", ge.step, tc.gitStep)
			}
			if ge.raw != tc.se.Output {
				t.Errorf("gitStepError.raw = %q, want git's verbatim output %q", ge.raw, tc.se.Output)
			}
			if want := "git " + strings.Join(tc.se.Args, " "); ge.cmd != want {
				t.Errorf("gitStepError.cmd = %q, want %q", ge.cmd, want)
			}
			if !strings.HasPrefix(err.Error(), tc.msgPrefix) {
				t.Errorf("message = %q, want prefix %q", err.Error(), tc.msgPrefix)
			}
		})
	}
}

// A hook's classification (gate dirt) replaces the backend's error, with the
// stage each step always reported for it.
func TestPrepareAndLandFailureOverrideStages(t *testing.T) {
	mr := &MergeRequest{ID: "mr-x", Branch: "feat", TargetRef: "main"}
	override := &gateDirtError{Stage: "x"}
	cases := []struct {
		step  vcs.Step
		land  bool
		stage string
	}{
		{vcs.StepCheckoutBranch, false, "fetch"},
		{vcs.StepRebase, false, "rebase"},
		{vcs.StepResetTarget, true, "fetch"},
		{vcs.StepMerge, true, "merge"},
	}
	for _, tc := range cases {
		se := stepErr(tc.step, "error: Your local changes would be overwritten")
		se.Override = override
		var stage string
		var err error
		if tc.land {
			stage, err = landFailure(mr, se)
		} else {
			stage, err = prepareFailure(mr, se)
		}
		if stage != tc.stage || err != error(override) {
			t.Errorf("%s: (stage, err) = (%q, %v), want (%q, the override unchanged)", tc.step, stage, err, tc.stage)
		}
	}
}

func dirtyRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	writeFile(t, dir, "records/probe.json", "{}\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "initial")
	writeFile(t, dir, "records/probe.json", "{\"run\": 2}\n")
	return dir
}

// The attempt-entry discard runs before the branch checkout, so a previous
// gate's writes cannot make the checkout refuse (gatedirt.go).
func TestLandHooksDiscardGateWritesBeforeCheckout(t *testing.T) {
	dir := dirtyRepo(t)
	r := &Refinery{}
	mr := &MergeRequest{ID: "mr-x", Branch: "feat", TargetRef: "main"}
	hooks := r.landMR(dir, mr, 1).Hooks

	hooks.BeforeStep(vcs.StepFetch)
	if left, _ := dirtyTrackedPaths(dir); len(left) == 0 {
		t.Fatal("discard ran before the fetch step; it belongs after the fetch, before the checkout")
	}
	hooks.BeforeStep(vcs.StepCheckoutBranch)
	if left, err := dirtyTrackedPaths(dir); err != nil || len(left) != 0 {
		t.Errorf("tree still dirty when checkout-branch starts: %v (err %v)", left, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "records/probe.json")); string(got) != "{}\n" {
		t.Errorf("probe.json = %q, want the committed content", got)
	}
}

// The StepFailed hook classifies gate dirt for exactly the four steps that
// always did, and never for the network steps.
func TestLandHooksClassifyGateDirt(t *testing.T) {
	dir := dirtyRepo(t)
	r := &Refinery{}
	mr := &MergeRequest{ID: "mr-x", Branch: "feat", TargetRef: "main"}
	hooks := r.landMR(dir, mr, 1).Hooks
	out := "error: cannot rebase: You have unstaged changes."

	for _, s := range []vcs.Step{vcs.StepCheckoutBranch, vcs.StepRebase, vcs.StepResetTarget, vcs.StepMerge} {
		err := hooks.StepFailed(s, out)
		var d *gateDirtError
		if !errors.As(err, &d) {
			t.Errorf("%s: StepFailed = %v, want a *gateDirtError", s, err)
			continue
		}
		if !strings.Contains(d.Error(), "records/probe.json") {
			t.Errorf("%s: gate-dirt message lost the path:\n%s", s, d.Error())
		}
	}
	for _, s := range []vcs.Step{vcs.StepFetch, vcs.StepFetchTarget, vcs.StepPush} {
		if err := hooks.StepFailed(s, out); err != nil {
			t.Errorf("%s: StepFailed = %v, want nil (network steps were never classified as gate dirt)", s, err)
		}
	}

	// A clean tree yields an untyped nil, not a typed-nil *gateDirtError that
	// would read as an override.
	clean := t.TempDir()
	run(t, clean, "git", "init", "-b", "main")
	if err := r.landMR(clean, mr, 1).Hooks.StepFailed(vcs.StepRebase, out); err != nil {
		t.Errorf("clean tree: StepFailed = %#v, want untyped nil", err)
	}
}
