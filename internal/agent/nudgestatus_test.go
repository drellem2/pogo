package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// drellem2/pogo#100: every nudge outcome reaches the caller as a status it can
// branch on, not as a 500 carrying prose.

func TestNudgeErrorStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"queued", fmt.Errorf("nudge to %q: mid-turn: %w", "x", ErrNudgeQueued), NudgeStatusQueued},
		{"unconfirmed", fmt.Errorf("nudge to %q: did not receive it: %w", "x", ErrNudgeUnconfirmed), NudgeStatusNotDelivered},
		{"not written", fmt.Errorf("wait for idle, %w: %w", ErrNudgeNotWritten, context.DeadlineExceeded), NudgeStatusNotDelivered},
		{"mangled", fmt.Errorf("nudge to %q: %w", "x", ErrNudgeMangled), NudgeStatusFailed},
		{"pty write", errors.New("write /dev/ptmx: input/output error"), NudgeStatusFailed},
	}
	for _, c := range cases {
		if got := NudgeErrorStatus(c.err); got != c.want {
			t.Errorf("%s: NudgeErrorStatus = %q, want %q", c.name, got, c.want)
		}
	}
}

// postNudge drives handleNudge the way the client does and decodes its body.
func postNudge(t *testing.T, reg *Registry, name string, req NudgeAPIRequest) (int, NudgeAPIResponse) {
	t.Helper()
	body, _ := json.Marshal(req)
	hr := httptest.NewRequest("POST", "/agents/"+name+"/nudge", bytes.NewReader(body))
	hr.SetPathValue("name", name)
	rr := httptest.NewRecorder()
	reg.handleNudge(rr, hr)
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json; body=%s", ct, rr.Body.String())
	}
	var resp NudgeAPIResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body is not a NudgeAPIResponse: %v; body=%s", err, rr.Body.String())
	}
	return rr.Code, resp
}

func TestHandleNudgeReportsEachOutcome(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	check := func(t *testing.T, code int, resp NudgeAPIResponse, wantCode int, wantStatus, agentName, errHas string) {
		t.Helper()
		if code != wantCode || resp.Status != wantStatus || resp.Agent != agentName {
			t.Fatalf("got %d %+v, want %d status %q agent %q", code, resp, wantCode, wantStatus, agentName)
		}
		if errHas == "" && resp.Error != "" {
			t.Fatalf("error set on %s: %q", wantStatus, resp.Error)
		}
		if !strings.Contains(resp.Error, errHas) {
			t.Fatalf("error %q does not contain %q", resp.Error, errHas)
		}
	}

	// Positive control: a listening harness is confirmed, 200 delivered.
	t.Run("delivered", func(t *testing.T) {
		a, _ := spawnWithReceipt(t, reg, "st-delivered", busyHarness)
		waitUntilBusy(t, a, 5*time.Second)
		code, resp := postNudge(t, reg, a.Name, NudgeAPIRequest{Message: "hi", Timeout: 6})
		check(t, code, resp, http.StatusOK, NudgeStatusDelivered, a.Name, "")
	})

	t.Run("not_delivered: confirm escalation ran out", func(t *testing.T) {
		a, _ := spawnWithReceipt(t, reg, "st-deaf", deafHarness)
		waitUntilIdle(t, a, 5*time.Second)
		code, resp := postNudge(t, reg, a.Name, NudgeAPIRequest{Message: "hi", Timeout: 3})
		check(t, code, resp, http.StatusInternalServerError, NudgeStatusNotDelivered, a.Name, "did not receive it")
	})

	t.Run("not_delivered: wait-idle never wrote it", func(t *testing.T) {
		a, err := reg.Spawn(SpawnRequest{
			Name:    "st-busy-idle",
			Type:    TypePolecat,
			Command: []string{"sh", fakeHarness(t, "busy.sh", busyHarness)},
			Env:     []string{"WITNESS=" + witnessFile(t)},
		})
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		waitUntilBusy(t, a, 5*time.Second)
		code, resp := postNudge(t, reg, a.Name, NudgeAPIRequest{Message: "hi", Mode: string(NudgeWaitIdle), Timeout: 1})
		check(t, code, resp, http.StatusInternalServerError, NudgeStatusNotDelivered, a.Name, "still producing output")
	})

	t.Run("queued: written mid-turn, no receipt", func(t *testing.T) {
		a, _ := spawnWithReceipt(t, reg, "st-queued", busyDeafHarness, "RAW="+witnessFile(t))
		waitUntilBusy(t, a, 5*time.Second)
		code, resp := postNudge(t, reg, a.Name, NudgeAPIRequest{Message: "hi", Timeout: 3})
		check(t, code, resp, http.StatusAccepted, NudgeStatusQueued, a.Name, "mid-turn")
	})

	t.Run("not_running", func(t *testing.T) {
		code, resp := postNudge(t, reg, "st-nobody", NudgeAPIRequest{Message: "hi"})
		check(t, code, resp, http.StatusNotFound, NudgeStatusNotRunning, "st-nobody", "")
	})
}
