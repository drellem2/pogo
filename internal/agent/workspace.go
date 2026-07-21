package agent

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/freshen"
)

// WorkspaceRepoDir returns the conventional long-lived repo checkout for a
// crew agent: $POGO_HOME/agents/<name>/repo.
//
// Nothing in pogo creates this directory — it is a hand-made convention that
// several crew agents follow, and that is precisely why it rots: no refinery
// merge touches it and no spawn path rebuilds it. Most agents have no repo/ at
// all, so callers must treat its absence as normal, not as a fault.
func WorkspaceRepoDir(name string) string {
	return filepath.Join(PromptDir(), name, "repo")
}

// freshenWorkspace brings a crew agent's long-lived checkout up to date with
// its upstream before the agent's harness process is started, or reports
// loudly why it did not.
//
// WHOSE CLOCK THIS RUNS ON, AND WHY THAT IS THE WHOLE DESIGN. The one thing
// that must never happen is fast-forwarding a checkout underneath an agent
// that is using it: an agent mid-edit whose tree moves under it is a surprise
// we file tickets about (mg-d5fc), and automating it is worse than doing it by
// hand. This function is called from StartCrewAgent BEFORE the harness process
// is spawned. At that instant the agent does not exist, so it cannot be
// mid-edit, cannot be mid-read, and cannot be surprised. The refresh therefore
// runs on the agent's OWN clock — its start — and consent is structural rather
// than negotiated. There is deliberately no path that freshens a workspace
// belonging to a running agent.
//
// WHY NOT A STANDING STALENESS MONITOR. See the internal/freshen package doc:
// a background check that watches how far behind a checkout has drifted is a
// guard that decays and needs a threshold nobody can justify. This asks the
// question at a lifecycle event nobody has to remember instead.
//
// RESIDUAL GAP, STATED PLAINLY. A crew agent that runs for months without a
// restart, park/wake, or autostart never reaches this code, so its workspace
// can still drift. This closes the common case (agents do restart) and makes
// the uncloseable case loud rather than silent. Closing it fully means shape
// (2) from the ticket — making the read path resolve against origin rather
// than a working copy — which is a much larger change and is NOT done here.
//
// Never returns an error: a workspace problem must not block an agent start.
// The bad news travels by event and mail instead.
func freshenWorkspace(name string) freshen.Result {
	res := freshen.Checkout(WorkspaceRepoDir(name))

	switch {
	case res.Status == freshen.StatusSkipped:
		// The overwhelmingly common case: this agent keeps no repo/ checkout.
		// Silent by design — logging it for every agent start would bury the
		// one line that matters.
		return res

	case res.Status == freshen.StatusUpdated:
		log.Printf("agent %s: workspace %s", name, res)

	case res.Status == freshen.StatusAlreadyCurrent:
		log.Printf("agent %s: workspace %s", name, res)

	default:
		// Declined or failed. THIS is the line the ticket exists for: a
		// workspace that is stale and was not fixed must not pass in silence,
		// and it must not be reported in language that reads like success
		// (mg-f86c — a DECLINED sync that logs "refreshed" is the defect).
		log.Printf("agent %s: WORKSPACE NOT FRESHENED: %s", name, res)
	}

	emitWorkspaceFreshened(name, res)

	// Mail only when we KNOW the checkout is stale and we did not fix it, or
	// when we could not determine freshness at all. That is not a tuned
	// threshold — it is the binary fact "this is rotting and nothing here can
	// stop it", which is exactly what went unnoticed for two months.
	if res.Stale() || res.Status == freshen.StatusFailed {
		staleWorkspaceAlert(name, res)
	}
	return res
}

// staleWorkspaceAlert is the loud sink, indirected through a var so tests can
// substitute it. Without this seam a test run shells out to the real `mg` and
// mails the live coordinator — a manufactured operator alarm, which is the
// same class of fault as writing test events into the real spine (see
// TestMain).
var staleWorkspaceAlert = mailWorkspaceStale

// emitWorkspaceFreshened records every non-skipped verdict on the event spine.
// The durable record goes down even when the mail fails — same posture as
// driftwatch and the orphan reporter: event first, loud channel best-effort.
func emitWorkspaceFreshened(name string, res freshen.Result) {
	events.Emit(context.Background(), events.Event{
		EventType: "agent_workspace_freshened",
		Agent:     name,
		Repo:      res.Path,
		Details: map[string]any{
			"status":   string(res.Status),
			"branch":   res.Branch,
			"upstream": res.Upstream,
			"behind":   res.Behind,
			"ahead":    res.Ahead,
			"detail":   res.Detail,
		},
	})
}

