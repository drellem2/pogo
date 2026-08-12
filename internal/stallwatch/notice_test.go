package stallwatch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
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
// different single item, then that same item repeating — and asserts what the
// old code could not do: every notice is distinguishable from every other by
// subject alone.
func TestBlockedReminderSubjectsAreDistinguishableAcrossTheMeasuredSequence(t *testing.T) {
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

	// Four notices about the one persisting item — the cap. These are the
	// repeats that produced six identical mails in the measured window. The
	// first fires immediately (an unseen item is never delayed); the rest are
	// spaced past the backoff.
	w.Check(base)
	for i := 1; i <= 3; i++ {
		w.Check(base.Add(time.Duration(i) * 5 * time.Hour))
	}

	got := rec.subjects()
	if len(got) < 5 {
		t.Fatalf("expected at least 5 notices (1 batch + 4 repeats), got %d: %q", len(got), got)
	}

	seen := make(map[string]int, len(got))
	for i, s := range got {
		if first, dup := seen[s]; dup {
			t.Errorf("notice %d repeats notice %d's subject verbatim: %q\n"+
				"That is mg-b6f8: in a Discord notification list these two are the same line.\n"+
				"all subjects: %q", i, first, s, got)
			continue
		}
		seen[s] = i
	}

	// And the subjects must actually say which items, not merely differ.
	if !strings.Contains(got[0], "mg-8888") || !strings.Contains(got[0], "mg-fbc1") {
		t.Errorf("first subject must name both blocked items, got %q", got[0])
	}
	for _, s := range got[1:] {
		if !strings.Contains(s, "mg-0218") {
			t.Errorf("subject %q must name the item it is about", s)
		}
	}
}

// The repeats above are distinguished by the oldest item's age, which is the
// only one of (category, count, ids, age) that must move when a stall persists.
// This pins that directly, because it is the property the whole design rests on
// and it would break silently if the age were ever dropped or coarsened.
func TestSubjectRepeatsAreSeparatedByAge(t *testing.T) {
	ids := []string{"mg-0218"}
	first := subject("1 item blocked on you", 90*time.Minute, ids)
	later := subject("1 item blocked on you", 4*time.Hour+30*time.Minute, ids)
	if first == later {
		t.Fatalf("two notices about the same item at different ages share a subject: %q", first)
	}

	// Minute resolution, against the shortest cooldown any category uses: the
	// priority wake's 3m. Two fires that far apart must not collide.
	a := subject("1 item unclaimed", 3*time.Hour, ids)
	b := subject("1 item unclaimed", 3*time.Hour+config.DefaultHighPriorityWakeCooldown, ids)
	if a == b {
		t.Fatalf("fires one priority-wake cooldown apart collide: %q", a)
	}
}

// Different checks reach the same recipient's notification list and mean
// different things — "dispatch these" versus "these are blocked ON YOU, do not
// dispatch". Identical counts and ages must not make them read alike.
func TestSubjectHeadsSeparateTheCategories(t *testing.T) {
	const age = 2 * time.Hour
	ids := []string{"mg-aaaa"}
	heads := map[string]string{
		"unclaimed":     subject(nItems(1)+" unclaimed", age, ids),
		"priority":      subject(nItems(1)+" high-priority, unclaimed", age, ids),
		"worked":        subject(nItems(1)+" unclaimed but WORKED", age, ids),
		"blocked":       subject(nItems(1)+" blocked on you", age, ids),
		"unreachable":   subject(nItems(1)+" with an UNREACHABLE blocker", age, ids),
		"unread-mail":   subject("12 unread mail", age, nil),
		"unread-mail-2": subject("13 unread mail", age, nil),
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
	s := subject(nItems(len(ids))+" unclaimed", time.Hour, ids)
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
