package promptstale

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// mailbox records every send and can be made to fail, which is the only way to
// test that a notice nobody received is not remembered as delivered.
type mailbox struct {
	mu   sync.Mutex
	sent []sentMail
	fail error
}

type sentMail struct{ to, from, subject, body string }

func (m *mailbox) send(to, from, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, sentMail{to, from, subject, body})
	return nil
}

func (m *mailbox) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *mailbox) last() sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sent[len(m.sent)-1]
}

type recorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) countOf(t string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.EventType == t {
			n++
		}
	}
	return n
}

// newWatcher builds a watcher over a fixture, with persistence at statePath
// (pass "" for the in-memory posture).
func newWatcher(t *testing.T, repo, root, statePath string, mail *mailbox, rec *recorder) *Watcher {
	t.Helper()
	return New(Options{
		Enabled: true, Repo: repo, Ref: "main", Root: root,
		Coordinator: "mayor", Mail: mail.send, Emit: rec.emit,
		SkipRemote: true, StatePath: statePath,
	})
}

// TestWatcherMailsTheAffectedAgentThenSuppresses is the notification policy in
// one test: announce on the transition in, stay quiet while nothing changes,
// re-announce once the renotify window has passed.
func TestWatcherMailsTheAffectedAgentThenSuppresses(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(1771, "m")})
	root := installTree(t, map[string][]byte{"mayor.md": lines(1642, "m")})
	mail, rec := &mailbox{}, &recorder{}
	w := newWatcher(t, repo, root, filepath.Join(t.TempDir(), NoticesFile), mail, rec)
	w.renotifyAfter = time.Hour

	now := time.Now()
	if rep := w.Sample(context.Background(), now); len(rep.Findings) != 1 {
		t.Fatalf("first sweep found %d findings, want 1: %+v", len(rep.Findings), rep.Findings)
	}
	if mail.count() != 1 {
		t.Fatalf("first sweep sent %d mails, want 1", mail.count())
	}
	got := mail.last()
	if got.to != "mayor" || got.from != mailFrom {
		t.Errorf("mail addressed %s from %s, want mayor from %s", got.to, got.from, mailFrom)
	}
	if !strings.Contains(got.subject, "YOUR prompt") {
		t.Errorf("subject = %q, want it to name the recipient's own prompt", got.subject)
	}

	// UNCHANGED — quiet.
	w.Sample(context.Background(), now.Add(10*time.Minute))
	if mail.count() != 1 {
		t.Fatalf("an unchanged finding re-notified after 10m: %d mails", mail.count())
	}

	// STILL UNRESOLVED past the window — announced again, because a stale prompt
	// that outlives a nightly is reporting a nightly that is not fixing it.
	w.Sample(context.Background(), now.Add(2*time.Hour))
	if mail.count() != 2 {
		t.Fatalf("an unresolved finding stayed quiet past the renotify window: %d mails", mail.count())
	}

	// The positive record fires on every sweep, clean or not.
	if n := rec.countOf(ranEvent); n != 3 {
		t.Errorf("%s emitted %d times over 3 sweeps, want 3 — an absence cannot tell "+
			"'ran and found nothing' from 'has not run'", ranEvent, n)
	}
}

// TestWatcherForgetsAResolvedPath. A redeploy clears the condition; a
// RECURRENCE after that is news and must mail immediately rather than inherit a
// suppression window from the incident that was already fixed.
func TestWatcherForgetsAResolvedPath(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(1771, "m")})
	stale := installTree(t, map[string][]byte{"mayor.md": lines(1642, "m")})
	current := installTree(t, map[string][]byte{"mayor.md": lines(1771, "m")})
	state := filepath.Join(t.TempDir(), NoticesFile)
	mail, rec := &mailbox{}, &recorder{}

	now := time.Now()
	newWatcher(t, repo, stale, state, mail, rec).Sample(context.Background(), now)
	if mail.count() != 1 {
		t.Fatalf("want 1 mail on the transition in, got %d", mail.count())
	}

	// Redeployed: the sweep is clean and the path is forgotten.
	newWatcher(t, repo, current, state, mail, rec).Sample(context.Background(), now.Add(time.Minute))
	if mail.count() != 1 {
		t.Fatalf("a clean sweep sent mail: %d", mail.count())
	}

	// Regressed, well inside the renotify window.
	newWatcher(t, repo, stale, state, mail, rec).Sample(context.Background(), now.Add(2*time.Minute))
	if mail.count() != 2 {
		t.Fatalf("a recurrence inherited the resolved incident's suppression window: %d mails", mail.count())
	}
}

