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
