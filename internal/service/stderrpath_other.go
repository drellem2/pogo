//go:build !darwin && !linux

package service

import "errors"

// fdPath has no implementation here: neither F_GETPATH nor /proc/self/fd is
// available, so startup rotation falls back to the installed plist's path
// and then the default (drellem2/pogo#104).
func fdPath(fd int) (string, error) {
	return "", errors.ErrUnsupported
}
