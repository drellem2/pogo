package main

import "fmt"

// The condition catalogue: one constructor per enumeration row, so the mapping
// between
// docs/investigations/pogod-log-conditions-with-no-reader-2026-07-30.md §4 and
// the running daemon is auditable in ONE file rather than scattered across
// fourteen decision points.
//
// That is the point of the file. mg-c3f0's finding was that an enumeration dies
// inside a completed work item; the second-order version of the same failure is
// an enumeration that survives as prose while the code drifts away from it
// underneath. Row ids are pinned by test (conditions_test.go) against the
// enumeration's numbering, and every constructor names its site.
//
// ROUTING RULE, one rule for all of them: the configured COORDINATOR, except
// where the affected subsystem already owns a configured mailbox for exactly
// this purpose (A13 → [gh_teardown] notify_to). Never `human`.
//
// Why the coordinator even for the rows the enumeration marks "human" or
// "platform" (A4, A7, A10, A11, A14): those are not mailboxes. `human` is, and it
// is measured at 988 unread against 0 for the coordinator — so addressing it is
// a way of writing down an intention, not of reaching anyone. The coordinator is
// the actor that can ACT on every row here: it can file a work item, dispatch a
// polecat, and escalate to a human deliberately with context. "Alarm the agent
// that can act" is mg-c3f0's measured constraint, and for a platform fault on
// this fleet that agent is the coordinator.
//
// ONE EXCEPTION, and it is to the addressee SET, not to To (mg-875d). A row
// whose trigger means pogod is unhealthy or the coordinator is down sets
// OutOfBand, and is then ALSO copied to `[agents] escalation_box`. Addressed to
// the coordinator alone, those rows are circular: the reader is an agent this
// daemon runs, and the condition is that it is not there. The rows that set it:
//
//   A2  scheduler_load_failed, scheduler_disabled_no_home — nothing can wake
//       the coordinator to read its mail
//   A5  autostart_failed:<coordinator> — the addressee is the casualty
//   A6  restart_failed:<coordinator>   — likewise (about any OTHER agent, no)
//   A11 pogod_heartbeat_write_failed   — every outside check reads pogod as dead
//   mg-a19a pogod_log_not_written      — its unread-pipe case kills pogod ~30s
//       after every boot, and the coordinator with it
//   mg-5af1 orchestration_left_stopped — the stop took the coordinator down too
//   mg-b968 harness_not_logged_in      — the refused crew includes the
//       coordinator (harnesslogin.go, drellem2/pogo#173)
//
// A3 (ackwatch_not_armed) does not: it is a consequence of A2 on the same boot,
// and A2 already reaches the box. The "never `human`" above predates the
// deadman bypass (pogo-reminders com.pogo.deadman, mg-65d2): the box is now
// read by a launchd job whether or not any agent is alive, which is exactly the
// property the out-of-band copy needs and the coordinator's box cannot have.

const (
	rowA2SchedulerLoad     = "scheduler_load_failed"
	rowA2SchedulerNoHome   = "scheduler_disabled_no_home"
	rowA3AckWatchNotArmed  = "ackwatch_not_armed"
	rowA4PromptRefresh     = "prompt_refresh_failed"
	rowA10RolePin          = "role_pin_failed"
	rowA11HeartbeatWrite   = "pogod_heartbeat_write_failed"
	rowA13TeardownNotArmed = "ghteardown_not_armed"
	rowA13IntakeNotArmed   = "ghintake_not_armed"
	rowA13IntakeNoCred     = "ghintake_no_credential"

	rowA13GHWatchNotReporting = "ghwatch_not_reporting"
	rowA14LogRotation         = "log_rotation_failed"

	rowA9TicketIndex       = "gitgc_no_ticket_index"
	rowA9PolecatWitness    = "gitgc_no_polecat_witness"
	rowA9OrphanScan        = "gitgc_orphan_scan_disabled"
	rowA9SweepFailedPrefix = "gitgc_sweep_failed:"

	rowA5AutoStartPrefix = "autostart_failed:"
	rowA6RestartPrefix   = "restart_failed:"
	rowA7ProviderPrefix  = "unknown_provider:"
)

