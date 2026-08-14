package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/filernotify"
	"github.com/drellem2/pogo/internal/refinery"
)

// mergedPolecatStopTimeout bounds how long reapMergedPolecat waits for a
// SIGTERMed polecat to exit before force-killing it — same budget as the
// pogo agent stop HTTP handler.
const mergedPolecatStopTimeout = 5 * time.Second

// deferDoneBackstopTimeout bounds how long a --defer-done polecat may stay
// alive after its branch merges before the backstop reaps + escalates it (gh
// drellem2/pogo #81). It is deliberately generous — a healthy post-merge flow
// (open PR, verify, mail PR URL, mg done) finishes in well under a minute — so
// the backstop only fires on a genuinely stuck polecat, never on a slow-but-
// live one. A deferred polecat that never ends its lifecycle would otherwise
// hold its slot forever and re-submit its branch, regressing the gh #34/#35
// slot-protection guarantees the auto-stop path exists to enforce.
const deferDoneBackstopTimeout = 15 * time.Minute

// filerNotifier tells the agent that FILED a work item that it has completed
// (mg-f120). It is the *filernotify.Notifier in production and an interface
// here so a test can watch what the reap path hands it — and so a nil one is a
// legal "not wired", which is what every test that is not about this seam
// passes.
type filerNotifier interface {
	Notify(filernotify.Completion) filernotify.Outcome
}

// notifyFiler is the nil-safe call. The reap path must never be conditional on
// the notifier existing: a merge whose close happened is a merge whose close
// happened, and a missing notifier is a wiring fault to be seen in one place
// rather than guarded at every callsite.
func notifyFiler(n filerNotifier, c filernotify.Completion) {
	if n == nil {
		return
	}
	n.Notify(c)
}

// polecatReaper is the slice of agent.Registry that reapMergedPolecat needs.
type polecatReaper interface {
	GetByWorkItemOrName(id string) *agent.Agent
	StopWithCause(name string, timeout time.Duration, cause string) error
	// ReleaseClaimAfterExit returns the work item of a polecat that ended its
	// own process to available/, reporting whether a held claim was actually
	// released. Registry.Stop covers the polecats pogod tears down; this covers
	// the deferred polecat that dies on its own between merge and `mg done`
	// (mg-c8d5).
	ReleaseClaimAfterExit(a *agent.Agent) (bool, error)
}

// postMergeVerdict is the answer to the one question the refinery cannot answer
// for itself: does merging this branch COMPLETE its work item, or is the merge a
// step with work after it? Declared is true only when something said so; Reason
// carries the human-readable why, which both the log line and the coordinator's
// MERGED mail print.
//
// The zero value means "merging completes it" — the common case, and the
// behaviour every caller had before mg-d86e.
type postMergeVerdict struct {
	Declared bool
	Reason   string
}

