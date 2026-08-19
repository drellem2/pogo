package refinery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// defaultMaxAttempts is the fallback retry budget when no per-repo
// max_attempts is configured. Bumped from 3 → 7 to absorb the ff-only
// retry race on repos whose CI auto-pushes a version-bump commit to
// main between our fetch and push (gh-issue #13). The retry is cheap
// when paired with [gates] skip_on_retry = true.
const defaultMaxAttempts = 7

// processMerge runs the full merge pipeline for a single MR:
//  1. Ensure worktree exists for the repo
//  2. Fetch, checkout branch, rebase onto latest target
//  3. Refuse the branch outright if it carries a commit message that would
//     close a GitHub issue — not skippable
//  4. Run quality gates on rebased code, then discard anything they wrote to
//     tracked files in the refinery's own checkout (gatedirt.go)
//  5. Fast-forward merge to target ref
//  6. Push
//  7. Fast-forward the source checkout's target branch (iff clean and on
//     that branch — see fastForwardSourceCheckout)
//  8. Run the per-repo deploy hook (if configured) against the just-merged commit
//
// If another polecat merges to the target between our rebase and push,
// the ff-only merge or push will fail. We retry up to maxAttempts times
// (default 7, configurable via [gates] max_attempts) with a fresh
// fetch+rebase+(gates)+merge+push cycle. When [gates] skip_on_retry is
// set, attempts after the first skip the quality-gate phase — gates
// already passed on near-identical code; only the version-bump commit
// from main differs.
//
// Emits refinery_merge_attempted, refinery_merged, refinery_merge_failed,
// and (when a deploy hook runs) refinery_deploy_* events. Emission is
// best-effort and never propagates errors — see internal/events.Emit.
//
// Returns a mergeResult describing what landed, and the merge error (nil on
// success). Neither a deploy failure nor a post-merge step failure causes a
// non-nil error: both run after the branch has landed remotely, and the merge
// is not unwound.
func (r *Refinery) processMerge(mr *MergeRequest) (mergeResult, error) {
	wtDir, err := r.ensureWorktree(mr.RepoPath)
	if err != nil {
		return mergeResult{}, fmt.Errorf("worktree setup: %w", err)
	}

	// Already-merged guard (gh #34): a polecat whose poll loop lost track of
	// its MR can re-submit a branch that already landed on the target. Probe
	// before attempting: if the branch tip is an ancestor of origin/<target>,
	// resolve as merged without re-running gates or pushing — a second merge
	// cycle would be a wasteful no-op. The probe only recognizes tips that
	// landed verbatim; a branch whose commits were rewritten by the rebase in
	// a prior merge falls through to the normal pipeline, which no-ops safely.
	// A probe error is not fatal here — the pipeline's own fetch/checkout
	// surfaces the real problem with full retry/lost handling.
	if merged, sha, probeErr := r.probeAlreadyMerged(mr); probeErr == nil && merged {
		log.Printf("refinery: MR %s branch=%s already merged into origin/%s — resolving as merged without re-running gates (no-op)", mr.ID, mr.Branch, mr.TargetRef)
		emitMerged(mr, 0, sha, 0, true)
		gateOutput := fmt.Sprintf("(branch already merged into origin/%s — quality gates, push, and deploy skipped)", mr.TargetRef)
		// The post-merge step is NOT skipped here, unlike gates/push/deploy.
		// Those are skipped because repeating them would be a no-op; the tag is
		// skipped only if it already exists, which runPostMergeSteps decides for
		// itself. A resubmit-after-losing-track (gh #34) is precisely the case
		// where the branch landed but the declared tag may never have been
		// pushed, so refusing to try here would reproduce the half-cut release
		// this exists to prevent (mg-6879).
		postMergeErr := r.runPostMergeSteps(wtDir, mr, sha)
		return mergeResult{
			GateOutput:     gateOutput,
			PostMergeError: postMergeErr,
			MergedSHA:      sha,
			AlreadyMerged:  true,
		}, nil
	} else if probeErr != nil {
		log.Printf("refinery: MR %s already-merged probe inconclusive (%v) — proceeding with merge", mr.ID, probeErr)
	}

	cfg := r.loadConfig(wtDir, mr.RepoPath)
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	skipGatesOnRetry := cfg.SkipGatesOnRetry

	var gateOutput string
	startTime := time.Now()

	// Three independent retry budgets, one per retryable class (mg-e5c2). They
	// are separate on purpose: a network blip must not consume the budget that
	// exists to absorb a lost race with another merge, and vice versa. The loop
	// is hard-capped at their sum so a pathological alternation still ends.
	contentionBudget := maxAttempts
	netBudget := networkMaxAttempts
	// A fourth budget, for the infrastructure failures whose retry costs a WHOLE
	// GATE RUN rather than a git command (mg-67c9). Same class as netBudget, and
	// separate for the same reason the other three are separate from each other:
	// spending 28 attempts here would hold one repo's lane for hours. See the
	// sizing note in gatenetwork.go.
	gateNetBudget := gateNetworkMaxAttempts
	// A fifth budget, for a gate whose OWN SETUP did not stand up (mg-15bb).
	// Separate from gateNetBudget even though both cost a whole gate run: the
	// two conditions have unrelated durations, and sharing a counter would let
	// a network outage spend the attempts that exist to ask "was the envelope
	// broken once, or standing?" — which is the only question this budget buys
	// an answer to. See the sizing note in gatesetup.go.
	setupBudget := gateSetupMaxAttempts
	unclassifiedBudget := defaultUnclassifiedAttempts
	var contentionUsed, netUsed, gateNetUsed, setupUsed, unclassifiedUsed int
	var backoffSpent time.Duration
	// gatesReached records whether any attempt has got as far as the quality
	// gates. It gates skip_on_retry — see the call site below.
	var gatesReached bool
	// hold carries a completed gate verdict across a retry so a transport
	// failure AFTER the gates does not discard the gate run (mg-c3b7). It is
	// per-merge-request and revalidated against the rebased tree on every
	// attempt — see gateHold.
	hold := &gateHold{}
	hardCap := contentionBudget + netBudget + gateNetBudget + setupBudget + unclassifiedBudget

	for attempt := 1; attempt <= hardCap; attempt++ {
		// Cancellation is honoured at attempt boundaries as well as inside the
		// gate, so a cancel that arrives during a git step still takes effect
		// rather than being swallowed until the next gate starts.
		if r.cancelWasRequested(mr) {
			emitMergeCancelled(mr, attempt, "before-attempt", gateOutput)
			return mergeResult{GateOutput: gateOutput}, cancelledMergeError("before-attempt")
		}
		if attempt > 1 {
			log.Printf("refinery: MR %s step=retry attempt=%d (cap=%d) contention=%d/%d network=%d/%d gate-network=%d/%d setup=%d/%d unclassified=%d/%d backoff_spent=%s",
				mr.ID, attempt, hardCap, contentionUsed, contentionBudget, netUsed, netBudget, gateNetUsed, gateNetBudget, setupUsed, setupBudget, unclassifiedUsed, unclassifiedBudget, backoffSpent.Round(time.Second))
		}

		emitMergeAttempted(mr, attempt)

		// skip_on_retry rests on a premise — "gates already passed on
		// near-identical code" — which network retries can falsify. Before
		// mg-e5c2 a fetch failure was terminal, so every retry followed an
		// attempt that HAD reached the gates. A retried fetch failure has not,
		// and skipping on `attempt > 1` alone would let a merge whose gates never
		// ran land ungated. The condition is therefore what the premise actually
		// claims: gates were reached at least once.
		skipGates := skipGatesOnRetry && gatesReached
		output, stage, sha, reached, attemptErr := r.attemptMerge(wtDir, mr, attempt, skipGates, cfg.PRMode, hold)
		gatesReached = gatesReached || reached
		gateOutput = output
		if attemptErr == nil {
			// A retried success names the attempt that won — the third condition
			// of pm-pogo's ruling. A silent retry converts a flaky night into an
			// invisible one, and invisible is how this box's network came to be
			// the dominant failure mode without anybody holding the evidence.
			if attempt > 1 {
				log.Printf("refinery: MR %s RECOVERED on attempt %d after %d failed attempt(s) (%s) — %s of backoff; the earlier failures are recorded on the merge request",
					mr.ID, attempt, attempt-1, r.attemptClassSummary(mr), backoffSpent.Round(time.Second))
				r.recordRecovery(mr, attempt, backoffSpent)
			}
			emitMerged(mr, attempt, sha, time.Since(startTime).Seconds(), false)
			// origin/<target> just advanced; refresh the checkout the MR
			// was submitted from so it doesn't go stale (gh #30).
			// Best-effort — logs and skips unless clean and on the target.
			fastForwardSourceCheckout(mr.RepoPath, mr.TargetRef)
			// Close out the branch's GitHub PR and reap its remote branch so
			// PR-flow loop-closure never leaves danglers (mg-f18c). Soft —
			// never unwinds an already-landed merge.
			r.closePRAndReap(wtDir, mr, sha)
			// Run the per-repo post-merge deploy hook against the refinery's
			// clone (which now has the merged commit on the target ref). The
			// hook owns refreshing runtime snapshots like ~/.pogo/<repo>/bin/
			// so they reflect the just-merged code. Failure is reported via
			// DeployError + event but does not unwind the merge.
			deployErr := r.runDeploy(wtDir, mr)
			// Perform the post-merge protocol the submitter declared, against
			// the SHA that just landed (mg-6879). This runs INSIDE the merge
			// pipeline on purpose: processNext fires OnMerged only after
			// processMerge returns, so the step is complete before the reap can
			// observe the merge at all. Deferring the reap would have left a
			// window; finishing the work first leaves none.
			postMergeErr := r.runPostMergeSteps(wtDir, mr, sha)
			return mergeResult{
				GateOutput:     gateOutput,
				DeployError:    deployErr,
				PostMergeError: postMergeErr,
				MergedSHA:      sha,
			}, nil
		}

		// A cancelled gate must not be retried: the retry loop exists to
		// absorb races with other merges, not to defeat an operator.
		if isCancelled(attemptErr) || r.cancelWasRequested(mr) {
			emitMergeCancelled(mr, attempt, stage, gateOutput)
			return mergeResult{GateOutput: gateOutput}, cancelledMergeError(stage)
		}

		// Classify BEFORE deciding, and record the transport and the raw error
		// whatever the decision is (mg-e5c2).
		fail, disp := r.describeAttemptFailure(wtDir, attempt, stage, attemptErr)

		retry := false
		reason := disp.Reason
		// retryReason is the mirror of reason and is recorded when a retry DOES
		// follow (mg-15bb). It is composed per arm rather than taken from the
		// classifier alone, because the sentence a reader needs includes where
		// in the budget this attempt sat — "attempt 2 of 3" is the difference
		// between a policy and a loop.
		retryReason := disp.RetryReason
		var backoff time.Duration
		switch {
		case !disp.Retryable:
			// reason is the classifier's sentence for why re-running would give
			// the same answer.
		case disp.Class == ClassInfrastructure && disp.GateRerun:
			// Ahead of the plain infrastructure arm: same class, different price.
			gateNetUsed++
			next := gateNetworkBackoffFor(gateNetUsed)
			switch {
			case gateNetUsed >= gateNetBudget:
				reason = fmt.Sprintf("not retryable: the gate-network retry budget is spent — %d of %d attempts used over %s of backoff, each one a whole gate run on the single serial slot every queued merge waits behind. The class is still INFRASTRUCTURE: the GATE could not reach the network, so this is not a verdict on the branch and no fix is warranted. The outage outlasted the refinery's patience — resubmit unchanged once the network is back",
					gateNetUsed, gateNetBudget, backoffSpent.Round(time.Second))
			case backoffSpent+next > networkRetryBudget:
				reason = fmt.Sprintf("not retryable: waiting %s more would exceed the %s retry budget (%s already slept). The class is still INFRASTRUCTURE — the GATE could not reach the network, not a verdict on the branch",
					next, networkRetryBudget, backoffSpent.Round(time.Second))
			default:
				retry, backoff = true, next
			}
		case disp.Class == ClassInfrastructure:
			netUsed++
			next := networkBackoffFor(netUsed)
			switch {
			case netUsed >= netBudget:
				reason = fmt.Sprintf("not retryable: the network retry budget is spent — %d of %d network-class attempts used over %s of backoff. The class is still INFRASTRUCTURE: the outage outlasted the refinery's patience, it is not a verdict on the branch",
					netUsed, netBudget, backoffSpent.Round(time.Second))
			case backoffSpent+next > networkRetryBudget:
				reason = fmt.Sprintf("not retryable: waiting %s more would exceed the %s network retry budget (%s already slept), and this is one serial slot every queued merge waits behind. The class is still INFRASTRUCTURE",
					next, networkRetryBudget, backoffSpent.Round(time.Second))
			default:
				retry, backoff = true, next
			}
		case disp.Class == ClassSetup:
			setupUsed++
			next := gateSetupBackoffFor(setupUsed)
			switch {
			case setupUsed >= setupBudget:
				reason = fmt.Sprintf("not retryable: the setup retry budget is spent — %d of %d attempts used over %s of backoff, each one a whole gate run on the single serial slot every queued merge waits behind. The class is still SETUP: the gate's own setup failed on every attempt, so it never returned a verdict on this tree and this is NOT a finding against the branch. What the retries established is that the envelope is broken STANDING rather than once; they did NOT establish whose setup it is — a branch can break its own. Read the banner and establish which; do NOT dispatch a fix on this alone",
					setupUsed, setupBudget, backoffSpent.Round(time.Second))
			case backoffSpent+next > networkRetryBudget:
				reason = fmt.Sprintf("not retryable: waiting %s more would exceed the %s retry budget (%s already slept). The class is still SETUP — the gate's own setup failed, not a verdict on the branch",
					next, networkRetryBudget, backoffSpent.Round(time.Second))
			default:
				retry, backoff = true, next
				retryReason = fmt.Sprintf("RETRYING (setup attempt %d of %d, after %s): %s",
					setupUsed+1, setupBudget, next, disp.RetryReason)
			}
		case disp.Class == ClassContention:
			contentionUsed++
			if contentionUsed >= contentionBudget {
				reason = fmt.Sprintf("not retryable: the contention retry budget is spent — %d of %d attempts used", contentionUsed, contentionBudget)
			} else {
				retry = true
			}
		case disp.Class == ClassUnclassified:
			unclassifiedUsed++
			if unclassifiedUsed >= unclassifiedBudget {
				reason = fmt.Sprintf("not retryable: %d of %d unclassified attempts used, and the refinery still cannot place this failure — read the raw error above before reacting", unclassifiedUsed, unclassifiedBudget)
			} else {
				retry = true
			}
		}

		fail.Retried = retry
		fail.BackoffSeconds = backoff.Seconds()
		if retry {
			if strings.TrimSpace(retryReason) == "" {
				// Symmetric with the not-retried case below, and for the same
				// reason: a retry with no stated purpose reads as a loop rather
				// than as a decision that was made.
				retryReason = fmt.Sprintf("RETRYING: the refinery classified this as %s, which establishes nothing about the branch that a re-run would repeat",
					fail.Class)
			}
			fail.RetriedReason = retryReason
		}
		if !retry {
			if strings.TrimSpace(reason) == "" {
				// Never leave the field empty. A blank reason is exactly the
				// silence mg-e5c2 was filed about: it reads as "no retry policy
				// exists" rather than as a decision that was made.
				reason = fmt.Sprintf("not retryable: the refinery classified this as %s and recorded no further reason — read the raw error below", fail.Class)
			}
			fail.NotRetriedReason = reason
		}
		r.recordAttemptFailure(mr, fail)
		emitMergeFailed(mr, attempt, stage, attemptErr, !retry, gateOutput, fail)

		// One line per attempt, carrying the class, the transport and whether a
		// retry followed. Before mg-e5c2 the log said only "attempt N failed",
		// so "failed once" and "gave up after three" read identically.
		log.Printf("refinery: MR %s %s", mr.ID, fail.Line())
		log.Printf("refinery: MR %s attempt %d raw error (verbatim, transport=%s): %v",
			mr.ID, attempt, transportOrUnknown(fail.Transport), attemptErr)

		if retry {
			if backoff > 0 {
				log.Printf("refinery: MR %s waiting %s before attempt %d (class=%s, budget %s of %s spent)",
					mr.ID, backoff, attempt+1, disp.Class, backoffSpent.Round(time.Second), networkRetryBudget)
				if !r.sleepUnlessCancelled(mr, backoff) {
					emitMergeCancelled(mr, attempt, "retry-backoff", gateOutput)
					return mergeResult{GateOutput: gateOutput}, cancelledMergeError("retry-backoff")
				}
				backoffSpent += backoff
			}
			continue
		}
		return mergeResult{GateOutput: gateOutput}, attemptErr
	}
	finalErr := fmt.Errorf("merge failed after %d attempts (hard cap)", hardCap)
	emitMergeFailed(mr, hardCap, "unknown", finalErr, true, gateOutput, AttemptFailure{
		Attempt:          hardCap,
		Stage:            "unknown",
		Time:             time.Now(),
		RawError:         finalErr.Error(),
		Class:            ClassUnclassified,
		NotRetriedReason: "not retryable: the combined attempt cap was reached",
	})
	return mergeResult{GateOutput: gateOutput}, finalErr
}

