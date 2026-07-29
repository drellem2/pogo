package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The prompt-ready timeout log line is the operator's only view of a gate that
// never opened, and until mg-01d3 it named the primary sentinel alone. That
// under-determined the cause across two repairs that are opposites:
//
//   - the binary predates PromptReadyAlternates — redeploy; the matcher is fine
//   - the alternates are configured but none matched — fix the matcher; a
//     redeploy changes nothing
//
// 299 occurrences of the message on one host could not be assigned to either.
// The tests below pin the distinction: the message names the alternates BY
// VALUE, and an empty list is visibly empty rather than absent, because
// "no alternates configured" is exactly the stale-binary signature.

// timeoutLogForAlternates drives one full wait-ready timeout against a harness
// that never emits any ready marker, and returns what the standard logger saw
// plus what reached the PTY. It exercises the real NudgeWithMode path — not a
// reconstruction of its format string — so a change to the message is caught
// here and nowhere else.
func timeoutLogForAlternates(t *testing.T, name, sentinel string, alternates []string) (logged, output string) {
	t.Helper()

	readLog := captureLog(t)

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })

	// Emits a banner that matches neither the sentinel nor any alternate, then
	// idles, so the gate can only end at the deadline.
	a, err := reg.Spawn(SpawnRequest{
		Name:    name,
		Type:    TypePolecat,
		Command: []string{"bash", "-c", "echo unrelated-banner; cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.nudge.PromptReadySentinel = sentinel
	a.nudge.PromptReadyAlternates = alternates

	// No behaviour change: the deadline path still delivers best-effort and
	// still returns nil. This is the "gate times out exactly as before" half of
	// the acceptance — a diagnostic fix must not touch the outcome.
	if err := a.NudgeWithMode("late msg", NudgeWaitReady, 2*time.Second); err != nil {
		t.Fatalf("best-effort delivery after timeout should return nil; got %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	return readLog(), string(a.RecentOutput(4096))
}

// TestReadyTimeoutLogNamesAlternates is the fix: a populated alternates list
// appears in the timeout message, so an operator reading pogo.err.log can see
// that the matcher had these candidates and none of them hit.
func TestReadyTimeoutLogNamesAlternates(t *testing.T) {
	alternates := []string{"shift+tabtocycle", "?forshortcuts"}
	logged, output := timeoutLogForAlternates(t, "alts-present", "? for shortcuts", alternates)

	if !strings.Contains(logged, "not seen within") {
		t.Fatalf("expected the prompt-ready timeout message; got %q", logged)
	}
	if !strings.Contains(logged, `"? for shortcuts"`) {
		t.Errorf("timeout message must still name the primary sentinel; got %q", logged)
	}
	for _, alt := range alternates {
		if !strings.Contains(logged, fmt.Sprintf("%q", alt)) {
			t.Errorf("timeout message must name alternate %q; got %q", alt, logged)
		}
	}
	if !strings.Contains(output, "late msg") {
		t.Errorf("expected best-effort delivery of 'late msg' after timeout; got %q", output)
	}
}

// TestReadyTimeoutLogShowsEmptyAlternates is the positive control the ticket
// requires. A change that only prints a POPULATED list leaves the stale-binary
// case reading exactly as it does today, which is the whole defect. So assert
// the empty case directly: the alternates must be rendered as an empty list,
// not omitted, and the two messages must not be identical.
func TestReadyTimeoutLogShowsEmptyAlternates(t *testing.T) {
	empty, output := timeoutLogForAlternates(t, "alts-empty", "? for shortcuts", nil)

	if !strings.Contains(empty, "not seen within") {
		t.Fatalf("expected the prompt-ready timeout message; got %q", empty)
	}
	if !strings.Contains(empty, "alternates []") {
		t.Errorf("an empty alternates list must be VISIBLY empty in the timeout "+
			"message (that is the stale-binary signature); got %q", empty)
	}

	populated, _ := timeoutLogForAlternates(t, "alts-present-2", "? for shortcuts",
		[]string{"shift+tabtocycle"})
	if stripAgentName(empty) == stripAgentName(populated) {
		t.Errorf("stale-binary and non-matching-alternate cases must be tellable "+
			"apart from the log line alone; both read %q", empty)
	}

	if !strings.Contains(output, "late msg") {
		t.Errorf("expected best-effort delivery of 'late msg' after timeout; got %q", output)
	}
}

// stripAgentName removes the leading "agent <name>: " so two captures taken
// under different agent names can be compared on their diagnostic content.
func stripAgentName(logged string) string {
	for _, line := range strings.Split(logged, "\n") {
		if !strings.Contains(line, "not seen within") {
			continue
		}
		if _, rest, ok := strings.Cut(line, ": "); ok {
			return rest
		}
		return line
	}
	return logged
}
