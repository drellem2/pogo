package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestE2ESmoke runs scripts/test-e2e.sh — the full pogo init → pogod → mayor
// → polecat → refinery merge/fail / crew crash + restart loop. The script is
// hermetic (sandboxed HOME, custom POGO_PORT, fake-agent.sh stand-in for
// claude) so it can run unattended, but it builds binaries and spawns pogod,
// which makes it slow (~30–60s) and expensive in unit-test terms.
//
// The test is gated behind POGO_RUN_E2E=1 so the standard `go test ./...`
// pass stays fast. Run it manually with:
//
//	POGO_RUN_E2E=1 go test ./internal/agent -run TestE2ESmoke -v -timeout 5m
//
// or just invoke the script directly:
//
//	scripts/test-e2e.sh
func TestE2ESmoke(t *testing.T) {
	if os.Getenv("POGO_RUN_E2E") != "1" {
		t.Skip("set POGO_RUN_E2E=1 to run the end-to-end smoke test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("e2e smoke test is bash-based; not supported on windows")
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	script := filepath.Join(repoRoot, "scripts", "test-e2e.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("smoke script missing: %v", err)
	}

	cmd := exec.Command("bash", script)
	cmd.Stdout = testWriter{t}
	cmd.Stderr = testWriter{t}
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		t.Fatalf("scripts/test-e2e.sh failed: %v", err)
	}
}

// findRepoRoot walks up from the current package directory until it finds
// the file that uniquely identifies this repo's root (go.mod next to a
// scripts/ directory).
func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "scripts", "test-e2e.sh")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", p)
	return len(p), nil
}