// TestWatcherSurvivesRestart. The suppression store is on disk precisely so a
// pogod restart does not reset the alarm clock — and on this host pogod restarts
// roughly daily, so in-memory state would re-announce every finding every day.
func TestWatcherSurvivesRestart(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(1771, "m")})
	root := installTree(t, map[string][]byte{"mayor.md": lines(1642, "m")})
	state := filepath.Join(t.TempDir(), NoticesFile)
	mail, rec := &mailbox{}, &recorder{}

	now := time.Now()
	newWatcher(t, repo, root, state, mail, rec).Sample(context.Background(), now)
	// A whole new Watcher, as a restarted daemon builds.
	newWatcher(t, repo, root, state, mail, rec).Sample(context.Background(), now.Add(time.Minute))
	if mail.count() != 1 {
		t.Fatalf("a restart re-announced an already-notified finding: %d mails", mail.count())
	}
}

// TestWatcherRetriesAFailedNotice. A finding that was detected and could not be
// reported must not be remembered as delivered. A notifier that silently stops
// is this detector's own failure mode, one level up.
func TestWatcherRetriesAFailedNotice(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(1771, "m")})
	root := installTree(t, map[string][]byte{"mayor.md": lines(1642, "m")})
	state := filepath.Join(t.TempDir(), NoticesFile)
	mail, rec := &mailbox{}, &recorder{}
	mail.fail = errNoMailbox{}

	now := time.Now()
	newWatcher(t, repo, root, state, mail, rec).Sample(context.Background(), now)
	if mail.count() != 0 {
		t.Fatalf("a failing send recorded a delivery: %d", mail.count())
	}

	mail.fail = nil
	newWatcher(t, repo, root, state, mail, rec).Sample(context.Background(), now.Add(time.Minute))
	if mail.count() != 1 {
		t.Fatalf("the retry after a failed notice did not happen: %d mails — the agent would "+
			"never be told", mail.count())
	}
}

type errNoMailbox struct{}

func (errNoMailbox) Error() string { return "no_such_mailbox" }

// TestWatcherRefAdvancingReNotifies. The repo shipping MORE while the installed
// copy stands still is a different job for the recipient, not the same notice
// repeated, and it must not be suppressed by the renotify window.
func TestWatcherRefAdvancingReNotifies(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(1700, "m")})
	root := installTree(t, map[string][]byte{"mayor.md": lines(1642, "m")})
	state := filepath.Join(t.TempDir(), NoticesFile)
	mail, rec := &mailbox{}, &recorder{}

	now := time.Now()
	newWatcher(t, repo, root, state, mail, rec).Sample(context.Background(), now)
	if mail.count() != 1 {
		t.Fatalf("want 1 mail, got %d", mail.count())
	}

	commitCorpus(t, repo, map[string][]byte{"mayor.md": lines(1771, "m")}, "corpus advances")
	newWatcher(t, repo, root, state, mail, rec).Sample(context.Background(), now.Add(time.Minute))
	if mail.count() != 2 {
		t.Fatalf("the ref advancing did not re-notify: %d mails", mail.count())
	}
	if !strings.Contains(mail.last().body, "behind by 129") {
		t.Errorf("the second notice does not carry the NEW gap:\n%s", mail.last().body)
	}
}

// TestWatcherUnresolvableReferenceIsNotClean. A comparison that could not be
// made has not found the fleet current. It must emit the error, send nothing,
// and — the part that matters — leave the suppression store alone, so a failed
// sweep does not forget what a working one announced.
func TestWatcherUnresolvableReferenceIsNotClean(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(1771, "m")})
	root := installTree(t, map[string][]byte{"mayor.md": lines(1642, "m")})
	state := filepath.Join(t.TempDir(), NoticesFile)
	mail, rec := &mailbox{}, &recorder{}

	now := time.Now()
	newWatcher(t, repo, root, state, mail, rec).Sample(context.Background(), now)
	if mail.count() != 1 {
		t.Fatalf("setup: want 1 mail, got %d", mail.count())
	}

	// A directory that is not a git repo at all.
	broken := New(Options{
		Enabled: true, Repo: t.TempDir(), Ref: "main", Root: root,
		Coordinator: "mayor", Mail: mail.send, Emit: rec.emit,
		SkipRemote: true, StatePath: state,
	})
	rep := broken.Sample(context.Background(), now.Add(time.Minute))
	if rep.Err == "" {
		t.Fatal("a sweep against a non-repo reported no error — it would read as a clean fleet")
	}
	if len(rep.Findings) != 0 || mail.count() != 1 {
		t.Errorf("a failed sweep mailed: %d mails", mail.count())
	}
	if rec.countOf(errorEvent) != 1 {
		t.Errorf("%s emitted %d times, want 1 — a blind detector must be visible in the event "+
			"log, not indistinguishable from a quiet one", errorEvent, rec.countOf(errorEvent))
	}
	if rec.countOf(ranEvent) != 1 {
		t.Errorf("the failed sweep emitted the positive record; %s counted %d, want 1 (the "+
			"first sweep only)", ranEvent, rec.countOf(ranEvent))
	}

	// The store was not rewritten by the failed sweep: a working sweep at the
	// same instant still suppresses.
	newWatcher(t, repo, root, state, mail, rec).Sample(context.Background(), now.Add(2*time.Minute))
	if mail.count() != 1 {
		t.Errorf("the failed sweep forgot an announced finding: %d mails", mail.count())
	}
}

