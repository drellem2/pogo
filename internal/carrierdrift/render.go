package carrierdrift

import (
	"fmt"
	"strings"
	"time"
)

// Render formats a report for human reading. The CLI body and the mail body are
// the same text, so what a coordinator is mailed about is exactly what they can
// re-derive on demand with `pogo check-carriers`.
func (r Report) Render() string {
	var b strings.Builder

	// The banner comes FIRST when the pass measured nothing, because everything
	// below it is a list of carriers that were not checked. A reader who skims
	// the findings and stops must not be able to come away thinking a scan ran.
	if r.InstrumentFailure() {
		fmt.Fprintf(&b, "NO CARRIER WAS RE-READ — all %d came back without a verdict.\n\n"+
			"This is what a BROKEN INSTRUMENT looks like, not what %d simultaneously drifted\n"+
			"carriers look like. This pass measured NOTHING, and in particular it is not\n"+
			"evidence that anything reported earlier has cleared.\n\n"+
			"Failure class(es): %s\n\n",
			r.Scanned, r.Scanned, strings.Join(r.FailureClasses(), ", "))
	}

	if len(r.Closed) > 0 {
		fmt.Fprintf(&b, "ISSUE CLOSED — %d LIVE carrier(s) whose issue is closed:\n\n", len(r.Closed))
		for _, f := range r.Closed {
			fmt.Fprintf(&b, "  %s  %s  closed %s ago, carrier still %s at stage %s\n",
				f.Carrier.ID, f.Carrier.Ref(), humanAge(f.Age), f.Carrier.Status, stageOr(f.Carrier.Stage))
			fmt.Fprintf(&b, "      %s\n\n", f.Carrier.Title)
		}
		b.WriteString("These are DISPATCHABLE work against a solved problem. Dispatching one sends a\n" +
			"worker at an issue that is already closed and posts an acknowledgement comment on\n" +
			"a closed thread. Resolve the carrier — shelve it with the reasoning on the item,\n" +
			"or complete it — or, if the work really is still owed on a closed issue, record\n" +
			"that on the carrier so it stops being reported:\n\n" +
			"  gh-closed: <why this carrier is live against a closed issue>\n\n")
	}

	if len(r.Unacknowledged) > 0 {
		fmt.Fprintf(&b, "NOT ACKNOWLEDGED — %d open issue(s) whose reporter has heard nothing:\n\n", len(r.Unacknowledged))
		for _, f := range r.Unacknowledged {
			fmt.Fprintf(&b, "  %s  %s  filed %s ago, %d comment(s), none acknowledging it\n",
				f.Carrier.ID, f.Carrier.Ref(), humanAge(f.Age), f.Snapshot.Comments)
			fmt.Fprintf(&b, "      %s\n\n", f.Carrier.Title)
		}
		b.WriteString("Each of these IS carried, and the intake ledger reads clean for every one of\n" +
			"them. That is the point: from the reporter's side, carried-but-silent is\n" +
			"indistinguishable from uncarried. They filed something and nothing has appeared\n" +
			"on the thread either way.\n\n" +
			"An acknowledgement is one line, and the triage workflow already prescribes it\n" +
			"(polecat-triage.md step 3). If the reporter HAS heard from us by a route this\n" +
			"check cannot see, say so on the carrier:\n\n" +
			"  gh-ack: <where the reporter was answered>\n\n")
	}

	if len(r.StuckStage) > 0 {
		fmt.Fprintf(&b, "STAGE NOT ADVANCING — %d carrier(s) still at the stage they were filed at:\n\n", len(r.StuckStage))
		for _, f := range r.StuckStage {
			fmt.Fprintf(&b, "  %s  %s  stage %s for %s\n",
				f.Carrier.ID, f.Carrier.Ref(), stageOr(f.Carrier.Stage), humanAge(f.Age))
			fmt.Fprintf(&b, "      %s\n\n", f.Carrier.Title)
		}
		fmt.Fprintf(&b, "This age is EXACT rather than a lower bound: mg records no stage-change\n"+
			"timestamp, so the check covers only the stages a carrier is FILED at [%s],\n"+
			"where the carrier's own age is that stage's age. Later stages are not covered.\n\n"+
			"Deliberately held? Record it on the carrier:\n\n"+
			"  gh-parked: <why this is held here>\n\n",
			strings.Join(displayStages(r.Windows.Stages), " "))
	}

	if len(r.Blocked) > 0 {
		fmt.Fprintf(&b, "NOT RE-READ — %d carrier(s) the instrument could not check at all:\n\n", len(r.Blocked))
		for _, f := range r.Blocked {
			fmt.Fprintf(&b, "  %s  %s  [%s] %s\n      %s\n\n",
				f.Carrier.ID, f.Carrier.Ref(), f.Class, f.Class.Describe(), f.Detail)
		}
		b.WriteString("These carry NO verdict. Nothing was learned about them, in either direction —\n" +
			"they are listed apart from the findings above so a failure to measure is never\n" +
			"read in the shape of a measurement.\n\n")
	}

	if len(r.Indeterminate) > 0 {
		fmt.Fprintf(&b, "UNRESOLVABLE — %d carrier(s) GitHub answered about, unusably:\n\n", len(r.Indeterminate))
		for _, f := range r.Indeterminate {
			fmt.Fprintf(&b, "  %s  %s  [%s] %s\n      %s\n\n",
				f.Carrier.ID, f.Carrier.Ref(), f.Class, f.Class.Describe(), f.Detail)
		}
		b.WriteString("The instrument worked; the answer about these carriers is not a usable state.\n" +
			"A re-run reproduces it exactly, so these are facts about the carriers — most\n" +
			"often a malformed `gh:` line, a deleted issue, or a repo that has been renamed.\n\n")
	}

	if len(r.Declared) > 0 {
		fmt.Fprintf(&b, "declared — %d carrier(s) held on purpose, listed and not alarmed:\n\n", len(r.Declared))
		for _, f := range r.Declared {
			fmt.Fprintf(&b, "  %s  %s  would be %s — %s\n",
				f.Carrier.ID, f.Carrier.Ref(), f.Suppressed, f.Detail)
		}
		b.WriteString("\nA declaration buys silence from the alert channel, never invisibility:\n" +
			"suppressed-forever-and-forgotten is the same absence this check exists to catch.\n\n")
	}

	fmt.Fprintf(&b, "re-read %d live carrier(s) from %d work item(s) in status [%s]; %d current, %d drifted.\n",
		r.Scanned, r.StoreItems, strings.Join(r.Statuses, " "),
		r.Current, len(r.Closed)+len(r.Unacknowledged)+len(r.StuckStage))
	fmt.Fprintf(&b, "windows: ack %s from the reporter's filing, stage %s at [%s], %s grace after a close.\n",
		humanWindow(r.Windows.Ack), humanWindow(r.Windows.Stage),
		strings.Join(displayStages(r.Windows.Stages), " "), humanWindow(r.Windows.Closed))

	return b.String()
}