// resolvePostMergeWork asks the WORK ITEM whether merging completes it
// (mg-d86e), by way of the declares probe (client.MGWorkItemDeclaresPostMergeWork
// in production, injectable here).
//
// It is deliberately a separate step from reapMergedPolecat: pogod's OnMerged
// hook needs the same answer twice — once for the reap decision, once for the
// wording of the MERGED mail it sends the coordinator — and asking the store
// once means the two can never disagree.
//
// It answers the zero verdict, without probing, for everything the reap path
// would ignore anyway (no author, no such agent, a crew author) and for MRs
// already deferred by other means. Probing there would at best waste an `mg
// show` and at worst mislabel a crew-authored merge, since a crew agent's name
// is not a work-item id.
//
// A probe that FAILS declares post-merge work. That direction is deliberate:
// the whole defect this closes is a completion asserted without the standing to
// assert it, and an unreadable item gives no standing. The cost of being wrong
// this way is bounded — the polecat keeps running, calls `mg done` itself as its
// own protocol tells it to, and the defer-done backstop reaps and escalates it
// within deferDoneBackstopTimeout if it does not. The cost of being wrong the
// other way is a ticket silently truncated, which nothing catches.
func resolvePostMergeWork(reg polecatReaper, mr *refinery.MergeRequest, declares func(id string) (bool, error)) postMergeVerdict {
	if mr == nil || mr.Author == "" {
		return postMergeVerdict{}
	}
	// The probe runs for a live polecat's merge AND for an authorless one, and
	// the second half is mg-be37. The completion path below no longer requires a
	// running polecat, so a guard that skipped the probe whenever the registry had
	// nobody would let exactly the merges this ticket added — a coordinator
	// resubmitting a dead polecat's branch — close an item that declares its own
	// remainder. What is still skipped is an author that names no work item at
	// all: a crew agent's name is not an id, and `mg show mayor` is a wasted call
	// whose failure would be read as a declaration.
	if a := reg.GetByWorkItemOrName(mr.Author); (a == nil || a.Type != agent.TypePolecat) &&
		!client.LooksLikeWorkItemID(mr.Author) {
		return postMergeVerdict{}
	}
	// The refinery performed a declared post-merge step and it FAILED
	// (mg-6879). The merge landed, so the MR reads "merged" and every
	// status-keyed reader sees success — but the deliverable does not exist,
	// which is the exact shape of the defect that let a release be reported
	// complete with no tag. Refuse to complete the item.
	//
	// Checked before every other signal, including the declares == nil guard
	// below: this verdict needs no probe, and it must not be suppressible by a
	// missing one.
	if mr.PostMergeError != "" {
		return postMergeVerdict{
			Declared: true,
			Reason: fmt.Sprintf("the refinery's declared post-merge step FAILED after the merge landed (%s) — "+
				"the branch is on %s but its deliverable does not exist, so this merge is not completion (mg-6879)",
				mr.PostMergeError, mr.TargetRef),
		}
	}
	if declares == nil {
		return postMergeVerdict{}
	}
	if mr.DeferDone || mr.PRFlow {
		return postMergeVerdict{}
	}
	declared, err := declares(mr.Author)
	if err != nil {
		return postMergeVerdict{
			Declared: true,
			Reason: fmt.Sprintf("owns an item that could not be read (%s: %v) — declining to complete it, "+
				"because an unreadable item is not evidence of completion (mg-d86e)", mr.Author, err),
		}
	}
	if !declared {
		return postMergeVerdict{}
	}
	return postMergeVerdict{
		Declared: true,
		Reason: fmt.Sprintf("declares post-merge work on its item %s (tag %q): merging is a STEP, not completion (mg-d86e)",
			mr.Author, client.PostMergeWorkTag),
	}
}

