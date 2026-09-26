package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// notifyEarlyExit tells the coordinator that a polecat died during its cold
// start (agent.EarlyExit). It is the reader-facing half of mg-c1e2 part 3; the
// agent_early_exit event is the durable half.
//
// Mail rather than an event alone because of who has to act. By the time the
// harness dies, the dispatcher has already been told the spawn succeeded, the
// work item reads claimed, and the worktree is being reaped — so from the
// coordinator's chair this polecat looks like one that is working. Nobody
// re-dispatches an item that looks owned, and nobody reads events.log to find
// out it is not. drellem2/pogo#177 lived exactly there.
//
// Best-effort: a failed send is logged and nothing else, because the exit
// handler it runs in must still clean up.
func notifyEarlyExit(name, workItemID, coordinator string, e agent.EarlyExit,
	mail func(to, from, subject, body string) error) {
	log.Printf("agent %s: EARLY EXIT %s after spawn (exit %d, composer seen: %v) — "+
		"the polecat died during its cold start", name, e.After.Round(100*time.Millisecond), e.ExitCode, e.ComposerSeen)
	if coordinator == "" || mail == nil {
		return
	}
	subject, body := earlyExitNotice(name, workItemID, e)
	if err := mail(coordinator, "pogod", subject, body); err != nil {
		log.Printf("agent %s: early-exit notice to %s failed: %v", name, coordinator, err)
	}
}

// earlyExitNotice renders the coordinator mail. Pure, so its wording can be
// tested without a mail transport.
func earlyExitNotice(name, workItemID string, e agent.EarlyExit) (subject, body string) {
	item := workItemID
	if item == "" {
		item = "(no work item)"
	}
	subject = fmt.Sprintf("Polecat %s died %s after spawn — %s has no worker",
		name, e.After.Round(100*time.Millisecond), item)

	var b strings.Builder
	fmt.Fprintf(&b, "Polecat %s exited %s after it was spawned (exit status %d), inside its %s cold-start window. Nobody asked it to stop.\n\n",
		name, e.After.Round(100*time.Millisecond), e.ExitCode, e.Window)
	if workItemID != "" {
		fmt.Fprintf(&b, "Its spawn was reported OK and %s still reads claimed, but no agent is working it: the worktree has been cleaned up with the process. Treat the dispatch as failed.\n\n", workItemID)
	}
	if !e.ComposerSeen {
		b.WriteString("The harness never showed a ready composer, so it died before it could take a prompt. A trust dialog answered the wrong way looks like this (drellem2/pogo#177); a trust_dialog_refused event at the same time confirms it.\n\n")
	}
	if e.LastOutput != "" {
		fmt.Fprintf(&b, "Last output:\n%s\n", strings.TrimSpace(e.LastOutput))
	}
	return subject, b.String()
}
