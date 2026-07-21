//go:build darwin

package sleep

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestIsUserWakeLine_Recognized verifies the parser accepts real wake-reason
// entries from the unified log. Fixtures are syslog-formatted samples
// captured via `log show --last 1d --predicate 'eventMessage CONTAINS[c]
// "wake reason"'` on darwin Sequoia.
func TestIsUserWakeLine_Recognized(t *testing.T) {
	cases := []string{
		// Kernel wake from keyboard press.
		`2026-05-03 16:05:52.080241+0100 0xca18f01  Default     0x0                  0      0    kernel: (AppleTopCaseHIDEventDriver) [HID] [ATC] AppleDeviceManagementHIDEventService::processWakeReason Wake reason: Keyboard (0x02)`,
		// airportd reporting kernel wake reason.
		`2026-05-03 16:06:37.525253+0100 0xca18572  Default     0x0                  179    0    airportd: (IO80211) [com.apple.WiFiManager:] Info: <airport[179]> systemWokenByWiFi: System wake reason: <smc.70070000 USB2_wake SMC.OutboxNotEmpty>, was not woken by WiFi, was not woken by Bluetooth`,
		// Lid-open wake (made-up but realistic).
		`2026-05-03 17:00:00.000000+0100 0xca18ff0  Default     0x0                  0      0    kernel: (AppleSMC) Wake reason: LID0`,
	}
	for _, line := range cases {
		if !isUserWakeLine(line) {
			t.Errorf("expected match for line: %s", line)
		}
	}
}

// TestIsUserWakeLine_Rejected verifies the parser ignores the stream's
// banner header, the unified log's echo of our own `log` invocations
// (feedback-loop guard), and unrelated lines.
func TestIsUserWakeLine_Rejected(t *testing.T) {
	cases := map[string]string{
		"empty":                     "",
		"banner":                    "Timestamp                       Thread     Type        Activity             PID    TTL  ",
		"self-reference (log show)": `2026-05-03 17:43:37.519139+0100 0xca5c279  Default     0x0                  32899  0    log: [com.apple.log:] log run noninteractively, parent: 32839 (zsh), args: '/usr/bin/log' 'show' '--last' '7d' '--predicate' 'eventMessage CONTAINS "Wake reason"'`,
		"self-reference (log stream, our predicate)": `2026-05-03 17:50:00.000000+0100 0xca6aaaa  Default     0x0                  40000  0    log: [com.apple.log:] log run noninteractively, parent: 39000 (pogod), args: '/usr/bin/log' 'stream' '--predicate' 'eventMessage CONTAINS[c] "Wake reason:"'`,
		"unrelated":                              `2026-05-03 16:00:00.000000+0100 0xca18000  Default     0x0                  100    0    powerd: (CoreFoundation) [com.apple.powerd] Display dimmed`,
		"contains 'wake' but not 'wake reason:'": `2026-05-03 16:00:00.000000+0100 0xca18001  Default     0x0                  101    0    kernel: NoWake-Lock created, dummy wake event`,
	}
	for name, line := range cases {
		if isUserWakeLine(line) {
			t.Errorf("[%s] expected reject, got match for: %s", name, line)
		}
	}
}

// TestWatch_NilHookRejected ensures we fail fast on a misuse rather than
// silently spawning a subprocess that nobody can listen to.
func TestWatch_NilHookRejected(t *testing.T) {
	err := Watch(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil hook, got nil")
	}
}

