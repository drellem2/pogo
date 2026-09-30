package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// drellem2/pogo#100: pogod's structured nudge outcomes survive the client as
// errors a caller can test with errors.Is, not prose to match.
func TestNudgeAgentMapsEachFailureStatus(t *testing.T) {
	cases := []struct {
		name         string
		code         int
		status       string
		detail       string
		notDelivered bool
		queued       bool
		prefix       string
	}{
		{"not delivered", http.StatusInternalServerError, agent.NudgeStatusNotDelivered, "the agent did not receive it", true, false, "nudge not delivered: "},
		{"queued", http.StatusAccepted, agent.NudgeStatusQueued, "written mid-turn", false, true, "nudge queued, unconfirmed: "},
		{"failed", http.StatusInternalServerError, agent.NudgeStatusFailed, "input/output error", false, false, "nudge failed: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.code)
				json.NewEncoder(w).Encode(agent.NudgeAPIResponse{Status: c.status, Agent: "p1", Error: c.detail})
			})
			err := NudgeRunning("p1", "hello", nil)
			var ne *NudgeError
			if !errors.As(err, &ne) || ne.Status != c.status || ne.Agent != "p1" {
				t.Fatalf("err = %#v, want *NudgeError with status %q", err, c.status)
			}
			if errors.Is(err, ErrNudgeNotDelivered) != c.notDelivered || errors.Is(err, ErrNudgeQueued) != c.queued {
				t.Fatalf("errors.Is: not_delivered=%v queued=%v, want %v %v",
					errors.Is(err, ErrNudgeNotDelivered), errors.Is(err, ErrNudgeQueued), c.notDelivered, c.queued)
			}
			if err.Error() != c.prefix+c.detail {
				t.Fatalf("Error() = %q, want %q", err.Error(), c.prefix+c.detail)
			}
		})
	}
}

// A pogod that predates the structured body answers a bare 500 with prose; it
// must still be an error, and must not be mistaken for either typed outcome.
func TestNudgeAgentOldDaemonProseIsUntyped(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nudge to \"p1\": did not receive it: nudge not confirmed by the agent", http.StatusInternalServerError)
	})
	err := NudgeRunning("p1", "hello", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "nudge failed: nudge to") {
		t.Fatalf("err = %v", err)
	}
	var ne *NudgeError
	if errors.As(err, &ne) || errors.Is(err, ErrNudgeNotDelivered) || errors.Is(err, ErrNudgeQueued) {
		t.Fatalf("prose body was classified: %#v", err)
	}
}
