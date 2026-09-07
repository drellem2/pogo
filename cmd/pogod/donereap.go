package main

import (
	"log"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/filernotify"
)

// The done-item reaper: completion, not merge, is what frees a slot (mg-56d1).
//
// THE DEFECT. Every automatic polecat teardown in this daemon hangs off ONE
// event: the refinery reporting a successful merge (reapMergedPolecat, gh #35).
// That is the right hook for a build polecat, whose deliverable IS a branch. It
// reaches nothing else. A triage polecat, an audit-only polecat, an
// investigation polecat — none of them merge anything. They finish by calling
// `mg done` themselves, and at that moment nothing in the fleet is watching:
//
//	merge-producing polecat  -> refinery merges -> pogod stops it, marks done  (automatic)
//	non-merge polecat        -> calls `mg done` -> nothing whatsoever          (manual)
//
// Measured 2026-07-30: polecat d764 finished its triage, delivered its packet,
// filed its successor item, went idle, and held one of five concurrency slots
// for 7m16s with two high-priority items queued and undispatchable. Nothing
// would have ended it but a coordinator noticing. Worse, the loss is invisible
// in the shape operators actually look at: `pogo agent list` says
// status=running, and only `pogo agent diagnose` says idle. It reads exactly
// like healthy saturation. Same family as mg-18d0 — the list reports LIVENESS,
// never PRODUCTIVITY.
//
// WHICH SIGNAL THIS KEYS ON, AND WHY IT IS NOT THE MERGE HOOK. The condition is
// the WORK ITEM reaching a terminal state (done/archived). Merge is one path to
// that state; `mg done` is the other; `done` is the general fact both produce.
// Extending the merge hook would have been the smaller change and would have
// fixed nothing here, because the polecats that leak are precisely the ones the
// refinery never hears about. So the merge hook stays exactly as it is — it has
// obligations this reaper does not (writing the completion, honouring
// --defer-done / PR-flow / post-merge-work, arming the deferred backstop) — and
// this is a second, independent detector keyed on the general condition.
//
// WHY IT POLLS RATHER THAN HOOKS THE TRANSITION. `mg done` runs in the
// POLECAT's own process, as a separate `mg` binary against the macguffin store.
// pogod is not in that call path and macguffin offers it no callback: OnMerged
// exists only because the refinery lives INSIDE pogod. So `done` can be
// observed but not delivered, and observing it is a per-polecat `mg show
// --json` on the heartbeat tick — the same shape as every other detector here.
// Calling this "event-driven" would be a claim the wiring cannot support.
//
// WHY `done` ALONE IS NOT SUFFICIENT. The polecat protocol tells a polecat to
// call `mg done` and then STAY ALIVE until the coordinator stops it, and work
// legitimately follows that call: mailing a verdict packet, filing a successor
// item, answering a coordinator follow-up. LivePolecatSet's doc names
// done-but-alive as "every polecat's NORMAL end state". Stopping on the `done`
// write alone would kill a polecat mid-sentence — the one outcome strictly
// worse than the leak, because a lost verdict is unrecoverable and a held slot
// is merely expensive.
//
// So the condition is a CONJUNCTION: the item is terminal AND the polecat has
// been quiet on its PTY for doneReapIdleGrace.
//
// # THE REVIEW EXEMPTION (mg-aaf6, drellem2/pogo#131)
//
// The conjunction above is right for a polecat whose deliverable is finished. It
// is WRONG for a builder on the gh-issue PR track, and the difference cost a
// review round twice. There, the build item is supposed to stay `claimed`
// through the whole review — the coordinator submits the branch and pogod closes
// the item at merge — but a builder that calls `mg done` at PR-open anyway looks
// from here exactly like a finished triage polecat: terminal item, quiet PTY.
// Two minutes later it is gone, and the review polecat that was about to mail it
// findings has no counterparty. The round dies leaving nothing that says why.
//
// The waits are not marginal. Measured over 17 real between-round builder waits
// in this fleet's mail: median 8.3m, max 20.0m, and 15 of the 17 (88%) longer
// than the two-minute grace. A self-closed builder is reaped in the ORDINARY
// case here, not an edge one.
//
// So a third clause: the item is terminal AND the polecat is quiet AND no live
// polecat's work item declares `reviews: <this item>`.
//
// WHY THAT DECLARATION AND NOT A TAG THE COORDINATOR CLEARS. The rejected shape
// was a tag on the build item removed at the pass/abort transition. It fails for
// the reason this whole ticket exists: it is enforcement by instruction-
// following, and the report is that instruction-following failed twice. A
// declaration someone must remember to CLEAR is state whose drift leaves no
// artifact — forget it once and the item holds a dispatch slot forever, against
// the per-repo cap (gh#128), with nothing anywhere saying so. That is mg-56d1's
// leak returning through the fix for it. A declaration written ONCE AT CREATION
// cannot rot, because its lifetime is bounded by something else's.
//
// AND IT IS SELF-CLEARING BY CONSTRUCTION: the exemption exists only while a
// review polecat is ALIVE. When the reviewer exits — stopped by the coordinator
// at the verdict, or reaped by this very reaper once its own item is done and it
// goes quiet — the exemption evaporates on the next tick and the builder is
// reaped normally. Nobody has to remember anything, and no ceiling has to be
// guessed. That matters: a 15m ceiling (deferDoneBackstopTimeout, the analogous
// number) was priced against those same 17 waits and 2 of them exceeded it, so a
// ceiling would have reaped live work about one round in eight.
//
// WHY IT KEYS ON A CARRIER LINE AND NOT ON `depends`. `--depends` carries
// DISPATCH semantics: it would gate the review ticket behind the build ticket,
// and the build ticket cannot clear because it stays claimed through review. The
// coordinator therefore files the review ticket with no depends edge on purpose
// (mayor.md), so there is no edge here to read. The other candidate joins were
// measured over the live store and both fail: a `gh:` ref join is ambiguous the
// moment an issue is split into parts — gh#131 itself has two build carriers
// sharing one ref — and a prose `mg-xxxx` scan is ambiguous in 17 of 23 real
// cases, because review bodies name the triage ticket too.
//
// WHAT ITS OWN SILENCE LOOKS LIKE. An exemption that is never granted because it
// is misconfigured and an exemption correctly not needed produce the same
// nothing, so this one keeps a POSITIVE RECORD: the grant is logged once when it
// starts, and the eventual reap says the polecat had been exempt and names the
// reviewer that is now gone. A reader can see the guard fire and see it expire
// without inferring either from an absence.
//
// TWO RESIDUALS, STATED RATHER THAN LEFT TO BE FOUND.
//
// The larger one: A REVIEW TICKET FILED WITHOUT THE LINE IS SILENT. The guard
// simply never fires, the round runs exactly as it did before, and the builder
// is reaped mid-review with nothing anywhere saying a declaration was missing.
// That is an instruction-following dependency, which is the very thing this
// design removes everywhere else — so it is worth being plain about where it
// survives. It is narrower than what it replaces, and the narrowing is the whole
// argument: the coordinator must write one line at the moment it is already
// writing the same fact in prose one line below, versus having to REMEMBER, at a
// transition minutes or hours later, to go back and clear something. A missed
// write costs one round the protection it would have had; a missed clear would
// have cost a dispatch slot indefinitely. It is also detectable after the fact —
// a review ticket with no `reviews:` line is a grep — where a stale declaration
// is indistinguishable from a live one.
//
// The smaller one: a review polecat that wedges without exiting holds its
// builder exempt for as long as it lives. That is bounded by the reviewer's own
// lifetime and is the same exposure the wedged reviewer already represents on
// its own slot — orphanwatch's and stallwatch's question, not this reaper's.
//
// GRACE PERIOD, NOT AN EXPLICIT "I AM FINISHED" SIGNAL. The alternative was to
// have polecats declare completion. It was rejected: a signal the polecat must
// remember to send fails for exactly the polecats that most need stopping — the
// ones that ran off the end of their protocol — and this tree has already
// measured that failure mode (mg-ddf7's ack deficit: an instructed step that a
// third of agents simply never perform). A grace period asks the polecat for
// nothing.
//
// The grace is measured from the LAST PTY WRITE, not from the `done`
// transition, and that choice does the post-`done` work case for free. A
// timer from the transition cannot tell a polecat still typing from one that
// stopped; PTY quiescence can. It also self-extends in the one case that
// worried us: an incoming coordinator mail is delivered as a PTY nudge, the
// answer is more PTY output, so a polecat handling a follow-up keeps resetting
// its own clock and is only reaped once it goes quiet again.
//
// WHAT IT DELIBERATELY CANNOT DO. It never marks an item done (the item is
// already done — that is the trigger) and never restarts. The
// restart-suppression rules (mg-18d0/mg-8cdb) are therefore not in tension with
// it: stopping a finished polecat without replacing it is the correct end of a
// lifecycle, not remediation. And a polecat whose item is still `claimed` is
// untouchable no matter how long it idles — item state is the gate, idleness is
// only the qualifier that says "not mid-sentence". That is the acceptance
// control for this ticket: the healthy 42-minute idle polecat mid-work must
// survive, and it does so structurally rather than by tuning.
//
// # THE GATE REAP (mg-9af1)
//
// Everything above keys on the item being TERMINAL, and there is one worker in
// this fleet whose item is deliberately never allowed to get there. A gh-issue
// TRIAGE polecat is told, correctly, NOT to call `mg done`: no successor exists
// until the coordinator files the build ticket after the human gate, so closing
// the item would retire a ticket whose decision has not been made. The
// instruction is right and it stays. Its consequence is that the polecat is
// finished — packet delivered, durable on the ticket body — and this reaper's
// condition can never be satisfied, so it holds a worker slot until a person
// answers. On a `stage: gated` ticket that wait is UNBOUNDED: mayor.md's own
// gate semantics are "silence = HOLD ... however long that takes".
//
// Neither half was wrong and nothing joined them. Measured 2026-08-12: p634e
// delivered its gh#135 packet at 14:28Z and at 14:38Z was still running,
// healthy, "last-activity: just now", while mg-0a43 (high) and five other pogo
// items sat undispatchable behind that repo's cap. p1539 and pc4a9 the same day.
// Measured again 2026-09-07: t7476 delivered its gh#161 packet at 16:34Z and
// held a slot with ten issues queued behind a three-slot cap. Every one of them
// was stopped by hand, and none of them looked anything but healthy.
//
// SO THE CONDITION IS A DISJUNCTION AT THE TOP AND THE SAME CONJUNCTION BELOW:
// the item is terminal OR parked at `stage: gated`, AND the polecat is quiet,
// AND no live reviewer declares it. The gate branch is a second way to satisfy
// "this worker has finished", not a second reaper — a separate detector would
// have to re-derive quietness, re-derive the review exemption, and could reach a
// different answer about the same polecat on the same tick.
//
// WHY RELEASING A GATED ITEM'S CLAIM IS SAFE, WHICH IS THE WHOLE QUESTION. This
// stop releases the claim (mg-fb13), so the item returns to available/ — and an
// available item on a queue the coordinator is actively feeding is a dispatch
// candidate. If it were re-dispatched, the new triage polecat's FIRST instructed
// act is to post an acknowledgement comment on a stranger's open GitHub issue.
// That does not happen, and the reason is not this file: `stage: gated` is
// enforced at the SPAWN POINT (mg-69b1) — config.IsStageGated, read by
// agent.MGDispatchGate, consulted by handleSpawnPolecat before any worktree
// exists. The gate already does the job "leave the polecat running so nothing
// re-dispatches it" was doing, at a layer that does not cost a worker slot.
//
// That dependency is LOAD-BEARING and it is checked rather than assumed: this
// branch uses config.IsStageGated itself, the same predicate the dispatch gate
// uses, so the two cannot drift into disagreeing about which word means gated.
// If that gate is ever narrowed, this reap must be re-argued in the same commit.
//
// WHY IT CANNOT REACH A BUILDER MID-REVIEW, WHICH IS THE OTHER HALF. The same
// ticket reports a build polecat idling after PR-open, and warns that stopping
// THAT one is unsafe: it owns an open PR, an unmerged branch, and the modify
// side of a live review loop, and there is no equivalent of "the packet is on
// the ticket". This branch cannot touch it, structurally rather than by a check:
// `stage: gated` is carried only by the TRIAGE ticket. mayor.md's state-carrier
// section assigns the stages to two different tickets — "the triage ticket
// carries triage -> gated; after the gate, the build ticket carries build ->
// review -> merge" — so a builder's item never reads `gated`, and the review
// exemption above still covers it if one ever did. The narrow fix stays narrow
// because the vocabulary is narrow, not because someone remembered to exclude a
// case. (The ticket's wider argument — that the per-repo cap counts WORKERS and
// worker count is not a measure of load, mg-eb47 and gh#128 — is a different
// accounting question in a different layer, and nothing here settles it.)
//
// WHAT IT DELIBERATELY DOES NOT DO: it does not close the item, does not move
// the stage, and does not notify the filer. The gate reap frees a WORKER; the
// ITEM stays exactly where the coordinator parked it, claimed by nobody,
// awaiting the decision. Reporting a completion here would be a straight
// falsehood — the reason the completion notice above is emitted on the terminal
// branch only, and the reason this stop carries its own cause
// (agent.StopCauseGateReap) rather than reusing done_reap: a reader holding the
// event record must not conclude the work finished.
//
// A PROBE ERROR LEAVES THE POLECAT RUNNING, and note that this reads the SAME
// error the opposite way from the dispatch gate. An unreachable carrier block
// (mg-27d4) makes the dispatch gate refuse — it may say `gated` — and makes this
// reaper decline to act — it may not. Both fail toward the recoverable outcome:
// a refused dispatch is retried and a held slot is merely expensive, while a
// polecat stopped on a guess is neither.
//
// TWO RESIDUALS, STATED RATHER THAN LEFT TO BE FOUND, and both are this
// reaper's own defect looked for in its remedy.
//
// The first: A TICKET THE COORDINATOR NEVER GATES IS SILENT HERE. The branch
// keys on a line somebody has to write, so a coordinator that stops at "mail the
// summary" leaves the polecat holding its slot exactly as before, with nothing
// saying a line was missing — the shape of the original defect, arriving through
// its fix. It is narrower than it sounds and the narrowing is not this file's
// doing: the same missing line leaves the ticket DISPATCHABLE, which is the
// mg-69b1 exposure and is loud in a way a held slot never was. So the omission
// costs a slot here and is caught there; it is not a second silent failure.
//
// The second: A GATED TICKET WHOSE BODY BECOMES UNREADABLE CANNOT BE REAPED.
// The triage polecat appends its packet to the ticket body while the reap is
// pending, and a bare unfenced `stage:` line anywhere in that append would make
// the carrier CarrierUnreadable — the probe would error, this branch would
// decline forever, and the polecat would hold its slot looking healthy. That is
// checked rather than reasoned about: on 2026-09-07 all 30 `stage: gated` items
// in the live store — every one of them carrying a delivered packet — parsed
// through client.MGWorkItemStage as `gated`, none errored, and the 29 non-gated
// items sampled beside them each read their own stage. The packet is written
// inside a ```json fence, and fenced content is what scanUnreachableCarrier
// skips, so the shape that would break this is the one the protocol already
// avoids for its own reasons.
//
// THE COST, STATED. The stage probe is a SECOND `mg show --json` per tick for
// each polecat that is quiet past the grace and whose item is not terminal. It
// is bounded by that conjunction — a busy polecat is never eligible and a
// terminal one never reaches this branch — and the tick is 30 seconds, so the
// steady-state price is one extra store read per idle-but-unfinished polecat per
// half-minute. Folding it into the terminal probe would have made one call do
// both, at the cost of a combined signature neither caller wants.
//
// # THE ONE THING IT DOES MAIL (mg-f120)
//
// "Never mails" held until mg-f120, and the exception is narrow enough to state
// exactly. This reaper is the ONLY place in the daemon that observes the close
// of a work item the daemon did not perform: `mg done` runs in the polecat's own
// process (see WHY IT POLLS above), and every other completion notice pogod
// sends hangs off the refinery's merge. So a triage, audit or investigation
// item — the ones with no branch — closed with nothing anywhere telling the
// agent that COMMISSIONED it, and this is the one vantage point from which that
// could be fixed.
//
// It is not an escalation and it is not remediation, which is what the original
// "never mails" was about: there is no fault here, and the mail goes to the
// item's own Creator rather than to a coordinator asked to intervene. The
// decision about who to tell, and whether to tell anyone, belongs entirely to
// internal/filernotify; this reaper hands over an observation and reads nothing
// back.