// mailWorkspaceStale sends the loud half. Best-effort: the
// agent_workspace_freshened event is already on the spine, and a mail failure
// must not take down an agent start.
//
// It goes to the coordinator rather than to `human` because the resolution is
// a judgement about what is in that tree — whether the staged work is worth
// keeping, whether the branch is abandoned — which the mayor can make or
// escalate. Handing a human a bare "run git pull" would be handing out the one
// instruction that is unsafe in the dirty case this mail is usually about.
func mailWorkspaceStale(name string, res freshen.Result) {
	coordinator := CoordinatorName()
	subject := fmt.Sprintf("[stale-workspace] %s's repo is %s behind and was not refreshed",
		name, behindPhrase(res.Behind))

	body := fmt.Sprintf(
		"A long-lived agent workspace was checked at agent start and NOT brought current.\n\n"+
			"Agent:     %s\n"+
			"Checkout:  %s\n"+
			"Branch:    %s\n"+
			"Upstream:  %s\n"+
			"Behind:    %s\n"+
			"Verdict:   %s\n"+
			"Reason:    %s\n\n"+
			"%s\n\n"+
			"WHY YOU ARE SEEING THIS. Nothing else keeps these checkouts fresh. The refinery\n"+
			"fast-forwards the checkout an MR was submitted from, and polecat worktrees are\n"+
			"branched from current origin/main, so both stay current for free. A crew agent's\n"+
			"own ~/.pogo/agents/<name>/repo sits outside both paths — one was found 129 commits\n"+
			"and about two months behind main, by accident, during unrelated work (mg-d5fc).\n"+
			"This check runs before the agent's process starts, so it can refresh a clean tree\n"+
			"without ever moving the ground under a running agent. When it cannot refresh, all\n"+
			"it can do is say so — which is this mail.\n\n"+
			"NOTE THAT A STALE CHECKOUT ALSO STALES A REF. If this worktree holds a branch that\n"+
			"other worktrees in the same repo resolve by name, they read this checkout's old\n"+
			"content too. In the original incident `main` as a local ref was itself 129 behind\n"+
			"origin/main because the worktree holding `main` was the stale one, so ANY worktree\n"+
			"resolving `main` rather than `origin/main` read two-month-old content.",
		name, res.Path, orNone(res.Branch), orNone(res.Upstream),
		behindPhrase(res.Behind), res.Status, res.Detail,
		remedyFor(res))

	cmd := exec.Command("mg", "mail", "send", coordinator,
		"--from", "pogod-workspace",
		"--subject", subject,
		"--body", body)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("workspace-freshen: mail to %s failed: %v: %s",
			coordinator, err, strings.TrimSpace(string(out)))
	}
}

// remedyFor states what to do, and states the dirty case's remedy as a
// decision rather than a command. Emitting a paste-ready `git reset --hard`
// for a tree with uncommitted work would be handing out data loss with the
// daemon's authority behind it.
func remedyFor(res freshen.Result) string {
	switch res.Status {
	case freshen.StatusDeclinedDirty:
		return "TO RESOLVE. The tree has staged or unstaged changes to tracked files, so this\n" +
			"check declined rather than risk them — one of the two checkouts that prompted\n" +
			"this ticket held 83 staged adds on an abandoned branch, and clobbering that would\n" +
			"have turned a staleness bug into silent data loss. Look at `git -C <checkout>\n" +
			"status` and decide whether the work is worth keeping BEFORE running anything:\n" +
			"commit it, stash it, or discard it deliberately. Then `git -C <checkout> pull\n" +
			"--ff-only`. Do not reset a tree you have not looked at."
	case freshen.StatusDeclinedDiverged:
		return "TO RESOLVE. The checkout has local commits the upstream does not, so no\n" +
			"fast-forward exists. Someone has to decide whether those commits should be\n" +
			"pushed, rebased, or abandoned — that is not a call this daemon can make."
	case freshen.StatusDeclinedDetached:
		return "TO RESOLVE. HEAD is detached, so there is no branch to advance. If that is\n" +
			"deliberate, nothing is wrong. If it is not, check the branch back out."
	case freshen.StatusDeclinedNoUpstream:
		return "TO RESOLVE. The branch tracks nothing, so \"behind\" has no referent and this\n" +
			"check cannot judge freshness at all. Set an upstream, or accept that this\n" +
			"checkout is deliberately parked and will never be reported on."
	case freshen.StatusFailed:
		return "TO RESOLVE. Git itself failed, so freshness is UNKNOWN — this is explicitly\n" +
			"NOT a clean bill of health. The checkout may be fine or may be months behind;\n" +
			"nothing here can tell. Run the failing command by hand to find out."
	default:
		return "TO RESOLVE. Inspect the checkout by hand."
	}
}

func behindPhrase(n int) string {
	if n < 0 {
		return "an undetermined number of commits"
	}
	return fmt.Sprintf("%d commit(s)", n)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
