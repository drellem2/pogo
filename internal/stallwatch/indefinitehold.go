package stallwatch

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/workitem"
)

// categoryIndefiniteHold keys the indefinite-hold report's cooldown and stamps
// its emitted event. It is distinct from every other category for the same
// reason categoryBlockedReminder is: a shared cooldown would let a busy
// dispatch queue silence this report, and this report is the one whose entire
// subject matter IS silence.
const categoryIndefiniteHold = "indefinite_hold"

// checkIndefiniteHolds reports, on a cadence, that work items have been sitting
// on a hold nothing will ever open (mg-f398). It is a READER. It releases
// nothing, edits nothing, and decides nothing.
//
// # The finding
//
// `snooze` and `depends` are holds with a driver: mg's */15 sweep evaluates them
// and promotes what has opened. `parked` and `human` have no driver — nothing
// scheduled ever evaluates their release condition, so an indefinite hold
// persists until a person happens to look. The release condition can be
// perfectly well-formed and it changes nothing, because nothing reads it.
//
// The exhibit is one item, and it is stronger than the other 21 it arrived with.
// On 2026-08-10 20:12-20:17Z, 22 items were parked under a token-budget cap and
// released 2026-08-13 02:2xZ — 2.5 days later, and only because a coordinator
// happened to trace one ticket's park reason while looking at something else.
// 21 of them carried a CIRCULAR release condition ("clear `parked` when the
// constraint lifts and this item is selected for work" — `parked` is the state
// that removes an item from selection, so nothing selects it, so nothing clears
// it). Forbidding that wording would have repaired nothing: the 22nd, mg-e7f5,
// said "Reopen/clear assignee when the cap lifts", named a condition entirely
// outside itself, was not circular in any way, and stranded for exactly as long.
// The circularity explained 21 cases and caused none of them. There was simply
// no reader.
//
// # Why this is not the rejected park-sweeper
//
// mayor.md rejects a park-SWEEPER on the grounds that anything given sight of
// gated items in order to RELEASE them could also DISPATCH them — the same
// predicate gates both. That ruling stands and this check is deliberately not
// the thing it forbids. Every clause of it is load-bearing here:
//
//   - It does not release. No item's assignee, or any other field, is written
//     by this file. Release stays a coordinator/human judgement.
//   - It does not infer. No title or body text is read, matched, or parsed. In
//     particular there is no "until"/"after" keyword sniff — the second thing
//     mayor.md prohibits, because it rots on the next phrasing and fires on rows
//     that are already right. This reports the FACT and AGE of a hold and never
//     its meaning.
//   - It acquires no dispatch capability and routes through nothing that has
//     any. It composes a message and hands it to the same nudge channel every
//     other notice uses.
//
// What changes is one thing only: "somebody happens to look" becomes "something
// looks on a cadence". `parked` keeps its meaning — it still buys silence from
// the two dispatch nudges, which is what it was for.
//
// # Why sight is affordable now
//
// The ruling was priced against "sight implies dispatch", and that stopped being
// true when mg-4798 shipped. The gate lives in the executable path at the spawn
// point: agent.MGDispatchGate.DispatchGated refuses on
// config.IsDispatchGated(item.Assignee) and names what gated it, and
// cmd/pogod/main.go arms it deliberately OUTSIDE stallWatchArmed so a daemon
// that never reaches that line still gates the defaults. So enumerating gated
// items confers no ability to dispatch one.
//
// The honest boundary on that claim, because a reader weighing "sight is now
// safe" is owed it: the gate FAILS OPEN on an unreadable store — it logs
// "dispatching WITHOUT the assignee gate" and proceeds when it cannot read the
// work item. That is a pre-existing condition this check neither creates nor
// worsens (a read-only reporter dispatches nothing under any branch), but the
// refusal is not unconditional.
//
// The ruling's other premise was that an indefinite hold has no release time, so
// degraded redundancy cannot make it LATE. That is true, and it is not the same
// as no cost. Nothing was late for those 22 items, because nothing was due, and
// 2.5 days of real work still did not happen.
//
// # Population: a rule, not a list
//
// Membership is "gated by ASSIGNEE, and not the one gated shape something
// already chases". `blocked:<agent>` is excluded because it HAS a driver since
// mg-3844 — checkBlockedReminders tells the named agent a decision is owed — and
// reporting it here too would be a second notice about the same hold. Everything
// else config.IsDispatchGated gates is in, which by default is `parked` and
// `human` and which is exactly mayor.md's bottom-two rows.
//
// Stated as a rule rather than the literal pair so a gate value added to
// `non_dispatchable_assignees` next year gets a reader for free. A hold with no
// driver is what this reports; enumerating today's spellings of it would
// re-create the gap on the first new one.
//
// Scope note, recorded rather than fixed: `stage: gated` and an unreadable
// carrier (config.IsStageGated, workitem.CarrierUnreadable) also hold an item
// indefinitely and are also unwatched. They are a different population — a
// carrier declaration rather than an assignee hold, and the unreadable case is a
// parse defect to repair rather than a hold to age — so they are deliberately
// not in here.
//
// # It is available/ only
//
// A gated item is never dispatched, so it stays in available/. items is the
// caller's already-listed available/ snapshot, so this costs one pass over a
// slice.
func (w *Watcher) checkIndefiniteHolds(now time.Time, items []workitem.WorkItem) {
	if !w.cfg.IndefiniteHoldReportEnabled {
		return
	}

	threshold := w.indefiniteHoldAgeThreshold()

	var held, unaged []workitem.WorkItem
	for _, it := range items {
		if !w.indefinitelyHeld(it) {
			continue
		}
		// A zero ModTime is a THIRD answer, not a very old date: it means the
		// file could not be stat'd, so this item's age is unknown. Counted
		// separately rather than folded into either side, because both ways of
		// folding it are wrong and one is absurd — arithmetic on the zero time
		// would report a fresh hold as ~739000 days old, which is the single
		// loudest thing this notice could say and would be false.
		//
		// It stays a candidate. It IS held; only the number is missing, and
		// dropping it would be this check reproducing its own finding — a hold
		// that nothing reports because one field was unreadable.
		if it.ModTime.IsZero() {
			unaged = append(unaged, it)
			continue
		}
		if now.Sub(it.ModTime) < threshold {
			continue
		}
		held = append(held, it)
	}

	// Oldest first. The reader's question is "what has been sitting longest",
	// and the order answers it without them scanning the ages.
	sort.Slice(held, func(i, j int) bool {
		if !held[i].ModTime.Equal(held[j].ModTime) {
			return held[i].ModTime.Before(held[j].ModTime)
		}
		return held[i].ID < held[j].ID
	})
	sort.Slice(unaged, func(i, j int) bool { return unaged[i].ID < unaged[j].ID })

	candidates := make([]workitem.WorkItem, 0, len(held)+len(unaged))
	candidates = append(candidates, held...)
	candidates = append(candidates, unaged...)

	// Called even when candidates is empty, per selectDue's contract: it prunes
	// this category's keys for items no longer held, which is what makes an item
	// released and later re-held read as new rather than as a continuation.
	due, sel := w.selectDue(categoryIndefiniteHold, candidates, now, w.indefiniteHoldCooldown())
	if len(due) == 0 {
		return
	}

	// The message lists EVERY held item, not just the due ones, while `due`
	// decides only WHETHER to send. A digest that showed a subset would answer
	// "what is being held?" with a number that depends on notice timing — the
	// one question this notice exists to answer, given a misleading answer by
	// its own rate limiter.
	parts := make([]string, 0, len(candidates))
	for _, it := range held {
		parts = append(parts, fmt.Sprintf("%s (%s, held %s)",
			it.ID, holdGate(it.Assignee), compactAge(now.Sub(it.ModTime))))
	}
	for _, it := range unaged {
		parts = append(parts, fmt.Sprintf("%s (%s, age UNKNOWN — its file could not be stat'd)",
			it.ID, holdGate(it.Assignee)))
	}
	ids := itemIDs(candidates)

	msg := fmt.Sprintf(
		// The threshold is stated as the REPORTING RULE, not as a claim about
		// every item listed: an unaged item is in the list and "held 1d or
		// longer" would be false of it.
		"indefinite-hold: %d work item(s) are on a hold that NOTHING SCHEDULED WILL EVER RELEASE (reported once a hold passes %s) — %s. "+
			"This is NOT a dispatch request and NOT a release: they stay gated, `pogo agent spawn-polecat` refuses them "+
			"by name, and this notice has changed no field on any of them. It reports the FACT and AGE of each hold and "+
			"reads nothing of its meaning — run `mg show <id>` for why each one is held and what would open it, then "+
			"decide. Releasing is yours: `mg edit <id> --assignee=<owner>` if the hold is over, or "+
			"`mg snooze <id> --until <time>` if what it is really waiting for is a time (a park has no driver, a snooze "+
			"does). A hold meant to last is fine — leave it. This report has NO notice cap, deliberately: the silence it "+
			"replaces is what left 22 items parked for 2.5 days.",
		len(candidates), compactAge(threshold), strings.Join(parts, ", "))
	msg += subsetRepeatNotice(sel, itemIDs(due))

	// Gate counts, not just ids: "4 parked, 1 human" is a different operational
	// picture from "5 held", and the two gates mean opposite things about who
	// has to act.
	gateCounts := make(map[string]int, 2)
	for _, it := range candidates {
		gateCounts[holdGate(it.Assignee)]++
	}

	details := map[string]any{
		"category":      categoryIndefiniteHold,
		"watched_agent": w.cfg.Agent,
		"item_count":    len(candidates),
		"item_ids":      ids,
		"hold_gates":    gateCounts,
		"age_threshold": threshold.String(),
		"cooldown":      w.indefiniteHoldCooldown().String(),
	}
	oldest := time.Duration(0)
	if len(held) > 0 {
		oldest = now.Sub(held[0].ModTime)
		details["oldest_age_seconds"] = oldest.Seconds()
	}
	if len(unaged) > 0 {
		details["unaged_ids"] = itemIDs(unaged)
	}
	sel.stampDetails(details)

	w.fire(categoryIndefiniteHold, Notice{
		// "held indefinitely" rather than a count of items, because this
		// notice's recipient is the coordinator and every other work-item
		// subject it receives means "dispatch this". The subject is where that
		// difference has to land: it arrives in the same notification list.
		Subject: subject(nItems(len(candidates))+" held indefinitely", oldest, ids),
		Message: msg,
	}, details)
}

