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
// Neither dispatch threshold trusts `available` on its own since mg-1a8a. A
// claim taken at spawn can fail open, leaving an item in available/ with a
// polecat already working it, so both checks first ask the Workers probe whether
// a live worker holds the item — and the ones that do are re-reported by
// checkWorkedButUnclaimed with the opposite remedy rather than silenced. See
// inflight.go.
//
// On a threshold cross the watcher fires one nudge per offending batch and
// appends a `stall_watch_fired` event to ~/.pogo/events.log.
//
// Repeat suppression is keyed PER ITEM, not per category, and escalates. The
// first notice about an item is immediate; each repeat about that same item
// waits twice as long as the last, up to RepeatBackoffCap. A tick where every
// offending item is still inside its own backoff produces no nudge at all.
//
// The per-item keying is the load-bearing part. Keyed per category (as it was
// until mg-1693), the cooldown suppressed repeats of a KIND of alert rather
// than repeats about a given item, which broke it in both directions: an item
// the coordinator was deliberately holding — behind a concurrency cap, a
// snooze, a sequencing call — was re-detected and re-notified every cooldown
// forever (mg-61f4 drew 22 notices in one night, mg-0e24 27), while a genuinely
// new item arriving mid-cooldown was silently swallowed by the held item's
// timer. Detection was correct in every one of those fires; only the
// repeat-suppressor was wrong. Note what that looks like from outside: a
// correct detector with a broken repeat-suppressor is indistinguishable from an
// over-firing detector unless you count per item — so the fix must not be to
// make detection stricter, and the emitted event now carries per-item repeat
// counts so the distinction stays countable.
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

// Category labels identify the three threshold checks. They form the first half
// of the cooldown key — paired with an item id for the two work-item categories,
// used alone for mail — and are stamped into the emitted event's details.
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
	// DeliveryMail, DeliveryMailFallback, or DeliverySuppressed (nothing was
	// carried, by policy). Empty when delivery failed outright.
	Channel string
	// FallbackReason, when non-empty, records why the preferred PTY channel
	// was not used. Delivery still SUCCEEDED — this is not an error, it is the
	// reason the message took the durable road instead. On DeliverySuppressed
	// it carries the PTY error that WOULD have caused a fallback, so a
	// suppressed fire still records why the terminal refused it.
	FallbackReason string
	// SuppressedConsecutive is the number of consecutive mail fallbacks to this
	// recipient — including the suppressed ones — since the last time the PTY
	// carried a message to them. It is zero on every channel except
	// DeliverySuppressed, where it is the damping term's state: a value that
	// keeps climbing across fires is a coordinator that has not gone idle once
	// in that whole span, which is the condition worth escalating on.
	SuppressedConsecutive int
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
	// DeliverySuppressed means the PTY refused the message AND the mail
	// fallback was withheld on purpose, because this recipient already has a
	// capful of stall-watch notices that pogod has never had evidence they
	// could receive. Nothing was delivered — but unlike an empty Channel this
	// is a decision, not a failure, and it is the damping term mg-61ce added to
	// a loop that previously had none.
	//
	// The distinction matters for reading the log: an empty Channel with a
	// nudge_error means every road was tried and none worked; DeliverySuppressed
	// means one road was deliberately not taken. Only the first is a fault.
	DeliverySuppressed = "suppressed"
)

// Nudger delivers a notice to an agent. pogod injects an implementation
// that nudges the agent's PTY when it is running and falls back to macguffin
// mail both when the agent is offline AND when the PTY nudge fails, so a busy
// recipient still hears the notice (mg-79dc).
//
// It takes a Notice rather than a bare message string because the mail road
// needs a subject and the delivery site cannot compose one — see Notice.
type Nudger func(agent string, notice Notice) (Delivery, error)

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
	// Capacity, when set, lets the two dispatch notices consult the per-repo
	// worker cap before naming a remedy (mg-dd77). Left nil the watcher keeps
	// its pre-mg-dd77 wording, which is right for a daemon with no cap to read
	// and wrong to fake: a notice that cannot see the cap must not claim to.
	Capacity Capacity
	// Workers, when set, lets every available/ check ask whether a live worker
	// is already on an item before calling it neglected (mg-1a8a). Left nil the
	// watcher behaves exactly as it did before that fix — it reports a worked
	// item as unclaimed, because status is then the only evidence it has.
	Workers Workers
}

