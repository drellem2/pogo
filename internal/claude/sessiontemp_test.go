package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestSessionTempRoot pins Claude Code's temp-root rule: CLAUDE_CODE_TMPDIR
// with trailing separators trimmed when set, else /tmp — never $TMPDIR — with
// a per-uid claude-<uid> directory beneath it.
func TestSessionTempRoot(t *testing.T) {
	cases := []struct {
		override string
		uid      int
		want     string
	}{
		{"", 501, "/tmp/claude-501"},
		{"/var/scratch", 1000, "/var/scratch/claude-1000"},
		{"/var/scratch///", 1000, "/var/scratch/claude-1000"},
		{"", -1, "/tmp/claude-0"},
	}
	for _, tc := range cases {
		if got := sessionTempRoot(tc.override, tc.uid); got != tc.want {
			t.Errorf("sessionTempRoot(%q, %d) = %q, want %q", tc.override, tc.uid, got, tc.want)
		}
	}
}

// TestSessionTempDir_MatchesThisMachine: the pair below was read off this
// machine's temp root on 2026-09-28, where TMPDIR is a /var/folders path and
// CLAUDE_CODE_TMPDIR is unset — which is exactly the case that shows $TMPDIR is
// not the root.
func TestSessionTempDir_MatchesThisMachine(t *testing.T) {
	t.Setenv("CLAUDE_CODE_TMPDIR", "")
	t.Setenv("TMPDIR", "/var/folders/xx/T/")
	want := filepath.Join("/tmp", "claude-"+strconv.Itoa(os.Getuid()), "-Users-daniel--pogo-polecats-p8c8a1")
	if got := SessionTempDir("/Users/daniel/.pogo/polecats/p8c8a1"); got != want {
		t.Fatalf("SessionTempDir = %q, want %q", got, want)
	}
	if got := SessionTempDir(""); got != "" {
		t.Fatalf("SessionTempDir(\"\") = %q, want \"\"", got)
	}
}

// TestSessionTempDir_ObservedForThisSession_Informational is INFORMATIONAL,
// not a drift detector: it logs when the constructed dir exists for the module
// root, and skips otherwise. It cannot fail on a missing dir, because the tests
// are routinely run from a directory no Claude session was started in (the
// refinery gate's worktree, a nested package) while CLAUDECODE is still set,
// so a miss there is not evidence of drift. The pin that DOES fail is
// TestSessionTempDir_MatchesThisMachine above. A skip here is
// not a pass (review round 1 advisory, mg-d0c10).
func TestSessionTempDir_ObservedForThisSession_Informational(t *testing.T) {
	if os.Getenv("CLAUDECODE") == "" {
		t.Skip("not running under Claude Code")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Skip("no working directory")
	}
	// go test runs in the package dir; the session's cwd is the module root.
	root := filepath.Dir(filepath.Dir(wd))
	dir := SessionTempDir(root)
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no session temp dir at %s (%v)", dir, err)
	}
	t.Logf("declared session temp dir exists: %s", dir)
}
