// Package ackwatch turns the scheduler's completion counters into an alert.
//
// # The gap this closes
//
// mg-a754 gave every scheduled fire a completion signal: the agent runs
// `pogo schedule ack` when the fire's work is done, and the scheduler keeps
// FiresDelivered / FiresCompleted / UnackedStreak on the persisted Entry.
// `pogo schedule list` even renders a `⚠ N unacked` marker. Nothing read any of
// it.
//
// On 2026-07-29 a crew agent had been completing 36% of its mail-check fires
// FOR ITS ENTIRE RUN:
//
//	mail-check-architect     751/757
//	mail-check-pa            753/757
//	mail-check-pm-onethird   751/757
//	mail-check-pm-pogo       270/757   <-- 36%
//
// It was found by a human eyeballing that table and comparing rows. Every
// liveness instrument said healthy: the process was alive, `pogo agent
// diagnose` reported health=healthy with last-activity 0s ago, and PTY output
// was flowing — Claude Code's working spinner is itself PTY output, so an
// output-based stall check can never fire on a spinning agent. The agent was
// emitting malformed tool calls that the harness renders as inert text; nothing
// executed, and it believed it had acted. See mg-1935, and mg-60ca for the
// wedge class that restart_on_crash cannot catch because nothing crashed.
//
// The completion ratio is the one number that saw through the spinner, because
// it measures COMPLETED WORK rather than liveness. This package is the reader
// it never had.
//
// # WHAT THIS MEASURES: delivery, not diligence
//
// Read as a measure of an agent's diligence, this number produces alerts in
// exactly the direction that discredits the tool — and mg-ddf7 established by
// measurement that the diligence reading is not merely unsafe but arithmetically
// empty. For any schedule,
//
//	FiresCompleted/FiresDelivered  ==  1/mean_attention_gap  -  outstanding/delivered
//
// exactly, where the attention gap is deliveries per ack cycle. That held to zero
// error across all 114 schedules in this fleet's event log. The ratio IS the
// reciprocal of the agent's turn length measured in cadence periods, corrected
// for where you stopped looking. There is no residual term left over for
// diligence to live in.
//
// The consequence is not subtle: an agent whose turns run longer than its
// cadence CANNOT score 100%, however perfectly it behaves. On the 2026-07-29
// storm night `pa` acked every single token it was handed and read 83%, with a
// longest unacked run of 9 — nine fires delivered into one turn, eight of whose
// tokens the scheduler itself superseded before pa could see them. The mayor
// read 29/37 on the same night for the same reason, having acked every token it
// was handed including one that arrived in a mail body after its nudge failed
// the idle gate.
//
// So the number to read for the failure this package exists to catch is
// UnackedStreak — the run length — because it is the statistic that does not
// saturate: a busy-but-healthy agent's run is bounded by its own turn length,
// and a wedged agent's climbs without bound (202, for the mayor, during the
// 2026-07-22 outage). See populations.go, and
// docs/investigations/ack-deficit-populations-2026-07-30.md for the full record.
//
// # Why the comparison is cross-agent — and the alternative that was REJECTED
//
// pm-pogo was always broken, from its first fire. A detector that compared an
// agent to its OWN history would have flagged nothing — there was no regression
// to see, only a plateau. What made it diagnosable was the neighbours: 36%
// against ~99% on the identical cadence is a per-agent fault, whereas everyone
// at 40% would be a scheduler or fleet fault. So the primary rule compares a
// schedule to its PEERS — same kind, same cadence, comparable fire count — and
// the whole-cohort case is reported separately (see Report.Fleet) rather than
// as N per-agent findings.
//
// REJECTED ALTERNATIVE: compare a schedule against its own history.
// REASON: bursty delivery then becomes a false-positive generator, because the
// ratio is an inverted attention gap (above) and an agent's attention gap moves
// whenever its workload does. Every such false positive would look EXACTLY like
// the pm-pogo 36% incident that motivated this package — a plateau at a low
// rate, on a live-looking agent — so the failure mode of the cheaper design is
// indistinguishable from the fault the expensive design exists to find. That is
// why the cross-agent comparison is not a nicety to be simplified away later:
// it is what makes the metric usable at all.
//
// This paragraph is here, rather than only in mg-ddf7, because it explains a
// CHOICE rather than describing behaviour, and that is precisely the class of
// note a later editor deletes as redundant.
//
// # The denominator: already deliveries, not dues — and the repair that would
// # have made it worse
//
// A recurring proposal is "count fires DELIVERED, not fires DUE — a batch of
// eight is ONE delivery". Measured (mg-ddf7): the denominator already counts
// deliveries. scheduler.Tick collapses every missed period into a single fire
// and calls recordDeliveryLocked once, so across this fleet's whole log 60830
// due periods produced 54021 deliveries — 6809 dues, 11%, already collapsed.
// Changing FiresDelivered to count deliveries is a no-op, and anyone who lands
// it will believe they fixed something.
//
// The batching that actually hurts is not several dues collapsing into one fire.
// It is several genuinely separate deliveries landing inside one agent turn,
// where only the newest token is redeemable.
//
// REJECTED ALTERNATIVE: exclude superseded fires from the denominator — count
// one ackable opportunity per batch.
// REASON: it is CIRCULAR. A fire is superseded precisely when nothing acked it
// before the next arrived, so this subtracts the deficit from its own
// denominator; what remains is completed/(completed+outstanding), a function of
// the ack COUNT alone. Any schedule with 20 acks then reads >= 95% whether the
// agent is perfect or wedged. Measured against the one true positive in this
// fleet's history, it lifts pm-pogo's calm-window reading from 50% to 97% and
// its peer gap from 50 points to 1 — the finding disappears. That is the
// mg-7254 failure (a signal pinned HEALTHY, producing false calm) substituted
// for this one (pinned UNHEALTHY, producing false noise); both end with the
// detector being ignored. Pinned by
// TestSplit_ExcludingBatchedFires_IsCircular_AndPinsTheSignalHealthy.
//
// The same objection rules out raising Floor or MinGap to quiet a storm: that
// converts noise into silence without ever restoring the signal's ability to
// vary. A repair has to un-pin the signal, not move the threshold it is read at.
//
// # When this control correctly declines to fire, it says so
//
// A silent correct outcome and a control that is not running are the same
// observation. On the 2026-07-29 storm night this detector would correctly have
// raised nothing, and nothing anywhere would have recorded that it considered
// the burst and declined — so the quiet reads as absence, and the next editor
// removes the guard as dead weight. Watcher.sample therefore emits
// ack_watch_clear on the no-findings path, carrying the coverage counts, for the
// same reason ack_watch_suppressed exists on the suppressed path.
//
// # Why an absolute floor as well
//
// The peer gap alone would fire on a fleet where everyone is merely good and
// one agent is slightly less good. Both conditions must hold: the schedule must
// be far below its peers AND below an absolute floor. That keeps the detector
// quiet in exactly the regime where a human reader would shrug.
//
// # The blind spot of every outlier detector, and the second arm that covers it
//
// A peer-relative test keys on DISPERSION. The worse an outage is the more
// UNIFORM it is, so the detector is weakest exactly where the failure is worst:
// when every schedule degrades in lockstep the median falls with the members and
// the gap stays near zero. That is not an arithmetic bug, it is the defining
// property of comparing agents to each other.
//
// Measured, on 2026-08-09 (mg-e2a4). Every crew agent and polecat stopped
// completing turns at ~12:50Z and resumed at ~17:20Z — an `ENOTFOUND` on the
// model API, classified by pogod's own wedge-watch as `cause=network_down`.
// `pogo schedule list` at 17:21Z showed the signature plainly: every mail-check
// schedule carrying the SAME deficit, `⚠ 27 unacked`, 27 fires x 10 minutes =
// 4.5 hours, identical on all six. Over 13:20-17:20 the events log holds 251
// `scheduler_fire_delivered` against 3 `scheduler_fire_completed`. And every
// `ack_watch_fired` that day reported `deficit_count: 0`, including the one that
// fired mid-outage at 16:12:59. A 100%-dead fleet produced zero per-schedule
// findings, and the reason is above: there was no outlier to find.
//
// So the per-schedule rule needs a PARTNER that keys on something other than
// dispersion. Report.Blackout is it: a single fleet-wide finding built from the
// ABSOLUTE completion rate over a trailing window — fires are being delivered to
// agents that are RUNNING and nothing is completing them — with no median, no
// peer set, and no comparison of any agent to any other. It gets MORE confident as the failure
// gets more uniform, which is the exact inverse of the peer test's failure mode,
// and that is the point of having both.
//
// The peer-relative test is NOT deleted or weakened. It catches the single deaf
// agent, which is a real and common shape (mg-1935 is the whole reason this
// package exists), and an absolute rule alone would be noisy across schedules
// with legitimately different cadences and turn lengths. Two arms, two failure
// modes, neither one sufficient.
//
// # Why the windowed arms read EVENTS rather than the counters
//
// Report.Fleet already reported a whole cohort below the floor, and it FIRED
// during this outage (`fleet_count: 1` in every firing that day). It was still
// useless, for two reasons this arm has to fix rather than repeat:
//
//  1. The counters are LIFETIME totals, so a cohort median below the floor
//     cannot distinguish "dead right now" from "carried a bad ratio for days".
//     A blackout is a statement about the last few hours, and only a windowed
//     measurement can make it.
//  2. The counters are ZEROED by re-registration, which the nightly redeploy
//     guarantees (mg-42ac), and MinFires then holds every schedule ineligible
//     for over three hours afterwards. An outage inside that window is
//     invisible to anything reading counters. The events log is where a fire
//     delivery and a fire completion both survive a restart — the same reason
//     populations.go reads events.
//
// Hence Snapshot.Recent, supplied by the caller (see source.RecentFires) so
// Detect stays pure. When the caller cannot measure it, that is recorded and
// rendered rather than read as calm — a blind arm and a quiet arm must never be
// the same observation.
//
// # Reason 1 was never fixed for the cohort arm, and cost 61 hours (mg-c232)
//
// The paragraph above diagnosed Report.Fleet correctly and then left it running
// on the diagnosed instrument. It kept triggering on the lifetime median, and a
// lifetime ratio is monotone in past damage: on 2026-08-10 two outages that had
// ALREADY ENDED — the crew stopped 01:56-12:40Z, a network loss 14:24-17:15Z —
// put ~80 dead fires into every 10-minute schedule. The cohort median went
// 40% -> 36% -> 26% over a day the fleet spent recovering, and "1 whole cohort
// below the completion floor" stayed escalated to the mayor for 61 hours. Its
// only exits were a counter reset (which hides the signal rather than clearing
// it) and being ignored.
//
// So detectCohort now judges the same statistic the blackout arm does — absolute
// completion rate over Params.BlackoutWindow, behind the same liveness gate —
// restricted to one cohort's schedules. Swept against this fleet's real events
// log for 2026-08-01..13, with the liveness gate reconstructed from
// agent_spawned/agent_stopped: 274 judged 30-minute samples, 77 firing, in SIX
// bounded episodes, every one of which ENDED. The instrument it replaced fired
// on 67 of 67 samples and ended none of them.
//
// The per-schedule peer arm keeps its lifetime ratio, because a lifetime average
// is what makes turn-length noise cancel (see the arithmetic above: the ratio is
// an inverted attention gap, and an agent's attention gap moves hourly). It
// gains a one-way RECENCY VETO instead — see recovered. A window may retire a
// lifetime finding and may never raise one, so the statistic that is too noisy
// to trigger on is safe to acquit on.
//
// # On suppressing during a known outage
//
// mg-c232 asked whether a cohort finding should be suppressed on a recent
// outage marker, the way stall-watch suppresses on a recent system_wake. It is
// not wired, and the reason is that windowing subsumes it: an outage that ended
// leaves the trailing window on its own, within BlackoutWindow, with no list of
// outage causes to keep current and nothing to go stale. A suppression list
// would have to name every cause — model API, network, credentials, host sleep —
// and would fail silently on the first one nobody added. The system_wake and
// restart suppressions remain, because those describe a distorted MEASUREMENT
// (replayed fires, zeroed counters) rather than a fault that has ended.
//
// # The two false-positive paths this arm has, both measured and both closed
//
// An absolute rule is easy to write and easy to write BADLY, because "fires
// delivered and nothing completing them" describes three different situations
// and only one of them is a fault. Both wrong ones were found by sweeping the
// real events log rather than by reasoning, and each has a gate:
//
//  1. THE FLEET IS NOT THERE. Between 00:00 and 09:30 on 2026-08-09 the
//     scheduler delivered ~30 mail-check fires an hour and completed zero of
//     them, every hour, all night — because no crew agent was running. Ungated,
//     this arm mails a person at 4am nightly, and an alarm that cries wolf every
//     night is strictly worse than the silence it replaced. Gate:
//     Snapshot.RunningSince, and only agents up for the WHOLE window are judged
//     (a spawn 40 minutes ago cannot speak for the last three hours — that
//     produced a measured false positive at 10:00Z, right after the crew came
//     up).
//
//  2. THE FLEET IS WORKING IN LONG TURNS. A fire is only ackable at the end of
//     an agent turn, so completions arrive in bursts. On the same day, with all
//     six crew agents up and demonstrably working, there was a TWO-HOUR stretch
//     with 84 fires delivered and none completed. Gate: DefaultBlackoutWindow,
//     set to 3 hours because that is the shortest window with no false positive
//     in the sweep recorded there. The cost is stated in that comment and it is
//     real: detection lands about three hours into an outage.
//
// After both gates, across the whole of 2026-08-09 at 30-minute steps, the arm
// fired on 4 samples — all inside the outage — and on no others.
//
// # Who a blackout is addressed to, and why that is not a timeout
//
// The routing is the other half of mg-e2a4, and it was the more expensive half.
// At 16:12:59, mid-outage, the fleet arm fired and mailed exactly one recipient:
// `mayor` — an agent whose own mail-check carried the same 27 unacked fires.
// Escalation to a person requires DefaultEscalateAfter (24h) of persistence, so
// a 4.5-hour total outage generated one notice, addressed to the outage.
//
//	The only recipient of a fleet-outage alarm was a member of the fleet.
//	A detector inside the job cannot report the job not running.
//
// A blackout therefore escalates on its FIRST sample, structurally, and NOT on
// an age gate — see Watcher.sample and deafwatch.escalateNow, which is the same
// rule for the same reason. Patience cannot help a recipient that is inside the
// failure. "One agent is lagging its peers" and "nothing in the fleet has
// completed a turn in an hour" do not share an escalation clock, and a negative
// EscalateAfter disables only the AGE-based escalation.
//
// The asymmetry that makes this work is that POGOD SURVIVED: it kept sampling
// and kept writing throughout, while every agent it hosts was inert. So the
// notice is written by the one live component and consumed by an out-of-process
// reader of the escalation box (see `[agents] escalation_box`), with no fleet
// agent, no agent turn, and no schedule anybody has to ack anywhere on that
// path.
//
// Two honesty requirements follow, and both are wired rather than described.
// Blackout.StalledAgents names the population, so a recipient that is ITSELF in
// it can be reported as such instead of being quietly mailed. And a blackout
// notice that fails to send is emitted as ack_watch_blackout_unreported: an
// alarm that reached nobody is this ticket's bug, one level up.
//
// # Counters reset on re-registration — and that is load-bearing here
//
// Registering a schedule with an `--id` that already exists REPLACES the entry
// and zeroes its counters (see scheduler.Add). Every crew agent re-registers
// its mail-check on startup, and there is a nightly redeploy (mg-42ac), so a
// naive absolute-floor rule would flag the whole crew every morning. Measured
// on 2026-07-29 03:03: `mail-check-mayor 6/7` before re-registration, `—`
// after, with one `pogo schedule` call in between.
//
// This package does NOT try to preserve the counter across re-registration —
// that trades a detector problem for a semantics problem, since a preserved
// ratio would mix fires from before and after a cadence or prompt change and
// then describe no single regime. It treats a reset counter as what it is: a
// known-benign event that makes the ratio unrepresentative for a while, exactly
// like a system_wake. Both are handled by the same mechanism (Params.SettleAfter
// plus Params.MinFires), not by two.
package ackwatch

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Sample is one schedule's completion state at a point in time — the subset of
// scheduler.Entry this detector reads. It is a plain struct with no scheduler
// dependency so tests build fixtures directly instead of standing up a store
// (mg-6092, mg-e8e7 and mg-5336 are three tickets for tests that read live
// ~/.pogo scheduler state; this package must not add a fourth).
type Sample struct {
	Agent string `json:"agent"`
	ID    string `json:"id"`
	// Kind is scheduler.ScheduleKind as a string ("mail-check", "sweep", …).
	// Half of the cohort key: a sweep and a mail-check are not peers.
	Kind string `json:"kind"`
	// Cadence is the spacing between consecutive firings. The other half of the
	// cohort key. Zero for a one-shot or an unparseable cron, which makes the
	// sample ineligible — a rate over a schedule that fires once is meaningless.
	//
	// Omitted from JSON rather than rendered as the nanosecond integer a
	// time.Duration marshals to. A custom MarshalJSON is the obvious fix and is
	// a trap here: Finding embeds Sample, so the method would be promoted and
	// silently swallow every field Finding adds. FleetFinding carries the
	// cadence as a string where it is actually needed.
	Cadence time.Duration `json:"-"`
	// CreatedAt is when this counter last started from zero. Registration sets
	// it, and re-registration RESETS it, which is precisely the property that
	// makes it the right freshness gate here.
	CreatedAt time.Time `json:"created_at"`

	FiresDelivered int       `json:"fires_delivered"`
	FiresCompleted int       `json:"fires_completed"`
	UnackedStreak  int       `json:"unacked_streak"`
	LastCompletion time.Time `json:"last_completion,omitempty"`

	// EverAcked mirrors scheduler.Entry.EverAcked: has this (agent, id) EVER
	// had a fire acknowledged, across re-registrations. The counters above are
	// zeroed by the boot-path re-registration and this bit is not, which is the
	// whole reason it exists — see Tracked (mg-00d6).
	EverAcked bool `json:"ever_acked,omitempty"`
}