// conditionSchedulerLoadFailed — A2, main.go's `scheduler load failed`.
//
// The enumeration's own highest severity, and the one condition here that needs
// a WAKE as well as a mail: every agent's mail-check loop is a scheduler entry,
// so on this boot the coordinator can be mailed but not prompted to look. See
// conditionWaker for why the nudge is not dead by the same fault.
func conditionSchedulerLoadFailed(to, schedPath, detail string) pogodCondition {
	return pogodCondition{
		ID:        rowA2SchedulerLoad,
		Row:       "A2",
		To:        to,
		Detail:    fmt.Sprintf("scheduler.New(%s): %s", schedPath, detail),
		Wake:      true,
		OutOfBand: true,
		Subject: "[pogod] SCHEDULER DID NOT LOAD — no mail-check fires for anyone, " +
			"fleet-wide proactive channel is down",
		Body: conditionBody("A2",
			"pogod's scheduler failed to load at boot. NOTHING SCHEDULED IS RUNNING.",
			"THE WHOLE FLEET IS DEAF, not just one agent. Every agent's mail-check loop is a\n"+
				"  `pogo schedule` entry, so no agent will be prompted to read mail; every daily\n"+
				"  sweep, triage sweep and gate-lift is dead; ack-watch cannot arm (there are no\n"+
				"  completion counters to read); and deaf-watch — the watcher whose whole job is to\n"+
				"  notice that the fleet has gone deaf — is degraded by the same fault, because the\n"+
				"  mail-check provider it judges against is installed on the scheduler path. The\n"+
				"  watchers built to catch this outage are disabled BY this outage.",
			"1. `pogo schedule list` — if it errors or is empty, confirmed.\n"+
				"  2. Inspect ~/.pogo/schedules.json (the DETAIL above names the exact failure).\n"+
				"     A parse failure is the common cause; move the file aside to recover, but keep\n"+
				"     a copy — it is the only record of what was registered.\n"+
				"  3. Restart pogod (`pogo recovery request --reason=...`) and re-verify with\n"+
				"     `pogo schedule list --agent <name>` for each standing agent.\n"+
				"  4. Until then, agents must be reached by direct nudge, not by mail.\n"+
				"  NOTE: you were also NUDGED for this, deliberately. Do not assume other agents\n"+
				"  have seen anything — their mail-check loops are not firing.",
			detail),
	}
}

// conditionSchedulerNoHome — A2's second site: the scheduler is skipped entirely
// because its state path could not be resolved. Same consequence as a load
// failure, different cause, so it gets its own id (one must not suppress the
// other) and the same severity.
func conditionSchedulerNoHome(to, detail string) pogodCondition {
	return pogodCondition{
		ID:        rowA2SchedulerNoHome,
		Row:       "A2",
		To:        to,
		Detail:    detail,
		Wake:      true,
		OutOfBand: true,
		Subject: "[pogod] SCHEDULER DISABLED (cannot resolve its state path) — " +
			"no mail-check fires for anyone",
		Body: conditionBody("A2",
			"pogod could not resolve the scheduler's state path, so the scheduler was never "+
				"started. NOTHING SCHEDULED IS RUNNING.",
			"Identical to a scheduler load failure: no mail-check loop fires for any agent, no\n"+
				"  sweep runs, and the deafness watchers are degraded by the same fault they exist\n"+
				"  to catch.",
			"The cause is environmental, not data: pogod could not determine a home directory.\n"+
				"  Check HOME and POGO_HOME in the daemon's environment (`launchctl print\n"+
				"  gui/$(id -u)/com.pogo.daemon`), fix the plist, and reload. You were also NUDGED\n"+
				"  for this because mail alone cannot reach anyone right now.",
			detail),
	}
}

// conditionAckWatchNotArmed — A3. The ack-watch half.
//
// A NOTE ON A3's OTHER HALF, because the enumeration lists two sites and only one
// of them can fire. `deaf-watch NOT armed` (main.go:1729) is guarded on
// `cfg.DeafWatch.Enabled && agentRegistry != nil`, and agentRegistry cannot be
// nil there: pogod os.Exit(1)s if agent.NewRegistry fails, hundreds of lines
// earlier. So that branch is unreachable in production and its stated reason
// ("the agent registry did not load") is not the degradation that actually
// happens. The real deaf-watch degradation under A2 is that MailLoopReport
// errors because SetMailCheckProvider is only called on the scheduler path — and
// that one is ALREADY instrumented, as a deaf_watch_error on the event spine.
// So A3 is wired once, here, and the A2 notice above names the deaf-watch
// consequence in its body rather than duplicating an alarm for it.
func conditionAckWatchNotArmed(to string) pogodCondition {
	return pogodCondition{
		ID:     rowA3AckWatchNotArmed,
		Row:    "A3",
		To:     to,
		Detail: "ack_watch is enabled in config but the scheduler did not load, so there are no completion counters to read",
		Subject: "[pogod] ACK-WATCH NOT ARMED — the fleet's schedule-completion watcher is off, " +
			"because the scheduler it reads is down",
		Body: conditionBody("A3",
			"ack-watch is enabled in config but did not arm on this boot.",
			"ack-watch is how the fleet notices that scheduled fires are being DELIVERED but not\n"+
				"  COMPLETED — the exact shape of the 23h30m outage of 2026-07-22, where 647\n"+
				"  deliveries were logged against a fleet whose every turn was failing. With it off,\n"+
				"  a 100%-dead fleet is again indistinguishable from a healthy one. It is off\n"+
				"  because the scheduler did not load, which means you are almost certainly also\n"+
				"  holding an A2 notice — fix that and this clears with it.",
			"Resolve the scheduler load failure (see the A2 notice). ack-watch arms on the next\n"+
				"  boot where the scheduler loads; confirm with `pogo events --type ack_watch_report`.",
			""),
	}
}

