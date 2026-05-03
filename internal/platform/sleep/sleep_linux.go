//go:build linux

package sleep

import "context"

// Watch is a no-op placeholder on Linux. The sleep-resilience design
// (docs/sleep-resilience-design.md §6, ticket mg-aa78 or follow-up) calls
// for a systemd-suspend / DBus PrepareForSleep listener here; until that
// lands, Linux pogod relies on the portable heartbeat detector for wake
// detection (correct, with up to one Interval of latency).
//
// Returning nil rather than an error means callers on Linux don't print a
// startup warning for an absence we expect. The hook is intentionally
// dropped — there is nothing to fire.
func Watch(_ context.Context, _ func()) error {
	return nil
}
