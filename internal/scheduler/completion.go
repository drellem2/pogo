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
	entry.UnackedStreak = 0
	entry.LastCompletion = now
	entry.PendingToken = ""
	entry.PendingSince = time.Time{}
	done := entry.Clone()
	_ = s.persistLocked()
	s.mu.Unlock()

	s.emitCompletionEvent(done, now, token, latency)

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
	Tracked int `json:"tracked"`
	// Stalled is how many TRACKED schedules currently have an unacked streak
	// at or above StallThreshold.
	Stalled int `json:"stalled"`
	// FiresDelivered / FiresCompleted are lifetime sums over tracked entries.
	FiresDelivered int `json:"fires_delivered"`
	FiresCompleted int `json:"fires_completed"`
	// Ratio is FiresCompleted/FiresDelivered over tracked entries, or 0 when
	// nothing has been delivered.
	Ratio float64 `json:"ratio"`
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
		stats.FiresDelivered += e.FiresDelivered
		stats.FiresCompleted += e.FiresCompleted
		if e.UnackedStreak >= threshold {
			stats.Stalled++
		}
	}
	if stats.FiresDelivered > 0 {
		stats.Ratio = float64(stats.FiresCompleted) / float64(stats.FiresDelivered)
	}
	return stats
}
