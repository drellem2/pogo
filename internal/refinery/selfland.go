package refinery

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// Self-landing on enth (docs/design/enth-vcs.md §6; mg-5b904).
//
// DECISION (Daniel, 2026-11-03): on an enth repo there is no merge queue. The
// worker lands its own work —
//
//	pogo presubmit --item=<item>
//	enth land <stack> --request-id <item>/<attempt>
//	pogo land --item=<item> --landed-file=<enth's output> --verdict-file=…
//
// — and the refinery's enth land path (slice 7) is deleted rather than left
// dormant, so the de facto workflow is the only one. The refinery refuses an
// enth submission, and an enth MR that reaches the queue anyway (one queued
// before this change) is failed before any git step, both with a pointer to
// `pogo land`.
//
// ACCEPTED TRADE-OFF, recorded so nobody "fixes" it: the merged result is not
// tested before it lands. The worker tests its own base, and `enth land`
// hunk-merges onto the tip. Post-land CI on the GitHub mirror is the safety
// net; an enth pre-land gate (mg-b729a) may close the gap later.
//
// What stays the refinery's is the RECORD. RecordSelfLand turns the worker's
// land into a merged MR in the refinery's history — slice 8's `enth` land
// record, `refinery_merged` with `landed: {enth, id}` — and fires the same
// OnMerged callback a refinery merge fires, so pogod closes the work item
// exactly as it closes one today: done, the result sidecar naming the landed
// id, the filer notice and the coordinator's MERGED mail. The record is
// persisted BEFORE the callback runs, so the worker cannot be reaped before
// its land is on the record (mg-cef7).

// SelfLandRequest is what a worker hands RecordSelfLand: the land it made.
type SelfLandRequest struct {
	RepoPath string
	Item     string
	// Author is the MR's author, which the reap path closes; default Item.
	Author    string
	TargetRef string
	// Landed is `enth land`'s stdout, verbatim: its one `landed` line.
	Landed []byte
	// RequestID is the --request-id the land was issued under. Optional;
	// recorded on the land record for a reader tying it to the attempt.
	RequestID string
	Verdict   json.RawMessage
}

// SelfLandResult is the recorded MR, and whether this call recorded it or an
// earlier one had (a worker that re-ran `pogo land` after losing its answer).
type SelfLandResult struct {
	MR              *MergeRequest
	AlreadyRecorded bool
}

// selfLandTimeout bounds the enthd reads that check a land record.
const selfLandTimeout = 2 * time.Minute

