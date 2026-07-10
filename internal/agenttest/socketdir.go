// Package agenttest provides helpers for tests that build a real
// agent.Registry. It is imported only from _test.go files; nothing in the
// shipped binaries depends on it.
package agenttest

import (
	"os"
	"path/filepath"
	"testing"
)

// SocketDir returns a directory path short enough that "<dir>/<name>.sock"
// fits inside AF_UNIX's sun_path limit (104 bytes on darwin, 108 on linux) for
// any name within config.MaxAgentNameLen.
//
// Every Registry a test builds must be rooted here. t.TempDir() on darwin
// returns paths under /var/folders/... that eat most of the budget before the
// agent name is appended, so a registry rooted there cannot bind an attach
// socket for a full-length name. That used to pass unnoticed — the failed bind
// was only a log line — and it meant such spawn tests silently never exercised
// attach. Since mg-ef80 a bind that can never succeed fails the spawn.
//
// Note what mg-ef80 does and does not catch: Spawn fails on the bind of the
// *actual* name, not on whether the dir has room for the longest legal one. A
// t.TempDir()-rooted registry therefore keeps passing for as long as its test
// function and agent names happen to stay short, and breaks the day either
// grows. Rooting here removes the coincidence (mg-7318).
//
// Production never has this problem: pogod takes its socket dir from
// config.AgentSocketDir, which falls back to a short hashed path under
// os.TempDir when POGO_HOME is too deep.
func SocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pogo-test-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}
