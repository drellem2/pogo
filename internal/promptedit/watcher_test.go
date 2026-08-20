package promptedit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// recorder is the mail seam substituted for client.SendMGMail. Without it a
// test run shells out to the real `mg` and mails a live crew agent — a
// manufactured fleet alarm, which is the same class of fault as writing test
// events onto the real spine.
type recorder struct {
	mu   sync.Mutex
	sent []sentMail
	fail error
}

type sentMail struct{ to, from, subject, body string }

func (r *recorder) send(to, from, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.sent = append(r.sent, sentMail{to, from, subject, body})
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (r *recorder) last() sentMail {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent[len(r.sent)-1]
}

type eventLog struct {
	mu     sync.Mutex
	events []events.Event
}

func (e *eventLog) emit(ev events.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
}

func (e *eventLog) ofType(t string) []events.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []events.Event
	for _, ev := range e.events {
		if ev.EventType == t {
			out = append(out, ev)
		}
	}
	return out
}

// fixture builds a corpus root and a watcher over it. shippedFS is a MapFS
// shaped like the real embed so LoadShipped has something to enumerate.
func fixture(t *testing.T, rec *recorder, log *eventLog) (root string, w *Watcher) {
	t.Helper()
	root = t.TempDir()
	state := filepath.Join(t.TempDir(), NoticesFile)
	w = New(Options{
		Enabled:       true,
		Root:          root,
		ShippedFS:     shippedFixtureFS(),
		Coordinator:   "mayor",
		Mail:          rec.send,
		Emit:          log.emit,
		Interval:      time.Hour,
		RenotifyAfter: 72 * time.Hour,
		StatePath:     state,
	})
	return root, w
}

// TestWatcher_MailsTheAffectedAgentOnceThenGoesQuiet is the notification
// policy. Mailing every sweep is how a real alarm gets filtered out — the
// failure mode this whole detector exists to catch, one level up.
func TestWatcher_MailsTheAffectedAgentOnceThenGoesQuiet(t *testing.T) {
	rec, log := &recorder{}, &eventLog{}
	root, w := fixture(t, rec, log)
	write(t, root, "crew/doctor.md", stampedRecording("# doctor\n", "# doctor\nedited\n"))

	now := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	if _, err := w.Sample(now); err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("want 1 mail on the transition into the condition, got %d", rec.count())
	}
	if got := rec.last().to; got != "doctor" {
		t.Errorf("mailed %q, want the agent that owns the prompt (doctor)", got)
	}
	if got := rec.last().from; got != mailFrom {
		t.Errorf("from = %q, want %q so a recipient can tell this from a declined-sync notice", got, mailFrom)
	}

	// Unchanged finding, well inside the renotify window: silent.
	if _, err := w.Sample(now.Add(24 * time.Hour)); err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("an unchanged finding must stay quiet inside the renotify window, got %d mails", rec.count())
	}

	// Past the window: raised again, because a finding nobody actioned must not
	// fall permanently silent.
	if _, err := w.Sample(now.Add(73 * time.Hour)); err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if rec.count() != 2 {
		t.Fatalf("want a reminder past the renotify window, got %d mails", rec.count())
	}
}

// TestWatcher_ReNotifiesWhenTheEditGROWS. The suppression key is the body hash,
// not merely "we told them about this file": a further edit means the state
// they were told about is not the state they have.
func TestWatcher_ReNotifiesWhenTheEditGrows(t *testing.T) {
	rec, log := &recorder{}, &eventLog{}
	root, w := fixture(t, rec, log)
	write(t, root, "crew/doctor.md", stampedRecording("# doctor\n", "# doctor\nedit one\n"))

	now := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	mustSample(t, w, now)
	if rec.count() != 1 {
		t.Fatalf("want 1 mail, got %d", rec.count())
	}
	write(t, root, "crew/doctor.md", stampedRecording("# doctor\n", "# doctor\nedit one\nedit two\n"))
	mustSample(t, w, now.Add(time.Minute))
	if rec.count() != 2 {
		t.Fatalf("a changed body must re-notify inside the quiet window, got %d mails", rec.count())
	}
}

