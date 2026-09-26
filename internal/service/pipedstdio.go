package service

import (
	"os"
	"path/filepath"
)

// mg-a7a1 — a pogod whose stdout or stderr is a PIPE is only as alive as the
// process reading the other end.
//
// MEASURED by the 2026-09-26 recovery session (its figures, not re-derived
// here): the pogods it watched (pids 1260, 1786) had fd 2 on a pipe, and each
// died about 30s after boot, taking the mayor it had just spawned with it. One
// appeared (pid 15229) while com.pogo.daemon was booted out, so launchd was not
// the spawner. The spawner in this repo is internal/client.StartServer, which
// every `lsp`, `pose` and `pogo visit` reaches through RunWithHealthCheck — and
// the zsh integration runs `pogo visit` on every chpwd. It captured pogod's
// stdio on a pipe read by the CLI, and the CLI exits the instant pogod answers
// /health. Go kills a process that writes to a broken pipe on fd 1 or 2, so
// pogod's next log line was its last. Emacs's pogo-start spawns over a pipe
// too, which it reads until Emacs exits.
//
// StartServer now spawns onto the log file. This is the half that does not
// depend on having found every spawner: pogod itself, first thing in main,
// notices a piped stdout/stderr and re-points both at the log. An exhaustive
// list of the spawners on a box cannot be kept — the incident's spawner was
// "unresolved" for exactly that reason — but the property that kills the
// daemon is visible from inside it, on every boot, whoever the parent is.
//
// Only a pipe is re-pointed. A tty is someone watching a foreground run; a
// regular file is launchd's redirect (or the CLI's); a socket is systemd's
// journal. None of those has a reader whose exit kills the writer.

// PipedStdio records which of pogod's output descriptors were pipes at boot.
type PipedStdio struct {
	Stdout bool
	Stderr bool
}

// Any reports whether either output descriptor is a pipe.
func (p PipedStdio) Any() bool { return p.Stdout || p.Stderr }

// ObservePipedStdio reads fds 1 and 2. It writes nothing: a write to a pipe
// whose reader has already gone is precisely the thing that kills us.
func ObservePipedStdio() PipedStdio {
	return PipedStdio{Stdout: isPipe(os.Stdout), Stderr: isPipe(os.Stderr)}
}

func isPipe(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeNamedPipe != 0
}

// PipedStdioLogPath is where a piped pogod sends its output instead: the file
// the INSTALLED job names when there is one, so the daemon's own mg-a19a
// log-destination check reads it as written, and this build's default
// otherwise.
func PipedStdioLogPath() string {
	if p, ok := InstalledLogPath(); ok {
		return p
	}
	return PogodLogPath()
}

// RepointStdioAt dup2s path, opened for append, over fds 1 and 2. If the file
// cannot be opened it falls back to /dev/null and still returns the error:
// losing the log is recoverable, and the mg-a19a annunciator reports it by
// mail; keeping the pipe is the outage. The error is the caller's to log — by
// the time it returns, the log is the only place that can go.
func RepointStdioAt(path string) error {
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err == nil {
		err = redirectStdioTo(path)
	}
	if err != nil {
		_ = redirectStdioTo(os.DevNull)
	}
	return err
}
