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

	"github.com/drellem2/pogo/internal/scheduler"
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
func renderAckCell(e scheduler.Entry) string {
	if !e.CompletionTracked() {
		return "—"
	}
	cell := fmt.Sprintf("%d/%d", e.FiresCompleted, e.FiresDelivered)
	if gap := e.AttentionGap(); gap > 0 {
		cell += fmt.Sprintf("  1 ack per %.1f fires", gap)
	}
	if e.UnackedStreak >= scheduler.DefaultStallThreshold {
		cell += fmt.Sprintf("  ⚠ %d unacked", e.UnackedStreak)
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
		"⚠ N unacked, the one number in this column that does not saturate.\n"
}
