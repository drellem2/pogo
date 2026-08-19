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
// # EVERY SENTENCE HERE NAMES THE NEAR END (mg-4e02)
//
// What this instrument measures is which of the channels in Channels() carried a
// verdict. What it used to print was "the filer never received a verdict", which
// quantifies over every channel there is — including one that had gone live two
// hours earlier. So the channel set is ENUMERATED above the findings, DELIVERED
// names the channel that carried it, and no line below claims a verdict reached
// nobody.
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
	fmt.Fprintf(&b, "  DELIVERED            : %d   (%s)\n", r.Delivered, r.byChannel())
	// THE DROPPED COUNT IS NEVER ALONE. Its two classes are different work: a
	// ROUTING row can be discharged right now from a verdict that already exists,
	// and only a LOST row has no outcome recorded anywhere.
	fmt.Fprintf(&b, "  DROPPED              : %d   (ROUTING %d — the verdict is recorded and can be handed over as it stands;\n",
		r.Dropped, r.Routing)
	fmt.Fprintf(&b, "                              LOST %d — no verdict recorded either, the only unrecoverable class)\n", r.Lost)
	if r.Unreachable > 0 {
		fmt.Fprintf(&b, "                         of those %d, %d had NO REACHABLE FILER — nobody to tell, as against nobody told\n",
			r.Dropped, r.Unreachable)
	}
	fmt.Fprintf(&b, "  UNDECIDABLE          : %d   (no polecat-* branch in the result sidecar)\n", r.Undecidable)
	if r.CollapsedCopies > 0 {
		fmt.Fprintf(&b, "  (%d extra on-disk cop%s of an already-seen item collapsed; each work item is judged once)\n",
			r.CollapsedCopies, plural(r.CollapsedCopies, "y", "ies"))
	}

	// The enumeration. A finding that quantifies over channels is only as honest
	// as its list of them, so the list is printed with the finding rather than
	// documented somewhere the reader of this output is not.
	b.WriteString("\nCHANNELS CHECKED — a DROPPED row below means NONE OF THESE carried a verdict to\n" +
		"the filer, and that is the whole of the claim:\n")
	for _, ch := range r.Channels {
		fmt.Fprintf(&b, "  %-13s %2d delivered  %s\n", ch.Channel, ch.Delivered, ch.Looked)
	}
	b.WriteString("Each is a SENDER plus that sender's own notice shape — not any mail mentioning\n" +
		"the item. A relayed headline is a mention of a verdict, not a verdict, and the\n" +
		"question here is who discharged the obligation. A channel absent from this list\n" +
		"is not measured by this run and nothing below is a claim about it.\n" +
		"The refinery's MERGED mail is deliberately NOT one of them: it reports a merge,\n" +
		"not an outcome — which is also why pogod no longer withholds its own notice from\n" +
		"the coordinator on the merge route. It did until mg-da12, on the premise that the\n" +
		"refinery's mail covered it, and a coordinator's merge-route rows were dropped here\n" +
		"by construction. They are now measured like anyone else's.\n")

	for _, agent := range r.MissingBoxes {
		fmt.Fprintf(&b, "  !! filer %s has NO MAILBOX — no channel could have reached it, so its rows read\n"+
			"     UNREACHABLE rather than untold\n", agent)
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
		b.WriteString("\nDROPPED — the item landed and no channel above carried a verdict to the filer.\n" +
			"Oldest landing first (elsewhere = mails this worker sent to somebody who was not\n" +
			"the filer; reach = whether the filer could have been reached at all):\n")
		fmt.Fprintf(&b, "  %-21s %-9s %-13s %-8s %-9s %9s  %-10s title\n",
			"landed", "item", "filer", "worker", "kind", "elsewhere", "reach")
		for _, row := range dropped {
			fmt.Fprintf(&b, "  %-21s %-9s %-13s %-8s %-9s %9d  %-10s %s\n",
				or(row.Landed, "?"), row.ID, row.Filer, or(row.Worker, "?"),
				row.Kind, row.MailsElsewhere, row.Reach, truncate(row.Title, 76))
			b.WriteString(row.renderVerdict())
		}
		b.WriteString("\nREPORTED, NOT REPAIRED. This command never files the missing verdict and\n" +
			"never mails on anyone's behalf — re-sending would have to forge a sender.\n")
		if r.Routing > 0 || r.Lost > 0 {
			fmt.Fprintf(&b, "The two classes above are DIFFERENT WORK: %d ROUTING row(s) can be discharged now\n"+
				"from the verdict printed with them, and %d LOST row(s) have no recorded outcome to\n"+
				"hand over at all.\n", r.Routing, r.Lost)
		}
		if r.Lost > 0 {
			// THIS REPORT IS A RECONSTRUCTION, AND IT IS NOT THE ONLY READING ANY
			// MORE (mg-c456). Every row above was rebuilt from the store after the
			// fact, which is why the LOST column could grow for weeks with no
			// owner: at the instant a verdict is lost the system's own answer is
			// success — the merge succeeded, the item closed, and the worker's
			// later `mg done --result` was refused only because the item was
			// already terminal, a refusal the protocol scores as completion.
			//
			// pogod now records one class of that loss AS IT HAPPENS, and what that
			// adds is NOT the accrual rate: this report's `landed` column already
			// gives a rate, which is how the census that produced mg-c456 got its
			// per-day table. What it adds is an observation the system makes ITSELF,
			// without anyone running a reconstruction — and one field no
			// reconstruction can recover, `worker_live_at_close`, because whether a
			// worker was registered and about to be stopped is not a fact the store
			// keeps.
			//
			// AND IT HEDGES ITS OWN RECOMMENDATION, because an empty
			// `pogo events list --type=...` is the exact shape this ticket sits
			// next to: an instrument answering cleanly about the wrong thing. Zero
			// rows means "no such close in the window" OR "the daemon emitting it
			// has not been restarted onto the revision that does" — and on the day
			// this shipped it was the second, since the running pogod was 1ebf2dc.
			// A report that advertises a counter without saying that has handed a
			// reader a guaranteed-constant and called it a measurement.
			b.WriteString("\nThe rows above were RECONSTRUCTED after the fact, which is why this column could\n" +
				"grow unowned: at the moment of loss the system's own answer is success. pogod now\n" +
				"records one class of it as it happens — the auto-done closes it performs with no\n" +
				"author verdict on the merge request:\n\n" +
				"  pogo events list --since=24h --type=work_item_closed_without_verdict\n\n" +
				"ZERO ROWS FROM THAT IS NOT ZERO LOSSES. It reads zero just as cleanly on a pogod\n" +
				"that predates the event, and a merged control is not a running one — check the\n" +
				"revision actually serving before concluding anything from an empty result:\n" +
				"  curl -s http://127.0.0.1:10000/version | jq -r .revision\n\n" +
				"That is not a second way to count these rows; `landed` above already gives a rate.\n" +
				"It is an observation the system makes itself, and it carries one thing no\n" +
				"reconstruction can recover: whether a worker was live at the close, so a shut\n" +
				"window is told apart from a hand-submitted branch nobody had a verdict for.\n" +
				"It does NOT cover every LOST row — a worker that won the race and wrote a\n" +
				"verdict-free result of its own is invisible to it, and is counted only here.\n")
		}
	}

	if !quiet && verbose {
		if deliv := r.DeliveredRows(); len(deliv) > 0 {
			b.WriteString("\nDELIVERED, with the channel that carried it — `worker-mail` means a polecat did\n" +
				"its job and `pogod-notify` means a backstop caught it, which are different facts\n" +
				"about fleet health:\n")
			for _, row := range deliv {
				fmt.Fprintf(&b, "  %-21s %-9s %-13s %s\n",
					or(row.Landed, "?"), row.ID, row.DeliveredBy, truncate(row.VerdictMail.Subject, 62))
			}
		}
		if undec := r.UndecidableRows(); len(undec) > 0 {
			b.WriteString("\nUNDECIDABLE — worker not resolvable from mg's own store:\n")
			for _, row := range undec {
				fmt.Fprintf(&b, "  %-21s %-9s %-13s %s\n",
					or(row.Landed, "?"), row.ID, row.Filer, truncate(row.Title, 70))
				b.WriteString(row.renderVerdict())
			}
		}
	}

	return b.String()
}

