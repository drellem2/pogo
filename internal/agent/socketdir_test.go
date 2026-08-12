package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// mustPerm returns dir's permission bits, failing the test if it cannot stat.
func mustPerm(t *testing.T, dir string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	return fi.Mode().Perm()
}

// TestNewRegistryTightensPreCreatedWorldWritableDir is the headline case of
// mg-f783. config.AgentSocketDir falls back to /tmp/pogo-agents-<hash of
// POGO_HOME> when POGO_HOME is too deep for sun_path. /tmp is world-writable
// and the hash is derived from a guessable root, so a local attacker can
// pre-create that leaf at 0777 and wait for pogod to bind attach sockets — a
// PTY — inside it. os.MkdirAll(dir, 0700) does not correct an existing dir's
// mode; NewRegistry must.
func TestNewRegistryTightensPreCreatedWorldWritableDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pogo-agents-deadbeef")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// Explicit chmod: the mode passed to Mkdir is masked by umask.
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if got := mustPerm(t, dir); got != 0777 {
		t.Fatalf("test setup: dir mode = %04o, want 0777", got)
	}

	if _, err := NewRegistry(dir); err != nil {
		t.Fatalf("NewRegistry on a dir we own: %v", err)
	}
	if got := mustPerm(t, dir); got != 0700 {
		t.Errorf("socket dir mode = %04o after NewRegistry, want 0700 — "+
			"an attach socket beneath it is reachable by any local user", got)
	}
}

// TestNewRegistryTightensGroupReadableDir covers the subtler half of the same
// bug: 0750 is not world-writable, but it still lets the group reach an attach
// socket. Only 0700 is acceptable.
func TestNewRegistryTightensGroupReadableDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if _, err := NewRegistry(dir); err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if got := mustPerm(t, dir); got != 0700 {
		t.Errorf("socket dir mode = %04o after NewRegistry, want 0700", got)
	}
}

// TestNewRegistryCreatesDirAt0700 pins the mode of a directory NewRegistry
// creates itself, which is the path every ordinary POGO_HOME takes.
func TestNewRegistryCreatesDirAt0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents", "sockets")
	if _, err := NewRegistry(dir); err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if got := mustPerm(t, dir); got != 0700 {
		t.Errorf("created socket dir mode = %04o, want 0700", got)
	}
}

// TestNewRegistryRefusesDirOwnedByAnotherUser is the other half of the mg-f783
// fix: chmod cannot save us from a directory we do not own, because the owner
// can chmod it right back. Refuse it instead.
//
// A directory owned by another uid cannot be manufactured without privileges,
// so this leans on a root-owned system directory. NewRegistry must not write to
// it — os.MkdirAll on an existing directory is a no-op — and the assertion that
// it returns an error is exactly the proof of that.
func TestNewRegistryRefusesDirOwnedByAnotherUser(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: every directory is owned by us")
	}
	const dir = "/usr"
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Skipf("%s is not a directory on this host", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || uint32(st.Uid) == uint32(os.Getuid()) {
		t.Skipf("%s is owned by the current user; no foreign-owned dir to test with", dir)
	}

	_, err = NewRegistry(dir)
	if err == nil {
		t.Fatalf("NewRegistry(%s) succeeded on a dir owned by uid %d", dir, st.Uid)
	}
	if !strings.Contains(err.Error(), "owned by uid") {
		t.Errorf("NewRegistry(%s) error = %v, want an ownership refusal", dir, err)
	}
}

// TestNewRegistryRefusesSymlinkedDir guards the pre-created-symlink variant:
// os.MkdirAll happily returns nil for a symlink pointing at an existing
// directory, so without O_NOFOLLOW pogod would bind its attach sockets wherever
// the attacker pointed, and the chmod would tighten the attacker's target
// rather than the leaf.
func TestNewRegistryRefusesSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "attacker")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Chmod(target, 0777); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	link := filepath.Join(root, "pogo-agents-deadbeef")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := NewRegistry(link)
	if err == nil {
		t.Fatalf("NewRegistry(%s) followed a symlink to %s", link, target)
	}
	// The raw ELOOP text ("too many levels of symbolic links") reads like a
	// filesystem oddity rather than a planted link; the message must name it.
	if !strings.Contains(err.Error(), "is a symlink") {
		t.Errorf("NewRegistry(%s) error = %v, want it to name the symlink", link, err)
	}
	if got := mustPerm(t, target); got != 0777 {
		t.Errorf("symlink target mode = %04o, want it untouched at 0777 — "+
			"NewRegistry chmod'd through the link", got)
	}
}

// ---------------------------------------------------------------------------
// the fallback nest (mg-a997)
// ---------------------------------------------------------------------------

