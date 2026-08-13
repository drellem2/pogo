package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/scheduler"
)

// The reading that cost 46 hours. `pogo schedule list` is where the mg-a14c
// FLEET DEFICIT was read off, and the column was headed COMPLETED — a claim the
// counter cannot support, because only the newest fire's token is redeemable.
// These tests pin the honest rendering by reading the string a human sees, which
// is the only place the old wording could be caught: it survived three tickets
// that each diagnosed it correctly in a Go package comment nobody renders.

func TestAckCell_StatesTheGap_NotJustTheRatio(t *testing.T) {
	// pm-pogo's row from the ticket, verbatim: 103/302, reported as 34%.
	e := scheduler.Entry{
		ID: "mail-check-pm-pogo", Agent: "pm-pogo", Cron: "*/10 * * * *",
		FiresDelivered: 302, FiresCompleted: 103, EverAcked: true,
	}
	got := renderAckCell(e)

	if !strings.Contains(got, "103/302") {
		t.Errorf("the raw counters must stay — they are what a reader compares across rows: %q", got)
	}
	// 302/103 = 2.93. The gap is the honest unit: it describes a turn length,
	// which is what the ratio measures, instead of a shortfall against a 100%
	// that was never on offer.
	if !strings.Contains(got, "1 ack per 2.9 fires") {
		t.Errorf("the cell does not state the attention gap, so the ratio still reads as a shortfall: %q", got)
	}
}

func TestAckCell_UntrackedIsStillADash_NotZeroOfN(t *testing.T) {
	// Predates this change (mg-a754) and must survive it: absent evidence is not
	// evidence of failure. A schedule that has never acked rendering "0/7" would
	// be the same could-not-look/looked-and-saw-nothing collapse one column over.
	e := scheduler.Entry{ID: "mail-check-new", Agent: "fresh", FiresDelivered: 7}
	if got := renderAckCell(e); got != "—" {
		t.Errorf("an untracked schedule must read —, got %q", got)
	}
}

func TestAckCell_GapIsOmittedWhenNoCycleClosed(t *testing.T) {
	// EverAcked survives a re-registration that zeroed the counters (mg-00d6),
	// so a tracked schedule can legitimately have zero closed cycles. A gap of
	// 0.0 would render as "1 ack per 0.0 fires", which is not a small gap but an
	// absent measurement — exactly the confusion this ticket is about.
	e := scheduler.Entry{ID: "mail-check-mayor", Agent: "mayor", FiresDelivered: 3, EverAcked: true}
	got := renderAckCell(e)
	if strings.Contains(got, "per 0.0") || strings.Contains(got, "1 ack per") {
		t.Errorf("an unmeasured gap must be omitted, not printed as zero: %q", got)
	}
	if !strings.Contains(got, "0/3") {
		t.Errorf("the counters should still render: %q", got)
	}
}

func TestAckCell_StreakKeepsItsWarningAndComesLast(t *testing.T) {
	// UnackedStreak is the number to act on — it is the one that does not
	// saturate — so it stays where the eye lands, after the gap.
	e := scheduler.Entry{
		ID: "mail-check-mayor", Agent: "mayor", FiresDelivered: 37, FiresCompleted: 29,
		EverAcked: true, UnackedStreak: 27,
	}
	got := renderAckCell(e)
	if !strings.Contains(got, "⚠ 27 unacked") {
		t.Fatalf("the streak marker was lost: %q", got)
	}
	if strings.Index(got, "⚠") < strings.Index(got, "1 ack per") {
		t.Errorf("the streak must come after the gap, so the actionable number is last: %q", got)
	}
}

func TestAckCell_BelowStallThreshold_NoWarning(t *testing.T) {
	e := scheduler.Entry{
		ID: "mail-check-pa", Agent: "pa", FiresDelivered: 10, FiresCompleted: 9,
		EverAcked: true, UnackedStreak: 1,
	}
	if got := renderAckCell(e); strings.Contains(got, "unacked") {
		t.Errorf("a streak of 1 is a turn in progress, not a stall: %q", got)
	}
}

func TestAckColumnHeader_DoesNotClaimWorkWasCompleted(t *testing.T) {
	if strings.Contains(strings.ToUpper(ackColumnHeader), "COMPLET") {
		t.Errorf("the header asserts work done, which the counter cannot support: %q", ackColumnHeader)
	}
	if ackColumnHeader != "ACKED" {
		t.Errorf("header = %q, want ACKED", ackColumnHeader)
	}
}

func TestAckColumnLegend_StatesTheCeilingAndRedirectsTheAlarm(t *testing.T) {
	entries := []scheduler.Entry{
		{ID: "mail-check-pm-pogo", Agent: "pm-pogo", FiresDelivered: 302, FiresCompleted: 103, EverAcked: true},
	}
	got := ackColumnLegend(entries)
	if got == "" {
		t.Fatal("a tracked row produced no legend, so the ceiling is stated nowhere the reader looks")
	}
	for _, want := range []string{
		"TOKEN REDEMPTIONS", // what it counts
		"newest fire's token",
		"below 100%",        // the ceiling, stated
		"does not saturate", // where to look instead
		"⚠ N unacked",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("legend is missing %q:\n%s", want, got)
		}
	}
}

func TestAckColumnLegend_OmittedWhenNothingIsTracked(t *testing.T) {
	// A caveat printed unconditionally is a caveat readers learn to skip, and
	// under a table of all-"—" rows it explains a ceiling for numbers nobody is
	// reading. It is attached to the reading, not to the command.
	entries := []scheduler.Entry{
		{ID: "mail-check-new", Agent: "fresh", FiresDelivered: 4},
		{ID: "one-shot-verify", Agent: "fresh", OneShot: true},
	}
	if got := ackColumnLegend(entries); got != "" {
		t.Errorf("no tracked row, so no legend was due:\n%s", got)
	}
}

func TestAckColumnLegend_DoesNotTellTheReaderToRaiseTheFloor(t *testing.T) {
	// mg-a14c's constraint: if the number is an artefact, the artefact is the
	// bug; silencing it is worse than the deficit. The legend must redirect the
	// alarm to a statistic that works, never suggest widening a threshold until
	// the alarm stops.
	entries := []scheduler.Entry{
		{ID: "mail-check-pm-pogo", Agent: "pm-pogo", FiresDelivered: 302, FiresCompleted: 103, EverAcked: true},
	}
	got := strings.ToLower(ackColumnLegend(entries))
	for _, banned := range []string{"raise the floor", "widen", "lower the threshold", "ignore this column"} {
		if strings.Contains(got, banned) {
			t.Errorf("legend suggests silencing rather than reading correctly (%q):\n%s", banned, got)
		}
	}
}

func TestAckCell_OutstandingTokenCountsAsAnOpenCycle(t *testing.T) {
	// The boundary term. A schedule holding a redeemable token has one cycle in
	// flight, and counting only closed cycles would overstate the gap at exactly
	// the moment a reader is most likely to be looking — during a live incident.
	closed := scheduler.Entry{
		ID: "x", Agent: "a", FiresDelivered: 10, FiresCompleted: 2, EverAcked: true,
	}
	open := closed
	open.PendingToken = "9f3c1ab2"
	open.PendingSince = time.Now()

	if closed.AttentionGap() != 5.0 {
		t.Errorf("closed gap = %v, want 5.0 (10 fires / 2 cycles)", closed.AttentionGap())
	}
	if got := open.AttentionGap(); got != 10.0/3.0 {
		t.Errorf("open gap = %v, want 10/3 (10 fires / 3 cycles, one still in flight)", got)
	}
}
