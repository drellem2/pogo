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
		// Collapsing both sides means each sentinel covers both renderings, not
		// just the era it was captured in: the spaced primary now matches a
		// column-move footer, and the spaceless alternates now match a spaced one.
		{"primary sentinel drawn with per-word column moves", "?\x1b[4Gfor\x1b[8Gshortcuts", true},
		{"mode-cycle marker drawn spaced", "bypass permissions on (shift+tab to cycle)", true},
		{"placeholder fallback drawn spaced", "❯ Try \"fix the failing test\"", true},
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

// TestComposerReadySentinelsSurviveSpaceCollapse pins the collapse on both
// sides of the match.
//
// The set deliberately mixes spellings — the primary sentinel is spaced because
// that is how older Claude Code drew it, the alternates are spaceless because
// v2.1.x positions the footer with per-word cursor-column moves. Matched raw,
// each spelling only ever hit its own era's rendering, and a silently
// non-matching sentinel costs the full InitialNudgeTimeout on every spawn
// (gh#76 / mg-d06a). Every sentinel must match both ways.
func TestComposerReadySentinelsSurviveSpaceCollapse(t *testing.T) {
	for _, s := range readySentinels() {
		if s == "" {
			t.Error("empty sentinel in the ready set: it would match everything")
			continue
		}
		if !composerReady([]byte("prefix " + s + " suffix")) {
			t.Errorf("composerReady rejects its own sentinel %q as spelled", s)
		}
		if !composerReady([]byte("prefix" + collapse(s) + "suffix")) {
			t.Errorf("composerReady rejects sentinel %q once the TUI's per-word "+
				"column moves have eaten its spaces — sentinels must be matched "+
				"against collapsed text", s)
		}
	}
	// The dialog must still fail the gate, or collapsing has swallowed the
	// distinction the hook exists to draw.
	if composerReady([]byte(dialogLine)) {
		t.Error("a composer sentinel matched the trust dialog: the gate would " +
			"stop the hook from ever dismissing it")
	}
}

// TestComposerScanReadsTheWholeRing is the pin for claude's half of mg-9270.
//
// The gate read 8KB out of a 64KB ring, and a marker a tick misses is not one it
// is offered again. Claude's Ink TUI repaints continuously, so it is the provider
// most able to push 8KB between two 250ms polls; when it does, the composer is
// hidden from every tick, the gate never closes, and the hook watches its full
// 60s budget with the echoed kickoff prompt in view. The read must be the whole
// retained buffer, sourced from the ring's own constant so the two cannot drift.
func TestComposerScanReadsTheWholeRing(t *testing.T) {
	if composerScanBytes != agent.OutputRingBytes {
		t.Errorf("composerScanBytes = %d, want the ring's own capacity %d — a "+
			"literal here goes stale the day the ring is resized",
			composerScanBytes, agent.OutputRingBytes)
	}
	if composerScanBytes <= 8192 {
		t.Errorf("composerScanBytes = %d: back at or below the 8KB read that let "+
			"a burst hide the composer from every poll", composerScanBytes)
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
