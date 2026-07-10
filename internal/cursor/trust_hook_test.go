package cursor

import "testing"

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