// doneReapIdleGrace is how long a polecat whose work item has reached a
// terminal state must be quiet on its PTY before it is stopped.
//
// Two minutes is chosen against the two failure costs, which are asymmetric.
// Too short kills a polecat mid-mail — unrecoverable. Too long re-creates the
// leak — merely expensive, and bounded. The post-`done` tail work a polecat
// actually does (one `mg mail send`, one `mg new`) is seconds; two minutes of
// unbroken silence is many multiples of it. The measured leak sat 7m16s, so
// this cuts it to under 25% while leaving a wide margin over any real tail. It
// is well inside deferDoneBackstopTimeout's 15 minutes, which bounds a
// different and looser question (did a deferred polecat's post-merge flow
// happen at all).
const doneReapIdleGrace = 2 * time.Minute

// doneReapRegistry is the slice of agent.Registry the done-item reaper needs.
// Narrow on purpose, like polecatReaper: it can read who is alive and it can
// stop one of them. It has no seam through which it could mark an item, mail,
// nudge, or spawn.
type doneReapRegistry interface {
	PolecatActivityAt(now time.Time) []agent.PolecatActivity
	StopWithCause(name string, timeout time.Duration, cause string) error
}

// doneReaper stops polecats whose work item has concluded and which have gone
// quiet. See the file comment for the reasoning behind the condition; this type
// is only the mechanics.
type doneReaper struct {
	reg doneReapRegistry
	// itemDone reports whether a work item reached a terminal state
	// (client.MGWorkItemDone in production). An ERROR means "cannot tell", and
	// the polecat is left alone: a store we could not read is not evidence of
	// completion. That direction matches resolvePostMergeWork, and for the same
	// reason — the expensive mistake here is asserting a completion we have no
	// standing to assert.
	itemDone func(id string) (bool, error)
	// itemReviews returns the BUILD item id that a work item's `reviews:` carrier
	// line names, or "" when it declares none (client.MGWorkItemReviews in
	// production). An ERROR means "cannot tell" — an unreadable store, or a
	// carrier block out of the parser's reach — and is LOGGED rather than read as
	// "declares nothing", because those two answers point opposite ways.
	//
	// Nil disables the exemption entirely and the reaper behaves exactly as it
	// did before mg-aaf6. That is the honest degraded mode for a daemon whose
	// probe did not wire up, but it is fail-OPEN — the builder gets reaped — so
	// production must always pass one.
	itemReviews func(id string) (string, error)
	// itemStage returns the `stage:` line of a work item's state carrier block,
	// or "" when it declares none (client.MGWorkItemStage in production). An
	// ERROR means "cannot tell" — an unreadable store, or a carrier block out of
	// the parser's reach — and leaves the polecat running.
	//
	// Nil disables the gate reap entirely and the reaper behaves exactly as it
	// did before mg-9af1: every test that predates this seam leaves it nil, and
	// each of them still asserts the terminal-state conjunction on its own. It is
	// fail-CLOSED for this branch — nothing is reaped that would not have been —
	// so an unwired daemon leaks a slot rather than stopping the wrong worker.
	// SetStageProbe is the one place production wires it.
	itemStage func(id string) (string, error)
	// grace is the required PTY quiet window. Zero falls back to
	// doneReapIdleGrace; a negative value would stop a done polecat the instant
	// it is seen, which only a test should ask for.
	grace time.Duration
	// filer is told that the item completed, so the agent that commissioned it
	// hears (mg-f120). Nil means not wired, which is what every test that is not
	// about this seam leaves it as.
	filer filerNotifier

	// mu serialises Check against itself, and guards exempt. The heartbeat fires
	// every ~30s and dispatches this in a goroutine, while a Check shells out to
	// `mg show` once per live polecat and can block on a slow store — so two
	// Checks can overlap. Without the guard the second would re-decide against
	// the same polecat the first is already stopping, and issue a duplicate Stop.
	mu       sync.Mutex
	checking bool
	// exempt maps a polecat name to the review item currently holding it back
	// from the reap, carried across ticks for ONE reason: so the grant is logged
	// when it starts rather than on every 30-second heartbeat for the twenty
	// minutes a review can run, and so the eventual reap can say the polecat had
	// been exempt. It is a log-deduplication record, never an input to the
	// decision — every tick re-derives the exemption from that tick's live set.
	exempt map[string]string
}