// indefinitelyHeld reports whether it sits on a hold with no driver: gated away
// from dispatch by its ASSIGNEE, and not the one gated shape something already
// chases.
//
// The `blocked:<agent>` exclusion is the whole reason this is a predicate rather
// than a call to isDispatchGated. config.IsDispatchGated returns true for the
// blocked shape too (it is a shape, not a vocabulary entry), and mg-3844 already
// gave that shape a reader — the one gated value that carries a recipient to
// tell. Including it here would mean two notices about one hold, to two
// recipients, on two cadences.
func (w *Watcher) indefinitelyHeld(it workitem.WorkItem) bool {
	if _, ok := config.BlockedOn(it.Assignee); ok {
		return false
	}
	return w.isDispatchGated(it.Assignee)
}

// holdGate renders an assignee for display, normalised the way the gate matches
// it (lower-cased, trimmed) so a " Parked " frontmatter value does not read as a
// different gate from `parked` in a notice whose whole content is a list.
func holdGate(assignee string) string {
	return strings.ToLower(strings.TrimSpace(assignee))
}

// indefiniteHoldAgeThreshold returns how long a hold must have sat before it is
// reportable. Zero configured falls back to the shipped default.
func (w *Watcher) indefiniteHoldAgeThreshold() time.Duration {
	if w.cfg.IndefiniteHoldAgeThreshold > 0 {
		return w.cfg.IndefiniteHoldAgeThreshold
	}
	return config.DefaultIndefiniteHoldAgeThreshold
}

// indefiniteHoldCooldown returns the base of the report's per-item backoff.
//
// Note what the default pair does, because it is load-bearing and it is
// implicit: with a 24h base against the 4h DefaultStallRepeatBackoffCap,
// repeatCooldown takes its `capDur < base` branch and returns a FLAT 24h. That
// is the intended cadence — a daily digest, not an escalating nag. An operator
// who raises repeat_backoff_cap above the base gets doubling instead, which is
// also defensible for a hold that has lasted weeks; it is documented rather than
// prevented.
func (w *Watcher) indefiniteHoldCooldown() time.Duration {
	if w.cfg.IndefiniteHoldReportCooldown > 0 {
		return w.cfg.IndefiniteHoldReportCooldown
	}
	return config.DefaultIndefiniteHoldReportCooldown
}
