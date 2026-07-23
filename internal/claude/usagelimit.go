// Fleet usage-limit episode coordinator (gh #45).
//
// The modal watcher runs one goroutine per agent, so a fleet-wide usage-limit
// event (the provider's weekly limit hits every agent at once) would produce
// one operator mail per agent — an N-agent notification storm. pm-pogo's
// adjustment: coalesce to ONE mail per fleet-wide episode at hit and ONE at
// clear.
//
// An "episode" opens when the first agent is reported rate-limited and closes
// when the last rate-limited agent clears. Agents that join a live episode are
// added to the roster silently. The hit mail fires on episode open; the clear
// mail fires on episode close and carries a resume checklist naming every agent
// that was limited during the episode, its work item, and a suggested recovery
// command. Both mails go to `human`, riding the existing notify bridge (pogo
// adds no new notifier).
package claude

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/events"
)

// UsageLimitEpisodeClearedEvent is the event_type emitted to events.log at every
// coordinator episode close (mg-8d04). It is the structured per-episode roster +
// window that the pogo-reminders notifier (mg-e0f6) consumes to coalesce
// usage-limit incident self-reports WITHOUT reconstructing coordinator semantics
// (the flap-gate, the release-not-recovery close) from raw per-agent atoms — a
// reconstruction that is unsafe because the roster and open/close window live
// only in this coordinator's memory and are otherwise rendered only as prose into
// the clear mail. The two sides meet at this contract: the details shape
// (episode_id, roster, opened_at, closed_at) is fixed — do not change field names
// or nesting without updating mg-e0f6.
const UsageLimitEpisodeClearedEvent = "usage_limit_episode_cleared"

// DefaultUsageLimitHoldDown is how long a fleet-wide usage-limit episode must
// stay open before its hit mail fires. It exists to suppress sub-second flaps
// (mg-4904): on 07-22 the modal watcher observed three usage-limit hit/clear
// pairs that each opened and resolved within ~1s, and each correctly emitted a
// hit+clear pair to `human` — six bedtime mails for three episodes a human
// never needed to know about. The coalescer already emits at most one hit and
// one clear per episode; the missing piece was a minimum-duration gate so an
// episode that never outlives a flap pages nobody.
//
// N=45s is chosen against the two measured extremes. The flaps resolved in ~1s,
// so any gate comfortably above that catches them; 45s gives ~45x margin, so a
// flap would have to last 45x longer than anything observed to leak through.
// The genuine episode this file was written for (the provider weekly limit)
// lasted ~23h, so delaying its single page by 45s is ~0.05% of the episode —
// imperceptible. Anything in 30-60s is defensible against these numbers; 45s
// sits in the middle. Too short and real flaps leak; too long and a genuine
// page is needlessly delayed.
const DefaultUsageLimitHoldDown = 45 * time.Second

// agentLimitInfo is one agent's membership in a usage-limit episode.
type agentLimitInfo struct {
	agentID    string
	workItemID string
	since      time.Time
}

// stoppableTimer is the subset of *time.Timer the coordinator needs from its
// hold-down timer. Injecting it (rather than calling time.AfterFunc directly)
// lets tests drive the hold-down deterministically without real sleeps.
type stoppableTimer interface {
	// Stop cancels the timer, reporting whether it was stopped before firing.
	Stop() bool
}

