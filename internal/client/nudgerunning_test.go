package client

import (
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// mg-e00c: a nudge to an agent that is not running used to fall back to
// `gt mail send` — a system nothing reads — and report success. It must now
// fail, name the mailbox, and spawn nothing.
func TestNudgeRunningRefusesNonRunningAgentAndNamesTheBox(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	spawned := false
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		spawned = true
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = old })

	err := NudgeRunning("mg-7666", "hello", nil)
	var nr *NotRunningError
	if !errors.As(err, &nr) {
		t.Fatalf("err = %v, want *NotRunningError", err)
	}
	if !errors.Is(err, ErrAgentNotRunning) {
		t.Error("NotRunningError does not unwrap to ErrAgentNotRunning")
	}
	if nr.Box != "7666" || !strings.Contains(err.Error(), "box 7666") || !strings.Contains(err.Error(), "nothing was sent") {
		t.Errorf("error does not name the box: %v", err)
	}
	if spawned {
		t.Error("a subprocess was spawned — the gt mail fallback is still reachable")
	}
}

func TestNudgeRunningDeliversToRunningAgent(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := NudgeRunning("pe00c", "hello", nil); err != nil {
		t.Fatalf("err = %v", err)
	}
}
