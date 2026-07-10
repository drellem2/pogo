package agent

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// secureSocketDir creates the agent socket dir if it does not exist, and
// refuses to use one that another local user could reach into.
//
// The attach sockets under this directory broker a PTY: whoever connects to
// one drives the agent's controlling terminal. The directory's own mode is the
// only thing guarding them, because net.Listen creates a unix socket at 0755
// no matter what umask says.
//
// os.MkdirAll(dir, 0700) alone does not establish that guard. It stamps 0700
// only on directories it creates; on an *existing* directory it returns nil
// without touching the mode. Under a world-writable parent — /tmp, which
// config.AgentSocketDir uses as its last resort for a POGO_HOME too deep for
// sun_path — a local attacker who guesses POGO_HOME can pre-create the hashed
// leaf at 0777 (or point it at a directory of their own) and then read or
// replace the sockets pogod binds inside it. Replacing one brokers a PTY: the
// attacker's listener answers `pogo agent attach` (mg-f783).
//
// So: never follow a symlink at the leaf, refuse a directory we do not own,
// and tighten a directory of ours that is more permissive than 0700. The
// checks run against an open file descriptor rather than the path, so a
// directory swapped in after the check cannot be the one we go on to use.
func secureSocketDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	// O_NOFOLLOW refuses a symlink at the final component. MkdirAll is happy
	// with one — it stats the path, finds a directory, and returns nil — so
	// this open is what stops an attacker-planted symlink to a directory they
	// control.
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		// O_NOFOLLOW on a symlink reports ELOOP on darwin and linux, EMLINK on
		// some BSDs. Say what actually happened: "too many levels of symbolic
		// links" reads like a filesystem oddity, not like someone planted a
		// link where pogod's sockets go.
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			return fmt.Errorf("socket dir %s is a symlink: refusing to broker agent PTYs "+
				"through a directory pogod did not create", dir)
		}
		return fmt.Errorf("open socket dir %s: %w", dir, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat socket dir %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("socket dir %s is not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("stat socket dir %s: no unix stat available", dir)
	}
	if uid := os.Getuid(); uint32(uid) != uint32(st.Uid) {
		return fmt.Errorf("socket dir %s is owned by uid %d, not %d: refusing to "+
			"broker agent PTYs through a directory owned by another user", dir, st.Uid, uid)
	}
	if perm := fi.Mode().Perm(); perm&0077 != 0 {
		// Ours, but group- or world-reachable. fchmod on the descriptor we
		// already validated, not on the path we validated a moment ago.
		if err := f.Chmod(0700); err != nil {
			return fmt.Errorf("tighten socket dir %s from mode %04o to 0700: %w", dir, perm, err)
		}
	}
	return nil
}
