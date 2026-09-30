package main

import (
	"errors"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
)

// nudgeFailureResult is what `pogo nudge` reports for a failed nudge: the JSON
// body printed under --json and the exit code (drellem2/pogo#100). The two
// outcomes a caller acts on differently — nobody got it (resend) and queued
// mid-turn (leave it) — each get their own status and code; everything else
// keeps exit 1.
func nudgeFailureResult(name string, err error) (map[string]string, int) {
	body := map[string]string{"agent": name, "error": err.Error()}
	var notRunning *client.NotRunningError
	switch {
	case errors.Is(err, client.ErrNudgeNotDelivered):
		body["status"] = agent.NudgeStatusNotDelivered
		return body, cli.ExitNudgeNotDelivered
	case errors.Is(err, client.ErrNudgeQueued):
		body["status"] = agent.NudgeStatusQueued
		return body, cli.ExitNudgeQueued
	case errors.As(err, &notRunning):
		body["status"] = agent.NudgeStatusNotRunning
		body["mailbox"] = notRunning.Box
		return body, cli.ExitError
	}
	body["status"] = agent.NudgeStatusFailed
	return body, cli.ExitError
}
