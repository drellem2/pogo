package stallwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/workitem"
)

// categoryBlockedReminder keys the blocked-reminder's cooldown and stamps its
// emitted event. It is distinct from the two dispatch categories because it is
// a different signal to a different recipient, and conflating their cooldowns
// would let a busy dispatch queue silence a block notice, or vice versa.
const categoryBlockedReminder = "blocked_reminder"

// checkBlockedReminders tells an agent named by a `blocked:<agent>` assignee
// that a decision is owed on an item nothing IN MG will move (mg-3844).
//
// The "IN MG" is load-bearing, and BOTH emitted messages carry it — see the
// comment on each. This check reads work items and nothing else; a `pogo
// schedule` one-shot aimed at a blocked item lives in a separate system, is
// invisible here, and does move it (mg-8188).
//
// # Why this is not the rejected park-sweeper
//
// mayor.md rejects a sweeper over gated items on the grounds that anything given
// sight of them in order to RELEASE them could also DISPATCH them, the blindness
// being the same predicate that stops dispatch. That argument is about release,
// and it survives — nothing here releases anything.
//
// But the argument's premise is worth restating accurately, because the obvious
// reading of it ("sight implies dispatch") stopped being true when mg-4798
// shipped. Dispatch is refused at the SPAWN POINT by
// agent.Registry.dispatchGateRefusal, independently of who saw what. So a
// component that enumerates gated items does not thereby gain the ability to
// dispatch one: the spawn is refused, by name, with a message that says what
// gated it. Stall-watch's blindness is now defence in depth rather than the only
// defence, which is precisely what makes a second reader of the gated population
// affordable.
//
// What actually distinguishes this from a sweeper is the pair (capability,
// recipient), not the sight:
//
//   - It does not release. It sends a message. The only party that can clear the
//     block is the one already named in the field, and asking them to exercise
//     the judgement the field says is theirs is the DESIGNED release path, not a
//     bypass of it.
//   - `blocked:<agent>` is the only gated value that carries a recipient. That
//     is what makes a targeted reminder implementable where a sweeper is not,
//     and it is why this check reads the `blocked:` shape rather than
//     config.IsDispatchGated. `parked` and `human` are NOT in this population.
//
// # Why parked and human stay silent
//
// Their silence is the feature. mayor.md holds items on `human` precisely so
// they stop generating traffic, and `parked` means "deliberately not chasing
// this". Treating all three gated assignees alike would convert an intentional
// silence into noise — which is a worse outcome than the gap this closes, since
// a channel that fires on things nobody wants chased is a channel readers learn
// to discount.
//
// # The failure this fixes is not forgetting, it is never knowing
//
// mg-3844's title says a hold "depends on that agent remembering". Mayor's
// first-hand instance sharpened it: on 2026-07-30, mg-e084 sat `available` and
// `high` behind `blocked:pm-pogo`, drew zero stall-watch and zero priority-wake
// alerts (the gate silenced both, as designed), and reached pm-pogo only because
// mayor hand-wrote a mail at block time. Had that diligence been skipped, the
// named agent would not have forgotten — it would never have been told. Hence
// the first notice is never delayed (selectDue fires immediately on an unseen
// item), and the recurring cadence is the lesser half.
//
// # It is available/ only
//
// A gated item is never dispatched, so it stays in available/ — mg-e084 was
// `status=available` throughout. Scanning claimed/ would add nothing and done/
// grows without bound. items is the caller's already-listed available/
// snapshot, so this costs one pass over a slice.
func (w *Watcher) checkBlockedReminders(now time.Time, items []workitem.WorkItem) {
	if !w.cfg.BlockedReminderEnabled {
		return
	}

	// Candidates are every `blocked:<agent>` item, INCLUDING ones already past
	// the notice cap. They must stay in the candidate set because selectDue
	// prunes any key whose item is no longer a candidate — dropping a capped item
	// here would forget its count, and it would read as new next tick and start
	// the four notices over. The cap is applied to the message below instead.
	//
	// Two kinds of `blocked:<agent>` item whose agent is REACHABLE are NOT
	// candidates, and both leave the set before selectDue so that neither draws
	// a notice nor holds a count (drellem2/pogo#161):
	//
	//   - one carrying a waiver tag: the named agent said, in the one place this
	//     check reads, that it knows and is waiting on purpose;
	//   - one whose current assignee the named agent wrote ITSELF: that value is
	//     a statement the agent made, not a request it received.
	//
	// Leaving the set prunes the item's key, so if the waiver is removed or
	// someone else re-writes the block, the item reads as new and its first
	// notice fires immediately — exactly what either change means.
	waivers := w.blockedReminderWaiverTags()
	readSetters := false
	var blocked []workitem.WorkItem
	var waived, selfSet []string
	for _, it := range items {
		who, ok := config.BlockedOn(it.Assignee)
		if !ok {
			continue
		}
		// Neither silence applies to a blocker no reminder can reach. That
		// item's notice goes to the COORDINATOR and says "fix the assignee",
		// and neither the named name nor its tagger can answer for it: a
		// typo'd name is not an agent that knows anything, and the OS-user
		// fallback actor (macguffin writes e.g. `daniel` when no agent name is
		// set) is unrecognised by definition — so `blocked:daniel` written by
		// `daniel` must still reach the coordinator (fail open; review round 1).
		who = normName(who)
		if who == "" || !w.hasMailbox(who) {
			blocked = append(blocked, it)
			continue
		}
		if config.HasAnyTag(it.TagList(), waivers) {
			waived = append(waived, it.ID)
			continue
		}
		if !readSetters {
			// Once per tick, and only when there is a block to ask about. A
			// failed read leaves every setter unknown, which fails open: the
			// notice is sent.
			w.setters.refresh()
			readSetters = true
		}
		if w.setters.setter(it.ID, it.Assignee) == who {
			selfSet = append(selfSet, it.ID)
			continue
		}
		blocked = append(blocked, it)
	}
	sort.Slice(blocked, func(i, j int) bool { return blocked[i].ID < blocked[j].ID })
	w.recordQuietBlocks(waived, selfSet, waivers)

	due, sel := w.selectDue(categoryBlockedReminder, blocked, now, w.blockedReminderCooldown())

	// Apply the notice cap. A first notice has count 1 and is omitted from
	// sel.repeats, so an absent id means one.
	max := w.blockedReminderMaxNotices()
	var capped []string
	kept := due[:0]
	for _, it := range due {
		count := 1
		if n, ok := sel.repeats[it.ID]; ok {
			count = n
		}
		if max > 0 && count > max {
			capped = append(capped, it.ID)
			delete(sel.repeats, it.ID)
			continue
		}
		kept = append(kept, it)
	}
	due = kept
	if len(sel.repeats) == 0 {
		sel.repeats = nil
	}

	if len(due) == 0 {
		// Nothing to say — but a cap reached is a deliberate silence, and the
		// mg-1693 lesson is that a silence nobody can count is indistinguishable
		// from a detector that stopped working. Record it even with no nudge.
		if len(capped) > 0 {
			w.emitBlockedCapEvent(capped, max)
		}
		return
	}

	// Group by recipient: one nudge per agent, not one per item. A batch of
	// three items blocked on the same agent is one message.
	byRecipient := make(map[string][]workitem.WorkItem)
	unreachable := make(map[string][]workitem.WorkItem)
	for _, it := range due {
		who, _ := config.BlockedOn(it.Assignee)
		who = strings.ToLower(strings.TrimSpace(who))
		if who == "" || !w.hasMailbox(who) {
			unreachable[who] = append(unreachable[who], it)
			continue
		}
		byRecipient[who] = append(byRecipient[who], it)
	}

	recipients := make([]string, 0, len(byRecipient))
	for who := range byRecipient {
		recipients = append(recipients, who)
	}
	sort.Strings(recipients)

	for _, who := range recipients {
		group := byRecipient[who]
		ids := itemIDs(group)
		// "nothing IN MG will move them", not "nothing scheduled". mg work
		// items and `pogo schedule` entries are separate systems, and this
		// check reads only the first: it is handed available/ and knows nothing
		// about the scheduler's store. A one-shot registered against an item
		// (`pogo schedule <agent> --once --in ... --id work-<id>`) WILL move it,
		// invisibly to this sentence — architect had exactly such a fire pending
		// on mg-dafb while the reminder called it unmoved.
		//
		// The old wording was true *of mg* and phrased as a claim about the
		// world. A reader who took it literally on an item someone had
		// mechanised externally could reasonably conclude the work was stranded
		// and pick it up: duplicated effort sourced from a true-but-overscoped
		// sentence (mg-8188). Say which system you are speaking for, especially
		// when the answer is "only mine".
		msg := fmt.Sprintf(
			"blocked-reminder: %d work item(s) are BLOCKED ON YOU and nothing IN MG will move them — %s. "+
				"This is NOT a dispatch request: they are gated away from automatic dispatch and stay that way "+
				"until you act. Run `mg show <id>` for what each one is waiting on, do it, then tell whoever set "+
				"the block (or clear it yourself with `mg edit <id> --assignee=<owner>`). "+
				blockedReminderStopSentence(max, waivers),
			len(group), strings.Join(ids, ", "))
		msg += subsetRepeatNotice(sel, ids)

		details := map[string]any{
			"category":           categoryBlockedReminder,
			"watched_agent":      w.cfg.Agent,
			"blocked_on":         who,
			"reachable":          true,
			"item_count":         len(group),
			"item_ids":           ids,
			"cooldown":           w.blockedReminderCooldown().String(),
			"max_notices":        max,
			"oldest_age_seconds": now.Sub(oldestModTime(group)).Seconds(),
		}
		if len(capped) > 0 {
			details["notice_cap_reached_ids"] = capped
		}
		sel.stampDetails(details)
		w.fireTo(who, categoryBlockedReminder, Notice{
			// "blocked on you" is second person because the recipient IS the
			// blocker — this is the one category whose recipient is read out of
			// the item rather than the config, and the subject is where that
			// difference has to land: it arrives in the same notification list
			// as the dispatch notices, which mean the opposite thing.
			Subject: subject(nItems(len(group))+" blocked on you", ids),
			Message: msg,
		}, details)
	}

	// An unreachable blocker is reported to the COORDINATOR, because the
	// alternative is the exact disease this check treats: a notice that looks
	// sent and reached nobody.
	//
	// It is safe to tell the coordinator, and the message says why in its own
	// text. macguffin's mail.Send validates a recipient as a path component and
	// nothing more — it has no roster — so mailing an unrecognised name silently
	// CREATES that mailbox and the reminder rots in a maildir no agent drains.
	// A name with an existing maildir has been corresponded with before; a name
	// without one has not, and inventing it would be worse than saying so.
	if len(unreachable) > 0 {
		w.fireUnreachableBlockers(now, unreachable, sel, capped, max)
	}
}

