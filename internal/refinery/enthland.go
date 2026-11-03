package refinery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// Landing an enth MR (docs/design/enth-vcs.md §6.1–§6.4; slice 7, mg-1fe2b).
//
// One attempt, in order:
//
//  0. REPLAY. If the MR carries an EnthPending land — issued by an earlier
//     attempt or an earlier pogod and never seen answered — re-issue it with
//     its ORIGINAL request id before anything else. enthd replays the
//     recorded answer if it landed (recorded as merged); stale_tip or
//     result_mismatch say it did not (cleared, and the attempt goes on). Any
//     other answer leaves the outcome unknown, so the attempt fails without
//     re-planning: a re-plan under a new request id would land the same
//     Changes a second time (measured: enth does not refuse that).
//  1. STATE. The submitted Changes' land records (E3). All landed is the
//     already-merged rule (§6.3): merged as a no-op only when the refinery's
//     own history holds a merged MR for the same item whose land record lists
//     every submitted id; otherwise a defect — nothing would be delivered.
//     Some landed is a defect too: pogo will not land part of a stack twice.
//  2. DRY RUN + CHECKOUT (vcs.Lander.Prepare): T and N, and a fresh
//     whole-repo checkout of N as the gate dir. A conflict is a defect.
//  3. CLOSING-REF GATE on the submitted Changes' messages (R12). Not skipped
//     by skip_on_retry, as on git.
//  4. GATES in the checkout of N. A passed gate is reused while N is the
//     same (§6.4: N is the key, enth has no tree id); skip_on_retry keeps its
//     meaning and its cost.
//  5. LAND (vcs.Lander.Land): --expect-tip T --expect-new N --request-id
//     <MR>/<attempt>, persisted as EnthPending BEFORE it is sent. stale_tip
//     and result_mismatch are contention and count against max_attempts.
//
// The git pipeline is untouched: processMerge hands an MR with
// EnthSubmitted here before any git step, and returns.

// EnthPendingLand is a land an enth MR has issued and not yet seen answered.
type EnthPendingLand struct {
	RequestID string    `json:"request_id"`
	Target    string    `json:"target"`
	Tip       string    `json:"tip"`
	New       string    `json:"new"`
	Changes   []string  `json:"changes"`
	Attempt   int       `json:"attempt"`
	IssuedAt  time.Time `json:"issued_at"`
}

// The stages an enth attempt records. "rebase", "closing-ref-check", the gate
// stages and "push" keep their git names, so readers of AttemptFailure.Stage
// need no enth table.
const (
	stageEnthReplay = "enth-replay"
	stageEnthState  = "enth-state"
	stageEnthConfig = "enth-config"
)

// enthLandTimeout bounds one enth call (a dry run, a checkout, a land). The
// gates are bounded by their own timeout, not this.
const enthLandTimeout = 10 * time.Minute

// enthRefusal is a refusal pogo itself makes about an enth MR, carrying the
// class and the reason the attempt record should say.
type enthRefusal struct {
	class  FailureClass
	signal string
	reason string
	err    error
}

func (e *enthRefusal) Error() string { return e.err.Error() }
func (e *enthRefusal) Unwrap() error { return e.err }

// enthGateDir is the refinery's gate checkout for an enth repo: one per repo,
// beside the git clones, re-made from scratch on every attempt.
func (r *Refinery) enthGateDir(repoPath string) string {
	return filepath.Join(r.cfg.WorktreeDir, filepath.Base(repoPath)+".enth-gate")
}