// Rate is the completed:delivered ratio, or 0 when nothing has been delivered.
func (s Sample) Rate() float64 {
	if s.FiresDelivered <= 0 {
		return 0
	}
	return float64(s.FiresCompleted) / float64(s.FiresDelivered)
}

// Tracked reports whether this schedule has ever had a fire acknowledged,
// mirroring scheduler.Entry.CompletionTracked — including the EverAcked
// disjunct, which is load-bearing here and not merely cosmetic.
//
// Reading FiresCompleted alone made this false for a crew schedule for as long
// as it went without acking after a re-registration, because re-registration
// zeroes the counters. Since ackAwareCohort requires a MAJORITY of tracked
// peers, a cohort whose majority had booted and not yet acked suspended both
// ratio arms — and for a schedule whose agent never comes back, "not yet" is
// forever, because the only thing that clears the state is an ack (mg-00d6).
//
// The bound is worth keeping in view: zeroing requires a boot, survivors carry
// the cohort, and a healthy agent re-arms itself within one cadence period.
// See TestBounce_TheBlindCaseNeedsAZEROEDMAJORITY_NotJustABounce.
func (s Sample) Tracked() bool { return s.FiresCompleted > 0 || s.EverAcked }

// cohortKey groups genuinely comparable schedules. Kind and cadence only —
// fire-count comparability is a per-candidate test (see comparable), because it
// is relative to the candidate rather than a property of the group.
func (s Sample) cohortKey() string {
	return s.Kind + "|" + s.Cadence.String()
}

