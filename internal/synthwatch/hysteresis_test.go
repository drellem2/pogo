package synthwatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/claude"
	"github.com/drellem2/pogo/internal/synthfail"
)

// The flapping half of the failing_turns alarm (mg-70f3).
//
// # Every time below is a SEND time, and that is not a detail
//
// `~/.pogo/reminders/deadman.log` stamps each line with when the delivering
// daemon NOTICED a mail; the send time is the maildir filename's leading
// nanosecond stamp, minted by the sender. That lag was 16m26s on the anchor page
// of 2026-08-14 and it varies with how busy the notifier was, so any gap between
// two mails computed from log lines is wrong by the difference of two lags.
// mg-70f3 re-extracted all 93 of this alarm's mails from their send stamps.
//
// Population, as of 2026-08-14T08:16Z: **49 open pages and 44 clear notices** in
// the log, which supersedes the 45/40 pc058 measured a few hours earlier — the
// alarm added four of each overnight, on old code. Of those 93 mails, 14 (7 open,
// 7 clear) were SENT between 2026-07-29 and 2026-08-07, before the log's poller
// started at 18:43:57Z on the 7th, and were swept in its first pass; the 79 sent
// inside the log's own window run 2026-08-08T15:21Z–2026-08-14T06:58Z, giving
// **42 open pages over 5.7 days — 7.4 a day**.
//
// Three figures in circulation that this supersedes, withdrawn by name:
//   - the original filing's "twelve pages in four days" — a partial listing.
//   - the 2026-08-10T20:17:18Z "SAME SECOND, twice" pair. Both mails were
//     NOTICED at 20:17:18Z; they were sent at 20:00:43Z and 20:01:24Z, 41.8s
//     apart (pc058's correction, re-derived here).
//   - **pm-pogo's clear→re-alarm gaps for 2026-08-14 — 2m29s, 2m07s, 14m04s and
//     6m37s.** Those are notice-time gaps. By send stamp the same four cycles are
//     **2m50s, 2m29s, 10m09s and 3m31s**, and there is a **fifth** that list does
//     not have: 06:26:37Z→06:58:09Z, **31m32s**. pm-pogo flagged the risk itself;
//     this is that flag coming true.

// step is one reading in a replayed night.
type step struct {
	at      time.Time
	failing bool
}

func utc(h, m, s int) time.Time { return time.Date(2026, 8, 14, h, m, s, 0, time.UTC) }

// buildWith is build() with the watcher's own knobs exposed, for the tests that
// need the hysteresis and the floor separated from each other.
func buildWith(rec *recorder, targets []Target, verdicts map[string]synthfail.Report, hold, floor time.Duration) *Watcher {
	globs, scan := scanByWorkdir(verdicts)
	return New(Options{
		Targets:         func() []Target { return targets },
		Globs:           globs,
		Scan:            scan,
		Mail:            rec.send,
		Emit:            rec.emit,
		Interval:        time.Nanosecond,
		ClearHold:       hold,
		MinPageInterval: floor,
	})
}

// ---------------------------------------------------------------------------
// HYSTERESIS ON THE CLOSE
// ---------------------------------------------------------------------------