// processEnthMerge runs the enth land pipeline for one MR. Its shape is
// processMerge's: the same retry budgets (retryPolicy), the same attempt
// records, events and cancellation.
func (r *Refinery) processEnthMerge(mr *MergeRequest) (mergeResult, error) {
	e, ok := enthRepoFor(mr.RepoPath)
	if !ok {
		return r.failEnthOnce(mr, stageEnthConfig, &enthRefusal{class: ClassInfrastructure, signal: "not an enth repo",
			reason: "not retryable: the MR was submitted as enth work, but the project record no longer says enth — the land cannot be placed; a person must decide",
			err:    fmt.Errorf("MR %s carries enth Changes but %s is not recorded as an enth repo (pogo project set-vcs): nothing was landed", mr.ID, mr.RepoPath)})
	}
	b := enthWork(e)
	gateDir := r.enthGateDir(mr.RepoPath)

	// pr_mode is a GitHub PR flow; an enth repo has no PR to push back to
	// (§6.6, §11). Refused, not ignored.
	cfg := r.loadConfig(mr.RepoPath, mr.RepoPath)
	if cfg.PRMode {
		return r.failEnthOnce(mr, stageEnthConfig, prModeRefusal(mr.RepoPath))
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}

	policy := newRetryPolicy(maxAttempts)
	hardCap := policy.hardCap()
	hold := &gateHold{}
	var gatesReached bool
	var gateOutput string
	startTime := time.Now()

	for attempt := 1; attempt <= hardCap; attempt++ {
		if r.cancelWasRequested(mr) {
			emitMergeCancelled(mr, attempt, "before-attempt", gateOutput)
			return mergeResult{GateOutput: gateOutput}, cancelledMergeError("before-attempt")
		}
		if attempt > 1 {
			log.Printf("refinery: MR %s step=retry attempt=%d (cap=%d) %s", mr.ID, attempt, hardCap, policy.counts())
		}
		emitMergeAttempted(mr, attempt)

		skipGates := cfg.SkipGatesOnRetry && gatesReached
		res := r.attemptEnthLand(b, gateDir, mr, attempt, skipGates, hold)
		gatesReached = gatesReached || res.gatesReached
		gateOutput = res.output

		if res.prior != nil {
			log.Printf("refinery: MR %s item=%s: every submitted Change already landed under MR %s (entry %d) — resolving as merged without re-running gates (no-op)",
				mr.ID, mr.Item, res.prior.ID, res.prior.Enth.Entry)
			emitMerged(mr, 0, vcs.Enth, res.prior.Enth.New, 0, true)
			return mergeResult{GateOutput: gateOutput, AlreadyMerged: true, Enth: res.prior.Enth}, nil
		}
		if res.err == nil {
			if attempt > 1 {
				log.Printf("refinery: MR %s RECOVERED on attempt %d after %d failed attempt(s) (%s) — %s of backoff; the earlier failures are recorded on the merge request",
					mr.ID, attempt, attempt-1, r.attemptClassSummary(mr), policy.backoffSpent.Round(time.Second))
				r.recordRecovery(mr, attempt, policy.backoffSpent)
			}
			// merged_sha stays empty: a Change id is not a git sha (§7).
			emitMerged(mr, attempt, vcs.Enth, res.record.New, time.Since(startTime).Seconds(), false)
			log.Printf("refinery: MR %s landed through enth: %s entry %d %s -> %s (%d Change(s), request %s)",
				mr.ID, res.record.Ref, res.record.Entry, shortSHA(res.record.Old), shortSHA(res.record.New), len(res.record.Changes), res.record.RequestID)
			deployErr := r.runEnthDeploy(b, gateDir, mr, res.record)
			return mergeResult{GateOutput: gateOutput, DeployError: deployErr, Enth: res.record}, nil
		}

		if isCancelled(res.err) || r.cancelWasRequested(mr) {
			emitMergeCancelled(mr, attempt, res.stage, gateOutput)
			return mergeResult{GateOutput: gateOutput}, cancelledMergeError(res.stage)
		}

		fail, disp := r.describeEnthAttemptFailure(gateDir, e, attempt, res.stage, res.err)
		retry, backoff, reason, retryReason := policy.decide(disp)
		fail.Retried = retry
		fail.BackoffSeconds = backoff.Seconds()
		if retry {
			if strings.TrimSpace(retryReason) == "" {
				retryReason = fmt.Sprintf("RETRYING: the refinery classified this as %s, which establishes nothing about the submitted Changes that a re-run would repeat", fail.Class)
			}
			fail.RetriedReason = retryReason
		} else {
			if strings.TrimSpace(reason) == "" {
				reason = fmt.Sprintf("not retryable: the refinery classified this as %s and recorded no further reason — read the raw error below", fail.Class)
			}
			fail.NotRetriedReason = reason
		}
		r.recordAttemptFailure(mr, fail)
		emitMergeFailed(mr, attempt, res.stage, res.err, !retry, gateOutput, fail)
		log.Printf("refinery: MR %s %s", mr.ID, fail.Line())
		log.Printf("refinery: MR %s attempt %d raw error (verbatim): %v", mr.ID, attempt, res.err)

		if retry {
			if backoff > 0 {
				log.Printf("refinery: MR %s waiting %s before attempt %d (class=%s, budget %s of %s spent)",
					mr.ID, backoff, attempt+1, disp.Class, policy.backoffSpent.Round(time.Second), networkRetryBudget)
				if !r.sleepUnlessCancelled(mr, backoff) {
					emitMergeCancelled(mr, attempt, "retry-backoff", gateOutput)
					return mergeResult{GateOutput: gateOutput}, cancelledMergeError("retry-backoff")
				}
				policy.backoffSpent += backoff
			}
			continue
		}
		return mergeResult{GateOutput: gateOutput}, res.err
	}
	finalErr := fmt.Errorf("merge failed after %d attempts (hard cap)", hardCap)
	emitMergeFailed(mr, hardCap, "unknown", finalErr, true, gateOutput, AttemptFailure{
		Attempt: hardCap, Stage: "unknown", Time: time.Now(), RawError: finalErr.Error(),
		Class: ClassUnclassified, NotRetriedReason: "not retryable: the combined attempt cap was reached",
	})
	return mergeResult{GateOutput: gateOutput}, finalErr
}

