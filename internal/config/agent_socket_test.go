package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortHome returns a POGO_HOME short enough that AgentSocketDir keeps the
// sockets under it. t.TempDir() is unusable here: on darwin it returns a
// ~72-byte path under /var/folders that trips the sun_path fallback.
func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ph")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// deepHome returns a POGO_HOME deep enough to force AgentSocketDir's fallback
// on any platform. It nests until the derived dir provably cannot fit a socket,
// rather than assuming t.TempDir() is long (it is on darwin, not on linux).
func deepHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for agentSocketDirFits(filepath.Join(dir, "agents", "sockets")) {
		dir = filepath.Join(dir, "deeper")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return dir
}

// legacySharedDir is the pre-mg-8532 path: one $TMPDIR-derived directory shared
// by every daemon on the host regardless of POGO_HOME.
func legacySharedDir() string {
	return filepath.Join(os.TempDir(), "pogo-agents")
}

// TestAgentSocketDirUnderPogoHome pins the headline behavior of mg-8532: a
// normal root keeps its attach sockets inside POGO_HOME, alongside the rest of
// the daemon state that PogoHome() seeds.
func TestAgentSocketDirUnderPogoHome(t *testing.T) {
	home := shortHome(t)
	t.Setenv("POGO_HOME", home)

	want := filepath.Join(home, "agents", "sockets")
	if got := AgentSocketDir(); got != want {
		t.Errorf("AgentSocketDir() = %q, want %q", got, want)
	}
}

// TestAgentSocketDirDistinctPerPogoHome is the invariant the whole change
// exists to establish: two daemons on distinct roots never share a socket path,
// so identically-named agents cannot collide. Before mg-8532 both roots
// resolved to legacySharedDir().
func TestAgentSocketDirDistinctPerPogoHome(t *testing.T) {
	for _, tc := range []struct {
		name string
		home func(*testing.T) string
	}{
		{"shallow root", shortHome},
		{"deep root (sun_path fallback)", deepHome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			homeA, homeB := tc.home(t), tc.home(t)
			if homeA == homeB {
				t.Fatalf("test bug: both roots are %q", homeA)
			}

			t.Setenv("POGO_HOME", homeA)
			dirA := AgentSocketDir()
			t.Setenv("POGO_HOME", homeB)
			dirB := AgentSocketDir()

			if dirA == dirB {
				t.Errorf("distinct POGO_HOME roots share socket dir %q", dirA)
			}
			for _, dir := range []string{dirA, dirB} {
				if dir == legacySharedDir() {
					t.Errorf("AgentSocketDir() = %q, the shared pre-mg-8532 path", dir)
				}
			}
		})
	}
}

// TestAgentSocketDirBindable is the load-bearing test for agentSocketLeafBudget:
// a socket named for the longest agent we budget for must actually bind under
// the returned dir, in both the PogoHome and the fallback branch. It checks the
// constant against the kernel rather than against the 104/108 folklore.
func TestAgentSocketDirBindable(t *testing.T) {
	longestName := strings.Repeat("a", 24)

	for _, tc := range []struct {
		name string
		home func(*testing.T) string
	}{
		{"shallow root", shortHome},
		{"deep root (sun_path fallback)", deepHome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POGO_HOME", tc.home(t))

			dir := AgentSocketDir()
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatalf("MkdirAll(%q): %v", dir, err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })

			sock := filepath.Join(dir, longestName+".sock")
			l, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatalf("cannot bind %q (%d bytes): %v", sock, len(sock), err)
			}
			l.Close()
		})
	}
}

// TestAgentSocketDirFallbackIsShort verifies the escape hatch is only taken when
// needed, and that when taken it lands somewhere that actually fits — a fallback
// that still overflows sun_path would trade a collision for a dead listener.
func TestAgentSocketDirFallbackIsShort(t *testing.T) {
	home := deepHome(t)
	t.Setenv("POGO_HOME", home)

	dir := AgentSocketDir()
	if strings.HasPrefix(dir, filepath.Clean(home)+string(filepath.Separator)) {
		t.Errorf("AgentSocketDir() = %q, want a path outside the too-deep root %q", dir, home)
	}
	if !agentSocketDirFits(dir) {
		t.Errorf("fallback dir %q (%d bytes) does not fit sun_path", dir, len(dir))
	}
}

// TestAgentSocketDirStableAcrossSpellings guards the filepath.Clean inside the
// fallback hash. LockfilePath already treats "/a/b" and "/a/b/" as one daemon;
// the socket dir must agree, or a restart with a trailing slash would silently
// move every agent's socket.
func TestAgentSocketDirStableAcrossSpellings(t *testing.T) {
	for _, tc := range []struct {
		name string
		home func(*testing.T) string
	}{
		{"shallow root", shortHome},
		{"deep root (sun_path fallback)", deepHome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := tc.home(t)

			t.Setenv("POGO_HOME", home)
			bare := AgentSocketDir()
			t.Setenv("POGO_HOME", home+string(filepath.Separator))
			slashed := AgentSocketDir()

			if bare != slashed {
				t.Errorf("POGO_HOME %q -> %q but %q/ -> %q; spellings must agree",
					home, bare, home, slashed)
			}
		})
	}
}

// TestAgentSocketDirDeterministic verifies repeated calls agree. Every agent in
// a daemon binds independently, so a dir that varied per call would scatter
// sockets across directories.
func TestAgentSocketDirDeterministic(t *testing.T) {
	for _, tc := range []struct {
		name string
		home func(*testing.T) string
	}{
		{"shallow root", shortHome},
		{"deep root (sun_path fallback)", deepHome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POGO_HOME", tc.home(t))
			if first, second := AgentSocketDir(), AgentSocketDir(); first != second {
				t.Errorf("AgentSocketDir() not deterministic: %q then %q", first, second)
			}
		})
	}
}

// TestAgentSocketDirLegacyHomeNormalized verifies the POGO_HOME=$HOME legacy
// normalization (mg-3dc3) reaches the socket dir too: sockets belong under
// $HOME/.pogo, not scattered at the home dir root.
func TestAgentSocketDirLegacyHomeNormalized(t *testing.T) {
	home := shortHome(t)
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", home)

	want := filepath.Join(home, ".pogo", "agents", "sockets")
	if got := AgentSocketDir(); got != want {
		t.Errorf("AgentSocketDir() with POGO_HOME=$HOME = %q, want %q", got, want)
	}
}
