package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

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
