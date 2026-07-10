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

// TestRenderRecoveryPlistBindsRecoveryDir pins the invariant that made
// com.pogo.recovery inert on a real host (mg-6e82): the plist's WatchPaths is
// derived from POGO_HOME, but pogo-recovery.sh falls back to $HOME/.pogo/recovery
// when POGO_RECOVERY_DIR is unset. If the plist does not export POGO_RECOVERY_DIR,
// a POGO_HOME pointing anywhere else leaves launchd watching one directory while
// the script drains another — the job spawns, logs "queue empty", and every
// recovery request is silently dropped.
func TestRenderRecoveryPlistBindsRecoveryDir(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("POGO_HOME", custom)
	os.Unsetenv("POGO_RECOVERY_DIR")

	rendered, data, err := renderRecoveryPlist()
	if err != nil {
		t.Fatalf("renderRecoveryPlist: %v", err)
	}

	wantDir := filepath.Join(custom, "recovery")
	if data.RecoveryDir != wantDir {
		t.Errorf("RecoveryDir = %q; want %q", data.RecoveryDir, wantDir)
	}
	// The watched dir must be exactly the queue under the dir the script drains.
	if data.QueueDir != filepath.Join(data.RecoveryDir, "queue") {
		t.Errorf("QueueDir %q is not queue/ under RecoveryDir %q", data.QueueDir, data.RecoveryDir)
	}
	if !strings.Contains(rendered, "<key>POGO_RECOVERY_DIR</key>") {
		t.Error("rendered plist does not export POGO_RECOVERY_DIR; the script would fall back to $HOME/.pogo/recovery")
	}
	if !strings.Contains(rendered, "<string>"+wantDir+"</string>") {
		t.Errorf("rendered plist does not bind POGO_RECOVERY_DIR to %q:\n%s", wantDir, rendered)
	}
}

// TestRenderRecoveryPlistHonorsRecoveryDirEnv verifies an operator-supplied
// POGO_RECOVERY_DIR is baked into the plist, so the installed job watches and
// drains the override rather than the POGO_HOME-derived default.
func TestRenderRecoveryPlistHonorsRecoveryDirEnv(t *testing.T) {
	override := t.TempDir()
	t.Setenv("POGO_RECOVERY_DIR", override)

	rendered, data, err := renderRecoveryPlist()
	if err != nil {
		t.Fatalf("renderRecoveryPlist: %v", err)
	}
	if data.RecoveryDir != override {
		t.Errorf("RecoveryDir = %q; want %q", data.RecoveryDir, override)
	}
	if data.QueueDir != filepath.Join(override, "queue") {
		t.Errorf("QueueDir = %q; want %q", data.QueueDir, filepath.Join(override, "queue"))
	}
	if !strings.Contains(rendered, "<string>"+override+"</string>") {
		t.Errorf("rendered plist does not bind POGO_RECOVERY_DIR to %q", override)
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