// TestHold_TheMeasuredNightIsOneEpisodeAndOnePage replays 2026-08-14 from the
// maildir SEND stamps: an open at 02:28:12Z and then FIVE clear→re-alarm cycles,
// gaps of 2m50s, 2m29s, 10m09s, 3m31s and 31m32s. Under the pre-mg-70f3 watcher
// that was ELEVEN mails to a sleeping human — six pages and five all-clears —
// for ONE intermittent github.com reachability fault that ran from at least
// 01:18Z past 06:58Z.
//
// The fifth cycle is why the hold is 60m and not 30m: at 31m32s a 30m hold would
// have missed it by 92 seconds, closed the episode, and paged again.
//
// The named agent differs per cycle (p82a6, architect, architect, mayor,
// pm-pogo): it is whoever happened to take a turn during a burst, which is
// exactly why closing and re-opening on it was wrong.
func TestHold_TheMeasuredNightIsOneEpisodeAndOnePage(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonServerError)}
	w := build(rec, targets, verdicts)

	night := []step{
		{utc(2, 28, 12), true},  // page: the one that woke Daniel
		{utc(3, 4, 32), false},  // "cleared — 9 agent(s)"   (noticed 03:22:09Z)
		{utc(3, 7, 23), true},   // 2m50s   (p82a6)          (noticed 03:24:38Z)
		{utc(3, 46, 58), false}, // "cleared — 14 agent(s)"  (noticed 04:04:15Z)
		{utc(3, 49, 28), true},  // 2m29s   (architect)      (noticed 04:06:22Z)
		{utc(5, 1, 38), false},  // "cleared — 19 agent(s)"  (noticed 05:18:37Z)
		{utc(5, 11, 47), true},  // 10m09s  (architect)      (noticed 05:32:41Z)
		{utc(5, 46, 35), false}, // "cleared — 14 agent(s)"  (noticed 06:02:54Z)
		{utc(5, 50, 7), true},   // 3m31s   (mayor)          (noticed 06:09:31Z)
		{utc(6, 26, 37), false}, // "cleared — 13 agent(s)"  (noticed 06:44:40Z)
		{utc(6, 58, 9), true},   // 31m32s  (pm-pogo)        (noticed 07:16:37Z)
	}
	for _, s := range night {
		if s.failing {
			verdicts["/w/mayor"] = failing(synthfail.ReasonServerError)
		} else {
			verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
		}
		w.Check(s.at)
	}

	if len(rec.mails) != 1 {
		t.Fatalf("sent %d mails across the night's five flap cycles, want 1 (the opening page only); pre-mg-70f3 this was 11", len(rec.mails))
	}
	if n := rec.countType(EventEpisodeHeld); n != 5 {
		t.Errorf("recorded %d %s events, want 5 — the absorbed flaps are the only measurement that this worked", n, EventEpisodeHeld)
	}
	if n := rec.countType(claude.IncidentEpisodeClearedEvent); n != 0 {
		t.Errorf("emitted %d episode-cleared events while one intermittent fault was still recurring, want 0", n)
	}

	// The fault finally stops. The all-clear waits out the whole hold and then
	// goes exactly once, stating what it absorbed.
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(utc(7, 30, 0))
	if len(rec.mails) != 1 {
		t.Fatalf("sent the all-clear on the first quiet reading — that is the flap (sent %d mails)", len(rec.mails))
	}
	w.Check(utc(8, 30, 0))

	if len(rec.mails) != 2 {
		t.Fatalf("sent %d mails, want 2 (one page, one all-clear after 60m of continuous quiet)", len(rec.mails))
	}
	clear := rec.mails[1]
	if !strings.Contains(clear.subject, "5 recurrence(s)") {
		t.Errorf("clear subject = %q does not state how many recurrences the hold absorbed; damping the mail must not under-report the fault", clear.subject)
	}
	if !strings.Contains(clear.body, "ONE intermittent fault") {
		t.Errorf("clear body does not tell the reader to read the episode as one fault:\n%s", clear.body)
	}
	if n := rec.countType(claude.IncidentEpisodeClearedEvent); n != 1 {
		t.Errorf("emitted %d episode-cleared events for the night, want exactly 1", n)
	}
}

// TestHold_NeverDelaysTheOpeningPage is the bound this whole item works under.
// "Wait and see whether it clears before paging" was proposed and ruled out on
// evidence: the 2026-08-14 fault ran over five hours, and on 2026-07-22 a
// genuinely dead fleet went 23h30m unnoticed. The hold applies to the CLOSE.
func TestHold_NeverDelaysTheOpeningPage(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	w := build(rec, targets, map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonServerError)})

	w.Check(utc(2, 28, 12))

	if len(rec.mails) != 1 {
		t.Fatalf("sent %d mails on the first failing reading, want 1 — no damping may sit in front of the alarm", len(rec.mails))
	}
}

// TestHold_TheSharpestCycle is the founding instance on its own. The ticket
// records it as "cleared at 03:22:09Z, re-alarmed 2m29s later at 03:24:38Z";
// those are NOTICE times, and by send stamp it is a clear at 03:04:32Z and a
// re-alarm at 03:07:23Z — **2m50s**. Neither mail may leave the process.
func TestHold_TheSharpestCycle(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonServerError)}
	w := build(rec, targets, verdicts)

	w.Check(utc(2, 28, 12))
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(utc(3, 4, 32))
	verdicts["/w/mayor"] = failing(synthfail.ReasonServerError)
	w.Check(utc(3, 7, 23))

	if len(rec.mails) != 1 {
		t.Fatalf("sent %d mails, want 1: a clear and a re-alarm 2m50s apart is the founding instance", len(rec.mails))
	}
	held := rec.eventsOfType(EventEpisodeHeld)
	if len(held) != 1 {
		t.Fatalf("recorded %d %s events, want 1", len(held), EventEpisodeHeld)
	}
	if got := held[0].Details["quiet_seconds"]; got != 171 {
		t.Errorf("details.quiet_seconds = %v, want 171 (2m51s) — the gap is the fact worth recording", got)
	}
	if got := held[0].Details["recurrence"]; got != 1 {
		t.Errorf("details.recurrence = %v, want 1", got)
	}
}

