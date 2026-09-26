package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
)

// A spawn whose outcome is unknown must not exit like a failure: a caller that
// retries on exit 1 would redispatch onto a possibly-live spawn, which is
// drellem2/pogo#167 (mg-c252).
func TestSpawnErrExitCode(t *testing.T) {
	unknown := fmt.Errorf("wrapped: %w", &client.SpawnOutcomeUnknownError{Op: "spawn-polecat", StillRunning: true})
	if got := spawnErrExitCode(unknown); got != cli.ExitUnknown {
		t.Errorf("outcome-unknown exit = %d, want %d", got, cli.ExitUnknown)
	}
	if got := spawnErrExitCode(errors.New("spawn-polecat failed: refused")); got != cli.ExitError {
		t.Errorf("refusal exit = %d, want %d", got, cli.ExitError)
	}
}
