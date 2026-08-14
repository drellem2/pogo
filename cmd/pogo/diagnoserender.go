package main

import (
	"fmt"
	"io"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/synthfail"
)

// The `pogo agent diagnose` renderings that a human reads and acts on. They
// live here rather than inline in the command so they can be tested: mg-c058
// was a defect entirely in what these lines SAID, and a rendering nothing can
// assert against is a rendering that drifts back.

// healthLine renders the `Health:` row. The value never prints alone.
//
// `failing_turns` is a trailing-window error COUNT, and the guidance built on
// it names only PERSISTENT causes (an expired credential, a rate limit, a spend
// cap) — so the bare token reads as a present-tense statement about the agent's
// capacity. On 2026-08-14 seven of nine agents diagnosed `failing_turns` off two
// network errors apiece while every one of them was completing turns, including
// the mayor that had run the query, and pogod paged a sleeping human off the
// same reading (mg-c058).
func healthLine(health, detail string) string {
	if detail == "" {
		return fmt.Sprintf("Health:         %s\n", health)
	}
	return fmt.Sprintf("Health:         %s (%s)\n", health, detail)
}

// writeFailingTurns renders the failing-turns detail block: what was counted,
// over what window, and the two things the count does NOT say.
func writeFailingTurns(w io.Writer, name string, tc *synthfail.Report) {
	window := tc.WindowString()

	fmt.Fprintf(w, "\n⚠ FAILING TURNS: %s answered %d turns locally and failed them.\n", name, tc.Count)
	fmt.Fprintf(w, "  Counted over a trailing %s window, ending %s.\n", window, tc.ScannedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "  Those turns span %s to %s.\n",
		tc.First.UTC().Format(time.RFC3339), tc.Last.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "  Reason: %s — %s\n", tc.Reason, tc.Reason.Human())
	if tc.Detail != "" {
		fmt.Fprintf(w, "  Harness said: %q\n", tc.Detail)
	}
	// The two misreadings, stated as their negations. Both were made on the
	// night of 2026-08-14, by two different agents, in the ticket about them.
	fmt.Fprintf(w, "  READ THAT AS A COUNT, NOT A RATE. %d failures in a %s window does not\n", tc.Count, window)
	fmt.Fprintf(w, "  mean every turn failed, and this agent may be completing turns right\n")
	fmt.Fprintf(w, "  now — on 2026-08-14 the flagged mayor was the agent that ran the query.\n")
	fmt.Fprintf(w, "  Nor is the window the size of the fault: it can be NARROWER at either\n")
	fmt.Fprintf(w, "  end. Establishing recovery takes a period with no instrument failures,\n")
	fmt.Fprintf(w, "  not one clean probe — every probe lands in a good minute (mg-c058).\n")
	fmt.Fprintf(w, "  This is NOT a wedge: the agent is alive and consuming every nudge on\n")
	fmt.Fprintf(w, "  time, it just accomplishes nothing with them — so delivery counters read\n")
	fmt.Fprintf(w, "  green throughout (mg-18d0).\n")
	fmt.Fprintf(w, "  DO NOT RESTART. A new session inherits the same credential/limit and the\n")
	fmt.Fprintf(w, "  restart discards this session's context. pogod has suppressed\n")
	fmt.Fprintf(w, "  restart-based remediation for this agent and paged `human`.\n")
}

// diagnoseHealthSection is the ordered pair, so a caller cannot print the value
// without whatever qualifies it.
func diagnoseHealthSection(w io.Writer, diag *agent.DiagnoseInfo) {
	fmt.Fprint(w, healthLine(diag.Health, diag.HealthDetail))
	if diag.TranscriptCheck != nil && diag.TranscriptCheck.State == synthfail.StateFailing {
		writeFailingTurns(w, diag.Name, diag.TranscriptCheck)
	}
}
