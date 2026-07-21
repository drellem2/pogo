// Package stallwatch implements pogod's passive work-pile-up watcher — the
// third leg of the wedge-response triad described in gh drellem2/macguffin #12.
//
// The watcher rides pogod's existing heartbeat loop (see internal/heartbeat):
// on each tick it samples macguffin state and nudges the watched agent (the
// mayor) when work has piled up behaviorally even though the agent's process is
// healthy:
//
//   - Threshold A — unclaimed items: an `available` work item awaiting the
//     watched agent's dispatch — anything not gated to a non-dispatchable
//     executor, whoever OWNS it (see watchedForDispatch) — has sat in the
//     available/ queue longer than UnclaimedItemAgeThreshold.
//   - Threshold B — unread mail: the watched agent's new/ maildir holds a
//     message older than UnreadMailAgeThreshold, or more than
//     MaxUnreadMailCount messages have accumulated.
//
// On a threshold cross the watcher fires one nudge per offending batch and
// appends a `stall_watch_fired` event to ~/.pogo/events.log. A per-category
// cooldown keeps a persistent backlog from producing one nudge per tick.
//
// Why pogod rather than an Ocean-side watcher: if the mayor's loop is dropping
// its own check-work / check-mail steps (prompt drift, LLM cycle-skip), a
// watcher living in that same loop can't catch it — watcher and watched drift
// together. pogod's heartbeat is the only watcher with a guaranteed-independent
// cadence. See docs/design/stall-watch-design.md.
package stallwatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/workitem"
)

// Category labels identify the two threshold checks. They key the per-category
// cooldown and are stamped into the emitted event's details.
const (
	categoryUnclaimedItems = "unclaimed_items"
	categoryUnreadMail     = "unread_mail"
	// categoryPriorityWake keys the priority-wake cooldown and stamps the
	// emitted event. It is deliberately distinct from categoryUnclaimedItems so
	// the fast wake and the standard 10-min stall nudge cool down independently.
	categoryPriorityWake = "priority_wake"
)

// Delivery reports how a nudge reached its recipient. It is returned alongside
// the error so a fire can record WHICH channel carried the message, not merely
// that something worked — a nudge that arrived by the slow durable channel is a
// different event from one that landed on the PTY, and the difference is the
// only way to measure the PTY channel's failure rate without parsing error
// strings out of a log.
type Delivery struct {
	// Channel names the channel that carried the message: DeliveryPTY,
	// DeliveryMail, or DeliveryMailFallback. Empty when delivery failed
	// outright.
	Channel string
	// FallbackReason, when non-empty, records why the preferred PTY channel
	// was not used. Delivery still SUCCEEDED — this is not an error, it is the
	// reason the message took the durable road instead.
	FallbackReason string
}

// Channel values for Delivery.Channel.
const (
	// DeliveryPTY means the message was written to the agent's live terminal.
	DeliveryPTY = "pty"
	// DeliveryMail means the recipient was not running, so the message went
	// straight to durable macguffin mail.
	DeliveryMail = "mail"
	// DeliveryMailFallback means the recipient WAS running but the PTY nudge
	// failed (typically: too busy to ever go idle), so the message went to
	// durable mail instead. This is the load-bearing case for mg-79dc — see
	// newStallNudger in cmd/pogod.
	DeliveryMailFallback = "mail_fallback"
)

// Nudger delivers a short message to an agent. pogod injects an implementation
// that nudges the agent's PTY when it is running and falls back to macguffin
// mail both when the agent is offline AND when the PTY nudge fails, so a busy
// recipient still hears the notice (mg-79dc).
type Nudger func(agent, message string) (Delivery, error)

// Emitter writes an event to the shared log. Defaults to events.Emit; tests
// substitute a recorder.
type Emitter func(events.Event)

// Options carries the watcher's external dependencies and filesystem roots so
// the package stays testable — tests point WorkRoot/MailRoot at temp dirs and
// pass recorder Nudge/Emit closures.
type Options struct {
	// WorkRoot is the macguffin work directory (default ~/.macguffin/work).
	WorkRoot string
	// MailRoot is the macguffin mail directory (default ~/.macguffin/mail).
	MailRoot string
	// Nudge delivers the nudge. Required.
	Nudge Nudger
	// Emit writes the stall_watch_fired event. Defaults to events.Emit.
	Emit Emitter
	// FastPoll, if set, requests an out-of-band heartbeat tick right after a
	// priority wake fires, so pogod re-checks the queue well before the next
	// ~30s poll instead of waiting a full interval. pogod wires this to
	// heartbeat.Detector.Nudge; tests leave it nil. Strictly optional — the
	// wake is correct without it. It cannot loop: it is only called on an
	// actual fire, and the priority cooldown suppresses the very next check,
	// so at most one extra tick follows each wake.
	FastPoll func()
}

