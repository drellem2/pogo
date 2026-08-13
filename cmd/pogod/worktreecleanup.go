package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/gitgc"
)

// worktreeCleanupOutcome reports what cleanupAgentWorktree decided, so callers
// and tests can distinguish the three cases without parsing log lines.
type worktreeCleanupOutcome int

const (
	// worktreeReaped: the worktree was clean and has been removed.
	worktreeReaped worktreeCleanupOutcome = iota
	// worktreePreserved: the worktree held uncommitted work and was kept.
	worktreePreserved
	// worktreeUndetermined: dirtiness could not be determined — `git status`
	// failed — and the tree was kept rather than reaped (mg-4d45). Distinct
	// from worktreePreserved because the FACT is different: preserved means
	// "there is work here", undetermined means "I could not look". Folding
	// the second into the first would report a false claim about the tree.
	worktreeUndetermined
	// worktreeCleanupFailed: removal was attempted and errored.
	worktreeCleanupFailed
	// worktreeNone: the agent had no worktree.
	worktreeNone
)

// exitedAgent is the identity of the agent whose worktree is being cleaned up.
//
// IT IS A STRUCT BECAUSE THE DEFECT IT FIXES WAS A MISSING ARGUMENT (mg-32e3).
// This function took five positional strings, and `a.WorkItemID` — sitting right
// next to `a.Name` and `a.SourceRepo` at the one call site — was simply not among
// them. Nothing was broken, nothing logged, nothing could notice: a positional
// call that compiles is a call that looks complete. Named fields do not make an
// omission impossible, but they make it visible in the diff, and the empty case
// is now reported in the notice rather than silently dropping a paragraph (see
// workItemLine).
type exitedAgent struct {
	// Name is the bare registry name, e.g. "c32e3". This is what appears in
	// mail subjects and log lines, where "Polecat c32e3" is what a reader
	// recognises.
	Name string
	// EventAgent is the event-spine identity, e.g. "cat-c32e3", per the
	// convention in docs/event-log.md. Empty falls back to Name.
	EventAgent string
	// WorkItemID is the item this agent was working, e.g. "mg-32e3". THE FIELD
	// THIS TICKET EXISTS FOR. Empty for a crew agent or a polecat spawned
	// without `--id`.
	WorkItemID string
	// SourceRepo is the repository the worktree belongs to.
	SourceRepo string
	// WorktreeDir is the tree itself. Empty means the agent had none.
	WorktreeDir string
}

// eventIdentity returns the name this agent is known by on the event spine.
func (e exitedAgent) eventIdentity() string {
	if e.EventAgent != "" {
		return e.EventAgent
	}
	return e.Name
}

