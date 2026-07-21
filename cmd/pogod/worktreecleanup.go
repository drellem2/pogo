package main

import (
	"errors"
	"fmt"
	"log"

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

	// OwnerUnproven, not OwnerGone — and the distinction is worth stating,
	// because this hook fires AFTER the process has exited, so a naive read of
	// "liveness" would answer GONE here and reap (mg-4d45).
	//
	// The process being dead is not the question. This tree belonged to an
	// agent that was RUNNING until moments ago; its files are that agent's
	// in-flight work, and an exit — normal, crashed, or force-stopped — says
	// nothing about whether the work was saved. Exactly one exit route
	// reaches this hook with work still in the tree, and it is the route that
	// cost us a 201-line race test.
	//
	// OwnerGone belongs where liveness has been positively excluded AND the
	// work has been accounted for: the gitgc sweep, which gates on
	// LivePolecats and a concluded ticket before it removes anything.
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
