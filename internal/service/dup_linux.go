package service

import "golang.org/x/sys/unix"

// dupFd duplicates oldfd onto newfd. Linux lacks dup2 on newer
// architectures (arm64), so use dup3, which all supported kernels provide.
func dupFd(oldfd, newfd int) error {
	return unix.Dup3(oldfd, newfd, 0)
}
