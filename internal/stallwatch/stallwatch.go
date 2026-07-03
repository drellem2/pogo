// Package stallwatch implements pogod's passive work-pile-up watcher — the
// third leg of the wedge-response triad described in gh drellem2/macguffin #12.
//
// The watcher rides pogod's existing heartbeat loop (see internal/heartbeat):
// on each tick it samples macguffin state and nudges the watched agent (the
// mayor) when work has piled up behaviorally even though the agent's process is
// healthy:
//
//   - Threshold A — unclaimed items: an `available` work item assigned to (or
//     unassigned and pickup-expected by) the watched agent has sat in the
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
)

// Nudger delivers a short message to an agent. pogod injects an implementation
// that nudges the agent's PTY when it is running and falls back to macguffin
// mail otherwise (mirroring the scheduler's PogodDeliverer).
type Nudger func(agent, message string) error

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
}

// Watcher samples macguffin state and nudges the watched agent on stall.
type Watcher struct {
	cfg      config.StallWatchConfig
	workRoot string
	mailRoot string
	nudge    Nudger
	emit     Emitter

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

	var stale []workitem.WorkItem
	for _, it := range items {
		if !w.assignedToWatched(it.Assignee) {
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

	if !w.tryFire(categoryUnclaimedItems, now) {
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

	if !w.tryFire(categoryUnreadMail, now) {
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

// assignedToWatched reports whether an available item is the watched agent's
// responsibility: explicitly assigned to it, or unassigned (the mayor is the
// expected dispatcher of unclaimed work).
func (w *Watcher) assignedToWatched(assignee string) bool {
	a := strings.TrimSpace(assignee)
	return a == "" || a == w.cfg.Agent
}

// tryFire enforces the per-category cooldown. It returns true and records the
// fire time when a nudge is allowed, false when the category is still cooling
// down. Recording before the nudge attempt means a failed delivery still
// counts toward the cooldown — better to retry next cooldown window than to
// hammer a wedged recipient every tick.
func (w *Watcher) tryFire(category string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, ok := w.lastNudge[category]; ok && now.Sub(last) < w.cfg.NudgeCooldown {
		return false
	}
	w.lastNudge[category] = now
	return true
}

// fire delivers the nudge and appends the stall_watch_fired event. A nudge
// error is recorded in the event details rather than dropped, so the event log
// still records that a threshold crossed even when delivery failed.
func (w *Watcher) fire(category, message string, details map[string]any) {
	if err := w.nudge(w.cfg.Agent, message); err != nil {
		details["nudge_error"] = err.Error()
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