// describeAttemptFailure builds the verbatim record for one failing attempt and
// classifies it. The transport is measured from the clone's origin URL, and
// only falls back to the error's own wording when the URL cannot be read — see
// failureclass.go for why the transport is recorded at all.
func (r *Refinery) describeAttemptFailure(wtDir string, attempt int, stage string, err error) (AttemptFailure, disposition) {
	fail := AttemptFailure{
		Attempt:  attempt,
		Stage:    stage,
		Time:     time.Now(),
		RawError: errorText(err),
	}
	var step *gitStepError
	if errors.As(err, &step) {
		fail.Command = step.cmd
		if step.raw != "" {
			fail.RawError = step.raw + "\n" + errorText(err)
		}
		if step.step != "" {
			fail.Stage = step.step
		}
	}
	// Read the remote directly rather than through gitCmdOutput: `config --get`
	// exits 1 when the key is simply absent, and routing that through the
	// logging helper would print an error line under a non-error outcome on
	// every failure in a repo with no origin. Benign lines at error level teach
	// a reader to skip error lines (mg-5d3f).
	if out, cerr := exec.Command("git", "-C", wtDir, "config", "--get", "remote.origin.url").Output(); cerr == nil {
		if remote := strings.TrimSpace(string(out)); remote != "" {
			fail.Remote = remote
			fail.Transport, _ = remoteTransport(remote)
		}
	}
	if fail.Transport == "" || fail.Transport == "unknown" {
		if t := transportFromError(fail.RawError); t != "" {
			fail.Transport = t
		}
	}
	// Bound what is persisted. Verbatim is the requirement, unbounded is not: a
	// merge request keeps up to hardCap of these and history retains 100 merge
	// requests. Transport errors are a few hundred bytes, so the cap is far
	// above any of them — and when it does bite it says so rather than silently
	// shortening the evidence.
	if len(fail.RawError) > rawErrorRecordCap {
		fail.RawError = truncate(fail.RawError, rawErrorRecordCap) +
			fmt.Sprintf("\n… (truncated at %d bytes for the persisted record; the full text is in the log line above)", rawErrorRecordCap)
	}
	disp := classifyFailure(stage, rawOf(err), err)
	fail.Class = disp.Class
	fail.Signal = disp.Signal
	return fail, disp
}

