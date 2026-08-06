package config

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultPort = 10000
	DefaultBind = "127.0.0.1"
)

// RunMode represents the operating mode of the pogo daemon.
type RunMode int

const (
	// ModeFull means everything is running: agents, refinery, indexing, HTTP.
	ModeFull RunMode = iota
	// ModeIndexOnly means only project indexing, search, and HTTP are running.
	// Agents and refinery are stopped.
	ModeIndexOnly
)

// String returns the human-readable name of the run mode.
func (m RunMode) String() string {
	switch m {
	case ModeFull:
		return "full"
	case ModeIndexOnly:
		return "index-only"
	default:
		return "unknown"
	}
}

// DefaultProvider is the agent harness provider used when none is configured.
// Keeping this "claude" means existing deployments work with no config change.
// The provider supplies the default agent command template; see
// internal/agent/provider.go and internal/claude/provider.go.
const DefaultProvider = "claude"

// DefaultCoordinator is the coordinator agent's name used when [agents]
// coordinator is not configured. The name is policy, not mechanism: it decides
// the coordinator's agent name (and therefore its mg mailbox and schedule ids)
// and what prompts call the role. Existing installs are unaffected by a change
// to this value: the default-migration guard (see migrate.go) pins their
// historical coordinator name into config.toml, so this flip only sets the
// default for fresh installs. See mg-71ea, mg-ce47.
//
// mg-ce47 flipped this to "ringmaster"; mg-2c17 flipped it back, on the
// instruction of the same person who authorized mg-ce47. It is a naming
// decision and nothing else: the coordinator's prompt filename is a frozen
// constant that does NOT follow this name, so "mayor", "ringmaster", and any
// configured name all resolve to agents/mayor.md (mg-04ce). Restoring "mayor"
// removes the cosmetic mismatch of a coordinator named "ringmaster" reading
// mayor.md. The worker default stays "pogocat" — mg-2c17 reverses the
// coordinator half of mg-ce47 only.
const DefaultCoordinator = "mayor"

// DefaultWorker (the worker role's display-name default, "pogocat") is
// declared in migrate.go alongside the role-default migration table that
// consumes it. The worker seam here — AgentsConfig.Worker, WorkerName(), the
// "worker" config key, and Load() defaulting — references it. See mg-ccec
// (design mg-6a24 §1.4).

// DefaultMaxFilesPerTree is the default per-tree file-count ceiling. A tree
// with more files than this is registered but marked skipped-too-large: it is
// not deep-walked. This bounds index cost (building the search index is
// O(files)) and catches pathological generated-data directories that no
// exclude list anticipated. See mg-d205.
const DefaultMaxFilesPerTree = 25000

// DefaultIndexInterval is how often the timer-driven incremental indexer
// re-walks every registered project. The re-index is incremental — a
// no-change tick costs one Lstat per file — so the interval only bounds how
// long a file change can take to surface in search results. Two minutes is a
// comfortable default given every index consumer is request-driven. See
// docs/design/indexing-strategy.md and mg-5b0d.
const DefaultIndexInterval = 2 * time.Minute

// DefaultGitGCInterval is how often pogod runs the polecat git garbage
// collector (stale `polecat-*` branch + leaked worktree cleanup). Hourly is
// deliberately conservative: the GC is a backstop for the per-exit cleanup,
// not a hot path. See internal/gitgc and mg-30d5.
const DefaultGitGCInterval = time.Hour

// DefaultReaperInterval is how often the tier-1 heartbeat reaper sweeps its
// job list when [reaper] interval is unset.
const DefaultReaperInterval = 60 * time.Second

// DefaultReaperMaxKickstarts caps consecutive kickstarts of one job before the
// reaper gives up and escalates, when [reaper] max_kickstarts is unset.
const DefaultReaperMaxKickstarts = 3

// Stall-watch defaults. The stall watcher is the pogod-side third leg of the
// wedge-response triad (gh drellem2/macguffin #12): it rides pogod's heartbeat
// loop and nudges the mayor when work piles up behaviorally (process healthy
// but items unclaimed / mail unread). Because it runs in pogod's
// guaranteed-independent heartbeat — not in the mayor's own loop — it catches
// the one failure mode an Ocean-side watcher can't: the mayor's loop silently
// dropping its check-work / check-mail steps. See internal/stallwatch and
// docs/design/stall-watch-design.md.
const (
	// DefaultStallWatchAgent is the agent the watcher monitors. Only the
	// coordinator is in scope today (it is the sole behavioral-stall target),
	// but the name is configurable so a deployment can point it elsewhere.
	// When [stall_watch] agent is unset, Load() resolves it to the configured
	// [agents] coordinator, so a renamed coordinator is watched under its
	// configured name without extra config.
	DefaultStallWatchAgent = DefaultCoordinator
	// DefaultUnclaimedItemAgeThreshold is how long an available work item
	// assigned to (or pickup-expected by) the watched agent may sit before the
	// watcher nudges. Mirrors the gh #12 spec's 600s.
	DefaultUnclaimedItemAgeThreshold = 10 * time.Minute
	// DefaultUnreadMailAgeThreshold is how old a message in the watched agent's
	// new/ maildir may get before the watcher nudges. Mirrors gh #12's 600s.
	DefaultUnreadMailAgeThreshold = 10 * time.Minute
	// DefaultMaxUnreadMailCount is the unread-count ceiling above which the
	// watcher nudges regardless of message age. Mirrors gh #12's 5.
	DefaultMaxUnreadMailCount = 5
	// DefaultStallNudgeCooldown is the minimum gap between two nudges for the
	// same threshold category, so a persistent backlog produces one nudge per
	// cooldown rather than one per heartbeat tick. Mirrors gh #12's 300s.
	DefaultStallNudgeCooldown = 5 * time.Minute
	// DefaultHighPriorityWakeDelay is the minimum age a high-priority available
	// item must reach before the priority wake fires. Small enough to feel
	// immediate versus the old up-to-30-min idle-poll gap, large enough to let
	// a burst of enqueues settle so a batch produces one nudge rather than one
	// per item. See the priority-wake half of gh drellem2/pogo #61.
	DefaultHighPriorityWakeDelay = 30 * time.Second
	// DefaultHighPriorityWakeCooldown is the minimum gap between two
	// priority-wake nudges. It is deliberately shorter than the standard stall
	// cooldown (urgent work should recover fast) but long enough that a
	// high-priority item which stays available — e.g. the coordinator can't
	// dispatch it yet — does not re-nudge every heartbeat tick.
	DefaultHighPriorityWakeCooldown = 3 * time.Minute
	// DefaultStallRepeatBackoffCap is the ceiling on the per-item repeat backoff
	// (see stallwatch.repeatCooldown). The FIRST notice about an item is never
	// delayed — this bounds only how far apart REPEAT notices about an item the
	// coordinator has already been told about can grow.
	//
	// Sized against the defect it exists to stop: with the cooldown keyed per
	// category rather than per item, a deliberately-held item re-notified every
	// cooldown forever — mg-61f4 drew 22 notices in a 4h20m window and mg-0e24
	// drew 27 (mg-1693). At a 4h cap the same held item settles to ~1 notice per
	// 4h, which is quiet enough not to train the reader to discount the channel
	// and still loud enough that a genuinely forgotten item resurfaces the same
	// day.
	//
	// Setting this equal to (or below) the category's base cooldown disables
	// escalation and restores a flat per-item cooldown — still a fix for the
	// per-category keying, just without the backoff.
	DefaultStallRepeatBackoffCap = 4 * time.Hour
	// DefaultBlockedReminderCooldown is the BASE of the per-item backoff for the
	// blocked-reminder (mg-3844) — the gap before the SECOND notice to an agent
	// named by a `blocked:<agent>` assignee. The FIRST notice is never delayed,
	// which is the load-bearing half: the failure this fixes is a named agent who
	// never learned a decision was owed at all, and only a first notice fixes
	// that. A cadence merely accelerates it.
	//
	// Deliberately an hour rather than the 5-minute stall cooldown. The recipient
	// of a stall nudge is the coordinator, whose response is a dispatch it can
	// make in seconds; the recipient of a blocked-reminder is an agent being
	// asked to make a DECISION, which is not faster for being asked twice an hour.
	DefaultBlockedReminderCooldown = 1 * time.Hour
	// DefaultBlockedReminderMaxNotices bounds how many notices one blocked item
	// may draw before the reminder goes quiet about it for good.
	//
	// This is the stop condition mayor asked for in mg-3844, and it is a COUNT
	// rather than a longer backoff on purpose. RepeatBackoffCap bounds the RATE;
	// it does not terminate. A hold left in place for a week still draws a notice
	// every cap-interval forever, which is the mg-1693 shape re-created on a new
	// recipient — and worse here, because a blocked agent may be waiting on
	// purpose and has no way to say "I know" other than clearing a block it is
	// not ready to clear.
	//
	// Four notices under a doubling backoff span ~7h (0, +1h, +2h, +4h). That is
	// long past the point where "the agent never knew" is a live explanation, and
	// everything after it is nagging an agent who has already decided to wait.
	DefaultBlockedReminderMaxNotices = 4
	// DefaultDriftCheckInterval is how often pogod's drift-check runner samples
	// the [reconcile] mirrors from the heartbeat OnTick loop (mg-345b). It is
	// deliberately COARSE — far larger than the ~30s heartbeat tick — because the
	// check shells out to `launchctl print` / `ps` per mirror and a genuine
	// deploy drift persists for minutes-to-hours, not seconds. This interval also
	// serves as the mail-rate limiter: a persistent drift re-mails `human` once
	// per interval, never once per tick. NOT a launchd timer — the nondemand-spawn
	// wedge (mg-50e0) means a launchd timer would silently never fire, the exact
	// "inert while appearing correct" failure the detector exists to catch.
	DefaultDriftCheckInterval = 15 * time.Minute

	// DefaultCredExpiryInterval is how often pogod samples the harness
	// credential's refresh-grant expiry (mg-7024). Deliberately COARSE: the
	// event being predicted is up to 30 days away and only ever moves when a
	// human runs `/login`, so sampling faster buys nothing. Being up to one
	// interval late at the tightest lead time is fine — the tiers are lead
	// times, not deadlines. Like drift-watch this rides the heartbeat and NOT a
	// launchd timer, because the nondemand-spawn wedge (mg-50e0) would leave a
	// launchd timer silently never firing.
	DefaultCredExpiryInterval = 15 * time.Minute
	// DefaultCredExpiryBlindRenotify throttles the "the credential exists but I
	// cannot read its expiry" mail. Once a day: often enough that a blind
	// warner is not quietly forgotten, rare enough that a permanently-moved
	// harness schema does not bury the inbox.
	DefaultCredExpiryBlindRenotify = 24 * time.Hour

	// DefaultGHTeardownInterval is how often pogod's gh-issue teardown detector
	// samples. Coarse: each sample costs one GitHub round-trip per done carrier,
	// and a teardown miss that has already lasted hours is not made worse by
	// being found an hour later.
	DefaultGHTeardownInterval = 1 * time.Hour
	// DefaultGHTeardownRenotify is how long an UNCHANGED set of teardown
	// findings stays quiet before being raised again.
	DefaultGHTeardownRenotify = 24 * time.Hour
	// DefaultGHTeardownNotifyTo is the mailbox teardown findings go to (mg-b586).
	// A FLEET mailbox, deliberately not `human`: a teardown miss is a workflow
	// failure the fleet chases, and mailing a human an operational task he can
	// only forward back to the fleet trains him to filter the sender.
	//
	// WHICH fleet mailbox is the coordinator's, not a named PM's (mg-f04b).
	// This default was a literal `pm-pogo` — one deployment's product PM — so a
	// fresh install mailed every teardown finding to an agent that does not
	// exist on that host, and mg's create-on-send maildir meant nothing
	// reported the miss. The coordinator is the one fleet mailbox pogo
	// guarantees, and it is what the three sibling watchers (ackwatch,
	// deafwatch, ghintake) already default to. A deployment with a PM who owns
	// the gh-issue workflow names it in [gh_teardown] notify_to.
	DefaultGHTeardownNotifyTo = DefaultCoordinator
	// DefaultGHTeardownEscalateAfter is how long ONE unresolved teardown finding
	// may persist before `human` is copied as well. A miss the fleet is not
	// clearing is a different fact from the miss itself, and that one IS a
	// human's to know.
	DefaultGHTeardownEscalateAfter = 72 * time.Hour

	// DefaultGHIntakeInterval is how often pogod's gh-issue INTAKE detector
	// reconciles open issues against `gh:` carriers (mg-039b). Much finer than
	// the teardown detector's hour, because the two race different clocks: a
	// teardown miss leaves a work item behind to be found, an intake miss leaves
	// nothing at all, and drellem2/pogo#99 spent ten hours in that state.
	DefaultGHIntakeInterval = 15 * time.Minute
	// DefaultGHIntakeGrace is how long an open issue may exist with no carrier
	// before it counts as a finding. Not zero: the poller mails within ~60s of an
	// issue being filed and the coordinator needs a turn to read it and file the
	// carrier, so alarming inside that window would fire on every new issue.
	DefaultGHIntakeGrace = 30 * time.Minute
	// DefaultGHIntakeRenotify is how long an UNCHANGED set of intake findings
	// stays quiet. A full day, because notification is on TRANSITION into the
	// uncarried state; this is only the backstop for a state nobody cleared, and
	// an issue uncarried for a week must not cost a mail per sample.
	DefaultGHIntakeRenotify = 24 * time.Hour
	// DefaultGHIntakeNotifyTo is the mailbox intake findings go to: the
	// COORDINATOR, not the PM and not `human`. Filing a carrier and dispatching
	// triage are things only the coordinator does, and the failure being detected
	// is a coordinator failure — a dropped `[gh]` mail — so this closes the loop
	// on the agent whose omission created the gap.
	DefaultGHIntakeNotifyTo = "mayor"
	// DefaultGHIntakeEscalateAfter is how long ONE uncarried issue may persist
	// before `human` is copied as well. Far shorter than the teardown detector's
	// 72h: an uncarried issue is a reporter waiting with no acknowledgement and
	// no record anywhere. This is also the answer to "what if the coordinator is
	// down" — the detector does not need it alive to be useful, only to be
	// sufficient.
	DefaultGHIntakeEscalateAfter = 4 * time.Hour

	// DefaultAckWatchInterval is how often pogod's completion-deficit detector
	// samples the scheduler's ack counters (mg-1935). Coarse: the condition is a
	// RATE over hundreds of fires, so it moves by fractions of a point per tick
	// and cannot be missed by sampling half-hourly. Rides the heartbeat, not a
	// launchd timer, for the usual reason (mg-50e0).
	DefaultAckWatchInterval = 30 * time.Minute
	// DefaultAckWatchRenotify is how long an UNCHANGED set of completion
	// findings stays quiet before being raised again.
	DefaultAckWatchRenotify = 6 * time.Hour
	// DefaultAckWatchNotifyTo is the mailbox completion findings go to. The
	// mayor: the remedy is `pogo nudge <agent> --immediate` or a doctor restart,
	// which is coordination work rather than a human-only decision.
	DefaultAckWatchNotifyTo = "mayor"
	// DefaultAckWatchEscalateAfter is how long ONE finding may persist before
	// `human` is copied as well. Shorter than the gh-teardown equivalent because
	// the coordinator is itself a crew agent and can have the exact defect being
	// reported (mg-d385) — an alert routed only to the patient reaches nobody.
	DefaultAckWatchEscalateAfter = 24 * time.Hour

	// DefaultDeafWatchInterval is how often pogod's missing-mail-loop announcer
	// samples the registry (mg-032b). Finer than the ack-watch cadence because
	// the condition is a BOOLEAN state rather than a rate: there is no averaging
	// to wait for, and the hold-down — not the sampling interval — is what keeps
	// it quiet. Rides the heartbeat, not a launchd timer (mg-50e0).
	DefaultDeafWatchInterval = 5 * time.Minute
	// DefaultDeafWatchHoldDown is how long an agent must be observed with no
	// mail-check schedule, unbroken, before it is announced. Spawn and schedule
	// registration are not simultaneous, and a redeploy re-runs that gap for the
	// whole fleet; without a hold-down every restart would announce everyone.
	// Same mechanism, same reason, as mg-4904's hold-down on usage-limit hits.
	DefaultDeafWatchHoldDown = 15 * time.Minute
	// DefaultDeafWatchRenotify is how long an UNCHANGED roster of unreachable
	// agents stays quiet before being raised again.
	DefaultDeafWatchRenotify = 6 * time.Hour
	// DefaultDeafWatchNotifyTo is the mailbox announcements go to. The mayor:
	// re-registering a mail-check, and deciding whether the agent also needs a
	// restart, is coordination work.
	DefaultDeafWatchNotifyTo = "mayor"
	// DefaultDeafWatchEscalateAfter is how long a finding may persist before
	// `human` is copied as well. Note that this detector ALSO escalates
	// immediately — regardless of this value — when the roster names NotifyTo
	// itself: mailing an agent that has no mail loop about its own missing mail
	// loop is not a weaker alert, it is no alert at all.
	DefaultDeafWatchEscalateAfter = 24 * time.Hour

	// DefaultWedgeWatchInterval is how often pogod's wedged-agent detector
	// samples every agent's PTY and uptime (mg-fc8d). Same cadence as
	// deaf-watch and for the same reason: the hold-downs, not the sampling
	// rate, are what keep it quiet. Rides the heartbeat, not a launchd timer
	// (mg-50e0) — which for a wedge detector would be a particularly complete
	// joke to get wrong.
	DefaultWedgeWatchInterval = 5 * time.Minute
	// DefaultWedgeWatchMarkerHoldDown is how long a KNOWN dead-end marker must
	// sit beside a stalled agent before it is reported. It is not zero because
	// an agent WRITING about the wedge — a ticket, a changelog, the detector
	// itself — puts every enumerated string into its own PTY. mg-4421 hit the
	// same wall with the rating dialog and solved it with an idle gate.
	DefaultWedgeWatchMarkerHoldDown = 10 * time.Minute
	// DefaultWedgeWatchFreezeHoldDown is how long the agent's own work counter
	// must hold ONE unchanged value before the un-enumerated case is reported.
	// Sized to span at least two mail-check fires at the fleet's 10-minute
	// cadence: a merely-idle agent still runs a turn on every fire, which moves
	// the counter, so surviving several fires unchanged means the fires are
	// being absorbed without running anything.
	DefaultWedgeWatchFreezeHoldDown = 30 * time.Minute
	// DefaultWedgeWatchMinUptime keeps young agents out of the report. Spawn is
	// the noisiest part of an agent's life.
	DefaultWedgeWatchMinUptime = time.Hour
	// DefaultWedgeWatchRatio is mg-fc8d's "orders of magnitude below" test as a
	// number: uptime must be at least this many times the frozen declared
	// counter. The two live signatures were 13h44m beside "3m 2s" (280x) and 7h
	// beside "2m 56s" (143x).
	DefaultWedgeWatchRatio = 20.0
	// DefaultWedgeWatchCoincidenceWindow is how long a connectivity failure
	// keeps a later 401 explained as ONE signature rather than two.
	//
	// The choice is deliberately asymmetric. Too SHORT and a refresh that
	// failed inside an outage is reported as a poisoned credential, which pages
	// a human for a re-login that fixes nothing — the exact error the doctor's
	// correction to mg-fc8d exists to prevent. Too LONG and a genuine
	// revocation is reported as an outage artifact, whose handling is "wait for
	// the network", which fails visibly and immediately. Prefer long.
	DefaultWedgeWatchCoincidenceWindow = 2 * time.Hour
	// DefaultWedgeWatchRenotify is how long an UNCHANGED roster of wedged
	// agents stays quiet before the finding is emitted again.
	DefaultWedgeWatchRenotify = 6 * time.Hour

	// DefaultDoneReapIdleGrace is how long a polecat whose work item has reached
	// a terminal state must be quiet on its PTY before pogod stops it (mg-56d1).
	// See cmd/pogod/donereap.go for why the condition is item-done AND idle
	// rather than item-done alone, and why the grace is measured from the last
	// PTY write rather than from the `done` transition.
	DefaultDoneReapIdleGrace = 2 * time.Minute
)