// Watcher samples macguffin state and nudges the watched agent on stall.
type Watcher struct {
	cfg      config.StallWatchConfig
	workRoot string
	mailRoot string
	nudge    Nudger
	emit     Emitter
	fastPoll func()

	mu        sync.Mutex
	lastNudge map[string]time.Time
}

// New builds a Watcher from cfg and opts, applying defaults for any zero
// config value or unset option so a zero-value cfg is still usable.
func New(cfg config.StallWatchConfig, opts Options) *Watcher {
	if cfg.Agent == "" {
		cfg.Agent = config.DefaultStallWatchAgent
	}
	if cfg.UnclaimedItemAgeThreshold <= 0 {
		cfg.UnclaimedItemAgeThreshold = config.DefaultUnclaimedItemAgeThreshold
	}
	if cfg.UnreadMailAgeThreshold <= 0 {
		cfg.UnreadMailAgeThreshold = config.DefaultUnreadMailAgeThreshold
	}
	if cfg.MaxUnreadMailCount <= 0 {
		cfg.MaxUnreadMailCount = config.DefaultMaxUnreadMailCount
	}
	if cfg.NudgeCooldown <= 0 {
		cfg.NudgeCooldown = config.DefaultStallNudgeCooldown
	}
	// Priority-wake defaults. PriorityWakeEnabled is intentionally NOT defaulted
	// here: New() cannot tell an unset bool from an explicit false, so the
	// production default (true) is set by config.Load(); a zero-value config
	// leaves the wake off. The numeric/slice knobs still default so a config
	// that enables the wake without tuning it is usable.
	if cfg.HighPriorityWakeDelay <= 0 {
		cfg.HighPriorityWakeDelay = config.DefaultHighPriorityWakeDelay
	}
	if cfg.HighPriorityWakeCooldown <= 0 {
		cfg.HighPriorityWakeCooldown = config.DefaultHighPriorityWakeCooldown
	}
	if len(cfg.FastPriorities) == 0 {
		cfg.FastPriorities = config.DefaultFastPriorities
	}
	if len(cfg.NonDispatchableAssignees) == 0 {
		cfg.NonDispatchableAssignees = config.DefaultNonDispatchableAssignees
	}

	workRoot := opts.WorkRoot
	mailRoot := opts.MailRoot
	if workRoot == "" || mailRoot == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if workRoot == "" {
				workRoot = filepath.Join(home, ".macguffin", "work")
			}
			if mailRoot == "" {
				mailRoot = filepath.Join(home, ".macguffin", "mail")
			}
		}
	}

	emit := opts.Emit
	if emit == nil {
		emit = func(e events.Event) { events.Emit(context.Background(), e) }
	}

	return &Watcher{
		cfg:       cfg,
		workRoot:  workRoot,
		mailRoot:  mailRoot,
		nudge:     opts.Nudge,
		emit:      emit,
		fastPoll:  opts.FastPoll,
		lastNudge: make(map[string]time.Time),
	}
}

// Check runs one stall-watch sample for the given wall-clock time. It is the
// integration point for the heartbeat OnTick callback. It is a no-op when the
// watcher is disabled or has no nudge delivery configured. Safe to call
// concurrently with itself, though pogod only ever calls it from the heartbeat
// goroutine.
func (w *Watcher) Check(now time.Time) {
	if w == nil || !w.cfg.Enabled || w.nudge == nil {
		return
	}
	w.checkUnclaimedItems(now)
	w.checkUnreadMail(now)
}