// reapMergedPolecat implements the event-driven polecat stop (gh #35): the
// moment the refinery reports a successful merge, pogod records the work item
// as done on the polecat's behalf and stops the polecat, instead of leaving
// both to the mayor's next coordination cycle — 90s+ of lag during which a
// lingering polecat holds a slot and can re-submit its branch (gh #34).
//
// The registry's OnExit hook owns the rest of the cleanup once the process
// exits (worktree removal, mail-check schedule reaping), and the mayor's reap
// loop stays as the backstop for polecats this path misses — e.g. a merge
// that lands while pogod is down. Blocks up to mergedPolecatStopTimeout, so
// the refinery loop invokes it in a goroutine.
//
// Only polecats are stopped: crew agents (or a human) can author MRs too, but
// their lifecycle is not tied to a single work item.
func reapMergedPolecat(reg polecatReaper, mr *refinery.MergeRequest, complete func(id, resultJSON string) error, postMerge postMergeVerdict, backstop *deferredBackstop, filer filerNotifier) {
	if mr.Author == "" {
		return
	}
	// Resolve by work-item id OR registry name: a polecat registers under its
	// bare id (a.Name, e.g. "d087") but authors its MR with the full work-item
	// id (mr.Author == a.WorkItemID, e.g. "mg-d087"), so a plain Get(mr.Author)
	// misses and the polecat lingers post-merge (gh #48).
	a := reg.GetByWorkItemOrName(mr.Author)
	polecat := a != nil && a.Type == agent.TypePolecat

	// NO LIVE POLECAT IS NOT A REASON TO LEAVE THE ITEM OPEN (mg-be37). Until
	// this line the function returned here, so completion was conditional on a
	// polecat being registered at merge time — and it is exactly the merges with
	// no polecat that need it most:
	//
	//	a coordinator submits a dead polecat's stranded branch by hand
	//	-> the merge lands, 1116 insertions go onto main,
	//	-> nothing closes the item, and it sits in available/
	//	-> priority-wake advertises it as unclaimed and high priority
	//	-> the recommended action re-derives work that is already merged.
	//
	// That happened four times on 2026-08-09 (mg-51f4, mg-00b3, mg-6c90,
	// mg-56ac). It is worse than the unmerged case it grew out of: while the
	// branch is unmerged the spawn-time stranded-work guard refuses the dispatch,
	// but the moment it merges the guard correctly stops refusing and the item is
	// still open. The window opens at merge and never closes. priority-wake told
	// the mayor to "claim or dispatch now: mg-6c90" four minutes after b9e1d1b.
	//
	// Nothing about closing an item needs a live worker — the author is on the
	// merge request. What it does need is an author that NAMES one: crew agents
	// and humans author merge requests too, and `mg done mayor` is not a
	// completion, it is an error the log would fill up with. So the shape test
	// stands in for the registry lookup, and only for the completion half — the
	// stop below still requires a real polecat, because there is nothing else to
	// stop.
	if !polecat && !client.LooksLikeWorkItemID(mr.Author) {
		return
	}

	// The polecat owns its own post-merge lifecycle in three cases:
	//
	//   - --defer-done (gh #81): the submitter asked for it explicitly.
	//   - PR flow (mg-7746): the merge landed on an *integration* branch, not
	//     the repo's default branch. That is an intermediate step — the
	//     deliverable is a PR to the default branch that the polecat has not
	//     opened yet — so this merge is categorically not completion. Derived
	//     by the refinery from the target ref rather than requested, because
	//     three separate items (mg-74ee, mg-6579, this one) merged, were marked
	//     done, and left no PR precisely because nobody passed the flag.
	//   - The WORK ITEM declares it (mg-d86e): the item carries the
	//     post-merge-work tag, or could not be read to tell. The two cases above
	//     both depend on the SUBMITTER knowing this merge is not the end — the
	//     flag is passed at submit time, the target ref is chosen at submit
	//     time — and a release polecat merging a version bump to main gets both
	//     wrong while doing exactly what its ticket said. mg-ca3c and mg-9f17
	//     each merged, were marked done, and were stopped before the tag step;
	//     both releases read as complete and neither existed.
	//
	// In every case the polecat still has work to do after the merge lands —
	// open the PR, push the tag, run verify checks, mail the result — and calls
	// `mg done` itself when that flow finishes. Skip the auto-done + auto-stop
	// below, which would kill it mid-flow, and arm a bounded backstop so a
	// deferred polecat that never ends its lifecycle is still reaped +
	// escalated. The backstop is disarmed by the OnExit hook when the polecat's
	// process ends (it completed cleanly and freed its slot).
	if mr.DeferDone || mr.PRFlow || postMerge.Declared {
		var reason string
		switch {
		case mr.PostMergeError != "":
			// Outranks the others: whatever lane this MR was in, a failed
			// post-merge step is the most important thing about it (mg-6879).
			reason = fmt.Sprintf("merged as %s but its declared post-merge step FAILED (%s) — deliverable missing",
				mr.MergedSHA, mr.PostMergeError)
		case mr.PRFlow:
			reason = fmt.Sprintf("merged into integration branch %q, not the repo default — PR still pending (mg-7746)", mr.TargetRef)
		case mr.DeferDone:
			reason = "submitted --defer-done (gh #81)"
		default:
			reason = postMerge.Reason
		}
		// The backstop reaps a RUNNING polecat that never ends its lifecycle, so
		// it can only be armed when there is one. An authorless deferred merge has
		// nothing to reap; it is logged and left to the sweep
		// (`pogo check-stranded`), which is the reader for exactly this residue.
		if !polecat {
			log.Printf("refinery: merged MR %s authored by %s %s — item deliberately NOT closed, and no polecat "+
				"is running to finish it; `pogo check-stranded` will report the item while it stays open (mg-be37)",
				mr.ID, mr.Author, reason)
			return
		}
		log.Printf("refinery: merged polecat %s %s — skipping auto-done/auto-stop; polecat owns its lifecycle", a.Name, reason)
		if backstop != nil {
			backstop.arm(a.Name, mr)
		}
		return
	}

	// Record completion before stopping: the polecat's own protocol runs
	// mg done when it observes the merged status, but it will be gone
	// before its next poll. If the polecat won the race after all, the
	// call fails with an already-done error — harmless. Keyed on mr.Author,
	// which is the mg work-item id.
	//
	// `target` is recorded because the mayor's classification logic reads it to
	// tell an integration-branch merge from a completed one; omitting it made a
	// documented signal actively misleading (mg-7746).
	//
	// There is deliberately no `pr_flow` key, and there never can be one: this
	// writer is only reached on the completion path, because a PR-flow merge
	// returns above without calling `mg done` at all. A sidecar written HERE is
	// by construction a default-branch completion. The PR-flow half of the
	// classification lives on the merge request instead (`pogo refinery show
	// <id> --json`), which is where the refinery records it. mg-c8d5 corrects
	// the docs that claimed otherwise.
	// `merged_sha` and `post_merge_tag` record what actually landed, so the
	// closed item answers "which commit is this" and "did the tag get pushed"
	// without a git round-trip. Reaching this writer at all means any declared
	// post-merge step succeeded — the guard above returns otherwise — so the
	// tag named here is one the refinery has confirmed on origin (mg-6879).
	sidecar := map[string]any{
		"branch":       mr.Branch,
		"mr":           mr.ID,
		"completed_by": "refinery",
		"target":       mr.TargetRef,
	}
	if mr.MergedSHA != "" {
		sidecar["merged_sha"] = mr.MergedSHA
	}
	if mr.PostMergeTag != "" {
		sidecar["post_merge_tag"] = mr.PostMergeTag
	}
	// The AUTHOR'S OWN verdict, carried in from submit time and merged in here
	// rather than replaced by this writer (mg-dfea).
	//
	// This writer used to be the sole author of the sidecar, and that is what
	// destroyed the worker's verdict. Not by clobbering it — `mg done` refuses
	// a second done rather than overwriting the first, which was measured, not
	// assumed — but by PREEMPTION: pogod closes the item the instant the merge
	// lands and stops the polecat ~0.5s later, so the polecat's own
	// `mg done --result` is refused as "already done", the protocol tells it
	// that refusal is success, and the verdict is gone with nothing complaining.
	// Over the live store on 2026-08-06 that left 139 of 149 sidecars
	// refinery-authored and 10 of 149 carrying any field beyond branch/mr/target.
	//
	// It is NESTED rather than flattened so the two records cannot collide.
	// Flattening would put the author's claims and the refinery's measurements
	// in one namespace, where a worker writing `branch` would either overwrite
	// the branch that actually merged or be silently dropped in favour of it —
	// a smaller instance of this same defect. Under `verdict` the author's
	// object is preserved verbatim and is identifiable as the author's.
	if len(mr.Verdict) > 0 {
		sidecar["verdict"] = mr.Verdict
	}
	result, _ := json.Marshal(sidecar)
	completeErr := complete(mr.Author, string(result))
	closed := itemIsClosed(completeErr)
	switch {
	case completeErr == nil:
	case closed:
		// The POLECAT won the race and its own result stands — the item is
		// closed with the worker's verdict, not ours, and that is the better
		// outcome, not a degraded one. mg enforces it: a second `mg done` is
		// refused, so there is no path by which this call replaces a result the
		// worker already wrote.
		log.Printf("refinery: mg done %s on merged polecat's behalf did not apply because the item is ALREADY DONE — "+
			"the polecat wrote its own result first and that result stands: %v", mr.Author, completeErr)
	default:
		// THE ITEM IS STILL OPEN, AND EVERY REPORT BELOW IS CONDITIONAL ON THAT
		// (mg-2b71). This line used to be followed, unconditionally, by a filer
		// mail saying COMPLETED and a summary line saying CLOSED — the failure
		// captured, formatted, printed, and then read by no control flow at all,
		// which reads as diligent error handling right up to the next line
		// contradicting it. Both of those now say what actually happened.
		log.Printf("refinery: MERGED BUT NOT CLOSED — work item %s is STILL OPEN after branch %s merged as %s: %v. "+
			"No completion is being reported for it; close it by hand once you know why, or leave it open if that is "+
			"correct (mg-2b71)", mr.Author, mr.Branch, mr.MergedSHA, completeErr)
	}

	// TELL THE AGENT THAT COMMISSIONED THIS ITEM (mg-f120). Everything above
	// this line reports the merge to somebody who did not ask for the work: the
	// refinery mails the coordinator, this function closes the item, the
	// coordinator archives it. The FILER was told by nobody, and learned only if
	// the worker volunteered a mail.
	//
	// It runs whether or not the close applied, and it says WHICH (mg-2b71). An
	// "already done" means the worker won the race and its own result stands —
	// the item is closed either way, and it is the CLOSE, not this call's
	// success, that the filer is waiting to hear about. A close that did NOT
	// happen is the opposite: the filer still needs the mail, because a merged
	// branch against an open item is exactly the state nobody else is watching,
	// but the mail must not call it a completion.
	//
	// The sidecar this writer produced is handed over as a FALLBACK only: the
	// notifier prefers what is actually in the store, which on the already-done
	// path is the worker's result rather than ours.
	worker := ""
	if a != nil {
		worker = a.Name
	}
	notifyFiler(filer, filernotify.Completion{
		ItemID:          mr.Author,
		Route:           filernotify.RouteMerge,
		Worker:          worker,
		Branch:          mr.Branch,
		MergedSHA:       mr.MergedSHA,
		Result:          fallbackSidecar(completeErr, string(result)),
		Closed:          closed,
		NotClosedReason: notClosedReason(completeErr, closed),
	})

	// There is nothing to stop. The close was the whole point of this call
	// (mg-be37) — say so at the same volume as the polecat path, because this
	// line is the only record that the merged-but-open window was shut, and its
	// absence over a night is how the four instances of 2026-08-09 went unnoticed.
	//
	// Conditional on the close having APPLIED (mg-2b71). The unconditional
	// version of this line is what told a reader the window was shut on the one
	// night it was not: mg-479c merged at 03:27:07, this line said CLOSED, and
	// `mg show` said `status=available` for the rest of the night. A failure
	// already announced itself above, so there is nothing more to say here.
	if !polecat {
		if closed {
			log.Printf("refinery: closed work item %s at merge with NO polecat running (branch=%s, merged=%s) — "+
				"a hand-submitted branch used to leave its item in available/, where priority-wake advertises "+
				"work that is already on the target (mg-be37)", mr.Author, mr.Branch, mr.MergedSHA)
		}
		return
	}

	// Stop keys on the registry name, which is the bare id — not mr.Author.
	if err := reg.StopWithCause(a.Name, mergedPolecatStopTimeout, agent.StopCauseMergeReap); err != nil {
		log.Printf("refinery: failed to stop merged polecat %s: %v", a.Name, err)
		return
	}
	log.Printf("refinery: stopped merged polecat %s (event-driven, gh #35)", a.Name)
}

