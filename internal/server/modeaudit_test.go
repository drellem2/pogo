package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/refinery"
)

var errNoRefinery = errors.New("refinery will not start")

// auditSink collects both halves of the instrument — the pogod.log line and
// the events.log record — so a test can assert on either without depending on
// which one a future reader happens to consume.
type auditSink struct {
	mu     sync.Mutex
	lines  []string
	events []events.Event
}

func (a *auditSink) logf(format string, args ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lines = append(a.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (a *auditSink) emit(e events.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *auditSink) linesContaining(sub string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, l := range a.lines {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return out
}

func (a *auditSink) ofType(t string) []events.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []events.Event
	for _, e := range a.events {
		if e.EventType == t {
			out = append(out, e)
		}
	}
	return out
}

// newAudited builds a server whose log and event sinks are both captured,
// starting from the boot record New() writes.
func newAudited(t *testing.T) (*Server, *auditSink) {
	t.Helper()
	sink := &auditSink{}
	prev := logf
	logf = sink.logf
	t.Cleanup(func() { logf = prev })
	s := newWithEmitter(nil, nil, sink.emit)
	return s, sink
}

// TestBootModeIsRecordedUnconditionally is requirement 2: "which mode did this
// process boot into" must be answerable from an artifact, not inferred from the
// absence of a later transition.
func TestBootModeIsRecordedUnconditionally(t *testing.T) {
	_, sink := newAudited(t)

	got := sink.linesContaining("run mode at startup")
	if len(got) != 1 {
		t.Fatalf("want exactly one startup line, got %d: %v", len(got), sink.lines)
	}
	if !strings.Contains(got[0], "full") {
		t.Errorf("startup line does not name the mode: %q", got[0])
	}

	evs := sink.ofType(EventModeBoot)
	if len(evs) != 1 {
		t.Fatalf("want exactly one %s event, got %d", EventModeBoot, len(evs))
	}
	if evs[0].Details["mode"] != "full" {
		t.Errorf("boot event mode = %v, want full", evs[0].Details["mode"])
	}
	if evs[0].Agent != "pogod" {
		t.Errorf("boot event agent = %q, want pogod", evs[0].Agent)
	}
}

// TestStopTransitionNamesItsHTTPCaller is the core of requirement 1 for the
// direction that darks the fleet: the record must carry previous mode, new
// mode, and who asked.
func TestStopTransitionNamesItsHTTPCaller(t *testing.T) {
	s, sink := newAudited(t)

	req := httptest.NewRequest(http.MethodPost, "/server/stop-orchestration", nil)
	req.Header.Set(HeaderActorAgent, "mayor")
	req.Header.Set(HeaderActorCmd, "pogo service install")
	req.Header.Set(HeaderActorPid, "4711")
	req.RemoteAddr = "127.0.0.1:52144"
	rec := httptest.NewRecorder()
	s.handleStopOrchestration(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned %d", rec.Code)
	}
	if s.Mode() != config.ModeIndexOnly {
		t.Fatalf("mode = %s, want index-only", s.Mode())
	}

	lines := sink.linesContaining("run mode changed")
	if len(lines) != 1 {
		t.Fatalf("want one transition line, got %d: %v", len(lines), sink.lines)
	}
	line := lines[0]
	for _, want := range []string{
		"full -> index-only",
		"trigger=http",
		"detail=\"POST /server/stop-orchestration\"",
		"agent=mayor",
		"client=\"pogo service install\"",
		"client_pid=4711",
		"remote_addr=127.0.0.1:52144",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("transition line missing %q:\n  %s", want, line)
		}
	}
	if strings.Contains(line, "UNATTRIBUTED") {
		t.Errorf("a fully-identified caller was marked unattributed:\n  %s", line)
	}

	evs := sink.ofType(EventModeChanged)
	if len(evs) != 1 {
		t.Fatalf("want one %s event, got %d", EventModeChanged, len(evs))
	}
	d := evs[0].Details
	for k, want := range map[string]any{
		"from":         "full",
		"to":           "index-only",
		"trigger":      "http",
		"actor_agent":  "mayor",
		"actor_client": "pogo service install",
		"actor_pid":    "4711",
		"remote_addr":  "127.0.0.1:52144",
		"attributed":   true,
	} {
		if d[k] != want {
			t.Errorf("event details[%q] = %v, want %v", k, d[k], want)
		}
	}
}

// TestStartTransitionNamesItsHTTPCaller covers the other direction. Both arms
// matter: the ticket's two indistinguishable histories differ precisely in
// whether a return to full happened.
func TestStartTransitionNamesItsHTTPCaller(t *testing.T) {
	s, sink := newAudited(t)
	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("SetMode(index-only): %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/server/start-orchestration", nil)
	req.Header.Set(HeaderActorAgent, "p293c")
	req.Header.Set(HeaderActorPid, "9001")
	req.RemoteAddr = "127.0.0.1:52145"
	rec := httptest.NewRecorder()
	s.handleStartOrchestration(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned %d: %s", rec.Code, rec.Body.String())
	}
	if s.Mode() != config.ModeFull {
		t.Fatalf("mode = %s, want full", s.Mode())
	}

	evs := sink.ofType(EventModeChanged)
	if len(evs) != 2 {
		t.Fatalf("want two %s events (stop then start), got %d", EventModeChanged, len(evs))
	}
	d := evs[1].Details
	if d["from"] != "index-only" || d["to"] != "full" {
		t.Errorf("start event = %v -> %v, want index-only -> full", d["from"], d["to"])
	}
	if d["actor_agent"] != "p293c" {
		t.Errorf("start event actor_agent = %v, want p293c", d["actor_agent"])
	}
}

// TestUnattributedTransitionIsMarked covers the caller that sends nothing —
// a bare curl, or a future in-process path that bypasses the handlers. The
// record still exists and says out loud that nobody can be named, rather than
// looking like an ordinary attributed transition.
func TestUnattributedTransitionIsMarked(t *testing.T) {
	s, sink := newAudited(t)

	req := httptest.NewRequest(http.MethodPost, "/server/stop-orchestration", nil)
	req.RemoteAddr = "127.0.0.1:52146"
	s.handleStopOrchestration(httptest.NewRecorder(), req)

	lines := sink.linesContaining("run mode changed")
	if len(lines) != 1 {
		t.Fatalf("want one transition line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "UNATTRIBUTED") {
		t.Errorf("caller sent no identity but the line does not say so:\n  %s", lines[0])
	}
	if !strings.Contains(lines[0], "remote_addr=127.0.0.1:52146") {
		t.Errorf("what little is known was dropped:\n  %s", lines[0])
	}

	evs := sink.ofType(EventModeChanged)
	if len(evs) != 1 || evs[0].Details["attributed"] != false {
		t.Errorf("event should record attributed=false, got %+v", evs)
	}
}

// TestDirectSetModeIsMarkedUnattributed guards the entry point that takes no
// cause. It is test-and-legacy-only today, but the correction on mg-293c is
// that call sites are open-ended — a future one must not be able to move the
// mode silently.
func TestDirectSetModeIsMarkedUnattributed(t *testing.T) {
	s, sink := newAudited(t)
	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	lines := sink.linesContaining("run mode changed")
	if len(lines) != 1 {
		t.Fatalf("want one transition line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "trigger=unattributed") {
		t.Errorf("direct SetMode not marked unattributed:\n  %s", lines[0])
	}
	if !strings.Contains(lines[0], "detail=\"direct SetMode call\"") {
		t.Errorf("direct SetMode did not name itself:\n  %s", lines[0])
	}
}

// TestNoTransitionRecordsNothing is the negative arm of requirement 4, and it
// is the half that makes the positive arm mean anything. A daemon that nobody
// touched must produce no transition record — otherwise "there is no record"
// stops being evidence that nothing happened.
func TestNoTransitionRecordsNothing(t *testing.T) {
	s, sink := newAudited(t)

	// A stop against a daemon already in index-only, and a start against one
	// already in full: both are requests, neither is a transition.
	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	before := len(sink.ofType(EventModeChanged))
	if before != 1 {
		t.Fatalf("setup: want 1 transition so far, got %d", before)
	}

	for i := 0; i < 3; i++ {
		if err := s.SetMode(config.ModeIndexOnly); err != nil {
			t.Fatalf("redundant stop %d: %v", i, err)
		}
	}
	if got := len(sink.ofType(EventModeChanged)); got != before {
		t.Errorf("redundant stops produced %d extra transition events, want 0", got-before)
	}

	if _, err := s.StartOrchestration(); err != nil {
		t.Fatalf("StartOrchestration: %v", err)
	}
	afterStart := len(sink.ofType(EventModeChanged))
	if afterStart != before+1 {
		t.Fatalf("want exactly one more transition event, got %d", afterStart-before)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.StartOrchestration(); err != nil {
			t.Fatalf("redundant start %d: %v", i, err)
		}
	}
	if got := len(sink.ofType(EventModeChanged)); got != afterStart {
		t.Errorf("redundant starts produced %d extra transition events, want 0", got-afterStart)
	}
}

// TestFailedReturnToFullRecordsNoTransition: transitionToFull returns early
// when the refinery will not restart, leaving the mode index-only. Recording a
// change there would put a full-mode transition in the audit trail for a daemon
// that is still dark — the exact class of wrong answer this ticket exists to
// stop producing.
func TestFailedReturnToFullRecordsNoTransition(t *testing.T) {
	s, sink := newAudited(t)
	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	s.SetRefineryStarter(func() (*refinery.Refinery, error) {
		return nil, errNoRefinery
	})

	before := len(sink.ofType(EventModeChanged))
	if _, err := s.StartOrchestration(); err == nil {
		t.Fatal("StartOrchestration succeeded with a failing refinery starter")
	}
	if s.Mode() != config.ModeIndexOnly {
		t.Fatalf("mode = %s after a failed transition, want index-only", s.Mode())
	}
	if got := len(sink.ofType(EventModeChanged)); got != before {
		t.Errorf("a failed transition recorded %d events, want 0", got-before)
	}
}