// DefaultFastPriorities is the set of WorkItem.Priority values that trigger the
// priority wake. Just "high" today; extend it (e.g. add "critical") if the
// priority vocabulary grows. Kept as a var because a slice cannot be a const;
// treat it as read-only.
var DefaultFastPriorities = []string{"high"}

// DefaultNonDispatchableAssignees is the set of WorkItem.Assignee values that
// mean "the coordinator must NOT dispatch this" — an execution gate rather
// than a statement of ownership. Two gates, and they gate for DIFFERENT
// reasons:
//
//   - "human" — a person must do this by hand. internal/agent/prompts/mayor.md
//     files manual-QA items with `--assignee=human` precisely so they are never
//     handed to a worker, and events.ResolveAgent already reserves "human" as
//     the no-agent identity.
//   - "parked" — deliberately set aside; nobody is expected to act on it now.
//     No owner is asserted at all.
//
// "parked" exists because until mg-a3a2 "human" was the ONLY value that
// silenced the dispatch detectors, so it accumulated three incompatible senses
// in one queue: gated-on-Daniel, parked-do-not-chase, and filed-here-for-lack-
// of-an-alternative. That is not a discipline failure — two agents who both
// understood the problem misfiled items within one session, because a gate with
// one expressible value leaves no way to say anything else. Every consumer that
// reads `assignee` to decide what to escalate (stall-watch, PM digests, mayor,
// architect) then re-derived the conflation independently and could not see the
// error from the field: architect reported the queue to Daniel as "entirely
// gated on you" when most of it was parked fleet-internal work.
//
// A CONVENTION about how to use "human" could not have fixed that, because a
// convention cannot be read back out of the data. A distinct sentinel can:
// `mg list --assignee=parked` is now an answerable question, and "human" means
// only "Daniel must decide" again — the property that makes that queue worth
// reading.
//
// Note what "parked" does NOT do. It buys silence from the nudge channel, not
// disappearance from listings (the `gh-open:` precedent, mg-6e57): a parked item
// still appears in `mg list` with its assignee and age visible. And the
// suppression, like every gate here, is unconditional and permanent — a gated
// item never ages back into the alert channel whatever sentinel it carries.
// Aging the gated queue belongs to the PM sweep, which reads it anyway and can
// flag "gated N days" with no code change. Recorded so the gap has a home
// rather than being assumed closed.
//
// This is deliberately a DENYLIST of gates, not an allowlist of dispatchable
// agents. An allowlist would have to enumerate the agent roster (mayor, every
// pm-*, every future crew name) and would silently stop watching work the day a
// new agent is added — which is exactly the defect mg-4bd4 fixed. The gate
// vocabulary, by contrast, is closed: it only grows if someone invents a second
// meaning for "do not execute this automatically", and then it grows by a
// config line rather than a code change.
//
// Kept as a var because a slice cannot be a const; treat it as read-only.
//
// This list is not the whole gate: the gate is this list PLUS the `blocked:`
// shape — see BlockedAssigneePrefix below.
var DefaultNonDispatchableAssignees = []string{"human", "parked"}

// BlockedAssigneePrefix introduces the one SHAPE the gate recognises alongside
// the sentinel vocabulary above: `blocked:<agent>` gates dispatch AND names who
// the item is waiting on.
//
// Why a shape and not a third sentinel (mg-6fb0). The vocabulary above answers
// "do not execute this automatically" with two stated reasons. It could not
// express a third one that three agents reached for within days of mg-a3a2
// shipping — BLOCKED ON A NAMED AGENT. A filer with that intent had to choose:
//
//	assignee=<agent>   keeps WHO, loses the GATE   (mg-bb43, mg-779b, mg-bf5e)
//	assignee=parked    keeps the GATE, loses WHO
//
// That is the surviving case of the third sense the comment above named and
// removed only one instance of: filed-here-for-lack-of-an-alternative. It is not
// a prediction — someone already invented a channel to say what the field could
// not, and `blocked-on-daniel` / `blocked-on-daniel-confirm` tags exist in the
// store (mg-cf48, mg-e925, mg-a96c), with `mg archive` taught to respect them
// (mg-3c53). The intent was being expressed; the gate just could not hear it.
//
// This satisfies the growth condition the comment above states — "it only grows
// if someone invents a second meaning for 'do not execute this automatically'" —
// and *blocked on a named agent* is exactly such a meaning. Crucially it grows by
// ONE SHAPE, not by a roster: `blocked:mayor`, `blocked:pm-pogo` and
// `blocked:some-agent-hired-next-year` all gate with no config line and no code
// change, so mg-4bd4 (an allowlist that stops watching work the day an agent is
// added) cannot recur through this door.
//
// Two properties worth stating because they are choices, not consequences:
//
//   - The shape is INDEPENDENT of the configured vocabulary. Replacing
//     non_dispatchable_assignees does not turn it off, because it is a structural
//     rule about how the field is written rather than a value in a denylist. A
//     deployment that drops "parked" still gates `blocked:mayor`.
//   - `blocked:` with nothing after it still gates. The author wrote the word
//     "blocked"; refusing to gate on a typo'd or truncated agent name would fail
//     in the unsafe direction. BlockedOn reports it as a blocked shape with an
//     empty agent so a caller that wants to complain about it can.
//
// This is ADDITIVE and nothing was migrated (mg-6fb0's sequencing hazard,
// measured before the change: zero of the 8 then-`human` items carried a
// `blocked-on-*` tag, so anything that stopped reading "human" would have
// stranded all eight as dispatchable). "human" and "parked" read exactly as they
// did before.
const BlockedAssigneePrefix = "blocked:"

// BlockedOn reports whether assignee is written in the `blocked:<agent>` shape,
// and returns the agent it names. The prefix match is case-insensitive and
// whitespace-trimmed on both sides of the colon, mirroring IsDispatchGated, so a
// value hand-edited to `Blocked: mayor` reads the same as `blocked:mayor` — the
// frontmatter parser splits on the FIRST colon, so the whole of
// `assignee: blocked:mayor` arrives here as one value. The agent name
// is returned as written rather than lowercased, because its only consumers are
// messages and `mg list --assignee=` queries.
//
// A bare `blocked:` returns ("", true) — a blocked shape naming nobody. That is
// deliberate: see BlockedAssigneePrefix for why it still gates.
func BlockedOn(assignee string) (string, bool) {
	a := strings.TrimSpace(assignee)
	if len(a) < len(BlockedAssigneePrefix) ||
		!strings.EqualFold(a[:len(BlockedAssigneePrefix)], BlockedAssigneePrefix) {
		return "", false
	}
	return strings.TrimSpace(a[len(BlockedAssigneePrefix):]), true
}

// IsDispatchGated reports whether assignee names a non-dispatchable executor —
// whether an item carrying it is gated away from AUTOMATIC dispatch. gates is
// the configured vocabulary; empty falls back to
// DefaultNonDispatchableAssignees, so the zero value is the safe default rather
// than "nothing is gated".
//
// Matching is case-insensitive and whitespace-trimmed, so a "Human" or
// " human " frontmatter value still gates — mirroring the priority vocabulary.
// Unassigned ("") is NOT gated: an item nobody owns is the ordinary
// dispatchable case, and it is the one the fleet runs on.
//
// This lives here, beside the vocabulary it tests, because it has two callers
// that must never disagree:
//
//   - internal/stallwatch decides what to WATCH with it (watchedForDispatch).
//   - internal/agent decides what to DISPATCH with it (handleSpawnPolecat).
//
// Those two answering differently is precisely the defect mg-4798 named: one
// rule enforced in Go on the path that watches, and described only in prose on
// the path that dispatches. A rule belongs in the executable path, and a rule
// with two implementations has already begun to drift. One function, both
// callers, no second copy — so a change to the gate vocabulary cannot leave the
// dispatcher honouring the old one.
//
// Two rules, not one list (mg-6fb0). An assignee gates if it is a sentinel in
// the vocabulary OR is written in the `blocked:<agent>` shape. The shape is
// checked first and does not consult gates at all — see BlockedAssigneePrefix
// for why a shape rather than a third magic value, and why a replaced vocabulary
// does not switch it off.
func IsDispatchGated(assignee string, gates []string) bool {
	if _, ok := BlockedOn(assignee); ok {
		return true
	}
	a := strings.ToLower(strings.TrimSpace(assignee))
	if a == "" {
		return false
	}
	if len(gates) == 0 {
		gates = DefaultNonDispatchableAssignees
	}
	for _, g := range gates {
		if a == strings.ToLower(strings.TrimSpace(g)) {
			return true
		}
	}
	return false
}