// itemIsClosed reports whether the work item is CLOSED, given what the close
// attempt returned (mg-2b71).
//
// Two answers count as closed and there is no third: the close applied, or it
// was refused because the item is already terminal — which is a close somebody
// else performed, not a failure. client.CloseMGWorkItemAtMerge establishes that
// second case by ASKING THE STORE rather than by reading mg's prose, because
// "already done" and "not claimed" are both exit 4 and neither the exit status
// nor a message match can separate them.
//
// Everything else — a refusal, an unreadable store, a gated item this daemon
// declined to close — means the item is still open. That direction is the whole
// point: a completion asserted without the standing to assert it is the defect,
// so anything short of positive evidence of closure reads as not closed.
func itemIsClosed(completeErr error) bool {
	return completeErr == nil || errors.Is(completeErr, client.ErrMGWorkItemAlreadyDone)
}

// notClosedReason is the one-line why, in words, for a merge whose item stayed
// open. Empty when the item is closed — the notification says COMPLETED then,
// and a reason attached to a completion would be read as a caveat on it.
func notClosedReason(completeErr error, closed bool) string {
	if closed || completeErr == nil {
		return ""
	}
	if errors.Is(completeErr, client.ErrMGWorkItemGated) {
		// Not a failure. pogod could have closed this item — it is unclaimed and
		// one `mg claim` away — and declined, because a `parked`/`human`/
		// `blocked:` item is work somebody stopped on purpose and a merged
		// branch is not a decision to restart it (mg-2b71 fix direction 4).
		return "pogod declined to close it: " + completeErr.Error()
	}
	return "`mg done` did not apply: " + completeErr.Error()
}

