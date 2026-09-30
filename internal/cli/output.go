package cli

import (
	"encoding/json"
	"fmt"
	"os"
)

// Standardized exit codes for all pogo CLI commands.
const (
	ExitSuccess  = 0
	ExitError    = 1
	ExitNotFound = 2
	// ExitUnknown: the command ran, and could not establish the thing it was
	// asked about. Distinct from ExitError because the two owe different
	// actions — a negative answer is acted on, an absent one is investigated —
	// and because a check that reports "I could not measure this" as either
	// success or failure is the exact defect mg-ed4a was filed for. Used by
	// `pogo service verify-revision`.
	ExitUnknown = 3
	// ExitNudgeNotDelivered: `pogo nudge` — nobody received the message (the
	// confirm escalation ran out, or it was never written). Resend it by
	// another channel. drellem2/pogo#100.
	ExitNudgeNotDelivered = 4
	// ExitNudgeQueued: `pogo nudge` — the message was written to an agent
	// mid-turn and cannot be confirmed either way. Probably fine; resending
	// would deliver it twice. drellem2/pogo#100.
	ExitNudgeQueued = 5
)

// PrintJSON marshals v as indented JSON and writes it to stdout.
// On marshal error it prints an error JSON object and exits with ExitError.
func PrintJSON(v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"error": "failed to marshal JSON: %s"}`+"\n", err)
		os.Exit(ExitError)
	}
	fmt.Println(string(data))
}

// ExitWithError prints an error message and exits with the given code.
// In JSON mode it prints a structured error object.
func ExitWithError(jsonMode bool, msg string, code int) {
	if jsonMode {
		PrintJSON(map[string]interface{}{
			"error": msg,
		})
	} else {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(code)
}