// Snapshot is one reading of the fleet's scheduler state plus the context
// needed to decide whether that reading means anything.
type Snapshot struct {
	// Now is the sample time.
	Now time.Time
	// Samples is every schedule known to the scheduler, filtered or not — Detect
	// applies its own eligibility rules and reports what it excluded.
	Samples []Sample
	// LastDisruption is the most recent event that makes the whole table
	// unrepresentative: a system_wake (post-sleep replay makes stale acks
	// expected) or a pogod start. Zero means none known.
	//
	// Note the asymmetry with per-sample freshness: a disruption suppresses the
	// ENTIRE report, because after a wake it is the relationship between
	// schedules — not just one counter — that is untrustworthy.
	LastDisruption time.Time
	// DisruptionReason names it for the suppression message ("system_wake",
	// "pogod restart"). Cosmetic; the suppression turns on LastDisruption alone.
	DisruptionReason string
	// Recent is the fleet-wide trailing-window count of fire deliveries and
	// completions, read from the events log by the caller (source.RecentFires).
	// It is what the blackout arm judges, and the counters cannot supply it: see
	// the package header, "Why the blackout arm reads EVENTS".
	//
	// A nil Recent means the caller did not offer the measurement, which is
	// reported as BlackoutBlind rather than as a clean blackout arm. A non-nil
	// Recent carrying Err means the caller tried and could not — same treatment,
	// different reason string.
	Recent *Recent
	// RunningSince maps every agent the caller considers RUNNING to the moment it
	// started. It is the blackout arm's liveness gate, and it is not optional: nil
	// means the caller did not supply it and the arm reports itself blind.
	//
	// The START TIME is load-bearing, not decoration. An agent is only judged once
	// it has been up for the WHOLE window being measured, because the window
	// otherwise reaches back into a period when that agent did not exist — and the
	// scheduler was delivering to its schedule the entire time it was gone. That
	// produced a measured false positive at 10:00Z on 2026-08-09: the crew came up
	// at 09:35Z, the 3-hour window still contained 07:00-09:35Z of deliveries to a
	// fleet that was not there, and the arm read 6/90 = 6.7%.
	//
	// It is required because "fires delivered, nothing completed" is ALSO what an
	// absent fleet looks like. Measured on this box: between 00:00 and 09:30 on
	// 2026-08-09 the scheduler delivered ~30 mail-check fires an hour and
	// completed ZERO of them, every hour, all night — because no crew agent was
	// running (all six were spawned at 09:35Z, `agent_spawned` x14 in that
	// bucket). Without this gate the arm would mail a person at 4am every night,
	// and an alarm that cries wolf nightly is worse than the silence it replaced.
	//
	// The gate is also the honest statement of what the finding MEANS: the fleet
	// is up and is not completing work. During the 2026-08-09 outage every agent
	// read `status=running` with `last-activity=just now`, so the running set was
	// full while nothing was being done — which is exactly the asymmetry pogod
	// can see and the fleet cannot report.
	RunningSince map[string]time.Time
}

// Recent is one windowed measurement of the fleet's fire traffic. Counted from
// events rather than the persisted counters, so it survives the nightly
// re-registration that zeroes them, and so it describes NOW rather than a
// lifetime.
//
// Like Sample, it is a plain struct with no events dependency: tests in this
// package build it by hand instead of reading the developer's live ~/.pogo (see
// the note at the top of source.go).
type Recent struct {
	// Window is the trailing span measured, ending at Snapshot.Now.
	Window time.Duration `json:"-"`
	// Delivered and Completed are scheduler_fire_delivered and
	// scheduler_fire_completed counts inside the window, over every schedule.
	Delivered int `json:"delivered"`
	Completed int `json:"completed"`
	// Schedules is how many distinct (agent, id) schedules were delivered to,
	// and Agents names the distinct agents. A blackout is a claim about a
	// population, so the population is carried with it.
	Schedules int      `json:"schedules"`
	Agents    []string `json:"agents"`
	// ByAgent breaks the same window down per recipient, so the arm can be
	// restricted to agents that are actually RUNNING (see
	// Snapshot.RunningAgents). The totals above are the whole window and are for
	// reporting; the judgement is made on the running subset.
	ByAgent map[string]AgentFires `json:"by_agent,omitempty"`
	// BySchedule breaks the window down per (agent, id), which is the grain the
	// COHORT arm and the peer arm's recency gate both need: a cohort is a set of
	// schedules, not a set of agents, and one agent can hold several.
	//
	// Keyed by scheduleKey — agent and id joined by a NUL, which no agent name or
	// schedule id contains. Unexported from JSON for that reason: a NUL inside a
	// map key is legal Go and hostile output. Every consumer that needs a number
	// out of here carries it in a finding field instead.
	BySchedule map[string]ScheduleFires `json:"-"`
	// LastCompletedAt is the newest completion seen in the window, zero when
	// there was none. It answers "how long since anything at all worked", which
	// is the number a person reading the alarm wants first.
	LastCompletedAt time.Time `json:"last_completed_at,omitempty"`
	// Err, when non-empty, says why this window could not be measured. A
	// measurement that failed must not arrive looking like a measurement of
	// zero.
	Err string `json:"err,omitempty"`
}

// AgentFires is one agent's share of a window.
type AgentFires struct {
	Delivered int `json:"delivered"`
	Completed int `json:"completed"`
	Schedules int `json:"schedules"`
}

// ScheduleFires is one schedule's share of a window.
type ScheduleFires struct {
	Delivered int `json:"delivered"`
	Completed int `json:"completed"`
	// LastCompletedAt is the newest completion by this schedule inside the
	// window, zero when there was none.
	LastCompletedAt time.Time `json:"last_completed_at,omitempty"`
}

// scheduleKey is the (agent, id) key used by Recent.BySchedule. NUL-joined
// because it appears in no agent name and no schedule id, so no pair of
// distinct schedules can collide onto one key.
func scheduleKey(agent, id string) string { return agent + "\x00" + id }

// Rate is the windowed completed:delivered ratio, or 0 when nothing was
// delivered. Absolute: no median, no peers, nothing compared to anything.
func (r Recent) Rate() float64 {
	if r.Delivered <= 0 {
		return 0
	}
	return float64(r.Completed) / float64(r.Delivered)
}

// restrictTo sums the window over the named agents only, returning the subset's
// delivered, completed and schedule counts plus the agents that completed
// NOTHING. Used to apply the liveness gate: an agent that is not running is not
// evidence of anything.
func (r Recent) restrictTo(want map[string]bool) (delivered, completed, schedules int, judged, silent []string) {
	for name, f := range r.ByAgent {
		if !want[name] || f.Delivered == 0 {
			continue
		}
		delivered += f.Delivered
		completed += f.Completed
		schedules += f.Schedules
		judged = append(judged, name)
		if f.Completed == 0 {
			silent = append(silent, name)
		}
	}
	sort.Strings(judged)
	sort.Strings(silent)
	return delivered, completed, schedules, judged, silent
}

// Params tunes the detector. The zero value is not usable; call
// DefaultParams and override.
type Params struct {
	// MinFires is how many fires a schedule must have been delivered before its
	// ratio is allowed to mean anything. This is the primary defence against the
	// nightly-redeploy false-positive storm: a freshly re-registered schedule
	// reads 0/0 and stays ineligible until it has accumulated a real sample.
	MinFires int
	// MinPeers is how many COMPARABLE peers a candidate needs before it can be
	// judged. Below this the fleet is not offering a comparison, and the
	// detector declines to invent one from a single neighbour.
	MinPeers int
	// ScaleBand bounds how different a peer's fire count may be from the
	// candidate's and still count as comparable, as a multiplicative factor.
	// 2 means "between half and double". Agents re-register at different
	// moments, so for a window after each bounce the fleet is a mix of fresh and
	// accumulated counters; without this band a 4-hours-old 24/24 would be
	// averaged against a 3-days-old 700/710 as though they measured the same
	// thing.
	ScaleBand float64
	// MinGap is how far below the peer median a schedule's rate must sit to be a
	// finding, in absolute ratio points. 0.2 means 20 percentage points.
	MinGap float64
	// Floor is the absolute ceiling on a finding's rate: a schedule at or above
	// it is never reported however far below its peers it sits. Peer gap AND
	// floor, never either alone.
	//
	// The two must be chosen together or one of them does nothing. A finding
	// needs rate <= median-MinGap AND rate < Floor; since median <= 1, the floor
	// is dead weight whenever 1-MinGap <= Floor. At the defaults (0.2, 0.75)
	// that is 0.80 > 0.75, so the floor binds for any cohort with a median above
	// 0.95 — which is every healthy cohort observed, and therefore the gate that
	// actually decides the real cases.
	Floor float64
	// SettleAfter is how long after a disruption (or a counter reset) the
	// numbers are treated as unrepresentative.
	SettleAfter time.Duration

	// BlackoutWindow is the trailing span the ABSOLUTE fleet completion rate is
	// measured over. Only used to describe and validate Snapshot.Recent — the
	// caller does the measuring.
	BlackoutWindow time.Duration
	// BlackoutRate is the absolute windowed completion rate at or below which
	// the fleet is declared blacked out. Severity keys on THIS rather than on
	// dispersion, which is the whole point of the second arm.
	BlackoutRate float64
	// BlackoutMinDeliveries is how many fires must have been delivered inside
	// the window before its rate means anything. It is the blackout arm's whole
	// defence against a sleeping laptop: nothing fires while the host is
	// suspended, so a quiet window is a SMALL window, and a small window is
	// ineligible rather than alarming.
	BlackoutMinDeliveries int
	// BlackoutMinSchedules is how many distinct schedules must have been
	// delivered to inside the window. Mirrors MinPeers+1: below a cohort's worth
	// of schedules, "the fleet" is a claim the sample cannot support.
	BlackoutMinSchedules int
	// BlackoutMinAgents is how many RUNNING agents must have been delivered to
	// inside the window. Schedules can pile up on one agent; agents cannot, so
	// this is the count that makes "the fleet" true rather than "one wedged
	// agent with three schedules".
	BlackoutMinAgents int

	// RecoveryMinFires is how many fires ONE schedule must have been delivered
	// inside the window before that window is allowed to RETIRE a lifetime peer
	// finding. See recovered: the window may only quiet a finding, never raise
	// one, so this floor exists to stop a two-fire window from acquitting a deaf
	// agent — not to stop a false alarm, which this path cannot produce.
	RecoveryMinFires int
}

