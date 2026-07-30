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
// mail is injected (client.SendMGMail in production) so the notification path
// is exercisable without a live daemon.
func cleanupAgentWorktree(
	agentName, sourceRepo, worktreeDir, coordinator string,
	mail func(to, from, subject, body string) error,
) worktreeCleanupOutcome {
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
		log.Printf("agent %s: PRESERVED worktree %s — %v", agentName, worktreeDir, dwe)
		if mail != nil && coordinator != "" {
			subject := fmt.Sprintf("preserved uncommitted work in %s's worktree", agentName)
			body := fmt.Sprintf(
				"Polecat %s exited with uncommitted work in its worktree. The tree was PRESERVED "+
					"rather than reaped (mg-ee02), so nothing was lost.\n\n"+
					"  worktree: %s\n  %v\n\n"+
					"Rescue what matters (it is still a live git worktree — `git -C %s status`), "+
					"then reclaim it with:\n\n  pogo gc --repo=%s --apply --force\n\n"+
					"Until it is reclaimed this worktree keeps its branch checked out, so that "+
					"branch cannot be deleted.",
				agentName, worktreeDir, dwe, worktreeDir, sourceRepo)
			if mErr := mail(coordinator, "pogod", subject, body); mErr != nil {
				log.Printf("agent %s: failed to mail preserved-worktree notice: %v", agentName, mErr)
				emitWorktreeNoticeUndelivered(agentName, coordinator, worktreeDir, "preserved", mErr)
			}
		}
		return worktreePreserved
	case errors.As(err, &uwe):
		// Cannot-tell. The notice must NOT say "uncommitted work" — we do not
		// know that, and sending an operator to rescue files that may not
		// exist is its own failure. It says what actually happened: the check
		// broke, so the tree was kept.
		log.Printf("agent %s: KEPT worktree %s — %v", agentName, worktreeDir, uwe)
		if mail != nil && coordinator != "" {
			subject := fmt.Sprintf("could not check %s's worktree for uncommitted work — kept it", agentName)
			body := fmt.Sprintf(
				"Polecat %s exited and its worktree could NOT be checked for uncommitted work — "+
					"`git status` failed. The tree was KEPT rather than reaped (mg-4d45).\n\n"+
					"This is not a report that there IS work here; it is a report that we could not "+
					"look. `git status` fails when .git is damaged, the disk is unhappy, or "+
					"permissions are broken — which is also when working files are least "+
					"reproducible, so the tree is kept until a human decides.\n\n"+
					"  worktree: %s\n  %v\n\n"+
					"Inspect it (`ls %s`, `git -C %s status`), rescue anything that matters, then "+
					"reclaim it with:\n\n  pogo gc --repo=%s --apply --force\n\n"+
					"Until it is reclaimed this worktree keeps its branch checked out, so that "+
					"branch cannot be deleted.",
				agentName, worktreeDir, uwe, worktreeDir, worktreeDir, sourceRepo)
			if mErr := mail(coordinator, "pogod", subject, body); mErr != nil {
				log.Printf("agent %s: failed to mail undetermined-worktree notice: %v", agentName, mErr)
				emitWorktreeNoticeUndelivered(agentName, coordinator, worktreeDir, "undetermined", mErr)
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
func emitWorktreeNoticeUndelivered(agentName, to, worktreeDir, outcome string, mailErr error) {
	events.Emit(context.Background(), events.Event{
		EventType: "worktree_notice_undelivered",
		// Attributed to the addressee that did NOT hear, not to the exited
		// agent: the open question is what the coordinator was never told.
		Agent: to,
		Details: map[string]any{
			"row":          "A15",
			"exited_agent": agentName,
			"worktree":     worktreeDir,
			"outcome":      outcome,
			"mail_error":   mailErr.Error(),
		},
	})
}
