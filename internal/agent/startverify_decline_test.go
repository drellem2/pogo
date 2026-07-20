package agent

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// captureLog redirects the standard logger to a buffer for the duration of the
// test and returns a closure yielding everything written so far. The
// start-verify decline path reports through log.Printf (as the rest of
// startverify.go does), so the only way to assert "silent" vs "loud" is to read
// the logger's own output.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return buf.String
}

// readEventLinesIfAny is readEventLines tolerant of a never-created log. Emit
// creates the file lazily, so "no events at all" shows up as a missing file
// rather than an empty one — which readEventLines treats as a fatal open error.
// The decline tests assert absence, so they need the distinction not to matter.
func readEventLinesIfAny(t *testing.T, path string) []map[string]any {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return readEventLines(t, path)
}

// TestVerifyStartAndRenudge_NoWorkItemDeclinesLoudly is the mg-2437 positive
// control, driven at the code level.
//
// The defect: `--no-worktree` dispatch without `--id` is exactly the shape
// documented as commonly carrying no work item (see mailcheck_test.go), and
// `--id` is optional. Such a polecat reaches verifyStartAndRenudge with an
// empty WorkItemID, which early-returns — so the started-verifier is not a
// degraded recovery net for that dispatch shape, it is a structurally absent
// one, and NOTHING said so.
//
// This test was first written against the bug, asserting the silent-decline
// behavior, and confirmed green on unfixed code; the fix inverted it. A
// recovery path only ever observed in its working case has not been tested.
//
// Note what is NOT asserted: that the agent gets watched. It cannot be — there
// is no hard started-signal without a work item, and the package doc explains
// at length why an output-quiescence substitute would reproduce the very
// false-idle bug the watcher exists to recover. Declining is correct. Declining
// SILENTLY is the defect.
func TestVerifyStartAndRenudge_NoWorkItemDeclinesLoudly(t *testing.T) {
	logged := captureLog(t)
	eventLog := useTempEventLog(t)

	a, readAll, _ := newRenudgeTestAgent(t, "")
	verifier, count := countingVerifier([]verifyCall{{started: false}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	// The decline itself is unchanged: no verifier query, no keystroke.
	if got := readAll(); got != "" {
		t.Errorf("expected no renudge for an agent with no work item, got %q", got)
	}
	if c := count(); c != 0 {
		t.Errorf("expected verifier never consulted, got %d", c)
	}

	// ...but it must now announce itself by name, with the remedy.
	out := logged()
	if !strings.Contains(out, a.Name) {
		t.Errorf("decline must name the unwatched agent %q; log was:\n%s", a.Name, out)
	}
	if !strings.Contains(out, "--id") {
		t.Errorf("decline must name the --id remedy; log was:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "unwatched") {
		t.Errorf("decline must say the agent is UNWATCHED; log was:\n%s", out)
	}

	// A log line alone is not loud enough. The auto_renudge machinery's own
	// history is the argument: a per-spawn log line was invisible for the whole
	// #76 sentinel episode (mg-ce4c), which is why the renudge emits an event.
	// A structurally absent recovery net deserves at least the same visibility.
	ev := findEvent(readEventLines(t, eventLog), "agent_unwatched", "pogod")
	if ev == nil {
		t.Fatalf("expected an agent_unwatched event for a polecat with no work item; got %v",
			readEventLines(t, eventLog))
	}
	details, _ := ev["details"].(map[string]any)
	if details["to"] != a.eventAgent() {
		t.Errorf("event must name the unwatched agent, got details=%v", details)
	}
	if details["reason"] != "no_work_item_id" {
		t.Errorf("event must distinguish the decline reason, got details=%v", details)
	}
}

// TestVerifyStartAndRenudge_NilVerifierDeclinesLoudly: the other structural
// decline. A polecat spawned while no verifier is wired at all is equally
// unwatched, and equally deserves to say so — distinguished by reason so an
// operator can tell "this dispatch had no --id" from "this daemon has no
// recovery net at all".
func TestVerifyStartAndRenudge_NilVerifierDeclinesLoudly(t *testing.T) {
	logged := captureLog(t)
	eventLog := useTempEventLog(t)

	a, readAll, _ := newRenudgeTestAgent(t, "mg-test")
	reg := fastRenudgeRegistry(nil, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("expected no renudge with nil verifier, got %q", got)
	}
	out := logged()
	if !strings.Contains(out, a.Name) || !strings.Contains(strings.ToLower(out), "unwatched") {
		t.Errorf("nil-verifier decline must loudly name the agent; log was:\n%s", out)
	}
	ev := findEvent(readEventLines(t, eventLog), "agent_unwatched", "pogod")
	if ev == nil {
		t.Fatalf("expected an agent_unwatched event when no verifier is wired")
	}
	details, _ := ev["details"].(map[string]any)
	if details["reason"] != "no_start_verifier" {
		t.Errorf("nil-verifier decline needs its own reason, got details=%v", details)
	}
}

// TestVerifyStartAndRenudge_CrewDeclineStaysQuiet keeps the new loudness from
// becoming noise. Crew agents are long-lived, never carry a work item by
// design, and are respawned/nudged on their own cycle — an alarm on every crew
// spawn would train an operator to ignore the line that matters. The gap this
// ticket closes is a POLECAT dispatch gap.
func TestVerifyStartAndRenudge_CrewDeclineStaysQuiet(t *testing.T) {
	logged := captureLog(t)
	eventLog := useTempEventLog(t)

	a, readAll, _ := newRenudgeTestAgent(t, "")
	a.Type = TypeCrew
	reg := fastRenudgeRegistry(func(string) (bool, error) { return false, nil }, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("expected no renudge for a crew agent, got %q", got)
	}
	if out := logged(); strings.Contains(strings.ToLower(out), "unwatched") {
		t.Errorf("crew decline is by-design and must stay quiet; log was:\n%s", out)
	}
	if ev := findEvent(readEventLinesIfAny(t, eventLog), "agent_unwatched", "pogod"); ev != nil {
		t.Errorf("crew decline must not emit an operator alarm, got %v", ev)
	}
}