// Detector defaults. Tuned against the 2026-07-29 observation: peers at ~99%,
// the faulty agent at 36%, all four on an identical 10-minute cadence.
const (
	// DefaultMinFires is 20 — a bit over three hours at the crew's 10-minute
	// mail-check cadence. Long enough that a morning's redeploy does not produce
	// findings, short enough that a fault has most of the day to be caught.
	DefaultMinFires = 20
	// DefaultMinPeers is 2, i.e. a cohort of three. Two peers give a median that
	// one outlier cannot single-handedly define.
	DefaultMinPeers = 2
	// DefaultScaleBand is 2 — a peer must be within half to double the
	// candidate's fire count.
	DefaultScaleBand = 2.0
	// DefaultMinGap is 0.20. The observed fault was a 63-point gap; 20 points
	// leaves ample headroom above the few-point spread of a healthy fleet
	// (751/757 vs 753/757 is under one point), and — see Params.Floor — leaves
	// the floor with real work to do rather than making it decorative.
	DefaultMinGap = 0.20
	// DefaultFloor is 0.75. A schedule completing three quarters of its fires is
	// not the failure this exists to catch, whatever its neighbours manage.
	DefaultFloor = 0.75
	// DefaultSettleAfter is 30 minutes: long enough to cover a post-sleep replay
	// burst, short enough that a napping laptop does not blind the detector all
	// day. Restart freshness is mostly enforced by MinFires, which is far
	// stricter in wall-clock terms.
	DefaultSettleAfter = 30 * time.Minute

	// DefaultBlackoutWindow is 3 hours, and the number was MEASURED rather than
	// chosen. It is the one parameter here with a real cost attached, so the
	// measurement is written down.
	//
	// A healthy fleet does not ack smoothly. Completions arrive in bursts,
	// because a fire is only ackable at the end of an agent turn and turns run
	// long: on 2026-08-09, with all six crew agents up and demonstrably working,
	// there was a TWO-HOUR stretch (10:30-12:30Z) with 84 fires delivered and
	// zero completed. That is not a fault, it is what a fleet doing long pieces of
	// work looks like from the ack counters.
	//
	// So this window was swept against that day's real events log, gated on the
	// crew being up, with the outage running 12:50-17:20Z:
	//
	//	window   fires during outage   first fired   FALSE POSITIVES
	//	1h              21               13:55            9
	//	2h              17               14:45            4
	//	2h30m           14               15:15            1
	//	3h              12               15:45            0
	//	4h               8               16:25            0
	//
	// 3h is the shortest window with no false positive, and the cost is stated
	// plainly: detection lands about three hours into an outage, not thirty
	// minutes. THE RESIDUAL IS REAL — an outage shorter than roughly three hours
	// will not be caught by this arm at all, because this fleet produces
	// two-hour zero-completion stretches while perfectly healthy and no
	// completion-based measurement can separate the two sooner.
	//
	// Three hours is nonetheless the difference between a notice and none: on the
	// day this was filed, nothing reached a person for 4.5 hours and the human
	// found out because it was his own wifi. Do not shorten this without
	// re-running the sweep on data containing a real outage; a shorter window
	// buys latency with false alarms, and a false alarm on this channel costs the
	// channel.
	DefaultBlackoutWindow = 3 * time.Hour
	// DefaultBlackoutRate is 0.10. The 2026-08-09 outage read 3/251 = 1.2% over
	// four hours. The margin above it is deliberately enormous, because the
	// number this threshold has to stay clear of is not a healthy fleet's 100%
	// but a BUSY one's: an agent whose turns run longer than its cadence cannot
	// score high however perfectly it behaves (see the header), and `pa` read 83%
	// on a storm night having acked every token it was handed. 10% is below
	// anything a working-but-slow fleet produces and far above a dead one.
	DefaultBlackoutRate = 0.10
	// DefaultBlackoutMinDeliveries is 24 — three schedules' worth of a 3-hour
	// window at a 10-minute cadence, well under the ~90 a six-agent fleet
	// actually produces. Below it the denominator is too small for a rate to mean
	// anything, which is also what makes a slept-through window ineligible
	// instead of alarming: nothing fires while the host is suspended, so a quiet
	// window is a SMALL window.
	DefaultBlackoutMinDeliveries = 24
	// DefaultBlackoutMinSchedules is 3, the same population floor as
	// DefaultMinPeers+1.
	DefaultBlackoutMinSchedules = 3
	// DefaultBlackoutMinAgents is 3. Three RUNNING agents delivered to and not
	// completing is a fleet; one agent holding three schedules is a wedged agent,
	// which is the peer arm's job and reaches a different recipient.
	DefaultBlackoutMinAgents = 3
	// DefaultRecoveryMinFires is 6 — a third of a 3-hour window at the crew's
	// 10-minute cadence. At the floor it takes 5 completions to retire a finding,
	// which is well outside what a deaf or spinning agent produces, and it is
	// deliberately far below MinFires: MinFires guards a LIFETIME ratio against
	// reading too early, while this guards a decision that can only ever make the
	// detector quieter.
	DefaultRecoveryMinFires = 6
)

// DefaultParams returns the tuned defaults.
func DefaultParams() Params {
	return Params{
		MinFires:              DefaultMinFires,
		MinPeers:              DefaultMinPeers,
		ScaleBand:             DefaultScaleBand,
		MinGap:                DefaultMinGap,
		Floor:                 DefaultFloor,
		SettleAfter:           DefaultSettleAfter,
		BlackoutWindow:        DefaultBlackoutWindow,
		BlackoutRate:          DefaultBlackoutRate,
		BlackoutMinDeliveries: DefaultBlackoutMinDeliveries,
		BlackoutMinSchedules:  DefaultBlackoutMinSchedules,
		BlackoutMinAgents:     DefaultBlackoutMinAgents,
		RecoveryMinFires:      DefaultRecoveryMinFires,
	}
}

// withDefaults fills any unset field so a partially-specified Params (a config
// file that sets one knob) is still usable.
func (p Params) withDefaults() Params {
	d := DefaultParams()
	if p.MinFires <= 0 {
		p.MinFires = d.MinFires
	}
	if p.MinPeers <= 0 {
		p.MinPeers = d.MinPeers
	}
	if p.ScaleBand < 1 {
		p.ScaleBand = d.ScaleBand
	}
	if p.MinGap <= 0 {
		p.MinGap = d.MinGap
	}
	if p.Floor <= 0 {
		p.Floor = d.Floor
	}
	if p.SettleAfter <= 0 {
		p.SettleAfter = d.SettleAfter
	}
	if p.BlackoutWindow <= 0 {
		p.BlackoutWindow = d.BlackoutWindow
	}
	if p.BlackoutRate <= 0 {
		p.BlackoutRate = d.BlackoutRate
	}
	if p.BlackoutMinDeliveries <= 0 {
		p.BlackoutMinDeliveries = d.BlackoutMinDeliveries
	}
	if p.BlackoutMinSchedules <= 0 {
		p.BlackoutMinSchedules = d.BlackoutMinSchedules
	}
	if p.BlackoutMinAgents <= 0 {
		p.BlackoutMinAgents = d.BlackoutMinAgents
	}
	if p.RecoveryMinFires <= 0 {
		p.RecoveryMinFires = d.RecoveryMinFires
	}
	return p
}

// FindingKind distinguishes the two shapes a per-agent deficit takes.
type FindingKind string

const (
	// KindDeficit is a schedule that acks SOMETIMES, far less often than its
	// peers. This is the 2026-07-29 shape.
	KindDeficit FindingKind = "deficit"
	// KindNeverAcked is a schedule with a meaningful number of fires and not one
	// acknowledgement, in a cohort whose peers do ack.
	//
	// This deliberately goes beyond scheduler.Entry.CompletionTracked, which
	// treats a never-acking schedule as UNKNOWN rather than failing — correct in
	// isolation, because an agent may simply not have been told to ack. The
	// cohort is what converts unknown into known: same kind, same cadence,
	// comparable fire count, and the majority of its peers produce the signal.
	// requireAckAwareCohort is the guard that keeps this honest.
	KindNeverAcked FindingKind = "never_acked"
)

// Finding is one schedule judged against its peers.
type Finding struct {
	Kind FindingKind `json:"kind"`
	Sample
	// Rate is the candidate's completion ratio, PeerMedian the median over its
	// comparable peers, and Gap the difference. Carried explicitly so a reader
	// (or a mail body) never has to recompute the judgement.
	Rate       float64 `json:"rate"`
	PeerMedian float64 `json:"peer_median"`
	Gap        float64 `json:"gap"`
	// Peers is how many comparable peers the median was taken over.
	Peers int `json:"peers"`
	// PeerNames lists them, because "compared against whom" is the first
	// question anyone reading this alert will ask.
	PeerNames []string `json:"peer_names"`
}

