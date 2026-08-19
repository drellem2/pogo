package main

// Rendering of the ack column in `pogo schedule list` (mg-a14c).
//
// # Why this is a file rather than three lines inside the Run func
//
// The column used to be headed COMPLETED and to read `103/302`, and that table
// is where every reader of this fleet's scheduler health starts — ack-watch's
// escalations quote it, the mayor's playbook points at it, and the 46-hour
// FLEET DEFICIT that produced mg-a14c was read off it verbatim. The header made
// a claim the counter cannot support: only the newest fire's token is
// redeemable, so a run of N fires landing inside one agent turn yields at most
// one ack no matter how completely the work was done, and the column's ceiling
// is below 100% for any schedule whose turns outlast its cadence. See
// scheduler.Entry.AttentionGap for the arithmetic and
// scheduler.issueFireTokenLocked for why the supersession rule is not the thing
// that gets fixed.
//
// Naming a metric honestly is not a cosmetic change and it is not testable
// while it lives inline in a cobra Run closure, which is exactly how the old
// wording survived three tickets that each diagnosed it correctly. Hence a
// function with a test that reads the string.
//
// What this deliberately does NOT do: change any threshold, any counter, or the
// ratio itself. mg-a14c's constraint is that if the number is an artefact the
// artefact is the bug, and widening a floor until the alarm stops firing is
// worse than the deficit. The numerator and denominator are unchanged and
// still printed; what is added is the unit that says what they mean and the
// ceiling that says what was available.

import (
	"fmt"
	"time"

	"github.com/drellem2/pogo/internal/scheduler"
	"github.com/drellem2/pogo/internal/turnlog"
)

// ackColumnHeader is what the column is called now. "COMPLETED" asserted that
// the count was of work finished; "ACKED" asserts only what is true, that
// something redeemed a token.
const ackColumnHeader = "ACKED"

// renderAckCell renders one schedule's ack state for the list table.
//
// A schedule that has never acked reads "—", not "0/N": absent evidence is not
// evidence of failure (mg-a754), and that distinction predates this file.
//
// A tracked schedule reads its raw counters — they are what a reader compares
// across rows, and removing them would break the comparison that found the
// original pm-pogo fault — followed by the same quantity as a GAP. The gap is
// the honest unit: "1 ack per 2.9 fires" describes a turn length, which is what
// the ratio measures, whereas "34%" describes a shortfall against a 100% that
// was never reachable.
//
// The unacked-streak marker keeps its ⚠ and stays last, where the eye lands. It
// is the number to act on: a busy-but-healthy agent's streak is bounded by its
// own turn length, a wedged one's climbs without bound.
//
// The marker now carries a turn-evidence clause (mg-7837). "The number to act
// on" was the whole of what this cell said about a streak, and it is only half
// an instruction: a streak climbing because the agent is not there and one
// climbing because a live agent drops its acks read identically, and the reader
// who has to tell them apart is the reader already reaching for the alarm. ev
// is what the agent's turnlog says about the outstanding fire; a zero-value ev
// (Known false) is safe and renders as unavailable rather than as absence.
func renderAckCell(e scheduler.Entry, ev unackedTurnEvidence) string {
	if !e.CompletionTracked() {
		return "—"
	}
	cell := fmt.Sprintf("%d/%d", e.FiresCompleted, e.FiresDelivered)
	if gap := e.AttentionGap(); gap > 0 {
		cell += fmt.Sprintf("  1 ack per %.1f fires", gap)
	}
	if e.UnackedStreak >= scheduler.DefaultStallThreshold {
		cell += fmt.Sprintf("  ⚠ %d unacked", e.UnackedStreak)
		cell += annotateUnacked(e, ev)
	}
	return cell
}

// ackColumnLegend is printed under the table when at least one row carries a
// tracked reading, and omitted entirely when none does.
//
// Printed CONDITIONALLY on purpose: a legend under a table of all-"—" rows
// would explain a ceiling for numbers nobody is looking at, and a caveat that
// prints unconditionally is a caveat readers learn to skip. It is attached to
// the reading, not to the command.
func ackColumnLegend(entries []scheduler.Entry) string {
	tracked := false
	for _, e := range entries {
		if e.CompletionTracked() {
			tracked = true
			break
		}
	}
	if !tracked {
		return ""
	}
	return "\nACKED counts TOKEN REDEMPTIONS, not work done. Only the newest fire's token is\n" +
		"redeemable, so a fire superseded before the agent's turn ends can never be acked\n" +
		"however completely its work was done: the ceiling here is below 100% for any\n" +
		"schedule whose turns outlast its cadence, and the ratio is exactly 1/gap. Do not\n" +
		"read a low ratio as work skipped, and do not raise an alarm off it — read\n" +
		"⚠ N unacked, the one number in this column that does not saturate.\n" +
		"\n" +
		"⚠ N unacked does not say the work was skipped either — a fire delivered into a\n" +
		"dead agent and a fire dropped by a live one produce the same marker. Any clause\n" +
		"after it reports whether that agent's turnlog records COMPLETED TURNS around\n" +
		"the newest fire, and it speaks about that ONE fire: the entry stores no delivery\n" +
		"time for the other N-1. \"Delivered into silence\" means look at whether the agent\n" +
		"was there (pogo check-turns, pogo agent list), not at this schedule.\n"
}

