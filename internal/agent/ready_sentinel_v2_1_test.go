package agent

import (
	"os"
	"strings"
	"testing"
)

// TestDefaultSentinelsMatchClaudeV2_1 guards the stale-sentinel regression that
// made every polecat spawn burn the full InitialNudgeTimeout as dead time.
// Claude Code v2.1.x dropped the "? for shortcuts" composer hint, so pogo's lone
// primary sentinel stopped matching and WaitForReady always fell through to the
// 60s best-effort timeout. The fix adds alternates that DO appear in v2.1.x; this
// test asserts them against a real captured polecat screen (testdata) run through
// the same StripANSI the gate uses.
func TestDefaultSentinelsMatchClaudeV2_1(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude_v2_1_201_composer.raw")
	if err != nil {
		t.Skipf("no captured composer sample: %v", err)
	}
	clean := string(StripANSI(raw))

	// Precondition = the bug: the legacy sentinel is genuinely absent in v2.1.x.
	if strings.Contains(clean, DefaultNudgeProfile.PromptReadySentinel) {
		t.Fatalf("expected legacy sentinel %q to be ABSENT in the v2.1.201 sample "+
			"(the regression that motivated the alternates)", DefaultNudgeProfile.PromptReadySentinel)
	}

	// The fix: at least one configured marker (primary or alternate) matches, so
	// the gate opens on a real v2.1.201 screen instead of timing out.
	all := append([]string{DefaultNudgeProfile.PromptReadySentinel}, DefaultNudgeProfile.PromptReadyAlternates...)
	matched := ""
	for _, s := range all {
		if s != "" && strings.Contains(clean, s) {
			matched = s
			break
		}
	}
	if matched == "" {
		t.Fatalf("no ready-marker matched real v2.1.201 output; detection would fall "+
			"through to the full InitialNudgeTimeout.\nmarkers=%q\nstripped=%q", all, clean)
	}
	t.Logf("v2.1.201 readiness detected via %q", matched)
}
