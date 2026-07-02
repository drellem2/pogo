package service

import (
	"fmt"
	"os"
	"path/filepath"
)

// Size-based startup rotation for the launchd-managed pogod.log (mg-6d02,
// follow-on to mg-fc73 / gh #22).
//
// launchd opens StandardOutPath/StandardErrorPath in append mode on modern
// macOS, so prior-run output (including crash traces) survives a KeepAlive
// respawn — but nothing ever trims the file, and a crash post-mortem needs
// the tail of the *previous* run to still be on disk, not lost to a manual
// `rm` when the log grows into the tens of megabytes. pogod therefore
// rotates its own log at startup: when pogod.log exceeds maxPogodLogSize it
// is renamed into a numbered chain (pogod.log.1 is the most recent) and a
// fresh file is dup2'd over stdout/stderr so the new run — and any Go panic
// it ends with — lands in the new pogod.log.
//
// Rotation happens only at startup, never mid-run: a crash boundary is a
// restart boundary, so the evidence for run N is always intact in either
// pogod.log or pogod.log.1 when run N+1 comes up.

const pogodLogName = "pogod.log"

// maxPogodLogSize is the startup-rotation threshold. 10 MiB holds several
// weeks of pogod output at observed volume, so the previous runs' evidence
// stays available well past any realistic post-mortem window.
const maxPogodLogSize = 10 << 20

// pogodLogKeep is how many rotated files are retained (pogod.log.1 ..
// pogod.log.N). Worst case on disk: (keep+1) * maxPogodLogSize ≈ 40 MiB.
const pogodLogKeep = 3

// PogodLogPath is the canonical daemon log location — the same path the
// launchd plist template points StandardOutPath/StandardErrorPath at.
func PogodLogPath() string {
	return filepath.Join(logDir(), pogodLogName)
}

// RotatePogodLogIfNeeded rotates pogod.log and re-points this process's
// stdout/stderr at the fresh file. Called by pogod first thing in main().
//
// It is a strict no-op unless the process's stderr is pogod.log itself
// (same device+inode) — i.e. unless we are actually running under the
// launchd redirect. A foreground dev run (stderr = tty) or a
// `pogo server start` spawn (stderr = capture pipe) is never redirected.
//
// Returns whether a rotation happened and the log path, for the caller's
// startup marker. Errors are advisory: the daemon must start even if
// rotation fails.
func RotatePogodLogIfNeeded() (rotated bool, logPath string, err error) {
	logPath = PogodLogPath()
	if !stderrIsSameFile(logPath) {
		return false, logPath, nil
	}
	fi, statErr := os.Stat(logPath)
	if statErr != nil {
		return false, logPath, nil
	}
	if fi.Size() < maxPogodLogSize {
		return false, logPath, nil
	}
	if err := rotateChain(logPath, pogodLogKeep); err != nil {
		return false, logPath, err
	}
	// From here our (and launchd's) original fd still writes to the renamed
	// pogod.log.1 — harmless. Reopen the path and dup2 over fds 1/2 so the
	// rest of this run, including any terminal panic, lands in the fresh
	// pogod.log.
	if err := redirectStdioTo(logPath); err != nil {
		return false, logPath, fmt.Errorf("rotated %s but could not reopen it: %w", logPath, err)
	}
	return true, logPath, nil
}

// rotateChain shifts logPath into a numbered retention chain:
// logPath.(keep-1) → logPath.keep (oldest dropped), …, logPath → logPath.1.
// Gaps in the chain are skipped; the final rename of logPath itself is the
// only step whose failure aborts the rotation.
func rotateChain(logPath string, keep int) error {
	os.Remove(fmt.Sprintf("%s.%d", logPath, keep)) // best-effort drop of the oldest
	for i := keep - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", logPath, i)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.Rename(src, fmt.Sprintf("%s.%d", logPath, i+1)); err != nil {
			return err
		}
	}
	return os.Rename(logPath, logPath+".1")
}