// cleanupAgentWorktree reaps an exited agent's worktree, PRESERVING it if it
// holds uncommitted work, and notifying the coordinator when it does.
//
// This is the operative removal path for `pogo agent stop` — it runs from the
// registry's onExit hook, which fires on every no-restart agent exit. It used
// to force-remove unconditionally and destroyed a mid-flight polecat's working
// tree, new race test included (mg-ee02).
//
// It lives here, as a named function rather than inline in the exit hook,
// because a decision nobody can call is a decision nobody can test — which is
// how the force-remove survived as long as it did.
//
// IT IS ALSO THE ONLY GUARD IN THIS DAEMON THAT SEES UNCOMMITTED WORK (mg-32e3).
// Every other defence against re-deriving a polecat's output — the spawn-time
// stranded-work refusal, `git cherry`, strandedwork.Inspect, the release-time
// report, the boot sweep and `pogo check-stranded` — is defined over PUSHED
// commits and is blind here by construction. `~/.pogo/polecats/qbe37` was
// preserved on 2026-08-10 holding 16 uncommitted paths including a 1450-line
// package that existed nowhere else on the machine, and `pogo gc` would
// eventually have reclaimed it.
//
// So this path carries the do-not-dispatch sentence that `work_item_stranded_push`
// carries for pushed work, and emits the fact as an event. Both halves needed the
// work-item id, and neither had it: the notice was composed from the agent name,
// the repo and the tree, so it could say "a tree is pinned, reclaim it with pogo
// gc" and could not say "do not dispatch a worker at this item". The fleet held
// both halves and combined neither — on 2026-08-10 the coordinator received two
// preservation notices for qbe37 and dispatched at its work item anyway, and the
// work survived because a human chain of mail reached the new polecat in time.
//
// mail is injected (client.SendMGMail in production) so the notification path
// is exercisable without a live daemon.
func cleanupAgentWorktree(
	a exitedAgent, coordinator string,
	mail func(to, from, subject, body string) error,
) worktreeCleanupOutcome {
	agentName, sourceRepo, worktreeDir := a.Name, a.SourceRepo, a.WorktreeDir
	if worktreeDir == "" {
		return worktreeNone
	}

	// The ownership argument CHANGES NOTHING, and that is the first thing to
	// know about it. Since gh #97 RemoveWorktree ignores its WorktreeOwner
	// entirely — every ownership reaches the same outcome on every path — so
	// OwnerGone is dormant API rather than the other half of a live decision.
	// Nothing in production constructs it: this hook and both gitgc sweep
	// call sites all pass OwnerUnproven. If you arrived here to pass OwnerGone
	// because some comment made the distinction sound load-bearing, there is
	// nothing for it to influence; see gitgc.WorktreeOwner.
	//
	// The reasoning behind the value is kept because it is what would be at
	// stake if a discriminator ever came back, not because it selects a branch
	// today. This hook fires AFTER the process has exited, so a naive read of
	// "liveness" would answer GONE here and reap (mg-4d45). The process being
	// dead is not the question. This tree belonged to an agent that was
	// RUNNING until moments ago; its files are that agent's in-flight work,
	// and an exit — normal, crashed, or force-stopped — says nothing about
	// whether the work was saved. Exactly one exit route reaches this hook
	// with work still in the tree, and it is the route that cost us a 201-line
	// race test.
	//
	// What actually saves that tree is not this argument but the two rules
	// RemoveWorktree applies unconditionally: a dirty tree is PRESERVED, and a
	// tree it could not read is REFUSED and reported. Both are handled below.
	err := gitgc.RemoveWorktree(sourceRepo, worktreeDir, gitgc.OwnerUnproven)

	var dwe *gitgc.DirtyWorktreeError
	var uwe *gitgc.UndeterminedWorktreeError
	switch {
	case errors.As(err, &dwe):
		// Preservation rather than refusal is deliberate, and the choice is
		// forced by where this code sits: the hook fires AFTER the process
		// has already exited. There is no stop left to refuse by the time we
		// get here. A pre-flight check in `pogo agent stop` could refuse, but
		// it would cover only operator-initiated stops — a polecat that
		// crashes with a dirty tree loses exactly as much and routes through
		// this same hook. Guarding here covers every exit route.
		//
		// The cost of preserving is a tree that pins its branch until someone
		// deals with it, so this must not be quiet.
		log.Printf("agent %s: PRESERVED worktree %s (work item %s) — %v",
			agentName, worktreeDir, workItemOrNone(a.WorkItemID), dwe)
		// The event goes on the spine FIRST and unconditionally, before any
		// mail is attempted, because the record half is what this path never
		// had. Its mail half worked — 22 delivered notices over three days —
		// and its record half did not exist, which is the exact mirror of
		// work_item_stranded_push, whose event half worked and whose mail half
		// was missing until mg-be37. Three days of this path had to be
		// reconstructed from unstructured `PRESERVED worktree` log lines.
		emitWorktreePreserved(a, "preserved", dwe, dwe.Error())
		if mail != nil && coordinator != "" {
			subject := fmt.Sprintf("preserved uncommitted work in %s's worktree", agentName)
			if a.WorkItemID != "" {
				// The prohibition goes in the SUBJECT, matching the
				// stranded-push alert, because the subject is the part that
				// gets skimmed and forwarded. A do-not-dispatch sentence in
				// paragraph four of a message filed under worktree hygiene is
				// a sentence that does not travel.
				subject = fmt.Sprintf("preserved uncommitted work in %s's worktree — do NOT dispatch at %s",
					agentName, a.WorkItemID)
			}
			body := fmt.Sprintf(
				"Polecat %s exited with uncommitted work in its worktree. The tree was PRESERVED "+
					"rather than reaped (mg-ee02), so nothing was lost.\n\n"+
					"  worktree:  %s\n  repo:      %s\n%s  %v\n\n"+
					"%s"+
					"Rescue what matters (it is still a live git worktree — `git -C %s status`), "+
					"then reclaim it with:\n\n  pogo gc --repo=%s --apply --force\n\n"+
					"%s"+
					"Until it is reclaimed this worktree keeps its branch checked out, so that "+
					"branch cannot be deleted — and reclaiming it destroys the only copy of "+
					"anything still uncommitted.",
				agentName, worktreeDir, sourceRepo, workItemLine(a.WorkItemID), dwe,
				dispatchWarning(a.WorkItemID, "preserved"),
				worktreeDir, sourceRepo, standingListNote(sourceRepo))
			if mErr := mail(coordinator, "pogod", subject, body); mErr != nil {
				log.Printf("agent %s: failed to mail preserved-worktree notice: %v", agentName, mErr)
				emitWorktreeNoticeUndelivered(a, coordinator, "preserved", mErr)
			}
		}
		return worktreePreserved
	case errors.As(err, &uwe):
		// Cannot-tell. The notice must NOT say "uncommitted work" — we do not
		// know that, and sending an operator to rescue files that may not
		// exist is its own failure. It says what actually happened: the check
		// broke, so the tree was kept.
		log.Printf("agent %s: KEPT worktree %s (work item %s) — %v",
			agentName, worktreeDir, workItemOrNone(a.WorkItemID), uwe)
		emitWorktreePreserved(a, "undetermined", nil, uwe.Error())
		if mail != nil && coordinator != "" {
			subject := fmt.Sprintf("could not check %s's worktree for uncommitted work — kept it", agentName)
			if a.WorkItemID != "" {
				subject = fmt.Sprintf("could not check %s's worktree for uncommitted work — "+
					"kept it; do NOT dispatch at %s until it is read", agentName, a.WorkItemID)
			}
			body := fmt.Sprintf(
				"Polecat %s exited and its worktree could NOT be checked for uncommitted work — "+
					"`git status` failed. The tree was KEPT rather than reaped (mg-4d45).\n\n"+
					"This is not a report that there IS work here; it is a report that we could not "+
					"look. `git status` fails when .git is damaged, the disk is unhappy, or "+
					"permissions are broken — which is also when working files are least "+
					"reproducible, so the tree is kept until a human decides.\n\n"+
					"  worktree:  %s\n  repo:      %s\n%s  %v\n\n"+
					"%s"+
					"Inspect it (`ls %s`, `git -C %s status`), rescue anything that matters, then "+
					"reclaim it with:\n\n  pogo gc --repo=%s --apply --force\n\n"+
					"%s"+
					"Until it is reclaimed this worktree keeps its branch checked out, so that "+
					"branch cannot be deleted.",
				agentName, worktreeDir, sourceRepo, workItemLine(a.WorkItemID), uwe,
				dispatchWarning(a.WorkItemID, "undetermined"),
				worktreeDir, worktreeDir, sourceRepo, standingListNote(sourceRepo))
			if mErr := mail(coordinator, "pogod", subject, body); mErr != nil {
				log.Printf("agent %s: failed to mail undetermined-worktree notice: %v", agentName, mErr)
				emitWorktreeNoticeUndelivered(a, coordinator, "undetermined", mErr)
			}
		}
		return worktreeUndetermined
	case err != nil:
		log.Printf("agent %s: worktree cleanup failed: %v", agentName, err)
		return worktreeCleanupFailed
	default:
		log.Printf("agent %s: removed worktree %s", agentName, worktreeDir)
		return worktreeReaped
	}
}

