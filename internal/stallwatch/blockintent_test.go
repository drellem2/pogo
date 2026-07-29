package stallwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTaggedItem writes an available work item carrying tags, so the
// block-intent advisory (mg-6fb0) can be exercised against the frontmatter shape
// mg actually writes rather than a struct literal.
func writeTaggedItem(t *testing.T, workRoot, id, assignee, tags, priority string, modTime time.Time) {
	t.Helper()
	dir := filepath.Join(workRoot, "available")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".md")
	content := fmt.Sprintf("---\nid: %s\ntype: task\ntags: [%s]\nassignee: %s\n", id, tags, assignee)
	if priority != "" {
		content += fmt.Sprintf("priority: %s\n", priority)
	}
	content += fmt.Sprintf("---\n# %s\n", id)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// TestBlockedShapeIsGatedInStallWatch: the new shape must be quiet in the
// watcher, exactly as `human` and `parked` are. This is the watch half of
// mg-6fb0 — the gate has two enforcement sites reading one predicate, and a
// shape honoured at the dispatch point but not here would be the mg-4798 drift
// the shared predicate exists to prevent.
//
// Both directions ride in one table, for mg-a3a2's stated reason: a gate only
// ever observed suppressing has not been shown to pass anything through.
func TestBlockedShapeIsGatedInStallWatch(t *testing.T) {
	tests := []struct {
		name      string
		assignee  string
		wantFires bool
		why       string
	}{
		{"blocked on a named agent is gated", "blocked:daniel", false,
			"the whole point: waiting on someone named, and not dispatchable while it waits"},
		{"blocked on a crew agent is gated", "blocked:pm-pogo", false,
			"one shape covers every agent — no roster, so mg-4bd4 cannot recur here"},
		{"the shape gates case-insensitively", " Blocked: Mayor ", false,
			"the value is hand-edited frontmatter as often as it is CLI-written"},

		{"the same agent as a plain owner still alarms", "pm-pogo", true,
			"ownership is not a block; this is most of the queue and muting it would hide real work"},
		{"the coordinator as a plain owner still alarms", "mayor", true,
			"mg-bf5e's case, and it is correct by design — owned-by-mayor is dispatchable"},
		{"the tag idiom as an assignee still alarms", "blocked-on-daniel", true,
			"tags do not gate, and a tag written into the assignee field is not the shape"},
		{"human is undisturbed", "human", false,
			"additive only — the pre-existing gates read exactly as before"},
		{"parked is undisturbed", "parked", false,
			"same"},
		{"unassigned still alarms", "", true,
			"the case the fleet runs on"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, rec, workRoot, _ := testEnv(t, baseConfig())
			now := time.Now()
			writeItem(t, workRoot, "mg-6fb0", tt.assignee, now.Add(-20*time.Minute))

			w.Check(now)

			if got := rec.nudgeCount() > 0; got != tt.wantFires {
				t.Errorf("assignee %q: fired=%v, want %v\nwhy: %s",
					tt.assignee, got, tt.wantFires, tt.why)
			}
		})
	}
}

// TestBlockedShapeSuppressesThePriorityWake: the fast wake reads the same
// predicate, so a high-priority blocked item must not bypass the gate by being
// urgent. Its twin — a high-priority OWNED item still waking — rides along,
// because a wake that had gone quiet for the wrong reason would look identical.
func TestBlockedShapeSuppressesThePriorityWake(t *testing.T) {
	for _, tt := range []struct {
		assignee  string
		wantFires bool
	}{
		{"blocked:daniel", false},
		{"pm-pogo", true},
	} {
		t.Run(tt.assignee, func(t *testing.T) {
			w, rec, workRoot, _ := testEnv(t, baseConfig())
			now := time.Now()
			writePriorityItem(t, workRoot, "mg-6fb1", tt.assignee, "high", now.Add(-time.Minute))

			w.Check(now)

			if got := rec.nudgeCount() > 0; got != tt.wantFires {
				t.Errorf("high-priority item assigned %q: fired=%v, want %v",
					tt.assignee, got, tt.wantFires)
			}
		})
	}
}