// newDoneReaper builds a reaper over reg. done is the terminal-state probe,
// reviews the `reviews:` carrier probe (nil disables the review exemption);
// grace of zero means doneReapIdleGrace.
func newDoneReaper(reg doneReapRegistry, done func(id string) (bool, error), reviews func(id string) (string, error), grace time.Duration) *doneReaper {
	if grace == 0 {
		grace = doneReapIdleGrace
	}
	return &doneReaper{reg: reg, itemDone: done, itemReviews: reviews, grace: grace}
}

// SetFilerNotifier wires the completion notification (mg-f120). Separate from
// newDoneReaper so that the reaper's constructor keeps saying what the reaper
// is FOR, and so the one place this is wired is a named line in main.
func (d *doneReaper) SetFilerNotifier(n filerNotifier) {
	if d == nil {
		return
	}
	d.filer = n
}

// SetStageProbe wires the gate reap's carrier-stage probe (mg-9af1). Separate
// from newDoneReaper for the reason SetFilerNotifier is: the constructor keeps
// saying what the reaper is FOR — a polecat whose item is finished — and the
// gate branch is a second answer to that question rather than a change to it.
func (d *doneReaper) SetStageProbe(stage func(id string) (string, error)) {
	if d == nil {
		return
	}
	d.itemStage = stage
}

