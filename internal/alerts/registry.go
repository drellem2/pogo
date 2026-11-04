// Package alerts is the one place pogod's alerts are declared and the one seam
// every alert mail goes through (drellem2/pogo#262).
//
// An alert is mail pogod (or a pogo command) sends on its own initiative to tell
// someone about something: a watcher's finding, a refinery verdict, a stranded
// branch. Each one has a stable name, "<family>.<kind>", declared ONCE in the
// registry below. Every send site calls Mail (or MailOutput) with that name, and
// TestNoSendBypassesTheRegistry fails the build if a send does not.
//
// Mail resolves the name and hands the message to the transport the caller
// already used — unless the alert is MUTED (slice 2, SetMutes), in which case
// it is withheld and recorded as an alert_muted event — or ROUTED (slice 3,
// SetRoutes), in which case its primary copy goes to another box, falling back
// to the default recipient rather than dropping mail. Nudges (slice 4) attach
// here later — which is the point of having one seam
// rather than ~60 call sites that each address `mg` on their own.
//
// Some mail is not an alert: the scheduler delivering a registered message is
// a delivery mechanic, not pogod telling anyone anything. Those are declared
// here too, with Class "delivery", so `pogo alerts --all` can show them and the
// guard test still sees every send — but they are not alerts, and later slices
// will not make them mutable.
package alerts

import (
	"sort"
	"strings"
)

// Name is an alert's stable identifier: "<family>.<kind>", lower snake case.
type Name string

// Family is the part before the dot ("ack_watch" of "ack_watch.finding").
func (n Name) Family() string {
	f, _, _ := strings.Cut(string(n), ".")
	return f
}

// Kind is the part after the dot ("finding" of "ack_watch.finding").
func (n Name) Kind() string {
	_, k, _ := strings.Cut(string(n), ".")
	return k
}

// Class separates alerts from delivery mechanics that also send mail.
const (
	ClassAlert    = "alert"
	ClassDelivery = "delivery"
)

// Role tokens used in Def.Default. They name a configured box rather than a
// literal one, so a renamed coordinator or a re-pointed escalation box is
// reflected wherever the default is shown. Resolve substitutes them.
const (
	RoleCoordinator   = "$coordinator"
	RoleEscalationBox = "$escalation_box"
)

// Def declares one alert.
type Def struct {
	Name Name
	// Class is ClassAlert or ClassDelivery.
	Class string
	// Default states who receives it with no configuration, as a rule:
	// role tokens ($coordinator, $escalation_box), literal boxes, and plain
	// words for per-message recipients ("the merge author", "the crew agent").
	Default string
	// Key is the existing config key that already changes the recipient (or
	// silences the mail) today, if any — e.g. "ack_watch.notify_to". Shown by
	// `pogo alerts`; empty when nothing but the built-in default applies.
	Key string
	// Summary is one line saying what the alert reports.
	Summary string
}