// conditionPromptRefreshFailed — A4. Strictly worse than A1: A1 is one prompt
// declined for a reason (local edits), A4 is EVERY prompt staying stale for no
// reason at all, and before this it got less annunciation than A1 did.
func conditionPromptRefreshFailed(to, detail string) pogodCondition {
	return pogodCondition{
		ID:      rowA4PromptRefresh,
		Row:     "A4",
		To:      to,
		Detail:  detail,
		Subject: "[pogod] PROMPT REFRESH FAILED — every agent prompt is stale, fleet-wide",
		Body: conditionBody("A4",
			"InstallPrompts failed at boot, so pogod refreshed NO prompt files on this boot.",
			"Every agent — coordinator, crew, and every polecat template — keeps running whatever\n"+
				"  prompt text is on disk, and a pogod restart no longer propagates prompt updates.\n"+
				"  This is the fleet-wide version of the incident that started this whole line of\n"+
				"  work (mg-c3f0), where ONE stale prompt ran for seven days: the coordinator acted\n"+
				"  on 13-day-old guidance and nobody could tell from the outside. Nothing retries,\n"+
				"  and every later boot fails the same way until the cause is fixed.",
			"1. `pogo agent prompt install` by hand — it takes the same path and will show you\n"+
				"     the same error interactively.\n"+
				"  2. The usual causes are permissions or a full disk under ~/.pogo/agents/.\n"+
				"     Check `ls -ld ~/.pogo/agents` and `df -h ~`.\n"+
				"  3. After fixing, restart pogod and confirm the boot log shows prompt refresh\n"+
				"     lines rather than this failure.",
			detail),
	}
}

// conditionAutoStartFailed — A5, and the hard one.
//
// THE CASE THAT CANNOT BE SOLVED IN-FLEET, stated plainly because the ticket asks
// for a conclusion rather than a hedge: when the agent that failed to auto-start
// IS the coordinator, the actor and the casualty are the same process, and there
// is no in-fleet reader by construction. What this does about it:
//
//   - failed agent != coordinator → mail the coordinator. It can restart the
//     agent, and the notice names the command. Fully solved.
//   - failed agent == coordinator → still mail the coordinator's mailbox, because
//     the mail is not lost: it sits in the maildir and is read on the FIRST
//     mail-check after the coordinator next starts, which is exactly when the
//     information becomes actionable. Then say so in the subject and body rather
//     than pretending it was delivered to a live reader.
//
// It does not REPLACE the coordinator with `human` (988 unread when this was
// written — it would have looked like escalation and been silence), and it does
// not synthesise an addressee (mail to a name no agent reads is accepted into a
// phantom mailbox and lost). Since mg-875d it does ADD one: the coordinator case
// sets OutOfBand, so the notice is also copied to `[agents] escalation_box`,
// which a launchd job (not the fleet) reads. That is the out-of-process
// instrument §3 and §4 of
// docs/investigations/pogod-condition-annunciation-2026-07-30.md said this row
// needed; it did not exist in-tree then.
func conditionAutoStartFailed(to, failedAgent, detail string, isCoordinator bool) pogodCondition {
	c := pogodCondition{
		ID:     rowA5AutoStartPrefix + failedAgent,
		Row:    "A5",
		To:     to,
		Detail: fmt.Sprintf("auto-start of %s failed: %s", failedAgent, detail),
	}
	if isCoordinator {
		c.OutOfBand = true
		c.Subject = fmt.Sprintf("[pogod] THE COORDINATOR (%s) FAILED TO AUTO-START — "+
			"nothing was coordinating the fleet after this boot", failedAgent)
		c.Body = conditionBody("A5",
			fmt.Sprintf("%s is the configured coordinator and it failed to auto-start. "+
				"You are reading this because it started later; at the time of the fault there "+
				"was no in-fleet reader for it at all.", failedAgent),
			"Between that boot and whenever you started, nothing dispatched work, nothing merged\n"+
				"  anything, and no agent was coordinated. Work items sat in available/ looking\n"+
				"  exactly like work nobody had started. This notice was mailed rather than\n"+
				"  delivered live BECAUSE the addressee was the casualty — that is the honest\n"+
				"  limit of an in-fleet alarm for this row, and it is why the notice waited in\n"+
				"  your maildir instead of reaching anyone at the time.",
			"1. Read the DETAIL — it is the spawn error, and it is the only evidence of why.\n"+
				"  2. Check for work items that were claimed-then-orphaned or left in available/\n"+
				"     across that window (`mg list --status=available`).\n"+
				"  3. If this recurs, it is an out-of-process supervision gap, not a coordinator\n"+
				"     bug: nothing inside the fleet can observe a coordinator that never starts.",
			detail)
		return c
	}
	c.Subject = fmt.Sprintf("[pogod] auto-start of %s FAILED — that agent is not running", failedAgent)
	c.Body = conditionBody("A5",
		fmt.Sprintf("Crew agent %s declares auto_start = true and failed to start on this boot.", failedAgent),
		fmt.Sprintf("%s is simply absent. Whatever it is responsible for — sweeps, triage, review —\n"+
			"  is not happening, and until now nothing said so: an agent that never started has no\n"+
			"  stall to detect and no missed ack to count, so it looks identical to an agent that\n"+
			"  is idle by design.", failedAgent),
		fmt.Sprintf("1. `pogo agent start %s` and watch it — the DETAIL above is the spawn error.\n"+
			"  2. `pogo agent list` to confirm which others came up.\n"+
			"  3. If the spawn error is a missing binary or PATH problem, it will hit every agent\n"+
			"     of that provider, so check the rest rather than just this one.", failedAgent),
		detail)
	return c
}

