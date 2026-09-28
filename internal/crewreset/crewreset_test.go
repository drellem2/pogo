package crewreset

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type sent struct{ to, from, subject, body string }

type fixture struct {
	pop     []Present
	mails   []sent
	nudges  []string
	events  []string
	mailErr error
}

func (f *fixture) watcher(o Options) *Watcher {
	o.Population = func() ([]Present, error) { return append([]Present(nil), f.pop...), nil }
	o.Mail = func(to, from, subject, body string) error {
		if f.mailErr != nil {
			return f.mailErr
		}
		f.mails = append(f.mails, sent{to, from, subject, body})
		return nil
	}
	o.Emit = func(ev string, _ map[string]any) { f.events = append(f.events, ev) }
	o.Logf = func(string, ...any) {}
	return New(o)
}

func (f *fixture) mailsTo(name string) int {
	n := 0
	for _, m := range f.mails {
		if m.to == name {
			n++
		}
	}
	return n
}

var t0 = time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)

// TestFourHourNoticeWithFakeClock is the positive control mg-5b58d asks for: a
// 4h01m crew agent gets exactly one notice, a 3h59m one gets none, and the
// re-notice goes at most once.
func TestFourHourNoticeWithFakeClock(t *testing.T) {
	now := t0
	f := &fixture{pop: []Present{
		{Name: "pm-old", StartedAt: now.Add(-(4*time.Hour + time.Minute))},
		{Name: "pm-young", StartedAt: now.Add(-(3*time.Hour + 59*time.Minute))},
	}}
	w := f.watcher(Options{})

	got := w.Tick(now)
	if len(got) != 1 || got[0].Agent != "pm-old" || got[0].Number != 1 {
		t.Fatalf("first tick: got %+v, want exactly one notice #1 to pm-old", got)
	}
	if f.mailsTo("pm-old") != 1 || f.mailsTo("pm-young") != 0 {
		t.Fatalf("mails after first tick: pm-old=%d pm-young=%d, want 1 and 0", f.mailsTo("pm-old"), f.mailsTo("pm-young"))
	}
	m := f.mails[0]
	if m.from != Sender {
		t.Errorf("From = %q, want %q", m.from, Sender)
	}
	for _, want := range []string{"pogo agent stop pm-old", "sweep.log", "memory directory", "4h01m", "safe point", "unread or unhandled mail"} {
		if !strings.Contains(m.subject+"\n"+m.body, want) {
			t.Errorf("notice does not contain %q:\n%s\n%s", want, m.subject, m.body)
		}
	}

	// Ticks inside the hour send pm-old nothing more — this is the "exactly
	// one". (pm-young crosses 4h at +1m and gets its own first notice here.)
	for _, d := range []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 59 * time.Minute} {
		for _, n := range w.Tick(now.Add(d)) {
			if n.Agent == "pm-old" {
				t.Fatalf("tick at +%s sent pm-old %+v, want nothing inside renotice_after", d, n)
			}
		}
	}
	if f.mailsTo("pm-old") != 1 {
		t.Fatalf("pm-old got %d mails in its first hour, want exactly 1", f.mailsTo("pm-old"))
	}
	if f.mailsTo("pm-young") != 1 {
		t.Fatalf("pm-young got %d mails once past 4h, want 1", f.mailsTo("pm-young"))
	}

	// One re-notice an hour after the first, then silence for the session.
	got = w.Tick(now.Add(time.Hour))
	var re []Notice
	for _, n := range got {
		if n.Agent == "pm-old" {
			re = append(re, n)
		}
	}
	if len(re) != 1 || re[0].Number != 2 {
		t.Fatalf("tick at +1h: pm-old notices %+v, want exactly one re-notice (#2)", re)
	}
	if last := f.mails[len(f.mails)-1]; last.to != "pm-old" || !strings.Contains(last.subject, "last one") {
		t.Errorf("re-notice to %s does not say it is the last: %q", last.to, last.subject)
	}
	for h := 2; h <= 24; h++ {
		w.Tick(now.Add(time.Duration(h) * time.Hour))
	}
	if f.mailsTo("pm-old") != MaxNotices {
		t.Fatalf("pm-old got %d mails over 24h, want %d (no nag loop)", f.mailsTo("pm-old"), MaxNotices)
	}
	if f.mailsTo("pm-young") != MaxNotices {
		t.Fatalf("pm-young got %d mails over 24h, want %d", f.mailsTo("pm-young"), MaxNotices)
	}
}