// recordAttemptFailure appends the record to the merge request and persists it,
// so the attempt history survives a pogod restart and is readable from
// `pogo refinery show` and `--json` without going to the log.
func (r *Refinery) recordAttemptFailure(mr *MergeRequest, fail AttemptFailure) {
	// After the unlock (defers are LIFO). The doc comment above claims this
	// record survives a restart, so it is flushed rather than left pending —
	// but the wait happens with r.mu released (mg-538e).
	defer r.flushState()
	r.mu.Lock()
	defer r.mu.Unlock()
	mr.Attempts = append(mr.Attempts, fail)
	mr.AttemptCount = fail.Attempt
	mr.FailureClass = fail.Class
	mr.NotRetriedReason = fail.NotRetriedReason
	r.saveStateLocked()
}

// recordRecovery marks the attempt that won after earlier attempts failed.
func (r *Refinery) recordRecovery(mr *MergeRequest, attempt int, backoffSpent time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mr.AttemptCount = attempt
	mr.RecoveredOnAttempt = attempt
	mr.RetryBackoffSeconds = backoffSpent.Seconds()
	// The merge succeeded: the failures on the way are history, not a verdict.
	mr.FailureClass = ""
	mr.NotRetriedReason = ""
	r.saveStateLocked()
}

// attemptClassSummary renders the classes seen so far, e.g. "2x infrastructure".
func (r *Refinery) attemptClassSummary(mr *MergeRequest) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return summarizeAttemptClasses(mr.Attempts)
}

// sleepUnlessCancelled waits for d, returning false if a cancel was requested
// for mr while waiting. Backoff must not make a cancel wait out the whole
// schedule.
//
// It takes the merge request because cancellation is per-LANE since mg-37ad:
// several merges can be backing off at once, and each must watch its own lane's
// cancel rather than any cancel anywhere in the refinery.
func (r *Refinery) sleepUnlessCancelled(mr *MergeRequest, d time.Duration) bool {
	const tick = 250 * time.Millisecond
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if r.cancelWasRequested(mr) {
			return false
		}
		remaining := time.Until(deadline)
		if remaining > tick {
			remaining = tick
		}
		time.Sleep(remaining)
	}
	return !r.cancelWasRequested(mr)
}

// summarizeAttemptClasses counts the classes across a set of failed attempts,
// in the order they first appeared.
func summarizeAttemptClasses(attempts []AttemptFailure) string {
	if len(attempts) == 0 {
		return "no failed attempts"
	}
	var order []FailureClass
	counts := map[FailureClass]int{}
	for _, a := range attempts {
		if counts[a.Class] == 0 {
			order = append(order, a.Class)
		}
		counts[a.Class]++
	}
	parts := make([]string, 0, len(order))
	for _, c := range order {
		parts = append(parts, fmt.Sprintf("%dx %s", counts[c], c))
	}
	return strings.Join(parts, ", ")
}

// errorText is err.Error() with a nil guard.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// rawOf returns git's verbatim combined output when the error carries it, and
// the empty string otherwise — the classifier then falls back to the error text.
func rawOf(err error) string {
	var step *gitStepError
	if errors.As(err, &step) {
		return step.raw
	}
	return ""
}

// gitStepError carries what a failing git step knew and used to throw away: the
// step name, the command as invoked, and git's combined output VERBATIM.
//
// The doubt section of mg-e5c2 asked for exactly this ("capture the failing
// fetch's actual command as invoked") and it is what makes the classification
// auditable: a reader can see the wording that was matched rather than trusting
// the class the refinery assigned.
type gitStepError struct {
	step string // pipeline step, e.g. "fetch" or "checkout-branch"
	cmd  string // git subcommand as invoked, e.g. "fetch origin"
	raw  string // git's combined stdout+stderr, verbatim
	msg  string // the sentence the pipeline reports
	err  error
}

func (e *gitStepError) Error() string { return e.msg }
func (e *gitStepError) Unwrap() error { return e.err }

// gitStepFail wraps a failed git invocation. The message keeps the shape the
// pipeline used before mg-e5c2 ("fetch: <git output>: <err>") so existing
// readers are unchanged; the structured fields are additions.
func gitStepFail(step, msg string, args []string, raw string, err error) error {
	return &gitStepError{
		step: step,
		cmd:  "git " + strings.Join(args, " "),
		raw:  raw,
		msg:  msg,
		err:  err,
	}
}

// defaultUnclassifiedAttempts bounds retries of failures the refinery could not
// place. Small on purpose: an unrecognised failure gets the benefit of the
// doubt once, not a full network budget.
const defaultUnclassifiedAttempts = 2

// gateHold carries a COMPLETED gate verdict across a retry, so a transport
// failure that lands after the gates does not throw the gate run away
// (mg-c3b7).
//
// The cost being removed is asymmetric and larger than it looks. Every network
// step except the first `fetch origin` runs AFTER `./build.sh`: fetch-target,
// reset-target, the ff-merge and the push. So a socket that fails there does
// not cost a retry, it costs the ENTIRE gate run — on 2026-08-10 that was 8m58s
// of CPU on a contended box, discarded and re-run from scratch on resubmit. The
// failure lands at the most expensive possible moment.
//
// The hold is keyed on the TREE OBJECT of the rebased branch, not on the
// attempt number, and that is the whole safety argument. `git rev-parse
// HEAD^{tree}` is content-addressed: an identical value means the re-fetch and
// re-rebase reproduced byte-identical content, so re-running the gates would
// compile and test exactly the same bytes. The moment origin/<target> or the
// branch moves during the outage, the rebase yields a different tree, the hold
// does not match, and the gates run again. Nothing decides to trust a stale
// verdict — the verdict is only reused while the thing it was a verdict ABOUT
// is provably unchanged.
//
// This is strictly stronger than the [gates] skip_on_retry option that has
// shipped for much longer: that one skips on `attempt > 1` whatever the tree
// says. Where both apply, skip_on_retry is checked first and this changes
// nothing.
//
// Two caveats, stated rather than assumed:
//
// One — a gate that reads git HISTORY rather than the tree (commit messages,
// author dates) could in principle see a difference the tree hash cannot
// express, since a rebase rewrites committer dates. The refinery's own
// history-reading check, the closing-ref gate, is therefore deliberately NOT
// part of the hold and re-runs on every attempt; see its call site above.
//
// Two — a held verdict means the gate's COMMANDS do not run again, so a gate
// with side effects outside the checkout fires once per distinct tree rather
// than once per attempt. That is the intended saving (the 8m58s of compute is
// exactly such a side effect), but it is a real behavioural change for anything
// that was counting gate invocations.
type gateHold struct {
	// tree is `git rev-parse HEAD^{tree}` of the rebased branch the gates
	// passed on. Empty means nothing is held.
	tree string
	// output is that run's verbatim gate output, replayed rather than recomputed.
	output string
	// attempt is the attempt number the gates actually ran on.
	attempt int
}

// held reports whether the hold covers the tree about to be merged.
func (h *gateHold) held(tree string) bool {
	return h != nil && h.tree != "" && tree != "" && h.tree == tree
}

