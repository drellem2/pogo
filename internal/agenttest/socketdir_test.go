package agenttest

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// TestSocketDirFitsLongestAgentName is the promise every test Registry rooted at
// SocketDir depends on: a socket named for the longest agent name
// config.MaxAgentNameLen admits must actually bind under it. Asserted by binding
// against the running kernel rather than against a recomputed sun_path constant,
// so it holds on darwin (104) and linux (108) alike.
//
// Without it, SocketDir could drift long — a wordier prefix, a nested
// subdirectory — and every caller would keep passing only for as long as its
// agent names stayed short. That coincidence is what hid the t.TempDir()-rooted
// registries in the pi and codex e2e tests: on this darwin box their 3-byte
// "e2e" name landed 13 bytes under the limit, while a full-length name overran
// it by 8 (mg-7318).
func TestSocketDirFitsLongestAgentName(t *testing.T) {
	dir := SocketDir(t)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}

	longest := strings.Repeat("a", config.MaxAgentNameLen)
	sock := filepath.Join(dir, longest+".sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("SocketDir() = %q: the longest legal agent name (%d bytes) must bind "+
			"beneath it, but %q (%d bytes) failed: %v",
			dir, config.MaxAgentNameLen, sock, len(sock), err)
	}
	l.Close()
}
