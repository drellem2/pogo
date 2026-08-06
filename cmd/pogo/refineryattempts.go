package main

import (
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/refinery"
)

// formatMRAttempts renders the per-attempt failure record for
// `pogo refinery show` (mg-e5c2).
//
// It answers three questions that the 2026-08-05 incident could not answer from
// any operator surface:
//
//  1. HOW HARD DID IT TRY? All thirty-one failures read `failure_count=1` and
//     nothing said whether a retry had been attempted, so "failed once" and
//     "gave up after three" were the same sentence. Here they are different
//     blocks.
//  2. WHY WAS THERE NO RETRY? A missing retry that says nothing looks exactly
//     like a policy that does not exist — which is what it was. Every attempt
//     with no successor prints "NOT RETRIED — <reason>".
//  3. OVER WHAT TRANSPORT, AND WHAT DID THE FAR END ACTUALLY SAY? 20 of the 31
//     failures were ssh reporting `Undefined error: 0` and 11 were HTTPS
//     reporting `Could not resolve host: github.com`. The HTTPS half named the
//     cause outright and several readers, sampling only the ssh subset, produced
//     two confident wrong mechanisms over several hours. So the transport is
//     printed on every attempt line and the error is printed VERBATIM, never as
//     a normalised summary.
//
// Returns "" when there is nothing to show — a first-attempt success.
func formatMRAttempts(mr *refinery.MergeRequest) string {
	if mr == nil || (len(mr.Attempts) == 0 && mr.RecoveredOnAttempt == 0) {
		return ""
	}
	var b strings.Builder

	switch {
	case mr.RecoveredOnAttempt > 1:
		// A retried success names the attempt that won. A silent retry turns a
		// flaky host into an invisible one.
		fmt.Fprintf(&b, "\n--- Attempts: %d (MERGED on attempt %d after %d failed) ---\n",
			mr.AttemptCount, mr.RecoveredOnAttempt, mr.RecoveredOnAttempt-1)
		if mr.RetryBackoffSeconds > 0 {
			fmt.Fprintf(&b, "Backoff:   %.0fs slept across the retries\n", mr.RetryBackoffSeconds)
		}
	case len(mr.Attempts) == 1:
		fmt.Fprintf(&b, "\n--- Attempts: 1 (failed once, no retry) ---\n")
	default:
		fmt.Fprintf(&b, "\n--- Attempts: %d (failed after %d attempts) ---\n",
			len(mr.Attempts), len(mr.Attempts))
	}

	for _, a := range mr.Attempts {
		fmt.Fprintf(&b, "\n  #%d  %s  stage=%s  class=%s  transport=%s\n",
			a.Attempt, a.Time.Format("15:04:05"), orUnknown(a.Stage), orUnknown(string(a.Class)), orUnknown(a.Transport))
		if a.Command != "" {
			fmt.Fprintf(&b, "      command:   %s\n", a.Command)
		}
		if a.Remote != "" {
			fmt.Fprintf(&b, "      remote:    %s\n", a.Remote)
		}
		if a.Signal != "" {
			fmt.Fprintf(&b, "      matched:   %q\n", a.Signal)
		}
		switch {
		case a.Retried && a.BackoffSeconds > 0:
			fmt.Fprintf(&b, "      retried:   yes, after %.0fs of backoff\n", a.BackoffSeconds)
		case a.Retried:
			fmt.Fprintf(&b, "      retried:   yes, immediately\n")
		default:
			fmt.Fprintf(&b, "      retried:   NO — %s\n", orUnknown(a.NotRetriedReason))
		}
		if a.RawError != "" {
			b.WriteString("      raw error (verbatim, as the far end reported it):\n")
			for _, line := range strings.Split(strings.TrimRight(a.RawError, "\n"), "\n") {
				fmt.Fprintf(&b, "        %s\n", line)
			}
		}
	}
	return b.String()
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
