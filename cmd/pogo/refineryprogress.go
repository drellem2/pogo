package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/client"
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
	// The CPU line is printed for a running gate whether or not the gate is
	// talking. It is the second half of the pair: output says whether the gate
	// is TALKING, this says whether it is WORKING, and only together do they
	// separate a slow gate from a stopped one (mg-0c51).
	if p.EndTime.IsZero() {
		fmt.Fprintf(&b, "Gate CPU:  %s\n", p.CPUSummary())
	}
	if !p.TimeoutAt.IsZero() && p.EndTime.IsZero() {
		fmt.Fprintf(&b, "Timeout:   %s (in %s)\n", p.TimeoutAt.Format("15:04:05"),
			p.TimeoutAt.Sub(now).Round(time.Second))
	}
	fmt.Fprintf(&b, "Verdict:   %s\n", p.Diagnosis(now))
	return b.String()
}

// formatQueue renders `pogo refinery queue`.
//
// The defect it fixes (mg-0c51) is not a missing field but a missing ROW: the
// endpoint used to list only pending requests, so the one merge actually
// moving was the one merge not shown. Two `status=queued` rows, unchanged
// across polls, with no in-flight row and nothing saying a merge was running,
// is byte-for-byte what a wedged refinery looks like. An operator nearly
// restarted pogod over a gate that was two minutes into a healthy run.
//
// So this function's job is to make the two states LOOK different:
//
//   - Something in flight, healthy → a processing row with a progress line
//     under it whose verdict says waiting is correct.
//   - Something in flight, suspect → the same row, with a verdict that says
//     so, derived from output staleness AND process-subtree CPU together.
//   - Nothing in flight, but rows queued → said outright, because that is the
//     genuinely alarming arrangement and it must not read like the first.
func formatQueue(queue []refinery.MergeRequest, now time.Time) string {
	var b strings.Builder
	if len(queue) == 0 {
		return "No merge requests: nothing in flight, nothing pending.\n"
	}

	processing := -1
	for i, mr := range queue {
		if mr.Status == refinery.StatusProcessing {
			processing = i
			break
		}
	}

	pending := len(queue)
	if processing >= 0 {
		pending--
	}

	for i, mr := range queue {
		line := fmt.Sprintf("%-12s  branch=%-30s  author=%-15s  status=%-10s  submitted=%s",
			mr.ID, mr.Branch, mr.Author, string(mr.Status), mr.SubmitTime.Format("2006-01-02 15:04"))
		if i != processing {
			// Position in the whole pipeline, so a long-queued row reads as
			// "waiting behind N" rather than as "ignored". The array is
			// processing-first, so the index IS the number ahead.
			line += "  (" + aheadNote(i) + ")"
		}
		fmt.Fprintln(&b, line)
		if i == processing {
			for _, l := range progressLines(&mr, now) {
				fmt.Fprintf(&b, "    %s\n", l)
			}
		}
	}

	if processing < 0 && pending > 0 {
		// The alarming case, stated rather than implied. Before this line
		// existed, its rendering was identical to the healthy one.
		fmt.Fprintf(&b, "\nNOTHING IN FLIGHT: %s pending and no merge request is being processed.\n",
			plural(pending, "merge request"))
		fmt.Fprintf(&b, "A refinery that is running picks the next one up within its poll interval, so if\n")
		fmt.Fprintf(&b, "this persists across polls, check 'pogo refinery status' — a stopped or disabled\n")
		fmt.Fprintf(&b, "refinery leaves the queue exactly like this.\n")
	}
	return b.String()
}

// formatQueuePosition explains why a queued merge request has not moved, by
// naming what is in front of it. Fetching the pipeline is a second round trip,
// so a failure here degrades to a stated "could not read" rather than to
// silence — an absent explanation is what made a normal wait look like neglect.
func formatQueuePosition(id string, now time.Time) string {
	queue, err := getRefineryQueue()
	if err != nil {
		return fmt.Sprintf("Position:  could not be read (%v) — so whether this request is waiting behind\n"+
			"           another merge or being ignored is UNKNOWN from this view.\n", err)
	}
	return formatQueuePositionFrom(queue, id, now)
}

// getRefineryQueue is overridden in tests.
var getRefineryQueue = client.GetRefineryQueue

// formatQueuePositionFrom is the pure half of formatQueuePosition.
func formatQueuePositionFrom(queue []refinery.MergeRequest, id string, now time.Time) string {
	idx := -1
	var active *refinery.MergeRequest
	for i := range queue {
		if queue[i].ID == id {
			idx = i
		}
		if queue[i].Status == refinery.StatusProcessing {
			active = &queue[i]
		}
	}
	if idx < 0 {
		return "Position:  not in the current pipeline — it may have just been picked up or resolved.\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Position:  %s\n", aheadNote(idx))
	if active == nil {
		fmt.Fprintf(&b, "           NOTHING IS IN FLIGHT. Nothing is being processed ahead of this request,\n")
		fmt.Fprintf(&b, "           so it is not waiting on a merge — check 'pogo refinery status'.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "           Waiting behind %s (%s), which is:\n", active.ID, active.Branch)
	for _, l := range progressLines(active, now) {
		fmt.Fprintf(&b, "             %s\n", l)
	}
	return b.String()
}

// aheadNote renders a pending request's place in line.
func aheadNote(ahead int) string {
	if ahead == 0 {
		return "next up"
	}
	return plural(ahead, "merge request") + " ahead"
}

// progressLines renders the in-flight request's liveness under its queue row.
// Returns a line saying so when there is no progress record yet, because a
// processing row with nothing under it is the ambiguity this replaces.
func progressLines(mr *refinery.MergeRequest, now time.Time) []string {
	if mr.StartTime.IsZero() && mr.Progress == nil {
		return []string{"in flight, no step reported yet"}
	}
	var lines []string
	if !mr.StartTime.IsZero() {
		lines = append(lines, fmt.Sprintf("in flight for %s", now.Sub(mr.StartTime).Round(time.Second)))
	}
	p := mr.Progress
	if p == nil || !p.EndTime.IsZero() {
		// Between steps: the gates are done (or have not started) and the
		// merge is in push/deploy. Say that rather than reprint a finished
		// gate's numbers as if they were current.
		lines = append(lines, "not inside a quality gate right now (setup, push, or deploy)")
		return lines
	}
	gate := ellipsize(p.Gate, 60)
	if p.GateCount > 1 {
		gate = fmt.Sprintf("%s (%d/%d)", gate, p.GateIndex, p.GateCount)
	}
	out := "no output yet"
	if age, spoke := p.OutputAge(now); spoke {
		out = fmt.Sprintf("%d lines, last %s ago", p.OutputLines, age.Round(time.Second))
	}
	lines = append(lines, fmt.Sprintf("step=%s gate=%s elapsed=%s", p.Step, gate, p.Elapsed(now).Round(time.Second)))
	lines = append(lines, fmt.Sprintf("output: %s   |   cpu: %s", out, p.CPUSummary()))
	lines = append(lines, "→ "+p.Diagnosis(now))
	return lines
}

// ellipsize keeps a one-line summary readable when a gate command is a whole
// shell pipeline. The full command is still printed by 'pogo refinery show'.
func ellipsize(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// plural renders "1 thing" / "2 things" for counts in operator-facing text.
func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
