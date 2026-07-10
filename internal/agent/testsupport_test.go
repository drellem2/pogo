package agent

import (
	"testing"

	"github.com/drellem2/pogo/internal/agenttest"
)

// shortSocketDir returns a directory path short enough that "<dir>/<name>.sock"
// fits inside AF_UNIX's sun_path limit for any name within MaxAgentNameLen.
// Every Registry a test in this package builds must be rooted here; see
// agenttest.SocketDir for why t.TempDir() is not usable.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	return agenttest.SocketDir(t)
}