// Key identifies a finding across samples, for the watcher's fingerprint and
// per-finding escalation clock. Deliberately excludes the rates: a drifting
// ratio must not read as a new finding and re-mail every interval.
func (f Finding) Key() string {
	return string(f.Kind) + "|" + f.Agent + "|" + f.ID
}

// FleetFinding reports a whole cohort that is not completing its fires RIGHT
// NOW. It is separate from the per-agent findings on purpose: everyone at 40%
// on the same cadence is a scheduler or fleet fault (an auth outage, a wedged
// pogod, a broken ack path), and reporting it as N per-agent alerts would name
// N innocent agents and bury the one fact that matters. The per-agent rule
// cannot fire in this regime anyway — a low median leaves no gap to clear.
//
// # It is judged on a WINDOW, and that is the whole of mg-c232
//
// This arm used to trigger on the median of the cohort's LIFETIME ratios
// against Params.Floor, and a lifetime ratio cannot describe now. Two outages
// on 2026-08-10 — the crew stopped 01:56-12:40Z, then a network loss
// 14:24-17:15Z — put roughly 80 dead fires into every 10-minute schedule's
// denominator. The cohort median fell 40% -> 36% -> 26% over a day in which the
// fault had already ENDED, and the finding escalated to the mayor for 61 hours
// without clearing. Cumulative arithmetic guarantees that: no quantity of later
// health can pull a lifetime ratio back over a floor, so the alarm's only exits
// were a counter reset or being ignored. An alarm that cannot clear after the
// fault clears trains its reader to ignore it, which is the same defect as
// mg-0155 (a deploy RED naming the wrong cause) and drellem2/pogo#122 (a false
// "schedules LOST" RED).
//
// So Rate below is the ABSOLUTE completion rate over Params.BlackoutWindow,
// restricted to cohort members whose agent was RUNNING for the whole window —
// the same statistic, gates and threshold as Blackout, applied to one cohort
// instead of the fleet. Reusing those constants rather than inventing a pair is
// deliberate: they were measured (see DefaultBlackoutWindow) on this fleet's
// aggregate fire traffic, and a cohort is a subset of that traffic with the
// same bursty turn-length behaviour. A cohort MEDIAN over the same window was
// rejected — no sweep supports it, and it can read 0 while the aggregate reads
// 15% whenever half a healthy cohort happens to be mid-turn.
//
// LifetimeMedian is retained and rendered, because "26% since registration" is
// genuinely useful context for a reader deciding how long this has been true.
// It is not consulted by the trigger.
//
// # What this arm still adds over Blackout
//
// Blackout aggregates every schedule of every running agent, so a cohort going
// dark while the rest of the fleet works is averaged away. A cohort is judged on
// its own traffic, so "every sweep is failing while mail-checks are fine" is
// visible here and invisible there.
type FleetFinding struct {
	Kind    string `json:"kind"`
	Cadence string `json:"cadence"`
	// Window is the trailing span Rate was measured over, as a string for the
	// same reason Cadence is one.
	Window string `json:"window"`
	// Delivered, Completed and Rate are the WINDOWED measurement over Judged,
	// and Rate is what Threshold is applied to.
	Delivered int     `json:"delivered"`
	Completed int     `json:"completed"`
	Rate      float64 `json:"rate"`
	Threshold float64 `json:"threshold"`
	// Judged names the cohort members the window was measured over — those whose
	// agent was running for the whole of it. A finding is a claim about these,
	// not about Names.
	Judged []string `json:"judged"`
	// LifetimeMedian is the median of the cohort's since-registration ratios.
	// CONTEXT ONLY — see the type comment. Kept under its original `median` JSON
	// name so an existing reader of the event log or `--json` does not silently
	// start reading a zero.
	LifetimeMedian float64 `json:"median"`
	// LastCompletedAt is the newest completion by any judged member inside the
	// window; zero when there was none.
	LastCompletedAt time.Time `json:"last_completed_at,omitempty"`
	Schedules       int       `json:"schedules"`
	Names           []string  `json:"names"`
}

// Key identifies a fleet finding for fingerprinting.
func (f FleetFinding) Key() string { return "fleet|" + f.Kind + "|" + f.Cadence }

// Blackout is the ABSOLUTE arm: fires are being delivered across the fleet and
// nothing is completing them. It is one finding for the whole fleet, not one per
// schedule, because that is what it is a statement about.
//
// It exists because the peer-relative arm cannot see a uniform failure, and the
// cohort arm can only see one through lifetime counters that a re-registration
// zeroes. See the package header for the 2026-08-09 measurement that separates
// the three.
//
// Nothing here is a comparison. Rate is completions over deliveries in a window,
// and the thresholds it is read against are constants — so the finding gets
// STRONGER as the failure becomes more uniform.
type Blackout struct {
	// Window is the measured span, as a string for the same reason
	// FleetFinding.Cadence is (a time.Duration marshals as a nanosecond int, and
	// a MarshalJSON on Recent would be promoted through embedding).
	Window string `json:"window"`
	// Delivered, Completed and Rate are the measurement. Rate is absolute.
	Delivered int     `json:"delivered"`
	Completed int     `json:"completed"`
	Rate      float64 `json:"rate"`
	// Threshold is the rate this was judged against, carried so a reader never
	// has to guess which build's constant applied.
	Threshold float64 `json:"threshold"`
	// Schedules is how many distinct schedules were delivered to in the window.
	Schedules int `json:"schedules"`
	// RunningJudged names every RUNNING agent the window was measured over — the
	// population the rate above is a rate OF.
	RunningJudged []string `json:"running_judged"`
	// StalledAgents is the subset of those that completed NOTHING in the window.
	// During a total outage that is all of them, and it is carried so the notifier
	// can report a recipient that is itself in the list rather than quietly
	// mailing it — the defect this arm was filed for (mg-e2a4).
	StalledAgents []string `json:"stalled_agents"`
	// LastCompletedAt is the newest completion inside the window; zero means not
	// one fire completed in the whole window.
	LastCompletedAt time.Time `json:"last_completed_at,omitempty"`
}

// Key identifies the blackout for fingerprinting. Constant: there is at most one
// blackout, and its numbers drift continuously while the outage lasts. A
// changing key would re-mail on every sample through the ordinary
// changed-fingerprint path, which is not how this arm gets its repetition — see
// Options.BlackoutRenotifyAfter.
func (b Blackout) Key() string { return "blackout" }

// Quiet is how long it has been since anything completed a fire, or the whole
// window when nothing did.
func (b Blackout) Quiet(now time.Time, window time.Duration) time.Duration {
	if b.LastCompletedAt.IsZero() {
		return window
	}
	return now.Sub(b.LastCompletedAt)
}

// Report is one evaluation of a Snapshot.
type Report struct {
	// Suppressed is true when the whole report was declined because the fleet
	// had just been disrupted. Findings are empty in that case — and that is a
	// deliberate silence, not a clean bill of health.
	Suppressed bool `json:"suppressed"`
	// SuppressReason explains the silence in one line.
	SuppressReason string `json:"suppress_reason,omitempty"`

	// Scanned is every sample offered. Eligible is how many survived the
	// eligibility gates.
	Scanned  int `json:"scanned"`
	Eligible int `json:"eligible"`
	// SkippedFresh / SkippedFewFires / SkippedNotRecurring count the exclusions
	// by reason, so a report of "no findings" can be read as "nothing wrong" or
	// "nothing measurable" rather than collapsing the two.
	SkippedFresh        int `json:"skipped_fresh"`
	SkippedFewFires     int `json:"skipped_few_fires"`
	SkippedNotRecurring int `json:"skipped_not_recurring"`
	// SkippedNoPeers counts eligible samples that had no comparable cohort to be
	// judged against. These are UNJUDGED, not healthy.
	SkippedNoPeers int `json:"skipped_no_peers"`
	// RetiredRecovered counts per-schedule findings that the LIFETIME rule raised
	// and the RECENT WINDOW retired: the counters still carry an old outage, the
	// window says the schedule is completing its fires now. Counted rather than
	// discarded silently, because "the alarm went quiet" and "the alarm was
	// talked out of firing" are different facts and only one of them means the
	// fleet recovered (mg-c232).
	RetiredRecovered int `json:"retired_recovered"`

	Deficits []Finding      `json:"deficits"`
	Fleet    []FleetFinding `json:"fleet"`
	// FleetBlind says why the COHORT arm produced no judgement at all — the
	// window was not offered, could not be read, or the running set was missing.
	// Empty means the arm ran.
	//
	// The arm fails CLOSED into this field rather than falling back to the
	// lifetime median, because that median is the trigger mg-c232 removed and a
	// fallback would reinstate it on exactly the days the events log is
	// unreadable. Blind and calm must stay distinguishable, which is what this
	// string is for — same contract as BlackoutBlind.
	FleetBlind string `json:"fleet_blind,omitempty"`
	// FleetNotMeasured names cohorts that were structurally eligible (enough
	// ack-aware members) but carried too little windowed traffic to be judged —
	// a daily-cadence cohort inside a 3-hour window, most obviously. These are
	// UNJUDGED, not healthy.
	FleetNotMeasured []string `json:"fleet_not_measured,omitempty"`
	// Blackout is the absolute arm's finding, nil when the fleet is completing
	// work (or when the arm could not look — see BlackoutBlind).
	Blackout *Blackout `json:"blackout,omitempty"`
	// BlackoutBlind says why the absolute arm produced no judgement at all: the
	// window was not offered, could not be read, or was too small to mean
	// anything. Empty means the arm ran.
	//
	// It is a string rather than a bool because "not measured" has to be
	// distinguishable from "measured and calm" BY A READER, not just by the
	// code. A control that cannot look and a control that looked and found
	// nothing are the same silence otherwise, which is the failure mode this
	// whole package exists to end.
	BlackoutBlind string `json:"blackout_blind,omitempty"`
}