// fallbackSidecar is the result JSON this writer produced, offered to the filer
// notification only when the write it came from SUCCEEDED.
//
// When `mg done` was refused the store holds somebody else's result — the
// worker's, which is the better one — and handing over ours would report a
// verdict that is not the one recorded. Empty is the honest answer there: the
// notifier reads the store and finds what actually landed.
func fallbackSidecar(completeErr error, result string) string {
	if completeErr != nil {
		return ""
	}
	return result
}

// backstopTimer is the subset of *time.Timer the deferred backstop needs. It
// is an interface so tests can substitute a hand-fired fake and drive both
// directions of the acceptance control deterministically, without waiting real
// wall-clock time.
type backstopTimer interface {
	Stop() bool
}

// deferredBackstop is the bounded safety net for --defer-done polecats (gh
// #81). When a deferred polecat's branch merges, reapMergedPolecat arms a
// timer here instead of stopping it. Two outcomes:
//
//   - The polecat finishes its post-merge flow, calls `mg done`, and its
//     process ends. pogod's OnExit hook calls cancel(), which disarms the
//     timer — the clean, common path.
//   - The polecat never ends its lifecycle. The timer fires after the bounded
//     window: fire() reaps the still-running process and escalates to the
//     mayor, so a deferred polecat can never silently LINGER holding its slot
//     (the gh #34/#35 regression this backstop exists to prevent).
//
// A third outcome was unhandled until mg-c8d5: the polecat DIES between the
// merge and `mg done`. cancel() disarmed the timer on any process exit, so that
// death drew no escalation and — because a self-exit never goes through
// Registry.Stop — left the work item stranded in claimed/ under a dead pid, the
// mg-fb13 leak through the one door mg-fb13 did not close. cancel() now settles
// the exit instead of merely disarming it.
//
// All state is guarded by mu; arm/cancel/fire are safe to call concurrently
// (arm runs on the refinery loop's reap goroutine, cancel on the OnExit hook,
// fire on the timer goroutine).
type deferredBackstop struct {
	mu      sync.Mutex
	timeout time.Duration
	timers  map[string]backstopTimer // keyed by registry name (bare id)
	// merged snapshots the MergeRequest each armed polecat merged, keyed like
	// timers. fire() carries its own copy in the timer closure; cancel() needs
	// one too, because the death path escalates with the same context and runs
	// off a hook that only knows the agent.
	merged   map[string]*refinery.MergeRequest
	reg      polecatReaper
	escalate func(mr *refinery.MergeRequest)

	// escalateDeath reports a deferred polecat that DIED after its merge landed
	// and before its work item left claimed/ — a merged branch whose post-merge
	// flow (PR creation, verify, mail) never finished and whose item pogod has
	// just returned to available/. Distinct from escalate, which reports the
	// opposite failure: a polecat that stayed alive too long. Nil disables the
	// mail; the release still happens (mg-c8d5).
	escalateDeath func(mr *refinery.MergeRequest, a *agent.Agent, released bool, releaseErr error)

	// workItemDone reports whether the deferred polecat's work item reached a
	// terminal state. A polecat that opened its PR, mailed it, and called
	// `mg done` is told by its own protocol to stay alive until the mayor
	// stops it — so a live process at the deadline does NOT imply an unfinished
	// flow. Consulted before escalating so that common, healthy case is reaped
	// quietly rather than reported as a failure (mg-7746). Nil (or an error
	// from the probe) means "unknown", and the conservative escalate-anyway
	// path applies.
	workItemDone func(id string) (bool, error)

	// afterFunc schedules f to run after d and returns a stoppable handle.
	// Defaults to time.AfterFunc; tests inject a fake for deterministic firing.
	afterFunc func(d time.Duration, f func()) backstopTimer
}

