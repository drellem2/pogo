package main

import (
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/drellem2/pogo/internal/service"
)

// logPipedStdio records that this pogod was started with stdio on a pipe and
// WHO started it. The parent is the half the 2026-09 outage never had: its
// postmortem left the spawner "unresolved" because by the time anyone looked,
// the pipe's reader — and usually the daemon — were gone (mg-a7a1). ppid 1
// means the parent had already exited before this line, which is itself the
// signature of the fault this redirect exists to survive.
func logPipedStdio(p service.PipedStdio, path string, err error) {
	var fds []string
	if p.Stdout {
		fds = append(fds, "stdout")
	}
	if p.Stderr {
		fds = append(fds, "stderr")
	}
	ppid := os.Getppid()
	parent := parentCommand(ppid)
	if err != nil {
		log.Printf("pogod: %s was a PIPE (parent pid=%d %s); could not open %s (%v), so stdio now goes to %s — this run's log is LOST, but a pipe would have killed the daemon once its reader exited",
			strings.Join(fds, "+"), ppid, parent, path, err, os.DevNull)
		return
	}
	log.Printf("pogod: %s was a PIPE (parent pid=%d %s); re-pointed stdout+stderr at %s so the daemon outlives the pipe's reader (mg-a7a1). A pogod not started by launchd should be started with its output on this file",
		strings.Join(fds, "+"), ppid, parent, path)
}

// parentCommand names the parent process for the log line, best-effort.
func parentCommand(ppid int) string {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(ppid)).Output()
	if err != nil {
		return "(command unreadable)"
	}
	c := strings.TrimSpace(string(out))
	if c == "" {
		return "(command unreadable)"
	}
	return "command=" + strconv.Quote(c)
}
