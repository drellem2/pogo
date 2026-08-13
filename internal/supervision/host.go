package supervision

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/nightlyone/lockfile"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/reconcile"
)

// Observe takes the real reading on this host: `launchctl print` for the job's
// pid and last exit reason, the pogod lockfile for the process that owns this
// POGO_HOME, and `ps` for the holder's parent.
//
// Every lookup fails soft. A missing reading lands in Observation with its ok
// flag false and, where the reason is knowable, a line in ReadErr — never as a
// zero pid that Check would compare. That is the whole reason the ok flags
// exist: pid 0 cannot distinguish "no such process" from "could not look".
//
// The launchctl target is gui/$UID/<label>, the session-scoped domain form
// every other launchd path in pogo uses; without the gui/$UID prefix launchctl
// cannot find a per-user LaunchAgent at all.
func Observe(label string) Observation {
	obs := Observation{Label: label}

	target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + label
	out, err := exec.Command("launchctl", "print", target).CombinedOutput()
	printed := string(out)
	switch {
	case err != nil && strings.TrimSpace(printed) == "":
		// launchctl itself is unavailable (not macOS, not on PATH). Say so:
		// this is a reading that could not be taken, not a job that is absent.
		obs.ReadErr = fmt.Sprintf("launchctl print %s: %v", target, err)
	case err != nil:
		// launchctl ran and refused — almost always "Could not find service".
		// The job is genuinely not loaded, which is a real reading.
		obs.JobLoaded = false
	default:
		obs.JobLoaded = true
		obs.JobPID, obs.JobPIDOK = reconcile.ParseLaunchctlPID(printed)
		obs.LastExitReason = parseLastExitReason(printed)
	}

	lock, lerr := lockfile.New(config.LockfilePath())
	if lerr != nil {
		obs.ReadErr = joinErr(obs.ReadErr, fmt.Sprintf("lockfile %s: %v", config.LockfilePath(), lerr))
		return obs
	}
	// GetOwner validates that the recorded pid is a live process, so a stale
	// lockfile left by a crashed daemon reads as "no holder" rather than as a
	// phantom rival. That is the behaviour this check wants: a dead pid is not
	// a displacement.
	proc, gerr := lock.GetOwner()
	if gerr != nil || proc == nil {
		return obs
	}
	obs.LockPID, obs.LockPIDOK = proc.Pid, true
	obs.LockPPID, obs.LockPPIDOK = procPPID(proc.Pid)
	return obs
}

// Check is the one call a caller needs: observe this host, then judge.
func CheckHost(label string) Result { return Check(Observe(label)) }

// parseLastExitReason pulls launchd's `last exit reason = …` line out of
// `launchctl print` output. Report-only (see the Observation field doc), but
// worth carrying: on 2026-08-13 it read OS_REASON_CODESIGNING on a healthy
// daemon, which was the only surviving trace of a launch-constraint violation
// that killed the first post-kickstart spawn 29ms in. Nothing else in pogo
// recorded that the deploy's restart took two attempts.
func parseLastExitReason(printOutput string) string {
	for _, ln := range strings.Split(printOutput, "\n") {
		t := strings.TrimSpace(ln)
		if v, ok := strings.CutPrefix(t, "last exit reason = "); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	return ""
}

// procPPID reads a process's parent pid via `ps -o ppid=`. Carried for the
// report only — see the package doc on why ppid 1 establishes nothing.
func procPPID(pid int) (int, bool) {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	ppid, cerr := strconv.Atoi(strings.TrimSpace(string(out)))
	if cerr != nil {
		return 0, false
	}
	return ppid, true
}

// joinErr appends a second reading failure to an existing one without losing
// the first. Two unreadable sides is a worse state than one and the report
// should say both.
func joinErr(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}