// Config holds pogo daemon configuration.
type Config struct {
	Port            int
	Bind            string
	MaxFilesPerTree int
	// IndexInterval is how often the timer-driven incremental indexer
	// re-walks every registered project. Zero falls back to
	// DefaultIndexInterval. See docs/design/indexing-strategy.md.
	IndexInterval time.Duration
	// IndexRoots, when non-empty, restricts auto-registration to git repos
	// under one of these paths (opt-in strict mode). Empty means the default
	// zero-config behavior: any visited git repo may be auto-registered,
	// bounded by MaxFilesPerTree and the default-exclude patterns.
	IndexRoots []string
	Refinery   RefineryConfig
	Agents     AgentsConfig
	Heartbeat  HeartbeatConfig
	GitGC      GitGCConfig
	StallWatch StallWatchConfig
	Reaper     ReaperConfig
	Reconcile  ReconcileConfig
	DriftWatch DriftWatchConfig
	CredExpiry CredExpiryConfig
	GHTeardown GHTeardownConfig
	GHIntake   GHIntakeConfig
	AckWatch   AckWatchConfig
	DeafWatch  DeafWatchConfig
	WedgeWatch WedgeWatchConfig
	DoneReap   DoneReapConfig
	// DispatchPairing declares repos whose items owe a paired work item before
	// dispatch. Zero value = no repos = inert. See dispatchpairing.go.
	DispatchPairing DispatchPairingConfig
	// DispatchCap bounds concurrent workers PER REPOSITORY and reserves part of
	// that budget for the refinery. Unlike DispatchPairing this ships ARMED —
	// see dispatchcap.go for why a per-repo bound is platform behaviour and a
	// per-fleet count is not.
	DispatchCap DispatchCapConfig
	// AuditSuccessor declares repos whose merged audits must be answered by a
	// successor inside a window. Zero value = no repos = inert. This is a
	// DETECTOR and never refuses anything — see auditsuccessor.go.
	AuditSuccessor AuditSuccessorConfig
	// Source is the path of the highest-precedence config file Load read, or
	// "" when no config file was found and everything is defaults + env. pogod
	// uses this to gate crew auto-start: a daemon with no config file is
	// treated as an unconfigured/isolated instance and must not spawn agents
	// (mg-3dc3). When two layers exist, the values in the Config come from
	// both — see Sources.
	Source string
	// Sources lists every config file Load actually read, lowest precedence
	// first (~/.config/pogo/config.toml, then $POGO_HOME/config.toml). Empty
	// when no config file was found. Source is the last entry.
	Sources []string
}

// StallWatchConfig configures pogod's passive stall watcher, which rides the
// heartbeat loop and nudges the watched agent (the mayor) when work piles up.
// See internal/stallwatch and docs/design/stall-watch-design.md.
//
// Note on shape: gh drellem2/macguffin #12 sketched this as a nested JSON
// stall_watch.agents.mayor.* block. pogo's config is flat single-line TOML
// (parsed by loadConfigFile), and the mayor is the only behavioral-stall
// target, so this is implemented as a single flat [stall_watch] section with a
// configurable `agent` key rather than a per-agent map. The thresholds carry
// the same meaning as the spec's *_seconds fields, expressed as Go durations.
type StallWatchConfig struct {
	// Enabled turns the watcher on. Defaults to true.
	Enabled bool
	// Agent is the macguffin agent name to watch. Empty falls back to
	// DefaultStallWatchAgent ("mayor").
	Agent string
	// UnclaimedItemAgeThreshold is how long an available work item assigned to
	// (or unassigned and pickup-expected by) Agent may sit before a nudge.
	// Zero falls back to DefaultUnclaimedItemAgeThreshold.
	UnclaimedItemAgeThreshold time.Duration
	// UnreadMailAgeThreshold is how old a message in Agent's new/ maildir may
	// get before a nudge. Zero falls back to DefaultUnreadMailAgeThreshold.
	UnreadMailAgeThreshold time.Duration
	// MaxUnreadMailCount is the unread-count ceiling above which a nudge fires
	// regardless of age. Zero falls back to DefaultMaxUnreadMailCount.
	MaxUnreadMailCount int
	// NudgeCooldown is the minimum gap between two nudges for the same
	// threshold category. Zero falls back to DefaultStallNudgeCooldown.
	//
	// For the two work-item categories this is the BASE of a per-item backoff,
	// not a flat per-category gate: it is the gap before the SECOND notice about
	// a given item, and each further repeat about that same item doubles up to
	// RepeatBackoffCap. A different item is never gated by it. See
	// stallwatch.repeatCooldown.
	NudgeCooldown time.Duration
	// RepeatBackoffCap bounds the per-item repeat backoff for both work-item
	// categories. Zero falls back to DefaultStallRepeatBackoffCap (4h). Setting
	// it at or below the category's base cooldown yields a flat per-item
	// cooldown with no escalation.
	RepeatBackoffCap time.Duration

	// PriorityWakeEnabled turns on the priority-aware fast wake (gh
	// drellem2/pogo #61): a ready, watched, high-priority available item
	// bypasses UnclaimedItemAgeThreshold and is delivered promptly via the same
	// wait-idle nudge, so urgent work no longer waits out the idle-coordinator
	// polling gap. Because New() cannot distinguish an unset bool from an
	// explicit false, the production default (true) is applied by Load(), not
	// New(); a hand-built config must set this field to activate the wake.
	PriorityWakeEnabled bool
	// HighPriorityWakeDelay is the minimum age a high-priority available item
	// must reach before the priority wake fires (bypassing
	// UnclaimedItemAgeThreshold). Zero falls back to
	// DefaultHighPriorityWakeDelay.
	HighPriorityWakeDelay time.Duration
	// HighPriorityWakeCooldown is the minimum gap between two priority-wake
	// nudges — a dedicated cooldown so a high-priority item that stays available
	// does not re-nudge every tick. Zero falls back to
	// DefaultHighPriorityWakeCooldown.
	HighPriorityWakeCooldown time.Duration
	// FastPriorities lists the WorkItem.Priority values that trigger the
	// priority wake. Empty falls back to DefaultFastPriorities (["high"]).
	FastPriorities []string
	// NonDispatchableAssignees lists the WorkItem.Assignee values that mark an
	// item as gated to a non-dispatchable executor, so neither work-item
	// detector watches it. Every other assignee — unassigned, the coordinator,
	// or any owning agent such as pm-<name> — IS watched. Empty falls back to
	// DefaultNonDispatchableAssignees (["human", "parked"]).
	NonDispatchableAssignees []string

	// BlockedReminderEnabled turns on the blocked-reminder (mg-3844): an item
	// whose assignee is written `blocked:<agent>` reminds THAT AGENT that a
	// decision is owed. It is a different signal to a different recipient from
	// the two dispatch nudges above, and it is the only check here whose
	// population is the GATED items rather than the watched ones.
	//
	// It applies to `blocked:<agent>` and to nothing else. `parked` and `human`
	// stay silent, because their silence is the point — see
	// stallwatch.checkBlockedReminders.
	//
	// Because New() cannot distinguish an unset bool from an explicit false, the
	// production default (true) is applied by Load(), not New(); a hand-built
	// config must set this field to activate the reminder.
	BlockedReminderEnabled bool
	// BlockedReminderCooldown is the base of the blocked-reminder's per-item
	// backoff. Zero falls back to DefaultBlockedReminderCooldown.
	BlockedReminderCooldown time.Duration
	// BlockedReminderMaxNotices caps how many notices one blocked item may draw
	// before the reminder goes quiet about it permanently. Zero falls back to
	// DefaultBlockedReminderMaxNotices; a negative value means no cap.
	BlockedReminderMaxNotices int
}

// GitGCConfig configures pogod's periodic polecat git garbage collector.
// It deletes stale `polecat-*` branches and reclaims leaked worktrees once
// their work items have concluded. See internal/gitgc.
type GitGCConfig struct {
	// Enabled turns on the startup sweep and the periodic ticker.
	// Defaults to true.
	Enabled bool
	// Interval between periodic sweeps. Zero falls back to
	// DefaultGitGCInterval.
	Interval time.Duration
	// Repos lists git repositories to sweep. pogod also sweeps the source
	// repo of every registered agent, so this is mainly needed so the
	// startup sweep can reach a repo after a pogod crash that left no live
	// agents behind.
	Repos []string
}

// HeartbeatConfig configures pogod's clock-jump detector. Zero values fall
// back to internal/heartbeat defaults (30s tick, 60s jump threshold).
type HeartbeatConfig struct {
	Interval      time.Duration
	JumpThreshold time.Duration
}

// ReaperConfig configures pogod's tier-1 heartbeat reaper, which kickstarts
// declared launchd jobs whose heartbeat state file has gone stale. Liveness is
// heartbeat freshness, never process existence. See internal/reaper and
// docs/design/reaper-design.md.
type ReaperConfig struct {
	// Enabled turns the reaper loop on. Defaults to true; with no Jobs it is a
	// logged no-op.
	Enabled bool
	// Interval between sweeps. Zero falls back to DefaultReaperInterval.
	Interval time.Duration
	// MaxKickstarts caps consecutive kickstarts of one job before the reaper
	// gives up and escalates. Zero falls back to DefaultReaperMaxKickstarts.
	MaxKickstarts int
	// Jobs is the declared job list. Each entry is a single line of the form
	//   "<launchd-label>|<heartbeat-path>|<period>"
	// e.g. "com.pogo.watchdog|~/.pogo/health/watchdog.heartbeat|5m". A leading
	// ~ in the path is expanded to the user's home directory. period is a Go
	// duration. Malformed entries are dropped (and reported) at load.
	Jobs []ReaperJob
}

// ReaperJob is one parsed [reaper] jobs entry.
type ReaperJob struct {
	Label     string
	Heartbeat string
	Period    time.Duration
}

// ReconcileConfig declares the host-side artifacts that `pogo service
// reconcile` and `pogo service check-drift` manage (mg-be0c). Each mirror is a
// COPY of a generator/repo source — never a symlink into a checkout — so the
// repo/host boundary is preserved and drift is detectable. See
// internal/reconcile.
type ReconcileConfig struct {
	// Mirrors is the declared mirror list. Each entry is a single line of the
	// form
	//   "<name>|<source>|<target>[|<launchd-label>]"
	// e.g. "watchdog|~/dev/pogo-reminders/bin/watchdog.sh|~/.pogo/pogo-reminders/bin/watchdog.sh|com.pogo.watchdog".
	// A leading ~ in either path is expanded to the user's home directory. The
	// label is optional: omit it for a file that is not a running launchd job.
	// Malformed entries are dropped (and reported) at load.
	Mirrors []ReconcileMirror
}

// ReconcileMirror is one parsed [reconcile] mirrors entry.
type ReconcileMirror struct {
	Name   string
	Source string
	Target string
	Label  string
}

// DriftWatchConfig configures pogod's drift-check RUNNER (mg-345b): the
// heartbeat-driven backstop that periodically runs the check-drift detector
// (internal/reconcile.CheckDrift) over the [reconcile] mirrors and mails
// `human` when a host artifact has drifted from its repo source.
//
// It is the DETECTION half of mg-75f9's ruling — the backstop for the four
// paths the refinery `[deploy]` PREVENTION misses (a probeAlreadyMerged
// early-return that skips deploy, a silently-failed deploy_command, a service
// that dies after a good deploy, and any un-enrolled repo). It is REPORT-ONLY:
// it never reconciles. Auto-fixing drift from the detector is a reconcile loop
// fighting a genuinely-broken artifact — the unbounded-reaper failure shape the
// reconcile package's own doc warns against.
//
// The runner rides pogod's heartbeat OnTick, NOT a launchd timer: the
// nondemand-spawn wedge on this box (mg-50e0) means a launchd timer would
// silently never fire, the exact failure the detector exists to catch.
type DriftWatchConfig struct {
	// Enabled turns the runner on. Defaults to true; with no [reconcile]
	// mirrors declared it is a no-op (there is nothing to watch).
	Enabled bool
	// Interval is the COARSE gap between drift samples. Zero falls back to
	// DefaultDriftCheckInterval. It must be far larger than the heartbeat tick;
	// the throttle enforces that the runner does not sample every ~30s tick.
	Interval time.Duration
}

// CredExpiryConfig configures pogod's credential-expiry WARNER (mg-7024): the
// heartbeat-driven check that reads `refreshTokenExpiresAt` from the harness
// credential and mails `human` on an escalating schedule as the fleet-wide auth
// expiry approaches.
//
// It is the PREDICTION half of the pair mg-ed45 ruled. The OAuth refresh grant
// has a fixed 30-day life that use does not extend, so the next fleet-wide auth
// outage has a knowable date sitting on local disk. Both prior outages (2026-06
// and 2026-07) went unnoticed until the fleet had already been dead about a
// day; warning beforehand costs nothing and the remedy — a human running
// `/login` — takes seconds.
//
// It does NOT replace mg-8cdb's reactive detector and must not be read as
// doing so: prediction covers the scheduled lapse of ONE periodic fault. Early
// revocation, and the genuinely chronic rate/weekly/spend limits, are
// detectable only after the fact.
//
// REPORT-ONLY, and necessarily so: only a human can run `/login`, so this
// warner has no seam through which it could act even if it wanted to.
type CredExpiryConfig struct {
	// Enabled turns the warner on. Defaults to true. It self-disarms (loudly,
	// in the log) on any host with no readable credential, so leaving it on is
	// safe for sandboxes and non-macOS boxes.
	Enabled bool
	// Interval is the COARSE gap between credential samples. Zero falls back to
	// DefaultCredExpiryInterval.
	Interval time.Duration
	// BlindRenotify throttles the unreadable-credential mail. Zero falls back to
	// DefaultCredExpiryBlindRenotify.
	BlindRenotify time.Duration
}

