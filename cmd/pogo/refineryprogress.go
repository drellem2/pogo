package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/health"
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
	// .UTC() and a trailing Z on every clock time here (mg-0235). Bare
	// "15:04:05" prints whatever Location the value was deserialized into, and
	// a reader holding a UTC clock read "started 18:51:05" at 18:28 and
	// concluded the gate had started IN THE FUTURE — which reads as a corrupt
	// record rather than as a missing zone label, and sent the diagnosis
	// somewhere it did not need to go. These three lines are also read next to
	// the Elapsed duration beside them, so they have to agree with it.
	fmt.Fprintf(&b, "Running:   %s (started %s)\n", p.Elapsed(now).Round(time.Second),
		p.StartTime.UTC().Format("15:04:05Z"))
	if !p.EndTime.IsZero() {
		fmt.Fprintf(&b, "Finished:  %s\n", p.EndTime.UTC().Format("15:04:05Z"))
	}
	if !p.TimeoutAt.IsZero() && p.EndTime.IsZero() {
		fmt.Fprintf(&b, "Timeout:   %s (in %s)\n", p.TimeoutAt.UTC().Format("15:04:05Z"),
			p.TimeoutAt.Sub(now).Round(time.Second))
	}
	// The evidence, one row per instrument, each labelled with the LAYER it
	// measures. Before mg-48d8 these were four unlabelled lines — "Heartbeat:
	// 33s ago" next to "Gate says: 124 lines, last 26m ago" — and the reader was
	// left to know unaided that the first is written by the supervisor and the
	// second by the thing being supervised. One did not; the freshness of the
	// heartbeat was read as the health of the gate.
	//
	// Host contention is printed for every sampled run, contended or not,
	// because the useful reading of "this gate took 40 minutes" needs to know
	// the host was quiet just as much as it needs to know the host was full. An
	// absent row would mean "not measured", and that is a third thing.
	for _, s := range p.Signals(now) {
		fmt.Fprintln(&b, "  "+signalLine(s))
	}
	fmt.Fprintf(&b, "Verdict:   %s\n", p.Diagnosis(now))
	// The gate's own TEXT, for a gate that is still running — the one place it
	// is reachable before the merge resolves (mg-9adc). Every row above this
	// point is metadata ABOUT the output: how many lines, how long ago, at what
	// CPU. None of them can tell a compute phase from a hang, because none of
	// them says what the gate was doing when it went quiet.
	//
	// Skipped once the record is sealed: `refinery show` prints the gate's full
	// output below this block for a finished merge, and a bounded excerpt above
	// an unbounded transcript is noise at best and a second, disagreeing copy at
	// worst.
	if p.EndTime.IsZero() {
		fmt.Fprintln(&b, "\n--- Gate output so far ---")
		for _, l := range p.OutputExcerpt.Report() {
			fmt.Fprintln(&b, l)
		}
	}
	return b.String()
}

// signalLine renders one layer-attributed measurement.
//
// The layer is the first token on the line and the widths are fixed, so the
// column of RUNNER / GATE / HOST reads down the page. That column is the whole
// mechanism: a reader skimming for "is it alive" has to pass the word naming
// what the aliveness belongs to before reaching the state.
func signalLine(s refinery.Signal) string {
	return fmt.Sprintf("%-6s %-15s %-9s %s", s.Layer, s.Name, s.State, s.Detail)
}