// TestWatcher_ForgetsAResolvedEdit. A path that stops reading as edited is
// dropped from the store, so a recurrence is news again rather than inheriting
// a suppression window from a resolved incident.
func TestWatcher_ForgetsAResolvedEdit(t *testing.T) {
	rec, log := &recorder{}, &eventLog{}
	root, w := fixture(t, rec, log)
	write(t, root, "crew/doctor.md", stampedRecording("# doctor\n", "# doctor\nedited\n"))

	now := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	mustSample(t, w, now)
	// Reconciled: the file matches its stamp again.
	write(t, root, "crew/doctor.md", stamped("# doctor\n"))
	mustSample(t, w, now.Add(time.Hour))
	if rec.count() != 1 {
		t.Fatalf("a clean sweep must not mail, got %d", rec.count())
	}
	// It comes back. Inside the renotify window — but this is news, not a
	// continuation.
	write(t, root, "crew/doctor.md", stampedRecording("# doctor\n", "# doctor\nedited again\n"))
	mustSample(t, w, now.Add(2*time.Hour))
	if rec.count() != 2 {
		t.Fatalf("a recurrence must mail immediately rather than inherit the resolved incident's "+
			"suppression window, got %d mails", rec.count())
	}
}

// TestWatcher_FailedMailIsNotRememberedAsDelivered. A notifier that records a
// failed send as done never retries, and the alarm dies silently — this
// detector's own failure mode, one level up.
func TestWatcher_FailedMailIsNotRememberedAsDelivered(t *testing.T) {
	rec, log := &recorder{fail: errors.New("no such mailbox")}, &eventLog{}
	root, w := fixture(t, rec, log)
	write(t, root, "crew/doctor.md", stampedRecording("# doctor\n", "# doctor\nedited\n"))

	now := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	mustSample(t, w, now)
	if rec.count() != 0 {
		t.Fatalf("the send failed; nothing should be recorded as sent")
	}
	fired := log.ofType(firedEvent)
	if len(fired) != 1 || fired[0].Details["mail_error"] == nil {
		t.Fatalf("a failed send must be on the event spine with its error: %+v", fired)
	}

	rec.mu.Lock()
	rec.fail = nil
	rec.mu.Unlock()
	// Immediately, well inside the renotify window: it must try again.
	mustSample(t, w, now.Add(time.Minute))
	if rec.count() != 1 {
		t.Fatalf("a failed send must be retried on the next sweep, got %d mails", rec.count())
	}
}

// TestWatcher_StatePersistsAcrossRestart. The tick here is the heartbeat, not
// the process restart, so in-memory state would re-mail every finding at every
// pogod restart — roughly daily on this host.
func TestWatcher_StatePersistsAcrossRestart(t *testing.T) {
	rec, log := &recorder{}, &eventLog{}
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), NoticesFile)
	write(t, root, "crew/doctor.md", stampedRecording("# doctor\n", "# doctor\nedited\n"))

	build := func() *Watcher {
		return New(Options{
			Enabled: true, Root: root, ShippedFS: shippedFixtureFS(), Coordinator: "mayor",
			Mail: rec.send, Emit: log.emit, Interval: time.Hour,
			RenotifyAfter: 72 * time.Hour, StatePath: state,
		})
	}
	now := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	mustSample(t, build(), now)
	if rec.count() != 1 {
		t.Fatalf("want 1 mail, got %d", rec.count())
	}
	// A fresh Watcher over the same store: the restart must not reset the clock.
	mustSample(t, build(), now.Add(time.Hour))
	if rec.count() != 1 {
		t.Fatalf("a restart must not re-announce a finding already delivered, got %d mails", rec.count())
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("the suppression store was not written: %v", err)
	}
}