// fireUnreachableBlockers tells the coordinator about items whose named blocker
// cannot be reached — either the assignee is a bare `blocked:` naming nobody, or
// the name has no maildir.
//
// Note what this message must NOT be mistaken for. Its recipient is the
// dispatcher, and every other work-item notice it receives means "dispatch
// this". So the text states the opposite explicitly. The item is also refused at
// the spawn point if the coordinator tries anyway (agent.dispatchGateRefusal),
// so the wording is the courtesy and the gate is the guarantee.
func (w *Watcher) fireUnreachableBlockers(now time.Time, unreachable map[string][]workitem.WorkItem, sel selection, capped []string, max int) {
	whos := make([]string, 0, len(unreachable))
	for who := range unreachable {
		whos = append(whos, who)
	}
	sort.Strings(whos)

	var parts []string
	var allIDs []string
	var all []workitem.WorkItem
	for _, who := range whos {
		ids := itemIDs(unreachable[who])
		allIDs = append(allIDs, ids...)
		all = append(all, unreachable[who]...)
		reason := fmt.Sprintf("blocked on %q, which has no mailbox", who)
		if who == "" {
			reason = "assigned a bare `blocked:` that names no agent"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", strings.Join(ids, ", "), reason))
	}
	sort.Strings(allIDs)

	// "Nothing IN MG will" is scoped for the same reason as the reachable
	// notice above (mg-8188): a `pogo schedule` one-shot aimed at this item is
	// in a different system, so it can chase a blocker this check cannot see.
	msg := fmt.Sprintf(
		"blocked-reminder: %d work item(s) name a blocker no reminder could reach — %s. "+
			"This is NOT a dispatch request; they stay gated and `pogo agent spawn-polecat` will refuse them. "+
			"Check the name for a typo, rewrite the assignee as `blocked:<agent>` if it is bare, or chase the "+
			"blocker yourself. Nothing IN MG will.",
		len(allIDs), strings.Join(parts, "; "))
	msg += subsetRepeatNotice(sel, allIDs)

	details := map[string]any{
		"category":           categoryBlockedReminder,
		"watched_agent":      w.cfg.Agent,
		"reachable":          false,
		"item_count":         len(allIDs),
		"item_ids":           allIDs,
		"unreachable_agents": whos,
		"cooldown":           w.blockedReminderCooldown().String(),
		"max_notices":        max,
		"oldest_age_seconds": now.Sub(oldestModTime(all)).Seconds(),
	}
	if len(capped) > 0 {
		details["notice_cap_reached_ids"] = capped
	}
	sel.stampDetails(details)
	w.fireTo(w.cfg.Agent, categoryBlockedReminder, Notice{
		// Deliberately NOT the reachable head with a different count. These two
		// notices share a category and a recipient can receive both, so if their
		// subjects differed only in a number the reader would be back to opening
		// each to find out which one this is.
		Subject: subject(nItems(len(allIDs))+" with an UNREACHABLE blocker", allIDs),
		Message: msg,
	}, details)
}

// emitBlockedCapEvent records that every due item was held back by the notice
// cap, so a permanently-quiet block is countable rather than merely absent. No
// nudge is sent — that is the whole point of the cap.
func (w *Watcher) emitBlockedCapEvent(capped []string, max int) {
	w.emit(events.Event{
		EventType: "stall_watch_fired",
		Agent:     "pogod",
		Details: map[string]any{
			"category":               categoryBlockedReminder,
			"watched_agent":          w.cfg.Agent,
			"item_count":             0,
			"max_notices":            max,
			"notice_cap_reached_ids": capped,
		},
	})
}

// hasMailbox reports whether who has an existing macguffin maildir. See the
// rationale in checkBlockedReminders' unreachable branch: this is a
// "have we ever corresponded with this name" test, not a roster lookup, because
// macguffin has no roster to look up.
//
// The cost of guessing wrong is asymmetric and the direction is deliberate. A
// brand-new agent that has never received mail reads as unreachable and its
// block is reported to the coordinator instead — one redundant notice about a
// real item. Guessing the other way sends the only notice into a mailbox nobody
// reads, which is silent and indistinguishable from the gap this closes.
func (w *Watcher) hasMailbox(who string) bool {
	if who == "" || w.mailRoot == "" {
		return false
	}
	// who reaches here already lowercased and trimmed by the caller, but it is
	// read out of frontmatter, so refuse anything that is not a single path
	// component before joining it — mirroring macguffin's own validComponent.
	if who == "." || who == ".." || strings.ContainsAny(who, `/\`) {
		return false
	}
	info, err := os.Stat(filepath.Join(w.mailRoot, who, "new"))
	return err == nil && info.IsDir()
}

func (w *Watcher) blockedReminderCooldown() time.Duration {
	if w.cfg.BlockedReminderCooldown > 0 {
		return w.cfg.BlockedReminderCooldown
	}
	return config.DefaultBlockedReminderCooldown
}

// blockedReminderStopSentence says, truthfully, what an agent waiting on
// purpose can do about this notice. Nothing in this check reads an item body,
// and that is deliberate (indefinitehold.go states the same prohibition for the
// sibling reader). Before drellem2/pogo#161 that left no acknowledgment path at
// all, and only the cap bounded the damage. The original sentence told the
// recipient to "say so in the item body", which had no effect on anything: a
// conscientious agent wrote a body section per notice and every notice still
// arrived unchanged (mg-1105). Do not offer an affordance the code does not
// honour. max is the effective cap; 0 means uncapped, which the old %d
// rendered as "stops after 0 reminders".
//
// Since drellem2/pogo#161 there IS an acknowledgement, and it is a tag, because
// a tag is what this check reads: the sentence names the configured waiver tag
// and the exact command that adds it. It still says a reply or a body edit does
// nothing, because that is still true and is what a reader would otherwise try.
func blockedReminderStopSentence(max int, waivers []string) string {
	ack := "If you are waiting on purpose, a reply or a body edit will not stop this notice"
	if tag := firstTag(waivers); tag != "" {
		ack = fmt.Sprintf("If you are waiting on purpose, tag the item `%s` (`mg edit <id> --add-tags=%s`) "+
			"and it stops — the item stays blocked; a reply or a body edit will not stop it", tag, tag)
	}
	if max <= 0 {
		return ack + ". Otherwise it repeats on a backoff until the block clears (the notice cap is disabled)."
	}
	return fmt.Sprintf("%s. Otherwise it stops by itself after %d reminders whether or not the block clears.", ack, max)
}

// firstTag returns the first non-blank waiver tag, trimmed, or "".
func firstTag(tags []string) string {
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" {
			return t
		}
	}
	return ""
}

func (w *Watcher) blockedReminderWaiverTags() []string {
	if len(w.cfg.BlockedReminderWaiverTags) > 0 {
		return w.cfg.BlockedReminderWaiverTags
	}
	return config.DefaultBlockedReminderWaiverTags
}

// recordQuietBlocks emits one event the first time each item is held silent by
// a waiver tag or by having been self-set, so a block that draws no notice is
// countable rather than merely absent (the mg-1693 lesson the cap event already
// follows). It emits on ENTRY only — not every tick — and forgets ids that are
// no longer quiet, so an item that goes quiet again later is recorded again.
func (w *Watcher) recordQuietBlocks(waived, selfSet []string, waivers []string) {
	w.mu.Lock()
	if w.quietBlocks == nil {
		w.quietBlocks = make(map[string]string)
	}
	now := make(map[string]string, len(waived)+len(selfSet))
	for _, id := range waived {
		now[id] = "waived"
	}
	for _, id := range selfSet {
		now[id] = "self_set"
	}
	var newWaived, newSelf []string
	for id, why := range now {
		if w.quietBlocks[id] == why {
			continue
		}
		if why == "waived" {
			newWaived = append(newWaived, id)
		} else {
			newSelf = append(newSelf, id)
		}
	}
	w.quietBlocks = now
	w.mu.Unlock()

	if len(newWaived) == 0 && len(newSelf) == 0 {
		return
	}
	sort.Strings(newWaived)
	sort.Strings(newSelf)
	details := map[string]any{
		"category":      categoryBlockedReminder,
		"watched_agent": w.cfg.Agent,
		"item_count":    0,
	}
	if len(newWaived) > 0 {
		details["waived_ids"] = newWaived
		details["waiver_tags"] = waivers
	}
	if len(newSelf) > 0 {
		details["self_set_ids"] = newSelf
	}
	w.emit(events.Event{
		EventType: "stall_watch_fired",
		Agent:     "pogod",
		Details:   details,
	})
}

// blockedReminderMaxNotices returns the effective notice cap. A negative
// configured value means "no cap" and is returned as 0, which is how the caller
// spells uncapped.
func (w *Watcher) blockedReminderMaxNotices() int {
	switch {
	case w.cfg.BlockedReminderMaxNotices < 0:
		return 0
	case w.cfg.BlockedReminderMaxNotices == 0:
		return config.DefaultBlockedReminderMaxNotices
	default:
		return w.cfg.BlockedReminderMaxNotices
	}
}

// itemIDs extracts ids in slice order.
func itemIDs(items []workitem.WorkItem) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

// subsetRepeatNotice renders selection.repeatNotice restricted to ids, so a
// message that covers one recipient's items does not quote repeat counts for
// another recipient's. Returns "" when none of ids is a repeat.
func subsetRepeatNotice(sel selection, ids []string) string {
	if len(sel.repeats) == 0 {
		return ""
	}
	sub := selection{repeats: make(map[string]int), nextBackoff: sel.nextBackoff}
	for _, id := range ids {
		if n, ok := sel.repeats[id]; ok {
			sub.repeats[id] = n
		}
	}
	if len(sub.repeats) == 0 {
		return ""
	}
	return sub.repeatNotice()
}
