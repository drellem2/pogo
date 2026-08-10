package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// fixedClock returns a clock that advances only when the test says so.
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time      { return c.t }
func (c *fixedClock) add(d time.Duration) { c.t = c.t.Add(d) }

// newResumeServer is a Server in an isolated $POGO_HOME, so recordBootMode and
// every transition record land in the sandbox rather than in the live
// ~/.pogo/events.log — an alarm-shaped event written by a test run is exactly
// the noise this mechanism exists not to make.
func newResumeServer(t *testing.T) *Server {
	t.Helper()
	testsandbox.Isolate(t)
	return New(nil, nil)
}

// TestStopArmsAResumeDeadline. The obligation exists at the instant of the
// stop, not when the stopper gets around to recording it — that is the whole
// difference between a holder and a promise.
func TestStopArmsAResumeDeadline(t *testing.T) {
	s := newResumeServer(t)
	clk := &fixedClock{t: time.Date(2026, 8, 8, 0, 44, 20, 0, time.UTC)}
	s.SetResumeClock(clk.now)
	s.SetResumeGrace(15 * time.Minute)

	if _, armed := s.ResumeObligation(); armed {
		t.Fatal("a daemon in full mode owes no restart, but one is armed")
	}

	rep, err := s.StopOrchestrationWithCause(Cause{Trigger: "http", Client: "pogo service install"}, 0)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	ob, armed := s.ResumeObligation()
	if !armed {
		t.Fatal("orchestration was stopped and NOTHING is holding the obligation to restart it — " +
			"that is the 2026-08-08 configuration")
	}
	if !ob.Since.Equal(clk.t) {
		t.Errorf("stop time %v, want %v", ob.Since, clk.t)
	}
	if want := clk.t.Add(15 * time.Minute); !ob.Due.Equal(want) {
		t.Errorf("resume due %v, want %v", ob.Due, want)
	}
	if ob.Cause.Client != "pogo service install" {
		t.Errorf("the obligation does not remember WHO stopped the fleet: %+v", ob.Cause)
	}
	if rep.ResumeDue == "" {
		t.Error("the stop response does not tell the caller when the fleet comes back; " +
			`"mode":"index-only" is equally true of a 2s quiesce and a 33h outage`)
	}
}

// TestOverdueIsFalseInsideTheWindow is the POSITIVE CONTROL at the arming layer:
// nothing about an ordinary stop is overdue while the stopper is still working.
func TestOverdueIsFalseInsideTheWindow(t *testing.T) {
	base := time.Date(2026, 8, 10, 3, 0, 0, 0, time.UTC)
	ob := ResumeObligation{Since: base, Due: base.Add(15 * time.Minute)}

	for _, d := range []time.Duration{0, time.Second, 14 * time.Minute, 15*time.Minute - time.Nanosecond} {
		if ob.Overdue(base.Add(d)) {
			t.Errorf("overdue at +%s, inside a 15m window", d)
		}
	}
	for _, d := range []time.Duration{15 * time.Minute, time.Hour, 33 * time.Hour} {
		if !ob.Overdue(base.Add(d)) {
			t.Errorf("NOT overdue at +%s, past a 15m window", d)
		}
	}
	// A zero Due is "nobody is holding this", and it must never read as overdue
	// — a disabled mechanism that silently starts acting is worse than one that
	// does not act.
	none := ResumeObligation{Since: base}
	if none.Overdue(base.Add(1000 * time.Hour)) {
		t.Error("an obligation with NO deadline reports overdue; a disarmed watchdog must stay disarmed")
	}
}

