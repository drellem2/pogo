package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// FallbackHomeMarker is the file inside a hashed fallback socket dir that
// records which POGO_HOME the directory belongs to.
//
// It is what makes the nest reapable. A leaf is named for a sha256 of its
// POGO_HOME, which does not run backwards, so a sweep looking at a sibling
// cannot otherwise tell whose it is. The name a directory carries answers "is
// this mine"; this file answers "does its owner still exist", which is the
// question the sweep needs.
//
// It cannot collide with the "<agent name>.sock" files beside it — every one of
// those ends in ".sock" and this does not — and nothing reads this directory
// expecting only sockets: pogod addresses each socket by the path it built, and
// no code path lists the directory.
const FallbackHomeMarker = ".pogo-home"

// PrepareFallbackSocketDir claims dir for pogoHome and reaps the siblings whose
// own POGO_HOME is gone. Call it — before NewRegistry — only for the hashed
// fallback config.AgentSocketDir returns for a POGO_HOME too deep to hold its
// own sockets, never for a directory inside POGO_HOME.
//
// # Why a sweep at all, and why it is not testtmp's
//
// mg-de3c's sweep of internal/testtmp keys on OWNERSHIP: the entry name encodes
// the pid that made it, and signal 0 answers whether that process is still
// there. That mechanism does not reach here and cannot be made to. A pogod and
// the test binary that spawned it must derive one socket dir from one
// POGO_HOME, independently, or they bind and look in different places — which
// is mg-8532, whose symptom under the mg-d216 attach supervisor is two daemons
// unlinking each other's live socket forever. So the name can carry no pid.
//
// What generalises is the SHAPE, not the key. A fallback socket dir's owner is
// not a process, it is a POGO_HOME: the directory exists to serve exactly one
// root and is unreachable — by anything, ever — once that root is gone. So the
// liveness question becomes "is the POGO_HOME still on disk", recorded in
// FallbackHomeMarker and read back by the sweep. In production POGO_HOME is
// ~/.pogo and the answer is always yes, so nothing is ever removed; under test
// binaries it is a t.TempDir() the testing package deletes at the end of the
// test, and the leaf becomes provably unreachable at that moment (mg-a997).
//
// # What it will not remove
//
// Anything it cannot prove is dead: a leaf owned by another uid, a symlink, a
// plain file, a leaf whose marker is missing or unreadable, a marker recording
// an empty path, a root that cannot be stat'ed for any reason other than not
// being there, and its own dir. Every one is left in place. The cost
// of that is a leaf that outlives its root; the cost of the other direction is
// deleting a live daemon's attach sockets, and the two are not comparable.
func PrepareFallbackSocketDir(dir, pogoHome string) error {
	// Before anything is created. A leaf claiming to be owned by "" is a leaf
	// every later sweep must refuse to judge, forever.
	if pogoHome == "" {
		return fmt.Errorf("record the POGO_HOME owning %s: no POGO_HOME was supplied", dir)
	}
	// The nest parent is new attack surface and gets the same treatment as the
	// leaf. Under /tmp — AgentSocketDir's last resort, and world-writable — an
	// attacker who pre-creates the parent at 0777 owns every leaf pogod goes on
	// to make inside it, which is mg-f783 one level up. secureSocketDir refuses
	// a foreign owner, refuses a symlink, and tightens a loose mode; run it on
	// the parent BEFORE anything is created underneath.
	root := filepath.Dir(dir)
	if err := secureSocketDir(root); err != nil {
		return fmt.Errorf("prepare the agent socket fallback root: %w", err)
	}
	if err := secureSocketDir(dir); err != nil {
		return err
	}
	// The marker before the sweep, not after: a concurrent pogod's sweep skips
	// a leaf whose marker it cannot read, so the only window this ordering
	// leaves is one where our directory is kept.
	marker := filepath.Join(dir, FallbackHomeMarker)
	if err := os.WriteFile(marker, []byte(pogoHome), 0600); err != nil {
		return fmt.Errorf("record the POGO_HOME owning %s: %w", dir, err)
	}
	reapFallbackSocketDirs(root, filepath.Base(dir))
	return nil
}

// reapFallbackSocketDirs removes the leaves of root whose recorded POGO_HOME no
// longer exists, skipping keep. Errors are swallowed: a sweep that cannot
// delete something has lost nothing the caller can act on, and it must never be
// the reason pogod fails to start.
func reapFallbackSocketDirs(root, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	uid := os.Getuid()
	for _, e := range entries {
		if e.Name() == keep {
			continue
		}
		path := filepath.Join(root, e.Name())
		// Lstat, so a symlink planted at a leaf name is a symlink and not the
		// directory it points at. RemoveAll on a link removes the link, but the
		// checks below would otherwise be reading somebody else's directory to
		// decide it.
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || uint32(st.Uid) != uint32(uid) {
			continue
		}
		home, err := os.ReadFile(filepath.Join(path, FallbackHomeMarker))
		if err != nil {
			continue
		}
		// An empty marker is a botched write, not a dead owner. os.Stat("")
		// fails, so without this the one leaf whose provenance we failed to
		// record would be the one leaf we deleted.
		owner := strings.TrimSpace(string(home))
		if owner == "" {
			continue
		}
		// Only "it is not there" counts as gone. A stat that fails for any other
		// reason — EACCES because an ancestor is unreadable, an unmounted
		// network home, EIO — is a root we cannot see, not a root that has been
		// removed, and the leaf beside it may belong to a daemon that is running
		// right now.
		if _, err := os.Stat(owner); err == nil || !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		os.RemoveAll(path)
	}
}
