package refinery

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/drellem2/pogo/internal/vcs"
)

// landMR describes mr to the backend, with the hooks that put the refinery's
// own work between the backend's steps:
//
//   - each step's "refinery: MR <id> step=..." log line, printed before it runs;
//   - the attempt-entry discard of gate-written files, after the fetch and
//     before the branch checkout (gatedirt.go);
//   - the gate-dirt classification of a failed checkout, rebase or ff-merge,
//     run BEFORE the backend aborts a failed rebase, because the abort's own
//     reset erases the evidence the message is built from (mg-393f).
func (r *Refinery) landMR(wtDir string, mr *MergeRequest, attempt int) vcs.MR {
	return vcs.MR{
		ID:     mr.ID,
		Repo:   vcs.Repo{Path: mr.RepoPath},
		Dir:    wtDir,
		Branch: mr.Branch,
		Target: mr.TargetRef,
		Hooks: vcs.LandHooks{
			BeforeStep: func(s vcs.Step) {
				switch s {
				case vcs.StepFetch:
					log.Printf("refinery: MR %s step=fetch branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
				case vcs.StepCheckoutBranch:
					// Clear anything a previous attempt's gate (or a previous
					// MR's, this clone is reused) left modified in the tracked
					// tree. Without this the checkout and rebase fail on writes
					// the author never made — see gatedirt.go.
					r.discardGateSideEffectsAt(wtDir, mr, attempt, "attempt-entry")
					log.Printf("refinery: MR %s step=checkout-branch branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
				case vcs.StepRebase:
					log.Printf("refinery: MR %s step=rebase target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
				case vcs.StepFetchTarget:
					log.Printf("refinery: MR %s step=fetch-target target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
				case vcs.StepResetTarget:
					log.Printf("refinery: MR %s step=reset-target target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
				case vcs.StepMerge:
					log.Printf("refinery: MR %s step=merge branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
				case vcs.StepPush:
					log.Printf("refinery: MR %s step=push target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
				}
			},
			StepFailed: func(s vcs.Step, out string) error {
				// A dirty tree is the one failure here that is never the
				// author's doing: this clone is private to the refinery, so
				// whatever is modified in it was written by a gate. Say that,
				// instead of relaying git's "commit or stash them" at someone
				// who has nothing to commit.
				var stage string
				switch s {
				case vcs.StepCheckoutBranch:
					stage = "checkout branch"
				case vcs.StepRebase:
					stage = "rebase onto " + mr.TargetRef
				case vcs.StepResetTarget:
					stage = "checkout target " + mr.TargetRef
				case vcs.StepMerge:
					stage = "merge --ff-only " + mr.Branch
				default:
					return nil
				}
				// classifyGateDirt returns a typed pointer; keep a nil one nil.
				if d := r.classifyGateDirt(wtDir, mr, stage, out); d != nil {
					return d
				}
				return nil
			},
		},
	}
}

// prepareFailure turns a failed vcs.Lander.Prepare into attemptMerge's stage
// and error, with the text and retry classification each step has always had.
func prepareFailure(mr *MergeRequest, err error) (string, error) {
	var se *vcs.StepError
	if !errors.As(err, &se) {
		return "fetch", err
	}
	out, gerr, args := se.Output, se.Err, se.Args
	switch se.Step {
	case vcs.StepFetch:
		// This is the step that failed all thirty-one merges on 2026-08-05,
		// and the one whose error was returned unclassified and unretried. It
		// reaches the network before it reaches the tree, so it establishes
		// nothing about the branch — the classifier decides from git's
		// verbatim output, which gitStepFail keeps.
		return "fetch", gitStepFail("fetch", fmt.Sprintf("fetch: %s: %v", out, gerr), args, out, gerr)
	case vcs.StepCheckoutBranch:
		if se.Override != nil {
			return "fetch", se.Override
		}
		return "fetch", gitStepFail("checkout-branch", fmt.Sprintf("checkout branch: %s: %v", out, gerr), args, out, gerr)
	case vcs.StepRebase:
		if se.Override != nil {
			return "rebase", se.Override
		}
		rebaseErr := gitStepFail("rebase", fmt.Sprintf("rebase onto %s: %s: %v", mr.TargetRef, out, gerr), args, out, gerr)
		// "invalid upstream" can be transient — e.g. the target branch
		// hasn't been fetched yet or the ref is missing from the clone.
		// Treat it as retryable so a fresh fetch gets another chance.
		if strings.Contains(out, "invalid upstream") {
			return "rebase", &retryableError{rebaseErr}
		}
		return "rebase", rebaseErr
	}
	return "fetch", err
}

// landFailure turns a failed vcs.Lander.Land into attemptMerge's stage and
// error. Everything here happens after the gates, so every non-auth failure is
// retryable: the next attempt re-fetches and the target self-heals (mg-f1db).
func landFailure(mr *MergeRequest, err error) (string, error) {
	var se *vcs.StepError
	if !errors.As(err, &se) {
		return "push", err
	}
	out, gerr, args := se.Output, se.Err, se.Args
	switch se.Step {
	case vcs.StepFetchTarget:
		return "fetch", &retryableError{gitStepFail("fetch-target",
			fmt.Sprintf("fetch target %s: %s: %v", mr.TargetRef, out, gerr), args, out, gerr)}
	case vcs.StepResetTarget:
		if se.Override != nil {
			return "fetch", se.Override
		}
		return "fetch", &retryableError{gitStepFail("reset-target",
			fmt.Sprintf("reset target %s to origin: %s: %v", mr.TargetRef, out, gerr), args, out, gerr)}
	case vcs.StepMerge:
		if se.Override != nil {
			return "merge", se.Override
		}
		return "rebase", &retryableError{gitStepFail("merge-ff-only",
			fmt.Sprintf("merge (ff-only): %s: %v", out, gerr), args, out, gerr)}
	case vcs.StepPush:
		// Auth failures don't recover on retry — surface the actionable
		// error immediately rather than burning attempts.
		if isAuthFailure(out) {
			return "push", gitStepFail("push", formatPushAuthError(out).Error(), args, out, gerr)
		}
		return "push", &retryableError{gitStepFail("push",
			fmt.Sprintf("push: %s: %v", out, gerr), args, out, gerr)}
	}
	return "push", err
}