// conditionRestartFailed — A6. A crashed restart_on_crash agent whose respawn
// also failed is gone, and the exit hook has already run.
//
// Raised only when the respawn was TRIED and FAILED. A respawn DECLINED by the
// registry's guards — the shutdown latch, or a generation that moved — is a
// deliberate stop working correctly, and noteRespawnOutcome keeps it off this
// row: raising it there put five false conditions in front of the coordinator
// on one fleet stop, which is every auto_start crew member and was the first
// thing this channel ever emitted (mg-0208).
func conditionRestartFailed(to, agentName, detail string) pogodCondition {
	return pogodCondition{
		ID:     rowA6RestartPrefix + agentName,
		Row:    "A6",
		To:     to,
		Detail: fmt.Sprintf("respawn of %s after an unexpected exit failed: %s", agentName, detail),
		// Out of band only when the agent that is gone IS the addressee
		// (mg-875d). About any other agent, the coordinator is alive to act.
		OutOfBand: agentName == to,
		Subject: fmt.Sprintf("[pogod] %s CRASHED and its restart FAILED — that agent is gone",
			agentName),
		Body: conditionBody("A6",
			fmt.Sprintf("%s exited unexpectedly, pogod scheduled the restart it is configured for, "+
				"and the restart failed.", agentName),
			fmt.Sprintf("%s is not running and nothing will try again — the respawn is one-shot.\n"+
				"  Everything that agent owns is stopped. The crash itself was handled correctly;\n"+
				"  this notice is about the recovery failing, which is the part that leaves no\n"+
				"  trace anywhere an agent can see.", agentName),
			fmt.Sprintf("1. `pogo agent start %s` — the DETAIL above is why the automatic attempt\n"+
				"     failed, and a manual start usually reproduces it.\n"+
				"  2. If the agent crash-looped before this, its worktree may have been preserved\n"+
				"     rather than reaped; check for a preserved-worktree notice in your mail.\n"+
				"  3. Repeated crashes with a failing restart are worth a work item, not another\n"+
				"     manual start.", agentName),
			detail),
	}
}

// conditionRestartFailedAfterStop — A6, when the exit that scheduled the respawn
// was a STOP someone asked for (a.StopRequested), not a crash.
//
// Same row, same id, same out-of-band rule: the agent is still down and
// nothing will try again, which is what A6 exists to say. What changes is
// every sentence that asserted a crash. restart_on_crash makes every exit a
// restart, deliberate ones included (gh #89), so a requested stop CAN reach
// this row when the restart that follows it genuinely fails — and the old
// wording then told its reader the agent "CRASHED", "exited unexpectedly" and
// "is gone" about an agent they had just stopped on purpose. Three false
// assertions in one notice is how drellem2/pogo#159 was filed; the reader
// cannot trust the one true claim (it is not running) when it arrives wrapped
// in those.
func conditionRestartFailedAfterStop(to, agentName, stopCause, detail string) pogodCondition {
	how := "on request"
	if stopCause != "" {
		how = fmt.Sprintf("on request (stop_cause=%s)", stopCause)
	}
	return pogodCondition{
		ID:        rowA6RestartPrefix + agentName,
		Row:       "A6",
		To:        to,
		Detail:    fmt.Sprintf("respawn of %s after a requested stop failed: %s", agentName, detail),
		OutOfBand: agentName == to,
		Subject: fmt.Sprintf("[pogod] %s was stopped on request and its automatic restart FAILED — it is not running",
			agentName),
		Body: conditionBody("A6",
			fmt.Sprintf("%s was stopped %s. It is restart_on_crash, so pogod scheduled the restart "+
				"it makes of every exit, and that restart failed. This was not a crash.", agentName, how),
			fmt.Sprintf("%s is not running and nothing will try again — the respawn is one-shot.\n"+
				"  Everything that agent owns is stopped until someone starts it.", agentName),
			fmt.Sprintf("1. `pogo agent list` first: if %s is running, something else started it\n"+
				"     and this notice is stale.\n"+
				"  2. Otherwise `pogo agent start %s` — the DETAIL above is why the automatic\n"+
				"     attempt failed, and a manual start usually reproduces it.\n"+
				"  3. To stop a restart_on_crash agent and have it STAY down, `pogo agent park`\n"+
				"     it; a bare stop is a restart by design (drellem2/pogo#89).", agentName, agentName),
			detail),
	}
}