// fallbackNest returns a nest root and a leaf inside it named for home, the way
// config.AgentSocketDir lays them out. The hash is faked — nothing here depends
// on the digest, only on the shape.
func fallbackNest(t *testing.T, hash string) (root, leaf string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "pogo-agents")
	return root, filepath.Join(root, hash)
}

// plantLeaf creates a leaf inside root recording home as its owner, standing in
// for a run that has already finished.
func plantLeaf(t *testing.T, root, hash, home string) string {
	t.Helper()
	leaf := filepath.Join(root, hash)
	if err := os.MkdirAll(leaf, 0700); err != nil {
		t.Fatalf("MkdirAll %s: %v", leaf, err)
	}
	if home != "" {
		if err := os.WriteFile(filepath.Join(leaf, FallbackHomeMarker), []byte(home), 0600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
	}
	return leaf
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

// TestPrepareFallbackSocketDirRecordsItsPogoHome pins the record the sweep runs
// on. Without it a leaf is a sha256 nobody can run backwards, and the only
// available rule would be age — which is the reading mg-de3c rejected, because
// on this box a sibling entry is very likely another agent's live run.
func TestPrepareFallbackSocketDirRecordsItsPogoHome(t *testing.T) {
	home := t.TempDir()
	_, leaf := fallbackNest(t, "abcdef01")

	if err := PrepareFallbackSocketDir(leaf, home); err != nil {
		t.Fatalf("PrepareFallbackSocketDir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(leaf, FallbackHomeMarker))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != home {
		t.Errorf("marker records %q, want the POGO_HOME %q", got, home)
	}
	if perm := mustPerm(t, leaf); perm != 0700 {
		t.Errorf("leaf mode = %04o, want 0700", perm)
	}
}

// TestPrepareFallbackSocketDirReapsRootsThatAreGone is the leak, closed. Every
// entry it removes belonged to a POGO_HOME that no longer exists on disk, so
// nothing can ever compute its path again — the directory is unreachable, not
// merely idle.
func TestPrepareFallbackSocketDirReapsRootsThatAreGone(t *testing.T) {
	root, _ := fallbackNest(t, "unused")

	deadHome := filepath.Join(t.TempDir(), "gone")
	if err := os.MkdirAll(deadHome, 0700); err != nil {
		t.Fatal(err)
	}
	dead := plantLeaf(t, root, "deadbeef", deadHome)
	if err := os.RemoveAll(deadHome); err != nil {
		t.Fatal(err)
	}

	liveHome := t.TempDir()
	live := plantLeaf(t, root, "11111111", liveHome)

	mine := filepath.Join(root, "22222222")
	if err := PrepareFallbackSocketDir(mine, t.TempDir()); err != nil {
		t.Fatalf("PrepareFallbackSocketDir: %v", err)
	}

	if exists(t, dead) {
		t.Errorf("leaf %s survived; its POGO_HOME %s is gone, so nothing will ever "+
			"look for it again and nothing else will ever remove it", dead, deadHome)
	}
	if !exists(t, live) {
		t.Errorf("leaf %s was removed but its POGO_HOME %s is still there — a live "+
			"daemon just lost its attach sockets", live, liveHome)
	}
	if !exists(t, mine) {
		t.Errorf("the sweep removed the dir it was called for (%s)", mine)
	}
}

// TestPrepareFallbackSocketDirKeepsWhatItCannotProveDead is the direction that
// costs more to get wrong. Every case here is a leaf the sweep has no proof
// about, and every one must survive: leaving a directory behind wastes an inode,
// deleting a live daemon's socket dir surfaces as a spawn that cannot bind,
// which agent.Spawn treats as fatal (mg-ef80).
func TestPrepareFallbackSocketDirKeepsWhatItCannotProveDead(t *testing.T) {
	root, _ := fallbackNest(t, "unused")

	// No marker at all: written by something that is not this code, or by a
	// concurrent pogod between its mkdir and its marker write.
	unmarked := plantLeaf(t, root, "aaaaaaaa", "")

	// A marker that records nothing. os.Stat("") fails, so a sweep that did not
	// special-case this would delete precisely the leaf whose provenance it
	// failed to write.
	empty := plantLeaf(t, root, "bbbbbbbb", "   \n")

	// A root that exists but cannot be stat'ed, because an ancestor is
	// unreadable. This is the difference between "gone" and "I cannot see it":
	// an unmounted network home or a tightened parent reads as an error, and
	// treating any error as absence deletes the socket dir of a daemon that is
	// running right now.
	unreadable := ""
	if os.Getuid() != 0 {
		locked := filepath.Join(t.TempDir(), "locked")
		hidden := filepath.Join(locked, "home")
		if err := os.MkdirAll(hidden, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0700) })
		if _, err := os.Stat(hidden); err == nil {
			t.Fatalf("test setup: %s is still stat-able through a 0000 parent", hidden)
		}
		unreadable = plantLeaf(t, root, "ffffffff", hidden)
	}

	// A plain file where a leaf would be.
	file := filepath.Join(root, "cccccccc")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}

	// A symlink pointing at a directory whose "POGO_HOME" is gone: following it
	// would delete the target, which is somebody else's tree by construction.
	targetHome := filepath.Join(t.TempDir(), "gone")
	if err := os.MkdirAll(targetHome, 0700); err != nil {
		t.Fatal(err)
	}
	target := plantLeaf(t, t.TempDir(), "target", targetHome)
	if err := os.RemoveAll(targetHome); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "dddddddd")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := PrepareFallbackSocketDir(filepath.Join(root, "eeeeeeee"), t.TempDir()); err != nil {
		t.Fatalf("PrepareFallbackSocketDir: %v", err)
	}

	for _, tc := range []struct{ what, path string }{
		{"a leaf with no marker", unmarked},
		{"a leaf whose marker is empty", empty},
		{"a plain file", file},
		{"a symlink", link},
		{"a symlink's target", target},
		{"a leaf whose root cannot be stat'ed", unreadable},
	} {
		if tc.path == "" {
			continue // the unreadable-root case does not apply when running as root
		}
		if !exists(t, tc.path) {
			t.Errorf("the sweep removed %s (%s); it had no proof the owner was gone",
				tc.what, tc.path)
		}
	}
}