// GHTeardownConfig configures pogod's gh-issue TEARDOWN detector (mg-6e57):
// the heartbeat-driven runner that checks whether the GitHub issue behind every
// `status=done` gh-issue carrier is actually closed.
//
// It exists because that last workflow step can silently not run. mg-07ba
// reached `done, stage: merge` with all its work genuinely finished, but nobody
// closed drellem2/pogo#89 and it sat open for four days. A carrier that
// completed its teardown and one that skipped it are the same three characters
// from the outside, so the miss emits nothing at all.
//
// REPORT-ONLY: it mails NotifyTo and never closes or comments. Closing an
// external issue is outward-facing and stays human-gated.
type GHTeardownConfig struct {
	// Enabled turns the runner on. Defaults to true; it is additionally armed
	// only when the `gh` CLI is available, since without it every lookup is
	// indeterminate and the runner would report an environment gap as findings.
	Enabled bool
	// Interval is the COARSE gap between samples. Zero falls back to
	// DefaultGHTeardownInterval.
	Interval time.Duration
	// RenotifyAfter is how long an unchanged set of findings stays quiet before
	// being mailed again. Zero falls back to DefaultGHTeardownRenotify.
	//
	// It is deliberately neither zero nor infinite: re-mailing every interval
	// trains a human to filter the sender, but going permanently quiet after one
	// notice is how #89 stayed open for four days.
	RenotifyAfter time.Duration
	// NotifyTo is the mailbox findings are reported to. Empty falls back to
	// DefaultGHTeardownNotifyTo (`pm-pogo`).
	//
	// The recipient obeys the same logic as the cadence above. A teardown miss
	// says "our gh-issue workflow's last step did not run" — a fleet workflow
	// failure, not a decision needing a human. Routing it to `human` would also
	// open a third, unbatched mail channel alongside the urgent-items and
	// daily-digest contract the digest exists to enforce.
	NotifyTo string
	// EscalateAfter is how long ONE finding may persist unbroken before the
	// notice also goes to `human`. Zero falls back to
	// DefaultGHTeardownEscalateAfter; a NEGATIVE value disables escalation.
	EscalateAfter time.Duration
}

// GHIntakeConfig configures pogod's gh-issue INTAKE detector (mg-039b): the
// heartbeat-driven runner that reconciles the OPEN issues on the watched repos
// against the `gh:` carrier markers in the work-item store, and reports the
// issues nothing is tracking.
//
// It exists because a delivered `[gh]` mail can be dropped with nothing noticing.
// drellem2/pogo#99 was filed 2026-07-29 18:53:58Z; the poller mailed the
// coordinator 46 seconds later and again 20 minutes after that, both delivered,
// and no carrier existed for ~10 hours. Its paired issue #100 was processed
// normally, so a pair filed to be considered together was split and the untracked
// half was invisible to every board the fleet reads. It surfaced only because a PM
// ran an open-issue sweep by hand, early, on a hunch.
//
// The shipped coordinator prompt already prescribes the discipline that would have
// prevented it (act-then-mark, end-of-turn unread check). Prescribing it was
// evidently not sufficient — there was no detector, only an instruction. The set
// difference between open issues and carried refs is trivially computable and
// nothing computed it.
//
// Sibling of GHTeardownConfig at the opposite end of the same workflow: that one
// catches a carrier that finished while its issue stayed open, this one catches an
// issue that never got a carrier. The intake end is the more dangerous, because a
// teardown miss at least leaves a work item behind.
//
// REPORT-ONLY: it mails NotifyTo and never files a work item or comments on an
// issue. What an issue IS (triage, duplicate, out of scope) is a judgement and it
// stays with the coordinator.
type GHIntakeConfig struct {
	// Enabled turns the runner on. Defaults to true; it is additionally armed
	// only when the `gh` CLI is available, since without it every repo lookup
	// fails and the runner would report an environment gap as a wall of
	// unreadable repos.
	Enabled bool
	// Interval is the COARSE gap between samples. Zero falls back to
	// DefaultGHIntakeInterval.
	Interval time.Duration
	// Grace is how long an open issue may exist with no carrier before it counts.
	// Zero falls back to DefaultGHIntakeGrace; a NEGATIVE value disables the
	// window so every uncarried issue counts immediately.
	Grace time.Duration
	// RenotifyAfter is how long an unchanged set of findings stays quiet before
	// being mailed again. Zero falls back to DefaultGHIntakeRenotify.
	RenotifyAfter time.Duration
	// NotifyTo is the mailbox findings are reported to. Empty falls back to
	// DefaultGHIntakeNotifyTo (`mayor`) — the agent that can actually file the
	// carrier. Routing this to `human` would land it in a maildir carrying ~990
	// unread messages, where it could only be forwarded back.
	NotifyTo string
	// EscalateAfter is how long ONE uncarried issue may persist unbroken before
	// the notice also goes to `human`. Zero falls back to
	// DefaultGHIntakeEscalateAfter; a NEGATIVE value disables escalation.
	EscalateAfter time.Duration
	// Repos is the explicit watch list, as `owner/name` strings. Empty means
	// "discover it", which reads the issue poller's own state directory
	// (`$POGO_HOME/gh-issues/seen-<owner>-<repo>.json`) so the two halves of the
	// reconciliation cannot drift: a repo added to the poller is covered on the
	// next sample with no second edit to forget. With neither, a built-in default
	// applies. See internal/ghintake.ResolveRepos.
	Repos []string
}

// AckWatchConfig configures pogod's scheduler-completion DEFICIT detector
// (mg-1935): the heartbeat-driven runner that reads the ack counters the
// scheduler has kept since mg-a754 and alerts when one schedule is completing
// far fewer of its fires than its directly comparable peers.
//
// It exists because that signal already existed and nothing consumed it. On
// 2026-07-29 a crew agent had been completing 36% of its mail-check fires for
// its entire run — 270/757 against ~751/757 for three peers on the identical
// cadence — and the only path to noticing was a human reading `pogo schedule
// list` and comparing rows. Every liveness instrument said healthy, because a
// spinning agent emits PTY output forever without accomplishing anything.
//
// The detector's tuning knobs live in internal/ackwatch (see its Params); this
// config carries only the runner's cadence and routing, matching how the other
// heartbeat detectors are configured.
//
// REPORT-ONLY: it mails NotifyTo and never nudges, restarts, or unregisters
// anything.
type AckWatchConfig struct {
	// Enabled turns the runner on. Defaults to true. It is inert on any daemon
	// with too few comparable schedules to form a cohort, so leaving it on is
	// safe for sandboxes and single-agent hosts.
	Enabled bool
	// Interval is the COARSE gap between samples. Zero falls back to
	// DefaultAckWatchInterval.
	Interval time.Duration
	// RenotifyAfter is how long an unchanged finding set stays quiet before
	// being mailed again. Zero falls back to DefaultAckWatchRenotify.
	RenotifyAfter time.Duration
	// NotifyTo is the mailbox findings are reported to. Empty falls back to
	// DefaultAckWatchNotifyTo (`mayor`).
	NotifyTo string
	// EscalateAfter is how long ONE finding may persist unbroken before the
	// notice also goes to `human`. Zero falls back to
	// DefaultAckWatchEscalateAfter; a NEGATIVE value disables escalation.
	EscalateAfter time.Duration
}

// DeafWatchConfig configures pogod's missing-mail-loop ANNOUNCER (mg-032b): the
// heartbeat-driven runner that applies `pogo agent diagnose`'s own mail-loop
// judgement to the whole registry and mails when an agent has no way to be
// woken.
//
// It exists because that judgement already existed and only one thing consumed
// it. mg-de08 taught diagnose to report `health=no_mail_loop`; mg-738f widened
// it to the deaf survivor — an auto_start=false agent that is running with its
// mail loop dead underneath it — and said in its own closing section that the
// fault was thereby DETECTABLE but never ANNOUNCED, because the only reader was
// `pogo agent diagnose <name>`: a subcommand that takes as an argument the name
// the operator does not know to type. An agent with no mail loop is unreachable
// by every coordination path the fleet has, while looking perfectly healthy.
//
// The detector's mechanics live in internal/deafwatch; this config carries the
// runner's cadence, hold-down and routing, matching how the other heartbeat
// detectors are configured.
//
// REPORT-ONLY: it mails NotifyTo and never registers a schedule, nudges, or
// restarts. Re-registering the loop on the agent's behalf would hide WHY it
// vanished, which is the part worth knowing.
type DeafWatchConfig struct {
	// Enabled turns the runner on. Defaults to true. It is inert on a daemon
	// with no mail-check provider (scheduler disabled) — that case reports as
	// an error rather than as a clean fleet — so leaving it on is safe.
	Enabled bool
	// Interval is the gap between samples. Zero falls back to
	// DefaultDeafWatchInterval.
	Interval time.Duration
	// HoldDown is how long a missing mail loop must persist, unbroken, before
	// it is announced. Zero falls back to DefaultDeafWatchHoldDown. A NEGATIVE
	// value disables the hold-down entirely, which only tests should do:
	// without it, every restart announces the whole fleet in the gap between
	// spawn and schedule registration.
	HoldDown time.Duration
	// RenotifyAfter is how long an unchanged roster stays quiet before being
	// mailed again. Zero falls back to DefaultDeafWatchRenotify.
	RenotifyAfter time.Duration
	// NotifyTo is the mailbox announcements are sent to. Empty falls back to
	// DefaultDeafWatchNotifyTo (`mayor`).
	NotifyTo string
	// EscalateAfter is how long a finding may persist unbroken before the
	// notice also goes to `human`. Zero falls back to
	// DefaultDeafWatchEscalateAfter; a NEGATIVE value disables the AGE-based
	// escalation. It does NOT disable the immediate escalation when the roster
	// names NotifyTo itself — that one is not a matter of patience.
	EscalateAfter time.Duration
}

// WedgeWatchConfig configures pogod's wedged-agent DETECTOR (mg-fc8d): the
// heartbeat-driven runner that reads every agent's PTY for known dead-end
// states and cross-checks each agent's own declared work counter against its
// process uptime.
//
// It exists because on 2026-08-04 twelve polecats and the doctor crew agent sat
// at a login prompt for thirteen hours, and again for seven on 2026-08-05, with
// every liveness instrument pogo has reading healthy throughout. The agents were
// ANIMATING: Claude Code redraws a spinner while parked at a prompt, so
// `last-activity` — which tracks PTY writes — read "just now" for the whole
// window, the process was alive so status read running, and CPU was near zero,
// which is also what a legitimately blocked agent looks like.
//
// The mechanics live in internal/wedgewatch, including why the counter/uptime
// cross-check gates on the counter being FROZEN rather than on the raw ratio (a
// ratio-only rule fires on every healthy agent at the start of every turn), and
// why a 401 shortly after a connectivity failure is treated as ONE signature
// rather than as a revoked credential.
//
// REPORT-ONLY, and more strictly than its siblings: there is no NotifyTo and no
// EscalateTo here, because mg-fc8d's item (3) — escalating a fleet-level wedge
// OUTSIDE the wedged party — is an alerting-policy decision reserved to Daniel
// and unruled at the time of writing. The runner emits events and exposes its
// findings; it holds no seam through which it could pick a recipient, nudge,
// dismiss, stop or re-dispatch anything.
type WedgeWatchConfig struct {
	// Enabled turns the runner on. Defaults to true; it is inert on a daemon
	// with no agent registry, and reports that case as an error rather than as
	// a clean fleet, so leaving it on is safe.
	Enabled bool
	// Interval is the gap between samples. Zero falls back to
	// DefaultWedgeWatchInterval.
	Interval time.Duration
	// MarkerHoldDown is how long a known dead-end marker must sit beside a
	// stalled agent before it is reported. Zero falls back to
	// DefaultWedgeWatchMarkerHoldDown. A NEGATIVE value removes the hold-down,
	// which only tests should do: without it, any agent writing about the wedge
	// reports itself.
	MarkerHoldDown time.Duration
	// FreezeHoldDown is how long the declared work counter must hold one
	// unchanged value before the un-enumerated case is reported. Zero falls
	// back to DefaultWedgeWatchFreezeHoldDown; NEGATIVE removes it, which only
	// tests should do — without it every healthy agent reports at the start of
	// every turn.
	FreezeHoldDown time.Duration
	// MinUptime is the process-age floor below which no cross-check finding is
	// made. Zero falls back to DefaultWedgeWatchMinUptime; NEGATIVE removes it.
	MinUptime time.Duration
	// Ratio is how many times the frozen declared counter uptime must exceed.
	// Zero or negative falls back to DefaultWedgeWatchRatio.
	Ratio float64
	// CoincidenceWindow is how long a connectivity failure keeps a later 401
	// explained as the same event. Zero falls back to
	// DefaultWedgeWatchCoincidenceWindow. See that constant for why the
	// trade-off is deliberately asymmetric.
	CoincidenceWindow time.Duration
	// RenotifyAfter is how long an unchanged roster stays quiet before the
	// finding is emitted again. Zero falls back to DefaultWedgeWatchRenotify.
	RenotifyAfter time.Duration
}

// DoneReapConfig configures pogod's done-item polecat reaper (mg-56d1): the
// heartbeat-driven runner that stops a polecat whose work item has reached a
// terminal state and which has gone quiet, regardless of whether that
// completion came from a merge or from the polecat's own `mg done`.
//
// It exists because every automatic teardown in the daemon was keyed on MERGE,
// and triage / audit-only / investigation polecats produce no merge: they called
// `mg done` and then held a concurrency slot until a coordinator noticed. The
// mechanics live in cmd/pogod/donereap.go, which also documents why the
// condition is item-done AND idle rather than item-done alone.
//
// ACTING, not report-only — the one detector here that is. It stops a process
// whose work is provably concluded; it cannot mark an item, mail, nudge, or
// spawn.
type DoneReapConfig struct {
	// Enabled turns the reaper on. Defaults to true.
	Enabled bool
	// IdleGrace is how long a done polecat must be quiet on its PTY before it is
	// stopped. Zero falls back to DefaultDoneReapIdleGrace. A NEGATIVE value
	// removes the grace entirely, which only a test should ask for: without it a
	// polecat is stopped the instant its item goes done, mid-mail if that is
	// where it happens to be.
	IdleGrace time.Duration
}