// checkUnclaimedItems fires when one or more available work items the watched
// agent is responsible for have aged past the threshold without being claimed.
// Only available/ is scanned — claimed/ and done/ are irrelevant here, and
// done/ grows unbounded, so walking it every 30s tick would get linearly more
// expensive over a long run.
func (w *Watcher) checkUnclaimedItems(now time.Time) {
	items, err := workitem.ListFrom(w.workRoot, "available")
	if err != nil {
		return
	}

	// Priority-aware fast wake (gh drellem2/pogo #61). A ready, watched,
	// high-priority available item bypasses the 10-min UnclaimedItemAgeThreshold
	// and is delivered promptly via the same wait-idle nudge, so urgent work no
	// longer waits out the idle-coordinator polling gap. This scans the same
	// listing, so it is nearly free.
	w.checkPriorityWake(now, items)

	// Standard unclaimed-item stall: an assigned available item aged past the
	// 10-min threshold. When the priority wake is active it OWNS fast-priority
	// items (they follow the short priority cooldown, not the 10-min gate), so
	// skip them here — otherwise a stuck high-priority item would draw a second,
	// slower nudge on top of the fast one. When the wake is disabled they fall
	// through to the standard gate exactly as before, so disabling the feature
	// never silences a high-priority item.
	var stale []workitem.WorkItem
	for _, it := range items {
		if !w.watchedForDispatch(it.Assignee) {
			continue
		}
		if w.cfg.PriorityWakeEnabled && w.isFastPriority(it.Priority) {
			continue
		}
		if it.ModTime.IsZero() {
			continue
		}
		if now.Sub(it.ModTime) >= w.cfg.UnclaimedItemAgeThreshold {
			stale = append(stale, it)
		}
	}
	if len(stale) == 0 {
		return
	}

	if !w.tryFire(categoryUnclaimedItems, now, w.cfg.NudgeCooldown) {
		return
	}

	sort.Slice(stale, func(i, j int) bool { return stale[i].ID < stale[j].ID })
	ids := make([]string, len(stale))
	for i, it := range stale {
		ids[i] = it.ID
	}

	msg := fmt.Sprintf(
		"stall-watch: %d available work item(s) have sat unclaimed for over %s — claim or dispatch them: %s",
		len(stale), w.cfg.UnclaimedItemAgeThreshold, strings.Join(ids, ", "))

	w.fire(categoryUnclaimedItems, msg, map[string]any{
		"category":           categoryUnclaimedItems,
		"watched_agent":      w.cfg.Agent,
		"item_count":         len(stale),
		"item_ids":           ids,
		"age_threshold":      w.cfg.UnclaimedItemAgeThreshold.String(),
		"oldest_age_seconds": now.Sub(oldestModTime(stale)).Seconds(),
	})
}

// checkPriorityWake delivers the priority-aware fast wake (gh drellem2/pogo
// #61). It fires when one or more ready, watched, high-priority items sit in
// available/ — bypassing the 10-min UnclaimedItemAgeThreshold in favor of the
// much shorter HighPriorityWakeDelay — and delivers via the same wait-idle
// nudge the standard checks use, so a BUSY agent is never interrupted (the
// nudge blocks until the agent's PTY goes quiet) and an idle agent is woken at
// once. items is the caller's already-listed available/ snapshot.
//
// Two structural properties keep it from loop-nudging a stuck item:
//
//   - Only available/ is listed. An item with unmet deps sits in pending/ and
//     an already-claimed item in claimed/, so neither is ever seen here — a
//     blocked or claimed high-priority item cannot trigger a wake at all.
//   - The dedicated HighPriorityWakeCooldown gates repeats, so a ready
//     high-priority item that simply stays available (e.g. the coordinator
//     can't dispatch it yet) draws at most one nudge per cooldown, not one per
//     heartbeat tick.
func (w *Watcher) checkPriorityWake(now time.Time, items []workitem.WorkItem) {
	if !w.cfg.PriorityWakeEnabled {
		return
	}

	var ready []workitem.WorkItem
	for _, it := range items {
		if !w.watchedForDispatch(it.Assignee) {
			continue
		}
		if !w.isFastPriority(it.Priority) {
			continue
		}
		if it.ModTime.IsZero() {
			continue
		}
		if now.Sub(it.ModTime) >= w.cfg.HighPriorityWakeDelay {
			ready = append(ready, it)
		}
	}
	if len(ready) == 0 {
		return
	}

	if !w.tryFire(categoryPriorityWake, now, w.cfg.HighPriorityWakeCooldown) {
		return
	}

	sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })
	ids := make([]string, len(ready))
	for i, it := range ready {
		ids[i] = it.ID
	}

	msg := fmt.Sprintf(
		"priority-wake: %d high-priority work item(s) are ready and unclaimed — claim or dispatch now: %s",
		len(ready), strings.Join(ids, ", "))

	w.fire(categoryPriorityWake, msg, map[string]any{
		"category":       categoryPriorityWake,
		"watched_agent":  w.cfg.Agent,
		"item_count":     len(ready),
		"item_ids":       ids,
		"wake_delay":     w.cfg.HighPriorityWakeDelay.String(),
		"wake_cooldown":  w.cfg.HighPriorityWakeCooldown.String(),
		"fast_priority":  strings.Join(w.cfg.FastPriorities, ","),
		"oldest_age_sec": now.Sub(oldestModTime(ready)).Seconds(),
	})

	// Collapse the ~30s poll for the follow-up check so pogod re-samples the
	// queue promptly (e.g. to notice the item got claimed, or that more urgent
	// work landed) instead of waiting a full interval. Safe from looping: the
	// priority cooldown suppresses the immediate re-check, so this yields at
	// most one extra tick per wake. See Options.FastPoll.
	if w.fastPoll != nil {
		w.fastPoll()
	}
}

