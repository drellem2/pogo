package gitvcs

import (
	"context"

	"github.com/drellem2/pogo/internal/vcs"
)

// Lander is the git land pipeline: fetch, check out the submitted branch,
// rebase it onto the target (Prepare); fetch the target again, reset the local
// target to origin, fast-forward it to the branch, push (Land).
//
// It runs in the MR's Dir, which for the refinery is its private clone of the
// repo. The ctx is accepted for the interface and not yet honoured: no git
// step was cancellable before this backend existed, and slice 1 changes no
// behaviour.
//
// Every failure is a *vcs.StepError carrying git's verbatim output, so the
// caller classifies it from the same text it always has.
type Lander struct {
	Runner Runner
}

var _ vcs.Lander = Lander{}

func before(mr vcs.MR, s vcs.Step) {
	if mr.Hooks.BeforeStep != nil {
		mr.Hooks.BeforeStep(s)
	}
}

// fail builds the StepError for a failed step and gives the caller's
// StepFailed hook its look at the tree. It must be called BEFORE any cleanup
// of the failure (rebase --abort), because the cleanup erases what the hook
// inspects (mg-393f).
func fail(mr vcs.MR, s vcs.Step, args []string, out string, err error) *vcs.StepError {
	se := &vcs.StepError{Step: s, Args: args, Output: out, Err: err}
	if mr.Hooks.StepFailed != nil {
		se.Override = mr.Hooks.StepFailed(s, out)
	}
	return se
}

func (l Lander) step(mr vcs.MR, s vcs.Step, args ...string) (string, *vcs.StepError) {
	before(mr, s)
	out, err := l.Runner.Run(mr.Dir, args...)
	if err != nil {
		return out, fail(mr, s, args, out, err)
	}
	return out, nil
}

// Prepare fetches origin, checks out the branch fresh from origin/<branch>, and
// rebases it onto origin/<target>. A failed rebase is aborted, after the
// StepFailed hook has seen the tree, so the clone is left clean.
func (l Lander) Prepare(ctx context.Context, mr vcs.MR) (vcs.Prepared, error) {
	if _, se := l.step(mr, vcs.StepFetch, "fetch", "origin"); se != nil {
		return vcs.Prepared{}, se
	}
	if _, se := l.step(mr, vcs.StepCheckoutBranch, "checkout", "-B", mr.Branch, "origin/"+mr.Branch); se != nil {
		return vcs.Prepared{}, se
	}
	if _, se := l.step(mr, vcs.StepRebase, "rebase", "origin/"+mr.Target); se != nil {
		// Abort the failed rebase to leave the clone in a clean state.
		l.Runner.Run(mr.Dir, "rebase", "--abort")
		return vcs.Prepared{}, se
	}
	return vcs.Prepared{MR: mr, GateDir: mr.Dir}, nil
}

// Land fetches the target (origin may have advanced while the caller gated),
// realigns the local target to origin/<target> with `checkout -B` so a local
// target left ahead by an earlier failed push self-heals (mg-f1db),
// fast-forwards it to the prepared branch, and pushes it.
//
// Landed.SHA is `rev-parse HEAD` read best-effort after the push: the merge has
// already landed, so a failure there does not fail Land, and SHA then holds
// whatever rev-parse printed (the refinery's behaviour before this backend).
func (l Lander) Land(ctx context.Context, p vcs.Prepared) (vcs.Landed, error) {
	mr := p.MR
	if _, se := l.step(mr, vcs.StepFetchTarget, "fetch", "origin", mr.Target); se != nil {
		return vcs.Landed{}, se
	}
	if _, se := l.step(mr, vcs.StepResetTarget, "checkout", "-B", mr.Target, "origin/"+mr.Target); se != nil {
		return vcs.Landed{}, se
	}
	if _, se := l.step(mr, vcs.StepMerge, "merge", "--ff-only", mr.Branch); se != nil {
		return vcs.Landed{}, se
	}
	if _, se := l.step(mr, vcs.StepPush, "push", "origin", mr.Target); se != nil {
		return vcs.Landed{}, se
	}
	sha, _ := l.Runner.Run(mr.Dir, "rev-parse", "HEAD")
	return vcs.Landed{SHA: sha}, nil
}

// Cleanup deletes the submitted branch on origin (R20).
func (l Lander) Cleanup(ctx context.Context, mr vcs.MR) error {
	if _, se := l.step(mr, vcs.StepBranchDelete, "push", "origin", "--delete", mr.Branch); se != nil {
		return se
	}
	return nil
}