// usageLimitCoordinator coalesces per-agent hit/clear signals into one mail per
// fleet-wide episode. All state is guarded by mu; the send call is made outside
// the lock so a slow mail write never blocks a modal-watcher goroutine holding
// the fleet lock.
type usageLimitCoordinator struct {
	mu     sync.Mutex
	active map[string]agentLimitInfo // currently rate-limited agents
	roster map[string]agentLimitInfo // every agent limited during the open episode
	send   func(to, from, subject, body string) error
	now    func() time.Time
	emit   func(ev events.Event) // structured event sink (events.Emit in production)

	// Episode identity + window, set when a new episode opens (OnHit) and cleared
	// when it closes (OnClear). episodeID is a stable per-episode id; openedAt is
	// the first agent's hit time (the episode window start). Both are carried into
	// the usage_limit_episode_cleared event at close.
	episodeID string
	openedAt  time.Time

	// Hold-down state (mg-4904). When an episode opens, the hit mail is not sent
	// immediately: a timer is armed for holdDown, and the mail fires only if the
	// episode is still open when it elapses. A flap that clears first cancels the
	// timer and sends neither mail. after builds the timer (time.AfterFunc in
	// production, a test double otherwise); timer is the armed timer for the open
	// episode (nil when no episode is open); opener is the info the hit mail
	// names; hitSent records whether the current episode's hit mail has fired,
	// which gates whether its clear mail may fire.
	holdDown time.Duration
	after    func(d time.Duration, f func()) stoppableTimer
	timer    stoppableTimer
	opener   agentLimitInfo
	hitSent  bool
}

func newUsageLimitCoordinator(send func(to, from, subject, body string) error, now func() time.Time) *usageLimitCoordinator {
	return newUsageLimitCoordinatorWithHoldDown(send, now, DefaultUsageLimitHoldDown, nil, nil)
}

// newUsageLimitCoordinatorWithHoldDown is the seam for tests: it takes an
// explicit hold-down, an injectable timer factory, and an injectable structured
// event sink. A nil after uses time.AfterFunc; a nil emit uses events.Emit.
// Production goes through newUsageLimitCoordinator.
func newUsageLimitCoordinatorWithHoldDown(send func(to, from, subject, body string) error, now func() time.Time, holdDown time.Duration, after func(d time.Duration, f func()) stoppableTimer, emit func(ev events.Event)) *usageLimitCoordinator {
	if now == nil {
		now = time.Now
	}
	if after == nil {
		after = func(d time.Duration, f func()) stoppableTimer { return time.AfterFunc(d, f) }
	}
	if emit == nil {
		emit = func(ev events.Event) { events.Emit(context.Background(), ev) }
	}
	return &usageLimitCoordinator{
		active:   map[string]agentLimitInfo{},
		roster:   map[string]agentLimitInfo{},
		send:     send,
		now:      now,
		emit:     emit,
		holdDown: holdDown,
		after:    after,
	}
}

// OnHit records agentID as rate-limited. If this opens a new episode (the
// active set was empty), it arms a hold-down timer rather than paging
// immediately; the coalesced hit mail fires only if the episode is still open
// when the hold-down elapses (see fireHoldDown). Re-reporting an already-active
// agent is a no-op, so the modal watcher can call this on every gate
// evaluation.
func (c *usageLimitCoordinator) OnHit(agentID, workItemID string, when time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.active[agentID]; ok {
		return
	}
	info := agentLimitInfo{agentID: agentID, workItemID: workItemID, since: when}
	newEpisode := len(c.active) == 0
	c.active[agentID] = info
	c.roster[agentID] = info

	if newEpisode {
		// Do not page yet: arm the hold-down. fireHoldDown sends the hit mail
		// only if the episode is still open when it elapses; a flap that clears
		// first stops this timer in OnClear and pages nobody. Agents joining a
		// live episode land in the `ok` early-return above and never re-arm.
		c.opener = info
		c.hitSent = false
		c.openedAt = when
		c.episodeID = makeEpisodeID(info.agentID, when)
		c.timer = c.after(c.holdDown, c.fireHoldDown)
	}
}

// fireHoldDown runs when an episode's hold-down elapses. If the episode is still
// open it sends the coalesced hit mail; if it closed first (a flap) the callback
// is stale and sends nothing. It is also the OnClear/timer race arbiter: OnClear
// stopping the timer may lose the race with a fire already in flight, so this
// guard — episode gone or hit already sent — is what makes the flap emit zero
// mails even when Stop() returns false.
func (c *usageLimitCoordinator) fireHoldDown() {
	c.mu.Lock()
	if len(c.active) == 0 || c.hitSent {
		c.mu.Unlock()
		return
	}
	c.hitSent = true
	c.timer = nil
	info := c.opener
	send := c.send
	c.mu.Unlock()

	if send != nil {
		subject, body := hitMail(info)
		if err := send("human", "pogod", subject, body); err != nil {
			log.Printf("usage-limit: failed to send hit mail: %v", err)
		}
	}
}