// TestWatch_StartsAndStops drives the full Watch lifecycle: spawn the `log
// stream` subprocess, let it run briefly, cancel the context, and verify
// the subprocess goroutine exits without leaking. We don't depend on a
// real wake event firing — that requires an actual host sleep, which the
// developer-Mac acceptance test covers.
func TestWatch_StartsAndStops(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns `log stream`; skipped in -short")
	}
	ctx, cancel := context.WithCancel(context.Background())

	var fired int32
	if err := Watch(ctx, func() { atomic.AddInt32(&fired, 1) }); err != nil {
		t.Skipf("log stream unavailable in this environment: %v", err)
	}

	// Give the subprocess a moment to start; we don't expect a wake event
	// during this window. The point is to verify clean shutdown, not to
	// observe a fire.
	time.Sleep(200 * time.Millisecond)
	cancel()

	// The reader goroutine has no externally visible done-signal, but it
	// shouldn't keep firing the hook after cancel. We give it a beat to
	// settle, then sample.
	time.Sleep(200 * time.Millisecond)
	first := atomic.LoadInt32(&fired)
	time.Sleep(300 * time.Millisecond)
	if atomic.LoadInt32(&fired) != first {
		t.Errorf("hook fired after ctx cancel; before=%d after=%d", first, atomic.LoadInt32(&fired))
	}
}

// TestParsePSLine covers the field split. The command field contains spaces —
// our own predicate has three — so a naive strings.Fields would truncate argv
// and silently stop matching the very processes the reaper exists to find.
func TestParsePSLine(t *testing.T) {
	pid, ppid, argv, ok := parsePSLine(` 3396  3335 log stream --predicate eventMessage CONTAINS[c] "Wake reason:"`)
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if pid != 3396 || ppid != 3335 {
		t.Errorf("pid/ppid = %d/%d, want 3396/3335", pid, ppid)
	}
	if want := `log stream --predicate eventMessage CONTAINS[c] "Wake reason:"`; argv != want {
		t.Errorf("argv = %q, want %q", argv, want)
	}

	for name, line := range map[string]string{
		"empty":        "",
		"pid only":     "3396",
		"non-numeric":  "header ppid command",
		"kernel pid 0": "    0     0 kernel_task",
	} {
		if _, _, _, ok := parsePSLine(line); ok {
			t.Errorf("[%s] expected parse to fail for %q", name, line)
		}
	}
}

// TestIsOrphanedWatcher_Matches pins the shapes the reaper must recognize:
// argv[0] as spawned today ("log"), and a fully-resolved path, both at PPID 1.
func TestIsOrphanedWatcher_Matches(t *testing.T) {
	for _, argv := range []string{
		`log stream --predicate ` + wakePredicate,
		`/usr/bin/log stream --predicate ` + wakePredicate,
	} {
		if !isOrphanedWatcher(1, argv) {
			t.Errorf("expected match for orphan argv: %s", argv)
		}
	}
}

// TestIsOrphanedWatcher_RejectsFleetProcesses is the safety test, and it is
// the reason the matcher is an exact comparison rather than a substring one.
// Anything isOrphanedWatcher accepts gets signalled. Every pogo poller idles
// in `sleep N` under `set -euo pipefail`, so a matcher that widened to a
// substring — the moral equivalent of an unanchored `pkill -f` — would take
// down the fleet's mail pollers and watchdog, as has happened before.
func TestIsOrphanedWatcher_RejectsFleetProcesses(t *testing.T) {
	cases := map[string]struct {
		ppid int
		argv string
	}{
		// The legitimate watcher of a RUNNING pogod. Reaping this would stop
		// wake detection outright — a regression disguised as a fix.
		"live pogod's own watcher": {3335, `log stream --predicate ` + wakePredicate},
		// Fleet infrastructure that an unanchored pattern has killed before.
		"mail poller":      {1, "sleep 600"},
		"watchdog poller":  {1, "/bin/sh -c while true; do sleep 60; done"},
		"pogod itself":     {1, "/Users/daniel/go/bin/pogod"},
		"a claude harness": {1, "claude --dangerously-skip-permissions"},
		"a polecat":        {1, "pogo-cat-55de"},
		// Other `log` invocations that are not ours.
		"unrelated log stream": {1, `log stream --predicate eventMessage CONTAINS "boot"`},
		"log show":             {1, `log show --last 1d --predicate ` + wakePredicate},
		// Substring-shaped near misses: a matcher doing strings.Contains
		// would accept these.
		"our predicate as an argument to something else": {1, `grep log stream --predicate ` + wakePredicate},
		"trailing junk after our predicate":              {1, `log stream --predicate ` + wakePredicate + ` --extra`},
		"argv0 only":                                     {1, "log"},
	}
	for name, c := range cases {
		if isOrphanedWatcher(c.ppid, c.argv) {
			t.Errorf("[%s] matcher accepted a process it must never signal: ppid=%d argv=%s", name, c.ppid, c.argv)
		}
	}
}