// enthAttempt is what one attempt established.
type enthAttempt struct {
	output       string
	stage        string
	record       *vcs.LandRecord
	prior        *MergeRequest // the already-merged rule's prior MR
	gatesReached bool
	err          error
}

func enthCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), enthLandTimeout)
}

// attemptEnthLand runs one attempt; see the file comment for the steps.
func (r *Refinery) attemptEnthLand(b enthvcs.Backend, gateDir string, mr *MergeRequest, attempt int, skipGates bool, hold *gateHold) enthAttempt {
	// 0. A land issued earlier and never seen answered.
	if p := r.enthPending(mr); p != nil {
		log.Printf("refinery: MR %s step=enth-replay request=%s tip=%s new=%s (issued on attempt %d, outcome never seen)",
			mr.ID, p.RequestID, shortSHA(p.Tip), shortSHA(p.New), p.Attempt)
		rec, landed, err := r.replayEnthPending(b, mr, p)
		switch {
		case landed:
			return enthAttempt{record: rec, gatesReached: true, stage: "push",
				output: fmt.Sprintf("(enth: request %s, issued on attempt %d, had landed — replayed by enthd, not landed again; its gates ran on that attempt)", p.RequestID, p.Attempt)}
		case err != nil:
			return enthAttempt{stage: stageEnthReplay, err: fmt.Errorf("replay of enth land request %s (outcome unknown, so nothing is re-planned until it is known): %w", p.RequestID, err)}
		}
	}

	// 1. The already-merged rule.
	ctx, cancel := enthCtx()
	st, err := b.State(ctx, vcs.Repo{}, vcs.Submission{Item: mr.Item, Revs: mr.EnthSubmitted}, mr.TargetRef)
	cancel()
	if err != nil {
		return enthAttempt{stage: stageEnthState, err: fmt.Errorf("reading the land state of %s's Changes: %w", mr.Item, err)}
	}
	if st.Landed {
		if prior := r.priorEnthMergeCovering(mr); prior != nil {
			return enthAttempt{prior: prior, output: fmt.Sprintf("(every submitted Change already landed under MR %s — quality gates, land and deploy skipped)", prior.ID)}
		}
		return enthAttempt{stage: stageEmptyBranch, err: &enthRefusal{class: ClassDefect, signal: "enth already landed, no record",
			reason: "not retryable: " + verdictStages[stageEmptyBranch],
			err: fmt.Errorf("every submitted Change of %s is already landed, and the refinery has no merged MR for this item whose land record lists them — NOTHING WAS DELIVERED by this MR. "+
				"If the work is new it was never made into a Change (enth status in the worker's checkout; enth change), so make it and resubmit. If it really landed under an earlier MR the refinery no longer has on record, confirm with `enth query log --trailer %s=%s` and close the item by hand",
				mr.Item, vcs.TrailerItem, mr.Item)}}
	}
	if st.Equivalent > 0 {
		return enthAttempt{stage: stageEmptyBranch, err: &enthRefusal{class: ClassDefect, signal: "enth partly landed",
			reason: "not retryable: part of the submitted stack has already landed; landing the rest would re-land it",
			err: fmt.Errorf("%d of the %d submitted Changes of %s have already landed: pogo does not land part of a stack twice — make the unlanded work again on the current trunk and resubmit",
				st.Equivalent, len(mr.EnthSubmitted), mr.Item)}}
	}

	// 2. Dry run, and the gate checkout of its result.
	if err := r.guardGateWorktree(gateDir, mr.ID); err != nil {
		return enthAttempt{stage: "fetch", err: fmt.Errorf("gate dir: %w", err)}
	}
	if err := enthvcs.RemoveGateDir(gateDir); err != nil {
		return enthAttempt{stage: "fetch", err: fmt.Errorf("gate dir: %w", err)}
	}
	if err := os.MkdirAll(filepath.Dir(gateDir), 0o755); err != nil {
		return enthAttempt{stage: "fetch", err: fmt.Errorf("gate dir: %w", err)}
	}
	vmr := vcs.MR{ID: mr.ID, Repo: vcs.Repo{Path: mr.RepoPath}, Dir: gateDir, Target: mr.TargetRef,
		Changes: mr.EnthSubmitted, RequestID: fmt.Sprintf("%s/%d", mr.ID, attempt)}
	log.Printf("refinery: MR %s step=enth-dry-run target=%s changes=%d attempt=%d", mr.ID, mr.TargetRef, len(mr.EnthSubmitted), attempt)
	ctx, cancel = enthCtx()
	p, err := b.Prepare(ctx, vmr)
	cancel()
	if err != nil {
		return enthAttempt{stage: "rebase", err: err}
	}
	log.Printf("refinery: MR %s dry run: T=%s N=%s (gate checkout %s)", mr.ID, shortSHA(p.Tip), shortSHA(p.New), gateDir)

	// 3. Closing-ref gate on what would land (R12).
	log.Printf("refinery: MR %s step=closing-ref-check item=%s attempt=%d", mr.ID, mr.Item, attempt)
	if cerr := r.checkEnthClosingRefs(b, mr); cerr != nil {
		return enthAttempt{output: cerr.Error(), stage: "closing-ref-check", err: cerr}
	}
	// The submission's own refinery.toml can carry pr_mode too.
	if r.loadConfig(gateDir, mr.RepoPath).PRMode {
		return enthAttempt{stage: stageEnthConfig, err: prModeRefusal(mr.RepoPath)}
	}

	// 4. Gates, in the checkout of N.
	var gateOutput string
	switch {
	case skipGates:
		log.Printf("refinery: MR %s step=quality-gates attempt=%d skipped (skip_on_retry=true)", mr.ID, attempt)
		gateOutput = "(quality gates skipped on retry — skip_on_retry=true)"
	case hold.held(p.New):
		log.Printf("refinery: MR %s step=quality-gates attempt=%d HELD from attempt %d — the dry run's result is unchanged (%s)",
			mr.ID, attempt, hold.attempt, shortSHA(p.New))
		gateOutput = hold.output + fmt.Sprintf(
			"\n\n(quality gates NOT re-run on attempt %d: they passed on attempt %d on the same enth result %s from the same tip, so the verdict above is that run's, replayed verbatim.)\n",
			attempt, hold.attempt, shortSHA(p.New))
	default:
		log.Printf("refinery: MR %s step=quality-gates attempt=%d gating=%s heartbeat_every=%s", mr.ID, attempt, shortSHA(p.New), r.gateHeartbeat())
		out, gates, qerr := r.runQualityGates(r.gateContext(mr), gateDir, mr.RepoPath, mr)
		gateOutput = out
		if qerr != nil {
			return enthAttempt{output: gateOutput, stage: gateStage(gates), err: fmt.Errorf("quality gate: %w", qerr)}
		}
		hold.record(p.New, gateOutput, attempt)
	}

	// 5. The land, bound to what was gated.
	pending := &EnthPendingLand{RequestID: vmr.RequestID, Target: mr.TargetRef, Tip: p.Tip, New: p.New,
		Changes: append([]string(nil), mr.EnthSubmitted...), Attempt: attempt, IssuedAt: time.Now().UTC()}
	r.setEnthPending(mr, pending)
	log.Printf("refinery: MR %s step=enth-land expect-tip=%s expect-new=%s request=%s attempt=%d", mr.ID, shortSHA(p.Tip), shortSHA(p.New), vmr.RequestID, attempt)
	ctx, cancel = enthCtx()
	landed, lerr := b.Land(ctx, p)
	cancel()
	if lerr != nil {
		if enthvcs.LandedNothing(lerr) {
			// enthd turned the land down: nothing landed. Anything else
			// (a dropped connection, an unknown answer) keeps it pending,
			// and the next attempt replays it first.
			r.setEnthPending(mr, nil)
		}
		return enthAttempt{output: gateOutput, stage: "push", gatesReached: true, err: lerr}
	}
	r.setEnthPending(mr, nil)
	return enthAttempt{output: gateOutput, stage: "push", gatesReached: true, record: landed.Record}
}

