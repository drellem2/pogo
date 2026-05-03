//go:build !darwin && !linux

package sleep

import "context"

// Watch is a no-op on platforms without a dedicated sleep/wake notifier
// shim. The portable heartbeat detector remains the source of truth for
// wake detection on these platforms — correct, with up to one Interval of
// latency.
func Watch(_ context.Context, _ func()) error {
	return nil
}