// RecordSelfLand checks a worker's land against enthd, records it as a
// merged MR, and fires OnMerged. It refuses — recording nothing — a repo that
// is not enth, a land that is not one, a land onto another ref, and a land
// whose Changes enthd does not list as the item's and as landed exactly where
// the record says: pogo closes an item on what enthd says landed, never on a
// worker's word alone. Recording the same land twice returns the first MR and
// fires nothing.
func (r *Refinery) RecordSelfLand(req SelfLandRequest) (SelfLandResult, error) {
	if req.RepoPath == "" {
		return SelfLandResult{}, fmt.Errorf("repo_path is required")
	}
	if req.Item == "" {
		return SelfLandResult{}, fmt.Errorf("item is required: on enth the work item names the work")
	}
	if err := RefusePogoHomeRepo(req.RepoPath); err != nil {
		return SelfLandResult{}, err
	}
	e, ok := enthRepoFor(req.RepoPath)
	if !ok {
		return SelfLandResult{}, fmt.Errorf("%s is not recorded as an enth repo: a git repo's work merges through the refinery — pogo refinery submit", req.RepoPath)
	}
	if req.Author == "" {
		req.Author = req.Item
	}
	if len(req.Verdict) > 0 {
		if err := ValidateVerdict(req.Verdict); err != nil {
			return SelfLandResult{}, fmt.Errorf("verdict: %w", err)
		}
	}
	rec, err := enthvcs.ParseLanded(req.Landed)
	if err != nil {
		return SelfLandResult{}, fmt.Errorf("the land to record: %w", err)
	}
	b := enthWork(e)
	// No target is the repo's trunk, as its project record names it — what
	// `enth land` with no --ref moves.
	want := b.LandRef(req.TargetRef)
	if rec.Ref != want {
		return SelfLandResult{}, fmt.Errorf("the land moved %s, not %s (the target %q): refusing to record it as this item's merge", rec.Ref, want, req.TargetRef)
	}
	if req.TargetRef == "" {
		req.TargetRef = strings.TrimPrefix(want, "refs/heads/")
	}
	if top := rec.Changes[len(rec.Changes)-1]; top.Landed != rec.New {
		return SelfLandResult{}, fmt.Errorf("the land record is inconsistent: its result %s is not its top Change's landed id %s", rec.New, top.Landed)
	}
	rec.RequestID = req.RequestID

	// What enthd says, not what the worker says.
	ctx, cancel := context.WithTimeout(context.Background(), selfLandTimeout)
	defer cancel()
	listed, err := b.ListChanges(ctx, req.Item, "", false)
	if err != nil {
		return SelfLandResult{}, fmt.Errorf("listing %s's Changes on %s to check the land: %w", req.Item, e.Remote, err)
	}
	byID := make(map[string]vcs.Change, len(listed))
	for _, c := range listed {
		byID[c.ID] = c
	}
	submitted := make([]string, 0, len(rec.Changes))
	worker := ""
	for _, c := range rec.Changes {
		l, ok := byID[c.Submitted]
		switch {
		case !ok:
			return SelfLandResult{}, fmt.Errorf("Change %s is not one of %s's on %s (no %s=%s trailer): refusing to close the item on another item's land",
				c.Submitted, req.Item, e.Remote, vcs.TrailerItem, req.Item)
		case l.Landed == "":
			return SelfLandResult{}, fmt.Errorf("enthd has no land record for Change %s: it has not landed, so there is nothing to record", c.Submitted)
		case l.Landed != c.Landed:
			return SelfLandResult{}, fmt.Errorf("enthd says Change %s landed as %s, and the record says %s: refusing a record enthd does not confirm", c.Submitted, l.Landed, c.Landed)
		}
		submitted = append(submitted, c.Submitted)
		worker = l.Trailer(vcs.TrailerWorker)
	}

	probe := &MergeRequest{Item: req.Item, RepoPath: req.RepoPath, EnthSubmitted: submitted}
	if prior := r.priorEnthMergeCovering(probe); prior != nil {
		log.Printf("refinery: self-land of %s (%d Change(s)) already recorded as MR %s — not recorded twice", req.Item, len(submitted), prior.ID)
		return SelfLandResult{MR: prior, AlreadyRecorded: true}, nil
	}

	now := time.Now()
	mr := &MergeRequest{
		ID:            generateID(),
		RepoPath:      req.RepoPath,
		TargetRef:     req.TargetRef,
		Author:        req.Author,
		Item:          req.Item,
		Status:        StatusMerged,
		EnthSubmitted: submitted,
		EnthWorker:    worker,
		Enth:          &rec,
		SelfLanded:    true,
		Verdict:       req.Verdict,
		SubmitTime:    now,
		StartTime:     now,
		DoneTime:      now,
		GateOutput: "(self-landed by the worker with `enth land`: the refinery ran no gate, and the merged result was not tested before it landed — " +
			"the accepted trade-off of enth-vcs §6; post-land CI on the mirror is the check)",
	}
	r.mu.Lock()
	// Checked again under the lock: two `pogo land` calls racing for the
	// same land record it once.
	if prior := r.priorEnthMergeCoveringLocked(mr); prior != nil {
		r.mu.Unlock()
		return SelfLandResult{MR: prior, AlreadyRecorded: true}, nil
	}
	r.byID[mr.ID] = mr
	if mr.Author != "" {
		delete(r.failureCounts, mr.Author)
	}
	r.history = append(r.history, mr)
	r.pruneHistoryLocked()
	r.saveStateLocked()
	onMerged := r.onMerged
	r.mu.Unlock()
	// Write-through before the callback: OnMerged closes the item and stops
	// the worker, and a land that is not on the record by then is the
	// mg-cef7 failure.
	r.flushState()

	log.Printf("refinery: self-landed %s recorded as MR %s: %s entry %d %s -> %s (%d Change(s), request %s, worker %s)",
		req.Item, mr.ID, rec.Ref, rec.Entry, shortSHA(rec.Old), shortSHA(rec.New), len(submitted), rec.RequestID, worker)
	emitMerged(mr, 0, vcs.Enth, rec.New, 0, false)
	if onMerged != nil {
		onMerged(mr)
	}
	c := *mr
	return SelfLandResult{MR: &c}, nil
}

