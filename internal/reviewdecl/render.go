package reviewdecl

import (
	"fmt"
	"strings"
	"time"
)

// Render formats a report for human reading. The CLI body and the mail body are
// the same text, so what a coordinator is mailed is exactly what they can
// re-derive on demand with `pogo check-review-decl`.
//
// EVERY PATH THROUGH THIS FUNCTION ENDS AT coverage(), including the clean one.
// mg-253e asked for the population to be stated and not just the count missing,
// because "0 missing" over an unstated denominator is not a pass — it is the
// reading a detector produces when it has silently scanned nothing. The clean
// path is exactly where that guarantee matters and exactly where it would be
// easiest to leave out.
func (r Report) Render() string {
	var b strings.Builder

	if len(r.Missing) > 0 {
		fmt.Fprintf(&b, "NO REVIEW DECLARATION — %d review ticket(s) filed since the convention landed\n"+
			"carry no `reviews:` line, so the done-reaper's exemption cannot fire for them:\n\n", len(r.Missing))
		for _, f := range r.Missing {
			writeItem(&b, f)
		}
		b.WriteString("Their builder is reapable the moment its item goes terminal and its polecat\n" +
			"goes quiet — exactly the gh#131 failure mg-aaf6 exists to prevent. The repair is\n" +
			"one line in the LEADING carrier block of the review ticket, above the blank line:\n\n" +
			"  reviews: <build-item-id>\n\n" +
			"It must be a BARE id and it must be inside the leading block: a value with a\n" +
			"parenthetical, or a block that does not lead the body, is read as no declaration\n" +
			"at all (mg-27d4).\n\n" +
			"THIS IS REPORT-ONLY and the line was NOT written for you. A coordinator that\n" +
			"skipped it may have had a reason, and a detector that repaired the thing it\n" +
			"measures could never again be trusted to measure it.\n\n")
	}

	if len(r.SelfReference) > 0 {
		fmt.Fprintf(&b, "SELF-REFERENCE — %d review ticket(s) declare THEMSELVES:\n\n", len(r.SelfReference))
		for _, f := range r.SelfReference {
			writeItem(&b, f)
		}
		b.WriteString("donereap refuses a self-reference by name (cmd/pogod/donereap.go:384): it would\n" +
			"exempt a polecat from its own completion forever. So the line is present and\n" +
			"protects nothing. Point it at the BUILD item.\n\n")
	}

	if len(r.Malformed) > 0 {
		fmt.Fprintf(&b, "UNUSABLE VALUE — %d `reviews:` line(s) are not a bare work-item id:\n\n", len(r.Malformed))
		for _, f := range r.Malformed {
			writeItem(&b, f)
		}
		b.WriteString("The exemption matches by EXACT STRING EQUALITY against a live polecat's work\n" +
			"item id, so a value carrying a parenthetical, a URL or any prose can never match\n" +
			"and the builder is as unprotected as if the line were absent. Strip it to the id.\n\n")
	}

	if len(r.Undatable) > 0 {
		fmt.Fprintf(&b, "UNDATABLE — %d review ticket(s) have no `reviews:` line AND no readable\n"+
			"`created:` stamp, so the convention boundary cannot be applied to them:\n\n", len(r.Undatable))
		for _, f := range r.Undatable {
			writeItem(&b, f)
		}
		b.WriteString("These are NOT resolved to either side. An item filed before the convention is\n" +
			"clean and one filed after is a finding, and without a stamp this scan cannot say\n" +
			"which — so it says that, rather than picking the side that makes the count look\n" +
			"better. Check them by hand.\n\n")
	}

	if len(r.PreConvention) > 0 {
		fmt.Fprintf(&b, "pre-convention — %d review ticket(s) with no `reviews:` line, filed BEFORE\n"+
			"%s and therefore not findings:\n\n", len(r.PreConvention), stamp(r.Boundary))
		for _, f := range r.PreConvention {
			fmt.Fprintf(&b, "  %s  status=%s  created=%s\n      %s\n", f.Item.ID, f.Item.Status, f.Detail, f.Item.Title)
		}
		b.WriteString("\nListed rather than dropped: an exclusion nobody can see is indistinguishable\n" +
			"from a scan that missed them.\n\n")
	}

	if len(r.Opaque) > 0 {
		fmt.Fprintf(&b, "not classifiable — %d item(s) whose carrier block this parser cannot reach,\n"+
			"so whether any of them is a review ticket is UNKNOWN:\n\n", len(r.Opaque))
		for _, f := range r.Opaque {
			fmt.Fprintf(&b, "  %s  status=%s\n      %s\n", f.Item.ID, f.Item.Status, f.Item.Title)
		}
		b.WriteString("\nNot counted as findings and not alerted on: agent.MGDispatchGate refuses an\n" +
			"item whose carrier cannot be read, so no review polecat is ever spawned against\n" +
			"one and the exemption is never needed. They are listed because they are outside\n" +
			"the denominator below, and an unstated exclusion is the defect this detector is\n" +
			"a member of the family of. Their own repair — move the block under the `# title`\n" +
			"heading — belongs to stallwatch's report, not this one.\n\n")
	}

	if len(r.Declared) > 0 {
		fmt.Fprintf(&b, "declared — %d review ticket(s) carrying a usable `reviews:` line:\n\n", len(r.Declared))
		for _, f := range r.Declared {
			fmt.Fprintf(&b, "  %s  ->  %s  (status=%s)\n", f.Item.ID, f.Detail, f.Item.Status)
		}
		b.WriteString("\n")
	}

	b.WriteString(r.coverage())
	return b.String()
}