// renderVerdict prints what the filer should have been told, for a row nobody was
// told about.
//
// THE VERDICT IS PRINTED, NOT POINTED AT. This package has the sidecar open to
// resolve the worker, so the outcome is already in hand; a row that says "nobody
// was told" and also says what they should have been told is recoverable in one
// pass. The path is emitted too, and the command that prints the whole thing —
// and both are the file this run actually read, because A RETRIEVAL INSTRUCTION
// THIS TOOL EMITS IS A CLAIM THAT THE ARTIFACT IS THERE. When such a claim is
// wrong the resulting negative does not stay inside the tool: it travels as the
// evidence of whoever repeated it.
// The label is taken from the row's own Class rather than from the presence of a
// verdict, because those are two different statements and printing one under the
// other's name is the defect this whole change is about: an UNDECIDABLE row with
// nothing on disk is not the LOST class, it is a row this detector could not reach.
func (r Row) renderVerdict() string {
	if r.Verdict == nil {
		if r.Class == ClassLost {
			return "      LOST — no verdict recorded on disk either; there is nothing to hand over,\n" +
				"             and this is the only class that is an actual loss\n"
		}
		return "      no verdict recorded on disk for this row either\n"
	}
	var b strings.Builder
	word := r.Verdict.Word
	if word == "" {
		word = "(recorded, in a shape that names no verdict word)"
	}
	if r.Class == ClassRouting {
		b.WriteString("      ROUTING — the verdict EXISTS. This is what the filer should have been told:\n")
	} else {
		b.WriteString("      the sidecar DOES record a verdict, though this row's worker could not be\n" +
			"      resolved. This is what it says:\n")
	}
	fmt.Fprintf(&b, "             %s", word)
	if r.Verdict.Summary != "" {
		fmt.Fprintf(&b, " — %s", truncate(oneLine(r.Verdict.Summary), 160))
	}
	b.WriteString("\n")
	if r.Verdict.CompletedBy != "" {
		fmt.Fprintf(&b, "             closed by %s", r.Verdict.CompletedBy)
		if r.Verdict.CompletedBy == "refinery" {
			// The merge route never asks the worker to mail anyone, so a DROPPED row
			// against it is a gap in the notification path and not a worker that
			// vanished. Saying so stops the reader chasing a polecat.
			b.WriteString(" (the merge route: nothing on it asks the worker to mail anyone)")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "             read it in full:  jq -r '.verdict' %s\n", r.Verdict.Sidecar)
	return b.String()
}

// byChannel renders the per-channel delivery counts, which is what makes
// DELIVERED an answer rather than a number.
func (r Report) byChannel() string {
	parts := make([]string, 0, len(r.Channels))
	for _, ch := range r.Channels {
		parts = append(parts, fmt.Sprintf("%s %d", ch.Channel, ch.Delivered))
	}
	if len(parts) == 0 {
		return "no channels checked"
	}
	return strings.Join(parts, ", ")
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

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncate cuts to n characters, counted in RUNES rather than bytes.
//
// Byte truncation splits a multi-byte character and renders the replacement
// glyph, which the verdict summaries make visible: every one of them is a
// sentence of prose with em dashes in it, and a report whose recovery text ends
// in mojibake reads as corrupt data rather than as a truncated line.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
