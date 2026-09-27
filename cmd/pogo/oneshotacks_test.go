package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/scheduler"
)

func oneShotNow() time.Time { return time.Date(2026, 8, 13, 9, 0, 0, 0, time.Local) }

func unansweredFixture(id, agent, msg string) scheduler.OneShotOutcome {
	return scheduler.OneShotOutcome{
		Reason:  scheduler.ReasonOneShotUnacked,
		ID:      id,
		Agent:   agent,
		Message: msg,
		Fired:   oneShotNow().Add(-25 * time.Hour),
		Removed: oneShotNow().Add(-time.Hour),
	}
}

// TestOneShotAckLine_OldWriterDoesNotReadAsClean is the reason this renderer is
// its own function rather than a count comparison at the call site.
//
// A pogod built before d71e1e2 removes every one-shot as the retired
// `one_shot_complete`, and the labels this row reads cannot appear at all — so the naive row prints "no unanswered one-shots"
// with total confidence over a fleet where the class is simply unmeasurable.
// That is the mg-afd0 / mg-3141 confusion, and a detector built to close a
// silence must not answer with one.
func TestOneShotAckLine_OldWriterDoesNotReadAsClean(t *testing.T) {
	rep := scheduler.OneShotReport{
		Since:      oneShotNow().Add(-7 * 24 * time.Hour),
		Legacy:     3,
		LegacyLast: oneShotNow().Add(-2 * time.Hour),
	}
	status, detail := oneShotAckLine(rep, nil, oneShotNow())
	if status != "warn" {
		t.Errorf("status = %q, want warn — an unmeasurable class is not a clean one", status)
	}
	if !strings.Contains(detail, "NOT MEASURABLE") {
		t.Errorf("detail = %q, want it to say the class cannot be measured here", detail)
	}
	if !strings.Contains(detail, "one_shot_complete") {
		t.Errorf("detail = %q, want it to name the retired label it found", detail)
	}
	if !strings.Contains(detail, "merge-base --is-ancestor d71e1e2") {
		t.Errorf("detail = %q, want a command that yields a verdict on the running build, not a bare revision", detail)
	}
}

// TestOneShotLegacyNotice_RemedyIsTheAgeOutNotARebuild pins mg-9ac7. The notice
// used to say an unanswered one-shot was invisible "until pogod is rebuilt" and
// offered `curl /version`. The running build already had the fix; the blocker
// was a retired record inside the window, which a redeploy does not remove — so
// the reader who redeployed saw NOT MEASURABLE the next morning and concluded
// the redeploy failed. Both renderers must name the age-out date, disown the
// rebuild as the thing that clears it, give a command with a verdict, and give
// the --since that measures the untouched part of the window today.
func TestOneShotLegacyNotice_RemedyIsTheAgeOutNotARebuild(t *testing.T) {
	now := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	rep := scheduler.OneShotReport{
		Since:      now.Add(-7 * 24 * time.Hour),
		Files:      []string{"/tmp/events.log"},
		Legacy:     198,
		LegacyLast: time.Date(2026, 8, 13, 2, 21, 0, 500e6, time.UTC),
	}
	ageOut := time.Date(2026, 8, 20, 2, 21, 0, 500e6, time.UTC).Local().Format("2006-01-02 15:04 MST")
	_, row := oneShotAckLine(rep, nil, now)
	full := renderOneShotReport(rep, now, false)
	for name, out := range map[string]string{"doctor row": row, "check-oneshots": full} {
		for _, want := range []string{
			ageOut,
			"7-day window",
			"rebuilding pogod does not bring that forward",
			"merge-base --is-ancestor d71e1e2",
			"echo $?",
			// Rounded UP past the fractional second, or [since, until) would
			// keep the very record it is meant to exclude.
			"--since=2026-08-13T02:21:01Z",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s is missing %q:\n%s", name, want, out)
			}
		}
		if strings.Contains(out, "until pogod is rebuilt") {
			t.Errorf("%s still names a rebuild as what clears it:\n%s", name, out)
		}
	}
}

// A fixed window (--until) never slides, so an age-out date would be false.
func TestOneShotLegacyAgeOut_FixedWindowHasNoDate(t *testing.T) {
	rep := scheduler.OneShotReport{
		Since:      oneShotNow().Add(-7 * 24 * time.Hour),
		Until:      oneShotNow().Add(-time.Hour),
		Legacy:     1,
		LegacyLast: oneShotNow().Add(-2 * time.Hour),
	}
	got := oneShotLegacyAgeOut(rep, oneShotNow())
	if !strings.Contains(got, "always contain") || strings.Contains(got, "stops containing") {
		t.Errorf("fixed window: %q", got)
	}
}