// TestHold_TheThirtyOneMinuteCycle is the cycle that decided the constant. The
// last clear→re-alarm pair of 2026-08-14 was 06:26:37Z→06:58:09Z by send stamp —
// **31m32s**, and it appears in no list assembled from notice times. A 30m hold
// misses it by 92 seconds; a 60m hold absorbs it.
func TestHold_TheThirtyOneMinuteCycle(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonServerError)}
	w := build(rec, targets, verdicts)

	w.Check(utc(5, 50, 7))
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(utc(6, 26, 37))
	verdicts["/w/mayor"] = failing(synthfail.ReasonServerError)
	w.Check(utc(6, 58, 9))

	if len(rec.mails) != 1 {
		t.Fatalf("sent %d mails, want 1 — a 31m32s gap is inside the 60m hold this constant was sized for", len(rec.mails))
	}
	if got := DefaultClearHold; got <= 31*time.Minute+32*time.Second {
		t.Errorf("DefaultClearHold = %v, which no longer covers the widest measured recurrence gap (31m32s)", got)
	}
}

// TestHold_ADifferentAgentFailingInsideTheHoldDoesNotReopen. The agent named on
// a page is incidental — it is whoever took a turn during a burst, and across
// the four cycles of 2026-08-14 it was p82a6, architect, architect and mayor. A
// recurrence on a DIFFERENT agent is the same episode.
func TestHold_ADifferentAgentFailingInsideTheHoldDoesNotReopen(t *testing.T) {
	rec := &recorder{}
	targets := []Target{
		{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"},
		{Name: "architect", Identity: "crew-architect", Workdir: "/w/architect"},
	}
	verdicts := map[string]synthfail.Report{
		"/w/mayor":     failing(synthfail.ReasonServerError),
		"/w/architect": {State: synthfail.StateQuiet},
	}
	w := build(rec, targets, verdicts)

	w.Check(utc(2, 28, 12))
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(utc(3, 22, 9))
	verdicts["/w/architect"] = failing(synthfail.ReasonServerError)
	w.Check(utc(3, 24, 38))

	if len(rec.mails) != 1 {
		t.Fatalf("sent %d mails, want 1 — a burst that lands on another agent is not a new fault", len(rec.mails))
	}

	// And the eventual all-clear names BOTH, because both were in the class.
	verdicts["/w/architect"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(utc(4, 0, 0))
	w.Check(utc(5, 0, 0))
	if len(rec.mails) != 2 {
		t.Fatalf("sent %d mails, want 2", len(rec.mails))
	}
	for _, name := range []string{"mayor", "architect"} {
		if !strings.Contains(rec.mails[1].body, name) {
			t.Errorf("clear mail does not name %s, which was in the class this episode:\n%s", name, rec.mails[1].body)
		}
	}
}

// TestHold_ClosesOnceTheHoldActuallyElapses — the hold must not make an episode
// immortal. A fault that really ends produces exactly one all-clear.
func TestHold_ClosesOnceTheHoldActuallyElapses(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonAuthFailed)}
	w := build(rec, targets, verdicts)

	w.Check(utc(1, 0, 0))
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	for i := 0; i <= 120; i++ { // two hours of 1-minute heartbeats
		w.Check(utc(1, 0, 0).Add(time.Duration(i) * time.Minute))
	}

	if len(rec.mails) != 2 {
		t.Fatalf("sent %d mails over two hours of quiet, want 2 (open + one all-clear)", len(rec.mails))
	}
	if strings.Contains(rec.mails[1].subject, "recurrence") {
		t.Errorf("clear subject = %q claims recurrences on an episode that had none", rec.mails[1].subject)
	}
}