// conditionUnknownProvider — A7. The fallback keeps the daemon booting, which is
// correct; the silence about it is not. An agent running on a different harness
// than its config asked for produces behaviour differences that get debugged as
// prompt or model problems.
func conditionUnknownProvider(to, badID, fallbackID, source string) pogodCondition {
	detail := fmt.Sprintf("provider %q (from %s) is not registered; agents fall back to %q", badID, source, fallbackID)
	return pogodCondition{
		ID:     rowA7ProviderPrefix + badID,
		Row:    "A7",
		To:     to,
		Detail: detail,
		Subject: fmt.Sprintf("[pogod] unknown agent provider %q — agents are running on %q instead",
			badID, fallbackID),
		Body: conditionBody("A7",
			fmt.Sprintf("Config asks for agent provider %q, which is not registered. pogod fell back "+
				"to %q rather than refusing to boot.", badID, fallbackID),
			fmt.Sprintf("Agents are silently running on a DIFFERENT harness than configured. That is\n"+
				"  not a crash, which is what makes it expensive: the fleet works, and every\n"+
				"  behavioural difference caused by running %q instead of %q gets debugged as a\n"+
				"  prompt problem, a model problem, or a flake. Nothing else reports it.",
				fallbackID, badID),
			fmt.Sprintf("1. `pogo agent providers` (or the same list in the DETAIL) for the ids that\n"+
				"     actually exist — %q is usually a typo or a provider that was renamed.\n"+
				"  2. Fix it in config.toml and restart pogod.\n"+
				"  3. If %q was intended and no longer exists, the change of harness is a decision\n"+
				"     worth recording, not a config edit to make quietly.", badID, badID),
			detail),
	}
}

// gitGCCondition — A9. Four sites, one shape: a GC guard tripped, so branches
// and worktrees accumulate silently.
//
// A9's severity is not the individual sweep; it is that the failure mode is
// UNBOUNDED GROWTH with no symptom until a disk fills or `git branch` becomes
// unreadable. Each site keeps its own id so a persistently broken ticket index
// does not suppress a newly broken witness store.
func gitGCCondition(id, to, what, cost, remedy, detail string) pogodCondition {
	return pogodCondition{
		ID:      id,
		Row:     "A9",
		To:      to,
		Detail:  detail,
		Subject: "[pogod] git GC degraded — " + what,
		Body: conditionBody("A9", "pogod's git GC sweep is degraded: "+what,
			cost, remedy, detail),
	}
}

func conditionGitGCNoTicketIndex(to, detail string) pogodCondition {
	return gitGCCondition(rowA9TicketIndex, to,
		"the work-item index could not be loaded, so NO sweep ran on any repo",
		"Nothing is reclaiming merged polecat branches or worktrees, on any repo. The sweep\n"+
			"  refuses to run without the ticket index on purpose (mg-0130) — deciding a branch is\n"+
			"  abandoned without knowing which work items exist would delete live work — so this\n"+
			"  is the safe failure, but it is a total stop, and branch/worktree count grows every\n"+
			"  time a polecat finishes.",
		"1. `mg list` — if that errors, the index is the problem and fixing it fixes this.\n"+
			"  2. Check the macguffin store under ~/.macguffin for a partial write.\n"+
			"  3. `pogo gc --repo=<repo>` (without --apply) shows what the sweep WOULD do once\n"+
			"     the index loads again — use it to size the backlog.",
		detail)
}

func conditionGitGCNoWitness(to, detail string) pogodCondition {
	return gitGCCondition(rowA9PolecatWitness, to,
		"the polecat witness store is unreadable, so NO sweep ran on any repo",
		"Nothing is reclaiming branches or worktrees. The witness store is the ONLY guard a\n"+
			"  restart-surviving polecat's worktree has, and an unreadable store is not an empty\n"+
			"  fleet — so the sweep refuses rather than risk deleting a running polecat's work.\n"+
			"  Correct, and a total stop.",
		"1. Inspect ~/.pogo/polecat-witness.json — a truncated or partial write is the usual\n"+
			"     cause and the DETAIL above names it.\n"+
			"  2. Do NOT simply delete it while polecats are live; `pogo agent list` first.\n"+
			"  3. With no polecats running, moving it aside lets pogod rebuild it.",
		detail)
}

func conditionGitGCOrphanScan(to, detail string) pogodCondition {
	return gitGCCondition(rowA9OrphanScan, to,
		"the orphan-directory scan is disabled, so abandoned polecat dirs are never found",
		"Per-repo sweeps still run; only the polecats-dir scan is off. Orphan directories —\n"+
			"  what a polecat leaves behind when pogod dies mid-run and its exit cleanup never\n"+
			"  ran — accumulate and are reachable through no other path.",
		"Resolve the path error in the DETAIL (usually POGO_HOME resolution), then confirm the\n"+
			"  next sweep logs no orphan-scan line. `ls ~/.pogo/polecats` sizes the backlog.",
		detail)
}

