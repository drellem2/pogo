package providers

// The live control for confirmed nudge delivery (mg-ebee).
//
// Everything in internal/agent/nudgedelivery_test.go is measured against fakes:
// shell scripts that model a harness's input loop. A fake cannot falsify a
// claim about a binary it is standing in for — and the defect being fixed is
// specifically about what the real Claude Code binary does with bytes written
// to its pty while it is working. So this drives the whole path against a real,
// disposable `claude`, in this package because it needs both the agent registry
// and the concrete Claude provider (internal/agent cannot import
// internal/claude; that is the cycle this package exists to break).
//
// It is opt-in — POGO_LIVE_CLAUDE=1 — because it spends tokens and needs the
// machine's Claude credentials, and it skips wherever the preconditions are
// absent. The run that was actually observed is written down in
// docs/investigations/confirmed-nudge-delivery-2026-07-29.md, so a skip in CI
// does not quietly turn into "never observed".
//
// It never touches a crew agent's pty. The agent it drives is spawned by this
// test, in a temp directory, and stopped at the end.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/agenttest"
	"github.com/drellem2/pogo/internal/claude"
)

// busyWork is a turn long enough to nudge into the middle of, whose output does
// not pause: Claude Code redraws while a tool runs, which is precisely why a
// working agent never satisfies wait-idle.
const busyWork = "Run exactly this command, then stop and say nothing else: " +
	"for i in $(seq 1 60); do echo $i; sleep 0.5; done"

