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
//  3. Run quality gates on rebased code
//  4. Fast-forward merge to target ref
//  5. Push
//  6. Fast-forward the source checkout's target branch (iff clean and on
//     that branch — see fastForwardSourceCheckout)
//  7. Run the per-repo deploy hook (if configured) against the just-merged commit
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
// Returns the captured gate output, a deploy error string (empty when no
// deploy ran or it succeeded — populates MergeRequest.DeployError), whether
// the branch was found already merged (see the guard below — populates
// MergeRequest.AlreadyMerged), and the merge error (nil on success). Deploy
// failure does NOT cause processMerge to return an error: the merge has
// already landed remotely.
func (r *Refinery) processMerge(mr *MergeRequest) (string, string, bool, error) {
	wtDir, err := r.ensureWorktree(mr.RepoPath)
	if err != nil {
		return "", "", false, fmt.Errorf("worktree setup: %w", err)
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
		return gateOutput, "", true, nil
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
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			log.Printf("refinery: MR %s step=retry attempt=%d/%d", mr.ID, attempt, maxAttempts)
		}

		emitMergeAttempted(mr, attempt)

		skipGates := skipGatesOnRetry && attempt > 1
		output, stage, sha, attemptErr := r.attemptMerge(wtDir, mr, attempt, skipGates, cfg.PRMode)
		gateOutput = output
		if attemptErr == nil {
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
			return gateOutput, deployErr, false, nil
		}

		retryRemaining := attempt < maxAttempts && isRetryable(attemptErr)
		emitMergeFailed(mr, attempt, stage, attemptErr, !retryRemaining, gateOutput)

		if retryRemaining {
			log.Printf("refinery: MR %s attempt %d failed (will retry): %v", mr.ID, attempt, attemptErr)
			continue
		}
		return gateOutput, "", false, attemptErr
	}
	finalErr := fmt.Errorf("merge failed after %d attempts", maxAttempts)
	emitMergeFailed(mr, maxAttempts, "unknown", finalErr, true, gateOutput)
	return gateOutput, "", false, finalErr
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
func (r *Refinery) attemptMerge(wtDir string, mr *MergeRequest, attempt int, skipGates, prMode bool) (output string, stage string, sha string, err error) {
	// Fetch latest from origin
	log.Printf("refinery: MR %s step=fetch branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
	if out, gerr := gitCmdOutput(wtDir, "fetch", "origin"); gerr != nil {
		return "", "fetch", "", fmt.Errorf("fetch: %s: %w", out, gerr)
	}

	// Checkout the branch fresh from origin
	log.Printf("refinery: MR %s step=checkout-branch branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
	if out, gerr := gitCmdOutput(wtDir, "checkout", "-B", mr.Branch, "origin/"+mr.Branch); gerr != nil {
		return "", "fetch", "", fmt.Errorf("checkout branch: %s: %w", out, gerr)
	}

	// Rebase onto latest target so the branch is a direct descendant of main.
	// Polecat branches fork from main at spawn time and may be behind by the
	// time they reach the refinery.
	log.Printf("refinery: MR %s step=rebase target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "rebase", "origin/"+mr.TargetRef); gerr != nil {
		// Abort the failed rebase to leave worktree in a clean state
		gitCmdOutput(wtDir, "rebase", "--abort")
		rebaseErr := fmt.Errorf("rebase onto %s: %s: %w", mr.TargetRef, out, gerr)
		// "invalid upstream" can be transient — e.g. the target branch
		// hasn't been fetched yet or the ref is missing from the clone.
		// Treat it as retryable so a fresh fetch gets another chance.
		if strings.Contains(out, "invalid upstream") {
			return "", "rebase", "", &retryableError{rebaseErr}
		}
		return "", "rebase", "", rebaseErr
	}

	// Run quality gates (on the rebased branch — tests what will actually
	// land). On retries with skip_on_retry set, bypass: gates already
	// passed on attempt 1 over near-identical code; the only change is
	// the version-bump commit fetched from main.
	var gateOutput string
	if skipGates {
		log.Printf("refinery: MR %s step=quality-gates attempt=%d skipped (skip_on_retry=true)", mr.ID, attempt)
		gateOutput = "(quality gates skipped on retry — skip_on_retry=true)"
	} else {
		log.Printf("refinery: MR %s step=quality-gates attempt=%d", mr.ID, attempt)
		out, gates, qerr := r.runQualityGates(wtDir, mr.RepoPath)
		gateOutput = out
		if qerr != nil {
			return gateOutput, gateStage(gates), "", fmt.Errorf("quality gate: %w", qerr)
		}
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
		return gateOutput, "fetch", "", &retryableError{fmt.Errorf("fetch target %s: %s: %w", mr.TargetRef, out, gerr)}
	}
	log.Printf("refinery: MR %s step=reset-target target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "checkout", "-B", mr.TargetRef, "origin/"+mr.TargetRef); gerr != nil {
		return gateOutput, "fetch", "", &retryableError{fmt.Errorf("reset target %s to origin: %s: %w", mr.TargetRef, out, gerr)}
	}

	// Fast-forward merge — guaranteed to work if target hasn't moved since fetch
	log.Printf("refinery: MR %s step=merge branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
	if out, gerr := gitCmdOutput(wtDir, "merge", "--ff-only", mr.Branch); gerr != nil {
		return gateOutput, "rebase", "", &retryableError{fmt.Errorf("merge (ff-only): %s: %w", out, gerr)}
	}

	// Push to origin
	log.Printf("refinery: MR %s step=push target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "push", "origin", mr.TargetRef); gerr != nil {
		// Auth failures don't recover on retry — surface the actionable
		// error immediately rather than burning attempts.
		if isAuthFailure(out) {
			return gateOutput, "push", "", formatPushAuthError(out)
		}
		return gateOutput, "push", "", &retryableError{fmt.Errorf("push: %s: %w", out, gerr)}
	}

	// Capture the merge commit SHA (HEAD on target after fast-forward).
	// Best-effort: if rev-parse fails, return empty SHA — the merge already
	// pushed successfully.
	headSHA, _ := gitCmdOutput(wtDir, "rev-parse", "HEAD")

	return gateOutput, "push", headSHA, nil
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
func (r *Refinery) runQualityGates(wtDir, repoPath string) (string, []string, error) {
	gates := r.loadGateConfig(wtDir, repoPath)
	if len(gates) == 0 {
		// No gates configured — pass by default
		return "(no quality gates configured)", nil, nil
	}

	var allOutput strings.Builder
	var ran []string
	for _, gate := range gates {
		allOutput.WriteString(fmt.Sprintf("=== Running: %s ===\n", gate))
		ran = append(ran, gate)
		output, err := runGate(wtDir, gate)
		allOutput.WriteString(output)
		allOutput.WriteString("\n")
		if err != nil {
			allOutput.WriteString(fmt.Sprintf("FAILED: %v\n", err))
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
	// Fall back to common scripts
	var defaults []string
	for _, script := range []string{"./build.sh", "./test.sh"} {
		if _, err := os.Stat(filepath.Join(wtDir, script)); err == nil {
			defaults = append(defaults, script)
		}
	}
	return defaults
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
	return wt
}

// refineryConfig holds parsed values from a .pogo/refinery.toml file.
type refineryConfig struct {
	Gates            []string
	DeployCommand    string
	MaxAttempts      int  // [gates] max_attempts — 0 means use defaultMaxAttempts
	SkipGatesOnRetry bool // [gates] skip_on_retry — bypass gates on attempt > 1
	PRMode           bool // pr_mode — push rebased branch back so open PRs read merged
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
func runGate(wtDir, command string) (string, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = wtDir
	cmd.Env = append(os.Environ(), "POGO_REFINERY=1")

	output, err := cmd.CombinedOutput()
	return string(output), err
}

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
		log.Printf("refinery: git %v failed: %s: %v", args, out, err)
	}
	return out, err
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
