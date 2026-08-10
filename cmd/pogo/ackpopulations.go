package main

// `pogo check-acks --populations` (mg-ddf7): split an ack deficit by the
// MECHANISM that produced it, rather than reporting which schedule is deficient.
//
// This reads events.log rather than the persisted counters, and that is not an
// implementation convenience. A re-registration zeroes a schedule's counters
// (see internal/ackwatch, "Counters reset on re-registration") and the nightly
// redeploy guarantees one, so the deficit a storm produced is erased by the
// restart that follows it. Every reading taken off the live table is therefore a
// quiet-afternoon reading — which is the one regime in which this metric was
// never wrong. The events log is the only place a storm survives.

import (
	"fmt"
	"time"

	"github.com/drellem2/pogo/internal/ackwatch"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/scheduler"
)

// defaultPopulationWindow is how far back --populations looks when --since is
// omitted. A week covers several redeploys, so the window spans regimes the
// counters cannot: the point of this mode is to see across the resets.
const defaultPopulationWindow = 7 * 24 * time.Hour

// runAckPopulations measures and prints the population split. It never exits
// non-zero: this is a measurement, and a measurement that sets an error status
// invites being read as a verdict.
func runAckPopulations(sinceRaw, untilRaw string, asJSON bool) {
	since, err := parsePopulationBound(sinceRaw, time.Now().Add(-defaultPopulationWindow))
	if err != nil {
		cli.ExitWithError(asJSON, fmt.Sprintf("--since: %v", err), cli.ExitError)
	}
	until, err := parsePopulationBound(untilRaw, time.Time{})
	if err != nil {
		cli.ExitWithError(asJSON, fmt.Sprintf("--until: %v", err), cli.ExitError)
	}
	if !until.IsZero() && !until.After(since) {
		cli.ExitWithError(asJSON,
			fmt.Sprintf("--until (%s) must be after --since (%s)",
				until.Format(time.RFC3339), since.Format(time.RFC3339)), cli.ExitError)
	}

	path, err := scheduler.DefaultPath()
	if err != nil {
		cli.ExitWithError(asJSON, fmt.Sprintf("cannot locate scheduler state: %v", err), cli.ExitError)
	}
	logPath := scheduler.EventLogPath(path)

	evs, err := ackwatch.ReadFireTimeline(logPath, since, until)
	if err != nil {
		// An events log we could not read is not an empty measurement. Saying so
		// is the whole discipline this package was written under: a silence that
		// reads as an all-clear is the original bug.
		cli.ExitWithError(asJSON,
			fmt.Sprintf("cannot read %s: %v", logPath, err), cli.ExitError)
	}

	// synthwatch's episodes, joined in so population 1 can be split by CAUSE
	// (mg-772f). A fire that superseded another while the agent's turns were
	// already known to be dying is not evidence about that agent's attention,
	// and without this join it is indistinguishable from one that arrived
	// during a long, healthy turn.
	//
	// An unreadable episode stream is NOT fatal here, unlike the timeline
	// above: the split is complete and correct without it, and refusing to
	// print a measurement we do have because an annotation is missing would
	// trade the finding for the footnote. It is reported instead, so a zero
	// dead-turn count is never mistaken for an acquittal.
	episodes, epErr := ackwatch.ReadFailureEpisodes(logPath, since, until)
	if epErr != nil && !asJSON {
		fmt.Printf("note: synthetic-failure episodes unreadable (%v);\n"+
			"      population 1 cannot be split by cause below.\n\n", epErr)
	}

	rep := ackwatch.SplitWithEpisodes(evs, episodes)
	if asJSON {
		cli.PrintJSON(rep)
		return
	}
	fmt.Print(rep.Render())
}

// parsePopulationBound parses an RFC3339 window bound, falling back to def when
// raw is empty.
func parsePopulationBound(raw string, def time.Time) (time.Time, error) {
	if raw == "" {
		return def, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not an RFC3339 timestamp (e.g. 2026-07-29T15:00:00Z)", raw)
	}
	return t, nil
}