// formatHealthRefinery renders the refinery line of `pogo server status`.
//
// The slot-holder is printed BESIDE the pending count and is never omitted,
// including when there is none — an absent line would read as "not measured",
// and the whole point is that the reader learns whether anything holds the
// slot (mg-48d8).
//
// The line it replaces was `refinery: running  (queue=11, ...)`. That is
// byte-identical for a refinery merging steadily and one that has stopped,
// because the count is of PENDING requests and the request being merged is not
// among them. Six agents read that arrangement as a fleet-wide stall in a
// single evening; two of them went on to reason about network causes for a
// stall that was not happening. It is a defect in the instrument, not in six
// readers.
func formatHealthRefinery(state string, r health.Refinery, now time.Time) string {
	inFlight := "none"
	if r.Processing != "" {
		inFlight = r.Processing
		if !r.ProcessingSince.IsZero() {
			inFlight += fmt.Sprintf(" for %s", now.Sub(r.ProcessingSince).Round(time.Second))
		}
	}
	line := fmt.Sprintf("refinery: %s  (in flight: %s, pending=%d, history=%d, recent_failures=%d)\n",
		state, inFlight, r.QueueLength, r.HistoryLength, r.RecentFailures)
	if r.Processing == "" && r.QueueLength > 0 {
		// The genuinely alarming arrangement, said outright rather than left to
		// be inferred from a count. Before this it rendered as the healthy one.
		line += fmt.Sprintf("          NOTHING IN FLIGHT: %s pending and nothing being merged — "+
			"see 'pogo refinery queue'.\n", plural(r.QueueLength, "request"))
	}
	return line
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

	// Merges run in per-repo lanes, so "N ahead" is counted WITHIN a repo. The
	// whole-pipeline count would tell an author they are fourth in line when
	// three of the four are for repos that cannot delay them at all — the
	// serialisation this view now has to stop implying (mg-37ad).
	inFlight := 0
	aheadInLane := make(map[string]int, len(queue))
	seenInLane := make(map[string]int, len(queue))
	for _, mr := range queue {
		lane := refinery.RepoLane(mr.RepoPath)
		aheadInLane[mr.ID] = seenInLane[lane]
		seenInLane[lane]++
		if mr.Status == refinery.StatusProcessing {
			inFlight++
		}
	}
	pending := len(queue) - inFlight

	for i := range queue {
		mr := queue[i]
		line := fmt.Sprintf("%-12s  repo=%-16s  branch=%-30s  author=%-15s  status=%-10s  submitted=%s",
			mr.ID, refinery.RepoLane(mr.RepoPath), mr.Branch, mr.Author, mr.StatusLabel(),
			refineryTimeMinute(mr.SubmitTime))
		if mr.Status == refinery.StatusProcessing {
			fmt.Fprintln(&b, line)
			for _, l := range progressLines(&mr, now) {
				fmt.Fprintf(&b, "    %s\n", l)
			}
			continue
		}
		// A long-queued row must read as "waiting behind N in its own repo"
		// rather than as "ignored".
		line += "  (" + aheadNote(aheadInLane[mr.ID]) + " in this repo)"
		fmt.Fprintln(&b, line)
	}

	if inFlight == 0 && pending > 0 {
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
	for i := range queue {
		if queue[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "Position:  not in the current pipeline — it may have just been picked up or resolved.\n"
	}

	// Merges run in per-repo lanes, so "ahead of you" means ahead of you IN
	// YOUR REPO. Counting the whole pipeline would tell an author they are
	// third in line when the two merges in front are for repos that cannot
	// delay them by a second — which is a more confident version of the
	// confusion this whole view exists to remove (mg-37ad).
	lane := refinery.RepoLane(queue[idx].RepoPath)
	ahead := 0
	var active *refinery.MergeRequest
	var otherLanes int
	for i := range queue {
		sameLane := refinery.RepoLane(queue[i].RepoPath) == lane
		if queue[i].Status == refinery.StatusProcessing {
			if sameLane {
				active = &queue[i]
			} else {
				otherLanes++
			}
		}
		if i < idx && sameLane {
			ahead++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Position:  %s%s\n", aheadNote(ahead), inLane(lane))
	if active == nil {
		if otherLanes > 0 {
			// Not the alarming case. Merges ARE running; none of them is this
			// repo's, and none of them is why this request is waiting.
			verb := "is"
			if otherLanes > 1 {
				verb = "are"
			}
			fmt.Fprintf(&b, "           Nothing is in flight for %s. %s %s running in other repos, which do\n",
				lane, plural(otherLanes, "merge"), verb)
			fmt.Fprintf(&b, "           NOT block this one — it is waiting for a free lane or the next poll.\n")
			return b.String()
		}
		fmt.Fprintf(&b, "           NOTHING IS IN FLIGHT. Nothing is being processed ahead of this request,\n")
		fmt.Fprintf(&b, "           so it is not waiting on a merge — check 'pogo refinery status'.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "           Waiting behind %s (%s) in the same repo, which is:\n", active.ID, active.Branch)
	for _, l := range progressLines(active, now) {
		fmt.Fprintf(&b, "             %s\n", l)
	}
	return b.String()
}

// inLane renders " in <repo>" for a known lane, and nothing for an unknown
// one. A merge request carrying no repo path would otherwise report its
// position "in .", which is worse than saying nothing.
func inLane(lane string) string {
	if lane == "" || lane == "." {
		return ""
	}
	return " in " + lane
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
	// Two lines of the gate's own words beside the count of them. The count
	// alone is what a reader has been polling for minutes, and it cannot
	// distinguish a compute phase from a hang (mg-9adc). The FIRST line is here
	// as well as the latest because a gate states what it resolved and what it
	// is about to run in its header, and by the time anyone is watching a slow
	// gate that header is far outside any tail.
	if x := p.OutputExcerpt; x.Spoke() {
		lines = append(lines, fmt.Sprintf("said first:  %s", ellipsize(x.First(), 90)))
		lines = append(lines, fmt.Sprintf("said latest: %s", ellipsize(x.Latest(), 90)))
		lines = append(lines, "(these two lines are a summary — 'pogo refinery show "+mr.ID+"' prints the bounded excerpt and states its bound)")
	}
	// The layer-attributed evidence, so the verdict below can be checked
	// against the signals it was derived from rather than taken on trust —
	// and so a reader can see them point in different directions (mg-48d8).
	for _, s := range p.Signals(now) {
		lines = append(lines, signalLine(s))
	}
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