// TestPrepareFallbackSocketDirRefusesAForeignNest is the security half of
// nesting. The leaf already refuses a parent it does not own (mg-f783); putting
// a shared directory ABOVE it under world-writable /tmp adds one more place an
// attacker can get there first, and pogod must refuse that too rather than
// create its sockets inside a directory somebody else controls.
func TestPrepareFallbackSocketDirRefusesAForeignNest(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: every directory is owned by us")
	}
	fi, err := os.Stat("/usr")
	if err != nil || !fi.IsDir() {
		t.Skip("/usr is not a directory on this host")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || uint32(st.Uid) == uint32(os.Getuid()) {
		t.Skip("/usr is owned by the current user; no foreign-owned dir to test with")
	}

	err = PrepareFallbackSocketDir("/usr/pogo-agents-leaf", t.TempDir())
	if err == nil {
		t.Fatal("PrepareFallbackSocketDir accepted a nest owned by another user")
	}
	if !strings.Contains(err.Error(), "owned by uid") {
		t.Errorf("error = %v, want an ownership refusal naming the uid", err)
	}
}

// TestPrepareFallbackSocketDirTightensALooseNest covers the same parent one
// step short of foreign: ours, but left group- or world-reachable by whatever
// umask created it. A 0777 nest lets any local user rename our leaf out from
// under the sockets inside it.
func TestPrepareFallbackSocketDirTightensALooseNest(t *testing.T) {
	root, leaf := fallbackNest(t, "abcdef01")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}

	if err := PrepareFallbackSocketDir(leaf, t.TempDir()); err != nil {
		t.Fatalf("PrepareFallbackSocketDir: %v", err)
	}
	if got := mustPerm(t, root); got != 0700 {
		t.Errorf("nest mode = %04o after prepare, want 0700 — any local user can "+
			"still replace the leaf holding pogod's attach sockets", got)
	}
}

// TestPrepareFallbackSocketDirRefusesAnEmptyPogoHome guards the one input that
// would poison the record rather than merely fail to write it: a leaf claiming
// to be owned by "" is a leaf the sweep must then refuse to judge forever.
func TestPrepareFallbackSocketDirRefusesAnEmptyPogoHome(t *testing.T) {
	_, leaf := fallbackNest(t, "abcdef01")
	if err := PrepareFallbackSocketDir(leaf, ""); err == nil {
		t.Fatal("PrepareFallbackSocketDir accepted an empty POGO_HOME")
	}
}

// TestPrepareFallbackSocketDirIsRepeatable pins that a second call over the same
// leaf is a no-op rather than a failure or a reset — pogod restarts constantly,
// and each restart runs this against a directory the previous one left behind.
func TestPrepareFallbackSocketDirIsRepeatable(t *testing.T) {
	home := t.TempDir()
	_, leaf := fallbackNest(t, "abcdef01")

	for i := 0; i < 3; i++ {
		if err := PrepareFallbackSocketDir(leaf, home); err != nil {
			t.Fatalf("PrepareFallbackSocketDir call %d: %v", i+1, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(leaf, FallbackHomeMarker))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != home {
		t.Errorf("marker records %q after three calls, want %q", got, home)
	}
}