// Watcher samples macguffin state and nudges the watched agent on stall.
type Watcher struct {
	cfg      config.StallWatchConfig
	workRoot string
	mailRoot string
	nudge    Nudger
	emit     Emitter
	fastPoll func()
	capacity Capacity
	workers  Workers

	mu sync.Mutex
	// lastNudge records when each cooldown key last fired and how many times it
	// has fired. Work-item categories key it per (category, item id) so a
	// deliberately-held item cannot hold the whole category's cooldown; the
	// mail category, which has no item identity, keys it by category alone. See
	// fireKey and repeatCooldown.
	lastNudge map[string]fireRecord
}

// fireRecord is the per-key cooldown state: when the key last fired, and how
// many notices it has produced. count drives the repeat backoff — it is the
// difference between "tell me about this item" and "tell me about this item for
// the twenty-second time".
type fireRecord struct {
	last  time.Time
	count int
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
	if cfg.RepeatBackoffCap <= 0 {
		cfg.RepeatBackoffCap = config.DefaultStallRepeatBackoffCap
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
	// BlockedReminderEnabled is NOT defaulted here, for the same reason
	// PriorityWakeEnabled is not: New() cannot tell an unset bool from an
	// explicit false. config.Load() sets the production default (true); a
	// zero-value config leaves the reminder off. Its numeric knobs resolve
	// lazily in blockedReminderCooldown/blockedReminderMaxNotices, because the
	// max-notices knob has a meaningful negative value ("no cap") that a
	// `<= 0 -> default` normalisation here would destroy.
	//
	// IndefiniteHoldReportEnabled (mg-f398) is not defaulted here for the same
	// unset-vs-false reason; its two duration knobs resolve lazily in
	// indefiniteHoldAgeThreshold/indefiniteHoldCooldown.

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
		capacity:  opts.Capacity,
		workers:   opts.Workers,
		lastNudge: make(map[string]fireRecord),
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

	// Which of these items already has a live worker on it (mg-1a8a). Probed
	// ONCE and shared by all three checks below, so a polecat cannot read as
	// in-flight to one of them and absent to another within the same sample.
	flight := w.probeInFlight()

	// Priority-aware fast wake (gh drellem2/pogo #61). A ready, watched,
	// high-priority available item bypasses the 10-min UnclaimedItemAgeThreshold
	// and is delivered promptly via the same wait-idle nudge, so urgent work no
	// longer waits out the idle-coordinator polling gap. This scans the same
	// listing, so it is nearly free.
	w.checkPriorityWake(now, items, flight)

	// Worked-but-unclaimed (mg-1a8a). Reads the SAME listing over the population
	// the two dispatch checks skip because a live worker holds the item, and says
	// the opposite thing about it: do NOT dispatch. It runs whether or not the
	// dispatch checks fire, because an item whose claim failed open is a defect
	// at any age.
	w.checkWorkedButUnclaimed(now, items, flight)

	// Blocked-reminder (mg-3844). Reads the SAME listing over the DISJOINT
	// population: every item the two dispatch checks skip because
	// `blocked:<agent>` gates it. Its recipient is the named agent, not the
	// coordinator, so it cannot become a dispatch nudge by another name — see
	// checkBlockedReminders for why that distinction holds and why `parked` and
	// `human` are deliberately not in its population.
	w.checkBlockedReminders(now, items)

	// Indefinite-hold report (mg-f398). Reads the SAME listing over the rest of
	// the gated population — everything config.IsDispatchGated holds by ASSIGNEE
	// that the blocked-reminder does not already chase, which by default is
	// `parked` and `human`. It is a READER: it releases nothing, writes nothing,
	// and infers nothing from any item's text. See checkIndefiniteHolds for why
	// that boundary is what separates it from the park-sweeper mayor.md forbids,
	// and why sight of a gated item stopped implying dispatch at mg-4798.
	w.checkIndefiniteHolds(now, items)

	// Standard unclaimed-item stall: an assigned available item aged past the
	// 10-min threshold. When the priority wake is active it OWNS fast-priority
	// items (they follow the short priority cooldown, not the 10-min gate), so
	// skip them here — otherwise a stuck high-priority item would draw a second,
	// slower nudge on top of the fast one. When the wake is disabled they fall
	// through to the standard gate exactly as before, so disabling the feature
	// never silences a high-priority item.
	var stale []workitem.WorkItem
	for _, it := range items {
		if !w.watchedForDispatch(it) {
			continue
		}
		// An item a live worker is on is not neglected, whatever its status says
		// (mg-1a8a). It is not dropped from the fleet's attention — the
		// worked-but-unclaimed check above reports it, with the opposite remedy.
		if _, worked := flight.worker(it.ID); worked {
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
	sort.Slice(stale, func(i, j int) bool { return stale[i].ID < stale[j].ID })

	// Per-item cooldown. Items still inside their own backoff are dropped here,
	// and a tick where every stale item is cooling down produces no nudge at
	// all — which is the whole point: a queue the coordinator is deliberately
	// holding goes quiet instead of re-reporting itself every cooldown.
	due, sel := w.selectDue(categoryUnclaimedItems, stale, now, w.cfg.NudgeCooldown)
	if len(due) == 0 {
		return
	}

	ids := make([]string, len(due))
	for i, it := range due {
		ids[i] = it.ID
	}

	// Name the remedy the cap would actually permit (mg-dd77). The FINDING is
	// unchanged — every aging item is still reported, in the same order — but
	// "claim or dispatch them" is a remedy pogod refuses for a repo already at
	// its worker cap, and a channel that recommends refused actions is one the
	// reader learns to skim. With no capacity probe wired this renders exactly
	// the sentence it always did.
	split := w.splitByCapacity(due)
	msg := split.message(noticeText{
		head: fmt.Sprintf("stall-watch: %d available work item(s) have sat unclaimed for over %s",
			len(due), w.cfg.UnclaimedItemAgeThreshold),
		capHead: fmt.Sprintf("stall-watch: %d available work item(s) have sat unclaimed for over %s, and every one of them is in a repository at its worker cap",
			len(due), w.cfg.UnclaimedItemAgeThreshold),
		verb: "claim or dispatch them",
	})

	advisory, advisedIDs := w.blockIntentAdvisory(due)
	msg += advisory
	msg += flight.uncertaintyNote()
	msg += sel.repeatNotice()

	details := map[string]any{
		"category":           categoryUnclaimedItems,
		"watched_agent":      w.cfg.Agent,
		"item_count":         len(due),
		"item_ids":           ids,
		"age_threshold":      w.cfg.UnclaimedItemAgeThreshold.String(),
		"oldest_age_seconds": now.Sub(oldestModTime(due)).Seconds(),
	}
	if len(advisedIDs) > 0 {
		details["block_intent_mismatch_ids"] = advisedIDs
	}
	sel.stampDetails(details)
	split.stampDetails(details)
	w.fire(categoryUnclaimedItems, Notice{
		Subject: subject(nItems(len(due))+" unclaimed", now.Sub(oldestModTime(due)), ids),
		Message: msg,
	}, details)
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
//   - The dedicated HighPriorityWakeCooldown gates repeats PER ITEM and
//     escalates, so a ready high-priority item that simply stays available
//     (e.g. the coordinator can't dispatch it yet) draws one nudge, then one a
//     cooldown later, then one at twice that, out to RepeatBackoffCap — not one
//     per heartbeat tick, and not one per cooldown forever. This is the
//     category mg-1693 was measured on; see selectDue.
func (w *Watcher) checkPriorityWake(now time.Time, items []workitem.WorkItem, flight WorkInFlight) {
	if !w.cfg.PriorityWakeEnabled {
		return
	}

	var ready []workitem.WorkItem
	for _, it := range items {
		if !w.watchedForDispatch(it) {
			continue
		}
		// Same exclusion as the standard check, and this is the surface it
		// mattered on (mg-1a8a): "claim or dispatch now" is the most imperative
		// wording the component emits and the shortest-cooldown one, so an item
		// whose claim failed open drew the fastest, loudest push toward a second
		// polecat on work already in progress.
		if _, worked := flight.worker(it.ID); worked {
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
	sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })

	// Per-item cooldown — see the same call in checkUnclaimedItems. This is the
	// category the mg-1693 measurement was dominated by: the three worst
	// offenders (mg-61f4, mg-0e24, mg-7c95) were all ready high-priority items
	// the coordinator was holding on purpose behind its polecat cap.
	due, sel := w.selectDue(categoryPriorityWake, ready, now, w.cfg.HighPriorityWakeCooldown)
	if len(due) == 0 {
		return
	}

	ids := make([]string, len(due))
	for i, it := range due {
		ids[i] = it.ID
	}

	// Same capacity check as the standard stall notice, and this is the surface
	// that needed it more (mg-dd77): "dispatch NOW" is the most imperative
	// wording the component emits, it is aimed at the items a coordinator is
	// least willing to ignore, and its cooldown is the shortest — so at cap it
	// was the unactionable alarm that repeated fastest, on the highest-value
	// work. The priority information is kept, not dropped: a coordinator still
	// wants to know a high-priority item is waiting on a slot.
	split := w.splitByCapacity(due)
	msg := split.message(noticeText{
		head: fmt.Sprintf("priority-wake: %d high-priority work item(s) are ready and unclaimed",
			len(due)),
		capHead: fmt.Sprintf("priority-wake: %d high-priority work item(s) are ready but their repository is at capacity",
			len(due)),
		verb: "claim or dispatch now",
	})

	advisory, advisedIDs := w.blockIntentAdvisory(due)
	msg += advisory
	msg += flight.uncertaintyNote()
	msg += sel.repeatNotice()

	details := map[string]any{
		"category":       categoryPriorityWake,
		"watched_agent":  w.cfg.Agent,
		"item_count":     len(due),
		"item_ids":       ids,
		"wake_delay":     w.cfg.HighPriorityWakeDelay.String(),
		"wake_cooldown":  w.cfg.HighPriorityWakeCooldown.String(),
		"fast_priority":  strings.Join(w.cfg.FastPriorities, ","),
		"oldest_age_sec": now.Sub(oldestModTime(due)).Seconds(),
	}
	if len(advisedIDs) > 0 {
		details["block_intent_mismatch_ids"] = advisedIDs
	}
	sel.stampDetails(details)
	split.stampDetails(details)
	w.fire(categoryPriorityWake, Notice{
		Subject: subject(nItems(len(due))+" high-priority, unclaimed", now.Sub(oldestModTime(due)), ids),
		Message: msg,
	}, details)

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

	// The one category with no item ids to name, so its whole discriminator is
	// the count and the age. Both move as the backlog does.
	subj := subject(fmt.Sprintf("%d unread mail", count), oldestAge, nil)

	w.fire(categoryUnreadMail, Notice{Subject: subj, Message: msg}, map[string]any{
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
// The item is passed whole rather than as its assignee because the assignee is
// no longer the only field that gates it. Since mg-69b1 an item whose body
// carrier declares `stage: gated` is gated too (config.IsStageGated), and this
// watcher is where that defect was OBSERVED: the priority wake read three
// gh-issue carriers parked at a human GO/NO-GO, saw `assignee=[]`, and told the
// coordinator they were "ready and unclaimed — claim or dispatch now". The spawn
// point refusing them (agent.dispatchGateRefusal) makes that nudge wrong as well
// as loud, and a channel that recommends actions the daemon then refuses is one
// readers learn to discount.
//
// Silence is the correct treatment here, matching `human`: mayor.md holds an
// item at the gate precisely so it stops generating traffic, and "silence =
// HOLD, however long that takes" is the gate's stated semantics. Aging the gated
// queue belongs to the PM sweep, which reads it anyway — see
// config.DefaultNonDispatchableAssignees for why that split, not this watcher,
// owns it.
//
// An item whose carrier block the parser COULD NOT READ is treated as gated too
// (mg-27d4). That is the third outcome the parse gained: `Stage` being empty no
// longer means "declares no stage", it can also mean "declares one somewhere I
// could not reach", and this watcher must not offer up the second as ready. Two
// items in available/ were in exactly that state when this landed — `stage:
// gated` written above their title heading, held only by an `assignee: parked`
// nobody could have relied on being there.
func (w *Watcher) watchedForDispatch(it workitem.WorkItem) bool {
	if it.CarrierUnreadable {
		return false
	}
	return !w.isDispatchGated(it.Assignee) && !config.IsStageGated(it.Stage)
}

// isDispatchGated reports whether an assignee names a non-dispatchable executor
// (default: "human", "parked"). Matching is case-insensitive and whitespace-trimmed, so a
// "Human" or " human " frontmatter value still gates, mirroring isFastPriority.
//
// The implementation lives in config.IsDispatchGated because stall-watch is no
// longer its only caller: the spawn handler enforces the same gate at the
// dispatch point (internal/agent.handleSpawnPolecat, mg-4798). This method stays
// as the Watcher-shaped spelling that binds it to this watcher's configured
// vocabulary — it must not grow a second copy of the rule.
func (w *Watcher) isDispatchGated(assignee string) bool {
	return config.IsDispatchGated(assignee, w.cfg.NonDispatchableAssignees)
}

// blockIntentAdvisory returns a sentence to append to a nudge when one of the
// items it is about DECLARES a block in its tags while its assignee leaves it
// dispatchable, plus the ids it named. Both are empty when nothing mismatches,
// which is the ordinary case.
//
// This fires exactly where the ambiguity does its damage: the moment stall-watch
// tells the coordinator an item is ready to dispatch. The item is watched
// correctly — `blocked-on-daniel` is a tag, and tags do not gate (see
// config.BlockTagPrefix for why that stays true) — so nothing here changes what
// is watched or what fires. It adds one sentence naming the contradiction the
// coordinator would otherwise have to spot by reading tags it has no reason to
// read.
//
// Advice, not a gate: the nudge still fires, the item is still dispatchable, and
// the coordinator still decides. mg-6fb0 ruled the repair is the
// `blocked:<agent>` shape and this is the interim that keeps working afterwards,
// because what it catches is someone still writing the old idiom.
func (w *Watcher) blockIntentAdvisory(items []workitem.WorkItem) (string, []string) {
	type mismatch struct{ id, tag, suggest string }
	var found []mismatch
	var ids []string
	for _, it := range items {
		tag, ok := config.BlockIntentMismatch(it.Assignee, it.TagList(), w.cfg.NonDispatchableAssignees)
		if !ok {
			continue
		}
		found = append(found, mismatch{id: it.ID, tag: tag, suggest: config.SuggestBlockedAssignee(tag)})
		ids = append(ids, it.ID)
	}
	if len(found) == 0 {
		return "", nil
	}

	parts := make([]string, len(found))
	for i, m := range found {
		// A `blocked-on-mg-1234` tag names another ITEM, and `mg new --depends`
		// already expresses that — pointing the reader at the assignee field for
		// it would be wrong advice, so SuggestBlockedAssignee declines and the
		// sentence changes rather than the check going quiet.
		fix := "if it is waiting on another item, express that with --depends"
		if m.suggest != "" {
			fix = "if it is genuinely waiting, set --assignee=" + m.suggest
		}
		parts[i] = fmt.Sprintf("%s is tagged %s but its assignee does not gate dispatch — %s",
			m.id, m.tag, fix)
	}
	return " [block-intent] " + strings.Join(parts, "; ") + ".", ids
}

// fireKey builds the cooldown map key for a category/item pair. An empty item
// yields the bare category key, which is what the mail category uses — it has
// no item identity to key on.
//
// The separator is a NUL because it cannot appear in either a category constant
// or a work-item id, so no (category, item) pair can collide with another.
func fireKey(category, item string) string {
	if item == "" {
		return category
	}
	return category + "\x00" + item
}

// repeatCooldown returns the gap required before the (count+1)-th notice about
// a key that has already fired count times: the base cooldown doubled once per
// prior repeat, ceilinged at capDur.
//
// The FIRST notice about an item is never delayed (count == 0 never reaches
// here — selectDue fires immediately on a missing record). The escalation only
// governs how often the watcher repeats itself about an item the coordinator
// has demonstrably already been told about.
//
// Doubling rather than a flat long cooldown keeps the second notice prompt — a
// genuine stall that the coordinator missed the first time is re-raised one
// base cooldown later, not four hours later — while a persistently-held item
// walks itself out to the cap within a handful of notices.
func repeatCooldown(base, capDur time.Duration, count int) time.Duration {
	if base <= 0 {
		return capDur
	}
	if capDur < base {
		// A cap at or below the base disables escalation: every repeat waits
		// exactly one base cooldown. This is the documented escape hatch back
		// to a flat per-item cooldown.
		return base
	}
	d := base
	for i := 1; i < count; i++ {
		d *= 2
		if d >= capDur {
			return capDur
		}
	}
	return d
}

// selection is the outcome of one selectDue call: what got through, what was
// held back by its own backoff, and how many notices each surviving item has
// now drawn. It exists so a fire can REPORT its own suppression — the mg-1693
// defect was invisible for a night because the log recorded fires but never
// counted them per item, and a fix that is equally uncountable would only move
// the blind spot.
type selection struct {
	// repeats maps item id -> total notices including this one, for items on
	// their second or later notice. First-time items are omitted.
	repeats map[string]int
	// suppressed lists the candidate ids held back by their own backoff.
	suppressed []string
	// nextBackoff is the longest gap now applied to any item in this fire.
	nextBackoff time.Duration
}

// stampDetails records the selection on an event's details map, so
// events.log can be counted per item the way mg-1693 had to be measured by
// hand. Absent keys mean "nothing to report" rather than zero.
func (s selection) stampDetails(details map[string]any) {
	if len(s.repeats) > 0 {
		details["repeat_counts"] = s.repeats
	}
	if len(s.suppressed) > 0 {
		details["backoff_suppressed_ids"] = s.suppressed
		details["backoff_suppressed_count"] = len(s.suppressed)
	}
	if s.nextBackoff > 0 {
		details["next_backoff"] = s.nextBackoff.String()
	}
}

// repeatNotice returns a sentence naming the items in this nudge that the
// watcher has reported before, or "" when every item is new to the reader.
//
// The reader needs this to tell a fresh stall from a re-raise. Without it a
// repeat is textually identical to a first notice, which is the property that
// teaches a coordinator to discount the channel.
func (s selection) repeatNotice() string {
	if len(s.repeats) == 0 {
		return ""
	}
	ids := make([]string, 0, len(s.repeats))
	for id := range s.repeats {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%s (notice #%d)", id, s.repeats[id])
	}
	return fmt.Sprintf(" [repeat] already reported: %s — backing off, next notice about these no sooner than %s.",
		strings.Join(parts, ", "), s.nextBackoff)
}

// selectDue applies the per-item cooldown to a category's candidate items and
// returns the subset that may be notified now, plus a selection describing what
// was held back.
//
// It also PRUNES: any key under this category whose item is no longer a
// candidate is forgotten. That does two jobs. It bounds the map — otherwise
// every id ever seen accumulates for the life of the process — and it makes an
// item that leaves available/ and later comes back read as new, which is
// correct: a claimed-then-released item is a fresh event, not a continuation of
// the old one. Callers must therefore call this even when candidates is empty,
// which is why neither work-item check early-returns on an empty candidate set
// anymore.
//
// Note the state is in-process: a pogod restart forgets every backoff and the
// next tick re-notifies each held item once. That is deliberate — a restart is
// exactly when a coordinator's in-memory picture of what it was holding is also
// gone.
func (w *Watcher) selectDue(category string, candidates []workitem.WorkItem, now time.Time, base time.Duration) ([]workitem.WorkItem, selection) {
	w.mu.Lock()
	defer w.mu.Unlock()

	live := make(map[string]bool, len(candidates))
	for _, it := range candidates {
		live[fireKey(category, it.ID)] = true
	}
	prefix := category + "\x00"
	for k := range w.lastNudge {
		if strings.HasPrefix(k, prefix) && !live[k] {
			delete(w.lastNudge, k)
		}
	}

	sel := selection{repeats: make(map[string]int)}
	var due []workitem.WorkItem
	for _, it := range candidates {
		key := fireKey(category, it.ID)
		rec, seen := w.lastNudge[key]
		if seen && now.Sub(rec.last) < repeatCooldown(base, w.cfg.RepeatBackoffCap, rec.count) {
			sel.suppressed = append(sel.suppressed, it.ID)
			continue
		}
		rec.last = now
		rec.count++
		w.lastNudge[key] = rec
		due = append(due, it)
		if rec.count > 1 {
			sel.repeats[it.ID] = rec.count
		}
		if next := repeatCooldown(base, w.cfg.RepeatBackoffCap, rec.count); next > sel.nextBackoff {
			sel.nextBackoff = next
		}
	}
	if len(sel.repeats) == 0 {
		sel.repeats = nil
	}
	return due, sel
}

// tryFire enforces a per-category cooldown. It is used only by the unread-mail
// check, which watches a single aggregate condition with no per-item identity
// to key on — the two work-item categories use selectDue's per-item cooldown
// instead (mg-1693).
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
	key := fireKey(category, "")
	if rec, ok := w.lastNudge[key]; ok && now.Sub(rec.last) < cooldown {
		return false
	}
	rec := w.lastNudge[key]
	rec.last = now
	rec.count++
	w.lastNudge[key] = rec
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
func (w *Watcher) fire(category string, n Notice, details map[string]any) {
	w.fireTo(w.cfg.Agent, category, n, details)
}

// fireTo is fire with an explicit recipient. Every dispatch-shaped notice goes
// to w.cfg.Agent (the coordinator) and uses fire; the blocked-reminder
// (mg-3844) is the one check whose recipient is read out of the work item
// rather than the config, because `blocked:<agent>` is the only gated assignee
// that names one. The recipient is stamped into the event so a notice sent to
// somebody other than the watched agent is countable as such.
func (w *Watcher) fireTo(recipient, category string, n Notice, details map[string]any) {
	if recipient != w.cfg.Agent {
		details["nudge_recipient"] = recipient
	}
	// Stamped so "which notices were indistinguishable in the recipient's
	// notification list" is answerable from events.log alone. That question was
	// only answerable by hand-reading the maildir when mg-b6f8 was filed, which
	// is why the complaint sat unfiled long enough to be measured twice.
	if n.Subject != "" {
		details["nudge_subject"] = n.Subject
	}
	delivery, err := w.nudge(recipient, n)
	if err != nil {
		details["nudge_error"] = err.Error()
	}
	if delivery.Channel != "" {
		details["nudge_delivery"] = delivery.Channel
	}
	if delivery.FallbackReason != "" {
		details["nudge_fallback_reason"] = delivery.FallbackReason
	}
	// A suppressed fire carries its damping state, so "the notice was withheld"
	// is countable per fire rather than inferable only from the absence of a
	// mail. The counter is the interesting half: one suppression is the damping
	// term working, a counter climbing across many fires is a coordinator that
	// has not gone idle once in that span.
	if delivery.SuppressedConsecutive > 0 {
		details["nudge_suppressed_consecutive"] = delivery.SuppressedConsecutive
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