// Actionable reports whether anything in this report warrants a notice.
func (r Report) Actionable() bool {
	return len(r.Deficits) > 0 || len(r.Fleet) > 0 || r.Blackout != nil
}

// Detect evaluates a snapshot. It is pure: same snapshot in, same report out,
// no clock and no filesystem.
func Detect(snap Snapshot, p Params) Report {
	p = p.withDefaults()
	rep := Report{Scanned: len(snap.Samples)}

	if !snap.LastDisruption.IsZero() && snap.Now.Sub(snap.LastDisruption) < p.SettleAfter {
		reason := snap.DisruptionReason
		if reason == "" {
			reason = "disruption"
		}
		rep.Suppressed = true
		rep.SuppressReason = fmt.Sprintf(
			"%s %s ago — counters are not representative until %s has elapsed",
			reason, snap.Now.Sub(snap.LastDisruption).Round(time.Second), p.SettleAfter)
		return rep
	}

	eligible := make([]Sample, 0, len(snap.Samples))
	for _, s := range snap.Samples {
		switch {
		case s.Cadence <= 0:
			rep.SkippedNotRecurring++
		case !s.CreatedAt.IsZero() && snap.Now.Sub(s.CreatedAt) < p.SettleAfter:
			// The counter was reset (registration or re-registration) too
			// recently to describe anything. Same class as the system_wake
			// suppression above, same mechanism.
			rep.SkippedFresh++
		case s.FiresDelivered < p.MinFires:
			rep.SkippedFewFires++
		default:
			eligible = append(eligible, s)
		}
	}
	rep.Eligible = len(eligible)

	// One windowed view, shared by all three arms. Built once because the
	// liveness gate and the window length must be IDENTICAL across them: a
	// cohort judged over a different span from the fleet it belongs to would let
	// the two arms disagree about the same minutes.
	view := newWindowView(snap, p)
	rep.FleetBlind = view.blind

	cohorts := map[string][]Sample{}
	for _, s := range eligible {
		cohorts[s.cohortKey()] = append(cohorts[s.cohortKey()], s)
	}

	// Iterate cohorts in a stable order so the report — and the fingerprint the
	// watcher derives from it — does not depend on map iteration.
	keys := make([]string, 0, len(cohorts))
	for k := range cohorts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		members := cohorts[k]
		sort.Slice(members, func(i, j int) bool {
			if members[i].Agent != members[j].Agent {
				return members[i].Agent < members[j].Agent
			}
			return members[i].ID < members[j].ID
		})

		for _, c := range members {
			peers := comparable(c, members, p.ScaleBand)
			if len(peers) < p.MinPeers {
				rep.SkippedNoPeers++
				continue
			}
			if !ackAwareCohort(peers) {
				// Nobody here acks. The cohort offers no evidence that acking is
				// even expected of this kind of schedule, so a zero means nothing.
				rep.SkippedNoPeers++
				continue
			}
			median := medianRate(peers)
			rate := c.Rate()
			gap := median - rate
			if gap < p.MinGap || rate >= p.Floor {
				continue
			}
			// The RECENCY gate (mg-c232). The two conditions above are lifetime
			// arithmetic, so a schedule that was dark for ten hours this morning
			// stays below its peers long after it recovered — the same
			// cannot-clear defect the cohort arm was rebuilt for, at a smaller
			// amplitude because a big denominator damps it.
			//
			// The window is used as a VETO and never as evidence: it can retire a
			// finding the lifetime rule raised, and there is no path by which it
			// raises one. That asymmetry is what makes it safe to apply a
			// statistic here that would be too noisy to trigger on — a healthy
			// agent in a long turn legitimately reads 0/18 over three hours (see
			// DefaultBlackoutWindow), which would be a false alarm as a trigger
			// and is merely "not retired" as a veto.
			//
			// It FAILS OPEN: an unmeasurable window leaves the lifetime finding
			// standing, matching LastDisruption's stated direction — fail toward
			// alerting, because this package exists because a silence hid a fault
			// for a week. The cost is that the clearing property is only as good
			// as the events log, which is stated rather than hidden.
			if recovered(view, c, p) {
				rep.RetiredRecovered++
				continue
			}
			kind := KindDeficit
			if !c.Tracked() {
				kind = KindNeverAcked
			}
			rep.Deficits = append(rep.Deficits, Finding{
				Kind: kind, Sample: c,
				Rate: rate, PeerMedian: median, Gap: gap,
				Peers: len(peers), PeerNames: names(peers),
			})
		}

		// Fleet-level: the cohort ITSELF is failing. Requires a full cohort
		// (MinPeers peers plus the member they are peers of) so a pair of broken
		// schedules is not announced as a fleet fault, and requires the cohort to
		// be ack-aware for the same reason a per-agent finding does.
		//
		// The ack-aware requirement leaves a stated gap: a cohort that has never
		// acked AT ALL reads as unknown, so an outage on a fleet that was never
		// taught to ack is invisible here. That is the honest price of not
		// accusing a fleet whose prompts simply never mention `ack`.
		//
		// That gap used to be wider than this comment claimed, and the widening
		// was invisible because it shared a trigger with the failure it hid.
		// Tracked read FiresCompleted alone, and re-registration zeroes it. A
		// cohort whose MAJORITY had re-registered without acking since was
		// therefore not ack-aware, and both arms took SkippedNoPeers — while
		// `pogo schedule completion`, named right here as a compensating
		// control, read the same predicate and went blind for the same reason
		// at the same moment. A backstop that shares a trigger with the thing
		// it backs up is redundancy in name only.
		//
		// scheduler.Entry.EverAcked closes it: the bit survives re-registration
		// even though the counters (deliberately, see mg-49b1) do not. Pinned by
		// TestBounceDoesNotBlindBothControlsAtOnce, which asserts the property
		// across BOTH controls from one shared state, because a unit test on the
		// bit alone passes while the correlation stays open.
		//
		// BOUNDS, so this is not re-filed at the size it was first written at.
		// Zeroing requires a BOOT — an agent that dies without booting never
		// re-registers — and survivors carry the cohort, so the blind case needs
		// a zeroed MAJORITY, not merely a bounce
		// (TestBounce_TheBlindCaseNeedsAZEROEDMAJORITY_NotJustABounce). mg-00d6
		// was filed on a fleet-wide operational story that its author withdrew,
		// and the mechanism has zero observed instances: across the 78
		// mail-check fires in events.log for 2026-08-11T03:00-05:59Z, the window
		// holding the nightly bounce, `completion_tracked: false` appears 0
		// times. This is a latent defect, not a post-mortem finding.
		//
		// THE COVER THAT ACTUALLY HELD, and the reason the gap above was priced
		// correctly even while it was mis-stated: detectBlackout below. It reads
		// Snapshot.Recent — a window counted from the EVENTS LOG — and never
		// touches Tracked, the cohorts or the counters, so a re-registration
		// provably cannot reset its input. That is why it emitted through the
		// 2026-08-11 outage while these arms could not (config.go's
		// FirstTurnConfig records 33 consecutive correct alarms), including a
		// resumption at 05:03Z with every counter at zero and nobody acking,
		// which no cohort-gated path can produce. Its independence is now
		// asserted rather than claimed: TestBlackoutArmIsIndependentOfTheBounce.
		// Do not give it a dependency on the counters.
		//
		// The other named cover, internal/synthwatch (mg-8cdb), was read for the
		// first time under mg-00d6 and also holds: its evidence is the harness
		// session transcript, which a scheduler re-registration cannot reset. It
		// stays a PARTIAL cover by design — the auth shape only, and only for
		// harnesses that expose a transcript.
		//
		// The TRIGGER is now the windowed rate, not the lifetime median — see
		// FleetFinding for the 61-hour escalation that bought that change. The
		// structural gates above (cohort size, ack-awareness) survive it: they say
		// whether this population can be judged at all, which no window changes.
		if len(members) >= p.MinPeers+1 && ackAwareCohort(members) {
			f, why := detectCohort(members, view, p)
			switch {
			case f != nil:
				rep.Fleet = append(rep.Fleet, *f)
			case why != "":
				rep.FleetNotMeasured = append(rep.FleetNotMeasured,
					members[0].Kind+"/"+members[0].Cadence.String()+": "+why)
			}
		}
	}

	rep.Blackout, rep.BlackoutBlind = detectBlackout(view, p)

	return rep
}

// windowView is the trailing-window measurement plus the liveness gate, built
// once per Detect and shared by every arm that judges NOW rather than
// SINCE-REGISTRATION.
//
// It exists because three arms need the same two facts — how long the window is,
// and which agents were up for the whole of it — and computing them separately
// would let a cohort be judged over a different span from the fleet it belongs
// to. blind is non-empty when the view could not be built at all; every arm
// treats that as "could not look", never as "looked and found nothing".
type windowView struct {
	recent Recent
	window time.Duration
	// running is the set of agents that were up for the WHOLE window, i.e. those
	// whose numbers describe the window rather than the period before they
	// existed.
	running map[string]bool
	// young counts running agents excluded for being up for less than the window.
	young int
	blind string
}