// workItemOrNone renders a work-item id for a log line, saying so when there
// isn't one rather than printing an empty field.
func workItemOrNone(id string) string {
	if id == "" {
		return "none recorded"
	}
	return id
}

// workItemLine is the work-item row of a preservation notice.
//
// IT IS NEVER EMPTY, and that is deliberate. The obvious shape — omit the row
// when there is no id — reproduces this ticket's defect at the reporting layer:
// an absent field and a field nobody passed look identical, which is exactly how
// `a.WorkItemID` went five arguments unnoticed. A crew agent legitimately has no
// item; a polecat with none is a broken agent record, and the reader is the only
// one who can tell which. So the notice says which case it is.
func workItemLine(id string) string {
	if id == "" {
		return "  work item: NONE RECORDED for this agent — expected for a crew agent, but a " +
			"POLECAT with no work item means its agent record lost the link between this tree " +
			"and the item it was working (mg-32e3). Check `pogo agent list` and the branch name.\n"
	}
	return fmt.Sprintf("  work item: %s\n", id)
}

// dispatchWarning is the sentence this whole ticket exists to make sayable.
//
// The preserved-worktree notice knew the tree and not the item, so it could only
// speak about worktree hygiene. `work_item_stranded_push` could say "do NOT
// dispatch a worker at this item" and is defined over PUSHED commits, so it never
// fires for a tree whose work was never committed. Same sentence, other half of
// the fleet, and until now nothing joined them.
//
// The two outcomes get DIFFERENT text because they are different claims. For a
// preserved tree we have positively read uncommitted files, and the prohibition
// is unqualified. For an undetermined tree we established only that we could not
// look — asserting there is work here would send a reader hunting files that may
// not exist, so it says what is true: the item cannot be certified safe until
// somebody reads the tree.
func dispatchWarning(workItemID, outcome string) string {
	if workItemID == "" {
		return ""
	}
	common := fmt.Sprintf(
		"The board will show %s as available and priority-wake will advertise it as unclaimed and "+
			"ready. That advice is wrong until this tree has been rescued or ruled on, and NOTHING "+
			"ELSE WILL TELL YOU: every other guard against re-deriving a polecat's work — the "+
			"spawn-time stranded-work refusal, `git cherry`, `pogo check-stranded`, and both "+
			"stranded-push reporters — is defined over PUSHED commits and is blind to a tree by "+
			"construction.\n\n", workItemID)
	if outcome == "undetermined" {
		return fmt.Sprintf(
			"DO NOT DISPATCH A WORKER AT %s UNTIL THIS TREE HAS BEEN READ. We did not establish "+
				"that there is uncommitted work here; we established that we could not look, which "+
				"is not the same as finding nothing. %s",
			workItemID, common)
	}
	return fmt.Sprintf(
		"DO NOT DISPATCH A WORKER AT %s. This tree holds work that was never committed, so it is "+
			"on no branch and no push, and NOTHING OUTSIDE THIS TREE CAN TELL YOU WHAT IS IN IT. "+
			"`~/.pogo/polecats/qbe37` was preserved this way holding an entire 1450-line package "+
			"that existed in no other location on the machine; `~/.pogo/polecats/p687f` was "+
			"preserved holding seven regenerated suite outputs a re-run reproduces in seconds. "+
			"Both are this same notice. Which one you have is visible only by reading the files, "+
			"so the prohibition holds until somebody has. %s",
		workItemID, common)
}