// OnClear removes agentID from the active set. When that empties the set the
// episode closes. If the episode's hit mail already fired (a genuine episode
// that outlived the hold-down), it snapshots and resets the roster and sends
// the coalesced clear mail with the resume checklist. If the hit mail never
// fired (a flap that closed inside the hold-down), it cancels the pending timer
// and sends nothing at all. Clearing an agent that isn't active is a no-op.
func (c *usageLimitCoordinator) OnClear(agentID string, when time.Time) {
	c.mu.Lock()
	if _, ok := c.active[agentID]; !ok {
		c.mu.Unlock()
		return
	}
	delete(c.active, agentID)
	if len(c.active) > 0 {
		c.mu.Unlock()
		return
	}

	// Episode closed. Stop the hold-down timer either way; if it already fired
	// (or is firing), fireHoldDown's own guard handles the race.
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}

	// A flap: the episode closed before its hit mail ever fired. To the
	// coordinator this hit/clear pair was a NON-episode, so it emits neither the
	// clear mail nor the structured episode event — an orphan "cleared" for an
	// episode nobody was told about would be worse than silence. Reset for the
	// next episode.
	if !c.hitSent {
		c.roster = map[string]agentLimitInfo{}
		c.episodeID = ""
		c.openedAt = time.Time{}
		c.mu.Unlock()
		return
	}

	// A genuine episode close by ANY path — normal recovery OR the
	// release-not-recovery case where the last flagged agent exited (modal_hook's
	// ctx.Done calls OnUsageLimitClear without emitting a per-agent
	// usage_limit_cleared atom). Both funnel here, so emitting the structured
	// episode event from this single close point is exactly what makes the
	// notifier see EVERY episode close, including the one that leaves no clear
	// atom behind (mg-8d04 / mg-e0f6).
	roster := make([]agentLimitInfo, 0, len(c.roster))
	for _, v := range c.roster {
		roster = append(roster, v)
	}
	episodeID := c.episodeID
	openedAt := c.openedAt
	c.roster = map[string]agentLimitInfo{}
	c.hitSent = false
	c.episodeID = ""
	c.openedAt = time.Time{}
	send := c.send
	emit := c.emit
	c.mu.Unlock()

	if emit != nil {
		emit(episodeClearedEvent(episodeID, roster, openedAt, when))
	}
	if send != nil {
		subject, body := clearMail(roster, when)
		if err := send("human", "pogod", subject, body); err != nil {
			log.Printf("usage-limit: failed to send clear mail: %v", err)
		}
	}
}

// makeEpisodeID builds a stable per-episode id from the opening agent and the
// episode's open time. Episodes are sequential (a new one opens only after the
// prior one fully closed), so (firstAgent, openedAt) is unique; deriving it from
// the injected clock keeps it deterministic under test.
func makeEpisodeID(firstAgent string, openedAt time.Time) string {
	return fmt.Sprintf("ep-%d-%s", openedAt.UTC().UnixNano(), firstAgent)
}

// episodeClearedEvent builds the structured usage_limit_episode_cleared event for
// a coordinator episode close. The roster is emitted sorted by agent id so the
// on-disk record is deterministic. The details shape is the mg-e0f6 contract.
func episodeClearedEvent(episodeID string, roster []agentLimitInfo, openedAt, closedAt time.Time) events.Event {
	ids := make([]string, 0, len(roster))
	for _, a := range roster {
		ids = append(ids, a.agentID)
	}
	sort.Strings(ids)
	return events.Event{
		EventType: UsageLimitEpisodeClearedEvent,
		Agent:     "pogod",
		Timestamp: closedAt.UTC().Format(time.RFC3339Nano),
		Details: map[string]any{
			"episode_id": episodeID,
			"roster":     ids,
			"opened_at":  openedAt.UTC().Format(time.RFC3339Nano),
			"closed_at":  closedAt.UTC().Format(time.RFC3339Nano),
		},
	}
}

