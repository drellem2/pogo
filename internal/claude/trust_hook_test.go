package claude

import (
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

func TestTrustDialogMarker(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "plain text trust dialog",
			input: "Quick safety check: Is this a project you created or one you trust?",
			want:  true,
		},
		{
			name:  "trust dialog with ANSI escapes",
			input: "\x1b[1mQuick \x1b[0msafety check\x1b[32m: Is this a project...",
			want:  true,
		},
		{
			name:  "safety check substring",
			input: "Running safety check now",
			want:  true,
		},
		{
			name:  "no match - normal output",
			input: "Hello, I am Claude. How can I help you today?",
			want:  false,
		},
		{
			name:  "empty output",
			input: "",
			want:  false,
		},
		{
			name:  "ansi only",
			input: "\x1b[2J\x1b[H",
			want:  false,
		},
		{
			// Claude's Ink TUI positions text with per-word cursor-column
			// moves, so after ANSI stripping the spaces can vanish. The old
			// `safety.check` pattern needed exactly one character between the
			// words and missed this — gh#76 / mg-d06a, same trap.
			name:  "space-collapsed by per-word column moves",
			input: "Quick\x1b[7Gsafety\x1b[14Gcheck\x1b[20G: Is this a project you trust?",
			want:  true,
		},
		{
			name:  "wrapped across lines",
			input: "Quick safety\ncheck: Is this a project you created?",
			want:  true,
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
// ready-markers prove the dialog is not on screen; none of them render while
// the dialog is up.
func TestComposerReady(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"primary sentinel", "  ? for shortcuts", true},
		{"spaceless bypass-mode marker", "bypasspermissionson (shift+tabtocycle)", true},
		{"spaceless shortcuts marker", "?forshortcuts", true},
		{"placeholder fallback", "❯ Try\"fix the failing test\"", true},
		{"with ANSI", "\x1b[2m? for shortcuts\x1b[0m", true},
		{"trust dialog up — no composer", "Quick safety check: Is this a project you trust?", false},
		{"loading spinner", "  ✻ Welcome to Claude Code", false},
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

// TestComposerReadyReusesTheNudgeSentinels keeps the guard honest: it must read
// the nudge profile's marker set rather than keeping a private second copy that
// can drift out of step when the harness rewords its footer.
func TestComposerReadyReusesTheNudgeSentinels(t *testing.T) {
	p := agent.DefaultNudgeProfile
	for _, s := range append([]string{p.PromptReadySentinel}, p.PromptReadyAlternates...) {
		if !composerReady([]byte("prefix " + s + " suffix")) {
			t.Errorf("composerReady does not accept nudge sentinel %q — the "+
				"hook and the initial nudge must agree on what 'composer is "+
				"up' means", s)
		}
	}
}

// TestTrustDialogTimeoutIsTheInitialNudgeBudget is the regression pin for
// drellem2/macguffin#25. The bug was a fixed 8s wall-clock guess that could
// expire before a loaded host rendered the dialog. The bound must be the
// initial nudge's own cold-start budget, so there is one timeout concept rather
// than two that disagree — the spawn path waits this long for the composer, so
// the hook that unblocks the composer must not stop watching first.
func TestTrustDialogTimeoutIsTheInitialNudgeBudget(t *testing.T) {
	want := agent.DefaultNudgeProfile.InitialNudgeTimeout
	if TrustDialogTimeout != want {
		t.Errorf("TrustDialogTimeout = %v, want the initial-nudge budget %v",
			TrustDialogTimeout, want)
	}
	if TrustDialogTimeout <= 8*time.Second {
		t.Errorf("TrustDialogTimeout = %v: back at or below the fixed 8s that "+
			"let a late-rendering dialog go undismissed", TrustDialogTimeout)
	}
	if TrustDialogPollInterval >= TrustDialogTimeout {
		t.Errorf("poll interval %v must be shorter than the timeout %v",
			TrustDialogPollInterval, TrustDialogTimeout)
	}
}
