package service

import (
	"fmt"
	"os"
)

// fdPath reads the path the open descriptor fd names from /proc
// (drellem2/pogo#104). An unlinked file reads back as "<path> (deleted)";
// that is left as is, so the caller's same-inode check against it fails and
// the rotation reports path-missing rather than rotating a stranger.
func fdPath(fd int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
}