// replayEnthPending re-issues p. landed reports that it had (or has now)
// landed; a refusal clears it, which means nothing landed; any other error
// leaves p in place.
func (r *Refinery) replayEnthPending(b enthvcs.Backend, mr *MergeRequest, p *EnthPendingLand) (*vcs.LandRecord, bool, error) {
	ctx, cancel := enthCtx()
	defer cancel()
	rec, err := b.LandExpect(ctx, p.Target, p.Changes, p.Tip, p.New, p.RequestID)
	if err == nil {
		r.setEnthPending(mr, nil)
		return &rec, true, nil
	}
	if enthvcs.LandedNothing(err) {
		log.Printf("refinery: MR %s enth land request %s did not land (%s): re-planning from a fresh dry run", mr.ID, p.RequestID, enthvcs.RefusalCode(err))
		r.setEnthPending(mr, nil)
		return nil, false, nil
	}
	return nil, false, err
}

func (r *Refinery) enthPending(mr *MergeRequest) *EnthPendingLand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return mr.EnthPending
}

// setEnthPending records (or clears) the land in flight, write-through: it is
// only worth anything if it is on disk before the land is sent.
func (r *Refinery) setEnthPending(mr *MergeRequest, p *EnthPendingLand) {
	r.mu.Lock()
	mr.EnthPending = p
	r.saveStateLocked()
	r.mu.Unlock()
	r.flushState()
}

