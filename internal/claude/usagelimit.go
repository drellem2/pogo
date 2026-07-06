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
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/client"
)

// agentLimitInfo is one agent's membership in a usage-limit episode.
type agentLimitInfo struct {
	agentID    string
	workItemID string
	since      time.Time
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
}

func newUsageLimitCoordinator(send func(to, from, subject, body string) error, now func() time.Time) *usageLimitCoordinator {
	if now == nil {
		now = time.Now
	}
	return &usageLimitCoordinator{
		active: map[string]agentLimitInfo{},
		roster: map[string]agentLimitInfo{},
		send:   send,
		now:    now,
	}
}

// OnHit records agentID as rate-limited. If this opens a new episode (the
// active set was empty), it sends the coalesced hit mail. Re-reporting an
// already-active agent is a no-op, so the modal watcher can call this on every
// gate evaluation.
func (c *usageLimitCoordinator) OnHit(agentID, workItemID string, when time.Time) {
	c.mu.Lock()
	if _, ok := c.active[agentID]; ok {
		c.mu.Unlock()
		return
	}
	info := agentLimitInfo{agentID: agentID, workItemID: workItemID, since: when}
	newEpisode := len(c.active) == 0
	c.active[agentID] = info
	c.roster[agentID] = info
	send := c.send
	c.mu.Unlock()

	if newEpisode && send != nil {
		subject, body := hitMail(info)
		if err := send("human", "pogod", subject, body); err != nil {
			log.Printf("usage-limit: failed to send hit mail: %v", err)
		}
	}
}

// OnClear removes agentID from the active set. When that empties the set the
// episode closes: it snapshots and resets the roster and sends the coalesced
// clear mail with the resume checklist. Clearing an agent that isn't active is
// a no-op.
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
	roster := make([]agentLimitInfo, 0, len(c.roster))
	for _, v := range c.roster {
		roster = append(roster, v)
	}
	c.roster = map[string]agentLimitInfo{}
	send := c.send
	c.mu.Unlock()

	if send != nil {
		subject, body := clearMail(roster, when)
		if err := send("human", "pogod", subject, body); err != nil {
			log.Printf("usage-limit: failed to send clear mail: %v", err)
		}
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
