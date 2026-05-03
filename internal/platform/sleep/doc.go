// Package sleep is the platform-specific sleep/wake notifier shim called for
// in the sleep-resilience design (mg-c4a3, ticket #5 of 6 — optional latency
// optimization). The portable internal/heartbeat detector already catches
// host wakes correctly within one tick interval (default 30s); this package
// reduces wake-event latency to <1s on platforms where the OS exposes a
// faster signal.
//
// Each platform implements:
//
//	func Watch(ctx context.Context, hook func()) error
//
// On wake, hook is invoked. The expected wiring is to pass
// (*heartbeat.Detector).Nudge as the hook so the heartbeat short-circuits
// its next scheduled Tick.
//
// Watch returns an error when the platform cannot register for sleep/wake
// notifications (missing binary, sandbox, CI runner). Callers should log
// and continue — the heartbeat detector keeps running and provides the
// portable fallback.
package sleep
