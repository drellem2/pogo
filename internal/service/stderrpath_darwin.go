package service

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fdPath asks the kernel which path the open descriptor fd names —
// fcntl(F_GETPATH), darwin's equivalent of reading /proc/self/fd/N (drellem2/pogo#104).
// x/sys exposes no pointer-taking fcntl wrapper, so this is the raw call; the
// uintptr(unsafe.Pointer(...)) conversion inside the syscall.Syscall argument
// list is the form the compiler keeps the buffer alive and in place for.
func fdPath(fd int) (string, error) {
	buf := make([]byte, unix.PathMax)
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", errno
	}
	return unix.ByteSliceToString(buf), nil
}