// record captures a gate run that has just passed.
func (h *gateHold) record(tree, output string, attempt int) {
	if h == nil || tree == "" {
		// A tree we could not read is a hold we cannot revalidate, so it is not
		// taken. Failing closed here costs a re-run; failing open would risk
		// landing an ungated tree, which is the one outcome this must not buy.
		return
	}
	h.tree, h.output, h.attempt = tree, output, attempt
}

// gatedTreeOf reads the tree object the gates would test. An unreadable tree
// returns "", which held() and record() both treat as "no hold".
func gatedTreeOf(wtDir string) string {
	out, err := exec.Command("git", "-C", wtDir, "rev-parse", "HEAD^{tree}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// shortSHA abbreviates an object name for log and mail lines. The full tree is
// never the thing a reader needs; that two attempts printed the SAME prefix is.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// attemptMerge runs a single fetch→rebase→gates→merge→push cycle. Returns
// the captured gate output, the pipeline stage that ran (or failed), the
// merge commit SHA on success (empty otherwise), and any error.
//
// When skipGates is true, the quality-gate phase is bypassed — used on
// retries when [gates] skip_on_retry is set, on the principle that gates
// already passed on near-identical code and only the version-bump commit
// from main differs.
//
// When prMode is true ([gates] pr_mode in .pogo/refinery.toml) and an open
// GitHub PR exists for the branch, the rebased branch is force-pushed back
// to origin after gates pass and before the ff-merge push, so GitHub marks
// the PR "merged" (not "closed") when the tip lands on the target. All
// failures on that path are soft — see pushBackForPR.
// gatesReached reports whether this attempt got past the quality-gate phase —
// either by running the gates and passing, or by legitimately skipping them.
// processMerge uses it to decide whether skip_on_retry may apply on the NEXT
// attempt; see the comment at its call site.
//
// hold is read-write: a passing gate run records its tree and output there, and
// a later attempt that rebases to the SAME tree replays it instead of spending
// another gate run. See gateHold.
func (r *Refinery) attemptMerge(wtDir string, mr *MergeRequest, attempt int, skipGates, prMode bool, hold *gateHold) (output string, stage string, sha string, gatesReached bool, err error) {
	// Fetch latest from origin
	log.Printf("refinery: MR %s step=fetch branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
	if out, gerr := gitCmdOutput(wtDir, "fetch", "origin"); gerr != nil {
		// This is the step that failed all thirty-one merges on 2026-08-05, and
		// the one whose error was returned unclassified and unretried. It reaches
		// the network before it reaches the tree, so it establishes nothing about
		// the branch — the classifier decides from git's verbatim output, which
		// gitStepFail keeps.
		return "", "fetch", "", false, gitStepFail("fetch", fmt.Sprintf("fetch: %s: %v", out, gerr),
			[]string{"fetch", "origin"}, out, gerr)
	}

	// Clear anything a previous attempt's gate (or a previous MR's, this clone
	// is reused) left modified in the tracked tree. Without this the checkout
	// and rebase below fail on writes the author never made — see gatedirt.go.
	r.discardGateSideEffectsAt(wtDir, mr, attempt, "attempt-entry")

	// Checkout the branch fresh from origin
	log.Printf("refinery: MR %s step=checkout-branch branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
	if out, gerr := gitCmdOutput(wtDir, "checkout", "-B", mr.Branch, "origin/"+mr.Branch); gerr != nil {
		if dirtErr := r.classifyGateDirt(wtDir, mr, "checkout branch", out); dirtErr != nil {
			return "", "fetch", "", false, dirtErr
		}
		return "", "fetch", "", false, gitStepFail("checkout-branch", fmt.Sprintf("checkout branch: %s: %v", out, gerr),
			[]string{"checkout", "-B", mr.Branch, "origin/" + mr.Branch}, out, gerr)
	}

	// Rebase onto latest target so the branch is a direct descendant of main.
	// Polecat branches fork from main at spawn time and may be behind by the
	// time they reach the refinery.
	log.Printf("refinery: MR %s step=rebase target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "rebase", "origin/"+mr.TargetRef); gerr != nil {
		// A dirty tree is the one rebase failure that is never the author's
		// doing: this clone is private to the refinery, so whatever is modified
		// in it was written by a gate. Say that, instead of relaying git's
		// "commit or stash them" at someone who has nothing to commit.
		// Classified BEFORE the abort, because the abort's own reset erases the
		// evidence the message is built from. (mg-393f)
		dirtErr := r.classifyGateDirt(wtDir, mr, "rebase onto "+mr.TargetRef, out)
		// Abort the failed rebase to leave worktree in a clean state
		gitCmdOutput(wtDir, "rebase", "--abort")
		if dirtErr != nil {
			return "", "rebase", "", false, dirtErr
		}
		rebaseErr := gitStepFail("rebase", fmt.Sprintf("rebase onto %s: %s: %v", mr.TargetRef, out, gerr),
			[]string{"rebase", "origin/" + mr.TargetRef}, out, gerr)
		// "invalid upstream" can be transient — e.g. the target branch
		// hasn't been fetched yet or the ref is missing from the clone.
		// Treat it as retryable so a fresh fetch gets another chance.
		if strings.Contains(out, "invalid upstream") {
			return "", "rebase", "", false, &retryableError{rebaseErr}
		}
		return "", "rebase", "", false, rebaseErr
	}

	// Reject commit messages that would close a GitHub issue by keyword
	// adjacency (mg-2627). Runs on the rebased branch, before the quality
	// gates, and is NOT subject to skip_on_retry — see checkClosingRefs for
	// why this is the refinery's job and not only the local hook's.
	log.Printf("refinery: MR %s step=closing-ref-check branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
	if cerr := checkClosingRefs(wtDir, mr.TargetRef, mr.Branch); cerr != nil {
		return cerr.Error(), "closing-ref-check", "", false, cerr
	}

	// The content the gates would test, read AFTER the rebase so it is the tree
	// that would actually land. This is the key the gate hold is validated on —
	// see gateHold.
	gatedTree := gatedTreeOf(wtDir)

	// Run quality gates (on the rebased branch — tests what will actually
	// land). On retries with skip_on_retry set, bypass: gates already
	// passed on attempt 1 over near-identical code; the only change is
	// the version-bump commit fetched from main.
	var gateOutput string
	switch {
	case skipGates:
		log.Printf("refinery: MR %s step=quality-gates attempt=%d skipped (skip_on_retry=true)", mr.ID, attempt)
		gateOutput = "(quality gates skipped on retry — skip_on_retry=true)"
	case hold.held(gatedTree):
		// A previous attempt on this MR gated this exact tree and passed, then
		// lost the transport on the way to the push. Re-running the gates would
		// compile and test byte-identical content for a byte-identical answer.
		log.Printf("refinery: MR %s step=quality-gates attempt=%d HELD from attempt %d — the rebased tree is unchanged (%s), so the completed verdict still applies and is not recomputed",
			mr.ID, attempt, hold.attempt, shortSHA(gatedTree))
		gateOutput = hold.output + fmt.Sprintf(
			"\n\n(quality gates NOT re-run on attempt %d: they passed on attempt %d and the rebased tree is byte-identical (tree %s), so the verdict above is that run's, replayed verbatim. A transport failure after the gates is a verdict on the network, not on the branch — mg-c3b7.)\n",
			attempt, hold.attempt, shortSHA(gatedTree))
	default:
		log.Printf("refinery: MR %s step=quality-gates attempt=%d heartbeat_every=%s", mr.ID, attempt, r.gateHeartbeat())
		out, gates, qerr := r.runQualityGates(r.gateContext(mr), wtDir, mr.RepoPath, mr)
		gateOutput = out
		if qerr != nil {
			return gateOutput, gateStage(gates), "", false, fmt.Errorf("quality gate: %w", qerr)
		}
		// The gate has just run arbitrary commands in this checkout. If any of
		// them wrote a tracked file — a regenerated record, a lockfile, a
		// coverage report — the target checkout and ff-merge below would both
		// refuse, and on the next attempt so would the rebase. Discard the
		// writes here and record them on the MR, so the gate's own output is
		// the thing named rather than the author's non-existent local changes.
		// (mg-393f)
		if discarded := r.discardGateSideEffectsAt(wtDir, mr, attempt, "post-gate"); len(discarded) > 0 {
			gateOutput += gateWriteNote(discarded)
		}
		// Recorded only after the side-effect discard, so a replay carries the
		// same text a reader would have seen from the original run.
		hold.record(gatedTree, gateOutput, attempt)
	}

	// PR-mode push-back (phase 2, mg-b828): the rebase above rewrote the
	// branch's SHAs, so the PR's head tip would never become reachable from
	// the target and GitHub would show the PR "closed" instead of "merged".
	// Pushing the rebased branch back to origin before the ff-merge push
	// realigns the PR head with exactly the gate-tested commits that are
	// about to land. Must happen before the target push — GitHub marks a PR
	// merged when the head tip becomes reachable from the base.
	if prMode {
		r.pushBackForPR(wtDir, mr, attempt)
	}

	// Check out the target and hard-reset it to origin, discarding any local
	// state on the target left by a prior attempt or a prior MR that reused
	// this persistent clone (ensureWorktree keeps one clone per repo).
	//
	// The old path — plain `git checkout <target>` + `git pull --ff-only` —
	// cannot recover a clone whose local target is AHEAD of origin. That
	// happens when an earlier cycle's local ff-merge (below) succeeded but the
	// subsequent `git push origin <target>` FAILED (protected branch, transient
	// network/remote error): the local target is left ahead and never rolled
	// back. The next `pull --ff-only` then aborts non-fatally with "fatal: Not
	// possible to fast-forward", which is both misleading (the real cause is the
	// earlier failed push, not the branch under merge) and was returned
	// non-retryable — wedging this MR and every later MR reusing the clone.
	//
	// Fetch fresh (origin may have advanced during the gate phase) and realign
	// the target to origin/<target> the same way the source branch is reset at
	// the top of this attempt (checkout -B origin/<branch>, above). `-B` forces
	// the local target ref to the fetched origin tip regardless of prior local
	// state, so a poisoned/ahead target self-heals instead of aborting. (mg-f1db)
	log.Printf("refinery: MR %s step=fetch-target target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "fetch", "origin", mr.TargetRef); gerr != nil {
		return gateOutput, "fetch", "", true, &retryableError{gitStepFail("fetch-target",
			fmt.Sprintf("fetch target %s: %s: %v", mr.TargetRef, out, gerr),
			[]string{"fetch", "origin", mr.TargetRef}, out, gerr)}
	}
	log.Printf("refinery: MR %s step=reset-target target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "checkout", "-B", mr.TargetRef, "origin/"+mr.TargetRef); gerr != nil {
		if dirtErr := r.classifyGateDirt(wtDir, mr, "checkout target "+mr.TargetRef, out); dirtErr != nil {
			return gateOutput, "fetch", "", true, dirtErr
		}
		return gateOutput, "fetch", "", true, &retryableError{gitStepFail("reset-target",
			fmt.Sprintf("reset target %s to origin: %s: %v", mr.TargetRef, out, gerr),
			[]string{"checkout", "-B", mr.TargetRef, "origin/" + mr.TargetRef}, out, gerr)}
	}

	// Fast-forward merge — guaranteed to work if target hasn't moved since fetch
	log.Printf("refinery: MR %s step=merge branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
	if out, gerr := gitCmdOutput(wtDir, "merge", "--ff-only", mr.Branch); gerr != nil {
		if dirtErr := r.classifyGateDirt(wtDir, mr, "merge --ff-only "+mr.Branch, out); dirtErr != nil {
			return gateOutput, "merge", "", true, dirtErr
		}
		return gateOutput, "rebase", "", true, &retryableError{gitStepFail("merge-ff-only",
			fmt.Sprintf("merge (ff-only): %s: %v", out, gerr),
			[]string{"merge", "--ff-only", mr.Branch}, out, gerr)}
	}

	// Push to origin
	log.Printf("refinery: MR %s step=push target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "push", "origin", mr.TargetRef); gerr != nil {
		// Auth failures don't recover on retry — surface the actionable
		// error immediately rather than burning attempts.
		if isAuthFailure(out) {
			return gateOutput, "push", "", true, gitStepFail("push", formatPushAuthError(out).Error(),
				[]string{"push", "origin", mr.TargetRef}, out, gerr)
		}
		return gateOutput, "push", "", true, &retryableError{gitStepFail("push",
			fmt.Sprintf("push: %s: %v", out, gerr),
			[]string{"push", "origin", mr.TargetRef}, out, gerr)}
	}

	// Capture the merge commit SHA (HEAD on target after fast-forward).
	// Best-effort: if rev-parse fails, return empty SHA — the merge already
	// pushed successfully.
	headSHA, _ := gitCmdOutput(wtDir, "rev-parse", "HEAD")

	return gateOutput, "push", headSHA, true, nil
}

// prLookupTimeout bounds the gh CLI call in openPRNumber so a hung network
// lookup can't stall the merge pipeline — the push-back is cosmetic-only and
// never worth blocking a merge on.
const prLookupTimeout = 30 * time.Second

// prCloseTimeout bounds the `gh pr close` call in closePRAndReap for the same
// reason — the close is loop-closure hygiene that runs after the merge has
// already landed, and must never hold the pipeline open.
const prCloseTimeout = 30 * time.Second

// pushBackForPR force-pushes the just-rebased branch back to origin when an
// open GitHub PR exists for it, so GitHub marks the PR "merged" once the tip
// lands on the target. Every failure here is soft: the merge itself must
// never be blocked by PR cosmetics, so lookup errors (gh missing, no
// network, non-GitHub remote) and push failures (lease lost to a concurrent
// push on the PR branch) are logged and skipped — the PR then reads
// "closed" instead of "merged", which is the pre-phase-2 status quo.
func (r *Refinery) pushBackForPR(wtDir string, mr *MergeRequest, attempt int) {
	num, err := openPRNumber(wtDir, mr.Branch)
	if err != nil {
		log.Printf("refinery: MR %s step=pr-push-back skipped: gh lookup failed (%v) — a PR for %s (if any) will read closed, not merged", mr.ID, err, mr.Branch)
		return
	}
	if num == 0 {
		log.Printf("refinery: MR %s step=pr-push-back skipped: no open PR for branch %s", mr.ID, mr.Branch)
		return
	}
	log.Printf("refinery: MR %s step=pr-push-back branch=%s pr=#%d attempt=%d", mr.ID, mr.Branch, num, attempt)
	// --force-with-lease uses origin/<branch> as fetched at the top of this
	// attempt: if anyone pushed to the PR branch since, the push is refused
	// instead of clobbering their commits.
	if out, gerr := gitCmdOutput(wtDir, "push", "--force-with-lease", "origin", mr.Branch); gerr != nil {
		log.Printf("refinery: MR %s step=pr-push-back failed (%s) — proceeding; PR #%d will read closed, not merged", mr.ID, out, num)
	}
}

// openPRNumber returns the number of the open GitHub PR whose head is
// branch, or 0 when the branch has a PR that is not open. A branch with no PR
// at all is reported as (0, nil); anything else that goes wrong is returned as
// an error for the caller to fail soft on. See lookupPR.
func openPRNumber(wtDir, branch string) (int, error) {
	num, state, err := lookupPR(wtDir, branch)
	if err != nil || !strings.EqualFold(state, "OPEN") {
		return 0, err
	}
	return num, nil
}

// lookupPR returns the number and state ("OPEN", "MERGED", "CLOSED") of the
// GitHub PR whose head is branch. gh infers the GitHub repo from the
// worktree's origin remote. A branch with no PR at all is reported as
// (0, "", nil); anything else that goes wrong (gh not installed, no network,
// non-GitHub remote, output drift) is returned as an error for the caller to
// fail soft on.
func lookupPR(wtDir, branch string) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), prLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", branch, "--json", "state,number")
	cmd.Dir = wtDir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.TrimSpace(string(ee.Stderr))
			// gh exits 1 with this message when the branch simply has no
			// PR — a normal state for internal mg-track branches, not a
			// lookup failure.
			if strings.Contains(strings.ToLower(stderr), "no pull requests found") {
				return 0, "", nil
			}
			return 0, "", fmt.Errorf("gh pr view %s: %s: %w", branch, stderr, err)
		}
		return 0, "", fmt.Errorf("gh pr view %s: %w", branch, err)
	}
	var pr struct {
		State  string `json:"state"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return 0, "", fmt.Errorf("parse gh pr view output: %w", err)
	}
	return pr.Number, pr.State, nil
}

// closePRAndReap closes out a merged branch's GitHub PR and deletes the
// branch from origin (mg-f18c). It runs after every successful merge.
//
// The refinery rebases a branch onto the target before merging, so for any
// 2nd-or-later MR in a batch the landed SHA differs from the PR's head SHA
// and GitHub cannot auto-detect the merge — the PR dangles OPEN even though
// the content shipped (gh drellem2/pogo #81). Closing it explicitly, with a
// comment pointing at the SHA it actually landed as, closes that loop.
//
// The paths where GitHub *did* auto-detect (a first/only MR that merged
// verbatim, or a pr_mode push-back that realigned the head) are no-ops here:
// the PR reads MERGED/CLOSED already, so only the branch reap runs.
//
// Every failure is soft. The merge has already landed on origin by the time
// this runs; a gh outage or a lost branch-delete race must never turn a
// successful merge into a failed one, so problems are logged and skipped.
func (r *Refinery) closePRAndReap(wtDir string, mr *MergeRequest, sha string) {
	num, state, err := lookupPR(wtDir, mr.Branch)
	if err != nil {
		log.Printf("refinery: MR %s step=pr-close skipped: gh lookup failed (%v) — a PR for %s (if any) may be left open", mr.ID, err, mr.Branch)
		return
	}
	if num == 0 {
		log.Printf("refinery: MR %s step=pr-close skipped: no PR for branch %s", mr.ID, mr.Branch)
		return
	}

	if strings.EqualFold(state, "OPEN") {
		log.Printf("refinery: MR %s step=pr-close branch=%s pr=#%d", mr.ID, mr.Branch, num)
		if out, cerr := ghClosePR(wtDir, num, prClosedComment(mr, sha)); cerr != nil {
			log.Printf("refinery: MR %s step=pr-close failed (%v: %s) — PR #%d left open; merge already landed", mr.ID, cerr, out, num)
		}
	} else {
		log.Printf("refinery: MR %s step=pr-close skipped: PR #%d already %s (GitHub auto-detected the merge)", mr.ID, num, state)
	}

	// Reap the remote branch so no stale head lingers behind the closed PR.
	// Deleting the head branch is what GitHub's own auto-delete does after a
	// merge; do it after the close so the delete can't race the close.
	log.Printf("refinery: MR %s step=branch-reap branch=%s", mr.ID, mr.Branch)
	if out, gerr := gitCmdOutput(wtDir, "push", "origin", "--delete", mr.Branch); gerr != nil {
		log.Printf("refinery: MR %s step=branch-reap failed (%s) — origin/%s may linger; merge already landed", mr.ID, strings.TrimSpace(out), mr.Branch)
	}
}

// prClosedComment is the comment left on a PR the refinery closes out, so a
// human reading the PR can find the commit its content actually landed as.
func prClosedComment(mr *MergeRequest, sha string) string {
	landed := strings.TrimSpace(sha)
	if landed == "" {
		landed = "the current " + mr.TargetRef + " tip"
	}
	return fmt.Sprintf("Merged as %s on `%s` by the pogo refinery (MR %s).\n\n"+
		"The refinery rebases each branch onto `%s` before merging, so the landed commits "+
		"have different SHAs than this PR's head and GitHub could not auto-detect the merge. "+
		"Closing explicitly — the content shipped.",
		landed, mr.TargetRef, mr.ID, mr.TargetRef)
}

// ghClosePR closes PR number with a comment. Bounded by prCloseTimeout so a
// hung gh call can't stall the merge pipeline after the merge has landed.
func ghClosePR(wtDir string, number int, comment string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), prCloseTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "close", strconv.Itoa(number), "--comment", comment)
	cmd.Dir = wtDir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// gateStage maps quality gate commands to a refinery_merge_failed stage value.
// Returns "build" for build-* commands, "test" as the conservative default.
func gateStage(gates []string) string {
	if len(gates) == 0 {
		return "test"
	}
	last := strings.ToLower(strings.TrimSpace(gates[len(gates)-1]))
	last = strings.TrimPrefix(last, "./")
	if strings.HasPrefix(last, "build") {
		return "build"
	}
	return "test"
}

// retryableError wraps errors from merge/push failures that can be retried
// with a fresh rebase (e.g. target moved because another polecat merged first).
type retryableError struct {
	err error
}

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func isRetryable(err error) bool {
	var re *retryableError
	return errors.As(err, &re)
}

// ensureWorktree creates or validates a worktree for the given repo.
// Uses a clone (not git-worktree) so the refinery is fully independent.
// The clone's origin remote is set to the original repo's remote URL
// so that push/fetch operations go to the actual remote (e.g. GitHub),
// not the local filesystem path.
func (r *Refinery) ensureWorktree(repoPath string) (string, error) {
	// Use the repo basename as the worktree directory name
	repoName := filepath.Base(repoPath)
	wtDir := filepath.Join(r.cfg.WorktreeDir, repoName)

	if _, err := os.Stat(filepath.Join(wtDir, ".git")); err == nil {
		// If an older clone was made without --no-local, it may have git
		// alternates linking back to the source repo. This leaks worktree
		// metadata and causes "already checked out" errors when the source
		// has linked polecat worktrees. Re-clone to fix.
		if hasAlternates(wtDir) {
			log.Printf("refinery: worktree %s has alternates (stale clone), re-cloning", wtDir)
			if err := os.RemoveAll(wtDir); err != nil {
				return "", fmt.Errorf("remove stale clone: %w", err)
			}
			// Fall through to fresh clone below
		} else {
			// Already cloned — ensure origin points at the real remote
			if err := fixRemoteURL(wtDir, repoPath); err != nil {
				return "", fmt.Errorf("fix remote url: %w", err)
			}
			return wtDir, nil
		}
	}

	// Clone the repo into the worktree dir.
	// Use --no-local to prevent git from sharing the object store via
	// alternates, which can leak worktree metadata from the source repo
	// and cause "already checked out" errors.
	if err := os.MkdirAll(r.cfg.WorktreeDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	cmd := exec.Command("git", "clone", "--no-local", repoPath, wtDir)
	cloneOutput, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("clone: %s: %w", strings.TrimSpace(string(cloneOutput)), err)
	}

	// Fix origin to point at the actual remote, not the local path
	if err := fixRemoteURL(wtDir, repoPath); err != nil {
		return "", fmt.Errorf("fix remote url after clone: %w", err)
	}

	return wtDir, nil
}

// fixRemoteURL ensures the worktree clone's origin points at the real remote
// (e.g. GitHub) rather than a local filesystem path. If the source repo has
// an origin remote configured, that URL is propagated to the worktree clone.
//
// If the source repo has no usable remote and is not a bare repo, an error is
// returned — processing with a local dev repo as origin can cause "already
// checked out" failures when the dev repo has linked polecat worktrees.
func fixRemoteURL(wtDir, repoPath string) error {
	// Try known remote names in priority order.
	for _, remote := range []string{"origin", "upstream"} {
		cmd := exec.Command("git", "-C", repoPath, "remote", "get-url", remote)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		remoteURL := strings.TrimSpace(string(out))
		if remoteURL == "" || remoteURL == repoPath {
			continue
		}
		// Found a usable remote URL — propagate it to the clone.
		if output, err := gitCmdOutput(wtDir, "remote", "set-url", "origin", remoteURL); err != nil {
			return fmt.Errorf("%s: %w", output, err)
		}
		return nil
	}

	// No usable remote found. If the source repo is a bare repo (typical in
	// tests), the clone's origin already points at the right place.
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--is-bare-repository")
	out, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(out)) == "true" {
		return nil
	}

	return fmt.Errorf(
		"source repo %s has no remote configured; "+
			"refinery cannot process MRs from repos without a push remote "+
			"(local paths cause 'already checked out' errors with linked worktrees)",
		repoPath,
	)
}

// runQualityGates runs the configured quality gates for the repo.
// Checks for per-repo .pogo/refinery.toml first, then falls back to defaults.
// Returns combined output, the slice of gates run up to and including the
// failing one (or all of them on success), and any error.
//
// Every gate runs under a gateWatch, which emits a heartbeat on a bounded
// interval for as long as the gate runs (mg-8595). This is the step that can
// take tens of minutes; before the watch existed it logged on entry and not
// again until it produced a result, which is indistinguishable from a hang
// from outside for the whole run.
//
// ctx cancellation kills the running gate, which is how an in-flight merge
// request becomes cancellable. mr may be nil in unit tests; the watch then
// logs without persisting.
func (r *Refinery) runQualityGates(ctx context.Context, wtDir, repoPath string, mr *MergeRequest) (string, []string, error) {
	cfg := r.loadConfig(wtDir, repoPath)
	gates := cfg.Gates
	var note string
	if len(gates) == 0 {
		gates, note = defaultGates(wtDir)
	}
	if len(gates) == 0 {
		// No gates configured — pass by default
		return "(no quality gates configured)", nil, nil
	}
	timeout := cfg.gateTimeout()

	var allOutput strings.Builder
	// A gate the defaults dropped is said out loud in the merge's own output.
	// A shorter gate list that nothing explains reads as coverage quietly going
	// missing, which is exactly what it must not be mistaken for.
	if note != "" {
		allOutput.WriteString(note + "\n")
	}
	var ran []string
	for i, gate := range gates {
		allOutput.WriteString(fmt.Sprintf("=== Running: %s ===\n", gate))
		ran = append(ran, gate)

		var deadline time.Time
		if timeout > 0 {
			deadline = time.Now().Add(timeout)
		}
		watch := startGateWatch(r, mr, "quality-gates", gate, i+1, len(gates), deadline)
		output, err := runGate(ctx, wtDir, gate, timeout, watch)
		watch.finish()

		allOutput.WriteString(output)
		allOutput.WriteString("\n")
		if err != nil {
			allOutput.WriteString(fmt.Sprintf("FAILED: %v\n", err))
			// The HOST ran out of a resource while the gate ran. That is not a
			// verdict on the branch and the package and test names below are not
			// findings, so this is judged BEFORE the summary that would print
			// them (mg-b41f, gatehostresource.go).
			//
			// Judged here, on `output`, because this is the last place the FULL
			// gate output exists: the copy stored on the merge request is capped
			// to 8 KiB with its middle elided, and an incident whose ENOSPC lines
			// fell in that middle would read back as a clean build failure.
			if hre := newHostResourceError(gate, output, wtDir, err); hre != nil {
				return allOutput.String(), ran, hre
			}
			// A gate KILLED BY A SIGNAL is returned as-is, ahead of the summary
			// below, for the same reason the host-resource error is (mg-0502).
			// The summary names the packages and tests the output was in the
			// middle of when the signal landed, and on a kill those are not
			// findings — they are whatever the gate happened to be doing. A
			// headline reading `./build.sh failed [cmd/pogo, +46 more]` in front
			// of "this is not a verdict" points the reader at the packages,
			// which is the accusation this ticket exists to remove.
			var sigErr *gateSignalError
			if errors.As(err, &sigErr) {
				return allOutput.String(), ran, sigErr
			}
			// The GATE could not reach the network. Judged here for the same two
			// reasons the host-resource error is (mg-67c9, gatenetwork.go): this
			// is the last place the FULL output exists, and the summary below
			// would otherwise name the package the toolchain failed to fetch FOR
			// — `[internal/agent]`, on the 2026-08-14 specimen — which reads as a
			// finding against that package and is not one.
			//
			// After the two kills above on purpose: a gate that was killed says
			// nothing about whether the branch caused the hang, and this is the
			// carve-out that retries.
			if gne := newGateNetworkError(gate, output, err); gne != nil {
				return allOutput.String(), ran, gne
			}
			// The gate's OWN SETUP did not stand up. Judged here for the same
			// two reasons as the three above (mg-15bb, gatesetup.go): this is
			// the last place the FULL output exists, and the summary below would
			// otherwise be the ONLY place the distinction survived — it has said
			// "test setup failed, not the branch" since mg-3412 while the class
			// beside it said DEFECT, "a fix is warranted", in the same record.
			//
			// LAST of the four, because the three above each name something more
			// specific and two of them do not retry. See classifyFailure for
			// why that ordering is the safe direction.
			if gse := newGateSetupError(gate, output, err); gse != nil {
				return allOutput.String(), ran, gse
			}
			// Name what failed INSIDE the gate. `./build.sh failed: exit status 1`
			// is the sentence that travels — onto the MR, into `pogo refinery
			// show`, into what a polecat is told about its branch — and it names
			// neither the package nor the cause (mg-216c).
			if what := summarizeGateFailure(output); what != "" {
				return allOutput.String(), ran, fmt.Errorf("%s failed [%s]: %w", gate, what, err)
			}
			return allOutput.String(), ran, fmt.Errorf("%s failed: %w", gate, err)
		}
		allOutput.WriteString("PASSED\n")
	}

	return allOutput.String(), ran, nil
}

// loadGateConfig returns the quality gate commands to run.
// Priority: per-repo .pogo/refinery.toml > default build.sh
func (r *Refinery) loadGateConfig(wtDir, repoPath string) []string {
	cfg := r.loadConfig(wtDir, repoPath)
	if len(cfg.Gates) > 0 {
		return cfg.Gates
	}
	return defaultGateCommands(wtDir)
}

// defaultGateCommands returns the conventional gate scripts present in a
// worktree, used when no per-repo config names any.
func defaultGateCommands(wtDir string) []string {
	gates, _ := defaultGates(wtDir)
	return gates
}

// defaultGates returns the conventional gate scripts present in a worktree,
// plus a note naming any script it deliberately left out and why. The note is
// empty when nothing was omitted.
//
// Listing every conventional script is right when they are independent steps,
// and wrong when one calls the other: on a repo whose build.sh runs test.sh,
// gating on both ran the whole suite TWICE on the critical path of every merge
// (mg-da30). Measured from pogod's own gate heartbeats over 49 two-gate
// merges, the second run was 34% of all gate wall-clock — a median of 2m30s
// per merge, on the single slot every other merge queues behind.
//
// The fix is deliberately conditional on the nesting rather than a blanket
// "prefer ./build.sh". Of the seven repos on this fleet carrying both scripts,
// FIVE have a build.sh that only compiles (bridget, libdig, macguffin,
// pogo-sleepwake, rent-a-programmer-api) — dropping ./test.sh for those would
// not halve their gate, it would stop testing them. Only where build.sh is
// measured to invoke test.sh does the second gate add nothing.
//
// The detection's failure directions are asymmetric and it is written to fail
// the safe way: an unrecognised invocation form keeps both gates (the suite
// runs twice, as it did before), while a false positive would drop coverage.
// See buildScriptRunsTests.
func defaultGates(wtDir string) ([]string, string) {
	var defaults []string
	for _, script := range []string{"./build.sh", "./test.sh"} {
		if _, err := os.Stat(filepath.Join(wtDir, script)); err == nil {
			defaults = append(defaults, script)
		}
	}
	if len(defaults) == 2 && buildScriptRunsTests(wtDir) {
		return defaults[:1], "(omitting gate ./test.sh: ./build.sh runs it, and running it twice per merge tests nothing new)"
	}
	return defaults, ""
}

// testScriptInvocation matches an invocation of ./test.sh in a shell script:
// `./test.sh`, `bash test.sh`, `sh ./test.sh`. A bare `test.sh` with no path
// and no interpreter is NOT matched — it does not execute anything, so a line
// like `echo "run test.sh yourself"` must not read as an invocation.
var testScriptInvocation = regexp.MustCompile(`(^|[\s;&|(])(\./test\.sh|(ba)?sh\s+(\./)?test\.sh)($|[\s;&|)])`)

// buildScriptRunsTests reports whether the worktree's build.sh invokes
// ./test.sh, which is what makes gating on both a duplicate run.
//
// This is a textual check, and the two ways it can be wrong are not equally
// costly. A missed invocation leaves both gates listed — the status quo, a
// suite run twice. A phantom invocation would remove a real gate, so the match
// requires an executable form and everything from the first `#` on a line is
// discarded before matching: over-stripping only ever loses a match, and
// losing a match is the safe direction.
//
// Conditional invocations count. This repo's build.sh runs ./test.sh inside
// `if [ "$skip_tests" = false ]`, and the gate invokes `./build.sh` with no
// arguments, so the tests run. Trying to decide statically whether a branch is
// taken would reject that — the real question is whether the two commands
// overlap at all, and a script that mentions ./test.sh at all is a script the
// gate cannot assume is independent of it.
func buildScriptRunsTests(wtDir string) bool {
	data, err := os.ReadFile(filepath.Join(wtDir, "build.sh"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if testScriptInvocation.MatchString(line) {
			return true
		}
	}
	return false
}

// loadConfig returns the merged refinery config for a repo. Worktree
// values win on a per-field basis, with origin filling in fields the
// worktree does not set. Used for both per-merge knobs (max_attempts,
// skip_on_retry) and the gate/deploy lookups.
func (r *Refinery) loadConfig(wtDir, repoPath string) refineryConfig {
	wt := parseRefineryConfig(filepath.Join(wtDir, ".pogo", "refinery.toml"))
	orig := parseRefineryConfig(filepath.Join(repoPath, ".pogo", "refinery.toml"))
	if len(wt.Gates) == 0 {
		wt.Gates = orig.Gates
	}
	if wt.DeployCommand == "" {
		wt.DeployCommand = orig.DeployCommand
	}
	if wt.MaxAttempts == 0 {
		wt.MaxAttempts = orig.MaxAttempts
	}
	if !wt.SkipGatesOnRetry {
		wt.SkipGatesOnRetry = orig.SkipGatesOnRetry
	}
	if !wt.PRMode {
		wt.PRMode = orig.PRMode
	}
	if !wt.GateTimeoutSet {
		wt.GateTimeout, wt.GateTimeoutSet = orig.GateTimeout, orig.GateTimeoutSet
	}
	return wt
}

// refineryConfig holds parsed values from a .pogo/refinery.toml file.
type refineryConfig struct {
	Gates            []string
	DeployCommand    string
	MaxAttempts      int  // [gates] max_attempts — 0 means use defaultMaxAttempts
	SkipGatesOnRetry bool // [gates] skip_on_retry — bypass gates on attempt > 1
	PRMode           bool // pr_mode — push rebased branch back so open PRs read merged
	// GateTimeout is the [gates] timeout bound on a single gate run; 0 means
	// no bound. GateTimeoutSet distinguishes "configured as 0" (deliberately
	// unbounded) from "not configured" (use defaultGateTimeout) — without it
	// the two are the same zero value and an omitted key would remove the
	// bound instead of taking the default.
	GateTimeout    time.Duration
	GateTimeoutSet bool
}

// parseRefineryToml reads a .pogo/refinery.toml and extracts gate commands.
// Format:
//
//	[gates]
//	commands = ["./build.sh", "./test.sh"]
//
// Or simpler:
//
//	quality_gate = "./build.sh"
func parseRefineryToml(path string) []string {
	return parseRefineryConfig(path).Gates
}

// parseRefineryConfig reads a .pogo/refinery.toml and extracts all known
// configuration. Recognized sections:
//
//	[gates]
//	commands       = ["./build.sh", "./test.sh"]
//	max_attempts   = 7      # ff-only retry budget; default 7 if omitted
//	skip_on_retry  = true   # bypass gates on attempts > 1 (race recovery)
//	pr_mode        = true   # push rebased branch back so open PRs read merged
//
//	[deploy]
//	command = "./deploy.sh"
//
// Or simpler top-level keys:
//
//	quality_gate = "./build.sh"
//
// Returns a zero-value config when the file is missing or unreadable;
// missing sections are not an error.
func parseRefineryConfig(path string) refineryConfig {
	var cfg refineryConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}

	section := ""

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") {
			section = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"")

		switch {
		case key == "quality_gate":
			cfg.Gates = append(cfg.Gates, val)
		case section == "gates" && key == "commands":
			// Parse simple array: ["./build.sh", "./test.sh"]
			arr := strings.Trim(val, "[]")
			for _, cmd := range strings.Split(arr, ",") {
				cmd = strings.TrimSpace(cmd)
				cmd = strings.Trim(cmd, "\"")
				if cmd != "" {
					cfg.Gates = append(cfg.Gates, cmd)
				}
			}
		case section == "gates" && key == "max_attempts":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.MaxAttempts = n
			}
		case section == "gates" && key == "skip_on_retry":
			cfg.SkipGatesOnRetry = parseTomlBool(val)
		case section == "gates" && key == "timeout":
			// An unreadable value leaves GateTimeoutSet false, so the default
			// bound stays in force. A typo must not silently remove the bound.
			if d, ok := parseGateTimeout(val); ok {
				cfg.GateTimeout = d
				cfg.GateTimeoutSet = true
			} else {
				log.Printf("refinery: ignoring unreadable [gates] timeout %q in %s — keeping the %s default", val, path, defaultGateTimeout)
			}
		case key == "pr_mode":
			// Accepted top-level or under [gates] — the ticket and design
			// doc cite both spellings (mg-b828).
			cfg.PRMode = parseTomlBool(val)
		case section == "deploy" && key == "command":
			cfg.DeployCommand = val
		}
	}

	return cfg
}

