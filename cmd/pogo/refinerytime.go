package main

import "time"

// Timestamp rendering for the `pogo refinery *` family (mg-6f5e,
// drellem2/pogo#109).
//
// THE DEFECT. A Go time.Time renders in whatever Location it was deserialized
// into, and the refinery's two history paths deserialize differently: the
// retained window is unmarshalled from ~/.pogo/refinery-state.json, whose
// RFC3339 carries the offset it was STORED with, while --since is
// reconstructed from events.log, which is written .UTC(). Formatted with a
// layout carrying no zone designator and no normalisation, `pogo refinery
// history` therefore disagreed with ITSELF by an hour on any non-UTC host,
// through a documented flag:
//
//	pogo refinery history            -> done=2026-08-04 21:17
//	pogo refinery history --since=2d -> done=2026-08-04 20:17
//
// WHY UTC WITH A TRAILING Z, and not .Local(). A Z-suffixed timestamp cannot be
// misread by a reader in any zone. A local one is unambiguous only to someone
// who already knows the host's offset — which an agent, a log reader, or a
// future reader at a different offset does not. The second failure mode of the
// same class cost more than the first: a gate reported as "started 18:51:05",
// read against a UTC clock showing 18:28, looks like it started IN THE FUTURE,
// which invites the conclusion that the RECORD IS CORRUPT rather than that a
// label is missing. UTC is also what the artifacts a reader correlates these
// against are in — events.log, the refinery mail-item epoch ids, and
// auditsuccessors — so it removes the arithmetic instead of relabelling it.
//
// The in-tree precedent for the pattern is auditsuccessors.go, whose comment
// records this exact class ("an hour off under BST") being repaired once
// before; mg-0235 brought refineryprogress and refineryattempts into line and
// added the recurrence check in timelayoutzone_test.go, which these two
// functions are written to satisfy rather than to evade: the layout stays an
// inline literal beside the .UTC() that makes its Z true, because a layout
// hoisted behind a variable is a hole the static check cannot judge.
//
// --json is deliberately untouched and was never defective: it emits RFC3339,
// which carries the offset.
//
// A RESIDUAL DIVERGENCE THAT IS NOT THIS DEFECT, recorded here because the
// tests below assert the two windows render byte-identically and a reader who
// then sees them differ on a live host has good reason to suspect a
// regression. The two sources can hold genuinely DIFFERENT INSTANTS for the
// same merge request — the state file's done time is stamped when the record is
// retained, the event log's when the event is appended. Observed on
// mr-d9pnghqtjv1h244d8430: 2026-08-05T20:14:00.750287+01:00 in the state file
// against 19:13:58.586140Z in the event log, 2.164s apart and straddling a
// minute boundary, so the minute-resolution rows read 19:14Z and 19:13Z. That
// is a provenance difference in the recorded data, not a zone defect, and it is
// out of scope here: normalising it means changing what one of the two sources
// records, not how either is rendered.

// refineryTimeMinute renders a refinery timestamp to the minute, as labelled
// UTC, for the fixed-width table rows of `refinery history` and `refinery
// queue`.
//
// A zero time renders as "-" rather than as 0001-01-01. The --since path
// reconstructs non-terminal rows from the event log (internal/refinery/
// historylog.go), and a StatusProcessing row has no done time at all — it was
// printing a year-one date, which reads as a corrupt record rather than as an
// absent one.
func refineryTimeMinute(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

// refineryTimeSecond is refineryTimeMinute at second resolution, for the
// per-field lines of `refinery show`.
//
// Its zero-time branch is currently UNREACHABLE from all three call sites —
// main.go:4124 and :4127 are already IsZero-guarded, and :4122 renders
// SubmitTime, which is always set. It is kept for symmetry with
// refineryTimeMinute, whose guard is load-bearing, and so a fourth call site
// cannot reintroduce a year-one date. Stated so the "-" is not misread as
// evidence that `refinery show` can emit one.
func refineryTimeSecond(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05Z")
}
