package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// shortSocketDir returns a directory path short enough that "<dir>/<name>.sock"
// fits inside AF_UNIX's sun_path limit (104 bytes on darwin, 108 on linux) for
// any name within config.MaxAgentNameLen.
//
// Every Registry a test builds must be rooted here. t.TempDir() on darwin
// returns paths under /var/folders/... that routinely exceed sun_path on their
// own, so a registry rooted there cannot bind an attach socket for *any* agent
// name. That used to pass unnoticed — the failed bind was only a log line — and
// it meant most of this package's spawn tests silently never exercised attach.
// Since mg-ef80 a bind that can never succeed fails the spawn, so a test on a
// long temp path now fails loudly, which is the point.
//
// Production never has this problem: pogod takes its socket dir from
// config.AgentSocketDir, which falls back to a short hashed path under
// os.TempDir when POGO_HOME is too deep.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pogo-test-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}