// parseTomlBool parses a TOML-ish bool from a string. Accepts true/false
// (case-insensitive) and 1/0. Anything else is treated as false.
func parseTomlBool(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// DeployCommand returns the configured post-merge deploy command for a repo,
// read from <repoPath>/.pogo/refinery.toml. Returns empty string when no
// [deploy] section is present or the file is missing — not an error, since
// most repos won't have a deploy hook.
func (r *Refinery) DeployCommand(repoPath string) string {
	return parseRefineryConfig(filepath.Join(repoPath, ".pogo", "refinery.toml")).DeployCommand
}

// runGate executes a single quality gate command in the worktree directory.

// hasAlternates reports whether the git repo at dir has an alternates file,
// which indicates the clone shares its object store with another repo via
// hardlinks. This happens when git clone is used without --no-local on a
// local path. Shared object stores leak worktree metadata from the source
// repo, causing "already checked out" errors.
func hasAlternates(dir string) bool {
	// Alternates file lives at .git/objects/info/alternates for a regular repo.
	altPath := filepath.Join(dir, ".git", "objects", "info", "alternates")
	info, err := os.Stat(altPath)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

// refineryCommitterName / refineryCommitterEmail is the identity git uses to
// author and commit the commits the refinery creates during a rebase replay
// (attemptMerge's `git rebase origin/<target>`). The refinery's worktree
// clones (created by ensureWorktree) have no local user.name/user.email, and
// pogod runs under launchd/systemd — often with no global/system git config
// and a username git can't auto-derive into a valid ident
// ("fatal: empty ident name (for <runner@host>) not allowed"). Supplying an
// explicit identity via the environment makes the refinery self-contained:
// rebase replays no longer depend on ambient git config (ia-1428, gh #7).
const (
	refineryCommitterName  = "pogo refinery"
	refineryCommitterEmail = "refinery@pogo.local"
)

// gitIdentityEnv returns GIT_AUTHOR_*/GIT_COMMITTER_* environment entries that
// fall back to the refinery identity for any that aren't already set in the
// process environment. A pre-existing non-empty value (a developer's shell
// identity, or a test's seeded identity) takes precedence; an unset or empty
// value gets the refinery default. Appended after os.Environ(), these entries
// win over any empty same-key values inherited from the environment (Go's
// exec uses the last value for duplicate keys).
func gitIdentityEnv() []string {
	defaults := map[string]string{
		"GIT_AUTHOR_NAME":     refineryCommitterName,
		"GIT_AUTHOR_EMAIL":    refineryCommitterEmail,
		"GIT_COMMITTER_NAME":  refineryCommitterName,
		"GIT_COMMITTER_EMAIL": refineryCommitterEmail,
	}
	var env []string
	for k, def := range defaults {
		if os.Getenv(k) == "" {
			env = append(env, k+"="+def)
		}
	}
	return env
}

// gitCmdOutput runs a git command in the given directory and captures
// combined stdout/stderr output. Returns the output and any error.
// This ensures git error messages (e.g. push rejection reasons) are
// available for logging and stored in MergeRequest.Error, rather than
// being lost to pogod's stdout/stderr.
func gitCmdOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// pogod runs under launchd/systemd with no TTY. Without disabling
	// interactive prompts, an HTTPS remote with no credentials makes git
	// hang forever waiting for a username on stdin. Force prompts off so
	// auth failures fail fast and we can detect them via isAuthFailure.
	//
	// Also supply a committer/author identity so rebase replays don't fail
	// with "Committer identity unknown" when no ambient git config is
	// available (ia-1428, gh #7).
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, gitIdentityEnv()...)
	output, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(output))
	if err != nil {
		if note, benign := benignGitOutcome(out); benign {
			log.Printf("refinery: git %v: %s (expected outcome)", args, note)
		} else {
			log.Printf("refinery: git %v failed: %s: %v", args, out, err)
		}
	}
	return out, err
}