// priorEnthMergeCovering is the already-merged rule's record (§6.3): a merged
// MR in history for the same repo and item, not itself a no-op, whose land
// record lists every id this MR submits.
func (r *Refinery) priorEnthMergeCovering(mr *MergeRequest) *MergeRequest {
	if mr.Item == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.history) - 1; i >= 0; i-- {
		h := r.history[i]
		if h == nil || h == mr || h.ID == mr.ID || h.Status != StatusMerged || h.AlreadyMerged {
			continue
		}
		if h.Item != mr.Item || filepath.Clean(h.RepoPath) != filepath.Clean(mr.RepoPath) {
			continue
		}
		if h.Enth.Covers(mr.EnthSubmitted) {
			c := *h
			return &c
		}
	}
	return nil
}

// checkEnthClosingRefs is checkClosingRefs for an enth submission: the whole
// message of every submitted Change. There is no PR body on enth (§11). The
// judgement is enthClosingRefs (closingref_gate.go, slice 9).
func (r *Refinery) checkEnthClosingRefs(b enthvcs.Backend, mr *MergeRequest) error {
	ctx, cancel := enthCtx()
	defer cancel()
	return enthClosingRefs(ctx, b, mr.EnthSubmitted)
}

func prModeRefusal(repoPath string) *enthRefusal {
	return &enthRefusal{class: ClassDefect, signal: "pr_mode on enth",
		reason: "not retryable: pr_mode is configured, and an enth repo has no pull request to push back to (enth-vcs §11) — remove pr_mode from .pogo/refinery.toml",
		err:    fmt.Errorf("%s is an enth repo and its refinery.toml sets pr_mode, which is refused on enth: nothing was landed", repoPath)}
}

