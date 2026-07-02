//go:build !windows

package service

import (
	"os"
	"syscall"
)

// stderrIsSameFile reports whether this process's stderr is the file at
// path (same device+inode). True only under the launchd
// StandardErrorPath redirect — a tty (foreground run) or a pipe
// (`pogo server start` output capture) never matches, which is what makes
// startup log rotation safe to call unconditionally.
func stderrIsSameFile(path string) bool {
	return sameFile(os.Stderr, path)
}

func sameFile(f *os.File, path string) bool {
	pfi, err := os.Stat(path)
	if err != nil {
		return false
	}
	pst, ok := pfi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	ffi, err := f.Stat()
	if err != nil {
		return false
	}
	fst, ok := ffi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return pst.Dev == fst.Dev && pst.Ino == fst.Ino
}

// redirectStdioTo opens path in append mode and dup2s it over fds 1 and 2,
// so everything written to stdout/stderr from now on — including runtime
// panic output — lands in the freshly rotated log. os.Stdout/os.Stderr
// wrap fds 1/2 directly, so they follow automatically.
func redirectStdioTo(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	fd := int(f.Fd())
	if err := dupFd(fd, 1); err != nil {
		return err
	}
	return dupFd(fd, 2)
}
