package codex

import (
	"strings"
	"testing"
)

// realDialog is the directory-trust screen exactly as Codex 0.132.0 puts it on
// the PTY, after ANSI stripping: the body's inter-word spaces are gone because
// Codex draws it glyph-by-glyph with cursor positioning. Captured from a live
// PTY spawn at pogo's 200x50 default (mg-86e7).
const realDialog = "> You are in /private/tmp/worktree" +
	"Doyoutrustthecontentsofthisdirectory?" +
	"Workingwithuntrustedcontentscomeswithhigherriskofpromptinjection." +
	"Trustingthedirectoryallowsproject-localconfig,hooks,andexecpoliciestoload." +
	"› 1. Yes, continue2.No,quitPress enter to continue"

// realComposer is the status box Codex 0.132.0 renders once the composer is up,
// captured from the same live spawns. Unlike the dialog body this one keeps its
// real spaces — the two take different render paths, which is exactly why the
// sentinel set carries both a spaced and a spaceless form of each marker.
const realComposer = "" +
	"╭────────────────────────────────────────────╮\n" +
	"│ >_ OpenAI Codex (v0.132.0)                 │\n" +
	"│                                            │\n" +
	"│ model:       gpt-5.5   /model to change    │\n" +
	"│ directory:   /private/tmp/worktree         │\n" +
	"│ permissions: YOLO mode                     │\n" +
	"╰────────────────────────────────────────────╯\n"

func TestMatchesTrustDialog(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name: "trust dialog body with normal spacing",
			input: "Working with untrusted contents comes with higher risk of prompt " +
				"injection. Trusting the directory allows project-local config.",
			want: true,
		},
		{
			// Codex draws the dialog body glyph-by-glyph; once ANSI is
			// stripped the spaces are gone. This is the real on-PTY form.
			name:  "trust dialog body rendered glyph-by-glyph (no spaces)",
			input: "Doyoutrustthecontentsofthisdirectory?Workingwithuntrustedcontentscomeswith",
			want:  true,
		},
		{
			name:  "trusting-the-directory glyph-by-glyph",
			input: "Trustingthedirectoryallowsproject-localconfig,hooks,andexecpolicies",
			want:  true,
		},
		{
			name:  "trust dialog with ANSI escapes",
			input: "\x1b[1mTrusting\x1b[0mthe\x1b[32mdirectory\x1b[0m allows ...",
			want:  true,
		},
		{
			name:  "no match - normal composer output",
			input: "OpenAI Codex (v0.132.0)  model: gpt-5.5  permissions: YOLO mode",
			want:  false,
		},
		{
			name:  "no match - the word trust alone",
			input: "You can trust the explorer results without re-verifying them.",
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

// TestComposerReady pins the hook's false-positive guard. The status box proves
// the dialog is not on screen; it never renders while the dialog is up.
func TestComposerReady(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"real status box", realComposer, true},
		{"model hint alone", "model:       gpt-5.5   /model to change", true},
		{"title line alone", "│ >_ OpenAI Codex (v0.132.0)   │", true},
		{"status box with ANSI", "\x1b[2mmodel: gpt-5.5 \x1b[0m/model to change\x1b[0m", true},
		{
			// The space-collapse form: if Codex ever draws the box the way it
			// draws the dialog body, the guard must still hold. This is the
			// gh#76 / mg-d06a trap.
			name:  "status box drawn glyph-by-glyph (no spaces)",
			input: "│>_OpenAICodex(v0.132.0)││model:gpt-5.5/modeltochange│",
			want:  true,
		},
		{"trust dialog up — no status box", realDialog, false},
		{
			// A model rename must not break the marker: the sentinel is cut to
			// carry no model name.
			name:  "different model name",
			input: "model:       gpt-6-codex-preview   /model to change",
			want:  true,
		},
		{
			// Nor must a version bump: the title sentinel stops before the
			// digits.
			name:  "different Codex version",
			input: "│ >_ OpenAI Codex (v0.146.0)  │",
			want:  true,
		},
		{
			// The composer PLACEHOLDER rotates between at least five strings
			// and is deliberately NOT a sentinel. If one ever became one this
			// case would start passing and the marker would be a coin flip.
			name:  "rotating placeholder is not a marker",
			input: "› Explain this codebase",
			want:  false,
		},
		{"rotating placeholder, another draw", "› Run /review on my current changes", false},
		{"update-available banner only", "│ ✨ Update available! 0.132.0 -> 0.146.0 │", false},
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

// TestReadySentinelsAreCollapseSafe guards the trap this ticket was warned
// about (gh#76 / mg-d06a): agent/nudge.go matches PromptReadySentinel RAW
// against StripANSI output, so a marker that only exists in spaced form silently
// stops matching if the TUI starts positioning the box with per-word cursor
// moves — and every spawn then pays the full InitialNudgeTimeout as dead time.
//
// The rule this pins: every spaced sentinel must have a spaceless counterpart in
// the set, so one of the two always matches whichever way Codex draws the box.
func TestReadySentinelsAreCollapseSafe(t *testing.T) {
	set := map[string]bool{}
	for _, s := range readySentinels() {
		set[s] = true
	}
	for _, s := range readySentinels() {
		collapsed := strings.Join(strings.Fields(s), "")
		if collapsed == s {
			continue // already spaceless
		}
		if !set[collapsed] {
			t.Errorf("sentinel %q has spaces but no spaceless counterpart %q in "+
				"the set; if Codex draws the status box glyph-by-glyph (as it "+
				"already draws the trust dialog) this marker stops matching raw "+
				"PTY text and every spawn pays the full cold-start budget",
				s, collapsed)
		}
	}
}

// TestEchoedTaskLooksLikeTheTrustDialog is the reason composerReady exists, and
// the reason this ticket was split off drellem2/pogo#91 instead of folded in.
//
// trustDialogMarker matches on PTY text, and the harness echoes the nudged task
// into the TUI. A work item that merely *quotes* the dialog matches the marker —
// mg-86e7's own body does. On an already-trusted directory (respawn: Codex
// persists trust in ~/.codex/config.toml) there is no dialog, so an unguarded
// hook would press Enter into the live composer.
func TestEchoedTaskLooksLikeTheTrustDialog(t *testing.T) {
	// A polecat task body describing the very dialog this hook dismisses.
	echoedTask := "Investigate the directory-trust dialog: it warns that " +
		"Working with untrusted contents comes with higher risk of prompt " +
		"injection, and the bypass flag does not suppress it."

	if !matchesTrustDialog([]byte(echoedTask)) {
		t.Fatal("precondition changed: the echoed task no longer matches " +
			"trustDialogMarker — if the marker got stricter, this guard may be " +
			"redundant, but verify before deleting composerReady")
	}

	// On an already-trusted spawn the status box is up and the task is echoed
	// beneath it. composerReady must gate the hook off before the marker fires.
	screen := realComposer + "\n› " + echoedTask + "\n"
	if !composerReady([]byte(screen)) {
		t.Error("composerReady must detect the rendered status box and stop the " +
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
