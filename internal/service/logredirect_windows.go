//go:build windows

package service

// Windows pogod is not launchd-managed, so startup log rotation never
// applies: report "not the log file" and make redirect a no-op.

func stderrIsSameFile(path string) bool { return false }

func redirectStdioTo(path string) error { return nil }