// coverage is the denominator paragraph. It is a method rather than inline text
// so that no future branch of Render can return without one.
func (r Report) coverage() string {
	var b strings.Builder
	if !r.Actionable() {
		b.WriteString("no missing review declarations.\n\n")
	}
	fmt.Fprintf(&b, "scanned %d review ticket(s) out of %d work item(s) read", r.Scanned, r.Population)
	if len(r.Statuses) > 0 {
		fmt.Fprintf(&b, " in %s", strings.Join(r.Statuses, ", "))
	}
	b.WriteString(".\n")
	fmt.Fprintf(&b, "  %d declared, %d missing, %d self-reference, %d unusable value, %d undatable.\n",
		len(r.Declared), len(r.Missing), len(r.SelfReference), len(r.Malformed), len(r.Undatable))
	fmt.Fprintf(&b, "  %d excluded as pre-convention (filed before %s, commit c045a9a);\n"+
		"  %d not classifiable (carrier block out of the parser's reach).\n",
		len(r.PreConvention), stamp(r.Boundary), len(r.Opaque))
	if r.Scanned == 0 {
		b.WriteString("\nNOTE: this run evaluated ZERO review tickets. That is a clean result only if the\n" +
			"store genuinely holds none — over a store with review work in flight it is what a\n" +
			"broken scan looks like. Check the population count above before reading the zero\n" +
			"as a pass.\n")
	}
	return b.String()
}

// MailSubject renders the one-line summary. Only called when the report is
// actionable — the subject leads with the number of unprotected builders,
// because that is the fact, and follows it with the denominator, because a count
// without one is what this family keeps being burned by.
func (r Report) MailSubject() string {
	var parts []string
	if n := r.Unprotected(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d review ticket(s) leave their builder unprotected", n))
	}
	if n := len(r.Undatable); n > 0 {
		parts = append(parts, fmt.Sprintf("%d undatable", n))
	}
	return fmt.Sprintf("%s (of %d scanned)", strings.Join(parts, "; "), r.Scanned)
}

func writeItem(b *strings.Builder, f Finding) {
	fmt.Fprintf(b, "  %s  status=%s\n", f.Item.ID, f.Item.Status)
	fmt.Fprintf(b, "      %s\n", f.Item.Title)
	if f.Detail != "" {
		fmt.Fprintf(b, "      reviews: %s\n", f.Detail)
	}
	if !f.Item.Created.IsZero() {
		fmt.Fprintf(b, "      filed %s\n", f.Item.Created.UTC().Format(time.RFC3339))
	}
	b.WriteString("\n")
}
