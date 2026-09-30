package service

import (
	"fmt"
	"os"
	"path/filepath"
)

// Size-based startup rotation for the launchd-managed pogod log (mg-6d02,
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
//
// "pogod.log" above is shorthand: the file rotated is whichever one the
// loaded job's StandardErrorPath names — the file fd 2 actually writes to —
// which on a host with an older or hand-edited plist is not this build's
// default (drellem2/pogo#104).

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

// Rotation reasons — the one word pogod's startup line reports for every
// boot (drellem2/pogo#104). Before it, "stderr is a tty", "the log is small"
// and "the path this build computed does not exist" all returned the same
// silent (false, path, nil), so a host whose installed plist named a
// different file went unrotated with nothing saying so.
const (
	// RotationNotRedirected: fd 2 is not a regular file (tty, pipe, socket,
	// /dev/null). A foreground dev run; there is no log to rotate.
	RotationNotRedirected = "not-redirected"
	// RotationBelowThreshold: the file fd 2 writes to is under maxPogodLogSize.
	RotationBelowThreshold = "below-threshold"
	// RotationPathMissing: fd 2 is a regular file, but no path we could find
	// names it (unlinked, renamed away, or its name unreadable and neither
	// fallback path is it). An anomaly — the log is being written somewhere
	// nobody can grep — not a no-op.
	RotationPathMissing = "path-missing"
	// RotationRotated: the file was rotated and fds 1/2 re-pointed.
	RotationRotated = "rotated"
	// RotationFailed: rotation was due but a rename or reopen failed; the
	// accompanying error says which.
	RotationFailed = "rotate-failed"
)

// Where LogRotation.Path came from.
const (
	logPathFromFd        = "fd 2"
	logPathFromInstalled = "installed plist"
	logPathFromDefault   = "default"
)

// LogRotation is the outcome of the startup rotation, for pogod's one-line
// report.
type LogRotation struct {
	// Reason is one of the Rotation* constants.
	Reason string
	// Path is the file judged: the one fd 2 writes to when it could be
	// resolved, or the first fallback tried when it could not. Empty when
	// not redirected.
	Path string
	// Source says where Path came from: fd 2 itself, the installed plist, or
	// this build's default.
	Source string
	// Size is the file's size when it was measured (below-threshold, rotated).
	Size int64
	// ResolveErr is why fd 2's own path could not be read, when the
	// fallbacks were used.
	ResolveErr error
}

// Rotated reports whether the file was rotated.
func (r LogRotation) Rotated() bool { return r.Reason == RotationRotated }

// Summary renders r for pogod's startup line: "<reason> (stderr=<path>)".
func (r LogRotation) Summary() string {
	reason := r.Reason
	switch r.Reason {
	case RotationBelowThreshold:
		reason = fmt.Sprintf("%s %d < %d bytes", r.Reason, r.Size, int64(maxPogodLogSize))
	case RotationRotated:
		reason = fmt.Sprintf("%s %d bytes; previous run's log is %s.1", r.Reason, r.Size, r.Path)
	}
	stderr := r.Path
	if r.Reason == RotationNotRedirected {
		stderr = "not a regular file"
	} else if r.Source != logPathFromFd {
		stderr = fmt.Sprintf("%s, from %s", r.Path, r.Source)
		if r.ResolveErr != nil {
			stderr += fmt.Sprintf("; fd 2's own path unreadable: %v", r.ResolveErr)
		}
	}
	return fmt.Sprintf("%s (stderr=%s)", reason, stderr)
}

// stderrLogPath resolves the path fd 2 actually points at (drellem2/pogo#104).
// redirected is false when fd 2 is not a regular file — a tty, pipe, socket
// or /dev/null — which is a foreground or captured run, not a log. When it is
// a regular file, path is the kernel's name for it (F_GETPATH on darwin,
// /proc/self/fd/2 on linux), or "" with err when the kernel would not say.
func stderrLogPath() (path string, redirected bool, err error) {
	fi, statErr := os.Stderr.Stat()
	if statErr != nil || !fi.Mode().IsRegular() {
		return "", false, nil
	}
	path, err = fdPath(2)
	return path, true, err
}

// resolveStderrPath is stderrLogPath, as a variable so a test can force the
// pre-#104 behaviour (a path computed from this build's template) and show
// that the tests for the fix fail against it.
var resolveStderrPath = stderrLogPath

// RotatePogodLogIfNeeded rotates the file pogod's stderr is redirected to and
// re-points this process's stdout/stderr at the fresh file. Called by pogod
// first thing in main().
//
// The file rotated is the one fd 2 actually writes to — whatever the loaded
// job's StandardErrorPath names — not a path this build computes
// (drellem2/pogo#104: a plist written by an older installer, or edited by
// hand, sends stderr elsewhere, and rotating the computed path rotated
// nothing). Only when fd 2's name cannot be read does it fall back to
// InstalledLogPath() and then PogodLogPath(). Whichever path is chosen must
// still be the same device+inode as fd 2, so a stale name never gets a
// stranger's file renamed.
//
// A foreground dev run (stderr = tty or pipe) is never redirected.
//
// The returned LogRotation always carries a Reason for the caller's startup
// line. Errors are advisory: the daemon must start even if rotation fails.
func RotatePogodLogIfNeeded() (LogRotation, error) {
	path, redirected, resErr := resolveStderrPath()
	if !redirected {
		return LogRotation{Reason: RotationNotRedirected}, nil
	}

	type candidate struct{ path, source string }
	var candidates []candidate
	if resErr == nil && path != "" {
		candidates = append(candidates, candidate{path, logPathFromFd})
	} else {
		if p, ok := InstalledLogPath(); ok {
			candidates = append(candidates, candidate{p, logPathFromInstalled})
		}
		candidates = append(candidates, candidate{PogodLogPath(), logPathFromDefault})
	}

	r := LogRotation{Reason: RotationPathMissing, Path: candidates[0].path, Source: candidates[0].source, ResolveErr: resErr}
	found := false
	for _, c := range candidates {
		if stderrIsSameFile(c.path) {
			r.Path, r.Source, found = c.path, c.source, true
			break
		}
	}
	if !found {
		return r, nil
	}

	fi, statErr := os.Stat(r.Path)
	if statErr != nil {
		// Matched a moment ago; gone now. Still an anomaly, not a no-op.
		return r, nil
	}
	r.Size = fi.Size()
	if r.Size < maxPogodLogSize {
		r.Reason = RotationBelowThreshold
		return r, nil
	}
	if err := rotateChain(r.Path, pogodLogKeep); err != nil {
		r.Reason = RotationFailed
		return r, err
	}
	// From here our (and launchd's) original fd still writes to the renamed
	// <log>.1 — harmless. Reopen the path and dup2 over fds 1/2 so the rest
	// of this run, including any terminal panic, lands in the fresh file.
	if err := redirectStdioTo(r.Path); err != nil {
		r.Reason = RotationFailed
		return r, fmt.Errorf("rotated %s but could not reopen it: %w", r.Path, err)
	}
	r.Reason = RotationRotated
	return r, nil
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
