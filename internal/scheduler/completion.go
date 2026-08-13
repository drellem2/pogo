package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Completion tracking: the counterpart to scheduler_fire_delivered.
//
// # Why this exists
//
// During the 23h30m fleet outage of 2026-07-22 the events log recorded 647
// `scheduler_fire_delivered` and 771 `nudge_sent` events. Every one was TRUE:
// the bytes really did reach a live harness, on time, with nothing queued. Not
// one was USEFUL, because every consuming turn was a synthetic zero-token
// "Login expired · Please run /login" that failed in ~10ms. Delivery is the
// half of the transaction pogod can see by itself; nothing recorded whether the
// turn the fire triggered accomplished anything. With no completion signal a
// 100%-dead fleet and a 100%-healthy fleet produce the same events log — which
// is why the failure survived twice.
// See docs/investigations/fleet-auth-expiry-2026-07-22.md §"The trap this
// incident actually sets".
//
// # What counts as completion
//
// A fire's work is "complete" when the agent SAYS SO, by running a command.
// That definition is chosen for one property above all others: producing the
// signal requires a live model turn that ran a tool. A synthetic error turn
// cannot emit it — it never calls the API and never runs anything. So the
// signal fails in the same direction as the work it measures, which is exactly
// what `scheduler_fire_delivered` does not do.
//
// Concretely: every fire carries a nonce token in its body plus the one-line
// `pogo schedule ack` invocation that redeems it. The agent runs that command
// when the fire's work is done; pogod validates the token against the
// outstanding fire and emits `scheduler_fire_completed`.
//
// Applied to 2026-07-22 00:00–23:40, this signal reads: **647 delivered, 0
// completed**, with per-schedule UnackedStreak climbing monotonically to 202
// (mayor) and 143 (each PM). That is the answer the dispatch note demanded be
// obviously different from 647.
//
// # Why not read the harness transcript
//
// mg-8cdb's synthetic-failure detector reads Claude Code session transcripts
// and is the right tool for the specific auth case, but it works only for
// harnesses that expose a readable transcript — codex, pi and cursor decline
// explicitly. An ack is a shell command, so it is harness-independent by
// construction: any harness that can run a tool can produce it.
//
// # The honest limitation, stated up front
//
// An agent can simply forget to ack. So a MISSING completion does not prove the
// turn failed, and this counter must never be read as a per-fire verdict. Two
// things make it useful anyway:
//
//  1. CompletionTracked gates interpretation. A schedule that has never acked
//     is UNKNOWN, not failing — only a schedule that has proven it can ack, and
//     then stopped, is evidence.
//
//     "Never acked" means never, not never-since-the-last-re-registration.
//     Entry.EverAcked carries that one bit across the boot-path re-registration
//     that zeroes the counters, because without it the gate could not tell a
//     schedule nobody ever taught to ack from a schedule whose agent acked 39
//     times and then stopped coming back — and the nightly bounce put the
//     entire crew into the second state while making it look like the first
//     (mg-00d6).
//  2. The signal that matters is FLEET-WIDE and RATIOED. One agent skipping one
//     ack is noise. Every ack-aware schedule in the fleet going to zero within
//     the same minute is the 2026-07-22 shape, and nothing else looks like it.
//
// The counters are deliberately on the persisted Entry rather than derived by
// scanning events.log: the denominator has to survive the pogod restarts that
// an outage tends to produce.

// completionTokenBytes is the nonce width. Eight hex chars is plenty for a
// value whose only job is to distinguish the outstanding fire from the previous
// one — it is not a security boundary, it is a replay guard.
const completionTokenBytes = 4

// AckStaleWindow bounds how long an issued token stays redeemable. A fire whose
// token is older than this has been superseded in spirit even if no newer fire
// has been issued yet (a one-shot, or a schedule with a long cron), and
// accepting its ack would credit completion to work that finished a day late.
// Generous on purpose: a legitimately long agent turn must still be able to ack.
//
// It doubles as the retention bound for a fired one-shot, which is held in the
// live set until its ack lands (mg-64e6) — GCExpiredOneShots reaps it at
// exactly the moment its token stops being redeemable here, so the two cannot
// drift apart into an entry that is kept but can no longer be acked.
const AckStaleWindow = 24 * time.Hour

