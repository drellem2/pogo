package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnqueueRecoveryRequest verifies that EnqueueRecoveryRequest writes a
// well-formed .req file using the temp+rename pattern (no *.tmp-* leaks
// into the queue) and embeds requester/reason/timestamp in the body.
func TestEnqueueRecoveryRequest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POGO_RECOVERY_DIR", dir)

	path, err := EnqueueRecoveryRequest("polecat-test", "unit-test")
	if err != nil {
		t.Fatalf("EnqueueRecoveryRequest: %v", err)
	}

	if !strings.HasSuffix(path, ".req") {
		t.Fatalf("expected .req suffix, got %s", path)
	}
	if filepath.Dir(path) != filepath.Join(dir, "queue") {
		t.Fatalf("expected file in %s/queue, got %s", dir, path)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	want := []string{"requester=polecat-test", "reason=unit-test", "ts="}
	for _, w := range want {
		if !strings.Contains(string(body), w) {
			t.Errorf("body missing %q; got: %s", w, string(body))
		}
	}

	// No .tmp-* files should remain in the queue dir — the rename must be
	// atomic so launchd's WatchPaths trigger never reads a partial file.
	entries, err := os.ReadDir(filepath.Join(dir, "queue"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leaked tmp file: %s", e.Name())
		}
	}
}

func TestSanitizeRequester(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"mayor", "mayor"},
		{"polecat-mg-1234", "polecat-mg-1234"},
		{"agent_42", "agent_42"},
		{"weird name with spaces", "weird_name_with_spaces"},
		{"path/with/slashes", "path_with_slashes"},
		{"", "unknown"},
	}
	for _, c := range cases {
		got := sanitizeRequester(c.in)
		if got != c.want {
			t.Errorf("sanitizeRequester(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestRecoveryDirHonorsEnv verifies that POGO_RECOVERY_DIR overrides the
// default ~/.pogo/recovery path. Important so the e2e test (and any future
// sandboxed harness) can redirect the queue without polluting the user's
// real ~/.pogo.
func TestRecoveryDirHonorsEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POGO_RECOVERY_DIR", dir)

	if got := recoveryDir(); got != dir {
		t.Errorf("recoveryDir() = %q; want %q", got, dir)
	}
	if got := recoveryQueueDir(); got != filepath.Join(dir, "queue") {
		t.Errorf("recoveryQueueDir() = %q; want %q", got, filepath.Join(dir, "queue"))
	}
}
