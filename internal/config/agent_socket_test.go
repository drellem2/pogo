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

// homeOfLen returns an existing directory whose path is exactly n bytes long, so
// a test can sit a POGO_HOME precisely on a sun_path boundary.
func homeOfLen(t *testing.T, n int) string {
	t.Helper()
	base := shortHome(t)
	pad := n - len(base) - 1 // -1 for the separator
	if pad < 1 {
		t.Fatalf("cannot build a %d-byte home: base %q is already %d bytes", n, base, len(base))
	}
	dir := filepath.Join(base, strings.Repeat("d", pad))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if len(dir) != n {
		t.Fatalf("built home %q is %d bytes, want %d", dir, len(dir), n)
	}
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

// bindOK reports whether a unix socket can actually be bound at path.
func bindOK(t *testing.T, path string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	l.Close()
	return nil
}

// TestMaxUnixSocketPathLenBinds pins maxUnixSocketPathLen against the running
// kernel from above: a path of exactly that length must bind. It deliberately
// does not assert that one more byte fails — the constant is the darwin figure
// (103) used on every platform, and linux tolerates 107, so being conservative
// is the intent. Too *large* a constant is the bug this catches.
func TestMaxUnixSocketPathLenBinds(t *testing.T) {
	sock := filepath.Join(homeOfLen(t, maxUnixSocketPathLen-len("/x.sock")), "x.sock")
	if len(sock) != maxUnixSocketPathLen {
		t.Fatalf("test bug: built a %d-byte path, want %d", len(sock), maxUnixSocketPathLen)
	}
	if err := bindOK(t, sock); err != nil {
		t.Fatalf("maxUnixSocketPathLen=%d but binding a %d-byte path failed: %v",
			maxUnixSocketPathLen, len(sock), err)
	}
}

// TestAgentSocketDirBindableAtBudgetLimit is the load-bearing test for the
// budget constants. It sizes POGO_HOME so the derived dir lands exactly on
// agentSocketLeafBudget's limit, then binds a socket for the longest agent name
// the budget promises. The resulting path is exactly maxUnixSocketPathLen bytes,
// so an over-large maxUnixSocketPathLen or an under-sized leaf budget makes this
// fail with EINVAL rather than passing quietly.
func TestAgentSocketDirBindableAtBudgetLimit(t *testing.T) {
	const suffix = "/agents/sockets"
	root := homeOfLen(t, maxUnixSocketPathLen-agentSocketLeafBudget-len(suffix))
	t.Setenv("POGO_HOME", root)

	dir, inside := AgentSocketDir()
	if !inside {
		t.Fatalf("a root exactly at the budget limit must keep its sockets under POGO_HOME, got %q", dir)
	}
	sock := filepath.Join(dir, strings.Repeat("a", MaxAgentNameLen)+".sock")
	if len(sock) != maxUnixSocketPathLen {
		t.Fatalf("test bug: socket path is %d bytes, want exactly %d", len(sock), maxUnixSocketPathLen)
	}
	if err := bindOK(t, sock); err != nil {
		t.Fatalf("a %d-byte agent name at the budget limit must bind, got: %v", MaxAgentNameLen, err)
	}

	// One byte deeper must tip into the fallback rather than into a failed bind.
	t.Setenv("POGO_HOME", homeOfLen(t, maxUnixSocketPathLen-agentSocketLeafBudget-len(suffix)+1))
	if dir, inside := AgentSocketDir(); inside {
		t.Errorf("a root one byte past the budget must fall back, got %q under POGO_HOME", dir)
	}
}

// TestAgentSocketDirUnderPogoHome pins the headline behavior of mg-8532: a
// normal root keeps its attach sockets inside POGO_HOME, alongside the rest of
// the daemon state that PogoHome() seeds.
func TestAgentSocketDirUnderPogoHome(t *testing.T) {
	home := shortHome(t)
	t.Setenv("POGO_HOME", home)

	want := filepath.Join(home, "agents", "sockets")
	dir, inside := AgentSocketDir()
	if dir != want {
		t.Errorf("AgentSocketDir() = %q, want %q", dir, want)
	}
	if !inside {
		t.Errorf("AgentSocketDir() reported the fallback for a shallow root %q", home)
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
			dirA, _ := AgentSocketDir()
			t.Setenv("POGO_HOME", homeB)
			dirB, _ := AgentSocketDir()

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

// TestAgentSocketDirBindable checks that a socket for the longest budgeted agent
// name binds under both branches for ordinary roots. The boundary case — where
// the budget is actually load-bearing — is TestAgentSocketDirBindableAtBudgetLimit.
func TestAgentSocketDirBindable(t *testing.T) {
	longestName := strings.Repeat("a", MaxAgentNameLen)

	for _, tc := range []struct {
		name string
		home func(*testing.T) string
	}{
		{"shallow root", shortHome},
		{"deep root (sun_path fallback)", deepHome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POGO_HOME", tc.home(t))

			dir, _ := AgentSocketDir()
			t.Cleanup(func() { os.RemoveAll(dir) })

			sock := filepath.Join(dir, longestName+".sock")
			if err := bindOK(t, sock); err != nil {
				t.Fatalf("cannot bind %q (%d bytes): %v", sock, len(sock), err)
			}
		})
	}
}

// TestAgentSocketDirFallbackIsShort verifies the escape hatch is only taken when
// needed, and that when taken it lands somewhere that actually fits — a fallback
// that still overflows sun_path would trade a collision for a dead listener.
func TestAgentSocketDirFallbackIsShort(t *testing.T) {
	home := deepHome(t)
	t.Setenv("POGO_HOME", home)

	dir, inside := AgentSocketDir()
	if inside {
		t.Errorf("AgentSocketDir() = %q, want a path outside the too-deep root %q", dir, home)
	}
	if !agentSocketDirFits(dir) {
		t.Errorf("fallback dir %q (%d bytes) does not fit sun_path", dir, len(dir))
	}
}

// TestAgentSocketDirRootPogoHome guards the edge that made a prefix test the
// wrong way to detect the fallback: POGO_HOME="/" derives "/agents/sockets",
// which fits, so the sockets really are inside the root.
func TestAgentSocketDirRootPogoHome(t *testing.T) {
	t.Setenv("POGO_HOME", "/")
	dir, inside := AgentSocketDir()
	if want := filepath.Join("/", "agents", "sockets"); dir != want {
		t.Errorf("AgentSocketDir() = %q, want %q", dir, want)
	}
	if !inside {
		t.Errorf("AgentSocketDir() reported the fallback for POGO_HOME=/, but %q is inside it", dir)
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
			bare, _ := AgentSocketDir()
			t.Setenv("POGO_HOME", home+string(filepath.Separator))
			slashed, _ := AgentSocketDir()

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
			first, _ := AgentSocketDir()
			second, _ := AgentSocketDir()
			if first != second {
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
	if dir, _ := AgentSocketDir(); dir != want {
		t.Errorf("AgentSocketDir() with POGO_HOME=$HOME = %q, want %q", dir, want)
	}
}

// tmpDirOfLen returns an existing directory of exactly n bytes, for sitting
// TMPDIR on a chosen side of the socket-dir budget.
func tmpDirOfLen(t *testing.T, n int) string {
	t.Helper()
	base := shortHome(t)
	pad := n - len(base) - 1 // -1 for the separator
	if pad < 1 || pad > 255 {
		t.Fatalf("cannot build a %d-byte TMPDIR from a %d-byte base in one component", n, len(base))
	}
	dir := filepath.Join(base, strings.Repeat("t", pad))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if len(dir) != n {
		t.Fatalf("built TMPDIR %q is %d bytes, want %d", dir, len(dir), n)
	}
	return dir
}

// TestAgentSocketDirAlwaysFits is the invariant MaxAgentNameLen's promise rests
// on: whatever the root and whatever TMPDIR, the directory AgentSocketDir hands
// back leaves room for the reserved "/<name>.sock" leaf. Without it, a name
// inside the 24-byte ceiling can still fail to bind — which agent.Spawn treats
// as fatal — so the ceiling would be a ceiling again and not a promise.
//
// The long-TMPDIR rows are the regression: the fallback used to derive from
// os.TempDir() unchecked, so a TMPDIR past ~52 bytes produced a socket dir in
// which no legal agent name could bind (mg-ef80, review round 1 blocking 1).
func TestAgentSocketDirAlwaysFits(t *testing.T) {
	// bindable is false for "/": its socket dir fits, but only root can create
	// /agents/sockets, so that row asserts the budget without binding.
	roots := map[string]struct {
		mk       func(*testing.T) string
		bindable bool
	}{
		"shallow root": {shortHome, true},
		"deep root":    {deepHome, true},
		"root is /":    {func(*testing.T) string { return "/" }, false},
	}
	tmpdirs := map[string]func(*testing.T) string{
		"short TMPDIR": func(t *testing.T) string { return tmpDirOfLen(t, 20) },
		"TMPDIR at the budget limit": func(t *testing.T) string {
			return tmpDirOfLen(t, maxUnixSocketPathLen-agentSocketLeafBudget-len("/pogo-agents-abcdef01"))
		},
		"TMPDIR one byte past the budget": func(t *testing.T) string {
			return tmpDirOfLen(t, maxUnixSocketPathLen-agentSocketLeafBudget-len("/pogo-agents-abcdef01")+1)
		},
		"pathologically long TMPDIR": func(t *testing.T) string { return tmpDirOfLen(t, 90) },
	}

	for rootName, root := range roots {
		for tmpName, mkTmp := range tmpdirs {
			t.Run(rootName+"/"+tmpName, func(t *testing.T) {
				t.Setenv("POGO_HOME", root.mk(t))
				t.Setenv("TMPDIR", mkTmp(t))

				dir, _ := AgentSocketDir()
				if !agentSocketDirFits(dir) {
					t.Fatalf("AgentSocketDir() = %q (%d bytes): no room for the %d-byte leaf under the %d-byte sun_path limit",
						dir, len(dir), agentSocketLeafBudget, maxUnixSocketPathLen)
				}
				if !root.bindable {
					return
				}
				// The budget is only meaningful if the longest promised name
				// actually binds there.
				sock := filepath.Join(dir, strings.Repeat("a", MaxAgentNameLen)+".sock")
				if err := bindOK(t, sock); err != nil {
					t.Fatalf("a %d-byte name must bind under %q, got: %v", MaxAgentNameLen, dir, err)
				}
			})
		}
	}
}

// TestAgentSocketDirPrefersTempDirWhenItFits pins that the /tmp last resort is a
// degradation, not the default: a TMPDIR with room keeps the sockets under it,
// preserving the per-user $TMPDIR isolation darwin gives us.
func TestAgentSocketDirPrefersTempDirWhenItFits(t *testing.T) {
	tmp := tmpDirOfLen(t, 20)
	t.Setenv("POGO_HOME", deepHome(t))
	t.Setenv("TMPDIR", tmp)

	dir, inside := AgentSocketDir()
	if inside {
		t.Fatalf("a deep root must fall back, got %q inside POGO_HOME", dir)
	}
	if filepath.Dir(dir) != tmp {
		t.Errorf("AgentSocketDir() = %q, want it under the fitting TMPDIR %q", dir, tmp)
	}
}

// TestAgentSocketDirFallsBackToTmpWhenTempDirTooLong is the other side: an
// unusable TMPDIR degrades to /tmp rather than to a directory nothing can bind
// in. Distinctness per root must survive that degradation.
func TestAgentSocketDirFallsBackToTmpWhenTempDirTooLong(t *testing.T) {
	t.Setenv("TMPDIR", tmpDirOfLen(t, 90))

	t.Setenv("POGO_HOME", deepHome(t))
	first, inside := AgentSocketDir()
	if inside {
		t.Fatalf("a deep root must fall back, got %q inside POGO_HOME", first)
	}
	if filepath.Dir(first) != "/tmp" {
		t.Errorf("AgentSocketDir() = %q, want it directly under /tmp", first)
	}

	t.Setenv("POGO_HOME", deepHome(t))
	second, _ := AgentSocketDir()
	if first == second {
		t.Errorf("two distinct roots share the degraded socket dir %q — the per-root distinctness guarantee is lost", first)
	}
}
