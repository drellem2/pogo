package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/refinery"
)

// formatMRProgress renders a merge request's gate-progress record for
// `pogo refinery show`.
//
// This block is the answer to "has this gate hung, or is it just slow?" — the
// question mg-8595 was filed because nothing could answer. It lives in a named
// function rather than inline in the command so it can be tested: the operator
// path is the one that misled a real diagnosis, so it needs a positive control
// of its own, not just the underlying record.
//
// Returns "" when there is no record to show.
func formatMRProgress(p *refinery.StepProgress, now time.Time) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	gate := p.Gate
	if p.GateCount > 1 {
		gate = fmt.Sprintf("%s (%d of %d)", gate, p.GateIndex, p.GateCount)
	}

	fmt.Fprintf(&b, "\n--- Progress: %s ---\n", p.Step)
	fmt.Fprintf(&b, "Gate:      %s\n", gate)
	fmt.Fprintf(&b, "Running:   %s (started %s)\n", p.Elapsed(now).Round(time.Second),
		p.StartTime.Format("15:04:05"))
	if p.EndTime.IsZero() {
		fmt.Fprintf(&b, "Heartbeat: %s ago — beat %d, every %s\n",
			p.HeartbeatAge(now).Round(time.Second), p.Beats, p.HeartbeatInterval)
	} else {
		fmt.Fprintf(&b, "Finished:  %s\n", p.EndTime.Format("15:04:05"))
	}
	if p.LastOutput.IsZero() {
		fmt.Fprintf(&b, "Gate says: nothing yet (0 lines)\n")
	} else {
		fmt.Fprintf(&b, "Gate says: %d lines, last %s ago\n", p.OutputLines,
			now.Sub(p.LastOutput).Round(time.Second))
	}
	if !p.TimeoutAt.IsZero() && p.EndTime.IsZero() {
		fmt.Fprintf(&b, "Timeout:   %s (in %s)\n", p.TimeoutAt.Format("15:04:05"),
			p.TimeoutAt.Sub(now).Round(time.Second))
	}
	fmt.Fprintf(&b, "Verdict:   %s\n", p.Diagnosis(now))
	return b.String()
}