// TestWatcher_UnreadableStoreReAnnouncesRatherThanGoingQuiet. The bias on a
// corrupt store is deliberately toward noise: at worst a duplicate mail. The
// opposite bias would let one bad file silently disable the alarm.
func TestWatcher_UnreadableStoreReAnnouncesRatherThanGoingQuiet(t *testing.T) {
	rec, log := &recorder{}, &eventLog{}
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), NoticesFile)
	write(t, root, "crew/doctor.md", stampedRecording("# doctor\n", "# doctor\nedited\n"))
	build := func() *Watcher {
		return New(Options{
			Enabled: true, Root: root, ShippedFS: shippedFixtureFS(), Coordinator: "mayor",
			Mail: rec.send, Emit: log.emit, Interval: time.Hour,
			RenotifyAfter: 72 * time.Hour, StatePath: state,
		})
	}
	now := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	mustSample(t, build(), now)
	if err := os.WriteFile(state, []byte("{not json"), 0644); err != nil {
		t.Fatalf("corrupt the store: %v", err)
	}
	mustSample(t, build(), now.Add(time.Minute))
	if rec.count() != 2 {
		t.Fatalf("an unreadable store must make the notifier forget and re-announce, got %d mails", rec.count())
	}
}

// TestWatcher_EmitsRanEventOnEveryPassIncludingCleanOnes. "The detector ran and
// found nothing" and "the detector has not run since the last restart" are the
// two states this whole lineage keeps confusing, and an absence cannot tell
// them apart.
func TestWatcher_EmitsRanEventOnEveryPassIncludingCleanOnes(t *testing.T) {
	rec, log := &recorder{}, &eventLog{}
	root, w := fixture(t, rec, log)
	write(t, root, "crew/doctor.md", stamped("# doctor\n"))
	write(t, root, "crew/architect.md", "# local\n")

	now := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	mustSample(t, w, now)
	ran := log.ofType(ranEvent)
	if len(ran) != 1 {
		t.Fatalf("want a ran event on a clean pass, got %d", len(ran))
	}
	d := ran[0].Details
	// The denominators travel with it. A domain that has quietly collapsed to
	// zero judged files must be visible as a number, not as a run of clean
	// reports.
	for _, key := range []string{"enumerated", "shipped_paths", "judged", "findings", "clean",
		"out_of_domain", "stamp_missing", "upstream_withdrawn", "no_upstream"} {
		if _, ok := d[key]; !ok {
			t.Errorf("the ran event omits %q — a reader cannot tell a clean sweep from a blind one", key)
		}
	}
	if d["judged"] != 1 || d["no_upstream"] != 1 {
		t.Errorf("ran event denominators = %+v, want judged=1 no_upstream=1", d)
	}
	if rec.count() != 0 {
		t.Fatal("a clean sweep must not mail")
	}
}

// TestWatcher_ThrottlesToTheCoarseInterval. The heartbeat ticks every ~30s; a
// sweep per tick would be 2,880 sweeps a day of a condition that changes on
// human timescales.
func TestWatcher_ThrottlesToTheCoarseInterval(t *testing.T) {
	rec, log := &recorder{}, &eventLog{}
	root, w := fixture(t, rec, log)
	write(t, root, "crew/doctor.md", stamped("# doctor\n"))

	now := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		w.Check(now.Add(time.Duration(i) * 30 * time.Second))
	}
	if got := len(log.ofType(ranEvent)); got != 1 {
		t.Fatalf("10 ticks inside one interval produced %d sweeps, want 1", got)
	}
	w.Check(now.Add(2 * time.Hour))
	if got := len(log.ofType(ranEvent)); got != 2 {
		t.Fatalf("a tick past the interval must sample, got %d sweeps", got)
	}
}

