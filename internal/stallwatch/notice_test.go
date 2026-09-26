package stallwatch

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The measured population mg-b6f8 was filed on, replayed as a test.
//
// `human` received 18 stall-watch mails between 2026-08-11 12:00Z and
// 2026-08-12 09:52Z. Every one was a blocked-reminder; the bodies named three
// different item sets at two different counts; all 18 subjects were the string
// "stall-watch: work piling up". The recipient reads mail through Discord,
// which renders the subject, so the whole population was one sentence eighteen
// times.
//
// This test replays the SHAPE of that sequence — a two-item batch, then a
// different single item, then that same item repeating — and asserts the two
// halves of what a subject is for:
//
//   - different item sets are distinguishable by subject alone, which the old
//     constant could not do (mg-b6f8);
//   - repeats about the SAME item set share one subject, so `mg mail reclaim`
//     can coalesce them (mg-09d9). mg-b6f8 first separated repeats by age,
//     which made every copy unique and the backlog undrainable.
func TestBlockedReminderSubjectsSeparateItemSetsAndCoalesceRepeats(t *testing.T) {
	cfg := blockedCfg()
	cfg.BlockedReminderMaxNotices = 4
	w, rec, workRoot, mailRoot := testEnv(t, cfg)
	makeMailbox(t, mailRoot, "human")

	base := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	// Two items blocked on the recipient, as the 08-11 evening mails were.
	writeItem(t, workRoot, "mg-8888", "blocked:human", base.Add(-2*time.Hour))
	writeItem(t, workRoot, "mg-fbc1", "blocked:human", base.Add(-3*time.Hour))
	w.Check(base)

	// Both clear; a different single item takes their place, as mg-0218 did.
	removeItem(t, workRoot, "available", "mg-8888")
	removeItem(t, workRoot, "available", "mg-fbc1")
	writeItem(t, workRoot, "mg-0218", "blocked:human", base.Add(-90*time.Minute))

	// Four notices about the one persisting item — the cap. The first fires
	// immediately (an unseen item is never delayed); the rest are spaced past
	// the backoff.
	w.Check(base)
	for i := 1; i <= 3; i++ {
		w.Check(base.Add(time.Duration(i) * 5 * time.Hour))
	}

	got := rec.subjects()
	if len(got) < 5 {
		t.Fatalf("expected at least 5 notices (1 batch + 4 repeats), got %d: %q", len(got), got)
	}

	// The batch and the single item are different alarms.
	if got[0] == got[1] {
		t.Errorf("two different item sets share a subject %q — that is mg-b6f8", got[0])
	}
	if !strings.Contains(got[0], "mg-8888") || !strings.Contains(got[0], "mg-fbc1") {
		t.Errorf("first subject must name both blocked items, got %q", got[0])
	}
	// The repeats are one alarm, hours apart: one subject.
	for i, s := range got[1:] {
		if !strings.Contains(s, "mg-0218") {
			t.Errorf("subject %q must name the item it is about", s)
		}
		if s != got[1] {
			t.Errorf("repeat %d about the same item has subject %q, first had %q — "+
				"a subject that moves while the stall persists defeats mg mail reclaim (mg-09d9)",
				i+1, s, got[1])
		}
	}
}

// A subject must not carry anything that moves while a condition merely
// persists. The age did, and it made 2162 of 2167 stall-watch mails distinct
// subjects (mg-09d9). Pinned at the builder so a future "make repeats
// distinguishable" change has to delete this test to reintroduce it.
func TestSubjectCarriesNoAge(t *testing.T) {
	ids := []string{"mg-0218"}
	s := subject("1 item blocked on you", ids)
	if s != "stall-watch: 1 item blocked on you — mg-0218" {
		t.Errorf("subject = %q", s)
	}
	if strings.Contains(s, "oldest") {
		t.Errorf("subject carries an age: %q", s)
	}
}

// The unread-mail alarm, driven through the watcher: a backlog that grows and
// ages between fires must keep one subject, while the body still carries the
// count and age a reader needs. This is the sender that held 62% of the
// mayor's 3152-message backlog, every copy under its own subject.
func TestUnreadMailSubjectIsStableAsTheBacklogGrows(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxUnreadMailCount = 1
	w, rec, _, mailRoot := testEnv(t, cfg)

	t0 := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		writeMail(t, mailRoot, cfg.Agent, fmt.Sprintf("a%d", i), t0.Add(-time.Hour))
	}
	w.Check(t0)
	for i := 0; i < 40; i++ {
		writeMail(t, mailRoot, cfg.Agent, fmt.Sprintf("b%d", i), t0)
	}
	w.Check(t0.Add(6 * time.Hour))

	if rec.nudgeCount() != 2 {
		t.Fatalf("want 2 notices, got %d: %q", rec.nudgeCount(), rec.subjects())
	}
	got := rec.subjects()
	if got[0] != got[1] {
		t.Errorf("the same backlog at 3 and 43 unread rendered two subjects: %q, %q", got[0], got[1])
	}
	for _, d := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		if strings.Contains(got[0], d) {
			t.Errorf("unread-mail subject carries a number: %q", got[0])
			break
		}
	}
	// The facts moved to the body; they were not dropped.
	rec.mu.Lock()
	first, second := rec.nudges[0].message, rec.nudges[1].message
	rec.mu.Unlock()
	if !strings.Contains(first, "3 unread") || !strings.Contains(second, "43 unread") {
		t.Errorf("bodies must carry the live count:\n%s\n%s", first, second)
	}
	if !strings.Contains(second, "old") {
		t.Errorf("body must carry the age: %s", second)
	}
}

