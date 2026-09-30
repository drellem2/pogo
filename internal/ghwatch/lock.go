package ghwatch

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked is returned by Lock when another run holds the lock.
var ErrLocked = errors.New("another gh-watch run holds the lock")

// Lock takes the run lock for home, so a scheduled fire and a hand-run
// `--force` cannot interleave their read-modify-write of the File. The returned
// func releases it.
func Lock(home string) (func(), error) {
	if err := os.MkdirAll(Dir(home), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(Dir(home), lockFileName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