// TestWatcher_RefusesToRunWithoutACoordinator. Every finding on a file no
// running agent owns is addressed to the coordinator, and a guessed name would
// deliver the whole report into a phantom mailbox that exists and is read by
// nobody. Refusing to run is the only answer that does not lose the mail
// silently.
func TestWatcher_RefusesToRunWithoutACoordinator(t *testing.T) {
	rec, log := &recorder{}, &eventLog{}
	root := t.TempDir()
	write(t, root, "mayor.md", stampedRecording("a", "b"))
	w := New(Options{
		Enabled: true, Root: root, ShippedFS: shippedFixtureFS(), Coordinator: "",
		Mail: rec.send, Emit: log.emit,
	})
	w.Check(time.Now())
	if rec.count() != 0 || len(log.ofType(ranEvent)) != 0 {
		t.Fatal("a watcher with no coordinator name must not run, and must not mail a guessed name")
	}
	if got := w.Summary(); !strings.Contains(got, "interval=") {
		t.Errorf("Summary = %q, want the arming line an operator can read in the log", got)
	}
}

// TestWatcher_OneMailPerAgentNotPerFile. A mayor with three edited templates
// gets one notice listing three files, not three notices — the volume is what
// decides whether the alarm survives being real.
func TestWatcher_OneMailPerAgentNotPerFile(t *testing.T) {
	rec, log := &recorder{}, &eventLog{}
	root, w := fixture(t, rec, log)
	write(t, root, "mayor.md", stampedRecording("a", "b"))
	write(t, root, "templates/polecat.md", stampedRecording("a", "b"))
	write(t, root, "pm/pm-template.md", stampedRecording("a", "b"))
	write(t, root, "crew/doctor.md", stampedRecording("a", "b"))

	mustSample(t, w, time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC))
	if rec.count() != 2 {
		t.Fatalf("want 2 mails (doctor, mayor), got %d", rec.count())
	}
	var mayorMail sentMail
	for _, m := range rec.sent {
		if m.to == "mayor" {
			mayorMail = m
		}
	}
	if !strings.Contains(mayorMail.subject, "3 prompts") {
		t.Errorf("subject = %q, want the count of files in one notice", mayorMail.subject)
	}
	for _, want := range []string{"mayor.md", "templates/polecat.md", "pm/pm-template.md"} {
		if !strings.Contains(mayorMail.body, want) {
			t.Errorf("the notice omits %s", want)
		}
	}
	// The notice must point at the on-demand half so the recipient can check
	// the whole classification rather than take the mail's word for it.
	if !strings.Contains(mayorMail.body, "pogo check-prompt-edits") {
		t.Error("the notice must name the command that reproduces the full sweep")
	}
}

// TestWatcher_DoesNotMailOutOfDomainFiles is the domain constraint at the
// DELIVERY boundary rather than in the report. This is where it costs money: a
// sweep that mailed its census would send a wall of notices to agents whose
// prompts have no upstream at all, and the report would be filtered inside a
// week.
func TestWatcher_DoesNotMailOutOfDomainFiles(t *testing.T) {
	rec, log := &recorder{}, &eventLog{}
	root, w := fixture(t, rec, log)
	// Two by-design locals and one withdrawn upstream, all "unknown" to a naive
	// sweep and none of them actionable.
	write(t, root, "crew/architect.md", "# no upstream\n")
	write(t, root, "crew/pa.md", "# no upstream\n")
	write(t, root, "crew/pm-pogo.md", stampedRecording("# shipped once\n", "# edited since\n"))

	mustSample(t, w, time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC))
	if rec.count() != 0 {
		t.Fatalf("out-of-domain files must not mail anyone, got %d mails to %v", rec.count(), rec.sent)
	}
	ran := log.ofType(ranEvent)[0].Details
	if ran["no_upstream"] != 2 || ran["upstream_withdrawn"] != 1 || ran["findings"] != 0 {
		t.Errorf("ran event = %+v, want no_upstream=2 upstream_withdrawn=1 findings=0", ran)
	}
}

func mustSample(t *testing.T, w *Watcher, now time.Time) {
	t.Helper()
	if _, err := w.Sample(now); err != nil {
		t.Fatalf("Sample: %v", err)
	}
}
