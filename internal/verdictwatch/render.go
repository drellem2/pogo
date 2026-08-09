package verdictwatch

import (
	"fmt"
	"strings"
)

// Render formats a report for human reading.
//
// The DROPPED table is ordered oldest landing first, and that ordering is the
// point rather than a presentation choice: the original's stated purpose is that
// a backlog can be RECOVERED, not merely alarmed about, and recovery starts with
// the oldest.
//
// verbose adds the DELIVERED and UNDECIDABLE listings (`--all`). quiet drops the
// tables and leaves the counts.
func (r Report) Render(verbose, quiet bool) string {
	var b strings.Builder

	// The banner comes FIRST, because everything below it is arithmetic on a
	// population this run could not establish. A reader who skims one line must
	// come away knowing this is not a report about verdicts.
	if r.InstrumentFailure() {
		fmt.Fprintf(&b, "INSTRUMENT FAILURE — this run measured nothing.\n\n")
		for _, why := range r.Blind {
			fmt.Fprintf(&b, "  %s\n", why)
		}
		b.WriteString("\nThis is NOT a result. Nothing below clears, confirms, or refutes any\n" +
			"previously reported finding — a verdict reported dropped yesterday is still\n" +
			"dropped and is merely invisible to this run.\n\n" +
			"Re-run once the cause above is addressed:\n  pogo check-verdicts\n\n")
	}

	scope := r.Filer
	if scope == "" {
		scope = "ALL FILERS"
	}
	fmt.Fprintf(&b, "check-verdicts — store %s — filer %s", r.Root, scope)
	if r.Since != "" {
		fmt.Fprintf(&b, " — since %s", r.Since)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  landed items scanned : %d\n", r.Scanned)
	fmt.Fprintf(&b, "  DELIVERED            : %d\n", r.Delivered)
	fmt.Fprintf(&b, "  DROPPED              : %d\n", r.Dropped)
	fmt.Fprintf(&b, "  UNDECIDABLE          : %d   (no polecat-* branch in the result sidecar)\n", r.Undecidable)
	if r.CollapsedCopies > 0 {
		fmt.Fprintf(&b, "  (%d extra on-disk cop%s of an already-seen item collapsed; each work item is judged once)\n",
			r.CollapsedCopies, plural(r.CollapsedCopies, "y", "ies"))
	}
	for _, agent := range r.MissingBoxes {
		fmt.Fprintf(&b, "  !! filer %s has NO MAILBOX — every one of its items is dropped by construction\n", agent)
	}

	if r.Scanned == 0 && !r.InstrumentFailure() {
		// A scoped scan that matched nothing is an answer to the question asked,
		// and it says so in words rather than rendering identically to a clean
		// fleet. "Nothing is wrong" and "I judged nothing" must never look alike.
		b.WriteString("\nThis run JUDGED NOTHING: no landed item matched the scope above. That is not\n" +
			"a clean bill of health for anything outside it.\n")
		return b.String()
	}

	dropped := r.DroppedRows()
	if !quiet && len(dropped) > 0 {
		b.WriteString("\nDROPPED VERDICTS, oldest landing first (elsewhere = mails this worker sent\n" +
			"to somebody who was not the filer):\n")
		fmt.Fprintf(&b, "  %-21s %-9s %-13s %-8s %-9s %9s  title\n",
			"landed", "item", "filer", "worker", "kind", "elsewhere")
		for _, row := range dropped {
			fmt.Fprintf(&b, "  %-21s %-9s %-13s %-8s %-9s %9d  %s\n",
				or(row.Landed, "?"), row.ID, row.Filer, or(row.Worker, "?"),
				row.Kind, row.MailsElsewhere, truncate(row.Title, 88))
		}
		b.WriteString("\nREPORTED, NOT REPAIRED. This command never files the missing verdict and\n" +
			"never mails on anyone's behalf — re-sending would have to forge a sender.\n")
	}

	if !quiet && verbose {
		if deliv := r.DeliveredRows(); len(deliv) > 0 {
			b.WriteString("\nDELIVERED:\n")
			for _, row := range deliv {
				fmt.Fprintf(&b, "  %-21s %-9s %s\n",
					or(row.Landed, "?"), row.ID, truncate(row.VerdictMail.Subject, 76))
			}
		}
		if undec := r.UndecidableRows(); len(undec) > 0 {
			b.WriteString("\nUNDECIDABLE — worker not resolvable from mg's own store:\n")
			for _, row := range undec {
				fmt.Fprintf(&b, "  %-21s %-9s %-13s %s\n",
					or(row.Landed, "?"), row.ID, row.Filer, truncate(row.Title, 70))
			}
		}
	}

	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