func TestLive_ConfirmedDeliveryReachesABusyClaude(t *testing.T) {
	if os.Getenv("POGO_LIVE_CLAUDE") != "1" {
		t.Skip("live Claude control: set POGO_LIVE_CLAUDE=1 to run (spends tokens)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("no claude binary on PATH")
	}
	pogoBin := livePogoBinary(t)

	// The receipt hook resolves `pogo` from PATH. Point it at the build under
	// test: an older installed binary has no `hook prompt-submit`, which would
	// make the hook a silent no-op and the whole control unreadable.
	t.Setenv("PATH", filepath.Dir(pogoBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("POGO_HOME", t.TempDir())

	reg, err := agent.NewRegistry(agenttest.SocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(5 * time.Second)
	reg.RegisterProvider(&claude.Provider)
	reg.SetDefaultProvider(claude.Provider.ID)

	a, err := reg.Spawn(agent.SpawnRequest{
		Name:    "live-ebee",
		Type:    agent.TypePolecat,
		Command: strings.Fields("claude --dangerously-skip-permissions"),
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	receipt := a.ReceiptFile()
	if receipt == "" {
		t.Fatal("no receipt signal for a real Claude spawn — the hook did not install")
	}
	t.Logf("receipt file: %s", receipt)

	profile := claude.Provider.Nudge
	sentinels := append([]string{profile.PromptReadySentinel}, profile.PromptReadyAlternates...)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if seen, err := a.WaitForReady(ctx, sentinels, profile.IdleThreshold); !seen {
		t.Fatalf("Claude never rendered a ready composer (err=%v):\n%s", err, tail(a))
	}

	// Baseline. Without it the busy result below is unreadable: a receipt that
	// never moves for ANY nudge would look identical to one dropped by a busy
	// harness.
	if err := a.NudgeWithMode("Reply with exactly: OK", agent.NudgeConfirm, 90*time.Second); err != nil {
		t.Fatalf("baseline confirm nudge to an idle Claude: %v\n%s", err, tail(a))
	}
	t.Logf("baseline: a nudge to an IDLE Claude was confirmed (receipts=%d)", count(t, receipt))

	// --- put it to work --------------------------------------------------
	if err := a.NudgeWithMode(busyWork, agent.NudgeConfirm, 90*time.Second); err != nil {
		t.Fatalf("could not start the long turn: %v\n%s", err, tail(a))
	}
	working := count(t, receipt)
	if !waitFor(30*time.Second, a.MidTurn) {
		t.Fatalf("Claude never entered a sustained-output turn:\n%s", tail(a))
	}

	// --- the defect, live -------------------------------------------------
	waitIdleErr := a.NudgeWithMode("first sweep item", agent.NudgeWaitIdle, 20*time.Second)
	if waitIdleErr == nil {
		t.Fatal("wait-idle found a quiet window mid-turn; the agent was not busy " +
			"enough for this control to say anything")
	}
	if !strings.Contains(waitIdleErr.Error(), "still producing output") {
		t.Fatalf("unexpected wait-idle failure shape: %v", waitIdleErr)
	}
	if n := count(t, receipt); n != working {
		t.Fatalf("wait-idle delivered something after all (receipts %d -> %d)", working, n)
	}
	t.Logf("LIVE DEFECT: wait-idle could not reach a working Claude — nothing was "+
		"even written: %v", waitIdleErr)

	// --- the fix, live, against the same busy agent -----------------------
	//
	// The acceptance bar is "delivered, or explicitly fails — never a success
	// nobody can check", so both halves are asserted: the message must reach
	// Claude (observed on its own screen), and pogod must not claim a delivery
	// it cannot prove.
	const probe = "EBEEPROBE"
	confirmErr := a.NudgeWithMode(
		"Ignore your current task for one moment and reply with exactly: "+probe,
		agent.NudgeConfirm, 120*time.Second)
	after := count(t, receipt)

	switch {
	case confirmErr == nil:
		if after <= working {
			t.Fatalf("confirm returned success with no new receipt (%d -> %d)", working, after)
		}
		t.Logf("LIVE FIX: confirmed delivery reached a working Claude and PROVED it "+
			"(receipts %d -> %d)", working, after)
		return

	case errors.Is(confirmErr, agent.ErrNudgeQueued):
		// The measured mid-turn outcome. Claude Code fires no UserPromptSubmit
		// for a prompt taken during a turn, so the receipt cannot move — see
		// docs/investigations/confirmed-nudge-delivery-2026-07-29.md §3. pogod
		// declines to claim the delivery. The delivery still has to HAPPEN, and
		// the only place that is visible is Claude's own screen.
		t.Logf("LIVE: pogod declined to claim an unprovable delivery: %v", confirmErr)
		if !waitFor(3*time.Minute, func() bool {
			return strings.Contains(string(agent.StripANSI(a.RecentOutput(64*1024))), probe)
		}) {
			t.Fatalf("the message never reached a busy Claude at all — %q absent from "+
				"its screen:\n%s", probe, tail(a))
		}
		t.Logf("LIVE FIX: the message DID reach the working Claude (%q echoed on its "+
			"screen) while pogod correctly refused to claim it", probe)
		if n := count(t, receipt); n != working {
			t.Errorf("receipt moved after all (%d -> %d): the mid-turn blind spot this "+
				"outcome is built on may have been fixed upstream — re-read the "+
				"investigation before trusting ErrNudgeQueued", working, n)
		}
		return

	default:
		t.Fatalf("confirm nudge to a busy Claude: %v\n%s", confirmErr, tail(a))
	}
}

// livePogoBinary locates the pogo built from this tree.
func livePogoBinary(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "bin", "pogo"))
	if err != nil {
		t.Skipf("cannot resolve ./bin/pogo: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("./bin/pogo not built (run ./build.sh): %v", err)
	}
	if out, err := exec.Command(path, "hook", "prompt-submit", "--help").CombinedOutput(); err != nil {
		t.Skipf("./bin/pogo has no `hook prompt-submit`: %v\n%s", err, out)
	}
	return path
}

func count(t *testing.T, path string) int {
	t.Helper()
	n, err := agent.CountSubmits(path)
	if err != nil {
		t.Fatalf("CountSubmits(%s): %v", path, err)
	}
	return n
}

func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func tail(a *agent.Agent) string {
	return string(agent.StripANSI(a.RecentOutput(3000)))
}
