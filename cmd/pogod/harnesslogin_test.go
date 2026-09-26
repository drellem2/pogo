package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

func refusedSweep(names ...string) []agent.AutoStartResult {
	var out []agent.AutoStartResult
	for _, n := range names {
		out = append(out, agent.AutoStartResult{Name: n, Status: agent.AutoStartStatusRefusedNotLoggedIn,
			Error: "agent harness is not logged in: claude reports no login (exit 1, loggedIn: false)"})
	}
	return out
}

// drellem2/pogo#173: a refused crew pages ONCE per episode — not per retry,
// not per refused agent — reaches the out-of-band box because the coordinator
// is among the refused, and the episode ends on the first sweep that refuses
// nothing, so the next recurrence pages again.
func TestAnnunciateHarnessLogin_OncePerEpisode(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	a.setOutOfBandBox("daniel")
	t0 := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)

	annunciateHarnessLogin(a, "mayor", refusedSweep("mayor", "pm-pogo", "doctor"), t0)
	if len(rec.sent) != 2 || rec.sent[0].to != "mayor" || rec.sent[1].to != "daniel" {
		t.Fatalf("first refusal sent %+v, want ONE notice to mayor + its out-of-band copy", rec.sent)
	}
	if !strings.Contains(rec.sent[0].subject, "not logged in") {
		t.Errorf("subject %q does not name the cause", rec.sent[0].subject)
	}

	// Retries — including one refusing a different set, as a sweep after a
	// `pogo agent start` by hand would — stay quiet.
	annunciateHarnessLogin(a, "mayor", refusedSweep("mayor", "pm-pogo", "doctor"), t0.Add(time.Minute))
	annunciateHarnessLogin(a, "mayor", refusedSweep("pm-pogo"), t0.Add(2*time.Hour))
	if len(rec.sent) != 2 {
		t.Fatalf("retries sent %d mails, want still 2", len(rec.sent))
	}

	// Logged in: the sweep refuses nothing and the episode ends...
	annunciateHarnessLogin(a, "mayor", []agent.AutoStartResult{{Name: "mayor", Status: agent.AutoStartStatusStarted}}, t0.Add(3*time.Hour))
	// ...so a later recurrence is a new episode and pages again.
	annunciateHarnessLogin(a, "mayor", refusedSweep("mayor"), t0.Add(4*time.Hour))
	if len(rec.sent) != 4 {
		t.Fatalf("recurrence after a clear sent %d mails total, want 4", len(rec.sent))
	}
}