// newWindowView applies the three availability checks and the liveness gate.
// The checks are the blackout arm's originals, moved here unchanged: they were
// always statements about the SNAPSHOT rather than about that one arm.
func newWindowView(snap Snapshot, p Params) windowView {
	if snap.Recent == nil {
		return windowView{blind: "no window measurement was supplied"}
	}
	r := *snap.Recent
	if r.Err != "" {
		return windowView{blind: "window could not be measured: " + r.Err}
	}
	window := r.Window
	if window <= 0 {
		window = p.BlackoutWindow
	}
	if snap.RunningSince == nil {
		return windowView{recent: r, window: window,
			blind: "the running-agent set was not supplied, and without it an absent " +
				"fleet is indistinguishable from a dead one"}
	}
	if r.ByAgent == nil {
		return windowView{recent: r, window: window,
			blind: "the window carried no per-agent breakdown, so the liveness gate " +
				"cannot be applied"}
	}

	// The liveness gate. Everything measured through this view is restricted to
	// agents that are RUNNING and have been up for the WHOLE window, because
	// "fires delivered, nothing completed" is also what an empty fleet looks like
	// — and on this box that is every night between midnight and 09:30, plus the
	// first window after every spawn. See Snapshot.RunningSince.
	v := windowView{recent: r, window: window, running: map[string]bool{}}
	for name, since := range snap.RunningSince {
		if since.IsZero() || snap.Now.Sub(since) < window {
			v.young++
			continue
		}
		v.running[name] = true
	}
	return v
}

// detectCohort applies the COHORT arm to one already-structurally-eligible
// cohort: the ABSOLUTE completion rate over the window, restricted to members
// whose agent was up for the whole of it.
//
// Same statistic, same gates and same threshold as detectBlackout, over a
// cohort's traffic instead of the fleet's. See FleetFinding for why the lifetime
// median it replaced could not clear, and why reusing the blackout constants is
// the honest choice rather than inventing an unswept pair.
//
// Returns (finding, "") when the cohort is dark, (nil, "") when it is judged and
// healthy, and (nil, reason) when it could not be judged — which is NOT the same
// as healthy and is reported as Report.FleetNotMeasured.
func detectCohort(members []Sample, v windowView, p Params) (*FleetFinding, string) {
	if v.blind != "" {
		// Reported once, globally, as Report.FleetBlind — repeating it per cohort
		// would turn one fact about the snapshot into N findings about cohorts.
		return nil, ""
	}
	var delivered, completed int
	var judged []string
	var last time.Time
	for _, m := range members {
		if !v.running[m.Agent] {
			continue
		}
		f, ok := v.recent.BySchedule[scheduleKey(m.Agent, m.ID)]
		if !ok || f.Delivered == 0 {
			continue
		}
		delivered += f.Delivered
		completed += f.Completed
		judged = append(judged, m.ID)
		if f.LastCompletedAt.After(last) {
			last = f.LastCompletedAt
		}
	}
	sort.Strings(judged)

	kind, cadence := members[0].Kind, members[0].Cadence.String()
	if len(judged) < p.BlackoutMinSchedules {
		return nil, fmt.Sprintf(
			"only %d of %d schedule(s) were both delivered to and owned by an agent up for "+
				"the whole %s (%d running agent(s) too young) — under the %d needed to speak "+
				"for a cohort",
			len(judged), len(members), v.window, v.young, p.BlackoutMinSchedules)
	}
	if delivered < p.BlackoutMinDeliveries {
		return nil, fmt.Sprintf(
			"only %d fire(s) delivered to those schedules in the last %s — under the %d "+
				"needed for a rate to mean anything",
			delivered, v.window, p.BlackoutMinDeliveries)
	}
	rate := float64(completed) / float64(delivered)
	if rate > p.BlackoutRate {
		return nil, ""
	}
	return &FleetFinding{
		Kind:            kind,
		Cadence:         cadence,
		Window:          v.window.String(),
		Delivered:       delivered,
		Completed:       completed,
		Rate:            rate,
		Threshold:       p.BlackoutRate,
		Judged:          judged,
		LifetimeMedian:  medianRate(members),
		LastCompletedAt: last,
		Schedules:       len(members),
		Names:           names(members),
	}, ""
}

// recovered reports whether the recent window shows this ONE schedule completing
// its fires at or above the floor — the single condition that retires a lifetime
// peer finding (mg-c232).
//
// Every path out of here that is not a measured recovery returns false, so an
// absent, unreadable, or too-small window leaves the lifetime finding exactly as
// it was. That is what makes this a veto rather than a second detector: it has
// no branch that can create a finding, so the noise this statistic carries as a
// trigger cannot reach the output.
//
// The liveness gate is deliberately NOT applied. A stopped agent's schedule
// cannot show a window at or above the floor anyway, so consulting it would add
// a dependency without changing an answer.
func recovered(v windowView, s Sample, p Params) bool {
	if v.blind != "" || v.recent.BySchedule == nil {
		return false
	}
	f, ok := v.recent.BySchedule[scheduleKey(s.Agent, s.ID)]
	if !ok || f.Delivered < p.RecoveryMinFires {
		return false
	}
	return float64(f.Completed)/float64(f.Delivered) >= p.Floor
}

// detectBlackout applies the ABSOLUTE arm. It reads Snapshot.Recent and nothing
// else — not the cohorts, not the eligible set, not any median — because every
// one of those is a comparison and a comparison is what cannot see a uniform
// failure.
//
// It deliberately does NOT consult MinFires or the eligibility gates above. A
// blackout is a claim about the last few hours of traffic, and the per-schedule gates
// exist to make a LIFETIME RATIO representative; applying them here would
// reintroduce the exact blind spot the events-log window was chosen to escape
// (three-plus hours of blindness after every nightly redeploy).
//
// It DOES sit behind the disruption suppression, because Detect returns before
// reaching here on that path. That is deliberate and it is the one gate the arm
// keeps: a system_wake replays a fire per schedule and a restart re-registers
// every mail-check, so for one settle window the traffic describes a regime that
// has just ended. The cost is bounded at SettleAfter (30 minutes) of delay on an
// outage that begins inside one, against a false alarm on every wake — and
// unlike MinFires, it does not scale with the cadence.
//
// Returns (finding, "") when the arm judged, (nil, reason) when it could not.
// The availability checks and the liveness gate live in newWindowView, which
// every windowed arm shares.
func detectBlackout(v windowView, p Params) (*Blackout, string) {
	if v.blind != "" {
		return nil, v.blind
	}
	r, window, young := v.recent, v.window, v.young
	delivered, completed, schedules, judged, silent := r.restrictTo(v.running)

	if len(judged) < p.BlackoutMinAgents {
		return nil, fmt.Sprintf(
			"only %d agent(s) were both delivered to and up for the whole %s (%d running "+
				"agent(s) too young to judge) — under the %d needed to speak for a fleet; "+
				"a fleet that is not RUNNING is not a blackout",
			len(judged), window, young, p.BlackoutMinAgents)
	}
	if delivered < p.BlackoutMinDeliveries {
		return nil, fmt.Sprintf(
			"only %d fire(s) delivered to running agents in the last %s — under the %d needed "+
				"for a rate to mean anything",
			delivered, window, p.BlackoutMinDeliveries)
	}
	if schedules < p.BlackoutMinSchedules {
		return nil, fmt.Sprintf(
			"only %d schedule(s) of running agents fired in the last %s — under the %d needed "+
				"to speak for a fleet",
			schedules, window, p.BlackoutMinSchedules)
	}
	rate := float64(completed) / float64(delivered)
	if rate > p.BlackoutRate {
		return nil, ""
	}
	return &Blackout{
		Window:          window.String(),
		Delivered:       delivered,
		Completed:       completed,
		Rate:            rate,
		Threshold:       p.BlackoutRate,
		Schedules:       schedules,
		RunningJudged:   judged,
		StalledAgents:   silent,
		LastCompletedAt: r.LastCompletedAt,
	}, ""
}

// comparable returns the members of cohort, other than c, whose fire count is
// within band× of c's in either direction. See Params.ScaleBand.
func comparable(c Sample, cohort []Sample, band float64) []Sample {
	lo := float64(c.FiresDelivered) / band
	hi := float64(c.FiresDelivered) * band
	out := make([]Sample, 0, len(cohort))
	for _, s := range cohort {
		if s.Agent == c.Agent && s.ID == c.ID {
			continue
		}
		d := float64(s.FiresDelivered)
		if d < lo || d > hi {
			continue
		}
		out = append(out, s)
	}
	return out
}

// ackAwareCohort reports whether a majority of peers have ever acked. It is the
// guard that lets KindNeverAcked exist without accusing a schedule whose whole
// cohort simply was never taught to ack.
func ackAwareCohort(peers []Sample) bool {
	tracked := 0
	for _, s := range peers {
		if s.Tracked() {
			tracked++
		}
	}
	return tracked*2 > len(peers)
}

// medianRate returns the median completion ratio over samples. Median, not
// mean: one dead peer must not drag the comparison point down far enough to
// hide a second dead peer.
func medianRate(samples []Sample) float64 {
	if len(samples) == 0 {
		return 0
	}
	rates := make([]float64, 0, len(samples))
	for _, s := range samples {
		rates = append(rates, s.Rate())
	}
	sort.Float64s(rates)
	n := len(rates)
	if n%2 == 1 {
		return rates[n/2]
	}
	return (rates[n/2-1] + rates[n/2]) / 2
}