func conditionGitGCSweepFailed(to, repo, detail string) pogodCondition {
	return gitGCCondition(rowA9SweepFailedPrefix+repo, to,
		fmt.Sprintf("the sweep of %s failed", repo),
		fmt.Sprintf("Other repos were still swept; %s alone accumulates merged branches and stale\n"+
			"  worktrees. A worktree that is never reclaimed also pins its branch, so branch\n"+
			"  deletion in that repo silently stops working too.", repo),
		fmt.Sprintf("1. `pogo gc --repo=%s` by hand — it reproduces the error interactively.\n"+
			"  2. A repo that has been moved or deleted out from under pogod fails this way; if so,\n"+
			"     remove it from config rather than leaving a failing sweep.", repo),
		detail)
}

// conditionRolePinFailed — A10. Role defaults left unpinned means the names the
// fleet uses for coordinator and worker can shift under a later pogo upgrade,
// which renames mailboxes and schedule ids.
func conditionRolePinFailed(to, detail string) pogodCondition {
	return pogodCondition{
		ID:      rowA10RolePin,
		Row:     "A10",
		To:      to,
		Detail:  detail,
		Subject: "[pogod] role-default pin FAILED — role names are not pinned in config.toml",
		Body: conditionBody("A10",
			"pogod could not pin the current role defaults into config.toml on this boot.",
			"The pin exists so that a future pogo release changing its DEFAULT coordinator or\n"+
				"  worker name cannot rename your agents underneath you (mg-bc47). Unpinned, an\n"+
				"  upgrade can change which agent name maps to the coordinator prompt — and an agent\n"+
				"  name is also a mailbox name and a schedule id, so a rename silently orphans both.\n"+
				"  Mail to the old name is accepted into a phantom mailbox and never read.",
			"1. Check config.toml is writable and well-formed — the DETAIL names the failure.\n"+
				"  2. Pin by hand: set [agents] coordinator and worker explicitly to the names in\n"+
				"     use now (`pogo agent list`).\n"+
				"  3. Restart pogod and confirm the boot log reports the pin rather than this.",
			detail),
	}
}

// conditionHeartbeatWriteFailed — A11. pogod's own heartbeat file is the only
// evidence an external supervisor has that pogod is alive; the tier-1 reaper
// cannot supervise its own parent.
func conditionHeartbeatWriteFailed(to, path, detail string) pogodCondition {
	return pogodCondition{
		ID:        rowA11HeartbeatWrite,
		Row:       "A11",
		To:        to,
		OutOfBand: true,
		Detail:    fmt.Sprintf("%s: %s", path, detail),
		Subject: "[pogod] cannot write its OWN heartbeat — pogod is now undetectably alive or dead " +
			"to every external check",
		Body: conditionBody("A11",
			"pogod failed to write its own heartbeat file on a heartbeat tick.",
			"That file is the ONLY evidence an out-of-process check has that pogod is alive.\n"+
				"  Nothing inside the fleet can supervise pogod — a child agent cannot reap its\n"+
				"  parent and launchd will not (mg-50e0) — so the heartbeat is the whole external\n"+
				"  liveness signal. A stale heartbeat file reads as A DEAD POGOD to every consumer,\n"+
				"  including the digest and any watchdog: this fault makes a healthy daemon\n"+
				"  indistinguishable from a corpse, in the direction that triggers recovery. And\n"+
				"  since liveness is heartbeat FRESHNESS and never process existence, a stuck file\n"+
				"  will keep looking dead for as long as it stays stuck.",
			"1. Check the path in the DETAIL: `ls -ld ~/.pogo/health` and `df -h ~`. Permissions\n"+
				"     or a full disk are the two causes.\n"+
				"  2. Expect spurious pogod-is-dead reports until it is fixed; a recovery kickstart\n"+
				"     will NOT fix it, because the disk problem survives the restart.\n"+
				"  3. This notice is rate-limited hard — the heartbeat ticks every ~30s and the\n"+
				"     failure would otherwise mail continuously.",
			detail),
	}
}

// A13 — the gh-issue detectors. Since mg-257a8 they do not run in pogod: they
// run in `pogo gh-watch`, which launchd starts through a login shell
// (com.pogo.ghwatch) so that pogod need not hold a GitHub credential for them.
// pogod raises these four conditions by READING that job's record,
// $POGO_HOME/gh-watch/state.json (see ghwatchwitness.go): the first three
// annunciate a detector the job could not arm, the fourth a job that has
// stopped producing records at all — the one failure the job cannot report
// about itself.

// ghWatchJobRemedy is the shared "where does this job's environment come from"
// paragraph. The job runs `zsh -c -l`, so its PATH and credential are what a
// NON-interactive login zsh has: ~/.zshenv and ~/.zprofile, never ~/.zshrc.
const ghWatchJobRemedy = "com.pogo.ghwatch runs `pogo gh-watch` through `zsh -c -l`, so its PATH and\n" +
	"     credential are what a NON-interactive login zsh has (~/.zshenv, ~/.zprofile —\n" +
	"     not ~/.zshrc). Reproduce its view with the same shell: `zsh -c -l '<command>'`."

