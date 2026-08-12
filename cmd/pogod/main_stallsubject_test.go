package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/stallwatch"
)

// mg-b6f8: the delivery site must carry the notice's own subject to the mail.
//
// This is the half of the fix that lives here. The watcher composes a subject
// from facts only it holds (category, count, item ids, oldest age); this
// function used to overwrite all of that with one constant, which is why 18
// stall-watch mails to `human` on 2026-08-11/12 arrived under one sentence.
// A subject that is composed and then discarded at the last step is the same
// defect with more code, so it is pinned at the delivery site as well as at the
// composition site.
func TestStallNudgerMailsTheNoticesOwnSubject(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	var got struct{ to, from, subject, body string }
	nudge := newStallNudger(reg, func(to, from, subject, body string) error {
		got.to, got.from, got.subject, got.body = to, from, subject, body
		return nil
	}, config.DefaultStallMailFallbackBacklogCap)

	const subj = "stall-watch: 1 item blocked on you, oldest 6h3m — mg-0218"
	if _, err := nudge("ghost-agent", stallwatch.Notice{
		Subject: subj,
		Message: "blocked-reminder: ...",
	}); err != nil {
		t.Fatalf("nudge: %v", err)
	}
	if got.subject != subj {
		t.Errorf("mail subject = %q, want the notice's own %q", got.subject, subj)
	}
	if got.subject == stallSubjectFallback {
		t.Error("delivery overwrote a composed subject with the pre-mg-b6f8 constant")
	}
}

// The busy-PTY road carries the same subject plus the marker that says the
// terminal refused it. The marker is a SUFFIX, not a replacement: before
// mg-b6f8 this road had its own constant, so a notice that took it lost its
// identity twice over.
func TestStallNudgerMailFallbackKeepsTheSubjectAndMarksIt(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	spawnBusyAgent(t, reg, "busy-mayor")

	var got struct{ to, from, subject, body string }
	nudge := newStallNudgerWithTimeout(reg, func(to, from, subject, body string) error {
		got.to, got.from, got.subject, got.body = to, from, subject, body
		return nil
	}, 300*time.Millisecond)

	const subj = "stall-watch: 3 items unclaimed, oldest 42m — mg-a, mg-b, mg-c"
	delivery, err := nudge("busy-mayor", stallwatch.Notice{Subject: subj, Message: "STALL"})
	if err != nil {
		t.Fatalf("nudge: %v", err)
	}
	if delivery.Channel != stallwatch.DeliveryMailFallback {
		t.Fatalf("delivery channel = %q, want %q", delivery.Channel, stallwatch.DeliveryMailFallback)
	}
	if !strings.HasPrefix(got.subject, subj) {
		t.Errorf("fallback subject %q dropped the notice's own subject %q", got.subject, subj)
	}
	if !strings.Contains(got.subject, "undelivered to terminal") {
		t.Errorf("fallback subject must still say the terminal refused it, got %q", got.subject)
	}
}

// A notice with no subject must still produce mail. The fallback string is the
// one every stall-watch mail used to carry, kept only so an empty subject line
// is impossible — seeing it in a maildir means the watcher composed nothing,
// which is a bug upstream rather than here.
func TestStallSubjectFallsBackWhenTheNoticeCarriesNone(t *testing.T) {
	for name, n := range map[string]stallwatch.Notice{
		"empty":      {Message: "m"},
		"whitespace": {Subject: "   ", Message: "m"},
	} {
		if got := stallSubject(n); got != stallSubjectFallback {
			t.Errorf("%s: stallSubject = %q, want the fallback %q", name, got, stallSubjectFallback)
		}
	}
	if got := stallSubject(stallwatch.Notice{Subject: "real", Message: "m"}); got != "real" {
		t.Errorf("a composed subject must win, got %q", got)
	}
}