// AgentsConfig holds agent command configuration.
type AgentsConfig struct {
	// Provider selects the agent harness ("claude", "codex", "pi", "cursor"). Resolved
	// by cmd/pogod to an agent.Provider. Empty is treated as DefaultProvider;
	// Load() fills it in.
	Provider string
	// Coordinator is the coordinator agent's name ([agents] coordinator).
	// Empty is treated as DefaultCoordinator ("mayor"); Load() fills it in.
	// Prefer CoordinatorName() over reading the field so zero-value configs
	// (tests, callers that skip Load) still resolve to the default.
	Coordinator string
	// Worker is the worker role's display name ([agents] worker). Empty is
	// treated as DefaultWorker ("pogocat"); Load() fills it in. Prefer
	// WorkerName() over reading the field so zero-value configs still resolve
	// to the default. Display-only — it never renames an identifier.
	Worker string
	// SME is the mailbox of a product subject-matter expert the gh-issue
	// triage workflow consults before a recommendation is finalized ([agents]
	// sme). It is a MAIL TARGET, so it must name an agent that exists.
	//
	// EMPTY IS THE SHIPPED DEFAULT AND IT MEANS "no SME" — the consult step is
	// omitted from the triage prompt entirely rather than addressed to a
	// guessed name. That emptiness is the point (mg-f04b): the default was
	// once a literal `pm-pogo`, which exists on exactly one machine, and a
	// fresh install's triage worker would have mailed a mailbox that does not
	// exist and then held for two hours waiting for a reply that could never
	// come. mg mail creates an unread-by-nobody maildir for an unknown name
	// rather than failing, so nothing would have reported the miss.
	//
	// A deployment that has such a PM names it here and gets the consult back.
	SME string
	// AutoStart globally gates crew auto-start at pogod boot ([agents]
	// autostart). Defaults to true. Setting it false keeps a *configured*
	// daemon from spawning any crew agents, regardless of per-prompt
	// auto_start frontmatter — the switch for sandboxes and tests that need
	// a config file (e.g. for an [agents] command override) but no fleet
	// (mg-9a1c). Complements the mg-3dc3 gate, which only covers daemons
	// with no config file at all. POGO_AGENT_AUTOSTART overrides. Note: the
	// zero value is false — read this via a Load()ed Config, not a
	// hand-built AgentsConfig.
	AutoStart bool
	// Command is the default command template for all agent types. When empty,
	// the active provider's CommandTemplate is used instead.
	// Supports Go template variables: {{.PromptFile}}, {{.AgentName}}, {{.AgentType}}, {{.WorkDir}}
	Command string
	// ExtraPath lists directories to prepend to pogod's PATH — and therefore
	// to every spawned child's PATH — beyond the automatic repair in
	// internal/pathenv. Use it for harness runtimes in locations the daemon
	// cannot discover on its own (e.g. a nonstandard Node install for pi; see
	// gh #25). Set via [agents] extra_path or POGO_EXTRA_PATH
	// (list-separator-joined, i.e. colon-separated on unix).
	ExtraPath []string
	// Crew overrides the command template for crew agents.
	Crew AgentTypeConfig
	// Polecat overrides the command template for polecat agents.
	Polecat AgentTypeConfig
}

// AgentTypeConfig holds per-agent-type spawn configuration.
type AgentTypeConfig struct {
	// Command overrides the command template for this agent type. Empty means
	// inherit the global [agents] command (or the provider default).
	Command string
	// Provider overrides the harness provider ("claude", "codex", "pi", "cursor") for
	// this agent type. Empty means inherit the global [agents] provider. This
	// is what lets a mixed fleet run — e.g. [agents.polecat] provider = "pi"
	// while crew agents stay on Claude. See mg-b31b.
	Provider string
}

// AgentCommand returns the explicitly-configured command template for a given
// agent type, or "" when none is set. An empty result is the signal for the
// caller (agent.Registry) to fall back to the active provider's default
// CommandTemplate. Precedence: per-type override > global [agents] command
// (which POGO_AGENT_COMMAND also feeds via Load).
func (c *AgentsConfig) AgentCommand(agentType string) string {
	switch agentType {
	case "crew":
		if c.Crew.Command != "" {
			return c.Crew.Command
		}
	case "polecat":
		if c.Polecat.Command != "" {
			return c.Polecat.Command
		}
	}
	return c.Command
}

// CoordinatorName returns the configured coordinator agent name, falling back
// to DefaultCoordinator ("mayor") when unset. Safe on a zero-value AgentsConfig.
func (c *AgentsConfig) CoordinatorName() string {
	if c != nil && c.Coordinator != "" {
		return c.Coordinator
	}
	return DefaultCoordinator
}

// WorkerName returns the configured worker display name, falling back to
// DefaultWorker ("pogocat") when unset. Safe on a zero-value AgentsConfig.
func (c *AgentsConfig) WorkerName() string {
	if c != nil && c.Worker != "" {
		return c.Worker
	}
	return DefaultWorker
}

// SMEName returns the configured product-SME mailbox, or "" when no SME is
// configured. Unlike CoordinatorName and WorkerName there is no fallback name:
// the empty string is a meaningful value that switches the consult off. Safe on
// a zero-value AgentsConfig.
func (c *AgentsConfig) SMEName() string {
	if c == nil {
		return ""
	}
	return c.SME
}

// AgentProvider returns the configured harness provider id for a given agent
// type. Precedence: per-type [agents.<type>] provider > global [agents]
// provider. The global value is non-empty after Load() (it defaults to
// DefaultProvider), so a "crew" or "polecat" argument always yields a usable
// id. Mirrors AgentCommand; see mg-b31b for the mixed-fleet rationale.
func (c *AgentsConfig) AgentProvider(agentType string) string {
	switch agentType {
	case "crew":
		if c.Crew.Provider != "" {
			return c.Crew.Provider
		}
	case "polecat":
		if c.Polecat.Provider != "" {
			return c.Polecat.Provider
		}
	}
	return c.Provider
}

// RefineryConfig holds merge queue configuration.
type RefineryConfig struct {
	Enabled      bool
	PollInterval time.Duration
	// MaxConcurrentMerges bounds how many merge requests the refinery runs at
	// once. Merges are partitioned by repo, so two merges for the SAME repo
	// are never concurrent whatever this is; this caps how many different
	// repos may merge at the same time. Zero means the refinery's own default.
	// One restores the historic single-slot behaviour.
	MaxConcurrentMerges int
}

// parsedConfig is the intermediate result of reading the config layers.
// It tracks which fields were explicitly set so Load() can distinguish
// "unset" from "set to a zero value" (e.g. enabled = false).
//
// One parsedConfig is filled by every layer in turn (lowest precedence first),
// which is what makes the merge key-by-key: parseConfigFileInto only assigns a
// field when its key appears on a line, so a higher layer overrides exactly the
// keys it names and leaves the rest of the lower layer's values in place.
type parsedConfig struct {
	Config
	refineryEnabledSet     bool
	gitgcEnabledSet        bool
	stallWatchEnabledSet   bool
	priorityWakeEnabledSet bool
	// blockedReminderEnabledSet mirrors priorityWakeEnabledSet: without it an
	// explicit `blocked_reminder_enabled = false` is indistinguishable from the
	// key being absent, and the layer merge would restore the default `true`.
	blockedReminderEnabledSet bool
	agentsAutoStartSet        bool
	reaperEnabledSet          bool
	driftWatchEnabledSet      bool
	credExpiryEnabledSet      bool
	ghTeardownEnabledSet      bool
	ghIntakeEnabledSet        bool
	ackWatchEnabledSet        bool
	deafWatchEnabledSet       bool
	wedgeWatchEnabledSet      bool
	doneReapEnabledSet        bool
	// dispatchCapMaxSet / dispatchCapReserveSet exist because ZERO is a
	// meaningful value for both keys and not merely an absent one:
	// max_polecats_per_repo = 0 disarms the cap, refinery_reserve = 0 drops the
	// reservation. Merging on `> 0` would silently restore the shipped defaults
	// and leave an operator who deliberately disarmed the gate looking at a
	// daemon that still refuses. Same shape as blockedReminderEnabledSet.
	dispatchCapMaxSet     bool
	dispatchCapReserveSet bool
	// sources are the files that were read, lowest precedence first.
	sources []string
}