// failEnthOnce fails mr on its first attempt without a retry.
func (r *Refinery) failEnthOnce(mr *MergeRequest, stage string, err *enthRefusal) (mergeResult, error) {
	emitMergeAttempted(mr, 1)
	fail := AttemptFailure{Attempt: 1, Stage: stage, Time: time.Now(), RawError: err.Error(),
		Class: err.class, Signal: err.signal, NotRetriedReason: err.reason}
	r.recordAttemptFailure(mr, fail)
	emitMergeFailed(mr, 1, stage, err, true, "", fail)
	log.Printf("refinery: MR %s %s", mr.ID, fail.Line())
	return mergeResult{}, err
}

// describeEnthAttemptFailure is describeAttemptFailure for an enth attempt:
// the remote is the enthd, and enth's stable refusal codes are classified
// before any text matching.
func (r *Refinery) describeEnthAttemptFailure(gateDir string, e vcs.EnthRepo, attempt int, stage string, err error) (AttemptFailure, disposition) {
	fail := AttemptFailure{Attempt: attempt, Stage: stage, Time: time.Now(), RawError: errorText(err)}
	fail.Remote = e.Remote
	fail.Transport, _ = remoteTransport(e.Remote)
	var ce *vcs.CommandError
	if errors.As(err, &ce) {
		fail.Command = "enth " + strings.Join(ce.Args, " ")
	}
	if len(fail.RawError) > rawErrorRecordCap {
		fail.RawError = truncate(fail.RawError, rawErrorRecordCap) +
			fmt.Sprintf("\n… (truncated at %d bytes for the persisted record; the full text is in the log line above)", rawErrorRecordCap)
	}
	disp, ok := classifyEnthFailure(err)
	if !ok {
		raw := ""
		if ce != nil {
			raw = string(ce.Output)
		}
		disp = classifyFailure(stage, raw, err)
	}
	fail.Class = disp.Class
	fail.Signal = disp.Signal
	return fail, disp
}

// classifyEnthFailure places an enth refusal by its stable code. ok is false
// for anything else (a gate's verdict, a failed exec), which the shared
// classifier judges.
func classifyEnthFailure(err error) (disposition, bool) {
	var own *enthRefusal
	if errors.As(err, &own) {
		return disposition{Class: own.class, Retryable: false, Signal: own.signal, Reason: own.reason}, true
	}
	var conflict *enthvcs.LandConflictError
	if errors.As(err, &conflict) {
		return disposition{Class: ClassDefect, Retryable: false, Signal: "enth conflict",
			Reason: "not retryable: the dry run reached the tree and the tree disagreed — exactly as true on the next attempt. " +
				"Make the work again on the current trunk (check it out, re-apply, enth change) and resubmit"}, true
	}
	code := enthvcs.RefusalCode(err)
	switch code {
	case "":
		return disposition{}, false
	case "stale_tip", "result_mismatch":
		return disposition{Class: ClassContention, Retryable: true, Signal: "enth " + code,
			RetryReason: "RETRYING: enth refused the land (" + code + ") because the trunk moved after the dry run — nothing landed, and a fresh dry run re-plans and re-gates against the new tip (counted against max_attempts)"}, true
	case "unreachable":
		return disposition{Class: ClassInfrastructure, Retryable: true, Signal: "enth unreachable"}, true
	case "unauthorized", "forbidden":
		return disposition{Class: ClassInfrastructure, Retryable: false, Signal: "enth " + code,
			Reason: "not retryable: enthd answered and refused the token this box offered — the same question gets the same answer on a retry. Fix the token; do NOT dispatch a code fix"}, true
	case "request_id_reused":
		return disposition{Class: ClassInfrastructure, Retryable: false, Signal: "enth request_id_reused",
			Reason: "not retryable: the refinery re-used a request id for a different land — a refinery defect, not the submission's; nothing landed"}, true
	}
	return disposition{Class: ClassDefect, Retryable: false, Signal: "enth " + code,
		Reason: "not retryable: enth refused the submitted Changes (" + code + ") — the same Changes get the same answer on a retry"}, true
}

