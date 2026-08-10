package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/refinery"
)

// The failure mail's classification block (mg-e5c2).
//
// The mail is the surface that reaches a coordinator who is not sitting at a
// terminal, so it carries the same three things `pogo refinery show` does: which
// class the failure is, how many attempts were made, and — for every failing
// attempt — the transport and git's RAW output.
//
// The raw output is not an optional extra. On 2026-08-05, 20 of 31 failures were
// ssh reporting `Undefined error: 0` and 11 were HTTPS reporting `Could not
// resolve host: github.com`. Readers who saw only the ssh wording reasoned from
// errno semantics and produced two confident wrong mechanisms over several
// hours; the HTTPS half named the cause outright. A failure record that shows
// one transport's summary can reproduce that, so this one shows neither summary
// nor a single transport.

// refineryFailureClassLabel is the class as it appears in the mail subject,
// falling back to "unclassified" so the subject shape is stable.
func refineryFailureClassLabel(mr *refinery.MergeRequest) string {
	if mr.FailureClass == "" {
		return string(refinery.ClassUnclassified)
	}
	return string(mr.FailureClass)
}

// refineryAttemptSummary states, in one line, how hard the refinery tried and —
// when it stopped — why it stopped. Before mg-e5c2 the mail said only
// "Consecutive failures: 1", which is the AUTHOR's streak and was routinely
// read as the attempt count.
func refineryAttemptSummary(mr *refinery.MergeRequest) string {
	n := mr.AttemptCount
	if n == 0 {
		n = len(mr.Attempts)
	}
	var s string
	switch n {
	case 0:
		s = "no attempt was recorded"
	case 1:
		s = "1 — it failed ONCE and was not retried"
	default:
		s = fmt.Sprintf("%d — it failed after %d attempts", n, n)
	}
	s += "\n" + refineryWaitSummary(mr)
	if mr.NotRetriedReason != "" {
		s += "\nNo further retry: " + mr.NotRetriedReason
	}
	return s
}

// refineryWaitSummary states how long the refinery ACTUALLY slept in retry
// backoff before giving up (mg-c3b7).
//
// The mail previously carried the attempt count and the budget's own wording,
// and those two cannot separate "the network was down for longer than anyone
// could wait" from "we did not really wait". On 2026-08-10 a clean 8m58s gate
// was discarded by a fetch failure, and the mail's reader had to go to the
// refinery log to learn that the whole retry campaign had lasted 52 seconds
// against an outage measured at 15m26s. The waited time is the number that
// makes the budget auditable against the event, so it goes in the mail.
//
// Derived by summing the per-attempt records rather than read from a new
// field, so it is equally correct for merge requests already on disk.
func refineryWaitSummary(mr *refinery.MergeRequest) string {
	var secs float64
	var retried int
	for _, a := range mr.Attempts {
		secs += a.BackoffSeconds
		if a.Retried {
			retried++
		}
	}
	if secs <= 0 {
		return "Waited: 0s in retry backoff — nothing here was retried, so this says nothing about how long the condition lasted."
	}
	return fmt.Sprintf("Waited: %s in retry backoff across %d retried attempt(s) before giving up. Compare that against how long the condition actually lasted: if it outlasted the wait, this is a budget that ran out, not a branch that is broken.",
		time.Duration(secs*float64(time.Second)).Round(time.Second), retried)
}

// refineryAttemptDetail renders one block per failing attempt.
func refineryAttemptDetail(mr *refinery.MergeRequest) string {
	if len(mr.Attempts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n--- Per-attempt record (transport and RAW error, never a normalised summary) ---\n")
	for _, a := range mr.Attempts {
		fmt.Fprintf(&b, "\n#%d  stage=%s  class=%s  transport=%s\n",
			a.Attempt, blankAs(a.Stage, "unknown"), blankAs(string(a.Class), "unknown"), blankAs(a.Transport, "unknown"))
		if a.Command != "" {
			fmt.Fprintf(&b, "    command: %s\n", a.Command)
		}
		switch {
		case a.Retried && a.BackoffSeconds > 0:
			fmt.Fprintf(&b, "    retried: yes, after %.0fs of backoff\n", a.BackoffSeconds)
		case a.Retried:
			b.WriteString("    retried: yes, immediately\n")
		default:
			fmt.Fprintf(&b, "    retried: NO — %s\n", blankAs(a.NotRetriedReason, "no reason recorded"))
		}
		if a.RawError != "" {
			b.WriteString("    raw error:\n")
			for _, line := range strings.Split(strings.TrimRight(a.RawError, "\n"), "\n") {
				fmt.Fprintf(&b, "      %s\n", line)
			}
		}
	}
	return b.String()
}

func blankAs(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
