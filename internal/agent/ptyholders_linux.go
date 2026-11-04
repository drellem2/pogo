package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ptySlaveHolders lists the processes that still hold slavePath open, by
// walking /proc/<pid>/fd. It runs only on waitAndHandle's drain-timeout path,
// so its cost (one readlink per open fd on the host) is paid only when a
// reader is already wedged.
//
// Why this exists (mg-c7a0d). On Linux a master Read returns EIO once the
// slave's last open file description is released. cat's fds 0/1/2 and pogod's
// own slave fd are all one description, so after the child is reaped and
// closeSlave runs, a reader that is still blocked means SOMEONE ELSE holds that
// description: a descendant of the agent that outlived it (it inherited
// stdout/stderr, and it survived the SIGHUP the session leader's exit sends,
// or was never in the foreground process group to receive it). The 10s bound
// (outputDrainTimeout) hides that holder; this names it, so the next
// occurrence carries its own diagnosis instead of a bare goroutine dump.
//
// The result says how many processes could NOT be inspected. An empty holder
// list is evidence of absence only when that count is zero: /proc/<pid>/fd of
// another user's process is unreadable, and a holder there would be missed.
func ptySlaveHolders(slavePath string) (holders []string, uninspected int) {
	if slavePath == "" {
		return nil, 0
	}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, -1
	}
	self := os.Getpid()
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a process directory (self, thread-self, sys, ...)
		}
		dir := "/proc/" + e.Name() + "/fd"
		fds, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				uninspected++ // permission: a holder here would be invisible
			}
			continue
		}
		var held []string
		for _, fd := range fds {
			if l, err := os.Readlink(dir + "/" + fd.Name()); err == nil && l == slavePath {
				held = append(held, fd.Name())
			}
		}
		if len(held) == 0 {
			continue
		}
		comm, _ := os.ReadFile("/proc/" + e.Name() + "/comm")
		who := strings.TrimSpace(string(comm))
		if pid == self {
			// pogod itself still holding the slave after closeSlave would be
			// an fd leak of ours, not an agent's straggler. Say so loudly.
			who += " (THIS pogod: a leaked slave fd)"
		}
		holders = append(holders, fmt.Sprintf("pid=%d ppid=%s comm=%s fds=%s",
			pid, procPPID(e.Name()), who, strings.Join(held, ",")))
	}
	return holders, uninspected
}

// procPPID reads field 4 of /proc/<pid>/stat. "?" when unreadable. The comm
// field (2) may contain spaces and parens, so parse after the LAST ')'.
func procPPID(pid string) string {
	b, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return "?"
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "?"
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return "?"
	}
	return f[1]
}

// ptyHolderScanSupported reports whether ptySlaveHolders can see anything on
// this OS. A test uses it to decide whether "no holders" is a claim.
const ptyHolderScanSupported = true