// TestBlockIntentAdvisory is the POSITIVE CONTROL mg-6fb0 required for the
// warning: it is demonstrated FIRING on a real mismatch, not only staying quiet.
//
// The mismatch is an item that declares a block in its tags while its assignee
// leaves it dispatchable — mg-a96c's live shape (`assignee: pm-pogo`,
// `blocked-on-daniel-confirm`). The nudge still fires (a tag is not a gate); the
// advisory rides on it and names the contradiction plus the value that would fix
// it.
func TestBlockIntentAdvisoryFires(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeTaggedItem(t, workRoot, "mg-a96c", "pm-pogo", "pogo, blocked-on-daniel-confirm", "", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("want 1 nudge (the item is dispatchable and stale), got %d", rec.nudgeCount())
	}
	msg := rec.nudges[0].message
	for _, want := range []string{"block-intent", "mg-a96c", "blocked-on-daniel-confirm", "blocked:daniel-confirm"} {
		if !strings.Contains(msg, want) {
			t.Errorf("advisory missing %q\ngot: %s", want, msg)
		}
	}
	if rec.eventCount() != 1 {
		t.Fatalf("want 1 event, got %d", rec.eventCount())
	}
	ids, ok := rec.events[0].Details["block_intent_mismatch_ids"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "mg-a96c" {
		t.Errorf("event details block_intent_mismatch_ids = %v (ok=%v), want [mg-a96c]", ids, ok)
	}
}

// TestBlockIntentAdvisoryAdvisesDependsForItemBlocks: a `blocked-on-mg-1234` tag
// is waiting on another ITEM, and `mg new --depends` already expresses that.
// Pointing the reader at the assignee field for it would be wrong advice, so the
// advisory says --depends instead — the check is only worth having if what it
// tells you to do is right.
func TestBlockIntentAdvisoryAdvisesDependsForItemBlocks(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeTaggedItem(t, workRoot, "mg-0ffc", "pm-pogo", "pogo, blocked-on-mg-01f7", "", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("want 1 nudge, got %d", rec.nudgeCount())
	}
	msg := rec.nudges[0].message
	if !strings.Contains(msg, "--depends") {
		t.Errorf("advisory should point at --depends for an item-to-item block\ngot: %s", msg)
	}
	if strings.Contains(msg, "blocked:mg-01f7") {
		t.Errorf("advisory must not suggest putting a work-item id in the assignee field\ngot: %s", msg)
	}
}

// TestBlockIntentAdvisoryStaysQuiet is the other half, and it is the half that
// decides whether this is usable. pm-template files every ticket with
// `--assignee=pm-<name>`, so an advisory that fired on a named assignee alone
// would ride on nearly every nudge and be trained away within a day.
//
// Each case is quiet for a different reason, including the one that matters most
// for the sequencing: the repair itself (`blocked:<agent>`) must not trip the
// interim check.
func TestBlockIntentAdvisoryStaysQuiet(t *testing.T) {
	tests := []struct {
		name     string
		assignee string
		tags     string
		why      string
	}{
		{"ordinary owned item", "pm-pogo", "pogo, cli",
			"ownership declares nothing about blocking"},
		{"unassigned item", "", "pogo",
			"the ordinary dispatchable case"},
		{"no tags at all", "mayor", "",
			"nothing declared, nothing to contradict"},
		{"a tag that merely mentions blocking", "pm-pogo", "unblocked, blocking",
			"the prefix is blocked-on-, not a substring search"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, rec, workRoot, _ := testEnv(t, baseConfig())
			now := time.Now()
			writeTaggedItem(t, workRoot, "mg-quiet", tt.assignee, tt.tags, "", now.Add(-20*time.Minute))

			w.Check(now)

			if rec.nudgeCount() != 1 {
				t.Fatalf("want the ordinary stall nudge, got %d nudges", rec.nudgeCount())
			}
			if strings.Contains(rec.nudges[0].message, "block-intent") {
				t.Errorf("advisory fired on a legitimate item (%s)\nwhy it should not: %s\ngot: %s",
					tt.name, tt.why, rec.nudges[0].message)
			}
			if _, present := rec.events[0].Details["block_intent_mismatch_ids"]; present {
				t.Errorf("event carries block_intent_mismatch_ids for a legitimate item (%s)", tt.name)
			}
		})
	}
}

// TestBlockIntentAdvisoryQuietOnGatedItems: a gated item never reaches a nudge
// at all, so the advisory cannot fire on it — including on `blocked:<agent>`,
// the value the advisory itself recommends. An interim check that flagged the
// repair it is recommending would be worse than no check.
func TestBlockIntentAdvisoryQuietOnGatedItems(t *testing.T) {
	for _, assignee := range []string{"human", "parked", "blocked:daniel"} {
		t.Run(assignee, func(t *testing.T) {
			w, rec, workRoot, _ := testEnv(t, baseConfig())
			now := time.Now()
			writeTaggedItem(t, workRoot, "mg-gated", assignee, "pogo, blocked-on-daniel", "", now.Add(-20*time.Minute))

			w.Check(now)

			if rec.nudgeCount() != 0 {
				t.Fatalf("gated item %q fired %d nudge(s): %v",
					assignee, rec.nudgeCount(), rec.nudges)
			}
		})
	}
}
