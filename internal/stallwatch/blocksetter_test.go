package stallwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// These tests pin drellem2/pogo#161: the blocked-reminder is not sent to an
// agent that wrote its own block (defect 1), and a waiver tag silences it
// (defect 2). Suppression must FAIL OPEN — every case below where the setter is
// not known with certainty must still produce the notice.

// eventsPathFor is where testEnv's watcher reads macguffin's event log: beside
// the work tree, as macguffin writes it.
func eventsPathFor(workRoot string) string {
	return filepath.Join(filepath.Dir(workRoot), "events.jsonl")
}

// appendEvents appends raw JSONL lines to the event log.
func appendEvents(t *testing.T, workRoot string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(eventsPathFor(workRoot), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

// editedAssignee is a work.edited event in macguffin's real shape (copied from
// ~/.macguffin/events.jsonl, hashes dropped).
func editedAssignee(id, actor, before, after string) string {
	return fmt.Sprintf(`{"actor":%q,"assignee_after":%q,"assignee_before":%q,"fields":"assignee","item_id":%q,"mode":"append","ts":"2026-11-03T14:23:38Z","type":"work.edited"}`,
		actor, after, before, id)
}

func createdWithAssignee(id, actor, assignee string) string {
	return fmt.Sprintf(`{"actor":%q,"assignee":%q,"item_id":%q,"tags":"","to_status":"available","ts":"2026-11-03T15:46:32Z","type":"work.created"}`,
		actor, assignee, id)
}

// writeWaiverItem writes an available item carrying tags.
func writeWaiverItem(t *testing.T, workRoot, id, assignee string, tags []string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(workRoot, "available", id+".md")
	content := fmt.Sprintf("---\nid: %s\ntype: task\nassignee: %s\ntags: [%s]\n---\n# %s\n",
		id, assignee, strings.Join(tags, ", "), id)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// quietEvents returns the recorded events that report blocks held silent.
func quietEvents(rec *recorder, key string) [][]string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out [][]string
	for _, e := range rec.events {
		if e.Details["category"] != categoryBlockedReminder {
			continue
		}
		if ids, ok := e.Details[key].([]string); ok {
			out = append(out, ids)
		}
	}
	return out
}

// TestBlockedReminderSkipsASelfSetBlock is defect 1 verbatim: mayor wrote
// `blocked:mayor` on five items and was then asked, five times in one notice, to
// reconsider. A block the named agent wrote itself draws nothing — and the
// silence is recorded, so it is countable.
func TestBlockedReminderSkipsASelfSetBlock(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "mayor")
	now := time.Now()
	for _, id := range []string{"mg-0305", "mg-83b0", "mg-9efb"} {
		writeItem(t, workRoot, id, "blocked:mayor", now)
		appendEvents(t, workRoot, editedAssignee(id, "mayor", "", "blocked:mayor"))
	}

	w.Check(now)

	if got := blockedNudges(rec); len(got) != 0 {
		t.Fatalf("self-set blocks drew %d reminders, want 0: %+v", len(got), got)
	}
	q := quietEvents(rec, "self_set_ids")
	if len(q) != 1 || strings.Join(q[0], ",") != "mg-0305,mg-83b0,mg-9efb" {
		t.Errorf("self-set silence not recorded once with all three ids: %v", q)
	}

	// Recorded on entry, not every tick.
	w.Check(now.Add(time.Minute))
	if q := quietEvents(rec, "self_set_ids"); len(q) != 1 {
		t.Errorf("self-set silence recorded %d times over two ticks, want 1", len(q))
	}
}

// TestBlockedReminderStillReachesABlockSomeoneElseSet is the positive control
// for the test above, and the case the mechanism exists for: pm-pogo blocks an
// item on mayor, so mayor is told.
func TestBlockedReminderStillReachesABlockSomeoneElseSet(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "mayor")
	now := time.Now()
	writeItem(t, workRoot, "mg-e084", "blocked:mayor", now)
	appendEvents(t, workRoot, editedAssignee("mg-e084", "pm-pogo", "", "blocked:mayor"))

	w.Check(now)

	got := blockedNudges(rec)
	if len(got) != 1 || got[0].agent != "mayor" {
		t.Fatalf("a block set by another agent drew %+v, want one reminder to mayor", got)
	}
}

