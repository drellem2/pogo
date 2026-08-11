package workitem

import "testing"

// The parse's THIRD OUTCOME (mg-27d4). Before it, scanCarrierBlock had two
// answers — a carrier, or no carrier — and it gave the second one to a body it
// could see carrier-shaped content in but could not reach. That is the
// fail-open direction: `stage: gated` one line too low did not gate.
//
// These tests are in two halves and BOTH halves are load-bearing. The positive
// half proves the new outcome fires. The negative half proves it did not become
// the body-wide search this parser exists to refuse — a flag that fired on
// prose would gate the tickets that DISCUSS the gate, which is every ticket in
// this file's history.

func TestCarrierUnreadable(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "the reported shape: one line of prose above the block",
			body: `# review: something (drellem2/pogo#83)
PR TO REVIEW: https://github.com/drellem2/pogo/pull/84

workflow: gh-issue
stage: gated
gh: drellem2/pogo#83
`,
			want: true,
		},
		{
			name: "mg-2997's actual body shape, straight off the live store",
			body: `# review: polecat mail-check auto-register hardening (drellem2/pogo#83)
PR TO REVIEW: https://github.com/drellem2/pogo/pull/84 (branch polecat-75d9, issue drellem2/pogo#83).

workflow: gh-issue
stage: review
gh: drellem2/pogo#83
`,
			want: true,
		},
		{
			name: "the block written ABOVE the title heading is consumed and never scanned",
			body: `workflow: gh-issue
stage: gated
gh: drellem2/pogo#100

# PARKED awaiting Daniel's GO/NO-GO on gh#100

Successor to the mg-2fcc triage.
`,
			want: true,
		},
		{
			name: "a stage line alone below prose still counts",
			body: `# triage: something
Reported: the daemon wedged overnight.
stage: gated
`,
			want: true,
		},
		{
			name: "the ordinary item, no carrier anywhere",
			body: "# Add user authentication\n\nDo the thing.\n",
			want: false,
		},
		{
			name: "the shipped shape parses, so nothing is unreadable",
			body: `# triage: something (drellem2/pogo#104)
workflow: gh-issue
stage: gated
gh: drellem2/pogo#104

Triage this GitHub issue.
`,
			want: false,
		},
		{
			name: "a blank line under the heading is still the leading block",
			body: `# build: something

workflow: gh-issue
stage: build
`,
			want: false,
		},
		{
			name: "a leading block followed by ordinary prose is not unreadable",
			body: `# build: something
workflow: gh-issue
stage: build

## The mechanism

Some prose about stages and gates and other things.
`,
			want: false,
		},
		{
			name: "workflow: with no stage is a readable block, not an unreadable one",
			body: `# a ticket filed with only a workflow line
workflow: internal

Found while fixing something else.
`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseBody(t, tt.body).CarrierUnreadable; got != tt.want {
				t.Errorf("CarrierUnreadable = %v, want %v", got, tt.want)
			}
		})
	}
}

// The negative arm that costs the most if it breaks. mg-69b1's own body quotes
// the carrier line it is complaining about; mg-27d4's quotes it repeatedly. A
// flag that fired on either would gate the bug report about the gate — an item
// nobody could then dispatch a fix from, which is the failure this whole area
// exists to avoid. See TestCarrierBlockIgnoresProseThatQuotesIt for the same
// guarantee on the value side.
func TestCarrierUnreadableIgnoresProseAndQuotations(t *testing.T) {
	item := parseBody(t, "# A gh-issue carrier at stage: gated is still dispatchable\n"+
		`OBSERVED LIVE 2026-08-09. Three gh-issue carriers sat at `+"`stage: gated`"+`
awaiting a GO/NO-GO.

THE DEFECT. The playbook's transition 2 says only:

  "When the triage packet arrives, set `+"`stage: gated`"+` and send the summary."

An indented quotation is not a declaration:

    workflow: gh-issue
    stage: gated
`)

	if item.CarrierUnreadable {
		t.Error("CarrierUnreadable on a body that only QUOTES carrier lines — a prose " +
			"mention is not a misplaced declaration, and gating on one would gate every " +
			"ticket written about the gate")
	}
}

// A fenced block is documentation, not a declaration. The mayor prompt's own
// state-carrier section is a fenced example at column 0, and every ticket that
// pastes it — this file's own ticket included — would otherwise gate itself.
func TestCarrierUnreadableIgnoresFencedExamples(t *testing.T) {
	item := parseBody(t, "# design: document the state carrier\n"+
		"The carrier block leads the body and looks like this:\n"+
		"\n```\nworkflow: gh-issue\nstage: triage | gated | build\ngh: <owner>/<repo>#<n>\n```\n"+
		"\nThat is the whole convention.\n")

	if item.CarrierUnreadable {
		t.Error("CarrierUnreadable on a body whose only carrier lines are inside a ``` fence")
	}
	// And the tilde spelling, which markdown allows and a filer may reach for.
	item = parseBody(t, "# design: document the state carrier\n"+
		"Like this:\n\n~~~\nworkflow: gh-issue\nstage: gated\n~~~\n")
	if item.CarrierUnreadable {
		t.Error("CarrierUnreadable on a body whose carrier lines are inside a ~~~ fence")
	}
}

// `gh:` declares no gate, so a stray one must not refuse dispatch. The narrowing
// is what keeps a fail-closed signal from stranding work over a line that could
// not have gated it either way.
func TestCarrierUnreadableIgnoresNonGateKeys(t *testing.T) {
	item := parseBody(t, `# some ticket
A lead-in sentence.
gh: drellem2/pogo#104
`)
	if item.CarrierUnreadable {
		t.Error("CarrierUnreadable on a stray `gh:` line, which carries no gate")
	}
}

// The bound is real and is documented rather than hidden: a declaration pushed
// past unreachableCarrierScanLimit is still invisible. Pinning it here means the
// residual is a stated property with a test, not a surprise for whoever next
// reads a body that got away.
func TestCarrierUnreadableScanIsBounded(t *testing.T) {
	body := "# a ticket with a very long lead-in\nlead-in prose.\n"
	for i := 0; i < unreachableCarrierScanLimit+5; i++ {
		body += "more prose about nothing in particular.\n"
	}
	body += "stage: gated\n"

	if parseBody(t, body).CarrierUnreadable {
		t.Errorf("CarrierUnreadable for a declaration more than %d lines below the title; "+
			"the scan is bounded on purpose and this pins where the bound sits",
			unreachableCarrierScanLimit)
	}
}

// A block truncated by carrierBlockScanLimit was not read to its end either, so
// it gets the same answer. Whatever sits on the line past the limit is unread,
// and the stage may be on it.
func TestCarrierUnreadableWhenLeadingBlockIsTruncated(t *testing.T) {
	body := "# a body that is nothing but carrier-shaped lines\n"
	for i := 0; i < carrierBlockScanLimit*2; i++ {
		body += "filler: value\n"
	}
	body += "stage: gated\n"

	item := parseBody(t, body)
	if item.Stage != "" {
		t.Errorf("Stage = %q; a stage past the %d-line block limit must not be read",
			item.Stage, carrierBlockScanLimit)
	}
	if !item.CarrierUnreadable {
		t.Error("a leading block truncated by the scan limit reads as fully parsed; " +
			"the line past the limit was never read and the stage may be on it")
	}
}