// priorEnthMergeCovering finds a merged MR in history for the same repo and
// item, not itself a no-op, whose land record lists every id mr submits.
func (r *Refinery) priorEnthMergeCovering(mr *MergeRequest) *MergeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.priorEnthMergeCoveringLocked(mr)
}

func (r *Refinery) priorEnthMergeCoveringLocked(mr *MergeRequest) *MergeRequest {
	if mr.Item == "" {
		return nil
	}
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

// selfLandPointer is what every enth refusal tells the submitter to do.
func selfLandPointer(item, repo string) string {
	if item == "" {
		item = "<item>"
	}
	return fmt.Sprintf("an enth repo has no merge queue: land your own work and record it — "+
		"pogo presubmit --item=%[1]s && enth land <stack> --request-id %[1]s/<attempt> > land.json && "+
		"pogo land --item=%[1]s --repo=%[2]s --landed-file=land.json (enth-vcs §6; the polecat template's self_land does all three)", item, repo)
}

// refuseEnthMR fails an enth MR that reached the queue — one queued before
// the refinery stopped taking them — on its first attempt, before any git
// step, as a defect naming the verb that replaces it.
func (r *Refinery) refuseEnthMR(mr *MergeRequest) (mergeResult, error) {
	err := fmt.Errorf("MR %s is enth work (%d Change(s) of %s), and the refinery no longer lands enth work: nothing was landed. %s",
		mr.ID, len(mr.EnthSubmitted), mr.Item, selfLandPointer(mr.Item, mr.RepoPath))
	emitMergeAttempted(mr, 1)
	fail := AttemptFailure{Attempt: 1, Stage: "enth-config", Time: time.Now(), RawError: err.Error(),
		Class: ClassDefect, Signal: "enth MR refused",
		NotRetriedReason: "not retryable: the refinery does not land enth work (enth-vcs §6, mg-5b904) — the worker lands it with enth land and records it with pogo land"}
	r.recordAttemptFailure(mr, fail)
	emitMergeFailed(mr, 1, fail.Stage, err, true, "", fail)
	log.Printf("refinery: MR %s %s", mr.ID, fail.Line())
	return mergeResult{}, err
}

// requeueRecoveredEnth puts an enth MR that was in flight when pogod stopped
// back at the head of the queue, where refuseEnthMR fails it. The refinery
// issues no enth lands, so there is nothing of its own to resume or replay.
func (r *Refinery) requeueRecoveredEnth(mr *MergeRequest) {
	r.mu.Lock()
	for i, p := range r.recovered {
		if p == mr {
			r.recovered = append(r.recovered[:i], r.recovered[i+1:]...)
			break
		}
	}
	mr.Status = StatusQueued
	mr.Error = ""
	mr.GateOutput = ""
	r.queue = append([]*MergeRequest{mr}, r.queue...)
	r.saveStateLocked()
	r.mu.Unlock()
	r.flushState()
	log.Printf("refinery: recovery re-queued in-flight enth MR %s; the refinery no longer lands enth work, so it will be refused", mr.ID)
}

// enthSubmitRefusal is Submit's answer to an enth repo.
func enthSubmitRefusal(req *MergeRequest) error {
	return fmt.Errorf("%s is an enth repo — %s", req.RepoPath, selfLandPointer(strings.TrimSpace(req.Item), req.RepoPath))
}