// benignGitOutcomes maps a git failure's COMPLETE output text to a plain-words
// description of the expected outcome it represents. The key is matched by
// exact string equality against gitCmdOutput's trimmed combined output — not
// against the command, and not against a prefix.
//
// That distinction is the whole point, not a detail (mg-5d3f). The refinery
// calls `git rebase --abort` unconditionally to clear crash debris, so the
// abort fails on every clean clone: 245 of these landed in one 50,603-line log,
// all of them reading `git [rebase --abort] failed`, none of them an error.
// Benign lines at error level teach a reader to skip error lines.
//
// Suppressing by COMMAND would silence the whole class, so a genuinely new
// `rebase --abort` failure — a concurrent git process holding .git/index.lock
// is a real one the refinery can hit, since gitgc and agents touch the same
// worktrees — would be swallowed on the day it first appeared. Matching the
// full text silences only the outcome measured to be benign; any other wording
// fails the equality test and still logs as a failure, which is where a new
// variant belongs. The safe form self-invalidates when the world changes.
//
// Add an entry only for an outcome measured to be benign in every occurrence.
var benignGitOutcomes = map[string]string{
	"fatal: no rebase in progress": "no rebase in progress, nothing to abort",
}

// benignGitOutcome reports whether git output is a known expected outcome
// rather than a failure, returning the description to log in its place.
func benignGitOutcome(out string) (string, bool) {
	note, ok := benignGitOutcomes[out]
	return note, ok
}

