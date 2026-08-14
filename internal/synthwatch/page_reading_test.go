package synthwatch

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/synthfail"
)

// mg-c058. At 02:28:12Z on 2026-08-14 this watcher paged `human` with
//
//	AGENTS ARE FAILING EVERY TURN — mayor (server_error)
//
// off two errors in a 30-minute trailing window. The underlying fault was real
// and ongoing — intermittent github.com unreachability from at least 01:18Z to
// 03:16Z over both HTTPS/DNS and SSH/22 — so this was not a false alarm. What
// was false was the scope and the attribution: not every turn, and mayor was
// completing turns throughout (it ran the query that found this). A reader sent
// after a persistent per-agent cause by that wording spent nine days on a
// credential question the credential was never the cause of.
//
// These tests pin the reading, which is the whole fix. None of them constrain
// WHETHER pogod pages: the fault was real, and delaying this page would have
// been the wrong remedy.

// blip is the incident's own report: two server errors, nine minutes apart,
// inside a 30-minute window.
func blip() synthfail.Report {
	return synthfail.Report{
		State:         synthfail.StateFailing,
		Reason:        synthfail.ReasonServerError,
		Count:         2,
		First:         time.Date(2026, 8, 14, 2, 24, 50, 0, time.UTC),
		Last:          time.Date(2026, 8, 14, 2, 33, 27, 0, time.UTC),
		Detail:        "API Error: Can't reach the API server — check your internet or DNS (ENOTFOUND)",
		WindowSeconds: 1800,
		ScannedAt:     time.Date(2026, 8, 14, 2, 28, 12, 0, time.UTC),
	}
}

func pageAt(t *testing.T, rep synthfail.Report, sent time.Time) mail {
	t.Helper()
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	w := build(rec, targets, map[string]synthfail.Report{"/w/mayor": rep})
	w.Check(sent)
	if len(rec.mails) != 1 {
		t.Fatalf("sent %d mails, want 1", len(rec.mails))
	}
	return rec.mails[0]
}

// The subject is the part that travels: it is what the deadman notifier prints
// and what a human reads on a phone at 2:44am. It must state what was measured.
func TestHitMail_SubjectStatesTheCountAndTheWindow(t *testing.T) {
	m := pageAt(t, blip(), time.Date(2026, 8, 14, 2, 28, 12, 0, time.UTC))

	if strings.Contains(m.subject, "EVERY TURN") {
		t.Errorf("subject = %q still claims a rate the detector never measured; the threshold is 2 turns in a 30m window", m.subject)
	}
	for _, part := range []string{"mayor", "server_error", "2 errors", "30m", "02:24:50Z", "02:33:27Z"} {
		if !strings.Contains(m.subject, part) {
			t.Errorf("subject = %q, missing %q", m.subject, part)
		}
	}
}