// gatedStage returns the item's `stage:` value when it is one this fleet gates
// on, and "" when it is not — the gate-reap branch's whole test (mg-9af1).
//
// The gating predicate is config.IsStageGated, DELIBERATELY the same one the
// dispatch gate applies at the spawn point. That shared predicate is what makes
// this reap safe: the item this stops is by construction an item that cannot be
// re-dispatched, and a local re-implementation of "does this stage gate" is how
// those two facts would quietly stop being the same fact.
func (d *doneReaper) gatedStage(id string) (string, error) {
	if d.itemStage == nil {
		return "", nil
	}
	stage, err := d.itemStage(id)
	if err != nil {
		return "", err
	}
	if !config.IsStageGated(stage) {
		return "", nil
	}
	return stage, nil
}

// Check samples the live polecats and stops every one whose work item has
// concluded and which has been quiet for at least the grace window. Safe to
// call from the heartbeat tick in a goroutine; overlapping calls return
// immediately.
//
// Returns the names it stopped, in the order it stopped them, so a test can
// assert BOTH arms of the acceptance control from one call: who was reaped, and
// by omission who was not.
func (d *doneReaper) Check(now time.Time) []string {
	if d == nil || d.reg == nil || d.itemDone == nil {
		return nil
	}
	d.mu.Lock()
	if d.checking {
		d.mu.Unlock()
		return nil
	}
	d.checking = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.checking = false
		d.mu.Unlock()
	}()

	live := d.reg.PolecatActivityAt(now)

	// The review exemption is resolved LAZILY: it costs one `mg show` per live
	// polecat, and on the overwhelming majority of ticks nothing is terminal, so
	// there is nobody it could exempt. Built at most once per Check, and only
	// once a candidate for the reap actually appears.
	var reviewedBy map[string]string
	openReviews := func() map[string]string {
		if reviewedBy == nil {
			reviewedBy = d.openReviews(live)
		}
		return reviewedBy
	}

	var stopped []string
	seen := make(map[string]bool, len(live))
	for _, p := range live {
		seen[p.Name] = true
		if !d.eligible(p) {
			continue
		}
		done, err := d.itemDone(p.WorkItemID)
		if err != nil {
			// Cannot read the item: say so and leave the polecat running. This
			// is one of the two branches that log a non-action, because they are
			// the ones where the reaper wanted to decide and could not.
			log.Printf("donereap: could not read work item %s for polecat %s (%v) — leaving it running (mg-56d1)", p.WorkItemID, p.Name, err)
			continue
		}
		// The gate branch (mg-9af1). Reached only when the item is NOT terminal,
		// because a done item has already answered the question this asks and
		// because the terminal branch carries obligations this one must not: the
		// gate reap reports no completion, since none has happened.
		gatedAt := ""
		if !done {
			stage, err := d.gatedStage(p.WorkItemID)
			if err != nil {
				// The second of the two branches that log a non-action: the
				// reaper wanted to decide and could not. Left running, which is
				// the direction that costs a slot rather than a worker.
				log.Printf("donereap: could not read the `stage:` carrier on work item %s (polecat %s): %v — "+
					"leaving it running (mg-9af1)", p.WorkItemID, p.Name, err)
				continue
			}
			if stage == "" {
				continue
			}
			gatedAt = stage
		}
		if done {
			// The item is closed. Tell whoever commissioned it (mg-f120) — here,
			// before the exemption and the stop, because the fact being reported is
			// the CLOSE, not the reap: an exempt builder's item is just as closed as
			// a reaped one's, and a stop that fails does not un-close it. The
			// notifier dedups, so repeating this on every tick sends one mail.
			notifyFiler(d.filer, filernotify.Completion{
				ItemID: p.WorkItemID,
				Route:  filernotify.RouteSelfClose,
				Worker: p.Name,
				// Earned, not assumed: this reaper reaches here only for an item
				// this tick READ as terminal (LivePolecatSet's Done), which is the
				// evidence the merge route lacked when it asserted the same thing
				// (mg-2b71).
				Closed: true,
			})
		}
		if reviewer, held := openReviews()[p.WorkItemID]; held {
			// The gh#131 case, caught. Logged ONCE per grant rather than every
			// heartbeat — a review runs for a median of 8 minutes and this tick
			// fires every 30 seconds — but logged, because a guard whose only
			// evidence is an absence cannot be told from a guard that never ran.
			if d.exempt[p.Name] != reviewer {
				// "done" or "parked at a gate" — the sentence names which,
				// because the exemption covers both branches now and an
				// EXEMPTING line that asserts the wrong one is a false statement
				// about the item in the one record that says the guard fired.
				why := "is done"
				if gatedAt != "" {
					why = "is parked at `stage: " + gatedAt + "`"
				}
				log.Printf("donereap: EXEMPTING polecat %s — its work item %s %s, but review item %s declares `reviews: %s` "+
					"and its polecat is still running; not reaping while the review loop has a counterparty (mg-aaf6, gh#131)",
					p.Name, p.WorkItemID, why, reviewer, p.WorkItemID)
			}
			if d.exempt == nil {
				d.exempt = map[string]string{}
			}
			d.exempt[p.Name] = reviewer
			continue
		}
		// The healthy, expected end of a non-merge polecat's life. Logged at
		// info, never mailed: there is no fault here to escalate, and an
		// escalation per completed triage would be pure noise.
		//
		// The lapse is recorded HERE — before the Stop, and dropping the record in
		// the same breath — because the exemption has already ended by this point:
		// it is a fact about the reviewer being gone, not about the stop
		// succeeding. Clearing it now is also what keeps the expiry line to ONE
		// occurrence. Left until after a successful Stop, a polecat whose Stop
		// keeps failing would re-announce the same lapse on every tick forever,
		// and a positive record that repeats is a positive record nobody reads.
		if reviewer, was := d.exempt[p.Name]; was {
			// The other half of the positive record: the exemption EXPIRED. This
			// is what says the guard is bounded by the reviewer's lifetime rather
			// than holding a slot forever.
			log.Printf("donereap: review exemption for polecat %s has LAPSED — review item %s no longer has a running polecat; "+
				"reaping normally (mg-aaf6)", p.Name, reviewer)
			delete(d.exempt, p.Name)
		}
		cause := agent.StopCauseDoneReap
		if gatedAt != "" {
			// Distinct cause and distinct sentence, because the two stops mean
			// different things about the ITEM. This one says the worker is
			// released and the ticket is still parked; done_reap says the ticket
			// is closed. A reader holding the event record has no other way to
			// tell them apart.
			cause = agent.StopCauseGateReap
			log.Printf("donereap: stopping polecat %s — work item %s is parked at `stage: %s` (a HUMAN DECISION gate it will "+
				"not leave through `done`) and it has been idle %s (>= %s); freeing its slot. The item stays claimed by nobody "+
				"and gated against re-dispatch (mg-69b1); it is NOT closed (mg-9af1)",
				p.Name, p.WorkItemID, gatedAt, p.IdleFor.Truncate(time.Second), d.grace)
		} else {
			log.Printf("donereap: stopping polecat %s — work item %s is done and it has been idle %s (>= %s); freeing its slot (mg-56d1)",
				p.Name, p.WorkItemID, p.IdleFor.Truncate(time.Second), d.grace)
		}
		if err := d.reg.StopWithCause(p.Name, mergedPolecatStopTimeout, cause); err != nil {
			// Losing the race with a clean exit lands here ("agent not found"),
			// as does a genuine stop failure. Either way the next tick re-decides
			// from a fresh snapshot, so there is nothing to retry inline.
			log.Printf("donereap: failed to stop polecat %s: %v", p.Name, err)
			continue
		}
		stopped = append(stopped, p.Name)
	}
	// Forget polecats that are gone, so the map cannot grow across the lifetime
	// of the daemon. Dropping an entry only re-arms the grant log; it can never
	// change a decision.
	for name := range d.exempt {
		if !seen[name] {
			delete(d.exempt, name)
		}
	}
	return stopped
}