// TestHold_ClosesOnATickWithNoScanAtAll. The hold expires on the clock, not on a
// reading, so it must be evaluated even when every target is inside its scan
// interval — otherwise a fleet that goes quiet and then busy holds the all-clear
// until the next scan happens to land.
func TestHold_ClosesOnATickWithNoScanAtAll(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonAuthFailed)}
	globs, scan := scanByWorkdir(verdicts)
	w := New(Options{
		Targets:  func() []Target { return targets },
		Globs:    globs,
		Scan:     scan,
		Mail:     rec.send,
		Emit:     rec.emit,
		Interval: 4 * time.Hour, // no second scan will happen inside this test
	})

	w.Check(utc(1, 0, 0))
	// Force the quiet reading through the scan throttle by clearing directly, as
	// a departure would.
	targets = nil
	w.Check(utc(1, 0, 30))
	targets = []Target{{Name: "mayor", Workdir: "/w/mayor"}}
	w.Check(utc(2, 0, 30))

	if len(rec.mails) != 2 {
		t.Fatalf("sent %d mails, want 2 — the hold was never evaluated on a tick that scanned nothing", len(rec.mails))
	}
}

// TestHold_DisabledRestoresTheOldImmediateClose. The escape hatch has to work,
// and it is the control that shows the tests above are measuring the hold rather
// than something else in the path.
func TestHold_DisabledRestoresTheOldImmediateClose(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonAuthFailed)}
	w := buildWith(rec, targets, verdicts, -1, -1)

	w.Check(utc(1, 0, 0))
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(utc(1, 0, 5))

	if len(rec.mails) != 2 {
		t.Fatalf("sent %d mails with the hold disabled, want 2 (open + immediate close)", len(rec.mails))
	}
}

// ---------------------------------------------------------------------------
// THE PAGING FLOOR
// ---------------------------------------------------------------------------

// TestFloor_TheFortyOneSecondPair is the 2026-08-10 case with the hysteresis
// taken out of the picture, so it is the FLOOR being measured and nothing else.
// Two identical opens 41.8s apart (20:00:43Z and 20:01:24Z by their maildir send
// stamps — the shared 20:17:18Z in deadman.log is the notice time) collapse to
// one page. Any dedup window narrower than ~42s would have missed this.
func TestFloor_TheFortyOneSecondPair(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonSpendLimit)}
	w := buildWith(rec, targets, verdicts, -1, 0) // hold OFF, floor at its default

	sent1 := time.Date(2026, 8, 10, 20, 0, 43, 121219000, time.UTC)
	sent2 := time.Date(2026, 8, 10, 20, 1, 24, 908327000, time.UTC)

	w.Check(sent1)
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(sent1.Add(time.Second)) // closes immediately: hold is off
	verdicts["/w/mayor"] = failing(synthfail.ReasonSpendLimit)
	w.Check(sent2)

	var opens int
	for _, m := range rec.mails {
		if strings.HasPrefix(m.subject, "AGENTS FAILING TURNS") {
			opens++
		}
	}
	if opens != 1 {
		t.Fatalf("sent %d open pages 41.8s apart, want 1", opens)
	}
	sup := rec.eventsOfType(EventPageSuppressed)
	if len(sup) != 1 {
		t.Fatalf("recorded %d %s events, want 1 — a suppression nobody can see is indistinguishable from one that never happened", len(sup), EventPageSuppressed)
	}
	if got := sup[0].Details["since_last_page_sec"]; got != 41 {
		t.Errorf("details.since_last_page_sec = %v, want 41", got)
	}
}

// TestFloor_NeverDelaysADifferentReason is the floor's half of the bound. A
// reason change is new information about a different fix — rate_limit decaying
// into auth_failed means a human has to run /login — and it pages immediately,
// however recently the last page went.
func TestFloor_NeverDelaysADifferentReason(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonRateLimit)}
	w := buildWith(rec, targets, verdicts, -1, 0) // hold OFF, floor at its default

	w.Check(utc(1, 0, 0))
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(utc(1, 0, 5))
	verdicts["/w/mayor"] = failing(synthfail.ReasonAuthFailed)
	w.Check(utc(1, 0, 10)) // ten seconds after the last page, but a NEW cause

	var opens int
	for _, m := range rec.mails {
		if strings.HasPrefix(m.subject, "AGENTS FAILING TURNS") {
			opens++
		}
	}
	if opens != 2 {
		t.Fatalf("sent %d open pages, want 2 — the floor withheld a page about a DIFFERENT fault, which is the thing this item may not do", opens)
	}
	if n := rec.countType(EventPageSuppressed); n != 0 {
		t.Errorf("recorded %d suppressions for a reason change, want 0", n)
	}
}

