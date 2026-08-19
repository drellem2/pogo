package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/refinery"
)

// readSubmitVerdict resolves `pogo refinery submit`'s --verdict / --verdict-file
// pair into the raw JSON carried on the merge request (mg-dfea).
//
// It validates here as well as in the daemon, and the duplication is deliberate
// rather than sloppy: an invalid verdict is unmarshalable, so a SubmitRequest
// carrying one fails inside json.Marshal and the submitter is told "json: error
// calling MarshalJSON for type json.RawMessage" — an error about the transport,
// for a mistake in the argument. Both sides call refinery.ValidateVerdict, so
// there is one rule and two places that state it.
//
// `-` reads stdin, matching `gh pr create --body-file -` and `mg mail send
// --body-file -`. That is the form to reach for from a shell: a double-quoted
// JSON object is expanded before the binary sees it, which is how a verdict
// full of backticks or `$` arrives mangled.
func readSubmitVerdict(inline, file string) (json.RawMessage, error) {
	if inline != "" && file != "" {
		return nil, fmt.Errorf("--verdict and --verdict-file are alternatives; pass one")
	}

	raw := inline
	if file != "" {
		var (
			b   []byte
			err error
		)
		if file == "-" {
			b, err = io.ReadAll(os.Stdin)
		} else {
			b, err = os.ReadFile(file)
		}
		if err != nil {
			return nil, fmt.Errorf("--verdict-file %s: %w", file, err)
		}
		raw = string(b)
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		// Reached only when a flag was passed and resolved to nothing — an
		// empty file, an empty stdin, a variable that did not expand. Silence
		// here would submit a verdict-free merge while the author believed it
		// had recorded one, which is this ticket's failure mode wearing a
		// different hat.
		if file != "" {
			return nil, fmt.Errorf("--verdict-file %s is empty — it records nothing", file)
		}
		return nil, nil
	}

	verdict := json.RawMessage(raw)
	if err := refinery.ValidateVerdict(verdict); err != nil {
		return nil, fmt.Errorf("--verdict: %w", err)
	}
	return verdict, nil
}

// verdictFreeSubmitWarning is the one line printed to STDERR when a submit that
// could carry an author verdict carries none (mg-c456).
//
// SUBMIT TIME IS THE ONLY MOMENT THE AUTHOR CAN STILL WIN. On the auto-done lane
// the attachment point is single and closes when the merge lands: pogod closes
// the item that instant and stops the polecat about half a second later, mg
// refuses a second result because the item is already terminal, and the protocol
// scores that refusal as normal completion. The loss is CREATED here and merely
// REALISED at the close — so the notice belongs here, while a fix still costs one
// flag, rather than only in the record pogod writes afterwards.
//
// IT NAMES THE CONDITION INSTEAD OF ASSERTING THE LANE, and that is deliberate.
// Two of the three deferred lanes are not knowable from this process: a
// `post-merge-work` tag lives on the work item and an integration `--target` is
// compared against the repo's default branch, both resolved by the daemon at
// merge time. Deriving the lane here would duplicate that logic and be wrong
// whenever the two drift — a warning that confidently mis-states which lane you
// are on is worse than one that states what it checked. So it says "no verdict
// was carried", which is a fact about this invocation, and names the lanes on
// which that is harmless.
//
// Returns "" when there is nothing to warn about: a verdict was passed,
// --defer-done was passed (the one deferral this process CAN see, and the author
// calls `mg done --result` itself there), or --author names no work item — an
// authorless or crew-authored merge has no item for a verdict to be lost from,
// which is the same shape test the reap itself applies before closing anything.
func verdictFreeSubmitWarning(verdict json.RawMessage, author string, deferDone bool) string {
	if len(verdict) > 0 || deferDone || !client.LooksLikeWorkItemID(author) {
		return ""
	}
	return "warning: no --verdict/--verdict-file — nothing of YOUR OWN will be recorded on " + author + ".\n" +
		"  On the auto-done path this is the LAST moment you can record one: pogod closes the item the\n" +
		"  instant this merges and your own `mg done --result` is then refused as already-done — a refusal\n" +
		"  the protocol reports as success, so nothing else will tell you.\n" +
		"  Harmless if this merge is deferred (an integration --target, or an item tagged post-merge-work):\n" +
		"  you call `mg done --result` yourself there.\n"
}