func names(samples []Sample) []string {
	out := make([]string, 0, len(samples))
	for _, s := range samples {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}

// pct renders a ratio as a whole-number percentage.
func pct(r float64) string { return fmt.Sprintf("%.0f%%", r*100) }

// Render produces the human-readable report used by `pogo check-acks` and by
// the alert mail body.
func (r Report) Render() string {
	var b strings.Builder

	if r.Suppressed {
		fmt.Fprintf(&b, "ack-watch: SUPPRESSED — %s\n", r.SuppressReason)
		fmt.Fprintf(&b, "%d schedule(s) were not evaluated. This is a deliberate silence, not a clean bill of health.\n", r.Scanned)
		return b.String()
	}

	if !r.Actionable() {
		fmt.Fprintf(&b, "ack-watch: no completion deficit. %d schedule(s) scanned, %d eligible.\n", r.Scanned, r.Eligible)
		r.renderBlackoutCoverage(&b)
		r.renderCoverage(&b)
		return b.String()
	}

	if bo := r.Blackout; bo != nil {
		fmt.Fprintf(&b, "FLEET BLACKOUT: %d fire(s) delivered to RUNNING agents in the last %s "+
			"and %d completed (%s, floor %s).\n",
			bo.Delivered, bo.Window, bo.Completed, pct(bo.Rate), pct(bo.Threshold))
		fmt.Fprintf(&b, "  %d schedule(s) across %d running agent(s): %s\n",
			bo.Schedules, len(bo.RunningJudged), strings.Join(bo.RunningJudged, ", "))
		if len(bo.StalledAgents) > 0 {
			fmt.Fprintf(&b, "  completed NOTHING at all: %s\n", strings.Join(bo.StalledAgents, ", "))
		}
		if bo.LastCompletedAt.IsZero() {
			fmt.Fprintf(&b, "  NOT ONE fire completed in the whole %s window.\n", bo.Window)
		} else {
			fmt.Fprintf(&b, "  last completion anywhere in the fleet: %s\n",
				bo.LastCompletedAt.UTC().Format(time.RFC3339))
		}
		b.WriteString("  This is measured ABSOLUTELY — no peer comparison, no median. A uniform\n")
		b.WriteString("  failure has no outlier, so the per-schedule rule below reads 0 findings\n")
		b.WriteString("  during exactly this event and the deficit table looks calm (mg-e2a4).\n")
		b.WriteString("  Suspect the model API, the network, credentials, or pogod's agent hosting\n")
		b.WriteString("  before suspecting any agent. Every agent will report status=running with\n")
		b.WriteString("  last-activity 'just now', because PTY animation is output.\n\n")
	}

	for _, f := range r.Fleet {
		fmt.Fprintf(&b, "COHORT DARK: %d fire(s) delivered to %s schedules on a %s cadence in the last %s "+
			"and %d completed (%s, floor %s).\n",
			f.Delivered, f.Kind, f.Cadence, f.Window, f.Completed, pct(f.Rate), pct(f.Threshold))
		fmt.Fprintf(&b, "  measured over %d schedule(s) whose agent was up for the whole window: %s\n",
			len(f.Judged), strings.Join(f.Judged, ", "))
		if len(f.Names) > len(f.Judged) {
			fmt.Fprintf(&b, "  cohort has %d schedule(s) in total: %s\n", f.Schedules, strings.Join(f.Names, ", "))
		}
		if f.LastCompletedAt.IsZero() {
			fmt.Fprintf(&b, "  NOT ONE fire completed by this cohort in the whole %s window.\n", f.Window)
		} else {
			fmt.Fprintf(&b, "  last completion in this cohort: %s\n",
				f.LastCompletedAt.UTC().Format(time.RFC3339))
		}
		// The lifetime figure is CONTEXT and is labelled as such in the body, not
		// only in the source. It used to be the trigger, and it could not clear:
		// two ended outages held this finding open for 61 hours (mg-c232).
		fmt.Fprintf(&b, "  for context only, NOT the trigger: median %s since registration.\n",
			pct(f.LifetimeMedian))
		b.WriteString("  A whole cohort at this level is a SCHEDULER or FLEET fault, not N agent faults —\n")
		b.WriteString("  suspect the ack path, an auth outage, or pogod itself before suspecting the agents.\n")
		b.WriteString("  This is measured over the window above, so it CLEARS once the cohort completes\n")
		b.WriteString("  fires again — no counter reset needed, and resetting one would hide it instead.\n\n")
	}

	for _, f := range r.Deficits {
		switch f.Kind {
		case KindNeverAcked:
			fmt.Fprintf(&b, "NEVER ACKED: %s (%s) has completed 0 of %d fires; %d comparable peer(s) median %s.\n",
				f.ID, f.Agent, f.FiresDelivered, f.Peers, pct(f.PeerMedian))
		default:
			fmt.Fprintf(&b, "COMPLETION DEFICIT: %s (%s) at %s (%d/%d); %d comparable peer(s) median %s — %.0f points below.\n",
				f.ID, f.Agent, pct(f.Rate), f.FiresCompleted, f.FiresDelivered, f.Peers, pct(f.PeerMedian), f.Gap*100)
		}
		fmt.Fprintf(&b, "  peers: %s\n", strings.Join(f.PeerNames, ", "))
		if f.UnackedStreak > 0 {
			fmt.Fprintf(&b, "  currently %d fire(s) outstanding unacked\n", f.UnackedStreak)
		}
		if !f.LastCompletion.IsZero() {
			fmt.Fprintf(&b, "  last completion: %s\n", f.LastCompletion.Format(time.RFC3339))
		}
		b.WriteString("\n")
	}

	b.WriteString("The agent's process may look perfectly healthy — output flowing, health=healthy,\n")
	b.WriteString("last-activity 0s. That is what this signal is for: it measures completed WORK,\n")
	b.WriteString("not liveness. A spinning agent emits PTY output forever without accomplishing\n")
	b.WriteString("anything, and no output-based check can see through it (mg-1935).\n\n")
	b.WriteString("A default `pogo nudge` cannot reach a spinning agent either — it waits for 2s of\n")
	b.WriteString("PTY silence that never arrives. Use `pogo nudge <agent> --immediate`.\n\n")
	r.renderBlackoutCoverage(&b)
	r.renderCoverage(&b)
	return b.String()
}

// renderBlackoutCoverage states when the WINDOWED arms did not judge. Reported
// on both the findings and the no-findings path, because "the peer arm found
// nothing and the absolute arm never ran" is the state that produced a calm
// report during a dead fleet, and it must be readable as such.
//
// The cohort arm is reported here too, and for the same reason: since mg-c232 it
// judges on the same window, so it goes blind in the same circumstances and a
// reader who is not told cannot tell that from calm.
func (r Report) renderBlackoutCoverage(b *strings.Builder) {
	if r.BlackoutBlind != "" {
		fmt.Fprintf(b, "Fleet-blackout arm did not judge: %s.\n", r.BlackoutBlind)
	}
	if r.FleetBlind != "" {
		fmt.Fprintf(b, "Cohort arm did not judge: %s.\n", r.FleetBlind)
	}
	for _, why := range r.FleetNotMeasured {
		fmt.Fprintf(b, "Cohort not measured — %s.\n", why)
	}
	if r.RetiredRecovered > 0 {
		fmt.Fprintf(b, "%d schedule(s) below their peers over their LIFETIME are completing fires "+
			"again in the recent window, and are not reported.\n", r.RetiredRecovered)
	}
}

// renderCoverage states what was NOT evaluated. A detector that reports only
// findings lets "nothing measurable" read as "nothing wrong" — the failure mode
// this package exists to end, reproduced one level up.
func (r Report) renderCoverage(b *strings.Builder) {
	skipped := r.SkippedFresh + r.SkippedFewFires + r.SkippedNotRecurring + r.SkippedNoPeers
	if skipped == 0 {
		return
	}
	fmt.Fprintf(b, "Not judged: %d of %d (fresh counter %d, too few fires %d, not recurring %d, no comparable peers %d).\n",
		skipped, r.Scanned, r.SkippedFresh, r.SkippedFewFires, r.SkippedNotRecurring, r.SkippedNoPeers)
}

// MailSubject summarises the report for a notification subject line.
//
// A blackout wins the subject line unconditionally, and it says the fleet-level
// fact rather than a count. The subject is the part that travels — into a
// notifier, into a phone banner, into a skim — so burying "nothing has completed
// a fire in an hour" behind "3 schedules below their peers" would put the
// severity in the part nobody reads.
func (r Report) MailSubject() string {
	if bo := r.Blackout; bo != nil {
		if bo.Completed == 0 {
			return fmt.Sprintf("FLEET BLACKOUT — %d fires delivered in the last %s, NONE completed",
				bo.Delivered, bo.Window)
		}
		return fmt.Sprintf("FLEET BLACKOUT — %d of %d fires completed in the last %s (%s)",
			bo.Completed, bo.Delivered, bo.Window, pct(bo.Rate))
	}
	switch {
	case len(r.Fleet) > 0 && len(r.Deficits) > 0:
		return fmt.Sprintf("%d cohort(s) DARK in the last %s, and %d schedule(s) below their peers",
			len(r.Fleet), r.Fleet[0].Window, len(r.Deficits))
	case len(r.Fleet) > 0:
		// "below the completion floor" was the old lifetime phrasing and it was
		// the part that travelled: it says nothing about WHEN, so a reader could
		// not tell a live outage from one that ended two days ago (mg-c232). The
		// window goes in the subject for that reason.
		f := r.Fleet[0]
		if f.Completed == 0 {
			return fmt.Sprintf("%s cohort DARK — %d fires delivered in the last %s, NONE completed",
				f.Kind, f.Delivered, f.Window)
		}
		return fmt.Sprintf("%s cohort at %s — %d of %d fires completed in the last %s",
			f.Kind, pct(f.Rate), f.Completed, f.Delivered, f.Window)
	case len(r.Deficits) == 1:
		f := r.Deficits[0]
		return fmt.Sprintf("%s is completing %s of its fires (peers %s)", f.ID, pct(f.Rate), pct(f.PeerMedian))
	default:
		return fmt.Sprintf("%d schedules far below their peers' completion rate", len(r.Deficits))
	}
}

// Fingerprint identifies the set of findings, so the watcher can tell a NEW
// finding from a standing one. Built from finding keys only — a rate that
// drifts from 36% to 34% is the same finding, and must not re-mail.
func (r Report) Fingerprint() string {
	keys := make([]string, 0, len(r.Deficits)+len(r.Fleet))
	for _, f := range r.Deficits {
		keys = append(keys, f.Key())
	}
	for _, f := range r.Fleet {
		keys = append(keys, f.Key())
	}
	if r.Blackout != nil {
		keys = append(keys, r.Blackout.Key())
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
