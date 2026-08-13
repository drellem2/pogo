package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReadOneShotOutcomes_ReadsWhatTheSchedulerWrote drives the REAL scheduler
// through all four one-shot outcomes and reads them back.
//
// It is deliberately end-to-end rather than fixture-driven, and that is the
// point of the test rather than a convenience. mg-64e6 shipped the labels and
// mg-8011 shipped this reader; the failure that would make both look fine while
// the pair is broken is a vocabulary that drifts on one side — a reader matching
// nothing reports "nothing unanswered" exactly as loudly as a healthy fleet
// does. Hand-written fixtures would encode the reader's own beliefs about what
// the writer emits and could not catch that.
func TestReadOneShotOutcomes_ReadsWhatTheSchedulerWrote(t *testing.T) {
	now := fixedTime()

	// Four schedulers, because each outcome needs its own delivery behaviour;
	// they share one events.log so the reader sees them as one stream, which is
	// also how they arrive in production.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")

	// 1. ACKED — the agent redeems the token.
	sAck := newSchedulerAtLog(t, dir, "acked", &recorder{})
	if _, err := sAck.Add(Entry{
		Agent: "mayor", OneShot: true, ID: "verify-after-redeploy",
		Message:  "Verify the absentwatch fix is live, then reply on mg-7d20.",
		NextFire: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	sAck.Tick(context.Background(), now.Add(2*time.Minute))
	live := sAck.List("mayor")
	if len(live) != 1 {
		t.Fatalf("want the fired one-shot retained for its ack, got %d", len(live))
	}
	if _, err := sAck.Ack("mayor", "verify-after-redeploy", live[0].PendingToken, now.Add(20*time.Minute)); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	// 2. UNACKED — nobody ever answers, and it is reaped at AckStaleWindow.
	sUnacked := newSchedulerAtLog(t, dir, "unacked", &recorder{})
	if _, err := sUnacked.Add(Entry{
		Agent: "crew-doctor", OneShot: true, ID: "revision-check-post-0300",
		Message:  "Check the running revision carries tonight's deploy.",
		NextFire: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	sUnacked.Tick(context.Background(), now.Add(2*time.Minute))
	sUnacked.Tick(context.Background(), now.Add(2*time.Minute).Add(AckStaleWindow+time.Minute))

	// 3. UNDELIVERED — the fire never reached anyone.
	sBroken := newSchedulerAtLog(t, dir, "undelivered", &recorder{failNth: 1})
	if _, err := sBroken.Add(Entry{
		Agent: "cat-gone", OneShot: true, ID: "predeploy-step",
		Message: "Stop the fleet before the rebuild.", NextFire: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	sBroken.Tick(context.Background(), now.Add(2*time.Minute))

	// 4. SKIPPED — the replay policy elides a stale fire.
	sSkip := newSchedulerAtLog(t, dir, "skipped", &recorder{})
	sSkip.SkipWindow = time.Minute
	if _, err := sSkip.Add(Entry{
		Agent: "crew-research", OneShot: true, ID: "stale-once",
		NextFire: now.Add(time.Minute), ReplayPolicy: ReplaySkip,
	}, now); err != nil {
		t.Fatal(err)
	}
	sSkip.Tick(context.Background(), now.Add(time.Hour))

	rep, err := ReadOneShotOutcomes(logPath, now.Add(-time.Hour), time.Time{})
	if err != nil {
		t.Fatalf("ReadOneShotOutcomes: %v", err)
	}

	if len(rep.Unanswered) != 2 {
		t.Fatalf("want 2 unanswered (unacked + undelivered), got %d: %+v", len(rep.Unanswered), rep.Unanswered)
	}
	byID := map[string]OneShotOutcome{}
	for _, o := range rep.Unanswered {
		byID[o.ID] = o
	}

	unacked, ok := byID["revision-check-post-0300"]
	if !ok {
		t.Fatal("the reaped one-shot is not in the finding")
	}
	if unacked.Reason != ReasonOneShotUnacked {
		t.Errorf("reason: want %s, got %s", ReasonOneShotUnacked, unacked.Reason)
	}
	if unacked.Agent != "crew-doctor" {
		t.Errorf("agent: want crew-doctor, got %q", unacked.Agent)
	}
	// Identity is the whole value of this class: a row that cannot say what was
	// missed is a count wearing a name.
	if !strings.Contains(unacked.Message, "running revision") {
		t.Errorf("the unanswered obligation does not say what it was for: %q", unacked.Message)
	}
	// The fire is a day older than the reap, so a windowed join would have lost
	// it and reported a zero wait.
	if unacked.Fired.IsZero() {
		t.Error("no fire time joined onto the reap — the delivery record was not found")
	}
	if w := unacked.Waited(); w < AckStaleWindow {
		t.Errorf("waited: want at least %s, got %s", AckStaleWindow, w)
	}

	undelivered, ok := byID["predeploy-step"]
	if !ok {
		t.Fatal("the undelivered one-shot is not in the finding")
	}
	if undelivered.Reason != ReasonOneShotUndelivered {
		t.Errorf("reason: want %s, got %s", ReasonOneShotUndelivered, undelivered.Reason)
	}

	if len(rep.Answered) != 1 || rep.Answered[0].ID != "verify-after-redeploy" {
		t.Errorf("want the acked one-shot named on the answered side, got %+v", rep.Answered)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].ID != "stale-once" {
		t.Errorf("want the skipped fire kept apart from the finding, got %+v", rep.Skipped)
	}
	if rep.WriterPredatesLabels() {
		t.Error("a log written by THIS binary was reported as written by an older one")
	}
}

// TestReadOneShotOutcomes_RetiredLabelIsReportedNotIgnored is the guard against
// this reader joining the confusion class it was written under (mg-afd0,
// mg-3141): d71e1e2 is inert until pogod is rebuilt, so on a box running an
// older daemon every one-shot leaves as `one_shot_complete` and the four labels
// this reads cannot appear at all. Reporting that as "nothing unanswered" is a
// confident falsehood, and the whole point of the ticket is that a silence
// nobody can distinguish from health is the defect.
func TestReadOneShotOutcomes_RetiredLabelIsReportedNotIgnored(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	now := fixedTime()

	writeRemovalLine(t, logPath, now.Add(-2*time.Hour), ReasonOneShotComplete, "verify-absentwatch-live-mayor", "mayor")
	writeRemovalLine(t, logPath, now.Add(-time.Hour), ReasonOneShotComplete, "revision-check-post-0300", "mayor")

	rep, err := ReadOneShotOutcomes(logPath, now.Add(-24*time.Hour), time.Time{})
	if err != nil {
		t.Fatalf("ReadOneShotOutcomes: %v", err)
	}
	if len(rep.Unanswered) != 0 {
		t.Fatalf("a retired-label record is not a finding, got %+v", rep.Unanswered)
	}
	if !rep.WriterPredatesLabels() {
		t.Fatal("two `one_shot_complete` records in the window and the report claims the writer can emit the new labels")
	}
	if rep.Legacy != 2 {
		t.Errorf("legacy count: want 2, got %d", rep.Legacy)
	}
	if !rep.LegacyLast.Equal(now.Add(-time.Hour)) {
		t.Errorf("legacy last: want the NEWEST of them (%s), got %s", now.Add(-time.Hour), rep.LegacyLast)
	}
}

// TestReadOneShotOutcomes_IgnoresRecurringSchedules: every removal reason in the
// vocabulary is shared with recurring schedules except these four, and a
// recurring schedule's removal is not a missed obligation. The `one_shot` detail
// is the discriminator.
func TestReadOneShotOutcomes_IgnoresRecurringSchedules(t *testing.T) {
	dir := t.TempDir()
	now := fixedTime()
	s := newSchedulerAtLog(t, dir, "recurring", &recorder{})
	if _, err := s.Add(Entry{Agent: "crew-doctor", Cron: "*/10 * * * *", ID: "sweep-morning-doctor"}, now); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Remove("crew-doctor", "sweep-morning-doctor"); err != nil || !ok {
		t.Fatalf("Remove: %v %v", ok, err)
	}
	rep, err := ReadOneShotOutcomes(filepath.Join(dir, "events.log"), now.Add(-time.Hour), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total() != 0 || rep.Fires != 0 {
		t.Errorf("a recurring schedule's removal reached the one-shot report: %+v", rep)
	}
}

// TestReadOneShotOutcomes_OtherRemovalsAreNotFindings: a one-shot a human
// removed on purpose, or one dropped because its agent went away, is not an
// obligation nobody answered. Reporting those would bury the two rows that mean
// something under the ones that do not.
func TestReadOneShotOutcomes_OtherRemovalsAreNotFindings(t *testing.T) {
	dir := t.TempDir()
	now := fixedTime()
	s := newSchedulerAtLog(t, dir, "explicit", &recorder{})
	if _, err := s.Add(Entry{
		Agent: "crew-doctor", OneShot: true, ID: "changed-my-mind", NextFire: now.Add(time.Hour),
	}, now); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Remove("crew-doctor", "changed-my-mind"); err != nil || !ok {
		t.Fatalf("Remove: %v %v", ok, err)
	}
	rep, err := ReadOneShotOutcomes(filepath.Join(dir, "events.log"), now.Add(-time.Hour), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unanswered) != 0 {
		t.Errorf("an explicitly removed one-shot was reported as unanswered: %+v", rep.Unanswered)
	}
}

// TestReadOneShotOutcomes_WindowExcludesOlderRemovals pins that --since scopes
// the finding, and that a fire OUTSIDE the window still joins onto a removal
// inside it.
func TestReadOneShotOutcomes_WindowExcludesOlderRemovals(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	now := fixedTime()

	writeRemovalLine(t, logPath, now.Add(-72*time.Hour), ReasonOneShotUnacked, "ancient", "mayor")
	writeRemovalLine(t, logPath, now.Add(-2*time.Hour), ReasonOneShotUnacked, "recent", "mayor")

	rep, err := ReadOneShotOutcomes(logPath, now.Add(-24*time.Hour), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unanswered) != 1 || rep.Unanswered[0].ID != "recent" {
		t.Fatalf("want only the in-window removal, got %+v", rep.Unanswered)
	}

	// And an --until bound excludes the newer one.
	rep, err = ReadOneShotOutcomes(logPath, now.Add(-96*time.Hour), now.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unanswered) != 1 || rep.Unanswered[0].ID != "ancient" {
		t.Fatalf("--until did not bound the window: %+v", rep.Unanswered)
	}
}

// TestReadOneShotOutcomes_UnreadableLogIsNotAnEmptyMeasurement. A run that could
// not look must not be indistinguishable from a run that found nothing — that
// equivalence is the shape of the original defect.
func TestReadOneShotOutcomes_UnreadableLogIsNotAnEmptyMeasurement(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	if err := os.WriteFile(logPath, []byte("{}\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0000 file is still readable")
	}
	if _, err := ReadOneShotOutcomes(logPath, fixedTime().Add(-time.Hour), time.Time{}); err == nil {
		t.Fatal("an unreadable events.log returned a clean report")
	}
}

// TestReadOneShotOutcomes_AbsentLogIsNotAQuietWeek is the spot where THIS
// reader could most easily commit the defect it was written to close.
//
// events.ScanFile treats a missing file as "no events yet" and returns
// (nil, nil), so an unresolvable POGO_HOME or a renamed log would otherwise
// produce a confident "no one-shot fired or was reaped" from a run that opened
// nothing — a silence indistinguishable from health, which is the whole
// complaint of the ticket, one level up.
func TestReadOneShotOutcomes_AbsentLogIsNotAQuietWeek(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nowhere", "events.log")
	if _, err := ReadOneShotOutcomes(missing, fixedTime().Add(-time.Hour), time.Time{}); err == nil {
		t.Error("a log that is not there returned a clean report")
	}
	if _, err := ReadOneShotOutcomes("", fixedTime().Add(-time.Hour), time.Time{}); err == nil {
		t.Error("an unresolvable log path returned a clean report")
	}
}

// TestOneShotRemovalRecordCarriesItsIdentity pins the writer half: the removal
// record is the ONLY surviving trace of a one-shot (the entry is gone from
// schedules.json by then), so if it does not carry what the one-shot was for,
// no reader can ever say.
func TestOneShotRemovalRecordCarriesItsIdentity(t *testing.T) {
	dir := t.TempDir()
	now := fixedTime()
	s := newSchedulerAtLog(t, dir, "identity", &recorder{})
	if _, err := s.Add(Entry{
		Agent: "mayor", OneShot: true, ID: "sch-deadbeef",
		Message:  "Post-redeploy verification owed on mg-7d20:\nconfirm absentwatch is live.",
		NextFire: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background(), now.Add(2*time.Minute))
	s.Tick(context.Background(), now.Add(2*time.Minute).Add(AckStaleWindow+time.Minute))

	ev := findScheduleRemoved(t, filepath.Join(dir, "events.log"), "sch-deadbeef", ReasonOneShotUnacked)
	if ev == nil {
		t.Fatal("no unacked removal record")
	}
	d, _ := ev["details"].(map[string]any)
	msg, _ := d["message"].(string)
	if !strings.Contains(msg, "mg-7d20") {
		t.Errorf("removal record does not carry what the one-shot was for: %q", msg)
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("message digest carries a raw newline, which mis-splits a line-oriented reader: %q", msg)
	}
	if kind, _ := d["kind"].(string); kind == "" {
		t.Error("removal record carries no kind")
	}
}

// TestRecurringRemovalDoesNotCarryTheMessage: the identity fields are for
// one-shots. A recurring schedule's message is boilerplate repeated on every
// removal (2138 `agent_gone` records on the box this was written against), and
// its id plus cron already identify it.
func TestRecurringRemovalDoesNotCarryTheMessage(t *testing.T) {
	dir := t.TempDir()
	now := fixedTime()
	s := newSchedulerAtLog(t, dir, "recurring-msg", &recorder{})
	if _, err := s.Add(Entry{
		Agent: "crew-doctor", Cron: "*/10 * * * *", ID: "sweep-morning-doctor",
		Message: "Run the morning sweep and report anything held.",
	}, now); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Remove("crew-doctor", "sweep-morning-doctor"); err != nil || !ok {
		t.Fatalf("Remove: %v %v", ok, err)
	}
	ev := findScheduleRemoved(t, filepath.Join(dir, "events.log"), "sweep-morning-doctor", "explicit_rm")
	if ev == nil {
		t.Fatal("no removal record")
	}
	d, _ := ev["details"].(map[string]any)
	if _, present := d["message"]; present {
		t.Error("a recurring schedule's removal record carries its message")
	}
}

func TestDigestMessage(t *testing.T) {
	if got := digestMessage(""); got != "" {
		t.Errorf("empty message: got %q", got)
	}
	if got := digestMessage("one\ntwo   three\t\tfour\n"); got != "one two three four" {
		t.Errorf("whitespace collapse: got %q", got)
	}
	// Rune-aware truncation: cutting mid-sequence would put invalid UTF-8 into
	// a log every consumer parses as JSON.
	long := strings.Repeat("é", oneShotMessageDigestLimit+50)
	got := digestMessage(long)
	if r := []rune(got); len(r) != oneShotMessageDigestLimit+1 {
		t.Errorf("truncated to %d runes, want %d + ellipsis", len(r), oneShotMessageDigestLimit)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncation is not marked: %q", got)
	}
	for _, r := range got {
		if r == '�' {
			t.Fatalf("truncation produced invalid UTF-8: %q", got)
		}
	}
}

// TestScanFilesCoveringStopsAtTheWindow: the live log is ~86MB on the box this
// was written against and a full parse costs ~0.8s, so a reader that always
// walked all six retained chunks would put seconds into a health check that
// mostly wants a week. Rotation only ever discards the OLDEST chunk, so the
// retained files hold a contiguous suffix of history and stopping at the first
// file that BEGINS at or before the floor is exact rather than a guess — every
// older file ends before that one starts.
func TestScanFilesCoveringStopsAtTheWindow(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	now := fixedTime()

	// .2 is the oldest chunk, then .1, then the live file, each beginning where
	// the previous one left off.
	writeRemovalLine(t, logPath+".2", now.Add(-80*24*time.Hour), ReasonOneShotUnacked, "oldest", "mayor")
	writeRemovalLine(t, logPath+".1", now.Add(-40*24*time.Hour), ReasonOneShotUnacked, "middle", "mayor")
	writeRemovalLine(t, logPath, now.Add(-10*24*time.Hour), ReasonOneShotUnacked, "newest", "mayor")

	// A week-wide window is entirely inside the live file, which begins before
	// it: nothing older can hold a record in range.
	if got := scanFilesCovering(logPath, now.Add(-7*24*time.Hour)); len(got) != 1 || got[0] != logPath {
		t.Errorf("week window read %v, want just the live log", got)
	}
	// A 20-day window reaches back past the live file's first record, so the
	// chunk that CONTAINS that instant must be read too — dropping it would
	// silently shorten the window the report claims to have covered.
	got := scanFilesCovering(logPath, now.Add(-20*24*time.Hour))
	if len(got) != 2 || got[0] != logPath+".1" || got[1] != logPath {
		t.Errorf("20-day window read %v, want [.1, live]", got)
	}
	// A window older than every retained record reads everything there is.
	if got := scanFilesCovering(logPath, now.Add(-365*24*time.Hour)); len(got) != 3 {
		t.Errorf("year window read %v, want all three", got)
	}
}

// newSchedulerAtLog builds a scheduler whose state file is its own but whose
// events.log is dir/events.log — so several of them write one stream, as pogod's
// single scheduler does in production.
func newSchedulerAtLog(t *testing.T, dir, name string, deliverer Deliverer) *Scheduler {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(filepath.Join(sub, "schedules.json"), deliverer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.logPath = filepath.Join(dir, "events.log")
	return s
}

// writeRemovalLine appends a hand-built schedule_removed record. Used only for
// the cases the live scheduler cannot produce — the RETIRED label, and records
// dated days apart — never for the four current ones, which are driven through
// the real emit path so a vocabulary drift fails the test.
func writeRemovalLine(t *testing.T, path string, at time.Time, reason, id, agent string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	line := `{"schema_version":1,"timestamp":"` + at.UTC().Format(time.RFC3339Nano) +
		`","event_type":"schedule_removed","agent":"pogod","details":{"one_shot":true,"reason":"` + reason +
		`","schedule_id":"` + id + `","to":"` + agent + `","removed_at":"` + at.Format(time.RFC3339) + `"}}` + "\n"
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}