// The alert names. Each is used by at least one send site and declared in
// registry exactly once (TestRegistryMatchesTheSendSites).
const (
	AbsentWatchDeclared Name = "absent_watch.declared"
	AbsentWatchFinding  Name = "absent_watch.finding"
	AbsentWatchCleared  Name = "absent_watch.cleared"

	AckWatchFinding Name = "ack_watch.finding"

	AgentEarlyExit Name = "agent.early_exit"

	BlindWatchFinding Name = "blind_watch.finding"
	BlindWatchCleared Name = "blind_watch.cleared"
	// BlindWatchOutOfBand is the copy of a blind-watch finding or clear sent
	// to the escalation box, so `[blind_watch] out_of_band = false` is the
	// mute of one name (slice 2) rather than a per-recipient special case.
	BlindWatchOutOfBand Name = "blind_watch.out_of_band"

	CarrierDriftFinding Name = "carrier_drift.finding"

	ConditionNotice    Name = "condition.notice"
	ConditionOutOfBand Name = "condition.out_of_band"

	CredExpiryWarning Name = "cred_expiry.warning"
	CredExpiryBlind   Name = "cred_expiry.blind"

	CrewResetNotice Name = "crew_reset.notice"

	DeafWatchFinding Name = "deaf_watch.finding"
	DeafWatchCleared Name = "deaf_watch.cleared"

	DriftWatchDrift         Name = "drift_watch.drift"
	DriftWatchNoFire        Name = "drift_watch.no_fire"
	DriftWatchStaleRevision Name = "drift_watch.stale_revision"

	FilerNotifyCompleted     Name = "filer_notify.completed"
	FilerNotifyUndeliverable Name = "filer_notify.undeliverable"

	FirstTurnFinding Name = "first_turn.finding"
	FirstTurnCleared Name = "first_turn.cleared"

	GHIntakeFinding   Name = "gh_intake.finding"
	GHTeardownFinding Name = "gh_teardown.finding"

	HeartWatchFinding Name = "heart_watch.finding"
	HeartWatchCleared Name = "heart_watch.cleared"

	MergedOpenNotClosed Name = "merged_open.not_closed"

	MidSessionWedgeExhausted Name = "mid_session_wedge.exhausted"

	ModalRateLimitDismissed Name = "modal.rate_limit_dismissed"

	OrphanPolecat Name = "orphan.polecat"

	ProgressWatchFinding Name = "progress_watch.finding"
	ProgressWatchBlind   Name = "progress_watch.blind"
	ProgressWatchCleared Name = "progress_watch.cleared"

	PromptEditNotice  Name = "prompt_edit.notice"
	PromptStaleNotice Name = "prompt_stale.notice"
	PromptSyncNotice  Name = "prompt_sync.notice"

	ReaperEscalation Name = "reaper.escalation"

	RefineryMerged            Name = "refinery.merged"
	RefineryMergeFailed       Name = "refinery.merge_failed"
	RefineryFailureThreshold  Name = "refinery.failure_threshold"
	RefineryDeferDoneBackstop Name = "refinery.defer_done_backstop"
	RefineryDeferredDeath     Name = "refinery.deferred_death"

	RefusalWatchAlarm Name = "refusal_watch.alarm"

	ReviewDeclFinding Name = "review_decl.finding"

	SentinelDrift Name = "sentinel.drift"

	ServiceInstallReport Name = "service.install_report"

	StallWatchMailFallback Name = "stall_watch.mail_fallback"
	// StallWatchBlockedReminder is the blocked-reminder nudge (mg-3844). It is
	// a nudge, not a mail: it is declared here, ahead of the other nudges
	// (slice 4), because `[stall_watch] blocked_reminder_enabled = false` is
	// one of the existing silencing keys slice 2 turns into a mute alias.
	StallWatchBlockedReminder Name = "stall_watch.blocked_reminder"

	StrandedWorkBranch Name = "stranded_work.branch"

	SynthWatchHit     Name = "synth_watch.hit"
	SynthWatchCleared Name = "synth_watch.cleared"

	TurnWatchFinding Name = "turn_watch.finding"

	UsageLimitHit     Name = "usage_limit.hit"
	UsageLimitCleared Name = "usage_limit.cleared"

	WakeWatchBounce     Name = "wake_watch.bounce"
	WakeWatchUnconsumed Name = "wake_watch.unconsumed"

	WorkspaceStale Name = "workspace.stale"

	WorktreePreserved    Name = "worktree.preserved"
	WorktreeUnpushed     Name = "worktree.unpushed"
	WorktreeUndetermined Name = "worktree.undetermined"

	// Delivery mechanics, not alerts.
	SchedulerDelivery Name = "scheduler.delivery"
)

// Tiered watchers: the first-tier box is the coordinator (or the watcher's
// notify_to), and the finding is ALSO sent to the escalation box when it is
// stale or names the coordinator itself. Stated once so every such row reads
// the same way.
const tiered = RoleCoordinator + "; also " + RoleEscalationBox + " when it escalates"