// TestReapOrphanedWatchers_KillsOrphanSparesLiveWatcher is the positive
// control. It reproduces the leak for real — a `log stream` whose parent has
// exited, reparented to PID 1, exactly as every non-graceful pogod death
// produces — then asserts the reaper kills that one and leaves a live,
// parented watcher running.
//
// Both halves matter. Without the orphan we would be testing a leak we never
// observed; without the live watcher we could "fix" the leak by killing the
// feature.
func TestReapOrphanedWatchers_KillsOrphanSparesLiveWatcher(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real `log stream` processes; skipped in -short")
	}
	if _, err := exec.LookPath("log"); err != nil {
		t.Skipf("log binary unavailable: %v", err)
	}

	// A live, correctly-parented watcher: the thing the reaper must spare.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := Watch(ctx, func() {}); err != nil {
		t.Skipf("log stream unavailable in this environment: %v", err)
	}
	live := watcherPIDsWithParent(t, os.Getpid())
	if len(live) != 1 {
		t.Fatalf("expected exactly 1 live watcher parented to this test, got %d", len(live))
	}

	// An orphan: sh spawns the watcher and exits, so it reparents to PID 1.
	// wakePredicate contains no single quotes, so single-quoting is safe.
	out, err := exec.Command("sh", "-c",
		`log stream --predicate '`+wakePredicate+`' >/dev/null 2>&1 & echo $!`).Output()
	if err != nil {
		t.Fatalf("spawning orphan: %v", err)
	}
	orphan, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parsing orphan pid from %q: %v", out, err)
	}
	// If the reaper fails to kill it, this test must not leave a leak behind —
	// the very thing it is policing. Kill by PID, never by pattern.
	t.Cleanup(func() {
		if alive(orphan) {
			_ = syscall.Kill(orphan, syscall.SIGTERM)
			t.Errorf("orphan %d survived the reaper; killed it in cleanup", orphan)
		}
	})

	if !waitFor(func() bool { return parentOf(t, orphan) == 1 }, 5*time.Second) {
		t.Fatalf("orphan %d never reparented to PID 1 (ppid=%d)", orphan, parentOf(t, orphan))
	}

	if n := reapOrphanedWatchers(); n < 1 {
		t.Fatalf("reapOrphanedWatchers reaped %d, want at least 1", n)
	}

	if !waitFor(func() bool { return !alive(orphan) }, 5*time.Second) {
		t.Errorf("orphan %d still alive after reap", orphan)
	}
	// The feature still works: our parented watcher was not collateral.
	if !alive(live[0]) {
		t.Errorf("reaper killed the LIVE watcher %d — wake detection would be dead", live[0])
	}
}

// watcherPIDsWithParent returns the pids of wake watchers whose parent is ppid.
func watcherPIDsWithParent(t *testing.T, ppid int) []int {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		pid, parent, argv, ok := parsePSLine(line)
		if !ok || parent != ppid {
			continue
		}
		if strings.HasSuffix(argv, "stream --predicate "+wakePredicate) {
			pids = append(pids, pid)
		}
	}
	return pids
}

func parentOf(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return -1
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return -1
	}
	return ppid
}

// alive reports whether pid exists. Signal 0 performs the existence check
// without delivering anything, and works for processes that are not our
// children (an orphan is init's child, so we cannot Wait on it).
func alive(pid int) bool {
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

func waitFor(cond func() bool, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}