// unackedTurnEvidence is what a turnlog can say about ONE outstanding fire.
//
// It is a struct rather than a bool because the question has four answers and
// three of them are not "the agent skipped it". See turnlog/window.go for the
// reading that produced this, and annotateUnacked for the wording.
type unackedTurnEvidence struct {
	// Known is false when the turnlog could not be read at all — no file, or
	// unreadable. Absence of the instrument is not absence of the agent, and a
	// caller that collapses the two rebuilds the defect this annotation exists
	// to remove. Polecats write no turnlog, so this is the common case.
	Known bool
	// Settled is false while the grace window after the fire is still open, so
	// a zero in AtFire means "not yet" rather than "never".
	Settled bool
	// AtFire is completed turns inside [fire, fire+Grace] — the window in which
	// something could have redeemed this fire's token.
	AtFire int
	// Since is completed turns after that window, up to now. It separates "the
	// agent is still gone" from "the agent came back and the fire had already
	// been superseded".
	Since int
	// Grace is the window width, echoed so the wording can name it rather than
	// hard-code a number a reader cannot check.
	Grace time.Duration
}

// annotateUnacked renders the turn-evidence clause that follows `⚠ N unacked`.
//
// # Why this clause exists
//
// The legend under this table tells the reader that ⚠ N unacked is "the one
// number in this column that does not saturate" — i.e. the one to act on. That
// is true about its arithmetic and silent about its meaning: a fire delivered
// into a dead fleet and a fire dropped by a live agent produce the identical
// marker. On 2026-08-19 the mayor read `⚠ 6 unacked` on predeploy-quiesce-mayor
// — the schedule that guards the deploy drain, next to a measurement that 4 of
// 4 deploy failures died at that drain — and had to file a ticket to find out
// which. The answer was that all seven crew turnlogs are empty across
// 2026-08-14T08:23Z..2026-08-19T06:52Z: nobody was there. This clause is that
// answer, computed in the tool instead of by hand.
//
// # What it deliberately does NOT claim
//
// It speaks about the OUTSTANDING fire only — the one whose token is still
// redeemable and whose delivery time the entry records. The streak is N fires
// and the entry stores no times for the other N-1, so "this fire" is the honest
// noun and the plural would be a claim the data cannot carry.
//
// It also never says "the agent was down". A turnlog records completed turns;
// zero of them means nothing acked and nothing could have, which covers a
// stopped agent and one wedged mid-turn alike. Both are somebody else's
// detector (internal/absentwatch, internal/ackwatch's blackout arm); the useful
// thing this can say is that the answer is not here.
func annotateUnacked(e scheduler.Entry, ev unackedTurnEvidence) string {
	if !e.Outstanding() || e.PendingSince.IsZero() {
		// Nothing is pending, or the record predates PendingSince. Either way
		// there is no fire to anchor a window to, and inventing one would be
		// the same defect in a new place.
		return ""
	}
	if !ev.Known {
		return fmt.Sprintf("  (no turnlog for %s — turn evidence unavailable)", e.Agent)
	}
	if !ev.Settled {
		return fmt.Sprintf("  (newest fire is under %s old — too early to read turn evidence)", short(ev.Grace))
	}
	if ev.AtFire > 0 {
		return fmt.Sprintf("  (%s completed %d turn(s) in the %s after the newest fire — it was there and did not ack)",
			e.Agent, ev.AtFire, short(ev.Grace))
	}
	if ev.Since > 0 {
		return fmt.Sprintf("  (%s completed NO turn in the %s after the newest fire, and has turned since — delivered into silence)",
			e.Agent, short(ev.Grace))
	}
	return fmt.Sprintf("  (%s has completed NO turn since the newest fire — delivered into silence)", e.Agent)
}

// short renders a duration the way the legend says it, without Go's "3h0m0s".
func short(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return d.String()
}

// gatherTurnEvidence reads one schedule's agent turnlog around its outstanding
// fire. It is the I/O half of the clause; annotateUnacked is the pure half, and
// they are separate so the wording is testable without a filesystem.
//
// It returns the zero value — Known false, rendering as "turn evidence
// unavailable" — for every case it cannot measure: no ⚠ marker to annotate, no
// outstanding fire, no turnlog file, an unreadable one. That direction is the
// one that matters. A reader who is told the evidence is missing goes and
// looks; a reader shown a confident "no turns" over an agent that simply never
// writes a turnlog has been handed the same two-worlds number in new wording.
// turnWindow is the seam gatherTurnEvidence's tests substitute. It is a var
// rather than a parameter so the one production call site cannot accidentally
// be handed a stub, and so a cmd/pogo test never has to point POGO_HOME at a
// throwaway tree to exercise the logic — internal/turnlog owns that envelope
// and its own positive control for it.
var turnWindow = turnlog.Window

func gatherTurnEvidence(e scheduler.Entry, now time.Time) unackedTurnEvidence {
	ev := unackedTurnEvidence{Grace: turnlog.DefaultMaxAge}
	if e.UnackedStreak < scheduler.DefaultStallThreshold {
		return ev
	}
	if !e.Outstanding() || e.PendingSince.IsZero() {
		return ev
	}
	end := e.PendingSince.Add(ev.Grace)
	atFire, err := turnWindow(e.Agent, e.PendingSince, end)
	if err != nil {
		return ev
	}
	ev.Known = true
	ev.AtFire = atFire
	// Settled asks whether the window has actually elapsed. Without it a fire
	// delivered ten minutes ago reads zero turns "in the 3h after the fire" and
	// the clause would announce silence over an agent that is simply not late
	// yet — the defect this whole change is about, rebuilt one level down.
	ev.Settled = !now.Before(end)
	if ev.Settled {
		// One second past the boundary: the turnlog is second-resolution, so
		// this keeps the two windows disjoint and stops a single turn landing
		// exactly on `end` from being counted in both.
		if since, err := turnWindow(e.Agent, end.Add(time.Second), now); err == nil {
			ev.Since = since
		}
	}
	return ev
}