// TestReturningToFullDischargesTheObligation. By any route: the original
// stopper, an operator, or the resumer's own restore. A detector that latched
// would fail every subsequent stop, which is how a real check gets deleted.
func TestReturningToFullDischargesTheObligation(t *testing.T) {
	s := newResumeServer(t)
	if _, err := s.StopOrchestrationWithCause(unattributedCause("test"), 0); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, armed := s.ResumeObligation(); !armed {
		t.Fatal("stop did not arm")
	}
	if _, err := s.StartOrchestration(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if ob, armed := s.ResumeObligation(); armed {
		t.Errorf("the fleet is back in full mode and an obligation is STILL armed (%+v) — "+
			"it would fire a second, duplicate restart", ob)
	}
	if s.Mode() != config.ModeFull {
		t.Fatalf("mode %v after start", s.Mode())
	}
}

// TestASecondStopDoesNotPushTheDeadlineOut. A retry loop that stops an
// already-stopped fleet every thirty seconds is exactly how a watchdog gets
// silently disabled by the thing it watches.
func TestASecondStopDoesNotPushTheDeadlineOut(t *testing.T) {
	s := newResumeServer(t)
	clk := &fixedClock{t: time.Date(2026, 8, 8, 0, 44, 20, 0, time.UTC)}
	s.SetResumeClock(clk.now)
	s.SetResumeGrace(15 * time.Minute)

	if _, err := s.StopOrchestrationWithCause(unattributedCause("first"), 0); err != nil {
		t.Fatalf("stop: %v", err)
	}
	first, _ := s.ResumeObligation()

	clk.add(10 * time.Minute)
	rep, err := s.StopOrchestrationWithCause(unattributedCause("second"), 0)
	if err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if !rep.AlreadyStopped {
		t.Error("the second stop does not report that it changed nothing")
	}
	again, _ := s.ResumeObligation()
	if !again.Due.Equal(first.Due) {
		t.Errorf("a second stop moved the deadline %v -> %v; the obligation must run from the "+
			"ORIGINAL stop or a repeating stopper can defer it forever", first.Due, again.Due)
	}
}

// TestADeclaredHoldIsHonoured, and a hold longer than the grace really extends
// the window — otherwise "declare a maintenance window" would be advice that
// does nothing.
func TestADeclaredHoldIsHonoured(t *testing.T) {
	s := newResumeServer(t)
	clk := &fixedClock{t: time.Date(2026, 8, 10, 3, 0, 0, 0, time.UTC)}
	s.SetResumeClock(clk.now)
	s.SetResumeGrace(15 * time.Minute)

	if _, err := s.StopOrchestrationWithCause(unattributedCause("maintenance"), 4*time.Hour); err != nil {
		t.Fatalf("stop: %v", err)
	}
	ob, _ := s.ResumeObligation()
	if want := clk.t.Add(4 * time.Hour); !ob.Due.Equal(want) {
		t.Errorf("declared hold of 4h produced deadline %v, want %v", ob.Due, want)
	}
	if ob.Hold != 4*time.Hour {
		t.Errorf("the declaration was not recorded: hold=%v", ob.Hold)
	}
}

// TestGraceOfZeroOrLessArmsNoDeadline — the configured-off path. The stop is
// still RECORDED (so /server/mode can say how long the fleet has been down) but
// no deadline is set, and that difference is the whole safety of the switch.
func TestGraceOfZeroOrLessArmsNoDeadline(t *testing.T) {
	for _, grace := range []time.Duration{0, -time.Second} {
		s := newResumeServer(t)
		s.SetResumeGrace(grace)
		if _, err := s.StopOrchestrationWithCause(unattributedCause("test"), 0); err != nil {
			t.Fatalf("stop: %v", err)
		}
		ob, armed := s.ResumeObligation()
		if !armed {
			t.Fatalf("grace=%v: the stop was not even recorded", grace)
		}
		if !ob.Due.IsZero() {
			t.Errorf("grace=%v: a deadline %v was armed on a daemon configured not to resume", grace, ob.Due)
		}
	}
}

// TestServerDefaultGraceMatchesConfigDefault. Two packages hold the same number
// for different reasons (the server needs one when nobody configures it; the
// config needs one to print). A drift between them means an operator reading
// config.toml is told a deadline the daemon does not apply.
func TestServerDefaultGraceMatchesConfigDefault(t *testing.T) {
	if DefaultResumeGrace != config.DefaultOrchestrationResumeGrace {
		t.Errorf("server.DefaultResumeGrace=%v but config.DefaultOrchestrationResumeGrace=%v",
			DefaultResumeGrace, config.DefaultOrchestrationResumeGrace)
	}
	if got := newResumeServer(t).ResumeGrace(); got != DefaultResumeGrace {
		t.Errorf("a fresh Server's grace is %v, not the default %v", got, DefaultResumeGrace)
	}
}

// --- the HTTP surface -------------------------------------------------------

func TestStopEndpointParsesAndRefusesHold(t *testing.T) {
	s := newResumeServer(t)
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	// A declared hold arrives.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/server/stop-orchestration?hold=2h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stop with hold: %d %s", rec.Code, rec.Body.String())
	}
	var rep StopReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rep.ResumeHold != "2h0m0s" {
		t.Errorf("hold not echoed back: %+v", rep)
	}

	// A malformed hold is REFUSED, not silently defaulted. "2hr" meaning
	// fifteen minutes is how a declared window turns into a surprise restart
	// that reads as the watchdog misfiring.
	s2 := newResumeServer(t)
	mux2 := http.NewServeMux()
	s2.RegisterHandlers(mux2)
	rec2 := httptest.NewRecorder()
	mux2.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/server/stop-orchestration?hold=2hr", nil))
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("malformed hold returned %d, want 400 — a silently defaulted declaration is worse "+
			"than a refused one", rec2.Code)
	}
	if s2.Mode() != config.ModeFull {
		t.Error("a refused stop still stopped the fleet")
	}
}

// TestModeEndpointStaysParseableByTheDeployScript.
//
// scripts/pogo-self-deploy reads this body with `json_str mode`, which is
// `sed -n 's/.*"mode":"\([^"]*\)".*/\1/p'` — BRE, and the leading `.*` is
// GREEDY, so it anchors on the LAST occurrence of `"mode":"`. Any key added
// here whose name ends in `mode` would shadow the real one and hand the deploy
// a wrong answer about whether the fleet came back, which is precisely the
// check mg-6d2f added. So this test runs the actual sed expression.
func TestModeEndpointStaysParseableByTheDeployScript(t *testing.T) {
	s := newResumeServer(t)
	if _, err := s.StopOrchestrationWithCause(unattributedCause("test"), 0); err != nil {
		t.Fatalf("stop: %v", err)
	}
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/server/mode", nil))

	body := strings.TrimSpace(rec.Body.String())
	// The greedy-prefix equivalent of the script's BRE.
	re := regexp.MustCompile(`^.*"mode":"([^"]*)"`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the deploy's parse finds no mode at all in %s", body)
	}
	if m[1] != "index-only" {
		t.Fatalf("the deploy's greedy parse of %s yields %q, not \"index-only\" — a later key "+
			"ending in `mode` is shadowing it", body, m[1])
	}
	if !strings.Contains(body, "resume_due") {
		t.Error("/server/mode does not report when the fleet is due back")
	}
}