// hitMail builds the episode-open mail. It names the first affected agent and
// explains that more agents may join the same episode silently; the full roster
// and resume steps arrive in the clear mail.
func hitMail(first agentLimitInfo) (subject, body string) {
	subject = "usage limit hit — fleet episode started"
	var b strings.Builder
	fmt.Fprintf(&b, "pogod's modal watcher detected a suspected provider usage-limit hit.\n\n")
	fmt.Fprintf(&b, "First affected agent: %s%s\n", first.agentID, workItemClause(first.workItemID))
	fmt.Fprintf(&b, "Detected at: %s\n\n", first.since.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Affected agents are alive but wedged on the rate-limit modal; they now\n")
	fmt.Fprintf(&b, "report health=rate_limited in `pogo status` and `pogo agent diagnose`\n")
	fmt.Fprintf(&b, "rather than being mistaken for stalled. To avoid a notification storm,\n")
	fmt.Fprintf(&b, "additional agents that hit the same limit join this episode silently —\n")
	fmt.Fprintf(&b, "you'll get ONE follow-up mail with a full resume checklist when the\n")
	fmt.Fprintf(&b, "episode clears (the limit resets and agents resume producing events).\n\n")
	fmt.Fprintf(&b, "No action is required now. See docs/operations.md → \"Recovering from a\n")
	fmt.Fprintf(&b, "usage-limit episode\".\n")
	return subject, b.String()
}

// clearMail builds the episode-close mail with a per-agent resume checklist.
func clearMail(roster []agentLimitInfo, when time.Time) (subject, body string) {
	sort.Slice(roster, func(i, j int) bool { return roster[i].agentID < roster[j].agentID })

	subject = fmt.Sprintf("usage limit cleared — %d agent(s) recovered", len(roster))
	var b strings.Builder
	fmt.Fprintf(&b, "The provider usage-limit episode has cleared as of %s.\n\n", when.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "%d agent(s) were rate-limited during this episode. Each has resumed\n", len(roster))
	fmt.Fprintf(&b, "producing events (or exited). Resume checklist:\n\n")
	for _, a := range roster {
		name := agentNameFromID(a.agentID)
		fmt.Fprintf(&b, "- %s%s\n", a.agentID, workItemClause(a.workItemID))
		fmt.Fprintf(&b, "    verify: pogo agent diagnose %s\n", name)
		fmt.Fprintf(&b, "    if idle: pogo nudge %s \"usage limit reset — resume your task\"\n", name)
		fmt.Fprintf(&b, "    if exited: pogo agent start %s\n", name)
	}
	fmt.Fprintf(&b, "\nSee docs/operations.md → \"Recovering from a usage-limit episode\".\n")
	return subject, b.String()
}

func workItemClause(workItemID string) string {
	if workItemID == "" {
		return ""
	}
	return " (work item " + workItemID + ")"
}

// agentNameFromID strips the identity prefix (cat-/crew-) from an event-log
// agent identity to recover the bare agent name that `pogo` verbs take.
func agentNameFromID(id string) string {
	for _, p := range []string{"cat-", "crew-"} {
		if strings.HasPrefix(id, p) {
			return strings.TrimPrefix(id, p)
		}
	}
	return id
}

var (
	usageLimitCoordOnce sync.Once
	usageLimitCoord     *usageLimitCoordinator
)

// defaultUsageLimitCoordinator returns the process-wide singleton, lazily
// wired to client.SendMGMail. Lazy so pure-library callers don't construct it.
func defaultUsageLimitCoordinator() *usageLimitCoordinator {
	usageLimitCoordOnce.Do(func() {
		usageLimitCoord = newUsageLimitCoordinator(client.SendMGMail, time.Now)
	})
	return usageLimitCoord
}
