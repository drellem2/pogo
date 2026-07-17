package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/stallwatch"
)

// shortSocketDir returns a socket dir short enough that "<dir>/<name>.sock"
// fits AF_UNIX's sun_path limit. t.TempDir() on darwin lives under
// /var/folders/... and already exceeds it on its own, so a registry rooted
// there cannot bind an attach socket for any agent — which fails the spawn
// outright since mg-ef80. Production takes this dir from config.AgentSocketDir,
// which guarantees the fit.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pogo-pogod-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// spawnBusyAgent starts an agent whose PTY is never quiet — it prints forever,
// so it can never satisfy the wait-idle threshold. This is the busy-mayor
// condition from mg-79dc, and the field data says it is realistic rather than
// pathological: every one of the 18 dropped fires on 2026-07-17 recorded a
// "last PTY write" of 2-305ms, i.e. a mayor writing continuously.
func spawnBusyAgent(t *testing.T, reg *agent.Registry, name string) *agent.Agent {
	t.Helper()
	a, err := reg.Spawn(agent.SpawnRequest{
		Name:    name,
		Type:    agent.TypeCrew,
		Command: []string{"sh", "-c", "while true; do printf x; sleep 0.05; done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wait until it is genuinely busy (has produced output), not just-spawned:
	// an agent with no output yet reads as not-idle for a different reason, and
	// would prove nothing.
	deadline := time.Now().Add(2 * time.Second)
	for len(a.RecentOutput(16)) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(a.RecentOutput(16)) == 0 {
		t.Fatal("busy agent never produced output")
	}
	return a
}

// TestStallNudgerNeverInterruptsBusyAgent is the end-to-end proof of gh
// drellem2/pogo #61 review point (a): the priority wake reuses newStallNudger,
// and the nudger delivers in wait-idle mode, so a BUSY agent (PTY never quiet)
// is never interrupted mid-turn — the wake message never reaches its PTY. This
// complements internal/agent TestNudgeWithModeWaitIdleTimeoutOnBusy (which
// proves the wait-idle primitive) by exercising the exact function the stall
// watcher and priority wake call.
//
// mg-79dc narrowed what this test asserts. It used to fail the mail fallback
// outright ("would double-deliver"), which conflated "the agent is running"
// with "the PTY took the message" — the very conflation that left ~38% of a
// day's stall fires unheard. The #61 guarantee is about the PTY only: the
// terminal must not be interrupted. Where the notice goes INSTEAD is not this
// test's business — TestStallNudgerFallsBackToMailWhenBusy owns that.
func TestStallNudgerNeverInterruptsBusyAgent(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a := spawnBusyAgent(t, reg, "busy-coordinator")

	// Mail is allowed here (the PTY nudge will fail, and the fallback is
	// correct) — this test only asserts the PTY was never written to.
	nudge := newStallNudger(reg, func(to, from, subject, body string) error { return nil })

	// Deliver in the background — wait-idle blocks up to DefaultNudgeTimeout
	// against a never-quiet agent. StopAll (deferred) unblocks it at teardown.
	const sentinel = "PRIORITY_WAKE_SENTINEL"
	go func() { _, _ = nudge("busy-coordinator", sentinel) }()

	// Sleep well past the 2s idle threshold: an IDLE agent would have been
	// delivered to by now, so if the sentinel is still absent the busy agent was
	// genuinely never interrupted (not merely still-within-the-idle-wait).
	time.Sleep(3 * time.Second)
	if strings.Contains(string(a.RecentOutput(1<<16)), sentinel) {
		t.Fatal("wait-idle nudge interrupted a busy agent — the wake reached its PTY")
	}
}

// TestStallNudgerFallsBackToMailWhenBusy is the mg-79dc regression test: the
// failure path, not the happy path. A RUNNING but BUSY agent cannot be reached
// by a wait-idle PTY nudge — that is structural, not a tuning problem — so the
// notice must take the durable channel instead of being dropped with an error.
//
// Before the fix this returned the wait-idle error and mailed nothing: the
// detector fired, the event log recorded `nudge_error`, and the mayor learned
// nothing. That is the exact shape of all 18 dropped fires on 2026-07-17.
//
// The injected 300ms timeout stands in for production's 30s. The length is
// irrelevant to what is proven here — a never-quiet agent busts ANY deadline —
// and that irrelevance is the point: this is why lengthening the timeout was
// ruled out as a fix.
func TestStallNudgerFallsBackToMailWhenBusy(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a := spawnBusyAgent(t, reg, "busy-mayor")

	var got struct{ to, from, subject, body string }
	mailed := false
	nudge := newStallNudgerWithTimeout(reg, func(to, from, subject, body string) error {
		mailed = true
		got.to, got.from, got.subject, got.body = to, from, subject, body
		return nil
	}, 300*time.Millisecond)

	const sentinel = "STALL_NOTICE_SENTINEL"
	delivery, err := nudge("busy-mayor", sentinel)

	// The nudge must NOT report failure — it was delivered, just not by PTY.
	if err != nil {
		t.Fatalf("busy agent: nudge must fall back to mail, not fail: %v", err)
	}
	if !mailed {
		t.Fatal("busy agent: nudge was neither written to the PTY nor mailed — the notice reached NOBODY (mg-79dc)")
	}

	// The delivery must be attributable: "arrived by mail_fallback" is the
	// countable fact that makes the PTY channel's failure rate measurable.
	if delivery.Channel != stallwatch.DeliveryMailFallback {
		t.Errorf("delivery channel = %q, want %q", delivery.Channel, stallwatch.DeliveryMailFallback)
	}
	if !strings.Contains(delivery.FallbackReason, "still producing output") {
		t.Errorf("fallback reason should name the busy-PTY cause, got %q", delivery.FallbackReason)
	}

	// The message must actually be in the mail, and must say why it came this
	// way — a stall notice read off the terminal and one read out of the inbox
	// carry different freshness, and the reader has to know which it holds.
	if got.to != "busy-mayor" {
		t.Errorf("mail routed to %q, want busy-mayor", got.to)
	}
	if !strings.Contains(got.body, sentinel) {
		t.Errorf("mail body lost the stall message: %q", got.body)
	}
	if !strings.Contains(got.body, "could not be delivered to your terminal") {
		t.Errorf("mail body must explain the fallback, got %q", got.body)
	}

	// And the #61 guarantee still holds: falling back to mail must not mean
	// writing to the busy terminal after all.
	if strings.Contains(string(a.RecentOutput(1<<16)), sentinel) {
		t.Error("fallback wrote to the busy agent's PTY — the never-interrupt guarantee broke")
	}
}

// TestStallNudgerBothChannelsDownReportsHardFailure proves the remaining
// failure is reported as one. Once mail backstops the PTY, a returned error
// means something much narrower than it used to: EVERY channel was tried and
// none carried the message. That must surface as an error rather than be
// swallowed by the fallback's optimism.
func TestStallNudgerBothChannelsDownReportsHardFailure(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	spawnBusyAgent(t, reg, "busy-mayor")

	nudge := newStallNudgerWithTimeout(reg, func(to, from, subject, body string) error {
		return fmt.Errorf("macguffin unreachable")
	}, 300*time.Millisecond)

	delivery, err := nudge("busy-mayor", "STALL")
	if err == nil {
		t.Fatal("both channels down must be a hard error, got nil")
	}
	if delivery.Channel != "" {
		t.Errorf("failed delivery must not claim a channel, got %q", delivery.Channel)
	}
	// The error must name BOTH causes — "mail failed" alone would hide that the
	// PTY was tried first, and vice versa.
	if !strings.Contains(err.Error(), "still producing output") {
		t.Errorf("error should name the pty cause, got %q", err)
	}
	if !strings.Contains(err.Error(), "macguffin unreachable") {
		t.Errorf("error should name the mail cause, got %q", err)
	}
}

// TestStallNudgerFallsBackToMailWhenOffline: when the target agent is not
// registered/running, the nudger delivers via macguffin mail so the signal is
// durable rather than dropped.
func TestStallNudgerFallsBackToMailWhenOffline(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	var got struct{ to, from, subject, body string }
	mailed := false
	nudge := newStallNudger(reg, func(to, from, subject, body string) error {
		mailed = true
		got.to, got.from, got.subject, got.body = to, from, subject, body
		return nil
	})

	delivery, err := nudge("ghost-agent", "priority-wake: urgent")
	if err != nil {
		t.Fatalf("nudge: %v", err)
	}
	if !mailed {
		t.Fatal("expected mail fallback for an unregistered agent")
	}
	if got.to != "ghost-agent" || got.body != "priority-wake: urgent" {
		t.Errorf("mail routed wrong: to=%q body=%q", got.to, got.body)
	}
	// An offline recipient is plain mail, distinct from the busy-agent
	// mail_fallback: nothing failed here, the PTY simply never existed. The
	// two must stay distinguishable or the fallback rate can't be measured.
	if delivery.Channel != stallwatch.DeliveryMail {
		t.Errorf("delivery channel = %q, want %q", delivery.Channel, stallwatch.DeliveryMail)
	}
	if delivery.FallbackReason != "" {
		t.Errorf("offline delivery is not a fallback; got reason %q", delivery.FallbackReason)
	}
}