var (
	// ErrScheduleNotFound is returned by Ack when no entry matches.
	ErrScheduleNotFound = errors.New("scheduler: schedule not found")

	// ErrNoPendingFire is returned when the schedule exists but has no
	// outstanding fire to acknowledge (already acked, or never fired).
	ErrNoPendingFire = errors.New("scheduler: no fire outstanding to acknowledge")

	// ErrStaleToken is returned when the presented token does not match the
	// outstanding fire, or has aged past AckStaleWindow. Rejecting rather than
	// silently accepting is what keeps FiresCompleted from being inflated by a
	// token copied out of an old transcript.
	ErrStaleToken = errors.New("scheduler: token does not match the outstanding fire")
)

// Outstanding reports whether this schedule is currently holding a redeemable
// token — a fire delivered, not yet acked, and not yet superseded. It is the
// "boundary" term of the identity below: one per schedule at most, and a
// property of WHEN YOU LOOKED rather than of the agent.
func (e Entry) Outstanding() bool { return e.PendingToken != "" }

// AttentionGap returns deliveries per ack cycle — 1.0 for a schedule whose
// agent acks every fire before the next one arrives, 2.9 for one that acks once
// per 2.9 fires. It returns 0 when no cycle has closed, which is not a gap of
// zero but an absence of measurement, and callers must render it as such.
//
// This is the SAME QUANTITY as FiresCompleted/FiresDelivered, inverted. The
// identity
//
//	FiresCompleted/FiresDelivered  ==  1/AttentionGap  -  boundary/FiresDelivered
//
// is algebra, not an empirical fit: FiresDelivered is by construction the number
// of fires in all the runs, and FiresCompleted plus the boundary term is the
// number of runs. mg-ddf7 confirmed it to zero residual across all 114 schedules
// in this fleet's event log, and populations.go exposes the same identity
// computed from events instead of counters.
//
// It exists because the two renderings are not equally honest. "103/302, 34%"
// invites the reading that 100% was available and 66% of the work was skipped;
// neither half is true. Only the newest fire's token is redeemable
// (issueFireTokenLocked), so a schedule whose agent's turns outlast its cadence
// CANNOT reach 100% however promptly and completely it works — its ceiling is
// 1/gap, set by the delivery interleaving rather than by the agent. Stated as a
// gap, the number says what it measures: how many fires land per turn. Stated
// as a percentage of an unreachable 100%, it reads as an accusation, and one
// such reading cost 46 hours of a coordinator's attention on an alarm naming no
// action it could take (mg-a14c).
//
// Read UnackedStreak, not this, for the failure the counters exist to catch: a
// busy-but-healthy agent's streak is bounded by its own turn length while a
// wedged one's climbs without bound (202, for the mayor, on 2026-07-22).
func (e Entry) AttentionGap() float64 {
	cycles := e.FiresCompleted
	if e.Outstanding() {
		cycles++
	}
	if cycles <= 0 || e.FiresDelivered <= 0 {
		return 0
	}
	return float64(e.FiresDelivered) / float64(cycles)
}

// AckResult describes an accepted completion.
type AckResult struct {
	Entry   Entry         `json:"entry"`
	Latency time.Duration `json:"-"`
	// LatencyMS is the wall time from fire delivery to acknowledgement.
	LatencyMS int64 `json:"latency_ms"`
}

