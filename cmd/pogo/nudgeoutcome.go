package main

import (
	"errors"
	"fmt"

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

// doctorNudgeReport is the "nudge" object `pogo doctor --json` prints for the
// question it nudges into a freshly started doctor agent. A failure carries the
// same status `pogo nudge --json` would give it (nudgeFailureResult), so
// "nobody got it" and "queued mid-turn" are told apart from every other error
// here too, instead of all reading "error".
func doctorNudgeReport(name, message string, err error) map[string]string {
	if err == nil {
		return map[string]string{"status": agent.NudgeStatusDelivered, "message": message}
	}
	body, _ := nudgeFailureResult(name, err)
	body["message"] = message
	return body
}

// doctorNudgeLine is the human-readable counterpart of doctorNudgeReport.
func doctorNudgeLine(name, message string, err error) string {
	if err == nil {
		return fmt.Sprintf("Nudged %s: %s", name, message)
	}
	body, _ := nudgeFailureResult(name, err)
	switch body["status"] {
	case agent.NudgeStatusNotDelivered:
		return fmt.Sprintf("Warning: question not delivered to %s — nobody received it; resend with 'pogo nudge %s <message>': %s", name, name, err)
	case agent.NudgeStatusQueued:
		return fmt.Sprintf("Question queued for %s, unconfirmed — it was typed while %s was mid-turn; do not resend: %s", name, name, err)
	}
	return fmt.Sprintf("Warning: could not nudge %s: %s", name, err)
}