// The founding case must get LOUDER under the new wording, not quieter — the
// change is about accuracy, and 143 errors is a worse fact than 2.
func TestHitMail_FoundingCaseStillReadsAsACatastrophe(t *testing.T) {
	rep := synthfail.Report{
		State:         synthfail.StateFailing,
		Reason:        synthfail.ReasonAuthFailed,
		Count:         143,
		First:         time.Date(2026, 7, 21, 23, 10, 26, 0, time.UTC),
		Last:          time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		Detail:        "Login expired · Please run /login",
		WindowSeconds: 1800,
	}
	m := pageAt(t, rep, time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(m.subject, "143 errors") {
		t.Errorf("subject = %q, want the 143-turn count front and centre", m.subject)
	}
	if !strings.Contains(m.subject, "auth_failed") {
		t.Errorf("subject = %q, want the reason", m.subject)
	}
}

// A page is read minutes or hours after it is sent — the 2026-08-14 one was
// noticed by the delivering daemon 16m26s later. A relative age would be wrong
// by the time anyone saw it, which is this ticket's defect one layer out.
func TestHitMail_IsAbsoluteAndDatesItself(t *testing.T) {
	sent := time.Date(2026, 8, 14, 2, 28, 12, 0, time.UTC)
	m := pageAt(t, blip(), sent)

	if !strings.Contains(m.body, "Sent 2026-08-14T02:28:12Z") {
		t.Errorf("body does not state its own send time; a reader dates an undated page to when they read it:\n%s", m.body)
	}
	for _, s := range []string{m.subject, m.body} {
		if strings.Contains(s, " ago") {
			t.Errorf("page carries a relative age, which decays in the mailbox:\n%s", s)
		}
	}
}

// The two claims that misled, stated as their negations.
func TestHitMail_SaysTheCountIsNotARateAndTheWindowIsNotTheFault(t *testing.T) {
	m := pageAt(t, blip(), time.Date(2026, 8, 14, 2, 28, 12, 0, time.UTC))

	if !strings.Contains(m.body, "NOT A RATE") {
		t.Errorf("body does not say the count is not a rate — mayor was completing turns while flagged:\n%s", m.body)
	}
	if !strings.Contains(m.body, "narrower") {
		t.Errorf("body does not warn that the window can be narrower than the fault; two readers concluded a nine-minute blip from a two-hour outage:\n%s", m.body)
	}
	if !strings.Contains(m.body, "30m") && !strings.Contains(m.body, "30M") {
		t.Errorf("body does not name the trailing window the count was taken over:\n%s", m.body)
	}
}

// server_error is the reason whose prognosis is opposite to the ones the mayor
// and doctor prompts enumerate. Read as one of those, it points at a
// credential — which is what parked mg-fb29 on `human` for nine days.
func TestHitMail_ServerErrorIsNotReportedAsACredentialProblem(t *testing.T) {
	m := pageAt(t, blip(), time.Date(2026, 8, 14, 2, 28, 12, 0, time.UTC))
	if !strings.Contains(m.body, "not a credential") {
		t.Errorf("a server_error page does not rule out the credential:\n%s", m.body)
	}
}

func TestHitMail_AuthFailureDoesNotCarryTheServerErrorNote(t *testing.T) {
	rep := blip()
	rep.Reason = synthfail.ReasonAuthFailed
	m := pageAt(t, rep, time.Date(2026, 8, 14, 2, 28, 12, 0, time.UTC))
	if strings.Contains(m.body, "not a credential") {
		t.Errorf("an auth_failed page tells the reader it is not a credential:\n%s", m.body)
	}
}

// The close fires on a QUIET transcript. Quiet is what an idle agent writes and
// what an intermittent fault looks like between recurrences — this alarm
// announced a clear at 03:22Z and re-opened at 03:24Z against a fault that ran
// until at least 03:16Z. The mail must state what it measured, not what a
// reader would like it to mean.
func TestClearMail_ClaimsTheWindowItMeasuredAndNotRecovery(t *testing.T) {
	rec := &recorder{}
	verdicts := map[string]synthfail.Report{"/w/mayor": blip()}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	w := build(rec, targets, verdicts)
	w.Check(time.Date(2026, 8, 14, 2, 28, 12, 0, time.UTC))
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet, WindowSeconds: 1800}
	// 03:22:09Z is the real clear from that night. Since mg-70f3 it only STARTS
	// the quiet hold — the mail goes at 04:22:09Z if nothing has failed since.
	// (That night something did, at 03:24:38Z; see TestHold_* in synthwatch_test.go.)
	w.Check(time.Date(2026, 8, 14, 3, 22, 9, 0, time.UTC))
	w.Check(time.Date(2026, 8, 14, 4, 22, 9, 0, time.UTC))

	if len(rec.mails) != 2 {
		t.Fatalf("sent %d mails, want 2 (open + close)", len(rec.mails))
	}
	m := rec.mails[1]
	if strings.Contains(m.subject, "producing real turns again") {
		t.Errorf("clear subject = %q asserts recovery; the close fires on a quiet transcript, which an idle agent also produces", m.subject)
	}
	if !strings.Contains(m.subject, "quiet 60m") {
		t.Errorf("clear subject = %q does not state the continuous quiet it measured", m.subject)
	}
	if !strings.Contains(m.body, "trailing 30m window") {
		t.Errorf("clear body does not state the scan window behind the readings:\n%s", m.body)
	}
	if !strings.Contains(m.body, "LESS THAN") {
		t.Errorf("clear body does not distinguish its measurement from \"the fault is over\":\n%s", m.body)
	}
	// Every probe run during that outage came back healthy, because probes land
	// in good minutes. Recovery needs a quiet PERIOD across instruments.
	if !strings.Contains(m.body, "instrument failures") {
		t.Errorf("clear body does not say what establishing recovery actually takes:\n%s", m.body)
	}
}

// The window has to reach the event log too: a later reader reconstructing the
// night from ~/.pogo/events.log has only these fields.
func TestDetectedEvent_CarriesTheWindowItCounted(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	w := build(rec, targets, map[string]synthfail.Report{"/w/mayor": blip()})
	w.Check(time.Date(2026, 8, 14, 2, 28, 12, 0, time.UTC))

	evs := rec.eventsOfType(EventDetected)
	if len(evs) != 1 {
		t.Fatalf("emitted %d %s events, want 1", len(evs), EventDetected)
	}
	if got := evs[0].Details["window_seconds"]; got != 1800 {
		t.Errorf("details.window_seconds = %v, want 1800 — without it a reader supplies their own window for the count", got)
	}
	if got := evs[0].Details["failing_turns"]; got != 2 {
		t.Errorf("details.failing_turns = %v, want 2", got)
	}
}

// The watcher must state the window the SCAN used, never a constant of its own.
func TestWindow_FollowsTheConfiguredScanWindow(t *testing.T) {
	w := New(Options{ScanOptions: synthfail.Options{Window: 90 * time.Minute}})
	if got := w.window(); got != 90*time.Minute {
		t.Errorf("window() = %v, want the configured 90m", got)
	}
	if got := New(Options{}).window(); got != synthfail.DefaultWindow {
		t.Errorf("window() = %v, want synthfail.DefaultWindow %v", got, synthfail.DefaultWindow)
	}
}