// Different checks reach the same recipient's notification list and mean
// different things — "dispatch these" versus "these are blocked ON YOU, do not
// dispatch". Identical counts must not make them read alike.
func TestSubjectHeadsSeparateTheCategories(t *testing.T) {
	ids := []string{"mg-aaaa"}
	heads := map[string]string{
		"unclaimed":   subject(nItems(1)+" unclaimed", ids),
		"priority":    subject(nItems(1)+" high-priority, unclaimed", ids),
		"worked":      subject(nItems(1)+" unclaimed but WORKED", ids),
		"blocked":     subject(nItems(1)+" blocked on you", ids),
		"unreachable": subject(nItems(1)+" with an UNREACHABLE blocker", ids),
		"unread-mail": subject(unreadMailSubjectHead, nil),
	}
	seen := make(map[string]string, len(heads))
	for name, s := range heads {
		if other, dup := seen[s]; dup {
			t.Errorf("%s and %s render the same subject %q", name, other, s)
		}
		seen[s] = name
	}
}

func TestSubjectTruncatesLongIDLists(t *testing.T) {
	var ids []string
	for i := 0; i < subjectIDLimit+3; i++ {
		ids = append(ids, fmt.Sprintf("mg-%04d", i))
	}
	s := subject(nItems(len(ids))+" unclaimed", ids)
	if !strings.Contains(s, "+3 more") {
		t.Errorf("subject must say how many ids it dropped, got %q", s)
	}
	if strings.Contains(s, ids[subjectIDLimit]) {
		t.Errorf("subject named an id past the limit: %q", s)
	}
	// The count survives truncation, so "how big is this" is never lost.
	if !strings.Contains(s, fmt.Sprintf("%d items", len(ids))) {
		t.Errorf("subject must keep the full count, got %q", s)
	}
}

func TestCompactAge(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "2m"},
		{42 * time.Minute, "42m"},
		{time.Hour, "1h"},
		{6*time.Hour + 3*time.Minute, "6h3m"},
		{25 * time.Hour, "1d1h"},
		{48 * time.Hour, "2d"},
	} {
		if got := compactAge(tc.in); got != tc.want {
			t.Errorf("compactAge(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The ages this is fed come from now.Sub(modtime), so they carry sub-second
	// noise. time.Duration.String would render that; a subject must not.
	if got := compactAge(6*time.Hour + 3*time.Minute + 499*time.Millisecond); got != "6h3m" {
		t.Errorf("sub-second noise reached the subject: %q", got)
	}
}

// Every fire must carry a subject. A notice that reaches the delivery site with
// an empty one falls back to the single string this ticket exists to remove, so
// the gap has to be a test failure here rather than a rediscovery in a maildir.
func TestEveryFiredNoticeCarriesASubject(t *testing.T) {
	cfg := baseConfig()
	cfg.PriorityWakeEnabled = true
	cfg.BlockedReminderEnabled = true
	cfg.MaxUnreadMailCount = 1
	w, rec, workRoot, mailRoot := testEnv(t, cfg)
	makeMailbox(t, mailRoot, "human")

	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	writeItem(t, workRoot, "mg-slow", "mayor", now.Add(-2*time.Hour))
	writePriorityItem(t, workRoot, "mg-fast", "mayor", "high", now.Add(-2*time.Hour))
	writeItem(t, workRoot, "mg-held", "blocked:human", now.Add(-2*time.Hour))
	writeItem(t, workRoot, "mg-lost", "blocked:nobody-here", now.Add(-2*time.Hour))
	for i := 0; i < 3; i++ {
		writeMail(t, mailRoot, cfg.Agent, fmt.Sprintf("m%d", i), now.Add(-2*time.Hour))
	}

	w.Check(now)

	if rec.nudgeCount() == 0 {
		t.Fatal("no notices fired; the fixture stopped exercising the checks")
	}
	for i, s := range rec.subjects() {
		if strings.TrimSpace(s) == "" {
			t.Errorf("notice %d fired with an empty subject; it would deliver as %q",
				i, "stall-watch: work piling up")
		}
		if !strings.HasPrefix(s, "stall-watch: ") {
			t.Errorf("notice %d subject %q must be attributable to stall-watch at a glance", i, s)
		}
	}
}