// TestBlockedReminderSkipsABlockSetAtCreation: `mg new --assignee=blocked:mayor`
// by mayor writes the value in work.created, not work.edited.
func TestBlockedReminderSkipsABlockSetAtCreation(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "mayor")
	now := time.Now()
	writeItem(t, workRoot, "mg-66191", "blocked:mayor", now)
	appendEvents(t, workRoot, createdWithAssignee("mg-66191", "mayor", "blocked:mayor"))

	w.Check(now)

	if got := blockedNudges(rec); len(got) != 0 {
		t.Fatalf("a block mayor created on itself drew %d reminders, want 0", len(got))
	}
}

// TestBlockedReminderSetterSuppressionFailsOpen is the written acceptance: every
// way the setter can be unknown or ambiguous produces the notice.
func TestBlockedReminderSetterSuppressionFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string // nil: no events.jsonl at all
	}{
		{"no event log", nil},
		{"no event for the item", []string{editedAssignee("mg-other", "mayor", "", "blocked:mayor")}},
		{"empty actor", []string{editedAssignee("mg-hold", "", "", "blocked:mayor")}},
		{"last write recorded a different value", []string{
			// mayor set it, then something wrote it to pm-pogo, and the field now
			// reads blocked:mayor again with no event to say who.
			editedAssignee("mg-hold", "mayor", "", "blocked:mayor"),
			editedAssignee("mg-hold", "architect", "blocked:mayor", "blocked:pm-pogo"),
		}},
		{"restored by an unclaim, not chosen", []string{
			`{"actor":"mayor","assignee":"blocked:mayor","from_status":"claimed","item_id":"mg-hold","to_status":"available","ts":"2026-11-03T16:40:26Z","type":"work.unclaim"}`,
		}},
		{"assignee not in the edited fields", []string{
			`{"actor":"mayor","assignee_after":"blocked:mayor","fields":"title","item_id":"mg-hold","type":"work.edited"}`,
		}},
		{"malformed line", []string{`{"actor":"mayor","assignee_after":"blocked:mayor","item_id":"mg-hold","type":"work.edited"`}},
		{"OS-user fallback actor", []string{editedAssignee("mg-hold", "daniel", "", "blocked:mayor")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
			makeMailbox(t, mailRoot, "mayor")
			now := time.Now()
			writeItem(t, workRoot, "mg-hold", "blocked:mayor", now)
			if tc.events != nil {
				appendEvents(t, workRoot, tc.events...)
			}

			w.Check(now)

			if got := blockedNudges(rec); len(got) != 1 {
				t.Fatalf("unknown setter drew %d reminders, want 1 — suppression must fail open", len(got))
			}
			if q := quietEvents(rec, "self_set_ids"); len(q) != 0 {
				t.Errorf("an unknown setter was recorded as self-set: %v", q)
			}
		})
	}
}

// TestBlockedReminderReadsTheLogIncrementally: a block written after the first
// tick is attributed on the next one, and a log that shrank (rotated) is re-read
// from the start rather than from a now-meaningless offset.
func TestBlockedReminderReadsTheLogIncrementally(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "mayor")
	now := time.Now()
	appendEvents(t, workRoot, `{"type":"mail.sent","actor":"x"}`)
	w.Check(now) // reads the log once; nothing blocked yet

	writeItem(t, workRoot, "mg-late", "blocked:mayor", now)
	appendEvents(t, workRoot, editedAssignee("mg-late", "mayor", "", "blocked:mayor"))
	w.Check(now.Add(time.Second))
	if got := blockedNudges(rec); len(got) != 0 {
		t.Fatalf("a self-set block appended after the first read drew %d reminders, want 0", len(got))
	}

	// Rotate: replace the log with a shorter one in which pm-pogo set the block.
	if err := os.Remove(eventsPathFor(workRoot)); err != nil {
		t.Fatal(err)
	}
	appendEvents(t, workRoot, editedAssignee("mg-late", "pm-pogo", "", "blocked:mayor"))
	w.Check(now.Add(2 * time.Second))
	if got := blockedNudges(rec); len(got) != 1 {
		t.Fatalf("after rotation the block reads as set by pm-pogo; got %d reminders, want 1", len(got))
	}
}