// Load reads configuration from (in priority order):
//  1. Environment variables (POGO_PORT, POGO_AGENT_COMMAND, …)
//  2. $POGO_HOME/config.toml, key by key
//  3. ~/.config/pogo/config.toml, key by key
//  4. Compiled-in defaults
//
// The two config files LAYER: a key set in $POGO_HOME/config.toml overrides the
// same key in ~/.config/pogo/config.toml, and every key it does not set keeps
// the ~/.config value. See loadConfigFiles for why whole-file precedence was a
// footgun (mg-cf9e).
func Load() *Config {
	cfg := &Config{
		Port:            DefaultPort,
		Bind:            DefaultBind,
		MaxFilesPerTree: DefaultMaxFilesPerTree,
		IndexInterval:   DefaultIndexInterval,
		Agents: AgentsConfig{
			AutoStart: true,
		},
		Refinery: RefineryConfig{
			Enabled:      true,
			PollInterval: 30 * time.Second,
			// Left at zero so the refinery package owns the number; see
			// refinery.DefaultMaxConcurrentMerges for why it is what it is.
			MaxConcurrentMerges: 0,
		},
		GitGC: GitGCConfig{
			Enabled:  true,
			Interval: DefaultGitGCInterval,
		},
		// Ships ARMED (mg-3977). See dispatchcap.go for why a per-repo bound is
		// platform behaviour rather than one deployment's policy.
		DispatchCap: DefaultDispatchCapConfig(),
		Reaper: ReaperConfig{
			Enabled:       true,
			Interval:      DefaultReaperInterval,
			MaxKickstarts: DefaultReaperMaxKickstarts,
		},
		StallWatch: StallWatchConfig{
			Enabled: true,
			// Agent is resolved at the end of Load: explicit [stall_watch]
			// agent wins, otherwise it follows the [agents] coordinator.
			UnclaimedItemAgeThreshold: DefaultUnclaimedItemAgeThreshold,
			UnreadMailAgeThreshold:    DefaultUnreadMailAgeThreshold,
			MaxUnreadMailCount:        DefaultMaxUnreadMailCount,
			NudgeCooldown:             DefaultStallNudgeCooldown,
			RepeatBackoffCap:          DefaultStallRepeatBackoffCap,
			// Priority wake is default-on for the watched coordinator (gh #61).
			PriorityWakeEnabled:      true,
			HighPriorityWakeDelay:    DefaultHighPriorityWakeDelay,
			HighPriorityWakeCooldown: DefaultHighPriorityWakeCooldown,

			BlockedReminderEnabled:    true,
			BlockedReminderCooldown:   DefaultBlockedReminderCooldown,
			BlockedReminderMaxNotices: DefaultBlockedReminderMaxNotices,
			FastPriorities:            DefaultFastPriorities,
			NonDispatchableAssignees:  DefaultNonDispatchableAssignees,
		},
		DriftWatch: DriftWatchConfig{
			Enabled:  true,
			Interval: DefaultDriftCheckInterval,
		},
		CredExpiry: CredExpiryConfig{
			Enabled:       true,
			Interval:      DefaultCredExpiryInterval,
			BlindRenotify: DefaultCredExpiryBlindRenotify,
		},
		GHTeardown: GHTeardownConfig{
			Enabled:       true,
			Interval:      DefaultGHTeardownInterval,
			RenotifyAfter: DefaultGHTeardownRenotify,
			NotifyTo:      DefaultGHTeardownNotifyTo,
			EscalateAfter: DefaultGHTeardownEscalateAfter,
		},
		GHIntake: GHIntakeConfig{
			Enabled:       true,
			Interval:      DefaultGHIntakeInterval,
			Grace:         DefaultGHIntakeGrace,
			RenotifyAfter: DefaultGHIntakeRenotify,
			NotifyTo:      DefaultGHIntakeNotifyTo,
			EscalateAfter: DefaultGHIntakeEscalateAfter,
		},
		AckWatch: AckWatchConfig{
			Enabled:       true,
			Interval:      DefaultAckWatchInterval,
			RenotifyAfter: DefaultAckWatchRenotify,
			NotifyTo:      DefaultAckWatchNotifyTo,
			EscalateAfter: DefaultAckWatchEscalateAfter,
		},
		DeafWatch: DeafWatchConfig{
			Enabled:       true,
			Interval:      DefaultDeafWatchInterval,
			HoldDown:      DefaultDeafWatchHoldDown,
			RenotifyAfter: DefaultDeafWatchRenotify,
			NotifyTo:      DefaultDeafWatchNotifyTo,
			EscalateAfter: DefaultDeafWatchEscalateAfter,
		},
		WedgeWatch: WedgeWatchConfig{
			Enabled:           true,
			Interval:          DefaultWedgeWatchInterval,
			MarkerHoldDown:    DefaultWedgeWatchMarkerHoldDown,
			FreezeHoldDown:    DefaultWedgeWatchFreezeHoldDown,
			MinUptime:         DefaultWedgeWatchMinUptime,
			Ratio:             DefaultWedgeWatchRatio,
			CoincidenceWindow: DefaultWedgeWatchCoincidenceWindow,
			RenotifyAfter:     DefaultWedgeWatchRenotify,
		},
		DoneReap: DoneReapConfig{
			Enabled:   true,
			IdleGrace: DefaultDoneReapIdleGrace,
		},
	}

	// Try config files first (lowest priority, overridden by env)
	if fileCfg, err := loadConfigFiles(); err == nil {
		cfg.Sources = fileCfg.sources
		cfg.Source = fileCfg.sources[len(fileCfg.sources)-1]
		if fileCfg.Port != 0 {
			cfg.Port = fileCfg.Port
		}
		if fileCfg.Bind != "" {
			cfg.Bind = fileCfg.Bind
		}
		if fileCfg.MaxFilesPerTree > 0 {
			cfg.MaxFilesPerTree = fileCfg.MaxFilesPerTree
		}
		if fileCfg.IndexInterval > 0 {
			cfg.IndexInterval = fileCfg.IndexInterval
		}
		if len(fileCfg.IndexRoots) > 0 {
			cfg.IndexRoots = fileCfg.IndexRoots
		}
		cfg.Agents = fileCfg.Agents
		if !fileCfg.agentsAutoStartSet {
			// The wholesale Agents copy above clobbers the default; restore
			// it unless the file set [agents] autostart explicitly.
			cfg.Agents.AutoStart = true
		}
		if fileCfg.refineryEnabledSet {
			cfg.Refinery.Enabled = fileCfg.Refinery.Enabled
		}
		if fileCfg.Refinery.PollInterval > 0 {
			cfg.Refinery.PollInterval = fileCfg.Refinery.PollInterval
		}
		if fileCfg.Refinery.MaxConcurrentMerges > 0 {
			cfg.Refinery.MaxConcurrentMerges = fileCfg.Refinery.MaxConcurrentMerges
		}
		if fileCfg.Heartbeat.Interval > 0 {
			cfg.Heartbeat.Interval = fileCfg.Heartbeat.Interval
		}
		if fileCfg.Heartbeat.JumpThreshold > 0 {
			cfg.Heartbeat.JumpThreshold = fileCfg.Heartbeat.JumpThreshold
		}
		if fileCfg.gitgcEnabledSet {
			cfg.GitGC.Enabled = fileCfg.GitGC.Enabled
		}
		if fileCfg.GitGC.Interval > 0 {
			cfg.GitGC.Interval = fileCfg.GitGC.Interval
		}
		if len(fileCfg.GitGC.Repos) > 0 {
			cfg.GitGC.Repos = fileCfg.GitGC.Repos
		}
		if fileCfg.reaperEnabledSet {
			cfg.Reaper.Enabled = fileCfg.Reaper.Enabled
		}
		if fileCfg.Reaper.Interval > 0 {
			cfg.Reaper.Interval = fileCfg.Reaper.Interval
		}
		if fileCfg.Reaper.MaxKickstarts > 0 {
			cfg.Reaper.MaxKickstarts = fileCfg.Reaper.MaxKickstarts
		}
		if len(fileCfg.Reaper.Jobs) > 0 {
			cfg.Reaper.Jobs = fileCfg.Reaper.Jobs
		}
		if len(fileCfg.Reconcile.Mirrors) > 0 {
			cfg.Reconcile.Mirrors = fileCfg.Reconcile.Mirrors
		}
		if fileCfg.driftWatchEnabledSet {
			cfg.DriftWatch.Enabled = fileCfg.DriftWatch.Enabled
		}
		if fileCfg.DriftWatch.Interval > 0 {
			cfg.DriftWatch.Interval = fileCfg.DriftWatch.Interval
		}
		if fileCfg.credExpiryEnabledSet {
			cfg.CredExpiry.Enabled = fileCfg.CredExpiry.Enabled
		}
		if fileCfg.CredExpiry.Interval > 0 {
			cfg.CredExpiry.Interval = fileCfg.CredExpiry.Interval
		}
		if fileCfg.CredExpiry.BlindRenotify > 0 {
			cfg.CredExpiry.BlindRenotify = fileCfg.CredExpiry.BlindRenotify
		}
		if fileCfg.ghTeardownEnabledSet {
			cfg.GHTeardown.Enabled = fileCfg.GHTeardown.Enabled
		}
		if fileCfg.GHTeardown.Interval > 0 {
			cfg.GHTeardown.Interval = fileCfg.GHTeardown.Interval
		}
		if fileCfg.GHTeardown.RenotifyAfter > 0 {
			cfg.GHTeardown.RenotifyAfter = fileCfg.GHTeardown.RenotifyAfter
		}
		if fileCfg.GHTeardown.NotifyTo != "" {
			cfg.GHTeardown.NotifyTo = fileCfg.GHTeardown.NotifyTo
		}
		// Non-zero, not >0: a negative value is the documented way to turn
		// escalation off, so it must survive the merge like any other override.
		if fileCfg.GHTeardown.EscalateAfter != 0 {
			cfg.GHTeardown.EscalateAfter = fileCfg.GHTeardown.EscalateAfter
		}
		if fileCfg.ghIntakeEnabledSet {
			cfg.GHIntake.Enabled = fileCfg.GHIntake.Enabled
		}
		if fileCfg.GHIntake.Interval > 0 {
			cfg.GHIntake.Interval = fileCfg.GHIntake.Interval
		}
		// Non-zero, not >0: a negative grace is the documented way to alarm on
		// every uncarried issue immediately, so it must survive the merge.
		if fileCfg.GHIntake.Grace != 0 {
			cfg.GHIntake.Grace = fileCfg.GHIntake.Grace
		}
		if fileCfg.GHIntake.RenotifyAfter > 0 {
			cfg.GHIntake.RenotifyAfter = fileCfg.GHIntake.RenotifyAfter
		}
		if fileCfg.GHIntake.NotifyTo != "" {
			cfg.GHIntake.NotifyTo = fileCfg.GHIntake.NotifyTo
		}
		// Non-zero, not >0: a negative value is the documented way to turn
		// escalation off.
		if fileCfg.GHIntake.EscalateAfter != 0 {
			cfg.GHIntake.EscalateAfter = fileCfg.GHIntake.EscalateAfter
		}
		if len(fileCfg.GHIntake.Repos) > 0 {
			cfg.GHIntake.Repos = fileCfg.GHIntake.Repos
		}
		if fileCfg.ackWatchEnabledSet {
			cfg.AckWatch.Enabled = fileCfg.AckWatch.Enabled
		}
		if fileCfg.AckWatch.Interval > 0 {
			cfg.AckWatch.Interval = fileCfg.AckWatch.Interval
		}
		if fileCfg.AckWatch.RenotifyAfter > 0 {
			cfg.AckWatch.RenotifyAfter = fileCfg.AckWatch.RenotifyAfter
		}
		if fileCfg.AckWatch.NotifyTo != "" {
			cfg.AckWatch.NotifyTo = fileCfg.AckWatch.NotifyTo
		}
		// Non-zero, not >0: a negative value is the documented way to turn
		// escalation off, so it must survive the merge like any other override.
		if fileCfg.AckWatch.EscalateAfter != 0 {
			cfg.AckWatch.EscalateAfter = fileCfg.AckWatch.EscalateAfter
		}
		if fileCfg.deafWatchEnabledSet {
			cfg.DeafWatch.Enabled = fileCfg.DeafWatch.Enabled
		}
		if fileCfg.DeafWatch.Interval > 0 {
			cfg.DeafWatch.Interval = fileCfg.DeafWatch.Interval
		}
		// Non-zero, not >0: a negative hold_down is the documented way to turn
		// the hold-down off, so it must survive the merge like any other
		// override.
		if fileCfg.DeafWatch.HoldDown != 0 {
			cfg.DeafWatch.HoldDown = fileCfg.DeafWatch.HoldDown
		}
		if fileCfg.DeafWatch.RenotifyAfter > 0 {
			cfg.DeafWatch.RenotifyAfter = fileCfg.DeafWatch.RenotifyAfter
		}
		if fileCfg.DeafWatch.NotifyTo != "" {
			cfg.DeafWatch.NotifyTo = fileCfg.DeafWatch.NotifyTo
		}
		// Non-zero, not >0: a negative value is the documented way to turn
		// age-based escalation off, so it must survive the merge like any other
		// override.
		if fileCfg.DeafWatch.EscalateAfter != 0 {
			cfg.DeafWatch.EscalateAfter = fileCfg.DeafWatch.EscalateAfter
		}
		if fileCfg.wedgeWatchEnabledSet {
			cfg.WedgeWatch.Enabled = fileCfg.WedgeWatch.Enabled
		}
		if fileCfg.WedgeWatch.Interval > 0 {
			cfg.WedgeWatch.Interval = fileCfg.WedgeWatch.Interval
		}
		// Non-zero, not >0: a negative hold-down is the documented way to turn
		// that hold-down off, so it must survive the merge like any other
		// override. Only tests should do it — without the hold-downs an agent
		// merely writing about the wedge reports itself.
		if fileCfg.WedgeWatch.MarkerHoldDown != 0 {
			cfg.WedgeWatch.MarkerHoldDown = fileCfg.WedgeWatch.MarkerHoldDown
		}
		if fileCfg.WedgeWatch.FreezeHoldDown != 0 {
			cfg.WedgeWatch.FreezeHoldDown = fileCfg.WedgeWatch.FreezeHoldDown
		}
		if fileCfg.WedgeWatch.MinUptime != 0 {
			cfg.WedgeWatch.MinUptime = fileCfg.WedgeWatch.MinUptime
		}
		if fileCfg.WedgeWatch.Ratio > 0 {
			cfg.WedgeWatch.Ratio = fileCfg.WedgeWatch.Ratio
		}
		if fileCfg.WedgeWatch.CoincidenceWindow > 0 {
			cfg.WedgeWatch.CoincidenceWindow = fileCfg.WedgeWatch.CoincidenceWindow
		}
		if fileCfg.WedgeWatch.RenotifyAfter > 0 {
			cfg.WedgeWatch.RenotifyAfter = fileCfg.WedgeWatch.RenotifyAfter
		}
		if fileCfg.doneReapEnabledSet {
			cfg.DoneReap.Enabled = fileCfg.DoneReap.Enabled
		}
		// Non-zero, not >0: a negative idle_grace is the documented way to remove
		// the grace window, so it must survive the merge like any other override.
		if fileCfg.DoneReap.IdleGrace != 0 {
			cfg.DoneReap.IdleGrace = fileCfg.DoneReap.IdleGrace
		}
		if fileCfg.stallWatchEnabledSet {
			cfg.StallWatch.Enabled = fileCfg.StallWatch.Enabled
		}
		if fileCfg.StallWatch.Agent != "" {
			cfg.StallWatch.Agent = fileCfg.StallWatch.Agent
		}
		if fileCfg.StallWatch.UnclaimedItemAgeThreshold > 0 {
			cfg.StallWatch.UnclaimedItemAgeThreshold = fileCfg.StallWatch.UnclaimedItemAgeThreshold
		}
		if fileCfg.StallWatch.UnreadMailAgeThreshold > 0 {
			cfg.StallWatch.UnreadMailAgeThreshold = fileCfg.StallWatch.UnreadMailAgeThreshold
		}
		if fileCfg.StallWatch.MaxUnreadMailCount > 0 {
			cfg.StallWatch.MaxUnreadMailCount = fileCfg.StallWatch.MaxUnreadMailCount
		}
		if fileCfg.StallWatch.NudgeCooldown > 0 {
			cfg.StallWatch.NudgeCooldown = fileCfg.StallWatch.NudgeCooldown
		}
		if fileCfg.StallWatch.RepeatBackoffCap > 0 {
			cfg.StallWatch.RepeatBackoffCap = fileCfg.StallWatch.RepeatBackoffCap
		}
		if fileCfg.priorityWakeEnabledSet {
			cfg.StallWatch.PriorityWakeEnabled = fileCfg.StallWatch.PriorityWakeEnabled
		}
		if fileCfg.StallWatch.HighPriorityWakeDelay > 0 {
			cfg.StallWatch.HighPriorityWakeDelay = fileCfg.StallWatch.HighPriorityWakeDelay
		}
		if fileCfg.StallWatch.HighPriorityWakeCooldown > 0 {
			cfg.StallWatch.HighPriorityWakeCooldown = fileCfg.StallWatch.HighPriorityWakeCooldown
		}
		if len(fileCfg.StallWatch.FastPriorities) > 0 {
			cfg.StallWatch.FastPriorities = fileCfg.StallWatch.FastPriorities
		}
		if len(fileCfg.StallWatch.NonDispatchableAssignees) > 0 {
			cfg.StallWatch.NonDispatchableAssignees = fileCfg.StallWatch.NonDispatchableAssignees
		}
		if fileCfg.blockedReminderEnabledSet {
			cfg.StallWatch.BlockedReminderEnabled = fileCfg.StallWatch.BlockedReminderEnabled
		}
		if fileCfg.StallWatch.BlockedReminderCooldown > 0 {
			cfg.StallWatch.BlockedReminderCooldown = fileCfg.StallWatch.BlockedReminderCooldown
		}
		// Not `> 0`: a negative value is the documented way to say "no cap", and a
		// `> 0` test would silently discard it in favour of the default — turning
		// an explicit request for unlimited notices into four. Zero still falls
		// through to the default, which is what "unset" looks like in flat TOML.
		if fileCfg.StallWatch.BlockedReminderMaxNotices != 0 {
			cfg.StallWatch.BlockedReminderMaxNotices = fileCfg.StallWatch.BlockedReminderMaxNotices
		}

		// [dispatch_pairing] has no code-side defaults to preserve — the zero
		// value is "no repos, gate inert" — so every field is copied whenever the
		// file names it. An explicitly empty `repos = []` therefore turns the gate
		// off, which is the only way an operator can say that.
		if len(fileCfg.DispatchPairing.Repos) > 0 {
			cfg.DispatchPairing.Repos = fileCfg.DispatchPairing.Repos
		}
		if len(fileCfg.DispatchPairing.RequireTags) > 0 {
			cfg.DispatchPairing.RequireTags = fileCfg.DispatchPairing.RequireTags
		}
		if len(fileCfg.DispatchPairing.PairTags) > 0 {
			cfg.DispatchPairing.PairTags = fileCfg.DispatchPairing.PairTags
		}
		if len(fileCfg.DispatchPairing.WaiverTags) > 0 {
			cfg.DispatchPairing.WaiverTags = fileCfg.DispatchPairing.WaiverTags
		}

		// [dispatch] DOES have code-side defaults to preserve, and zero is a
		// value rather than an absence for both keys — 0 disarms the cap, 0
		// drops the refinery reservation. So the merge is gated on the
		// "key appeared" flags, never on `> 0` (mg-3977).
		if fileCfg.dispatchCapMaxSet {
			cfg.DispatchCap.MaxPolecatsPerRepo = fileCfg.DispatchCap.MaxPolecatsPerRepo
		}
		if fileCfg.dispatchCapReserveSet {
			cfg.DispatchCap.RefineryReserve = fileCfg.DispatchCap.RefineryReserve
		}

		// [audit_successor] has no code-side defaults to preserve either — the
		// zero value is "no repos, detector inert". Window is the one exception:
		// zero means unset and AuditWindow() supplies the calibrated default, so
		// only a positive value is copied.
		if len(fileCfg.AuditSuccessor.Repos) > 0 {
			cfg.AuditSuccessor.Repos = fileCfg.AuditSuccessor.Repos
		}
		if len(fileCfg.AuditSuccessor.AuditTags) > 0 {
			cfg.AuditSuccessor.AuditTags = fileCfg.AuditSuccessor.AuditTags
		}
		if len(fileCfg.AuditSuccessor.CleanVerdictTags) > 0 {
			cfg.AuditSuccessor.CleanVerdictTags = fileCfg.AuditSuccessor.CleanVerdictTags
		}
		if fileCfg.AuditSuccessor.Window > 0 {
			cfg.AuditSuccessor.Window = fileCfg.AuditSuccessor.Window
		}
	}

	// Environment variables override config file
	if portStr := os.Getenv("POGO_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
			cfg.Port = port
		}
	}
	if bind := os.Getenv("POGO_BIND"); bind != "" {
		cfg.Bind = bind
	}
	if mfStr := os.Getenv("POGO_MAX_FILES_PER_TREE"); mfStr != "" {
		if mf, err := strconv.Atoi(mfStr); err == nil && mf > 0 {
			cfg.MaxFilesPerTree = mf
		}
	}

	// POGO_AGENT_COMMAND overrides the default agent command from config file
	if agentCmd := os.Getenv("POGO_AGENT_COMMAND"); agentCmd != "" {
		cfg.Agents.Command = agentCmd
	}

	// POGO_AGENT_PROVIDER overrides the [agents] provider from the config file.
	if provider := os.Getenv("POGO_AGENT_PROVIDER"); provider != "" {
		cfg.Agents.Provider = provider
	}

	// POGO_EXTRA_PATH overrides [agents] extra_path from the config file.
	if extra := os.Getenv("POGO_EXTRA_PATH"); extra != "" {
		cfg.Agents.ExtraPath = filepath.SplitList(extra)
	}

	// POGO_AGENT_AUTOSTART overrides [agents] autostart from the config file.
	if v := os.Getenv("POGO_AGENT_AUTOSTART"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Agents.AutoStart = b
		}
	}
	// Default the provider so existing deployments work with no config change.
	if cfg.Agents.Provider == "" {
		cfg.Agents.Provider = DefaultProvider
	}

	// Default the coordinator name so existing deployments work with no
	// config change, then let the stall watcher follow it unless an explicit
	// [stall_watch] agent was configured.
	if cfg.Agents.Coordinator == "" {
		cfg.Agents.Coordinator = DefaultCoordinator
	}
	// Default the worker display name so existing deployments work with no
	// config change. Display-only; touches no identifier.
	if cfg.Agents.Worker == "" {
		cfg.Agents.Worker = DefaultWorker
	}
	if cfg.StallWatch.Agent == "" {
		cfg.StallWatch.Agent = cfg.Agents.Coordinator
	}

	return cfg
}