// openReviews maps each BUILD item that a LIVE polecat's work item declares it
// reviews, to that reviewer's own item id. Membership is the exemption: the
// builder is held back for exactly as long as a reviewer is running against it.
//
// Liveness comes from the same snapshot the reap decision is made against, so
// there is no window in which the exemption is computed from a different fleet
// than the one being reaped.
//
// A probe error is LOGGED and the reviewer contributes no exemption. That is
// fail-open — the builder can then be reaped — and it is stated here rather than
// hidden because the alternative is worse: inventing a target we could not read
// would hold an arbitrary builder against a declaration nobody has seen. The log
// line is what makes the difference visible; the store being unreadable is
// already the loud condition it needs to be.
func (d *doneReaper) openReviews(live []agent.PolecatActivity) map[string]string {
	out := make(map[string]string)
	if d.itemReviews == nil {
		return out
	}
	for _, p := range live {
		if p.WorkItemID == "" {
			continue
		}
		target, err := d.itemReviews(p.WorkItemID)
		if err != nil {
			log.Printf("donereap: could not read the `reviews:` declaration on work item %s (polecat %s): %v — "+
				"no review exemption from it this tick (mg-aaf6)", p.WorkItemID, p.Name, err)
			continue
		}
		if target == "" || target == p.WorkItemID {
			// A self-reference declares nothing useful and would exempt a polecat
			// from its own completion forever. Never written by the coordinator;
			// refused here so it cannot be.
			continue
		}
		out[target] = p.WorkItemID
	}
	return out
}

// eligible applies the two cheap, local gates before the reaper spends an `mg
// show` on a polecat. Split out so the tests can pin each refusal
// independently of the store probe.
func (d *doneReaper) eligible(p agent.PolecatActivity) bool {
	// No work item: nothing to ask, so nothing to conclude. A polecat spawned
	// without one is not on this lifecycle at all.
	if p.WorkItemID == "" {
		return false
	}
	// Never wrote to its PTY: its idleness is UNMEASURABLE, not zero (see
	// PolecatActivity.HasOutput). A polecat seconds into spawn and one wedged
	// before its first turn look identical here, and neither is finished.
	if !p.HasOutput {
		return false
	}
	return p.IdleFor >= d.grace
}