// isFastPriority reports whether a work item's Priority value is in the
// configured fast-priority set (default ["high"]). The comparison is
// case-insensitive and whitespace-trimmed so a "High" or " high " frontmatter
// value still triggers the wake.
func (w *Watcher) isFastPriority(priority string) bool {
	p := strings.ToLower(strings.TrimSpace(priority))
	if p == "" {
		return false
	}
	for _, fp := range w.cfg.FastPriorities {
		if p == strings.ToLower(strings.TrimSpace(fp)) {
			return true
		}
	}
	return false
}

// checkUnreadMail fires when the watched agent's new/ maildir holds a message
// older than the age threshold, or has accumulated more than the count ceiling.
func (w *Watcher) checkUnreadMail(now time.Time) {
	newDir := filepath.Join(w.mailRoot, w.cfg.Agent, "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		// No maildir yet (agent never received mail) or unreadable — nothing
		// to watch. Both are benign.
		return
	}

	count := 0
	var oldestAge time.Duration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		count++
		info, err := e.Info()
		if err != nil {
			continue
		}
		if age := now.Sub(info.ModTime()); age > oldestAge {
			oldestAge = age
		}
	}
	if count == 0 {
		return
	}

	overCount := count > w.cfg.MaxUnreadMailCount
	overAge := oldestAge >= w.cfg.UnreadMailAgeThreshold
	if !overCount && !overAge {
		return
	}

	if !w.tryFire(categoryUnreadMail, now, w.cfg.NudgeCooldown) {
		return
	}

	var reason string
	switch {
	case overCount && overAge:
		reason = fmt.Sprintf("%d unread (over %d) and oldest is %s old (over %s)",
			count, w.cfg.MaxUnreadMailCount, oldestAge.Truncate(time.Second), w.cfg.UnreadMailAgeThreshold)
	case overCount:
		reason = fmt.Sprintf("%d unread message(s), over the limit of %d", count, w.cfg.MaxUnreadMailCount)
	default:
		reason = fmt.Sprintf("oldest unread message is %s old, over %s", oldestAge.Truncate(time.Second), w.cfg.UnreadMailAgeThreshold)
	}

	msg := fmt.Sprintf("stall-watch: unread mail piling up — %s. Check your mail and process it.", reason)

	w.fire(categoryUnreadMail, msg, map[string]any{
		"category":           categoryUnreadMail,
		"watched_agent":      w.cfg.Agent,
		"unread_count":       count,
		"max_count":          w.cfg.MaxUnreadMailCount,
		"oldest_age_seconds": oldestAge.Seconds(),
		"age_threshold":      w.cfg.UnreadMailAgeThreshold.String(),
		"over_count":         overCount,
		"over_age":           overAge,
	})
}

// watchedForDispatch reports whether an available item is the watched agent's
// dispatch responsibility.
//
// The Assignee field carries two incompatible meanings, and this predicate
// exists to separate them:
//
//   - OWNERSHIP — "pm-pogo owns this ticket". The owner is who to ask about the
//     item, NOT who executes it; the coordinator still dispatches a worker. This
//     is the overwhelming majority of the queue (13 of 14 items on 2026-07-17),
//     because pm-template files every ticket with `--assignee=pm-<name>`.
//   - EXECUTION GATE — "do not dispatch this automatically". Two sentinels
//     carry it, for different reasons: `human` ("a person must do this by
//     hand" — mayor.md files manual-QA items that way so a worker is never
//     dispatched at them) and `parked` ("deliberately set aside; nobody is
//     expected to act on it now", mg-a3a2). They are separate values because a
//     single one would force every parked item to falsely claim a human owner,
//     and no consumer downstream could tell the two apart afterwards. See
//     config.DefaultNonDispatchableAssignees.
//
// So the test is NOT "is this assigned to the coordinator?" — it is "is this
// gated away from automatic dispatch?". Everything that is not gated is watched,
// whoever owns it.
//
// This inverts the original predicate (`a == "" || a == w.cfg.Agent`), which
// allowlisted the two assignee values a DISPATCHER would carry and therefore
// skipped every item that named an OWNER. It was not silent — unassigned items
// fired routinely, which is why it held confidence — but it could only ever see
// the shrinking population of unassigned work (mg-4bd4).
//
// The failure directions are deliberately asymmetric. An unrecognized assignee
// is watched, so a newly-added agent's work is visible on day one; the cost of
// guessing wrong is a nudge about an item the coordinator cannot dispatch, which
// is loud and self-correcting. The old default guessed the other way and paid in
// silence, which is indistinguishable from a healthy queue.
func (w *Watcher) watchedForDispatch(assignee string) bool {
	return !w.isDispatchGated(assignee)
}