// TestCheckPreconditionsDisarmRatherThanDefault. Each missing input would
// produce confident output about nothing — a comparison with no reference, or
// findings addressed to a guessed coordinator, which is a phantom mailbox that
// accepts mail and is read by nobody.
func TestCheckPreconditionsDisarmRatherThanDefault(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(1771, "m")})
	root := installTree(t, map[string][]byte{"mayor.md": lines(1642, "m")})

	base := func() Options {
		return Options{Enabled: true, Repo: repo, Ref: "main", Root: root,
			Coordinator: "mayor", Mail: (&mailbox{}).send, Emit: (&recorder{}).emit, SkipRemote: true}
	}
	for name, mutate := range map[string]func(*Options){
		"disabled":       func(o *Options) { o.Enabled = false },
		"no coordinator": func(o *Options) { o.Coordinator = "" },
		"no repo":        func(o *Options) { o.Repo = "" },
		"no ref":         func(o *Options) { o.Ref = "" },
		"no root":        func(o *Options) { o.Root = "" },
		"no mail":        func(o *Options) { o.Mail = nil },
	} {
		mail, rec := &mailbox{}, &recorder{}
		o := base()
		o.Mail, o.Emit = mail.send, rec.emit
		mutate(&o)
		New(o).Check(context.Background(), time.Now())
		if mail.count() != 0 || rec.countOf(ranEvent) != 0 {
			t.Errorf("%s: the sweep ran anyway (%d mails, %d %s events)",
				name, mail.count(), rec.countOf(ranEvent), ranEvent)
		}
	}
}

// TestCheckThrottles. Check is the heartbeat path and the heartbeat ticks every
// ~30s; one sample per interval, never one per tick.
func TestCheckThrottles(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(1771, "m")})
	root := installTree(t, map[string][]byte{"mayor.md": lines(1642, "m")})
	mail, rec := &mailbox{}, &recorder{}
	w := newWatcher(t, repo, root, "", mail, rec)
	w.interval = time.Hour

	now := time.Now()
	for i := 0; i < 5; i++ {
		w.Check(context.Background(), now.Add(time.Duration(i)*30*time.Second))
	}
	if n := rec.countOf(ranEvent); n != 1 {
		t.Fatalf("5 ticks inside one interval produced %d sweeps, want 1", n)
	}
	w.Check(context.Background(), now.Add(2*time.Hour))
	if n := rec.countOf(ranEvent); n != 2 {
		t.Fatalf("a tick past the interval did not sample: %d sweeps", n)
	}
}

// TestSummaryStatesTheReferenceAndThePosture. The arming line is how an operator
// sees the detector is wired without reading the event log, and this lineage's
// recurring defect is a detector that is silently not running.
func TestSummaryStatesTheReferenceAndThePosture(t *testing.T) {
	w := New(Options{Enabled: true, Repo: "/ref", Ref: "origin/main", Root: "/root",
		Coordinator: "mayor", Mail: (&mailbox{}).send})
	s := w.Summary()
	for _, want := range []string{"/ref", "origin/main", "/root", "mayor", "report-only", "never fetches"} {
		if !strings.Contains(s, want) {
			t.Errorf("Summary() = %q, missing %q", s, want)
		}
	}
	if got := (*Watcher)(nil).Summary(); got != "disabled" {
		t.Errorf("nil Watcher Summary() = %q, want \"disabled\"", got)
	}
}