// standingListNote points a one-shot notice at the standing list (mg-f4c0).
//
// THIS NOTICE FIRES ONCE AND IS NEVER REPEATED, which is half of why the
// retained population grew from six trees to twenty-three with nobody reading
// it: the mail lands in the middle of other traffic, and after that the only
// way to see the population was `ls ~/.pogo/polecats`. The other half was that
// no list existed to point at. One does now, so the notice names it.
//
// It also states the blast radius of the reclaim command it just recommended,
// because that command is the reason a careful reader does nothing. `pogo gc
// --repo=<repo> --apply --force` is repo-scoped and forced: it acts on every
// eligible retained tree in the repository, not on the one this notice is
// about. A reader who inspects the named tree, decides it is safe, and runs the
// command discards the other trees' work too — so the alternative to saying
// this is not "they run it", it is "they correctly decline to run it and the
// tree stays forever."
func standingListNote(sourceRepo string) string {
	return fmt.Sprintf(
		"That command is REPO-SCOPED AND FORCED: it reclaims every eligible retained worktree in "+
			"%s at once, not just the one named above, and it discards their uncommitted work "+
			"too. Read the whole population before running it.\n\n"+
			"This notice fires ONCE and is never repeated. The standing list — every retained "+
			"worktree on this machine, across all repos, with the uncommitted paths in each split "+
			"into modified and untracked — is:\n\n  pogo gc --list-preserved\n\n",
		sourceRepo)
}