// conditionTeardownNotArmed — A13. The one row that does NOT go to the
// coordinator: the gh-issue teardown detector already has a configured mailbox
// ([gh_teardown] notify_to, `pm-pogo` by default) chosen deliberately in mg-b586
// because a teardown miss is a workflow failure the fleet chases. Its
// not-armed condition belongs to the same reader as its findings.
func conditionTeardownNotArmed(to, detail string) pogodCondition {
	return pogodCondition{
		ID:     rowA13TeardownNotArmed,
		Row:    "A13",
		To:     to,
		Detail: detail,
		Subject: "[pogod] gh-issue teardown detector NOT ARMED — `gh` is not on the gh-watch job's PATH; " +
			"done carriers are unchecked",
		Body: conditionBody("A13",
			"The gh-issue teardown detector is enabled but did not arm, because `gh` is not\n"+
				"reachable on the PATH of `pogo gh-watch`, the scheduled job that runs it\n"+
				"(com.pogo.ghwatch; pogod read this from that job's last record).",
			"Nothing is checking done work items against the GitHub issues they carry, so an\n"+
				"  issue that should have been closed by a merged carrier just stays open with no\n"+
				"  finding raised. This is a PATH fault, not a config choice, and the job's PATH\n"+
				"  is not necessarily the PATH in your interactive shell — `gh` working when you\n"+
				"  type it proves nothing about the job.",
			"1. "+ghWatchJobRemedy+"\n"+
				"  2. `zsh -c -l 'command -v gh'` — if empty, put gh's directory on PATH in\n"+
				"     ~/.zprofile or ~/.zshenv.\n"+
				"  3. Confirm with `pogo gh-watch --force` (it prints each detector's arming).",
			detail),
	}
}

// conditionIntakeNotArmed — A13's second consequence (mg-039b). Same root cause as
// conditionTeardownNotArmed, a `gh` the gh-watch job cannot reach, and the same
// deviation from the routing rule for the same reason: the gh-issue INTAKE
// detector has a deliberately-chosen mailbox for its findings ([gh_intake]
// notify_to, the coordinator by default), and its not-armed condition belongs to
// the same reader as those findings.
//
// It is a SEPARATE condition rather than a sentence added to A13's body, because
// the two detectors have different readers by design — the teardown detector
// reports to the PM, the intake detector to the coordinator — and one root cause
// with two affected readers needs two notices or one of them learns nothing.
//
// What it costs is worse than A13's, which is why it is worth its own notice: a
// disarmed teardown detector leaves a done work item behind for someone to find,
// while a disarmed intake detector leaves nothing at all — the whole point of
// mg-039b is that an uncarried issue is invisible to every listing the fleet runs.
func conditionIntakeNotArmed(to, detail string) pogodCondition {
	return pogodCondition{
		ID:     rowA13IntakeNotArmed,
		Row:    "A13",
		To:     to,
		Detail: detail,
		Subject: "[pogod] gh-issue INTAKE detector NOT ARMED — `gh` is not on the gh-watch job's PATH; " +
			"open issues are not being reconciled against carriers",
		Body: conditionBody("A13",
			"The gh-issue intake detector is enabled but did not arm, because `gh` is not\n"+
				"reachable on the PATH of `pogo gh-watch`, the scheduled job that runs it\n"+
				"(com.pogo.ghwatch; pogod read this from that job's last record).",
			"Nothing is reconciling the OPEN issues on the watched repos against the `gh:`\n"+
				"  carrier markers in the work-item store. A `[gh]` mail that is delivered and then\n"+
				"  dropped leaves no trace: the issue appears in no `mg list`, no `--tag=gh-issue`\n"+
				"  board, and no stall watch, so a reporter waits with no acknowledgement and\n"+
				"  nothing notices. That is measured, not hypothetical — drellem2/pogo#99 went ~10\n"+
				"  hours uncarried on 2026-07-29 and was found only because a PM ran a sweep by hand\n"+
				"  on a hunch.",
			"1. "+ghWatchJobRemedy+"\n"+
				"  2. `zsh -c -l 'command -v gh'` — if empty, put gh's directory on PATH in\n"+
				"     ~/.zprofile or ~/.zshenv.\n"+
				"  3. In the meantime run the check by hand: `pogo check-intake`. It is the same\n"+
				"     detector, and it will tell you immediately whether anything is uncarried.\n"+
				"  4. Confirm with `pogo gh-watch --force` (it prints each detector's arming).",
			detail),
	}
}