// ServerURL returns the base URL for connecting to the pogo daemon.
func (c *Config) ServerURL() string {
	return fmt.Sprintf("http://localhost:%d", c.Port)
}

// ListenAddr returns the address string for the server to listen on.
func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Bind, c.Port)
}

// ConfigDir returns the pogo configuration directory path.
func ConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "pogo")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "pogo")
}

// ConfigFilePath returns the path pogo WRITES config to, and the path whose
// existence answers "is this an install with a config file?".
//
// When POGO_HOME is set and $POGO_HOME/config.toml exists, that file wins so
// an isolated daemon (tests, CI) writes its own config instead of the real
// user's (mg-3dc3). Otherwise the XDG path from ConfigDir applies. The
// POGO_HOME probe is existence-gated rather than unconditional so
// deployments that set POGO_HOME but keep config.toml in ~/.config/pogo
// (the historical layout) are unaffected.
//
// It is NOT the whole read path: Load reads every layer ConfigFilePaths
// returns and merges them key by key. Callers that want "where did this value
// come from" should read Config.Sources.
func ConfigFilePath() string {
	if os.Getenv("POGO_HOME") != "" {
		p := filepath.Join(PogoHome(), "config.toml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	dir := ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.toml")
}

// ConfigFilePaths returns the config file layers Load reads, LOWEST precedence
// first: the XDG file (~/.config/pogo/config.toml), then — when POGO_HOME is
// set — $POGO_HOME/config.toml. Paths are returned whether or not they exist;
// Load skips the missing ones.
//
// The POGO_HOME layer is gated on the env var, not on PogoHome(), so an install
// that never sets POGO_HOME keeps reading exactly one file (~/.pogo/config.toml
// has never been consulted in that case, and starting now would be a surprise).
// A POGO_HOME that resolves onto the XDG directory yields one layer, not two.
func ConfigFilePaths() []string {
	var paths []string
	if dir := ConfigDir(); dir != "" {
		paths = append(paths, filepath.Join(dir, "config.toml"))
	}
	if os.Getenv("POGO_HOME") != "" {
		p := filepath.Join(PogoHome(), "config.toml")
		if len(paths) == 0 || filepath.Clean(paths[0]) != filepath.Clean(p) {
			paths = append(paths, p)
		}
	}
	return paths
}

// PogoHome returns the pogo state directory: $POGO_HOME, or ~/.pogo when
// unset. It deliberately never falls back to os.TempDir(): $TMPDIR differs
// between the launchd domain and an interactive shell/agent, so a
// TempDir-based path is not shared across domains. The singleton daemon
// lockfile (see LockfilePath) must resolve to the SAME path from launchd,
// shells, and agents, otherwise a second pogod acquires its own lock and
// displaces the running daemon (the :10000 race in #22).
//
// Every pogo state path (refinery-state.json, schedules.json, agents/,
// polecats/, events.log, recovery/, projects.json, plugin/) derives from this
// function, so overriding POGO_HOME (or HOME, via the default) fully isolates
// a daemon's state (mg-3dc3).
//
// Legacy normalization: an old shell integration exported POGO_HOME=$HOME
// ("where the dotfiles live"), and that value survives in existing zshrc
// copies and launchd plists. Honoring it literally would scatter agents/,
// refinery-state.json, etc. across the home directory root, so a POGO_HOME
// equal to the user's home dir is normalized to $HOME/.pogo — the documented
// default, and where all of that state already lives on such machines.
func PogoHome() string {
	if h := os.Getenv("POGO_HOME"); h != "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" &&
			filepath.Clean(h) == filepath.Clean(home) {
			return filepath.Join(h, ".pogo")
		}
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pogo")
}

// LockfilePath returns the deterministic path of the pogod singleton lockfile
// (pogo.pid) under PogoHome. Because PogoHome is domain-independent, the lock
// is shared across the launchd-managed pogod, shells, and agents, so a second
// pogod's TryLock fails instead of racing the live daemon for :10000 (#22).
func LockfilePath() string {
	return filepath.Join(PogoHome(), "pogo.pid")
}

// maxUnixSocketPathLen is the longest bindable AF_UNIX path. sockaddr_un's
// sun_path field is 104 bytes on darwin and 108 on linux, both counting the NUL
// terminator. We budget against the smaller (darwin) figure on every platform so
// that one POGO_HOME resolves to the same socket dir regardless of GOOS.
const maxUnixSocketPathLen = 103

// MaxAgentNameLen is the longest agent name whose attach socket is guaranteed to
// bind under AgentSocketDir. Real names are far shorter — "pm-dealdesk" (11) is
// the longest crew name, and a polecat is named for its work item ("8532") — so
// 24 bytes leaves better than 2x headroom.
//
// The reservation is a fixed constant rather than a function of the agent being
// bound: every agent under one POGO_HOME must agree on one socket dir, so the
// dir cannot depend on which agent binds first.
//
// agent.ValidateAgentName enforces this at spawn, so it is a promise and not
// merely a ceiling: a longer name is refused (HTTP 400) under every POGO_HOME,
// shallow or deep, rather than spawning an agent that runs fine but can never be
// attached to. Enforcing it unconditionally is the point — a name's fate must
// not depend on how deep the operator's root happens to be.
//
// The promise holds only because AgentSocketDir never returns a directory with
// less than agentSocketLeafBudget bytes of headroom, on any root and any TMPDIR;
// TestAgentSocketDirAlwaysFits pins that. Should this arithmetic ever drift from
// the real sun_path limit anyway, agent.Spawn treats a permanent bind failure as
// fatal, so the failure is loud rather than silent either way (mg-ef80).
const MaxAgentNameLen = 24

// agentSocketLeafBudget reserves room for the "/<agent name>.sock" leaf that
// callers append to AgentSocketDir.
const agentSocketLeafBudget = len("/") + MaxAgentNameLen + len(".sock")

// AgentSocketDir returns the directory holding the per-agent unix domain sockets
// that back `pogo agent attach`, and whether that directory lives inside
// PogoHome. Callers that want to report the fallback should use the returned
// bool rather than re-deriving it by inspecting the path: a POGO_HOME of "/"
// makes any prefix test lie.
//
// The directory derives from PogoHome() so two daemons on distinct POGO_HOME
// roots never share a socket path. Deriving it from os.TempDir() instead — as
// pogod did before mg-8532 — gave identically-named agents under different roots
// a single shared socket file, because $TMPDIR is per-user, not per-POGO_HOME.
// The singleton lockfile bars two pogods on the *same* root, but nothing stopped
// two on *different* roots from colliding here. The old symptom was quiet:
// whichever daemon bound last owned the path and the other silently lost attach.
// Once the mg-d216 attach supervisor shipped, it turned loud — each daemon
// observes the other's bind as its own socket being replaced, unlinks that live
// socket and rebinds, forever, on a 30s ticker.
//
// The sun_path limit forces one wrinkle. A sufficiently deep POGO_HOME (a
// t.TempDir() under /var/folders on darwin, say) leaves no room for the socket
// leaf, and bind would fail with EINVAL. Such a root falls back to a short
// directory named for a hash of the root — so the per-root distinctness this
// function exists to guarantee survives the fallback. The hash is taken over the
// cleaned root so that "/a/b" and "/a/b/" — which the lockfile already treats as
// one daemon — agree on one socket dir too.
//
// The returned directory always leaves room for the reserved MaxAgentNameLen
// leaf; every caller, and MaxAgentNameLen's promise to agent.ValidateAgentName,
// depends on that. The fallback therefore prefers os.TempDir() — per-user on
// darwin, and where these sockets already live — but only when it fits, because
// TMPDIR is itself unbounded: a TMPDIR over ~52 bytes leaves a directory in which
// no legal agent name could bind, which agent.Spawn treats as a fatal error
// rather than the silent attach loss it used to be. "/tmp" is the last resort;
// at 4 bytes it fits under any budget these constants could grow to. If it is
// not writable, NewRegistry's MkdirAll fails and pogod exits loudly at startup,
// which is the honest outcome (mg-ef80).
func AgentSocketDir() (dir string, insidePogoHome bool) {
	if dir := filepath.Join(PogoHome(), "agents", "sockets"); agentSocketDirFits(dir) {
		return dir, true
	}
	sum := sha256.Sum256([]byte(filepath.Clean(PogoHome())))
	leaf := "pogo-agents-" + hex.EncodeToString(sum[:4])
	if dir := filepath.Join(os.TempDir(), leaf); agentSocketDirFits(dir) {
		return dir, false
	}
	return filepath.Join("/tmp", leaf), false
}

// agentSocketDirFits reports whether dir leaves room to bind an agent socket
// beneath it without exceeding sun_path.
func agentSocketDirFits(dir string) bool {
	return len(dir)+agentSocketLeafBudget <= maxUnixSocketPathLen
}

// DialAddr returns a loopback TCP address (127.0.0.1:<port>) for probing
// whether a pogod is already bound to the daemon port. It targets 127.0.0.1
// explicitly rather than the raw Bind so a wildcard bind (0.0.0.0/::) is still
// probed on a concrete loopback address, and so the probe never races
// IPv6-vs-IPv4 resolution of "localhost". Callers use this to avoid spawning a
// rival pogod when the port is already held (#22).
func (c *Config) DialAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", c.Port)
}

// loadConfigFiles reads every config layer in ConfigFilePaths and merges them
// KEY BY KEY into one parsedConfig, lowest precedence first. It returns an
// error when no layer exists at all, which is Load's signal to stay on
// defaults + env.
//
// Key-by-key is the whole point. $POGO_HOME/config.toml used to shadow
// ~/.config/pogo/config.toml wholesale: whichever file ConfigFilePath picked
// was the only file read. That made the file a trapdoor — anything that
// created a partial $POGO_HOME/config.toml (a sandbox script, a test fixture,
// an operator pinning a port) silently dropped every key the real config
// carried, including the [agents] coordinator/worker pin the default-migration
// guard writes there. Dropping the pin re-arms the role-default flip (mg-ce47)
// against a deployment that was explicitly protected from it. Layering keeps
// the unnamed keys and overrides only what the higher file actually says
// (mg-cf9e).
func loadConfigFiles() (*parsedConfig, error) {
	paths := ConfigFilePaths()
	if len(paths) == 0 {
		return nil, fmt.Errorf("no config path")
	}

	cfg := &parsedConfig{}
	var firstErr error
	for _, path := range paths {
		switch err := parseConfigFileInto(cfg, path); {
		case err == nil:
			cfg.sources = append(cfg.sources, path)
		case os.IsNotExist(err):
			// A missing layer is the normal case, not an error.
		case firstErr == nil:
			firstErr = err
		}
	}
	if len(cfg.sources) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, os.ErrNotExist
	}
	return cfg, nil
}

