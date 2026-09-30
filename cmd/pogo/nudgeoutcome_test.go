package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
)

// drellem2/pogo#100: `pogo nudge` gives "nobody got it" and "queued mid-turn"
// their own exit codes and a JSON status, distinct from every other failure.
func TestNudgeFailureResult(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus string
		wantCode   int
	}{
		{"not delivered", &client.NudgeError{Agent: "p1", Status: agent.NudgeStatusNotDelivered, Detail: "x"}, agent.NudgeStatusNotDelivered, cli.ExitNudgeNotDelivered},
		{"queued", &client.NudgeError{Agent: "p1", Status: agent.NudgeStatusQueued, Detail: "x"}, agent.NudgeStatusQueued, cli.ExitNudgeQueued},
		{"daemon failed", &client.NudgeError{Agent: "p1", Status: agent.NudgeStatusFailed, Detail: "x"}, agent.NudgeStatusFailed, cli.ExitError},
		{"not running", &client.NotRunningError{Agent: "p1", Box: "p1"}, agent.NudgeStatusNotRunning, cli.ExitError},
		{"pogod unreachable", errors.New("dial tcp: connection refused"), agent.NudgeStatusFailed, cli.ExitError},
	}
	seen := map[int]string{}
	for _, c := range cases {
		body, code := nudgeFailureResult("p1", c.err)
		if body["status"] != c.wantStatus || code != c.wantCode {
			t.Errorf("%s: status %q code %d, want %q %d", c.name, body["status"], code, c.wantStatus, c.wantCode)
		}
		if body["agent"] != "p1" || body["error"] != c.err.Error() {
			t.Errorf("%s: body %v does not carry agent and error", c.name, body)
		}
		if code != cli.ExitError {
			if prev, dup := seen[code]; dup {
				t.Errorf("exit %d shared by %s and %s", code, prev, c.name)
			}
			seen[code] = c.name
		}
	}
	if cli.ExitNudgeNotDelivered == cli.ExitNudgeQueued {
		t.Fatal("the two nudge outcomes share an exit code")
	}
	for _, other := range []int{cli.ExitSuccess, cli.ExitError, cli.ExitNotFound, cli.ExitUnknown} {
		if other == cli.ExitNudgeNotDelivered || other == cli.ExitNudgeQueued {
			t.Fatalf("a nudge exit code collides with an existing code %d", other)
		}
	}
}

// mg-3e961: `pogo doctor "<question>"` reports its nudge with the same
// distinct statuses as `pogo nudge`, not a blanket "error".
func TestDoctorNudgeReport(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus string
		wantLine   string
	}{
		{"delivered", nil, agent.NudgeStatusDelivered, "Nudged doctor: why?"},
		{"not delivered", &client.NudgeError{Agent: "doctor", Status: agent.NudgeStatusNotDelivered, Detail: "x"}, agent.NudgeStatusNotDelivered, "not delivered"},
		{"queued", &client.NudgeError{Agent: "doctor", Status: agent.NudgeStatusQueued, Detail: "x"}, agent.NudgeStatusQueued, "do not resend"},
		{"daemon failed", &client.NudgeError{Agent: "doctor", Status: agent.NudgeStatusFailed, Detail: "x"}, agent.NudgeStatusFailed, "could not nudge"},
		{"not running", &client.NotRunningError{Agent: "doctor", Box: "doctor"}, agent.NudgeStatusNotRunning, "could not nudge"},
	}
	for _, c := range cases {
		body := doctorNudgeReport("doctor", "why?", c.err)
		if body["status"] != c.wantStatus {
			t.Errorf("%s: status %q, want %q", c.name, body["status"], c.wantStatus)
		}
		if body["status"] == "error" {
			t.Errorf("%s: the retired blanket status \"error\" came back", c.name)
		}
		if body["message"] != "why?" {
			t.Errorf("%s: body %v does not carry the message", c.name, body)
		}
		if c.err != nil && body["error"] != c.err.Error() {
			t.Errorf("%s: body %v does not carry the error", c.name, body)
		}
		if line := doctorNudgeLine("doctor", "why?", c.err); !strings.Contains(line, c.wantLine) {
			t.Errorf("%s: line %q does not contain %q", c.name, line, c.wantLine)
		}
	}
}