// TestFloor_DoesNotBiteUnderTheShippedDefaults. DefaultClearHold ==
// DefaultMinPageInterval, so two episode-opens can never be closer together than
// a full hold and the floor is a pure backstop. If this ever fails, the shipped
// configuration has started withholding real pages.
func TestFloor_DoesNotBiteUnderTheShippedDefaults(t *testing.T) {
	if DefaultMinPageInterval > DefaultClearHold {
		t.Fatalf("floor %s exceeds hold %s: the floor can now withhold the first page of a genuinely new episode", DefaultMinPageInterval, DefaultClearHold)
	}
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonServerError)}
	w := build(rec, targets, verdicts)

	t0 := utc(1, 0, 0)
	w.Check(t0)
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(t0.Add(time.Second))
	w.Check(t0.Add(time.Second + DefaultClearHold)) // episode 1 closes
	verdicts["/w/mayor"] = failing(synthfail.ReasonServerError)
	w.Check(t0.Add(2*time.Second + DefaultClearHold)) // a genuinely new episode

	if n := rec.countType(EventPageSuppressed); n != 0 {
		t.Fatalf("the floor withheld %d page(s) under the shipped defaults, want 0", n)
	}
	var opens int
	for _, m := range rec.mails {
		if strings.HasPrefix(m.subject, "AGENTS FAILING TURNS") {
			opens++
		}
	}
	if opens != 2 {
		t.Errorf("sent %d open pages for two episodes separated by a full hold, want 2", opens)
	}
}

// TestClearMail_StatesTheFlooredPagesToo. The clear mail is the one place a
// human learns what the damping did; a floored page that appears nowhere would
// be the under-reporting failure this fix is supposed to avoid.
func TestClearMail_StatesTheFlooredPagesToo(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonSpendLimit)}
	// A five-minute hold with the default floor: short enough that episodes close
	// between recurrences, so the floor is what catches the second open.
	w := buildWith(rec, targets, verdicts, 5*time.Minute, 0)

	t0 := utc(1, 0, 0)
	w.Check(t0)
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(t0.Add(time.Minute))
	w.Check(t0.Add(6 * time.Minute)) // episode 1 closes
	verdicts["/w/mayor"] = failing(synthfail.ReasonSpendLimit)
	w.Check(t0.Add(7 * time.Minute)) // episode 2 opens, page FLOORED
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(t0.Add(8 * time.Minute))
	w.Check(t0.Add(14 * time.Minute)) // episode 2 closes

	last := rec.mails[len(rec.mails)-1]
	if !strings.Contains(last.body, "withheld by the paging") {
		t.Errorf("the clear mail for an episode whose page was floored does not say so:\n%s", last.body)
	}
	if !strings.Contains(last.body, EventPageSuppressed) {
		t.Errorf("the clear mail does not name the event a reader would grep for:\n%s", last.body)
	}
}

// TestNewEventTypesAreDocumented. The catalog in docs/event-log.md is the only
// place a reader who finds one of these lines can look it up, and an event that
// ships undocumented is one nobody can interpret later. Same guard as
// cmd/pogo's TestInvestigations_EventTypeIsDocumented.
func TestNewEventTypesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "event-log.md"))
	if err != nil {
		t.Skipf("event-log.md not readable from here: %v", err)
	}
	for _, et := range []string{EventEpisodeHeld, EventPageSuppressed} {
		if !strings.Contains(string(doc), et) {
			t.Errorf("%s is emitted but absent from docs/event-log.md", et)
		}
	}
}

// TestHitMail_SaysTheAllClearIsHeld. Damping the mail changes what SILENCE
// means, and a reader who does not know that reads no-news as the fault having
// stopped — or reads the eventual all-clear as a fault that lasted only until
// then. The page has to carry its own new semantics.
func TestHitMail_SaysTheAllClearIsHeld(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	w := build(rec, targets, map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonServerError)})
	w.Check(utc(2, 28, 12))

	body := rec.mails[0].body
	if !strings.Contains(body, "ALL-CLEAR IS HELD") {
		t.Errorf("the opening page does not tell the reader the all-clear is held:\n%s", body)
	}
	if !strings.Contains(body, "60m with nothing failing") {
		t.Errorf("the opening page does not state the hold in force:\n%s", body)
	}
	if !strings.Contains(body, "silence from here is not") {
		t.Errorf("the opening page does not say what silence now means:\n%s", body)
	}
}
