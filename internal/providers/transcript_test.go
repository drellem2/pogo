package providers

import (
	"path/filepath"
	"strings"
	"testing"
)

// Enforcement tests for the session-transcript declaration (mg-8cdb), mirroring
// the memory-root tests above: a provider must state its intent by construction
// rather than by omission, and the globs must stay containable under home.

// TestSessionTranscriptGlobsCarriesClaudeRoot: Claude's transcript root is the
// only one declared today and the only one the synthetic-failure-turn detector
// can read. If a refactor drops it, the detector silently reports
// StateUnavailable for the entire fleet — which degrades correctly, and
// therefore looks exactly like everything working.
func TestSessionTranscriptGlobsCarriesClaudeRoot(t *testing.T) {
	globs := SessionTranscriptGlobs("/Users/daniel/.pogo/agents/pm-pogo")
	if len(globs) == 0 {
		t.Fatal("SessionTranscriptGlobs() is empty — no harness declares a transcript root, so the detector is dark for every agent")
	}
	want := filepath.Join(".claude", "projects", "-Users-daniel--pogo-agents-pm-pogo", "*.jsonl")
	for _, g := range globs {
		if g == want {
			return
		}
	}
	t.Fatalf("Claude's transcript root %q is no longer declared by any provider; got %v", want, globs)
}

// TestSessionTranscriptGlobsAreHomeRelative pins the contract synthfail.Locate
// relies on. An absolute glob would escape the caller's home and, in tests,
// escape t.TempDir() into the real user's transcripts.
func TestSessionTranscriptGlobsAreHomeRelative(t *testing.T) {
	for _, g := range SessionTranscriptGlobs("/Users/daniel/.pogo/agents/mayor") {
		if filepath.IsAbs(g) {
			t.Errorf("glob %q is absolute; SessionTranscriptGlob must return a home-relative path", g)
		}
		if strings.HasPrefix(g, "~") {
			t.Errorf("glob %q starts with ~; expansion is the caller's job, so this would never match", g)
		}
		if strings.Contains(g, "..") {
			t.Errorf("glob %q contains ..; a transcript glob must not be able to climb out of home", g)
		}
	}
}

// TestSessionTranscriptGlobsEmptyWorkdir: an agent whose working directory is
// unknown yields no globs, which synthfail turns into StateUnavailable —
// degrade, never guess at another agent's transcript.
func TestSessionTranscriptGlobsEmptyWorkdir(t *testing.T) {
	if got := SessionTranscriptGlobs(""); len(got) != 0 {
		t.Fatalf("SessionTranscriptGlobs(\"\") = %v, want none: with no workdir there is no transcript to name", got)
	}
}

// TestEveryProviderDeclaresTranscriptIntent forces a decision when a provider is
// added. The default that must never happen silently is the WRONG one in both
// directions: an undeclared harness loses the detector, and a wrongly-declared
// one whose records never match would report a confident StateQuiet — a false
// all-clear, which is precisely the failure this detector exists to catch.
func TestEveryProviderDeclaresTranscriptIntent(t *testing.T) {
	// id -> whether it is expected to declare a session-transcript glob.
	want := map[string]bool{
		"claude": true,  // ~/.claude/projects/<slug-of-cwd>/*.jsonl
		"codex":  false, // rollouts are keyed by start time, not workdir (measured 2026-07-23)
		"pi":     false, // per-workdir transcripts exist, record shape uncharacterised (2026-07-23)
		"cursor": false, // chats keyed by opaque hash, not workdir (measured 2026-07-23)
	}
	all := All()
	if len(all) != len(want) {
		t.Fatalf("All() has %d providers but this test enumerates %d — a new provider must declare whether its session transcript is readable per-agent", len(all), len(want))
	}
	for _, p := range all {
		expect, known := want[p.ID]
		if !known {
			t.Errorf("provider %q is not enumerated here; declare whether it exposes a per-agent session transcript", p.ID)
			continue
		}
		if got := p.SessionTranscriptGlob != nil; got != expect {
			t.Errorf("provider %q declares SessionTranscriptGlob = %v, want %v", p.ID, got, expect)
		}
	}
}
