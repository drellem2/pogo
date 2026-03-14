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
