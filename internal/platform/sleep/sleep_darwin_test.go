//go:build darwin

package sleep

import (
	"context"
	"sync/atomic"
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