// TestBlockSetterLogLeavesAPartialLine: a line without its newline is a write
// in progress and must be read whole on the next call, not half-parsed now and
// skipped forever.
func TestBlockSetterLogLeavesAPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	line := editedAssignee("mg-p", "mayor", "", "blocked:mayor")
	if err := os.WriteFile(path, []byte(line[:40]), 0o644); err != nil {
		t.Fatal(err)
	}
	l := newBlockSetterLog(path)
	if !l.refresh() {
		t.Fatal("refresh failed on a readable log")
	}
	if got := l.setter("mg-p", "blocked:mayor"); got != "" {
		t.Fatalf("partial line attributed to %q", got)
	}
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l.refresh()
	if got := l.setter("mg-p", "blocked:mayor"); got != "mayor" {
		t.Fatalf("completed line attributed to %q, want mayor", got)
	}
}

// TestBlockedReminderHonoursTheWaiverTag is defect 2: the notice's escape hatch
// works. An item tagged with the default waiver draws nothing, the silence is
// recorded, and removing the tag re-arms the reminder with an immediate first
// notice.
func TestBlockedReminderHonoursTheWaiverTag(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeWaiverItem(t, workRoot, "mg-a31c", "blocked:pm-pogo", []string{"pogo", "Waiting-On-Purpose"}, now)
	writeItem(t, workRoot, "mg-loud", "blocked:pm-pogo", now) // positive control

	w.Check(now)

	got := blockedNudges(rec)
	if len(got) != 1 {
		t.Fatalf("got %d reminders, want 1 (for mg-loud only)", len(got))
	}
	if strings.Contains(got[0].message, "mg-a31c") || !strings.Contains(got[0].message, "mg-loud") {
		t.Errorf("waived item named, or control missing: %q", got[0].message)
	}
	if q := quietEvents(rec, "waived_ids"); len(q) != 1 || q[0][0] != "mg-a31c" {
		t.Errorf("waiver silence not recorded: %v", q)
	}

	// Remove the waiver: the reminder is re-armed and fires at once.
	writeWaiverItem(t, workRoot, "mg-a31c", "blocked:pm-pogo", []string{"pogo"}, now)
	w.Check(now.Add(time.Second))
	got = blockedNudges(rec)
	if len(got) != 2 || !strings.Contains(got[1].message, "mg-a31c") {
		t.Fatalf("removing the waiver did not re-arm the reminder: %+v", got)
	}
}

// TestBlockedReminderWaiverTagIsConfigurableAndNamed: the notice names the tag
// that actually silences it, and the exact command — so the instruction it gives
// is one the code honours.
func TestBlockedReminderWaiverTagIsConfigurableAndNamed(t *testing.T) {
	cfg := blockedCfg()
	cfg.BlockedReminderWaiverTags = []string{"hold-ack"}
	w, rec, workRoot, mailRoot := testEnv(t, cfg)
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeWaiverItem(t, workRoot, "mg-ack", "blocked:pm-pogo", []string{"hold-ack"}, now)
	writeWaiverItem(t, workRoot, "mg-def", "blocked:pm-pogo", config.DefaultBlockedReminderWaiverTags, now)

	w.Check(now)

	got := blockedNudges(rec)
	if len(got) != 1 {
		t.Fatalf("got %d reminders, want 1", len(got))
	}
	msg := got[0].message
	if strings.Contains(msg, "mg-ack") || !strings.Contains(msg, "mg-def") {
		t.Errorf("configured waiver not honoured, or the default still honoured in its place: %q", msg)
	}
	if !strings.Contains(msg, "--add-tags=hold-ack") {
		t.Errorf("notice does not name the configured waiver command: %q", msg)
	}
	if !strings.Contains(msg, "stays blocked") {
		t.Errorf("notice does not say the waiver leaves the item gated: %q", msg)
	}
}

// TestBlockedReminderWaiverDoesNotUngateDispatch: the waiver silences a NOTICE.
// A waived blocked item is still invisible to the dispatch checks.
func TestBlockedReminderWaiverDoesNotUngateDispatch(t *testing.T) {
	cfg := blockedCfg()
	cfg.UnclaimedItemAgeThreshold = time.Minute
	w, rec, workRoot, mailRoot := testEnv(t, cfg)
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeWaiverItem(t, workRoot, "mg-w", "blocked:pm-pogo", config.DefaultBlockedReminderWaiverTags, now.Add(-time.Hour))

	w.Check(now)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, n := range rec.nudges {
		if strings.HasPrefix(n.message, "blocked-reminder:") {
			continue
		}
		if strings.Contains(n.message, "mg-w") {
			t.Errorf("a waived blocked item appeared in a notice: %+v", n)
		}
	}
}
