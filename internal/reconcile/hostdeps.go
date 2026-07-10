package reconcile

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// HostDeps returns a Deps wired to the real host: `launchctl print` for the
// loaded program and pid, `ps` for the process start time, and os.Stat for the
// file mtime. The launchctl target is gui/$UID/<label>, the same session-scoped
// domain form pogo's install and reaper paths use — without the gui/$UID prefix
// launchctl cannot find a per-user LaunchAgent.
//
// Every lookup fails soft (ok=false) rather than erroring: on a non-macOS host,
// or when a job is not loaded, the drift check simply skips the running-reality
// dimensions instead of reporting false drift. An absent signal is not drift.
func HostDeps() Deps {
	// One `launchctl print` per label is reused for both program and pid so a
	// single label costs one exec, not two.
	printCache := map[string]string{}
	getPrint := func(label string) string {
		if out, ok := printCache[label]; ok {
			return out
		}
		target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + label
		out, _ := exec.Command("launchctl", "print", target).CombinedOutput()
		printCache[label] = string(out)
		return string(out)
	}
	return Deps{
		LoadedProgram: func(label string) (string, bool) {
			return ParseLaunchctlProgram(getPrint(label))
		},
		RunningPID: func(label string) (int, bool) {
			return ParseLaunchctlPID(getPrint(label))
		},
		ProcStart: procStart,
		FileMtime: func(path string) (time.Time, bool) {
			fi, err := os.Stat(path)
			if err != nil {
				return time.Time{}, false
			}
			return fi.ModTime(), true
		},
	}
}

// procStart reads process pid's start time via `ps -o lstart=`. lstart prints a
// full local timestamp like "Wed Jul 10 15:50:52 2026", which we parse in the
// local zone. ok=false when the process is gone or the field cannot be parsed.
func procStart(pid int) (time.Time, bool) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		return time.Time{}, false
	}
	return parsePsLstart(string(out))
}

// parsePsLstart parses the `ps -o lstart=` field. Split out from procStart so it
// can be unit tested without a live process.
func parsePsLstart(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// e.g. "Wed Jul 10 15:50:52 2026" (day-of-month may be space-padded).
	if t, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", s, time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
}