// newDeferredBackstop builds a backstop that reaps via reg and escalates via
// the given mailer. escalate may be nil (reap without a mail — the process is
// still freed). The caller sets workItemDone if it wants completed-but-lingering
// polecats distinguished from stalled ones; leaving it nil keeps the
// conservative escalate-on-every-fire behaviour.
func newDeferredBackstop(timeout time.Duration, reg polecatReaper, escalate func(mr *refinery.MergeRequest)) *deferredBackstop {
	return &deferredBackstop{
		timeout:   timeout,
		timers:    make(map[string]backstopTimer),
		merged:    make(map[string]*refinery.MergeRequest),
		reg:       reg,
		escalate:  escalate,
		afterFunc: func(d time.Duration, f func()) backstopTimer { return time.AfterFunc(d, f) },
	}
}

// arm starts the bounded backstop for a merged --defer-done polecat. name is
// the registry name (bare id); mr carries the escalation context. Re-arming an
// already-armed polecat is a no-op — the first deadline stands.
func (b *deferredBackstop) arm(name string, mr *refinery.MergeRequest) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.timers[name]; ok {
		return
	}
	// Snapshot the MR: the caller's *mr may be mutated/pruned after arm returns,
	// but the escalation, minutes later, must report the state at merge time.
	mrCopy := *mr
	b.merged[name] = &mrCopy
	b.timers[name] = b.afterFunc(b.timeout, func() { b.fire(name, &mrCopy) })
	log.Printf("refinery: armed defer-done backstop for polecat %s — reaps + escalates if it does not complete within %s (gh #81)", name, b.timeout)
}

