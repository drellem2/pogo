package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// marshalReportAsPrinted renders a report exactly as `check-mailloops --json`
// prints it. cli.PrintJSON uses MarshalIndent with a two-space indent, which is
// why the contract quotes `"unjudged": null` WITH the space — an assertion
// against compact JSON would pass while the documented literal was wrong.
func marshalReportAsPrinted(t *testing.T, rep agent.MailLoopReport) string {
	t.Helper()
	blob, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(blob)
}

// TestMailLoopsJSONContractMatchesTheWire is mg-4692's acceptance.
//
// The ruling on drellem2/pogo#127's round-1 review was to keep the bare `null`
// and pay the cost in DOCUMENTATION — the two-state contract stated where a
// machine reader looks, rather than only in a Go doc comment no consumer reads.
// A documented wire contract is exactly the kind of artifact that rots away
// from the wire it documents, which would reproduce the reported defect in a
// worse form: a reader who believed the help text and was wrong. So the text is
// held against the bytes the command actually prints.
func TestMailLoopsJSONContractMatchesTheWire(t *testing.T) {
	// The help output must actually carry the contract. A const nothing
	// references is documentation that reaches nobody.
	if !strings.Contains(checkMailLoopsLong, mailLoopsJSONContract) {
		t.Fatal("checkMailLoopsLong does not embed mailLoopsJSONContract — the contract must reach `--help`, " +
			"which is the whole remit: documented where a consumer reads it, not only in the Go source")
	}

	// State 1: the daemon reports the set and it is empty.
	empty := marshalReportAsPrinted(t, agent.MailLoopReport{
		Scanned: 2, Judged: 2, Unjudged: &[]agent.MailLoopExclusion{},
	})
	// State 2: the daemon does not report the set (older than this client).
	absent := marshalReportAsPrinted(t, agent.MailLoopReport{Scanned: 6, Judged: 2})

	const (
		nullLiteral  = `"unjudged": null`
		emptyLiteral = `"unjudged": []`
	)
	// Every literal the contract quotes must be quoted correctly...
	for _, lit := range []string{nullLiteral, emptyLiteral} {
		if !strings.Contains(mailLoopsJSONContract, lit) {
			t.Errorf("the contract does not quote %s; it documents two wire states and must name both verbatim", lit)
		}
	}
	// ...and must be the bytes the command emits for the state it describes.
	if !strings.Contains(empty, emptyLiteral) {
		t.Errorf("a supplied-but-empty set prints as:\n%s\nwant it to contain %s, which is what the help text promises", empty, emptyLiteral)
	}
	if !strings.Contains(absent, nullLiteral) {
		t.Errorf("an unreported set prints as:\n%s\nwant it to contain %s, which is what the help text promises", absent, nullLiteral)
	}
	// The two states must stay DISTINGUISHABLE on the wire. This is the
	// requirement the whole ruling is priced against: if these ever collapse,
	// the documentation stops being sufficient and the shape question reopens.
	if strings.Contains(empty, nullLiteral) || strings.Contains(absent, emptyLiteral) {
		t.Errorf("absent and empty are no longer distinguishable on the wire:\nempty:\n%s\nabsent:\n%s", empty, absent)
	}

	// The actionable line: a consumer never needs `unjudged` to get the COUNT.
	// Both fields the arithmetic uses must be on the wire in BOTH states, or
	// the sentence telling a machine reader to compute it is a lie in the case
	// that matters.
	for name, blob := range map[string]string{"empty": empty, "absent": absent} {
		for _, field := range []string{`"scanned"`, `"judged"`} {
			if !strings.Contains(blob, field) {
				t.Errorf("%s state omits %s, but the contract tells a reader to derive the count from it:\n%s", name, field, blob)
			}
		}
	}
	if !strings.Contains(mailLoopsJSONContract, `"scanned" - "judged"`) {
		t.Error("the contract no longer states the count arithmetic — that line is the actionable half of the ruling: " +
			"it tells a machine reader it never needs the field to get the number")
	}
	// And the null must never be documented as an empty set. The one thing the
	// contract exists to prevent is a reader flattening the two.
	for _, forbidden := range []string{"null means empty", "null is empty"} {
		if strings.Contains(strings.ToLower(mailLoopsJSONContract), forbidden) {
			t.Errorf("the contract says %q — 'the daemon did not say' and 'nobody was excluded' are opposite statements", forbidden)
		}
	}
}