// newCompletionToken returns a fresh nonce. On the (practically impossible)
// failure of crypto/rand it falls back to a time-derived value rather than
// returning an error: a weaker token still discriminates consecutive fires,
// whereas failing the fire outright would trade a telemetry gap for an outage.
func newCompletionToken(now time.Time) string {
	b := make([]byte, completionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%08x", now.UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b)
}

// Ack records that the agent finished the work a fire triggered.
//
// agent may be empty, in which case the id is resolved only when a single agent
// owns it (mirroring lookupByID). The token must match the outstanding fire
// exactly; anything else is rejected with ErrStaleToken so a replayed ack
// cannot manufacture a healthy-looking ratio.
//
// On success it increments FiresCompleted, clears the pending token, resets
// UnackedStreak, persists, and emits scheduler_fire_completed.
func (s *Scheduler) Ack(agentName, id, token string, now time.Time) (AckResult, error) {
	if token == "" {
		return AckResult{}, ErrStaleToken
	}

	// Resolve outside the mutation lock, reusing the same id-disambiguation
	// rules as GET and DELETE so `pogo schedule ack <id>` behaves like its
	// siblings when --agent is omitted.
	resolved, ok, err := s.lookupByID(agentName, id)
	if err != nil {
		return AckResult{}, err
	}
	if !ok {
		return AckResult{}, fmt.Errorf("%w: %q", ErrScheduleNotFound, id)
	}

	key := entryKey{Agent: resolved.Agent, ID: resolved.ID}

	s.mu.Lock()
	entry, ok := s.entries[key]
	if !ok {
		s.mu.Unlock()
		return AckResult{}, fmt.Errorf("%w: %q", ErrScheduleNotFound, id)
	}
	if entry.PendingToken == "" {
		s.mu.Unlock()
		return AckResult{}, ErrNoPendingFire
	}
	if entry.PendingToken != token {
		s.mu.Unlock()
		return AckResult{}, ErrStaleToken
	}
	if !entry.PendingSince.IsZero() && now.Sub(entry.PendingSince) > AckStaleWindow {
		s.mu.Unlock()
		return AckResult{}, fmt.Errorf("%w: issued %s ago, past the %s window",
			ErrStaleToken, now.Sub(entry.PendingSince).Round(time.Second), AckStaleWindow)
	}

	latency := time.Duration(0)
	if !entry.PendingSince.IsZero() {
		latency = now.Sub(entry.PendingSince)
	}
	entry.FiresCompleted++
	entry.EverAcked = true
	entry.UnackedStreak = 0
	entry.LastCompletion = now
	entry.PendingToken = ""
	entry.PendingSince = time.Time{}
	done := entry.Clone()
	// A one-shot is retained past its fire ONLY so this ack can land (mg-64e6);
	// the ack is what it was waiting for, so it leaves now rather than sitting
	// spent until GCExpiredOneShots gets to it. The reason is what makes the
	// removal readable: `one_shot_acked` says a live model turn ran a tool to
	// report the work done, which is precisely the claim the old fire-time
	// `one_shot_complete` made without evidence.
	oneShotDone := entry.OneShot && !entry.LastFire.IsZero()
	if oneShotDone {
		delete(s.entries, key)
	}
	_ = s.persistLocked()
	s.mu.Unlock()

	s.emitCompletionEvent(done, now, token, latency)
	if oneShotDone {
		s.emitSchedulerRemovalEvent(ReasonOneShotAcked, done, now, nil)
	}

	return AckResult{Entry: done, Latency: latency, LatencyMS: latency.Milliseconds()}, nil
}

// issueFireTokenLocked stamps a fresh completion token on the entry and returns
// it. Caller must hold s.mu.
//
// It is called BEFORE delivery so the token is durable the moment the agent
// could possibly see it: an agent that acks within milliseconds of a fast nudge
// must not race a scheduler that has not stored the token yet.
//
// Issuing a new token silently abandons the previous one. That is the intended
// semantics — an unacked fire superseded by the next fire is exactly the event
// UnackedStreak is counting, and letting the old token stay redeemable would
// let a late ack for fire N-1 mask the fact that fire N also went unanswered.
//
// # The two repairs this rules out, and the measurement that rules them out
//
// Because only the newest token is redeemable, a run of N fires that lands
// inside one agent turn can yield AT MOST ONE ack, so FiresCompleted counts
// TOKEN REDEMPTIONS and its per-schedule ceiling sits below FiresDelivered for
// any schedule whose turns outlast its cadence. mg-a14c asked, correctly,
// whether the honest response is to make an ack retire the earlier fires too.
// It is not, and the reason is measured rather than argued:
//
//   - "let a superseded token stay redeemable" is the paragraph above, and its
//     objection stands: a late ack for fire N-1 would mask that fire N also
//     went unanswered.
//
//   - "one ack retires the ENTIRE outstanding set, reporting how many it
//     covered" survives that objection and dies on a different one. It assumes
//     the run of fires was superseded by an agent BUSY doing their work, so one
//     catch-up genuinely discharged all N. mg-772f measured whether that
//     assumption holds and it does not: across this fleet's token era 51.5% of
//     superseded fires landed while synthwatch had already detected that the
//     agent's turns were DYING. In the worked 27-fire episode of 2026-08-09, 26
//     of the 27 turns died on `API Error: ... (ENOTFOUND)` and never ran at
//     all. Retiring the set would have booked 27 completions for one surviving
//     turn — converting a 4.5-hour fleet outage into a clean reading, in the
//     one instrument built to see it. A repair that scores a dead fleet at 100%
//     is worse than the deficit it tidies.
//
// So the supersession rule stays and the NAME changes instead: the counter is
// rendered as acks-per-fire with its ceiling stated (see Entry.AttentionGap and
// `pogo schedule list`), and the number to alarm on is UnackedStreak, which is
// the one that does not saturate. Anyone reopening this needs a measurement
// that separates a busy agent from a dead one BEFORE the ack, not after.
func issueFireTokenLocked(entry *Entry, now time.Time) string {
	tok := newCompletionToken(now)
	entry.PendingToken = tok
	entry.PendingSince = now
	return tok
}

// recordDeliveryLocked bumps the delivered counter and the unacked streak.
// Caller must hold s.mu. Returns the streak after the increment, i.e. the count
// of fires outstanding INCLUDING this one — so a healthy agent that acks
// promptly reads 1 at delivery time, and a dead one climbs without bound.
func recordDeliveryLocked(entry *Entry) int {
	entry.FiresDelivered++
	entry.UnackedStreak++
	return entry.UnackedStreak
}

// emitCompletionEvent writes scheduler_fire_completed. It carries the running
// counters, not just the single completion, so one line of the events log
// answers "is this schedule actually accomplishing anything" without a join
// against the delivery events.
func (s *Scheduler) emitCompletionEvent(e Entry, at time.Time, token string, latency time.Duration) {
	details := map[string]any{
		"schedule_id":     e.ID,
		"to":              e.Agent,
		"fire_token":      token,
		"completed_at":    at.Format(time.RFC3339),
		"latency_ms":      latency.Milliseconds(),
		"fires_delivered": e.FiresDelivered,
		"fires_completed": e.FiresCompleted,
	}
	if e.Cron != "" {
		details["cron"] = e.Cron
	}
	events.EmitTo(context.Background(), s.logPath, events.Event{
		EventType: "scheduler_fire_completed",
		Agent:     "pogod",
		Details:   details,
	})
}

// CompletionStats is a fleet-wide roll-up of the delivered:completed ratio,
// used by `pogo schedule completion`. It answers the question the 2026-07-22
// events log could not: are the fires accomplishing anything?
type CompletionStats struct {
	// Schedules is every schedule considered.
	Schedules int `json:"schedules"`
	// Tracked is how many have ever acked, and so carry a meaningful streak.
	// Untracked schedules are excluded from Stalled and from Ratio: an entry
	// that never acks is unknown, not failing.
	//
	// "Ever" spans re-registrations (Entry.EverAcked, mg-00d6). It did not
	// used to: this count collapsed to ~0 every time the crew re-registered
	// its schedules at boot, which is once a night for the whole fleet.
	Tracked int `json:"tracked"`
	// TrackedReset is the subset of Tracked whose LIVE counters are zero — a
	// schedule that has proven it can ack but was re-registered since, so its
	// contribution to FiresDelivered/FiresCompleted is nil and the ratio below
	// is computed over a thinner denominator than Tracked suggests.
	//
	// It is reported because Tracked is a named compensating control for a
	// blind spot in internal/ackwatch (see the ack-aware cohort gate there),
	// and a control whose reading is thin has to say so rather than let the
	// reader infer breadth it does not have.
	TrackedReset int `json:"tracked_reset"`
	// Stalled is how many TRACKED schedules currently have an unacked streak
	// at or above StallThreshold.
	Stalled int `json:"stalled"`
	// FiresDelivered / FiresCompleted are lifetime sums over tracked entries.
	FiresDelivered int `json:"fires_delivered"`
	FiresCompleted int `json:"fires_completed"`
	// Ratio is FiresCompleted/FiresDelivered over tracked entries, or 0 when
	// nothing has been delivered.
	//
	// Its ceiling is NOT 1. See Entry.AttentionGap: only the newest fire's token
	// is redeemable, so a cohort whose turns outlast its cadence cannot reach it,
	// and a Ratio read as "the fraction of scheduled work that got done" is
	// reading a quantity that does not exist. MeanGap below is the same number in
	// units that do not invite that reading.
	Ratio float64 `json:"ratio"`
	// Outstanding is how many tracked schedules were holding a redeemable token
	// at the instant of measurement — the boundary term, one per schedule at
	// most. It is reported because it is the part of the deficit that is a
	// property of when you looked rather than of any agent, and because Ratio,
	// MeanGap and it are only reconcilable together:
	//
	//	Ratio  ==  1/MeanGap  -  Outstanding/FiresDelivered
	Outstanding int `json:"outstanding"`
	// MeanGap is fires delivered per ack cycle across tracked entries — the
	// reciprocal reading of Ratio, corrected for Outstanding. 0 when no cycle
	// closed, which means UNMEASURED rather than instantaneous.
	//
	// That zero has the same two readings this whole signal exists to separate,
	// so its denominator travels with it rather than being inferable only by
	// someone who already knows: a JSON consumer reconstructs the cycle count as
	// FiresCompleted+Outstanding, and cycles == 0 is the unmeasured case. The
	// human rendering omits the line entirely instead of printing a zero. The
	// same discipline as ackwatch's DeliveryMeasured, for the same reason — an
	// unmeasured window and a measured-and-clean one must never render alike.
	MeanGap float64 `json:"mean_gap"`
	// StallThreshold is the streak length at which a tracked schedule counts as
	// stalled, echoed back so the numbers are self-describing.
	StallThreshold int `json:"stall_threshold"`
}

// DefaultStallThreshold is how many consecutive unacked fires a tracked
// schedule may accumulate before it is reported stalled. Two, not one: a single
// unacked fire is routinely just a long turn still in progress, whereas a
// schedule that has missed two consecutive fires has had a full cron period to
// answer and did not.
const DefaultStallThreshold = 2

// Completion rolls up completion state across schedules, optionally filtered to
// one agent. threshold <= 0 uses DefaultStallThreshold.
func (s *Scheduler) Completion(agentName string, threshold int) CompletionStats {
	if threshold <= 0 {
		threshold = DefaultStallThreshold
	}
	stats := CompletionStats{StallThreshold: threshold}
	for _, e := range s.List(agentName) {
		stats.Schedules++
		if !e.CompletionTracked() {
			continue
		}
		stats.Tracked++
		if e.FiresCompleted == 0 {
			stats.TrackedReset++
		}
		stats.FiresDelivered += e.FiresDelivered
		stats.FiresCompleted += e.FiresCompleted
		if e.Outstanding() {
			stats.Outstanding++
		}
		if e.UnackedStreak >= threshold {
			stats.Stalled++
		}
	}
	if stats.FiresDelivered > 0 {
		stats.Ratio = float64(stats.FiresCompleted) / float64(stats.FiresDelivered)
		if cycles := stats.FiresCompleted + stats.Outstanding; cycles > 0 {
			stats.MeanGap = float64(stats.FiresDelivered) / float64(cycles)
		}
	}
	return stats
}