// The log's own evidence about the current writer: a labelled outcome after the
// newest retired one says the writer has the fix; none says nothing either way.
func TestOneShotLabelledSince(t *testing.T) {
	rep := scheduler.OneShotReport{Legacy: 1, LegacyLast: oneShotNow().Add(-3 * time.Hour)}
	if got := oneShotLabelledSince(rep); !strings.Contains(got, "says nothing either way") {
		t.Errorf("no labelled outcome: %q — absence must not read as evidence", got)
	}
	rep.Answered = []scheduler.OneShotOutcome{{Reason: scheduler.ReasonOneShotAcked, Removed: oneShotNow().Add(-4 * time.Hour)}}
	if got := oneShotLabelledSince(rep); !strings.Contains(got, "says nothing either way") {
		t.Errorf("labelled outcome OLDER than the retired one: %q — it says nothing about the current writer", got)
	}
	rep.Answered = append(rep.Answered, scheduler.OneShotOutcome{Reason: scheduler.ReasonOneShotAcked, Removed: oneShotNow().Add(-time.Hour)})
	if got := oneShotLabelledSince(rep); !strings.Contains(got, "has the fix") {
		t.Errorf("labelled outcome after the retired one: %q", got)
	}
}

// TestOneShotAckLine_UnreadableLogDoesNotReadAsClean: a run that could not look
// and a run that found nothing are the two readings this row must never merge.
func TestOneShotAckLine_UnreadableLogDoesNotReadAsClean(t *testing.T) {
	status, detail := oneShotAckLine(scheduler.OneShotReport{}, errors.New("permission denied"), oneShotNow())
	if status != "warn" {
		t.Errorf("status = %q, want warn", status)
	}
	if !strings.Contains(detail, "NOT MEASURED") || !strings.Contains(detail, "permission denied") {
		t.Errorf("detail = %q, want it to say it measured nothing and why", detail)
	}
}

// TestOneShotAckLine_NothingRanIsNotNothingMissed. One-shots are rare; most
// windows contain none. "No one-shot ran" and "every one-shot was answered" are
// different facts and a reader deciding whether to keep looking needs the
// difference.
func TestOneShotAckLine_NothingRanIsNotNothingMissed(t *testing.T) {
	rep := scheduler.OneShotReport{Since: oneShotNow().Add(-7 * 24 * time.Hour)}
	status, detail := oneShotAckLine(rep, nil, oneShotNow())
	if status != "pass" {
		t.Errorf("status = %q, want pass", status)
	}
	if !strings.Contains(detail, "no one-shot fired") {
		t.Errorf("detail = %q, want it to say none ran", detail)
	}
	if !strings.Contains(detail, "not the same as nothing missed") {
		t.Errorf("detail = %q, must not let an empty window read as an all-clear", detail)
	}
}

// TestOneShotAckLine_NamesTheMissedObligation. The ticket's own scope note: a
// consumer of this class must say WHICH one-shot and what it was for, not a
// count — the value is entirely in the identity of the missed obligation.
func TestOneShotAckLine_NamesTheMissedObligation(t *testing.T) {
	rep := scheduler.OneShotReport{
		Since: oneShotNow().Add(-7 * 24 * time.Hour),
		Unanswered: []scheduler.OneShotOutcome{
			unansweredFixture("verify-absentwatch-live-mayor", "mayor", "verify the fix is live"),
		},
		Answered: []scheduler.OneShotOutcome{{Reason: scheduler.ReasonOneShotAcked, ID: "other", Agent: "doctor"}},
	}
	status, detail := oneShotAckLine(rep, nil, oneShotNow())
	if status != "warn" {
		t.Errorf("status = %q, want warn", status)
	}
	if !strings.Contains(detail, "verify-absentwatch-live-mayor") {
		t.Errorf("detail = %q, want the one-shot named", detail)
	}
	if !strings.Contains(detail, "mayor") {
		t.Errorf("detail = %q, want the agent it fired into named", detail)
	}
	if !strings.Contains(detail, "check-oneshots") {
		t.Errorf("detail = %q, want it to point at the detail view", detail)
	}
}

// TestOneShotIdentity_GeneratedIDCarriesTheMessage. `pogo schedule --once`
// without an explicit --id produces `sch-<hex>`, which names nothing. For those
// the message is the only thing that says what was missed, so the short form
// must carry it.
func TestOneShotIdentity_GeneratedIDCarriesTheMessage(t *testing.T) {
	got := oneShotIdentity(unansweredFixture("sch-a1b2c3d4", "mayor", "post-redeploy verification owed on mg-7d20"))
	if !strings.Contains(got, "mg-7d20") {
		t.Errorf("identity = %q — a generated id names nothing, so the message must appear", got)
	}
	named := oneShotIdentity(unansweredFixture("verify-absentwatch-live-mayor", "mayor", "post-redeploy verification owed on mg-7d20"))
	if strings.Contains(named, "mg-7d20") {
		t.Errorf("identity = %q — a descriptive id already says it; the row stays scannable", named)
	}
}

