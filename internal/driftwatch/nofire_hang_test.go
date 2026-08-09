package driftwatch

import (
	"strings"
	"testing"
)

// The runner's half of the hang finding (mg-56ac): does pogod actually MAIL when
// a run starts and does not finish, and does the subject — the part that travels
// — say so?
//
// The pairing matters as much here as in the judging package. On 2026-08-09 this
// runner did fire, did mail, and reported five silent nights while saying
// nothing about the night a run had hung for 31h39m. A notice that goes out is
// not evidence that the right notice goes out.

// hangIncidentLog is the real deploy log across the 2026-08-08 outage, with the
// interior of the untouched runs elided. The 08-08 fire starts at 02:00:05Z and
// the next line it writes is 31h39m later.
const hangIncidentLog = `[2026-08-07T02:00:05Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-07T02:00:10Z] attempt recorded: 2026-08-07 attempt=1 rc=6 (/Users/daniel/.pogo/deploy-attempt.stamp)
[2026-08-08T02:00:05Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-08T02:00:05Z] budget: drain gets up to 7200s (window ends 6:00, reserve 1200s, cap 7200s)
[2026-08-08T02:00:05Z] GH_TOKEN: sourced from /Users/daniel/.zshenv (present, 40 chars)
[2026-08-09T09:39:43Z] sync: /Users/daniel/.pogo/deploy-src at main 738e322
[2026-08-09T09:43:23Z] pogo-deploy: done — pogod redeployed to 738e322
[2026-08-09T09:43:23Z] attempt recorded: 2026-08-09 attempt=1 rc=0 (/Users/daniel/.pogo/deploy-attempt.stamp)
`

// TestNoFireMailsTheHang is the runner-side acceptance. It requires a mail, and
// it requires the HANG to be in the subject rather than buried under the
// missed-night count that shares the notice.
func TestNoFireMailsTheHang(t *testing.T) {
	rec := &recorder{}
	w := New(noFireCfg(), noFireOpts(rec, hangIncidentLog))

	w.Check(nofireAt(t, "2026-08-09T12:00"))

	if rec.mailCount() != 1 {
		t.Fatalf("mails = %d, want 1 — a run that started and took 31h39m produced no notice", rec.mailCount())
	}
	m := rec.mails[0]
	if m.to != "human" {
		t.Errorf("mailed %q, want human", m.to)
	}
	if !strings.Contains(m.subject, "HUNG") {
		t.Errorf("subject = %q — the hang has to be in the line that travels. A subject that opens with the missed-night count puts the hang back on the branch mg-56ac is about", m.subject)
	}
	if !strings.Contains(m.subject, "2026-08-08") {
		t.Errorf("subject = %q, want the night the run STARTED on (not 08-09, the date it stamped when it woke up)", m.subject)
	}
	if !strings.Contains(m.subject, "31h43m") {
		t.Errorf("subject = %q, want the duration — a count cannot separate a 6h01m run from a 31h one", m.subject)
	}

	// The body has to give the reader the two things they can act on: where the
	// run went quiet, and that the fleet may still be down underneath it.
	if !strings.Contains(m.body, "GH_TOKEN: sourced") {
		t.Error("body does not print the last line the run emitted before it went silent — that line is what bounds the stall")
	}
	if !strings.Contains(m.body, "pogo agent list") {
		t.Error("body does not tell the reader to check the FLEET; a hung run holds the drain, and on 08-08 that was 33 hours of no crew")
	}

	var fired bool
	for _, e := range rec.events {
		if e.EventType == "deploy_nofire" {
			fired = true
			if got := e.Details["hung_total"]; got != 1 {
				t.Errorf("event hung_total = %v, want 1", got)
			}
			if _, ok := e.Details["hung"]; !ok {
				t.Error("event carries no `hung` detail — a subject string is not something a detector can filter on")
			}
		}
	}
	if !fired {
		t.Error("no deploy_nofire event emitted")
	}
}

// TestNoFireQuietWhenEveryRunFinishes is the negative control at the same
// instant: the same runner, the same schedule, a log whose runs all finish in
// minutes, and nothing is mailed.
func TestNoFireQuietWhenEveryRunFinishes(t *testing.T) {
	healthy := `[2026-08-07T02:00:05Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)
[2026-08-07T02:00:10Z] pogo-deploy: end (rc=6 after 5s)
[2026-08-08T02:00:05Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)
[2026-08-08T02:03:47Z] pogo-deploy: end (rc=0 after 222s)
[2026-08-09T02:00:05Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)
[2026-08-09T02:03:47Z] pogo-deploy: end (rc=0 after 222s)
`
	rec := &recorder{}
	w := New(noFireCfg(), noFireOpts(rec, healthy))

	w.Check(nofireAt(t, "2026-08-09T12:00"))

	if rec.mailCount() != 0 {
		t.Fatalf("mails = %d, want 0 — every run in this log started and finished: %q", rec.mailCount(), rec.mails[0].subject)
	}
	for _, e := range rec.events {
		if e.EventType == "deploy_nofire" {
			t.Errorf("deploy_nofire emitted on a log where every night fired and every run terminated: %v", e.Details)
		}
	}
}

// TestNoFireNoticeBudgetSurvivesAGrowingHang. An unterminated run gets LONGER on
// every sample, and a signature keyed on its length would mail on every sample
// forever — turning the one notice that matters into the noise the budget
// exists to prevent. The night and the count are the key; the duration is not.
func TestNoFireNoticeBudgetSurvivesAGrowingHang(t *testing.T) {
	log := `[2026-08-08T02:00:05Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)
[2026-08-08T02:00:06Z] pogo-deploy: end (rc=0 after 1s)
[2026-08-09T02:00:05Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)
[2026-08-09T02:00:06Z] GH_TOKEN: sourced from /Users/daniel/.zshenv (present, 40 chars)
`
	rec := &recorder{}
	w := New(noFireCfg(), noFireOpts(rec, log))

	// 08:00 BST is 5h59m after the 03:00 BST start — inside the six-hour
	// threshold, so the run is still a DEPLOY and this sample must be silent.
	// Without this arm the test could pass on a witness that reports every
	// in-flight run, which would mail a RED on every healthy night.
	w.Check(nofireAt(t, "2026-08-09T08:00"))
	if rec.mailCount() != 0 {
		t.Fatalf("a run 5h59m old was reported at the 6h threshold: %q", rec.mails[0].subject)
	}

	// Then three samples across the next three hours, with the hang growing
	// between each.
	w.Check(nofireAt(t, "2026-08-09T09:30"))
	w.Check(nofireAt(t, "2026-08-09T10:30"))
	w.Check(nofireAt(t, "2026-08-09T11:30"))

	if rec.mailCount() != 1 {
		t.Fatalf("mails = %d, want 1 — the hang's LENGTH must not be part of the notice signature, or every sample mails", rec.mailCount())
	}
	if !strings.Contains(rec.mails[0].subject, "still not finished") {
		t.Errorf("subject = %q, want the unterminated wording — this is the branch a deadline cannot catch and the witness must", rec.mails[0].subject)
	}

	// The EVENT is not capped, so the pull-side record stays complete while the
	// mail stays quiet. "pogod stopped mailing" must never read as "the run
	// finished".
	var events int
	for _, e := range rec.events {
		if e.EventType == "deploy_nofire" {
			events++
		}
	}
	if events != 3 {
		t.Errorf("deploy_nofire events = %d, want 3 (one per sample) — the mail cap must not make the condition invisible", events)
	}
}
