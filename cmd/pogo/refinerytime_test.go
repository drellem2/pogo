package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/refinery"
)

// The acceptance tests for mg-6f5e / drellem2/pogo#109.
//
// The criterion is not "a Z appears in the output". It is the two readings the
// defect actually produced:
//
//  1. `pogo refinery history` and `pogo refinery history --since` print the
//     same merge request's done time an HOUR APART, because the retained
//     window unmarshals the offset stored in refinery-state.json and the
//     --since window parses an events.log written in UTC. Same command, same
//     MR, documented flag.
//  2. An unresolved row prints `done=0001-01-01 00:00`, which reads as a
//     corrupt record rather than as an absent time.
//
// So the fixtures below build the SAME INSTANT in two Locations and require
// the rendered rows to be byte-identical, rather than checking each in
// isolation — a test that only asserted a suffix would pass on output that
// still disagreed with itself.

// plusOneZone is the offset that made the bug visible: a BST host, whose
// refinery-state.json therefore stores +01:00 while its events.log stores Z.
var plusOneZone = time.FixedZone("BST", 3600)

// TestRefineryTimeRendersLabelledUTC covers the two helpers directly.
func TestRefineryTimeRendersLabelledUTC(t *testing.T) {
	// One instant, held in the two Locations the two history paths produce.
	utc := time.Date(2026, 8, 4, 20, 17, 29, 419503000, time.UTC)
	stored := utc.In(plusOneZone) // reads 21:17 on its own clock

	if got, want := refineryTimeMinute(stored), "2026-08-04 20:17Z"; got != want {
		t.Errorf("refineryTimeMinute(stored +01:00) = %q, want %q — this is the retained-window\n"+
			"reading that disagreed with --since by an hour", got, want)
	}
	if got, want := refineryTimeMinute(utc), "2026-08-04 20:17Z"; got != want {
		t.Errorf("refineryTimeMinute(UTC) = %q, want %q", got, want)
	}
	if got, want := refineryTimeSecond(stored), "2026-08-04 20:17:29Z"; got != want {
		t.Errorf("refineryTimeSecond(stored +01:00) = %q, want %q", got, want)
	}
	if got, want := refineryTimeSecond(utc), "2026-08-04 20:17:29Z"; got != want {
		t.Errorf("refineryTimeSecond(UTC) = %q, want %q", got, want)
	}

	// The whole point of the Z is that it is not the host's clock relabelled.
	// If the conversion were dropped, the layout would still print a Z and the
	// digits would be 21:17 — worse than printing them bare, because the label
	// would assert something false.
	for _, got := range []string{refineryTimeMinute(stored), refineryTimeSecond(stored)} {
		if strings.Contains(got, "21:17") {
			t.Errorf("%q renders the value's stored +01:00 offset and labels it Z", got)
		}
	}
}

// TestRefineryTimeZeroRendersAsAbsent is item 2 of the plan posted on the
// issue. The --since path is the only one that emits non-terminal rows
// (internal/refinery/historylog.go sets StatusProcessing and never sets a done
// time), so an in-flight MR was rendering a year-one date.
func TestRefineryTimeZeroRendersAsAbsent(t *testing.T) {
	for name, got := range map[string]string{
		"minute": refineryTimeMinute(time.Time{}),
		"second": refineryTimeSecond(time.Time{}),
	} {
		if got != "-" {
			t.Errorf("%s: zero time rendered as %q, want %q", name, got, "-")
		}
		if strings.Contains(got, "0001-01-01") {
			t.Errorf("%s: an unresolved entry still prints a year-one date (%q), which reads as a\n"+
				"corrupt record rather than as an absent one", name, got)
		}
	}
}

// TestHistoryRowIsIdenticalAcrossTheTwoWindows is the reported defect, stated
// as a test: the retained window and the --since window hand formatHistoryRow
// the same instant in different Locations, and the reader must not be able to
// tell which one they are looking at.
func TestHistoryRowIsIdenticalAcrossTheTwoWindows(t *testing.T) {
	done := time.Date(2026, 8, 4, 20, 17, 29, 0, time.UTC)

	// As unmarshalled from ~/.pogo/refinery-state.json, which stores the
	// offset the daemon was running in.
	retained := refinery.MergeRequest{
		ID: "mr-d9p4fjqtjv1h", Branch: "polecat-6f5e", Author: "mg-6f5e",
		Status: refinery.StatusMerged, DoneTime: done.In(plusOneZone),
	}
	// As reconstructed from ~/.pogo/events.log, which is written .UTC().
	fromLog := retained
	fromLog.DoneTime = done

	gotRetained := formatHistoryRow(retained)
	gotFromLog := formatHistoryRow(fromLog)

	if gotRetained != gotFromLog {
		t.Errorf("`refinery history` and `refinery history --since` disagree about the same MR:\n"+
			"  retained: %s\n  --since:  %s", gotRetained, gotFromLog)
	}
	if !strings.Contains(gotRetained, "done=2026-08-04 20:17Z") {
		t.Errorf("history row does not render the done time as labelled UTC:\n%s", gotRetained)
	}
	if strings.Contains(gotRetained, "21:17") {
		t.Errorf("history row renders the stored +01:00 offset:\n%s", gotRetained)
	}
}

// TestHistoryRowSaysNothingRatherThanYearOne covers the unresolved row on the
// --since path.
func TestHistoryRowSaysNothingRatherThanYearOne(t *testing.T) {
	got := formatHistoryRow(refinery.MergeRequest{
		ID: "mr-inflight", Branch: "polecat-6f5e", Author: "mg-6f5e",
		Status: refinery.StatusProcessing,
	})

	if strings.Contains(got, "0001-01-01") {
		t.Errorf("an unresolved merge request still prints a year-one done time:\n%s", got)
	}
	if !strings.Contains(got, "done=-") {
		t.Errorf("an unresolved merge request should say its done time is absent:\n%s", got)
	}
}

// TestQueueSubmittedTimeIsLabelledUTC covers `pogo refinery queue`, folded in
// so the family is not left half-labelled: a reader who learns "refinery
// prints UTC now" would confidently misread whichever surface was left bare,
// which is worse than today's uniform distrust.
func TestQueueSubmittedTimeIsLabelledUTC(t *testing.T) {
	submitted := time.Date(2026, 8, 4, 20, 17, 0, 0, time.UTC)
	now := submitted.Add(30 * time.Minute)

	out := formatQueue([]refinery.MergeRequest{{
		ID: "mr-queued", Branch: "polecat-6f5e", Author: "mg-6f5e",
		Status: refinery.StatusQueued, RepoPath: "/Users/daniel/dev/pogo",
		SubmitTime: submitted.In(plusOneZone),
	}}, now)

	if !strings.Contains(out, "submitted=2026-08-04 20:17Z") {
		t.Errorf("queue submit time is not rendered as labelled UTC:\n%s", out)
	}
	if strings.Contains(out, "21:17") {
		t.Errorf("queue submit time rendered in the record's stored +01:00 offset:\n%s", out)
	}
}