// runEnthDeploy runs the per-repo deploy hook in a checkout of what landed.
// The gate dir already holds N unless the land was a replay.
func (r *Refinery) runEnthDeploy(b enthvcs.Backend, gateDir string, mr *MergeRequest, rec *vcs.LandRecord) string {
	if r.deployCommandFor(gateDir, mr.RepoPath) == "" && r.DeployCommand(mr.RepoPath) == "" {
		return ""
	}
	if st, err := enthvcs.ReadState(gateDir); err != nil || st.Base != rec.New {
		if err := enthvcs.RemoveGateDir(gateDir); err != nil {
			return "deploy: " + err.Error()
		}
		ctx, cancel := enthCtx()
		cerr := b.CheckoutWhole(ctx, rec.New, gateDir)
		cancel()
		if cerr != nil {
			return "deploy: check out the landed result " + shortSHA(rec.New) + ": " + cerr.Error()
		}
	}
	return r.runDeploy(gateDir, mr, vcs.Enth, rec.New)
}

// resolveRecoveredEnth resolves an enth MR that was in flight when pogod
// stopped (§6.3). With no pending land, nothing this MR did can have landed:
// re-queued at head. With one, the same land is re-issued under the same
// request id: enthd replays it if it landed (recorded as merged, OnMerged
// fired, gates not re-run), refuses it if it did not (re-queued, re-planned).
// An enthd that does not answer leaves the pending land on the record and
// re-queues the MR, whose first attempt replays it again — so an enth MR is
// never marked lost here: everything needed to finish it is on the record.
func (r *Refinery) resolveRecoveredEnth(mr *MergeRequest) {
	var rec *vcs.LandRecord
	landed := false
	var why string
	if p := mr.EnthPending; p != nil {
		e, ok := enthRepoFor(mr.RepoPath)
		if !ok {
			why = "the project record no longer says enth; the pending land stays on the record"
		} else {
			var err error
			rec, landed, err = r.replayEnthPending(enthWork(e), mr, p)
			switch {
			case landed:
			case err != nil:
				why = fmt.Sprintf("replay of request %s did not answer (%v); it is replayed again first on the re-run", p.RequestID, err)
			default:
				why = fmt.Sprintf("request %s had not landed", p.RequestID)
			}
		}
	} else {
		why = "no land had been issued"
	}

	r.mu.Lock()
	for i, p := range r.recovered {
		if p == mr {
			r.recovered = append(r.recovered[:i], r.recovered[i+1:]...)
			break
		}
	}
	var fire OnMerged
	if landed {
		mr.Status = StatusMerged
		mr.DoneTime = r.nowFunc()
		mr.Enth = rec
		mr.GateOutput = fmt.Sprintf("(recovered after a pogod restart: enth land request %s had landed — replayed by enthd, not landed again)", rec.RequestID)
		if mr.Author != "" {
			delete(r.failureCounts, mr.Author)
		}
		r.history = append(r.history, mr)
		fire = r.onMerged
		log.Printf("refinery: recovery found in-flight enth MR %s landed (request %s, entry %d, new %s) — recorded as merged",
			mr.ID, rec.RequestID, rec.Entry, shortSHA(rec.New))
	} else {
		mr.Status = StatusQueued
		mr.Error = ""
		mr.GateOutput = ""
		r.queue = append([]*MergeRequest{mr}, r.queue...)
		log.Printf("refinery: recovery re-queued in-flight enth MR %s at head (%s)", mr.ID, why)
	}
	r.saveStateLocked()
	r.mu.Unlock()
	r.flushState()

	if landed {
		emitMerged(mr, 0, vcs.Enth, rec.New, 0, false)
		if fire != nil {
			fire(mr)
		}
	}
}
