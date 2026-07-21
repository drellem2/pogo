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

	err := gitgc.RemoveWorktree(sourceRepo, worktreeDir)

	var dwe *gitgc.DirtyWorktreeError
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
	case err != nil:
		log.Printf("agent %s: worktree cleanup failed: %v", agentName, err)
		return worktreeCleanupFailed
	default:
		log.Printf("agent %s: removed worktree %s", agentName, worktreeDir)
		return worktreeReaped
	}
}