// cancel disarms the backstop for a polecat whose process has ended, and then
// settles what that exit meant. Called from pogod's OnExit hook. Safe to call
// for an agent that was never armed (the common case — most polecats are not
// deferred), in which case it does nothing at all.
//
// Disarming alone was the whole of this function until mg-c8d5, and it treated
// every exit as a clean completion. It is not: an armed polecat has merged and
// been left running precisely because it still owes work, so its process ending
// is only good news if the work item actually left claimed/. See settleExit.
func (b *deferredBackstop) cancel(a *agent.Agent) {
	if b == nil || a == nil {
		return
	}
	name := a.Name
	b.mu.Lock()
	t, armed := b.timers[name]
	mr := b.merged[name]
	if armed {
		t.Stop()
		delete(b.timers, name)
		delete(b.merged, name)
	}
	b.mu.Unlock()
	if !armed {
		return
	}
	log.Printf("refinery: disarmed defer-done backstop for polecat %s — its process ended (gh #81)", name)
	b.settleExit(a, mr)
}

// settleExit decides what a deferred polecat's OWN exit meant, and is the
// deferred-death half of the claim-leak fix (mg-c8d5).
//
// The question is not "did the process end" — cancel already knows that — but
// "did it end having handed its work item on". A polecat in this lane merged,
// was deliberately not auto-done'd, and owes a PR plus its own `mg done`. Two
// endings look identical to OnExit:
//
//   - It finished: `mg done` moved the item out of claimed/, the mayor stopped
//     it (or it exited), and there is nothing to clean up.
//   - It died mid-flow: crash, OOM, harness failure, an operator kill. The
//     branch is merged, the PR is not open, and the item sits in claimed/ under
//     a pid that no longer exists — never dispatched again, and invisible to
//     stall-watch, which only scans available/.
//
// The claim file is what tells them apart, so that is what is asked. The
// release is scoped to this polecat's own work item and is a no-op when the
// claim is not held, which also makes the ordinary mayor-initiated stop quiet:
// that path goes through Registry.Stop, which has already released.
//
// Only a claim that was actually stranded (or one we failed to release)
// escalates. Escalating on every deferred exit would page the mayor for every
// clean PR-flow completion and for every agent lost to a fleet drain.
func (b *deferredBackstop) settleExit(a *agent.Agent, mr *refinery.MergeRequest) {
	released, err := b.reg.ReleaseClaimAfterExit(a)
	if !released && err == nil {
		log.Printf("refinery: deferred polecat %s exited with no claim held on %s — its post-merge flow completed (gh #81)", a.Name, a.WorkItemID)
		return
	}
	if err != nil {
		log.Printf("refinery: deferred polecat %s DIED holding the claim on %s and the release FAILED: %v — the item is stranded in claimed/ under a dead pid; release it by hand with `mg unclaim %s` (mg-c8d5)", a.Name, a.WorkItemID, err, a.WorkItemID)
	} else {
		log.Printf("refinery: deferred polecat %s DIED between its merge and `mg done` — released the claim on %s so the item returns to available/ (mg-c8d5)", a.Name, a.WorkItemID)
	}
	if b.escalateDeath != nil {
		b.escalateDeath(mr, a, released, err)
	}
}

