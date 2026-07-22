package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSessionTranscriptGlob_MatchesRealProjectDirs pins the slug encoding
// against paths this machine actually produced. The encoding is Claude Code's,
// not pogo's, so the only honest test is agreement with observed reality: these
// pairs were read off ~/.claude/projects on 2026-07-23.
func TestSessionTranscriptGlob_MatchesRealProjectDirs(t *testing.T) {
	cases := []struct{ workdir, wantDir string }{
		{"/Users/daniel/.pogo/agents/pm-pogo", "-Users-daniel--pogo-agents-pm-pogo"},
		{"/Users/daniel/.pogo/agents/mayor", "-Users-daniel--pogo-agents-mayor"},
		{"/Users/daniel/.pogo/polecats/8cdb", "-Users-daniel--pogo-polecats-8cdb"},
		{"/Users/daniel/dev/pogo", "-Users-daniel-dev-pogo"},
		// Non-alphanumerics all collapse to '-' individually, with no run
		// squashing: two adjacent separators stay two dashes.
		{"/a/b_c", "-a-b-c"},
		{"/Mixed/Case42", "-Mixed-Case42"},
	}
	for _, tc := range cases {
		want := filepath.Join(".claude", "projects", tc.wantDir, "*.jsonl")
		if got := SessionTranscriptGlob(tc.workdir); got != want {
			t.Errorf("SessionTranscriptGlob(%q) = %q, want %q", tc.workdir, got, want)
		}
	}
}

// TestSessionTranscriptGlob_EmptyWorkdir: no workdir, no claim. Returning a
// glob here would match every project directory on the machine and attribute
// some other agent's transcript to this one.
func TestSessionTranscriptGlob_EmptyWorkdir(t *testing.T) {
	if got := SessionTranscriptGlob(""); got != "" {
		t.Fatalf("SessionTranscriptGlob(\"\") = %q, want \"\"", got)
	}
}

// TestSessionTranscriptGlob_ResolvesOnThisMachine is the end-to-end check that
// the declared glob actually finds files where the harness puts them. It skips
// everywhere the transcripts are absent — which is the same degradation the
// detector performs, so a skip here is not a silent pass anywhere else.
func TestSessionTranscriptGlob_ResolvesOnThisMachine(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	workdir := filepath.Join(home, ".pogo", "agents", "pm-pogo")
	matches, err := filepath.Glob(filepath.Join(home, SessionTranscriptGlob(workdir)))
	if err != nil {
		t.Fatalf("glob error: %v", err)
	}
	if len(matches) == 0 {
		t.Skipf("no transcripts for %s on this machine", workdir)
	}
	t.Logf("declared glob resolved to %d transcript file(s) for pm-pogo", len(matches))
}
