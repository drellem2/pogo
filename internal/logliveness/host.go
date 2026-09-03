package logliveness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nightlyone/lockfile"

	"github.com/drellem2/pogo/internal/config"
)

// Observe takes the real reading on this host: the pogod lockfile for the
// process that owns this POGO_HOME, `lsof` for where that process's fd 2
// actually points, and `stat` on the named log for the context lines.
//
// Every lookup fails soft. A missing reading lands in Observation with its ok
// flag false and, where the reason is knowable, a line in ReadErr — never as
// an empty path Check would compare against LogPath and call Detached. An
// unreadable descriptor is Unknown, and Unknown is not a pass; getting that
// backwards would turn a broken instrument into a confident wrong answer,
// which is the failure mode this whole package is about.
//
// WHY lsof AND NOT A PROPERTY OF THE FILE. See the package doc: mtime,
// size and content all read healthy in the state this catches. The descriptor
// is the only reading that separates "this daemon writes here" from "something
// else wrote here recently", and from outside the daemon lsof is how the
// descriptor is read on darwin. When lsof is absent the answer is Unknown —
// stated, not guessed.
func Observe(logPath, jobLogPath string, now time.Time) Observation {
	obs := Observation{LogPath: logPath, JobLogPath: jobLogPath, Now: now}

	if lock, err := lockfile.New(config.LockfilePath()); err != nil {
		obs.ReadErr = joinErr(obs.ReadErr, fmt.Sprintf("lockfile %s: %v", config.LockfilePath(), err))
	} else if proc, gerr := lock.GetOwner(); gerr == nil && proc != nil {
		// GetOwner validates that the recorded pid is live, so a stale
		// lockfile from a crashed daemon reads as "no holder" rather than as
		// a phantom whose fd 2 we would then fail to read and report Unknown
		// about.
		obs.DaemonPID, obs.DaemonPIDOK = proc.Pid, true
	}

	if obs.DaemonPIDOK {
		path, err := processStderrPath(obs.DaemonPID)
		if err != nil {
			obs.ReadErr = joinErr(obs.ReadErr, err.Error())
		} else {
			obs.StderrPath, obs.StderrOK = resolve(path), true
		}
		obs.DaemonStart, obs.DaemonStartOK = processStart(obs.DaemonPID)
	}

	// LogPath is resolved the same way StderrPath is, so a symlinked log
	// directory does not read as a mismatch. Both sides through one function
	// is the point: resolving only one of them is how a comparison starts
	// disagreeing with itself.
	obs.LogPath = resolve(logPath)
	if st, err := os.Stat(obs.LogPath); err == nil {
		obs.LogExists, obs.LogMtime, obs.LogSize = true, st.ModTime(), st.Size()
	}
	return obs
}

// CheckHost is the one call a caller needs: observe this host, then judge.
func CheckHost(logPath, jobLogPath string) Result {
	return Check(Observe(logPath, jobLogPath, time.Now()))
}

// processStderrPath reads the name behind a process's fd 2.
//
// `lsof -p N -a -d 2 -F fn` emits one field per line, prefixed by field id:
// `p<pid>`, `f2`, `n<name>`. For the 2026-09 daemon this is `n/dev/ttys007`;
// for a launchd-started one it is the log path. A rotated-away log reads as
// the ROTATED name (lsof reports the inode's current name), which is the
// correct answer to "is the daemon writing the file you are about to grep" —
// it is not.
func processStderrPath(pid int) (string, error) {
	out, err := exec.Command("lsof", "-p", strconv.Itoa(pid), "-a", "-d", "2", "-F", "fn").Output()
	if err != nil {
		return "", fmt.Errorf("lsof -p %d -d 2: %v", pid, err)
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimRight(ln, "\r"), "n"); ok {
			if name = strings.TrimSpace(name); name != "" {
				return name, nil
			}
		}
	}
	return "", fmt.Errorf("lsof -p %d -d 2: no name field in output", pid)
}

// processStart reads a process's start time via `ps -o lstart=`. Context only
// — but it is the reading that turns "the log is stale" into the stronger and
// more actionable "the running daemon has never written to it".
//
// A parse failure is not an error worth surfacing: the field is decoration on
// the report and the verdict does not consult it.
func processStart(pid int) (time.Time, bool) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, false
	}
	t, perr := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.TrimSpace(string(out)), time.Local)
	if perr != nil {
		return time.Time{}, false
	}
	return t, true
}

// resolve canonicalises a path so both sides of the comparison are spelled the
// same way. A path that cannot be resolved (a tty, /dev/null, a deleted file,
// anything not on disk) is returned unchanged rather than blanked — the
// comparison still wants to see "/dev/ttys007", and blanking it would turn a
// legible mismatch into an empty string.
func resolve(p string) string {
	if p == "" {
		return p
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func joinErr(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}