// fire runs when a deferred polecat outlives the backstop window without
// ending its lifecycle. It reaps the lingering process and escalates to the
// mayor. If the polecat has already left the registry (it exited in the race
// between the timer firing and cancel acquiring the lock), there is nothing to
// reap and no escalation is sent.
func (b *deferredBackstop) fire(name string, mr *refinery.MergeRequest) {
	b.mu.Lock()
	// If cancel already removed our entry, a concurrent cancel won — stand down.
	if _, ok := b.timers[name]; !ok {
		b.mu.Unlock()
		return
	}
	delete(b.timers, name)
	delete(b.merged, name)
	b.mu.Unlock()

	a := b.reg.GetByWorkItemOrName(name)
	if a == nil {
		// Raced with a clean exit — the slot is already free, no escalation.
		log.Printf("refinery: defer-done backstop for polecat %s fired but the polecat was already gone — no action (gh #81)", name)
		return
	}

	// Did the polecat actually finish? A PR-flow polecat that opened its PR and
	// called `mg done` is instructed to stay alive until the mayor stops it, so
	// "still running" is not evidence of a stalled flow. Reap it either way —
	// slot protection is the point — but only escalate when the work is
	// genuinely incomplete (mg-7746).
	completed := false
	if b.workItemDone != nil && mr.Author != "" {
		done, err := b.workItemDone(mr.Author)
		if err != nil {
			log.Printf("refinery: defer-done backstop could not read the state of work item %s (%v) — escalating conservatively", mr.Author, err)
		} else {
			completed = done
		}
	}

	if completed {
		log.Printf("refinery: defer-done backstop fired for polecat %s — work item %s is already done, so its post-merge flow completed; reaping the lingering process to free its slot, no escalation (mg-7746)", name, mr.Author)
	} else {
		log.Printf("refinery: defer-done backstop FIRED for polecat %s — merged but did not complete within %s; reaping + escalating (gh #34/#35 slot protection, gh #81)", name, b.timeout)
	}
	if err := b.reg.StopWithCause(a.Name, mergedPolecatStopTimeout, agent.StopCauseMergeBackstop); err != nil {
		log.Printf("refinery: defer-done backstop failed to stop lingering polecat %s: %v", a.Name, err)
	}
	if !completed && b.escalate != nil {
		b.escalate(mr)
	}
}