// emitWorktreePreserved puts a RETAINED worktree on the event spine, with the
// work item it belongs to.
//
// THIS IS THE RECORD HALF THAT DID NOT EXIST (mg-32e3). This path's mail half
// works and has been validated end to end; its record half was nothing but
// unstructured `log.Printf` lines, so three days of preservations had to be
// reconstructed by grepping pogod.log — and pogod logs to inherited stderr, so
// even that is not durable. It is the exact mirror of work_item_stranded_push,
// whose event half worked and whose mail half was missing until mg-be37.
//
// ONE EVENT TYPE COVERS BOTH RETENTION OUTCOMES, discriminated by `outcome`,
// matching worktree_notice_undelivered's shape and vocabulary so the two join on
// that field. A consumer asking "does this work item have work nobody pushed?"
// wants both: `preserved` is a positive finding, `undetermined` is a tree we
// could not rule out, and folding the second into the first would state a fact
// about the tree that nobody established.
//
// IT CARRIES THE BRANCH AND THE MODIFIED/UNTRACKED SPLIT (mg-d45b). mg-32e3 put
// the record on the spine; those two fields were named in the same requirement
// and were still missing, and each is load-bearing rather than decorative. The
// branch is where a rescuer starts and is the thing the retained tree is pinning.
// The split is what makes the count actionable: `dirty_paths: 16` cannot
// distinguish sixteen edits to tracked files, all of which have a committed
// version in the object store, from sixteen untracked paths that exist on no
// branch, in no stash and on no remote — and the second is why qbe37's tree was
// the only copy of a 1450-line package. Without the split a consumer had to open
// the tree to learn which it was, which is the by-hand reconstruction this event
// exists to replace.
//
// Observable as: `pogo events --type worktree_preserved`. It is a REPORT — it
// blocks no dispatch and reclaims no tree. Making it refuse a spawn would be a
// separate decision with its own failure mode (a stale event wedging an item
// forever), and this ticket's finding is that the detection already happened and
// was correct; what it lacked was the ability to say the sentence.
func emitWorktreePreserved(a exitedAgent, outcome string, dirty *gitgc.DirtyWorktreeError, detail string) {
	details := map[string]any{
		"worktree": a.WorktreeDir,
		"outcome":  outcome,
		"detail":   detail,
		// Stated rather than implied: this is the population every pushed-work
		// guard misses, and a consumer must not have to infer that from the
		// event type's name.
		"pushed": false,
	}

	// The branch is read here, from the tree, rather than threaded down from
	// the registry record: the tree is the authority on what it has checked
	// out, and a name copied from elsewhere is a claim that can rot while
	// `git rev-parse` is an observation that cannot.
	//
	// It matters because retention and the branch are the same fact seen twice:
	// a preserved worktree keeps its branch checked out, so that branch cannot
	// be deleted, cannot be re-used, and is where a rescuer has to start.
	if branch, err := gitgc.WorktreeBranch(a.WorktreeDir); err == nil {
		details["branch"] = branch
	} else {
		// REPORTED, never omitted — the rule workItemLine applies two functions
		// up, for the same reason. A `branch` key that simply disappears when
		// the read fails is indistinguishable, to every consumer, from a
		// `branch` key nobody implemented; and "the field is missing exactly
		// when something went wrong" is the defect this event exists to end,
		// reproduced one layer down. An unreadable branch is EXPECTED on the
		// undetermined path — `git status` already failed there, and rev-parse
		// usually fails for the same underlying reason.
		details["branch_error"] = err.Error()
	}

	// Counts ride on the error rather than as loose ints because a count is
	// meaningful only when the tree was actually read, and nil says that in a
	// way a zero cannot.
	if dirty != nil {
		details["dirty_paths"] = dirty.Total
		// The split, not just the total (mg-d45b): an untracked path exists on
		// no branch, in no stash and on no remote, so it is the one that makes
		// a preserved tree urgent. Both are computed over the full porcelain
		// list, so they still sum to dirty_paths when `files` is capped.
		details["modified_paths"] = dirty.Modified
		details["untracked_paths"] = dirty.Untracked
		if len(dirty.Files) > 0 {
			details["files"] = dirty.Files
		}
	}
	events.Emit(context.Background(), events.Event{
		EventType: "worktree_preserved",
		// Attributed to the agent whose tree it is — the opposite choice from
		// worktree_notice_undelivered next door, and for the same reason: there
		// the open question is what the ADDRESSEE was never told, here it is
		// what this AGENT left behind.
		Agent:      a.eventIdentity(),
		WorkItemID: a.WorkItemID,
		Repo:       a.SourceRepo,
		Details:    details,
	})
}