// isDispatchGated reports whether an assignee names a non-dispatchable executor
// (default: "human", "parked"). Matching is case-insensitive and whitespace-trimmed, so a
// "Human" or " human " frontmatter value still gates, mirroring isFastPriority.
func (w *Watcher) isDispatchGated(assignee string) bool {
	a := strings.ToLower(strings.TrimSpace(assignee))
	if a == "" {
		return false
	}
	for _, g := range w.cfg.NonDispatchableAssignees {
		if a == strings.ToLower(strings.TrimSpace(g)) {
			return true
		}
	}
	return false
}

// tryFire enforces a per-category cooldown. It returns true and records the
// fire time when a nudge is allowed, false when the category is still cooling
// down. The caller passes the cooldown so each category can have its own — the
// priority wake recovers faster (HighPriorityWakeCooldown) than the standard
// stall categories (NudgeCooldown).
//
// Recording before the nudge attempt means a failed delivery still counts
// toward the cooldown, so a wedged recipient is not hammered every tick. Be
// precise about what that costs, because mg-79dc's ticket asked and the
// answer is not what the cooldown's existence suggests: THERE IS NO RETRY. A
// failed nudge is not queued and never re-sent. What happens after the
// cooldown is that the CONDITION is sampled again from scratch — and only if
// it still holds does a fresh message get composed. So a stall that resolves
// inside the cooldown window takes its undelivered notice with it, silently,
// and a stall that resolves-then-recurs reports the recurrence as if it were
// the first (the original is neither re-sent nor referenced).
//
// This is why delivery must succeed on the FIRST attempt rather than lean on
// the cooldown as a safety net: the cooldown is a rate limiter, not a retry
// queue, and treating it as one is what left ~38% of a day's fires unheard.
// The mail fallback in newStallNudger is that first-attempt guarantee.
func (w *Watcher) tryFire(category string, now time.Time, cooldown time.Duration) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, ok := w.lastNudge[category]; ok && now.Sub(last) < cooldown {
		return false
	}
	w.lastNudge[category] = now
	return true
}

// fire delivers the nudge and appends the stall_watch_fired event.
//
// The event records the delivery CHANNEL, not just the failure. Recording only
// the error was how mg-79dc stayed invisible: ~38% of a day's fires carried a
// `nudge_error` and nothing else changed, so the fires looked identical to
// successful ones from every vantage point except a hand-read of the log.
// Stamping the channel makes "this notice took the slow road" a first-class,
// countable fact.
//
// A nudge error is still recorded rather than dropped, so the log shows that a
// threshold crossed even when BOTH channels failed. But note what a
// nudge_error now means: with the mail fallback in place (see newStallNudger),
// an error here is a HARD failure — every channel was tried and none carried
// the message. It is no longer the routine busy-agent case.
func (w *Watcher) fire(category, message string, details map[string]any) {
	delivery, err := w.nudge(w.cfg.Agent, message)
	if err != nil {
		details["nudge_error"] = err.Error()
	}
	if delivery.Channel != "" {
		details["nudge_delivery"] = delivery.Channel
	}
	if delivery.FallbackReason != "" {
		details["nudge_fallback_reason"] = delivery.FallbackReason
	}
	w.emit(events.Event{
		EventType: "stall_watch_fired",
		Agent:     "pogod",
		Details:   details,
	})
}

// oldestModTime returns the earliest ModTime in items. items is non-empty at
// every call site.
func oldestModTime(items []workitem.WorkItem) time.Time {
	oldest := items[0].ModTime
	for _, it := range items[1:] {
		if it.ModTime.Before(oldest) {
			oldest = it.ModTime
		}
	}
	return oldest
}
