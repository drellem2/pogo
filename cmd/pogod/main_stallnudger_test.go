package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
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

// TestStallNudgerNeverInterruptsBusyAgent is the end-to-end proof of gh
// drellem2/pogo #61 review point (a): the priority wake reuses newStallNudger,
// and the nudger delivers in wait-idle mode, so a BUSY agent (PTY never quiet)
// is never interrupted mid-turn — the wake message never reaches its PTY. This
// complements internal/agent TestNudgeWithModeWaitIdleTimeoutOnBusy (which
// proves the wait-idle primitive) by exercising the exact function the stall
// watcher and priority wake call.
func TestStallNudgerNeverInterruptsBusyAgent(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// A shell that prints forever — the PTY is never quiet, so the agent never
	// satisfies the 2s idle threshold and wait-idle can never deliver.
	a, err := reg.Spawn(agent.SpawnRequest{
		Name:    "busy-coordinator",
		Type:    agent.TypeCrew,
		Command: []string{"sh", "-c", "while true; do printf x; sleep 0.05; done"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait until it is genuinely busy (has produced output), not just-spawned.
	deadline := time.Now().Add(2 * time.Second)
	for len(a.RecentOutput(16)) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(a.RecentOutput(16)) == 0 {
		t.Fatal("busy agent never produced output")
	}

	nudge := newStallNudger(reg, func(to, from, subject, body string) error {
		t.Errorf("must not fall back to mail for a running agent (would double-deliver)")
		return nil
	})

	// Deliver in the background — wait-idle blocks up to DefaultNudgeTimeout
	// against a never-quiet agent. StopAll (deferred) unblocks it at teardown.
	const sentinel = "PRIORITY_WAKE_SENTINEL"
	go func() { _ = nudge("busy-coordinator", sentinel) }()

	// Sleep well past the 2s idle threshold: an IDLE agent would have been
	// delivered to by now, so if the sentinel is still absent the busy agent was
	// genuinely never interrupted (not merely still-within-the-idle-wait).
	time.Sleep(3 * time.Second)
	if strings.Contains(string(a.RecentOutput(1<<16)), sentinel) {
		t.Fatal("wait-idle nudge interrupted a busy agent — the wake reached its PTY")
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

	if err := nudge("ghost-agent", "priority-wake: urgent"); err != nil {
		t.Fatalf("nudge: %v", err)
	}
	if !mailed {
		t.Fatal("expected mail fallback for an unregistered agent")
	}
	if got.to != "ghost-agent" || got.body != "priority-wake: urgent" {
		t.Errorf("mail routed wrong: to=%q body=%q", got.to, got.body)
	}
}
