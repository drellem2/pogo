//go:build linux

package sleep

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// TestIsWakeSignal_Recognized verifies the parser fires only on the resume
// edge of PrepareForSleep — body=false. logind sends one signal with
// body=true just before suspend and a second with body=false on resume; we
// must skip the first and act on the second.
func TestIsWakeSignal_Recognized(t *testing.T) {
	cases := []struct {
		name string
		sig  *dbus.Signal
		want bool
	}{
		{
			name: "resume edge (body=false) fires",
			sig: &dbus.Signal{
				Sender: ":1.42",
				Path:   logindObjectPath,
				Name:   logindFullName,
				Body:   []any{false},
			},
			want: true,
		},
		{
			name: "about-to-sleep edge (body=true) does not fire",
			sig: &dbus.Signal{
				Sender: ":1.42",
				Path:   logindObjectPath,
				Name:   logindFullName,
				Body:   []any{true},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		if got := isWakeSignal(tc.sig); got != tc.want {
			t.Errorf("[%s] isWakeSignal = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIsWakeSignal_Rejected guards against firing the heartbeat nudge for
// unrelated signals delivered on the same bus connection — every match rule
// is additive on a shared system bus, so other consumers' subscriptions can
// land in our channel.
func TestIsWakeSignal_Rejected(t *testing.T) {
	cases := map[string]*dbus.Signal{
		"nil signal": nil,
		"wrong member": {
			Name: "org.freedesktop.login1.Manager.PrepareForShutdown",
			Body: []any{false},
		},
		"wrong interface": {
			Name: "org.freedesktop.UPower.Resuming",
			Body: []any{false},
		},
		"empty body": {
			Name: logindFullName,
			Body: []any{},
		},
		"non-bool first arg": {
			Name: logindFullName,
			Body: []any{"false"},
		},
	}
	for name, sig := range cases {
		if isWakeSignal(sig) {
			t.Errorf("[%s] expected reject, got match", name)
		}
	}
}

// TestWatch_NilHookRejected ensures we fail fast on a misuse rather than
// opening a system-bus connection for no listener.
func TestWatch_NilHookRejected(t *testing.T) {
	err := Watch(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil hook, got nil")
	}
}

// TestWatch_StartsAndStops drives the full Watch lifecycle on a real
// system-bus connection: open, subscribe, cancel context, verify the
// dispatcher exits without firing the hook spuriously. Skipped when the
// host has no D-Bus or no logind (containers, minimal images, CI runners
// without dbus-daemon) — that fallback path is the spec-required degrade.
func TestWatch_StartsAndStops(t *testing.T) {
	if testing.Short() {
		t.Skip("opens a system-bus connection; skipped in -short")
	}
	ctx, cancel := context.WithCancel(context.Background())

	var fired int32
	if err := Watch(ctx, func() { atomic.AddInt32(&fired, 1) }); err != nil {
		cancel()
		t.Skipf("system bus / logind unavailable in this environment: %v", err)
	}

	// Give the dispatcher a moment to settle. We don't expect a real
	// suspend during the test; the point is to verify clean shutdown,
	// not to observe a wake.
	time.Sleep(200 * time.Millisecond)
	cancel()

	time.Sleep(200 * time.Millisecond)
	first := atomic.LoadInt32(&fired)
	time.Sleep(300 * time.Millisecond)
	if atomic.LoadInt32(&fired) != first {
		t.Errorf("hook fired after ctx cancel; before=%d after=%d", first, atomic.LoadInt32(&fired))
	}
}