// MailSubject renders the one-line summary for the alert channel. Only called
// when the report is actionable.
//
// The subject is the part that travels: it is what a reader skims, forwards and
// files a ticket from. So it names the KIND of drift rather than a count of
// carriers — "3 drifted carriers" would send its reader to look for one thing
// when there are three different remedies in the body.
func (r Report) MailSubject() string {
	var parts []string
	if r.InstrumentFailure() {
		parts = append(parts, fmt.Sprintf("NO CARRIER RE-READ (%d scanned, 0 verdicts; %s)",
			r.Scanned, strings.Join(r.FailureClasses(), ",")))
	}
	if n := len(r.Closed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d live carrier(s) on CLOSED issues: %s", n, refsOf(r.Closed)))
	}
	if n := len(r.Unacknowledged); n > 0 {
		parts = append(parts, fmt.Sprintf("%d reporter(s) with NO acknowledgement: %s", n, refsOf(r.Unacknowledged)))
	}
	if n := len(r.StuckStage); n > 0 {
		parts = append(parts, fmt.Sprintf("%d carrier(s) stuck at their filing stage: %s", n, refsOf(r.StuckStage)))
	}
	// Blocked and indeterminate are named only when they are not already covered
	// by the instrument-failure banner, so a total outage produces one clause and
	// not three saying the same thing.
	if !r.InstrumentFailure() {
		if n := len(r.Blocked); n > 0 {
			parts = append(parts, fmt.Sprintf("%d carrier(s) NOT re-read: %s", n, refsOf(r.Blocked)))
		}
		if n := len(r.Indeterminate); n > 0 {
			parts = append(parts, fmt.Sprintf("%d unresolvable carrier(s): %s", n, refsOf(r.Indeterminate)))
		}
	}
	return strings.Join(parts, "; ")
}

// refsOf lists the issue refs behind a group of findings.
func refsOf(fs []Finding) string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Carrier.Ref())
	}
	return strings.Join(out, ", ")
}

// displayStages renders the covered-stage set for a human. The empty stage is a
// real member — a carrier whose body names no stage is covered — and printing it
// as nothing would make the list look shorter than it is.
func displayStages(stages []string) []string {
	out := make([]string, 0, len(stages))
	for _, s := range stages {
		if strings.TrimSpace(s) == "" {
			out = append(out, "(none)")
			continue
		}
		out = append(out, s)
	}
	return out
}

// stageOr renders a carrier's stage, naming the absence rather than printing a
// gap that reads as a formatting bug.
func stageOr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

// humanWindow renders a threshold. Negative means the check is off, and it says
// so: a report that printed "-1s" would leave a reader deducing it.
func humanWindow(d time.Duration) string {
	if d < 0 {
		return "off"
	}
	if d == 0 {
		return "immediate"
	}
	return humanAge(d)
}

// humanAge renders a duration the way a reader triages by: minutes under an
// hour, hours and minutes under a day, then days.
func humanAge(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd%dh", days, int(d.Hours())%24)
}