// authFailurePatterns match git stderr emitted when a remote requires
// credentials that pogod can't supply (no TTY, no askpass, no helper).
// Patterns are matched case-insensitively against combined stdout/stderr.
var authFailurePatterns = []string{
	"could not read username",
	"could not read password",
	"authentication failed",
	"invalid username or password",
	"terminal prompts disabled",
	"support for password authentication was removed",
}

// isAuthFailure reports whether git output indicates a credential or
// authentication failure against the remote. Such failures don't recover
// on retry — they need a user-side fix (SSH remote, credential helper,
// or GIT_ASKPASS exported into pogod's env).
func isAuthFailure(output string) bool {
	s := strings.ToLower(output)
	for _, pat := range authFailurePatterns {
		if strings.Contains(s, pat) {
			return true
		}
	}
	return false
}

// formatPushAuthError wraps a raw git-stderr auth failure with actionable
// next-steps text. The actionable summary is at the top so it survives
// truncation; the raw git output is preserved verbatim at the bottom for
// debugging.
func formatPushAuthError(gitOutput string) error {
	return fmt.Errorf(
		"refinery push failed: git could not authenticate against the HTTPS remote.\n"+
			"pogod runs under launchd / systemd and does not see your interactive shell credentials.\n"+
			"Fix one of these:\n"+
			"  a) Switch the remote to SSH:\n"+
			"       git -C <repo> remote set-url origin git@github.com:<owner>/<repo>.git\n"+
			"  b) Configure git's credential helper for non-interactive use:\n"+
			"       git config --global credential.helper osxkeychain   # macOS\n"+
			"       git config --global credential.helper store         # Linux/BSD\n"+
			"       gh auth setup-git\n"+
			"  c) Export GIT_ASKPASS in pogod's environment to a script that emits your token on stdin.\n"+
			"\n"+
			"git output:\n%s",
		gitOutput,
	)
}
