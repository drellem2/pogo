package cursor

import (
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// realDialog is the whole trust dialog exactly as Cursor 2026.07.09-a3815c0
// draws it, captured from a PTY spike at 200×50 and passed through
// agent.StripANSI (box-drawing glyphs retained, ANSI gone).
const realDialog = `
  ╭──────────────────────────────────────────────╮
  │                                              │
  │  🔒 Workspace Trust Required                 │
  │                                              │
  │  Cursor Agent can execute code and access    │
  │  files in this directory.                    │
  │                                              │
  │  Do you trust the contents of this           │
  │  directory?                                  │
  │                                              │
  │    /tmp/pogo-worktree/polecat-mg-c146        │
  │                                              │
  │  ▶ [a] Trust this workspace                  │
  │    [q] Quit                                  │
  │                                              │
  │  Use arrow keys to navigate, Enter to        │
  │  select, or press the key shown              │
  │                                              │
  ╰──────────────────────────────────────────────╯
`

func TestMatchesTrustDialog(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "the real dialog as drawn on the PTY",
			input: realDialog,
			want:  true,
		},
		{
			name:  "header phrase alone",
			input: "Workspace Trust Required",
			want:  true,
		},
		{
			name:  "menu item alone (header reworded)",
			input: "▶ [a] Trust this workspace\n    [q] Quit",
			want:  true,
		},
		{
			// The dialog is drawn inside a box that re-wraps at narrow
			// winsizes; matchesTrustDialog collapses whitespace so a phrase
			// split across lines still matches.
			name:  "phrase split across a box line-wrap",
			input: "│  Workspace Trust\n│  Required  │",
			want:  true,
		},
		{
			name:  "with ANSI escapes",
			input: "\x1b[1mWorkspace\x1b[0m \x1b[32mTrust\x1b[0m Required",
			want:  true,
		},
		{
			name:  "case insensitive",
			input: "workspace trust required",
			want:  true,
		},
		{
			name:  "no match - normal composer output",
			input: "  Cursor Agent\n  v2026.07.09-a3815c0\n  → Plan, search, build anything",
			want:  false,
		},
		{
			name:  "no match - the word trust alone",
			input: "You can trust the explorer results without re-verifying them.",
			want:  false,
		},
		{
			name:  "no match - post-turn composer",
			input: "→ Add a follow-up                    ctrl+c to stop",
			want:  false,
		},
		{
			name:  "empty output",
			input: "",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesTrustDialog([]byte(tt.input))
			if got != tt.want {
				t.Errorf("matchesTrustDialog(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestComposerReady pins the hook's false-positive guard. The composer
// placeholder proves the dialog is not on screen; it never renders while the
// dialog is up.
func TestComposerReady(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"composer placeholder rendered", "  → Plan, search, build anything\n  Auto", true},
		{"placeholder with ANSI", "\x1b[2m→ Plan, search, build anything\x1b[0m", true},
		{"trust dialog up — no placeholder", realDialog, false},
		{"loading banner", "  Cursor Agent\n  v2026.07.09-a3815c0", false},
		// Cursor REPLACES the pre-turn placeholder once a turn starts, so the
		// post-turn one is the only composer marker on screen for the rest of
		// the run. It has to count as ready — it proves the composer at least as
		// well, since a running turn means nothing modal is in the way (mg-9270).
		{"post-turn composer (placeholder replaced)", "→ Add a follow-up", true},
		{"post-turn composer with a footer beside it", "→ Add a follow-up            ctrl+c to stop", true},
		// Space-collapse: a TUI positions footer text with per-word column
		// moves, so the spaces can be gone by the time StripANSI is done —
		// gh#76 / mg-d06a. Both sentinels are matched collapsed.
		{"pre-turn placeholder drawn spaceless", "→Plan,search,buildanything", true},
		{"post-turn placeholder drawn spaceless", "→Addafollow-up", true},
		{"post-turn placeholder with per-word column moves", "→ \x1b[4GAdd\x1b[8Ga\x1b[10Gfollow-up", true},
		{"placeholder wrapped across a line break", "→ Plan, search,\n  build anything", true},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := composerReady([]byte(tt.input)); got != tt.want {
				t.Errorf("composerReady(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestEchoedTaskLooksLikeTheTrustDialog is the reason composerReady exists.
//
// trustDialogMarker matches on PTY text, and Cursor echoes the argv-delivered
// task into the TUI. A work item that merely *quotes* the dialog matches the
// marker — mg-c146's own body does. On an already-trusted worktree (respawn:
// Cursor persists trust per workspace) there is no dialog, so an unguarded hook
// would type a stray "a" into the live composer.
//
// The assertions below encode exactly that: the echoed task DOES match the
// dialog marker (so the marker alone is not sufficient), and the composer
// placeholder that accompanies it DOES gate the hook off.
func TestEchoedTaskLooksLikeTheTrustDialog(t *testing.T) {
	// A polecat task body describing the very dialog this hook dismisses.
	echoedTask := "Investigate the workspace-trust dialog: it offers " +
		"[a] Trust this workspace and [q] Quit, and --force does not suppress it."

	if !matchesTrustDialog([]byte(echoedTask)) {
		t.Fatal("precondition changed: the echoed task no longer matches " +
			"trustDialogMarker — if the marker got stricter, this guard may be " +
			"redundant, but verify before deleting composerReady")
	}

	// On an already-trusted spawn the composer is up and the task is echoed
	// beneath it. composerReady must gate the hook off before the marker fires.
	screen := "  → Plan, search, build anything\n\n  " + echoedTask + "\n"
	if !composerReady([]byte(screen)) {
		t.Error("composerReady must detect the rendered composer and stop the " +
			"hook from typing into it")
	}

	// And with the real dialog up, the hook must still fire.
	if composerReady([]byte(realDialog)) {
		t.Error("composerReady must be false while the trust dialog is up")
	}
	if !matchesTrustDialog([]byte(realDialog)) {
		t.Error("the real dialog must still match the marker")
	}
}

// TestComposerReadySpansTheComposerToTurnTransition is the string-level pin for
// mg-9270's second half.
//
// The gate used to rest on ONE placeholder, and Cursor replaces that placeholder
// the moment a turn starts. So the marker it was watching for was a *window*, not
// a screen feature — and a spawn whose transition fell between two 250ms polls
// left the gate open for the rest of the budget, which is exactly the window in
// which an echoed task quoting the dialog can be mistaken for the dialog.
//
// Both sides of the transition must now satisfy the gate. The count assertion is
// the anti-regression: dropping back to a lone sentinel is the defect returning.
func TestComposerReadySpansTheComposerToTurnTransition(t *testing.T) {
	if len(composerReadySentinels) < 2 {
		t.Fatalf("composerReadySentinels has %d entry: a gate resting on one "+
			"exact string reopens mg-9270 the moment Cursor swaps or rewords it",
			len(composerReadySentinels))
	}

	// Pre-turn must still work: it is the sentinel the nudge profile publishes.
	if !composerReady([]byte("→ " + promptReadySentinel)) {
		t.Error("the pre-turn placeholder must still satisfy the gate — it is " +
			"Provider.Nudge.PromptReadySentinel")
	}

	// Post-turn must work too, because after the first turn it is all there is.
	if !composerReady([]byte("→ Add a follow-up")) {
		t.Error("the post-turn placeholder must satisfy the gate: Cursor replaces " +
			"the pre-turn one when a turn starts, so a gate that only knows the " +
			"pre-turn spelling has nothing left to match")
	}

	// And every sentinel must survive the space-collapse trap, whichever way the
	// TUI draws it. A spaced sentinel matched raw against spaceless output is
	// how gh#76 / mg-d06a broke this exact subsystem.
	for _, s := range composerReadySentinels {
		if s == "" {
			t.Error("empty sentinel in composerReadySentinels: it would match everything")
			continue
		}
		if !composerReady([]byte("prefix " + s + " suffix")) {
			t.Errorf("composerReady rejects its own sentinel %q as drawn spaced", s)
		}
		if !composerReady([]byte("prefix" + collapse(s) + "suffix")) {
			t.Errorf("composerReady rejects sentinel %q once the TUI's per-word "+
				"column moves have eaten its spaces — sentinels must be matched "+
				"against collapsed text (gh#76 / mg-d06a)", s)
		}
	}

	// The dialog must still fail the gate, or the widened set has swallowed the
	// distinction the hook exists to draw.
	if composerReady([]byte(realDialog)) {
		t.Error("a composer sentinel matched the trust dialog: the gate would " +
			"stop the hook from ever dismissing it")
	}
}

// TestComposerScanReadsTheWholeRing is the pin for mg-9270's first half.
//
// The gate read 8KB out of a 64KB ring. Because the marker it looks for is only
// on screen for a window, an 8KB-plus burst across the composer->turn transition
// could hide it from EVERY tick — no tick sees the placeholder, the gate never
// closes, and the hook polls out its whole budget. The read must be the full
// retained buffer, sourced from the ring's own constant so the two cannot drift.
func TestComposerScanReadsTheWholeRing(t *testing.T) {
	if composerScanBytes != agent.OutputRingBytes {
		t.Errorf("composerScanBytes = %d, want the ring's own capacity %d — a "+
			"literal here goes stale the day the ring is resized",
			composerScanBytes, agent.OutputRingBytes)
	}
	if composerScanBytes <= 8192 {
		t.Errorf("composerScanBytes = %d: back at or below the 8KB read that let "+
			"a burst hide the composer placeholder from every poll", composerScanBytes)
	}
}

// TestTrustDialogAcceptIsExplicitAccelerator guards a deliberate divergence
// from claude/codex, whose hooks send "\r". Cursor's dialog is a two-item menu
// where Enter selects the highlighted row; if Cursor ever reorders it, "\r"
// would select "[q] Quit" and kill the polecat. "a" is bound to Trust
// explicitly, so a UI change degrades to a visible stall instead.
func TestTrustDialogAcceptIsExplicitAccelerator(t *testing.T) {
	if trustDialogAccept != "a" {
		t.Errorf("trustDialogAccept = %q, want \"a\" — the explicit Trust "+
			"accelerator, not a highlight-dependent Enter", trustDialogAccept)
	}
	if trustDialogAccept == "\r" {
		t.Error("trustDialogAccept must not be Enter: it selects whatever menu " +
			"row is highlighted, which could become [q] Quit")
	}
}

// TestTrustDialogTimeoutsAreSane keeps the poll well inside the timeout, and
// the timeout generous against the ~0.7s dialog render measured on this CLI.
func TestTrustDialogTimeoutsAreSane(t *testing.T) {
	if TrustDialogPollInterval <= 0 || TrustDialogTimeout <= 0 {
		t.Fatal("trust dialog poll interval and timeout must both be positive")
	}
	if TrustDialogPollInterval >= TrustDialogTimeout {
		t.Errorf("poll interval %v must be shorter than the timeout %v",
			TrustDialogPollInterval, TrustDialogTimeout)
	}
}
