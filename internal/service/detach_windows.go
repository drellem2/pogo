//go:build windows

package service

import "fmt"

// DefaultDetachLogPath is unused on Windows but kept exported for symmetry
// so callers compile cleanly on every platform.
const DefaultDetachLogPath = ""

// Detach is a no-op stub on Windows: the launchd/systemd installers don't
// run there either, so there is nothing to detach from. Returns an error
// rather than silently succeeding.
func Detach(logPath string) (int, string, error) {
	return 0, "", fmt.Errorf("--detach is not supported on windows (service install requires darwin or linux)")
}