// TestYoungAgentNeverNoticed: the negative arm on its own, so a watcher that
// sends to nobody cannot pass the test above by accident — it must also fail
// there, and this one must pass on the same instrument.
func TestYoungAgentNeverNoticed(t *testing.T) {
	now := t0
	f := &fixture{pop: []Present{{Name: "pm-young", StartedAt: now.Add(-(3*time.Hour + 59*time.Minute))}}}
	w := f.watcher(Options{})
	if got := w.Tick(now); len(got) != 0 || len(f.mails) != 0 {
		t.Fatalf("3h59m agent got %+v / %d mails, want none", got, len(f.mails))
	}
	// Positive control on the same watcher: two minutes later it is 4h01m.
	if got := w.Tick(now.Add(2 * time.Minute)); len(got) != 1 {
		t.Fatalf("same agent at 4h01m got %d notices, want 1", len(got))
	}
}

// TestRespawnStartsAFreshCount: the session is (name, start time), so an agent
// that followed the notice and came back is a new session with a new budget —
// and is not asked again until IT passes 4h.
func TestRespawnStartsAFreshCount(t *testing.T) {
	now := t0
	f := &fixture{pop: []Present{{Name: "mayor", StartedAt: now.Add(-5 * time.Hour)}}}
	w := f.watcher(Options{})
	w.Tick(now)
	w.Tick(now.Add(time.Hour))
	if f.mailsTo("mayor") != 2 {
		t.Fatalf("got %d mails before the reset, want 2", f.mailsTo("mayor"))
	}
	// The agent resets: new session.
	restart := now.Add(time.Hour + 10*time.Minute)
	f.pop = []Present{{Name: "mayor", StartedAt: restart}}
	for d := time.Duration(0); d < 4*time.Hour; d += 5 * time.Minute {
		if got := w.Tick(restart.Add(d)); len(got) != 0 {
			t.Fatalf("fresh session noticed at uptime %s", d)
		}
	}
	if got := w.Tick(restart.Add(4*time.Hour + time.Minute)); len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("fresh session at 4h01m: got %+v, want notice #1", got)
	}
}

func TestExcludedAgentIsNeverAsked(t *testing.T) {
	now := t0
	f := &fixture{pop: []Present{
		{Name: "architect", StartedAt: now.Add(-10 * time.Hour)},
		{Name: "pm-pogo", StartedAt: now.Add(-10 * time.Hour)},
	}}
	w := f.watcher(Options{Exclude: []string{"architect"}})
	w.Tick(now)
	w.Tick(now.Add(2 * time.Hour))
	if f.mailsTo("architect") != 0 {
		t.Fatalf("excluded agent got %d mails", f.mailsTo("architect"))
	}
	if f.mailsTo("pm-pogo") != 2 {
		t.Fatalf("control agent got %d mails, want 2", f.mailsTo("pm-pogo"))
	}
}

// TestFailedMailIsNotCounted: a notice that never landed must not use up the
// budget — otherwise one mg outage silently skips the agent for the session.
func TestFailedMailIsNotCounted(t *testing.T) {
	now := t0
	f := &fixture{pop: []Present{{Name: "pm-pogo", StartedAt: now.Add(-4*time.Hour - time.Minute)}},
		mailErr: errors.New("mg: store locked")}
	w := f.watcher(Options{})
	got := w.Tick(now)
	if len(got) != 1 || got[0].MailErr == nil {
		t.Fatalf("got %+v, want one failed attempt", got)
	}
	f.mailErr = nil
	got = w.Tick(now.Add(5 * time.Minute))
	if len(got) != 1 || got[0].Number != 1 || got[0].MailErr != nil {
		t.Fatalf("retry: got %+v, want notice #1 delivered", got)
	}
}

func TestNudgeOnlyWhenWired(t *testing.T) {
	now := t0
	f := &fixture{pop: []Present{{Name: "pm-pogo", StartedAt: now.Add(-5 * time.Hour)}}}
	var nudged []string
	w := f.watcher(Options{Nudge: func(name, text string) error {
		nudged = append(nudged, name+": "+text)
		return nil
	}})
	w.Tick(now)
	if len(nudged) != 1 || !strings.Contains(nudged[0], "pm-pogo: ") {
		t.Fatalf("nudges = %v, want one pointer at pm-pogo", nudged)
	}
	if len(Pointer(2)) > 100 {
		t.Errorf("pointer is %d bytes; keep it within wake-watch's 100-byte pointer cap", len(Pointer(2)))
	}
	// Without Nudge, the mail alone goes (wake-watch types the pointer).
	f2 := &fixture{pop: f.pop}
	w2 := f2.watcher(Options{})
	if got := w2.Tick(now); len(got) != 1 || got[0].NudgeErr != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestHM(t *testing.T) {
	for d, want := range map[time.Duration]string{
		4*time.Hour + time.Minute:           "4h01m",
		3*time.Hour + 59*time.Minute + 59e9: "3h59m",
		4 * time.Hour:                       "4h00m",
	} {
		if got := HM(d); got != want {
			t.Errorf("HM(%s) = %q, want %q", d, got, want)
		}
	}
}
