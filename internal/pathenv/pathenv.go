// Package pathenv repairs the PATH for processes spawned by pogod.
//
// When pogod is launched by launchd (macOS) or systemd (Linux) it inherits a
// minimal or empty PATH. Any child it spawns by bare command name — most
// importantly `mg` for the scheduler/refinery mail-send fallback, plus `gh` and
// `git` — then fails to resolve with "executable file not found in $PATH".
//
// Ensure rewrites the current process's PATH so that:
//   - the directory containing the running pogod binary comes first (so an `mg`
//     shipped alongside pogod resolves), then
//   - whatever PATH was inherited, then
//   - well-known install locations as a backstop for an empty inherited PATH.
//
// Because Go's os/exec resolves bare command names against the parent process's
// PATH at exec.Command time, fixing the process PATH once at startup fixes every
// subprocess pogod spawns thereafter.
package pathenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// fallbackDirs lists common install locations for the tools pogod shells out to
// (mg, gh, git). They are appended last so a child resolves these even when the
// daemon's inherited PATH is empty, as happens under launchd.
func fallbackDirs() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return []string{
		"/opt/pogo/current/bin",
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
	}
}

// Augment builds an augmented PATH value from the current PATH and the directory
// of the running executable. exeDir (if non-empty) is placed first, then the
// entries of currentPATH, then the fallback directories. Empty and duplicate
// entries are dropped while order is otherwise preserved. It is pure so it can
// be unit tested without touching the process environment.
func Augment(currentPATH, exeDir string) string {
	var dirs []string
	if exeDir != "" {
		dirs = append(dirs, exeDir)
	}
	if currentPATH != "" {
		dirs = append(dirs, filepath.SplitList(currentPATH)...)
	}
	dirs = append(dirs, fallbackDirs()...)
	return strings.Join(dedupe(dirs), string(os.PathListSeparator))
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, d := range in {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// exeDir returns the directory containing the running executable, or "" if it
// cannot be determined (e.g. the binary was removed). os.Executable resolves
// symlinks well enough for our purposes here.
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// PATH returns the augmented PATH value for the current process.
func PATH() string {
	return Augment(os.Getenv("PATH"), exeDir())
}

// Ensure rewrites the current process's PATH environment variable to the
// augmented value. Call it once, early, before any subprocess is spawned.
// Idempotent: re-running it folds the already-present entries back together via
// dedupe rather than stacking them.
func Ensure() error {
	return os.Setenv("PATH", PATH())
}