// parseConfigFileInto parses one TOML config file into cfg, overwriting only
// the fields whose keys the file names. Only the minimal subset pogo needs is
// understood; unknown sections and keys are ignored.
func parseConfigFileInto(cfg *parsedConfig, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	currentSection := ""
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Section headers
		if strings.HasPrefix(line, "[") {
			currentSection = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		// Strip surrounding quotes from values
		unquotedVal := unquote(val)

		switch currentSection {
		case "server":
			switch key {
			case "port":
				if port, err := strconv.Atoi(val); err == nil && port > 0 && port <= 65535 {
					cfg.Port = port
				}
			case "bind":
				cfg.Bind = unquotedVal
			}
		case "refinery":
			switch key {
			case "enabled":
				cfg.Refinery.Enabled = val == "true"
				cfg.refineryEnabledSet = true
			case "poll_interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.Refinery.PollInterval = d
				}
			case "max_concurrent_merges":
				if n, err := strconv.Atoi(val); err == nil && n > 0 {
					cfg.Refinery.MaxConcurrentMerges = n
				}
			}
		case "search":
			switch key {
			case "max_files_per_tree":
				if mf, err := strconv.Atoi(val); err == nil && mf > 0 {
					cfg.MaxFilesPerTree = mf
				}
			case "index_interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.IndexInterval = d
				}
			case "index_roots":
				cfg.IndexRoots = parseStringArray(val)
			}
		case "heartbeat":
			switch key {
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.Heartbeat.Interval = d
				}
			case "jump_threshold":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.Heartbeat.JumpThreshold = d
				}
			}
		case "gitgc":
			switch key {
			case "enabled":
				cfg.GitGC.Enabled = val == "true"
				cfg.gitgcEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.GitGC.Interval = d
				}
			case "repos":
				cfg.GitGC.Repos = parseStringArray(val)
			}
		case "stall_watch":
			switch key {
			case "enabled":
				cfg.StallWatch.Enabled = val == "true"
				cfg.stallWatchEnabledSet = true
			case "agent":
				cfg.StallWatch.Agent = unquotedVal
			case "unclaimed_item_age_threshold":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.UnclaimedItemAgeThreshold = d
				}
			case "unread_mail_age_threshold":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.UnreadMailAgeThreshold = d
				}
			case "max_unread_mail_count":
				if n, err := strconv.Atoi(unquotedVal); err == nil && n > 0 {
					cfg.StallWatch.MaxUnreadMailCount = n
				}
			case "nudge_cooldown":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.NudgeCooldown = d
				}
			case "repeat_backoff_cap":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.RepeatBackoffCap = d
				}
			case "priority_wake_enabled":
				cfg.StallWatch.PriorityWakeEnabled = val == "true"
				cfg.priorityWakeEnabledSet = true
			case "high_priority_wake_delay":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.HighPriorityWakeDelay = d
				}
			case "high_priority_wake_cooldown":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.HighPriorityWakeCooldown = d
				}
			case "fast_priorities":
				cfg.StallWatch.FastPriorities = parseStringArray(val)
			case "non_dispatchable_assignees":
				cfg.StallWatch.NonDispatchableAssignees = parseStringArray(val)
			case "blocked_reminder_enabled":
				cfg.StallWatch.BlockedReminderEnabled = val == "true"
				cfg.blockedReminderEnabledSet = true
			case "blocked_reminder_cooldown":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.BlockedReminderCooldown = d
				}
			case "blocked_reminder_max_notices":
				// Negative is accepted — it is the "no cap" spelling. Only an
				// unparseable value is ignored.
				if n, err := strconv.Atoi(unquotedVal); err == nil {
					cfg.StallWatch.BlockedReminderMaxNotices = n
				}
			}
		case "dispatch":
			switch key {
			case "max_polecats_per_repo":
				// A negative value is clamped to 0 (unlimited) rather than
				// rejected: the two readings of `-1` are "no limit" and "refuse
				// everything", and only one of those is recoverable without a
				// second config edit.
				if n, err := strconv.Atoi(unquotedVal); err == nil {
					if n < 0 {
						n = 0
					}
					cfg.DispatchCap.MaxPolecatsPerRepo = n
					cfg.dispatchCapMaxSet = true
				}
			case "refinery_reserve":
				if n, err := strconv.Atoi(unquotedVal); err == nil {
					if n < 0 {
						n = 0
					}
					cfg.DispatchCap.RefineryReserve = n
					cfg.dispatchCapReserveSet = true
				}
			}
		case "dispatch_pairing":
			switch key {
			case "repos":
				cfg.DispatchPairing.Repos = parseStringArray(val)
			case "require_tags":
				cfg.DispatchPairing.RequireTags = parseStringArray(val)
			case "pair_tags":
				cfg.DispatchPairing.PairTags = parseStringArray(val)
			case "waiver_tags":
				cfg.DispatchPairing.WaiverTags = parseStringArray(val)
			}
		case "audit_successor":
			switch key {
			case "repos":
				cfg.AuditSuccessor.Repos = parseStringArray(val)
			case "audit_tags":
				cfg.AuditSuccessor.AuditTags = parseStringArray(val)
			case "clean_verdict_tags":
				cfg.AuditSuccessor.CleanVerdictTags = parseStringArray(val)
			case "window":
				// An unparseable or non-positive window falls through to
				// DefaultAuditSuccessorWindow rather than to zero. Zero would mean
				// "report every merged audit the instant it lands", which is the
				// loudest possible reading of a typo.
				if d, err := time.ParseDuration(unquotedVal); err == nil && d > 0 {
					cfg.AuditSuccessor.Window = d
				}
			}
		case "reaper":
			switch key {
			case "enabled":
				cfg.Reaper.Enabled = val == "true"
				cfg.reaperEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.Reaper.Interval = d
				}
			case "max_kickstarts":
				if n, err := strconv.Atoi(unquotedVal); err == nil && n > 0 {
					cfg.Reaper.MaxKickstarts = n
				}
			case "jobs":
				cfg.Reaper.Jobs = parseReaperJobs(parseStringArray(val))
			}
		case "reconcile":
			switch key {
			case "mirrors":
				cfg.Reconcile.Mirrors = parseReconcileMirrors(parseStringArray(val))
			}
		case "drift_watch":
			switch key {
			case "enabled":
				cfg.DriftWatch.Enabled = val == "true"
				cfg.driftWatchEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.DriftWatch.Interval = d
				}
			}
		case "cred_expiry":
			switch key {
			case "enabled":
				cfg.CredExpiry.Enabled = val == "true"
				cfg.credExpiryEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.CredExpiry.Interval = d
				}
			case "blind_renotify":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.CredExpiry.BlindRenotify = d
				}
			}
		case "ack_watch":
			switch key {
			case "enabled":
				cfg.AckWatch.Enabled = val == "true"
				cfg.ackWatchEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.AckWatch.Interval = d
				}
			case "renotify_after":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.AckWatch.RenotifyAfter = d
				}
			case "notify_to":
				cfg.AckWatch.NotifyTo = unquotedVal
			case "escalate_after":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.AckWatch.EscalateAfter = d
				}
			}
		case "deaf_watch":
			switch key {
			case "enabled":
				cfg.DeafWatch.Enabled = val == "true"
				cfg.deafWatchEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.DeafWatch.Interval = d
				}
			case "hold_down":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.DeafWatch.HoldDown = d
				}
			case "renotify_after":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.DeafWatch.RenotifyAfter = d
				}
			case "notify_to":
				cfg.DeafWatch.NotifyTo = unquotedVal
			case "escalate_after":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.DeafWatch.EscalateAfter = d
				}
			}
		case "wedge_watch":
			switch key {
			case "enabled":
				cfg.WedgeWatch.Enabled = val == "true"
				cfg.wedgeWatchEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.WedgeWatch.Interval = d
				}
			case "marker_hold_down":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.WedgeWatch.MarkerHoldDown = d
				}
			case "freeze_hold_down":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.WedgeWatch.FreezeHoldDown = d
				}
			case "min_uptime":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.WedgeWatch.MinUptime = d
				}
			case "ratio":
				if f, err := strconv.ParseFloat(unquotedVal, 64); err == nil {
					cfg.WedgeWatch.Ratio = f
				}
			case "coincidence_window":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.WedgeWatch.CoincidenceWindow = d
				}
			case "renotify_after":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.WedgeWatch.RenotifyAfter = d
				}
			}
		case "done_reap":
			switch key {
			case "enabled":
				cfg.DoneReap.Enabled = val == "true"
				cfg.doneReapEnabledSet = true
			case "idle_grace":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.DoneReap.IdleGrace = d
				}
			}
		case "gh_teardown":
			switch key {
			case "enabled":
				cfg.GHTeardown.Enabled = val == "true"
				cfg.ghTeardownEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.GHTeardown.Interval = d
				}
			case "renotify_after":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.GHTeardown.RenotifyAfter = d
				}
			case "notify_to":
				cfg.GHTeardown.NotifyTo = unquotedVal
			case "escalate_after":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.GHTeardown.EscalateAfter = d
				}
			}
		case "gh_intake":
			switch key {
			case "enabled":
				cfg.GHIntake.Enabled = val == "true"
				cfg.ghIntakeEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.GHIntake.Interval = d
				}
			case "grace":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.GHIntake.Grace = d
				}
			case "renotify_after":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.GHIntake.RenotifyAfter = d
				}
			case "notify_to":
				cfg.GHIntake.NotifyTo = unquotedVal
			case "escalate_after":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.GHIntake.EscalateAfter = d
				}
			case "repos":
				cfg.GHIntake.Repos = parseStringArray(val)
			}
		case "agents":
			switch key {
			case "autostart":
				cfg.Agents.AutoStart = val == "true"
				cfg.agentsAutoStartSet = true
			case "command":
				cfg.Agents.Command = unquotedVal
			case "provider":
				cfg.Agents.Provider = unquotedVal
			case "coordinator":
				cfg.Agents.Coordinator = unquotedVal
			case "worker":
				cfg.Agents.Worker = unquotedVal
			case "sme":
				cfg.Agents.SME = unquotedVal
			case "extra_path":
				cfg.Agents.ExtraPath = parseStringArray(val)
			}
		case "agents.crew":
			switch key {
			case "command":
				cfg.Agents.Crew.Command = unquotedVal
			case "provider":
				cfg.Agents.Crew.Provider = unquotedVal
			}
		case "agents.polecat":
			switch key {
			case "command":
				cfg.Agents.Polecat.Command = unquotedVal
			case "provider":
				cfg.Agents.Polecat.Provider = unquotedVal
			}
		}
	}

	return scanner.Err()
}

// unquote strips one matched pair of surrounding TOML string quotes — basic
// ("...") or literal ('...') — from val. Values without a matched pair are
// returned unchanged, so a bare (technically invalid, but historically
// accepted) value like `bind = 127.0.0.1` keeps working, and interior quotes
// are never eaten. This is the regression from mg-a616: `bind = "127.0.0.1"`
// used to keep its quotes and produce an unusable listen address.
func unquote(val string) string {
	if len(val) >= 2 {
		first, last := val[0], val[len(val)-1]
		if first == last && (first == '"' || first == '\'') {
			return val[1 : len(val)-1]
		}
	}
	return val
}

// parseReaperJobs turns raw "<label>|<heartbeat-path>|<period>" entries into
// ReaperJob values. Malformed entries (wrong field count, empty label/path, or
// an unparseable period) are dropped with a log line rather than failing the
// whole config load — a typo in one job should not take the reaper (or pogod)
// down. The flat single-line encoding is deliberate: pogo's config is
// hand-parsed flat TOML with no table-array support (see the [stall_watch]
// note), so a per-field nested block is not available.
func parseReaperJobs(entries []string) []ReaperJob {
	var out []ReaperJob
	for _, e := range entries {
		parts := strings.Split(e, "|")
		if len(parts) != 3 {
			log.Printf("config: [reaper] ignoring malformed job %q (want label|path|period)", e)
			continue
		}
		label := strings.TrimSpace(parts[0])
		path := strings.TrimSpace(parts[1])
		period, err := time.ParseDuration(strings.TrimSpace(parts[2]))
		if label == "" || path == "" || err != nil || period <= 0 {
			log.Printf("config: [reaper] ignoring invalid job %q", e)
			continue
		}
		out = append(out, ReaperJob{Label: label, Heartbeat: path, Period: period})
	}
	return out
}

// parseReconcileMirrors turns raw "<name>|<source>|<target>[|<label>]" entries
// into ReconcileMirror values. The label is optional (three or four fields).
// A leading ~ in source/target is expanded to the home directory so config can
// be written portably. Malformed entries (wrong field count or an empty
// name/source/target) are dropped with a log line rather than failing the whole
// config load — a typo in one mirror should not take reconcile (or pogod) down.
// The flat single-line encoding matches [reaper] jobs: pogo's config is
// hand-parsed flat TOML with no table-array support.
func parseReconcileMirrors(entries []string) []ReconcileMirror {
	var out []ReconcileMirror
	for _, e := range entries {
		parts := strings.Split(e, "|")
		if len(parts) != 3 && len(parts) != 4 {
			log.Printf("config: [reconcile] ignoring malformed mirror %q (want name|source|target[|label])", e)
			continue
		}
		name := strings.TrimSpace(parts[0])
		source := expandTildePath(strings.TrimSpace(parts[1]))
		target := expandTildePath(strings.TrimSpace(parts[2]))
		label := ""
		if len(parts) == 4 {
			label = strings.TrimSpace(parts[3])
		}
		if name == "" || source == "" || target == "" {
			log.Printf("config: [reconcile] ignoring invalid mirror %q", e)
			continue
		}
		out = append(out, ReconcileMirror{Name: name, Source: source, Target: target, Label: label})
	}
	return out
}

// expandTildePath expands a leading ~ to the user's home directory. A bare ~ or
// ~/... only; ~user is left untouched (unsupported). Mirrors the reaper's
// expandHome so config paths are written portably.
func expandTildePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// parseStringArray parses a minimal single-line TOML string array,
// e.g. `["/home/user/dev", "/work"]`, into a slice. Empty/blank entries are
// dropped. This is intentionally simple — it does not handle multi-line arrays.
func parseStringArray(val string) []string {
	val = strings.TrimSpace(val)
	val = strings.TrimPrefix(val, "[")
	val = strings.TrimSuffix(val, "]")
	var out []string
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "\"'")
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