// TestOneShotAckLine_EveryVerdictCarriesTheLimits. The limits sentence is
// written once and asserted on every branch for the reason the audit-successors
// row gives: the PASS branch is what a person reads when deciding whether to
// keep looking, and it is the branch limits usually go missing from.
func TestOneShotAckLine_EveryVerdictCarriesTheLimits(t *testing.T) {
	cases := map[string]struct {
		rep scheduler.OneShotReport
		err error
	}{
		"unreadable":  {scheduler.OneShotReport{}, errors.New("boom")},
		"old writer":  {scheduler.OneShotReport{Legacy: 1, LegacyLast: oneShotNow()}, nil},
		"nothing ran": {scheduler.OneShotReport{}, nil},
		"clean": {scheduler.OneShotReport{
			Answered: []scheduler.OneShotOutcome{{ID: "a", Agent: "mayor"}}, Fires: 1,
		}, nil},
		"finding": {scheduler.OneShotReport{
			Unanswered: []scheduler.OneShotOutcome{unansweredFixture("x", "mayor", "")},
		}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, detail := oneShotAckLine(tc.rep, tc.err, oneShotNow())
			if !strings.Contains(detail, oneShotLimits) {
				t.Errorf("verdict %q renders without the limits sentence: %s", name, detail)
			}
		})
	}
}

// TestOneShotAckLine_PendingFiresAreNeitherAnsweredNorMissed. A one-shot inside
// its 24h ack window has no outcome yet; a clean row that silently counted it as
// answered would be asserting something nothing knows — the exact claim mg-64e6
// removed from the fire-time label.
func TestOneShotAckLine_PendingFiresAreNeitherAnsweredNorMissed(t *testing.T) {
	rep := scheduler.OneShotReport{
		Since:    oneShotNow().Add(-7 * 24 * time.Hour),
		Answered: []scheduler.OneShotOutcome{{ID: "a", Agent: "mayor"}},
		Fires:    3,
	}
	_, detail := oneShotAckLine(rep, nil, oneShotNow())
	if !strings.Contains(detail, "still inside") {
		t.Errorf("detail = %q, want the 2 in-flight fires accounted for", detail)
	}
}

// TestDescribeOneShotWindow_SaysWhenTheLogIsShorterThanTheQuestion. A report
// whose oldest record is newer than --since answered a shorter question than it
// was asked, and a row that does not say so overstates its own coverage.
func TestDescribeOneShotWindow_SaysWhenTheLogIsShorterThanTheQuestion(t *testing.T) {
	rep := scheduler.OneShotReport{
		Since:  oneShotNow().Add(-7 * 24 * time.Hour),
		Oldest: oneShotNow().Add(-2 * 24 * time.Hour),
	}
	got := describeOneShotWindow(rep, oneShotNow())
	if !strings.Contains(got, "only reaches back") {
		t.Errorf("window = %q, want it to admit the log is shorter than the window asked for", got)
	}
	rep.Spilled = true
	if got := describeOneShotWindow(rep, oneShotNow()); !strings.Contains(got, "rotation has discarded") {
		t.Errorf("window = %q, want it to distinguish 'the log starts here' from 'the log was cut off here'", got)
	}
}

// TestRenderOneShotReport_PrintsWhatEachWasCarrying is the detail view's
// contract: the row says how many, the command says what each one was for.
func TestRenderOneShotReport_PrintsWhatEachWasCarrying(t *testing.T) {
	rep := scheduler.OneShotReport{
		Since: oneShotNow().Add(-7 * 24 * time.Hour),
		Files: []string{"/tmp/events.log"},
		Unanswered: []scheduler.OneShotOutcome{
			unansweredFixture("revision-check-post-0300", "mayor", "check the running revision carries tonight's deploy"),
		},
	}
	out := renderOneShotReport(rep, oneShotNow(), false)
	for _, want := range []string{
		"revision-check-post-0300",
		"check the running revision",
		"unanswered for 24h",
		"one_shot_unacked",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

// TestRenderOneShotReport_MissingFireTimeIsNamed: a delivery record older than
// the files scanned is a gap in THIS reading, not a fire that did not happen,
// and a blank field would be read as the latter.
func TestRenderOneShotReport_MissingFireTimeIsNamed(t *testing.T) {
	o := unansweredFixture("orphan", "mayor", "")
	o.Fired = time.Time{}
	rep := scheduler.OneShotReport{
		Since: oneShotNow().Add(-7 * 24 * time.Hour), Files: []string{"/tmp/events.log"},
		Unanswered: []scheduler.OneShotOutcome{o},
	}
	out := renderOneShotReport(rep, oneShotNow(), false)
	if !strings.Contains(out, "not found in the scanned log") {
		t.Errorf("report leaves the missing fire time blank:\n%s", out)
	}
}

func TestParseOneShotBound(t *testing.T) {
	def := oneShotNow()
	if got, err := parseOneShotBound("", def); err != nil || !got.Equal(def) {
		t.Errorf("empty: got %v %v, want the default", got, err)
	}
	if got, err := parseOneShotBound("2026-08-01T00:00:00Z", def); err != nil || got.Year() != 2026 || got.Month() != 8 {
		t.Errorf("RFC3339: got %v %v", got, err)
	}
	got, err := parseOneShotBound("48h", def)
	if err != nil {
		t.Fatalf("duration: %v", err)
	}
	if d := time.Since(got); d < 47*time.Hour || d > 49*time.Hour {
		t.Errorf("48h resolved to %s ago, want ~48h", d)
	}
	if _, err := parseOneShotBound("yesterday", def); err == nil {
		t.Error("a bound this command cannot parse must be refused, not silently defaulted")
	}
}