// conditionIntakeNoCredential — A13's third condition (mg-fb29). Same row and
// the same category of argument as the two above: an arming PRECONDITION, not a
// finding.
//
// It is SEPARATE from conditionIntakeNotArmed, which shares its row and its
// reader, because the remedies do not overlap at all: one is a PATH fix, the
// other is `gh auth login`. A single id would let whichever fired first suppress
// the other, and a host with neither gh nor a credential would be told about one
// of its two problems.
func conditionIntakeNoCredential(to, detail string) pogodCondition {
	return pogodCondition{
		ID:     rowA13IntakeNoCred,
		Row:    "A13",
		To:     to,
		Detail: detail,
		Subject: "[pogod] gh-issue INTAKE detector NOT ARMED — the gh-watch job has no GitHub credential; " +
			"run `gh auth login`",
		Body: conditionBody("A13",
			"The gh-issue intake detector is enabled and `gh` IS on the gh-watch job's PATH, but\n"+
				"no GitHub credential could be established there, so it did not arm (pogod read\n"+
				"this from the job's last record).",
			"Identical to the not-armed-for-PATH case: nothing is reconciling the OPEN issues on\n"+
				"  the watched repos against the `gh:` carrier markers in the work-item store, so a\n"+
				"  dropped `[gh]` mail leaves a reporter waiting with no record anywhere (measured:\n"+
				"  drellem2/pogo#99, ~10 uncarried hours).\n"+
				"  It is reported as ONE fault on purpose. Left to run, the detector would call\n"+
				"  `gh issue list` once per watched repo, every repo would fail for this one reason,\n"+
				"  and you would be mailed N unreadable-repo findings whose real cause appeared as\n"+
				"  one of four guesses in a footnote. That message cost nine days of a person's\n"+
				"  queue once already (mg-fb29).",
			"1. "+ghWatchJobRemedy+"\n"+
				"  2. `zsh -c -l 'gh auth status'` — confirm. Then `gh auth login`, or export\n"+
				"     GH_TOKEN in ~/.zshenv (NOT ~/.zshrc, which the job never reads).\n"+
				"  3. No restart is needed: the job reads the credential afresh on every fire.\n"+
				"     Confirm with `pogo gh-watch --force`, which prints the credential line\n"+
				"     (existence-only) and each detector's arming.",
			detail),
	}
}

// conditionGHWatchNotReporting — A13's fourth condition (mg-257a8). The three
// above are the job reporting that it could not arm a detector; this one is the
// job not reporting at all. A launchd job that is unloaded, wedged in the
// nondemand-spawn state (mg-50e0), or failing before it writes its record emits
// nothing, and every gh-issue detector is then dark with no notice from any of
// them — which is why the witness lives here, in the process that is running.
//
// Routed to the COORDINATOR: all three detectors are affected and they report to
// two different mailboxes, so the job itself is a fleet-infrastructure fault.
func conditionGHWatchNotReporting(to, detail string) pogodCondition {
	return pogodCondition{
		ID:      rowA13GHWatchNotReporting,
		Row:     "A13",
		To:      to,
		Detail:  detail,
		Subject: "[pogod] gh-watch job NOT REPORTING — the gh-issue intake, teardown and re-read detectors are all dark",
		Body: conditionBody("A13",
			"The scheduled job that runs the gh-issue detectors (`pogo gh-watch`, launchd label\n"+
				"com.pogo.ghwatch) has not written a fresh record to $POGO_HOME/gh-watch/state.json\n"+
				"within the window pogod allows for it, while at least one of those detectors is\n"+
				"enabled.",
			"Nothing is reconciling open issues against carriers (intake), checking done\n"+
				"  carriers' issues were closed (teardown), or re-reading live carriers' issues.\n"+
				"  None of the three can say so, because none of them is running — this notice is\n"+
				"  the only one you will get.",
			"1. `launchctl print gui/$(id -u)/com.pogo.ghwatch | grep -E 'state|runs|last exit'`.\n"+
				"     Not found: `pogo service install-gh-watch`. Stuck at `pended nondemand\n"+
				"     spawn`: `launchctl kickstart gui/$(id -u)/com.pogo.ghwatch`.\n"+
				"  2. Read the job's log: ~/Library/Logs/pogo/pogo-gh-watch.log.\n"+
				"  3. Run it by hand: `pogo gh-watch --force`. A clean run clears this notice on\n"+
				"     pogod's next check.",
			detail),
	}
}

// conditionLogRotationFailed — A14, and the one whose consequence is the other
// thirteen. If rotation is broken, the post-mortem log every other condition
// falls back to may be lost or unbounded.
func conditionLogRotationFailed(to, detail string) pogodCondition {
	return pogodCondition{
		ID:      rowA14LogRotation,
		Row:     "A14",
		To:      to,
		Detail:  detail,
		Subject: "[pogod] log rotation FAILED — the post-mortem log may be lost or unbounded",
		Body: conditionBody("A14",
			"pogod could not rotate its log at startup. It continued booting, which is correct.",
			"pogod.log is where roughly 90 conditions land, and where 12 notification sites that\n"+
				"  DO reach an actor go to die when their mail send fails. Rotation is what keeps\n"+
				"  the previous run's tail available in pogod.log or pogod.log.1 for a post-mortem\n"+
				"  (mg-6d02) while keeping the file bounded. Broken, you get one of two bad\n"+
				"  outcomes: a log that grows without limit, or a prior run's evidence that is gone\n"+
				"  when someone finally needs it. The cost is paid later, by whoever is debugging\n"+
				"  the next incident.",
			"1. `ls -l ~/Library/Logs/pogo/` — check sizes, permissions and free space.\n"+
				"  2. The DETAIL names the exact failure; a read-only or full volume is typical.\n"+
				"  3. Rotation is attempted once per boot, so it retries on the next restart. Fix\n"+
				"     the cause before the file gets large enough to be its own problem.",
			detail),
	}
}
