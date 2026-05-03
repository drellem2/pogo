//go:build linux

package sleep

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/godbus/dbus/v5"
)

// systemd-logind D-Bus identifiers. The PrepareForSleep signal fires twice
// around every suspend/resume cycle: once with body=true just before sleep,
// once with body=false on resume. We only care about the resume edge —
// that's what short-circuits the heartbeat detector's next tick.
const (
	logindBusName    = "org.freedesktop.login1"
	logindObjectPath = "/org/freedesktop/login1"
	logindInterface  = "org.freedesktop.login1.Manager"
	logindSignalName = "PrepareForSleep"
	logindFullName   = logindInterface + "." + logindSignalName
)

// Watch subscribes to systemd-logind's PrepareForSleep D-Bus signal and
// invokes hook when the system wakes from sleep. It returns once the
// subscription is established; the dispatcher runs in a goroutine until ctx
// is canceled (which closes the bus connection).
//
// The sleep-resilience design (docs/sleep-resilience-design.md §5) calls for
// a logind listener as the Linux equivalent of the macOS shim. We use the
// pure-Go github.com/godbus/dbus/v5 client so the shim builds with
// CGO_ENABLED=0 alongside the rest of pogod.
//
// Returns an error when the system bus is unreachable (containers without
// /var/run/dbus, minimal images), when logind is not present (Alpine /
// fly.io machines), or when the match rule is rejected. Callers should log
// and continue — the heartbeat detector keeps running and provides the
// portable fallback. Only wake latency is lost, not correctness.
func Watch(ctx context.Context, hook func()) error {
	if hook == nil {
		return errors.New("platform/sleep: hook must not be nil")
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("platform/sleep linux: connect system bus: %w", err)
	}
	if err := requireLogind(conn); err != nil {
		_ = conn.Close()
		return err
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(logindObjectPath),
		dbus.WithMatchInterface(logindInterface),
		dbus.WithMatchSender(logindBusName),
		dbus.WithMatchMember(logindSignalName),
	); err != nil {
		_ = conn.Close()
		return fmt.Errorf("platform/sleep linux: subscribe %s: %w", logindFullName, err)
	}
	// Buffer covers a burst of sleep/wake toggles without dropping signals
	// before the dispatcher goroutine reads them.
	ch := make(chan *dbus.Signal, 16)
	conn.Signal(ch)
	go runDispatch(ctx, conn, ch, hook)
	return nil
}

// requireLogind probes the system bus for the logind well-known name. We
// reject early when logind is absent so the caller's "shim unavailable" log
// names the actual reason, instead of leaving a silent subscription that
// will never fire.
func requireLogind(conn *dbus.Conn) error {
	var hasOwner bool
	err := conn.BusObject().Call(
		"org.freedesktop.DBus.NameHasOwner", 0, logindBusName,
	).Store(&hasOwner)
	if err != nil {
		return fmt.Errorf("platform/sleep linux: probe logind presence: %w", err)
	}
	if !hasOwner {
		return fmt.Errorf("platform/sleep linux: %s not registered on system bus (no logind)", logindBusName)
	}
	return nil
}

func runDispatch(ctx context.Context, conn *dbus.Conn, ch chan *dbus.Signal, hook func()) {
	defer conn.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-ch:
			if !ok {
				if ctx.Err() == nil {
					log.Printf("platform/sleep linux: D-Bus signal channel closed; falling back to heartbeat-only wake detection")
				}
				return
			}
			if isWakeSignal(sig) {
				hook()
			}
		}
	}
}

// isWakeSignal returns true for the resume edge of PrepareForSleep — i.e. a
// signal whose interface.member is org.freedesktop.login1.Manager.PrepareForSleep
// and whose single boolean argument is false ("woken"). The about-to-sleep
// edge (true) and unrelated signals on the same connection return false.
func isWakeSignal(s *dbus.Signal) bool {
	if s == nil || s.Name != logindFullName || len(s.Body) == 0 {
		return false
	}
	preparing, ok := s.Body[0].(bool)
	if !ok {
		return false
	}
	return !preparing
}
