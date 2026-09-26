package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubClaudeEnv puts a `claude` on PATH whose `auth status` prints out and
// exits with code.
func stubClaudeEnv(t *testing.T, out, code string) []string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = auth ]; then\n  echo '" + out + "'\n  exit " + code + "\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_USE_BEDROCK=", "CLAUDE_CODE_USE_VERTEX=",
	}
}

// drellem2/pogo#173: `pogo doctor --check` states the permission posture pogo
// pre-accepts, and reads the harness login — failing only on a positive
// not-logged-in, warning on anything it cannot read.
func TestDoctorCheck_HarnessOnboardingRows(t *testing.T) {
	for _, tc := range []struct {
		name, out, code, wantStatus string
	}{
		{"fresh profile fails", `{"loggedIn": false, "authMethod": "none"}`, "1", "fail"},
		{"logged in passes", `{"loggedIn": true, "authMethod": "claude.ai"}`, "0", "pass"},
		{"garbage warns, never fails", "zzz", "2", "warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := doctorChecks(t, stubClaudeEnv(t, tc.out, tc.code))
			login, ok := checks["claude login"]
			if !ok {
				t.Fatalf("no \"claude login\" row; rows: %v", checks)
			}
			if !strings.HasPrefix(login, tc.wantStatus+"\t") {
				t.Errorf("claude login = %q, want status %s", login, tc.wantStatus)
			}
			if tc.wantStatus == "fail" && !strings.Contains(login, "log in") {
				t.Errorf("a not-logged-in row must say how to fix it: %q", login)
			}
			perm := checks["claude permission mode"]
			if !strings.HasPrefix(perm, "pass\t") || !strings.Contains(perm, "bypass-permissions") {
				t.Errorf("claude permission mode = %q, want a pass row announcing bypass-permissions mode", perm)
			}
		})
	}
}
