package stallwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCarrierItem writes an available gh-issue carrier exactly as the
// coordinator's playbook writes one: the workflow state is the leading lines of
// the BODY, under the title heading, and the assignee is whatever the caller
// says — empty, in the case this file exists for.
func writeCarrierItem(t *testing.T, workRoot, id, stage, assignee, priority string, modTime time.Time) {
	t.Helper()
	dir := filepath.Join(workRoot, "available")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".md")
	content := fmt.Sprintf("---\nid: %s\ntype: task\nassignee: %s\npriority: %s\ntags: [gh-issue]\n---\n",
		id, assignee, priority)
	content += fmt.Sprintf("# triage: something a stranger reported (drellem2/pogo#104)\n"+
		"workflow: gh-issue\nstage: %s\ngh: drellem2/pogo#104\n\nTriage this issue.\n", stage)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// TestPriorityWakeIgnoresGatedCarrier reproduces the observation that produced
// mg-69b1, and is the reason this watcher changed at all.
//
// On 2026-08-09 three gh-issue carriers sat at `stage: gated` awaiting a human
// GO/NO-GO. All three read `status=available, assignee=[]` — so the assignee gate
// saw nothing to gate, and the priority wake fired at the coordinator: "2
// high-priority work item(s) are ready and unclaimed — claim or dispatch now".
// They were not ready and they were not the coordinator's to dispatch; the gate
// they were behind was written in a field nothing here read.
func TestPriorityWakeIgnoresGatedCarrier(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeCarrierItem(t, workRoot, "mg-a3f0", "gated", "", "high", now.Add(-1*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("a carrier at `stage: gated` woke the coordinator to dispatch it "+
			"(%d nudges); the spawn point would then refuse the dispatch it recommended",
			rec.nudgeCount())
	}
}

// The standard 10-minute stall nudge is the other detector reading the same
// predicate. A gated carrier ages — that is what the gate is for ("silence =
// HOLD, however long that takes") — so an aged one must not become a stall.
func TestUnclaimedStallIgnoresGatedCarrier(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeCarrierItem(t, workRoot, "mg-b055", "gated", "", "", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("an aged carrier at the human gate fired the stall nudge (%d); "+
			"the gate's whole semantics is that it may sit indefinitely", rec.nudgeCount())
	}
}

// The negative half, and the one that keeps the fix from being a mute button:
// every other stage still draws the nudges it drew before. A carrier at
// `stage: review` is the coordinator's to dispatch — that is the dispatch the
// stage exists to receive.
func TestWatchersStillSeeUngatedCarriers(t *testing.T) {
	for _, stage := range []string{"triage", "build", "review", "merge"} {
		t.Run(stage, func(t *testing.T) {
			w, rec, workRoot, _ := testEnv(t, baseConfig())
			now := time.Now()
			writeCarrierItem(t, workRoot, "mg-live", stage, "", "high", now.Add(-1*time.Minute))

			w.Check(now)

			if rec.nudgeCount() != 1 {
				t.Fatalf("a carrier at stage %q drew %d nudges, want 1 — only `gated` gates",
					stage, rec.nudgeCount())
			}
		})
	}
}