var registry = []Def{
	{AbsentWatchDeclared, ClassAlert, tiered, "absent_watch.notify_to", "a crew agent is absent because auto_start = false declares it so"},
	{AbsentWatchFinding, ClassAlert, tiered, "absent_watch.notify_to", "a crew agent that should be running is absent"},
	{AbsentWatchCleared, ClassAlert, tiered, "absent_watch.notify_to", "an absent-agent episode is over"},
	{AckWatchFinding, ClassAlert, tiered, "ack_watch.notify_to", "a scheduled message completed with no acknowledgement"},
	{AgentEarlyExit, ClassAlert, RoleCoordinator, "", "a polecat died during its cold start"},
	{BlindWatchFinding, ClassAlert, RoleCoordinator + " (" + RoleEscalationBox + "'s copy is blind_watch.out_of_band)", "blind_watch.mail", "the wedge detector cannot judge some agents"},
	{BlindWatchCleared, ClassAlert, RoleCoordinator + " (" + RoleEscalationBox + "'s copy is blind_watch.out_of_band)", "blind_watch.mail", "the wedge detector is judging again"},
	{BlindWatchOutOfBand, ClassAlert, RoleEscalationBox + ", when the detector or coordinator is involved", "blind_watch.mail, blind_watch.out_of_band", "escalation-box copy of a blind-watch finding or clear"},
	{CarrierDriftFinding, ClassAlert, tiered, "carrier_drift.notify_to", "a work item's workflow carrier drifted from its stage"},
	{ConditionNotice, ClassAlert, "the condition's addressee (usually " + RoleCoordinator + ")", "conditions.mail, conditions.disable", "a daemon condition row was raised"},
	{ConditionOutOfBand, ClassAlert, RoleEscalationBox, "conditions.out_of_band", "out-of-band copy of a condition notice"},
	{CredExpiryWarning, ClassAlert, RoleEscalationBox, "", "the harness credential expires soon"},
	{CredExpiryBlind, ClassAlert, RoleEscalationBox, "", "the harness credential's expiry cannot be read"},
	{CrewResetNotice, ClassAlert, "the crew agent", "", "a long-running crew session should reset"},
	{DeafWatchFinding, ClassAlert, tiered, "deaf_watch.notify_to", "a crew agent has no mail check registered"},
	{DeafWatchCleared, ClassAlert, tiered, "deaf_watch.notify_to", "a deaf-agent episode is over"},
	{DriftWatchDrift, ClassAlert, RoleEscalationBox, "", "deployed host artifacts drifted from the source"},
	{DriftWatchNoFire, ClassAlert, RoleEscalationBox, "", "the nightly deploy did not fire"},
	{DriftWatchStaleRevision, ClassAlert, RoleEscalationBox, "", "the running pogod is behind its source"},
	{FilerNotifyCompleted, ClassAlert, "the work item's filer (" + RoleCoordinator + " when redirected)", "", "a work item the recipient filed has completed"},
	{FilerNotifyUndeliverable, ClassAlert, RoleCoordinator, "", "a completion notice could not reach its filer"},
	{FirstTurnFinding, ClassAlert, tiered, "first_turn.notify_to", "crew agents never completed a first turn"},
	{FirstTurnCleared, ClassAlert, tiered, "first_turn.notify_to", "a first-turn episode is over"},
	{GHIntakeFinding, ClassAlert, tiered, "gh_intake.notify_to", "GitHub issues are not reaching intake"},
	{GHTeardownFinding, ClassAlert, tiered, "gh_teardown.notify_to", "a GitHub-track item was not torn down"},
	{HeartWatchFinding, ClassAlert, RoleCoordinator + ", or " + RoleEscalationBox + " instead when the coordinator is stale", "", "crew heartbeats are stale"},
	{HeartWatchCleared, ClassAlert, RoleCoordinator + " (and " + RoleEscalationBox + " if it was told)", "", "crew heartbeats are fresh again"},
	{MergedOpenNotClosed, ClassAlert, RoleCoordinator, "", "a merged work item was not closed"},
	{MidSessionWedgeExhausted, ClassAlert, tiered, "mid_session_wedge.notify_to", "mid-session wedge recovery attempts are spent"},
	{ModalRateLimitDismissed, ClassAlert, RoleCoordinator, "", "a rate-limit modal was auto-dismissed"},
	{OrphanPolecat, ClassAlert, RoleCoordinator, "", "a polecat process outlived its registration"},
	{ProgressWatchFinding, ClassAlert, tiered, "progress_watch.notify_to", "the fleet is awake but not producing"},
	{ProgressWatchBlind, ClassAlert, RoleCoordinator, "progress_watch.notify_to", "the progress detector cannot measure"},
	{ProgressWatchCleared, ClassAlert, tiered, "progress_watch.notify_to", "a no-progress episode is over"},
	{PromptEditNotice, ClassAlert, "the agent whose prompt changed", "", "an agent's prompt was edited under it"},
	{PromptStaleNotice, ClassAlert, "the agent whose prompt is stale", "", "a running agent's prompt is stale"},
	{PromptSyncNotice, ClassAlert, RoleCoordinator, "", "deployed prompts are not reconciled with the source"},
	{ReaperEscalation, ClassAlert, RoleCoordinator + " and " + RoleEscalationBox, "", "the launchd reaper gave up kickstarting a job"},
	{RefineryMerged, ClassAlert, RoleCoordinator, "", "a merge request merged"},
	{RefineryMergeFailed, ClassAlert, RoleCoordinator + " and the merge author", "", "a merge request failed"},
	{RefineryFailureThreshold, ClassAlert, RoleCoordinator, "", "an author reached the consecutive merge-failure threshold"},
	{RefineryDeferDoneBackstop, ClassAlert, RoleCoordinator, "", "a --defer-done polecat lingered after its merge and was reaped"},
	{RefineryDeferredDeath, ClassAlert, RoleCoordinator, "", "a deferred polecat died after its merge, before mg done"},
	{RefusalWatchAlarm, ClassAlert, RoleEscalationBox, "", "a refusal outage alarm (the wedge alarm)"},
	{ReviewDeclFinding, ClassAlert, RoleCoordinator, "review_decl.notify_to", "review declarations are missing"},
	{SentinelDrift, ClassAlert, RoleCoordinator, "", "a harness prompt-ready sentinel stopped matching"},
	{ServiceInstallReport, ClassAlert, RoleCoordinator, "", "`pogo service install` report"},
	{StallWatchMailFallback, ClassAlert, "the stall notice's recipient", "", "a stall notice that could not be typed into a terminal"},
	{StallWatchBlockedReminder, ClassAlert, "the agent an item is blocked on (" + RoleCoordinator + " when it is unreachable)", "stall_watch.blocked_reminder_enabled", "nudge: work items are blocked on the recipient"},
	{StrandedWorkBranch, ClassAlert, RoleCoordinator + " and the repo's pm-<repo>, if it exists", "", "a stopped polecat left unmerged work"},
	{SynthWatchHit, ClassAlert, RoleEscalationBox, "", "synthetic error messages are being emitted"},
	{SynthWatchCleared, ClassAlert, RoleEscalationBox, "", "a synthetic-error episode is over"},
	{TurnWatchFinding, ClassAlert, RoleCoordinator + ", or " + RoleEscalationBox + " instead when the coordinator is stuck", "", "an agent is stuck mid-turn"},
	{UsageLimitHit, ClassAlert, RoleEscalationBox, "", "the fleet hit a usage limit"},
	{UsageLimitCleared, ClassAlert, RoleEscalationBox, "", "a usage-limit episode is over"},
	{WakeWatchBounce, ClassAlert, "the sender of the unread mail", "", "mail landed for an agent that is not running"},
	{WakeWatchUnconsumed, ClassAlert, RoleCoordinator, "", "an agent is not consuming its mail"},
	{WorkspaceStale, ClassAlert, RoleCoordinator, "", "a workspace checkout is behind its upstream"},
	{WorktreePreserved, ClassAlert, RoleCoordinator, "", "a worktree with uncommitted work was preserved"},
	{WorktreeUnpushed, ClassAlert, RoleCoordinator, "", "a worktree with unpushed commits was preserved"},
	{WorktreeUndetermined, ClassAlert, RoleCoordinator, "", "a worktree whose state could not be read was preserved"},
	{SchedulerDelivery, ClassDelivery, "the schedule's target", "", "a scheduled message delivered by mail"},
}

var byName = func() map[Name]Def {
	m := make(map[Name]Def, len(registry))
	for _, d := range registry {
		m[d.Name] = d
	}
	return m
}()

// Lookup returns the declaration for name.
func Lookup(name Name) (Def, bool) {
	d, ok := byName[name]
	return d, ok
}

// All returns every declaration, alerts and deliveries, sorted by name.
func All() []Def {
	out := append([]Def(nil), registry...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