// emitWorktreeNoticeUndelivered records a preserved/undetermined-worktree notice
// that could not be delivered — enumeration row A15 (mg-342d).
//
// THIS ROW IS DELIBERATELY AN EVENT AND NOT A MAIL, and it is the one row where
// that is the right answer. A15 is not "a condition with no channel"; it is "the
// channel itself failed". Reacting to a failed mail send by sending another mail
// is a retry dressed as an alarm, and it fails the same way for the same reason —
// exactly the shape mg-c3f0's meta-finding described, where 12 notification
// sites degrade to log.Printf when their send fails and pogod.log becomes the
// place routed conditions go to die.
//
// So the fix is to make the failure STRUCTURED rather than louder.
// worktreecleanup.go emitted no events at all before this: a preserved worktree
// whose notice was lost left nothing behind but a log line, so the tree pinned
// its branch indefinitely with no record anywhere a query could reach. The six
// watcher packages already do exactly this — they attach mail_error to an event
// rather than only logging it — and this brings the two ad-hoc sites here up to
// that standard.
//
// Observable as: `pogo events --type worktree_notice_undelivered`. A non-empty
// result means a worktree is being preserved that nobody was told about.
//
// It carries the work item too (mg-32e3). A lost notice is exactly when the
// event is the only surviving trace, so it must answer the same question the
// notice would have: which item is now unsafe to dispatch at.
func emitWorktreeNoticeUndelivered(a exitedAgent, to, outcome string, mailErr error) {
	events.Emit(context.Background(), events.Event{
		EventType: "worktree_notice_undelivered",
		// Attributed to the addressee that did NOT hear, not to the exited
		// agent: the open question is what the coordinator was never told.
		Agent:      to,
		WorkItemID: a.WorkItemID,
		Repo:       a.SourceRepo,
		Details: map[string]any{
			"row":          "A15",
			"exited_agent": a.Name,
			"worktree":     a.WorktreeDir,
			"outcome":      outcome,
			"mail_error":   mailErr.Error(),
		},
	})
}
