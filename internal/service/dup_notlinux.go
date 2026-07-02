//go:build !windows && !linux

package service

import "golang.org/x/sys/unix"

// dupFd duplicates oldfd onto newfd (darwin and the BSDs provide dup2).
func dupFd(oldfd, newfd int) error {
	return unix.Dup2(oldfd, newfd)
}
