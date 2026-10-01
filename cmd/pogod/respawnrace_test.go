package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/scheduler"
)

// Tests for drellem2/pogo#159 and #217 (and #218's notifier half): a requested
// stop whose agent is started again inside pogod's respawn backoff.
//
// Each suppressed case below carries its own positive control in the same
// harness, because "no condition" and "the row survived" are both satisfied by
// a build that deleted the alarm or never reaps.

// respawnHarness is pogod's OnExit hook in miniature, both arms of it: the
// respawn arm (restart_on_crash) via wireRespawnSupervisor's shape, and the
// cleanup arm's mail-check reap via reapMailChecksAfterExit. errs receives
// every deferred respawn's raw result, so a test can prove which refusal it
// classified rather than assume it.
type respawnHarness struct {
	reg     *agent.Registry
	wg      sync.WaitGroup
	exits   chan bool
	errs    chan error
	backoff time.Duration
}

func newRespawnHarness(t *testing.T, conds conditionRaiser, backoff time.Duration) *respawnHarness {
	t.Helper()
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	h := &respawnHarness{reg: reg, exits: make(chan bool, 16), errs: make(chan error, 16), backoff: backoff}
	reg.SetOnExit(func(a *agent.Agent, _ error) {
		respawn, _ := reg.ShouldRespawnAgent(a)
		if !respawn {
			h.exits <- false
			return
		}
		gen := reg.Generation()
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			time.Sleep(h.backoff)
			_, rerr := reg.RespawnFromGeneration(a.Name, gen)
			requested, cause := a.StopRequested()
			noteRespawnOutcome(conds, "mayor", respawnOutcome{
				Agent:         a.Name,
				Err:           rerr,
				StopRequested: requested,
				StopCause:     cause,
				AliveNow:      registryAgentAlive(reg, a.Name),
			}, time.Now())
			h.errs <- rerr
		}()
		h.exits <- true
	})
	return h
}

func (h *respawnHarness) spawn(t *testing.T, name string) *agent.Agent {
	t.Helper()
	a, err := h.reg.Spawn(agent.SpawnRequest{Name: name, Type: agent.TypeCrew, Command: []string{"cat"}, RestartOnCrash: true})
	if err != nil {
		t.Fatalf("Spawn %s: %v", name, err)
	}
	return a
}

// stop issues a requested stop and returns once the exit hook has scheduled
// the respawn — failing if it did not, since every assertion downstream is
// vacuous without one.
func (h *respawnHarness) stop(t *testing.T, name string) {
	t.Helper()
	if err := h.reg.Stop(name, 2*time.Second); err != nil {
		t.Fatalf("Stop %s: %v", name, err)
	}
	select {
	case scheduled := <-h.exits:
		if !scheduled {
			t.Fatalf("precondition: stopping %s scheduled no respawn", name)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the exit hook for %s never ran", name)
	}
}

// outcome waits for the deferred respawn and returns its raw error.
func (h *respawnHarness) outcome(t *testing.T) error {
	t.Helper()
	h.wg.Wait()
	select {
	case err := <-h.errs:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the deferred respawn never reported")
		return nil
	}
}

// TestStopThenStartInsideBackoffClearsRestartFailed is #217's shape, and #159's:
// a requested stop of a restart_on_crash agent, then a start that lands inside
// the respawn backoff and wins. The deferred respawn finds the agent running.
// Before the fix that refusal fell to noteRespawnOutcome's default branch and
// raised "<name> CRASHED and its restart FAILED — that agent is gone" about the
// live agent. Now: no A6, the row is cleared, the agent is alive.
//
// Positive control, same harness: the same requested stop, no start, and a
// respawn that genuinely fails. That still raises A6 — with the requested-stop
// wording, which is the third of #159's three false assertions.
func TestStopThenStartInsideBackoffClearsRestartFailed(t *testing.T) {
	sandboxPogoHome(t)

	t.Run("start wins the race", func(t *testing.T) {
		conds := &recordingConditions{}
		h := newRespawnHarness(t, conds, 300*time.Millisecond)
		h.spawn(t, "mayor")
		h.stop(t, "mayor")
		// The supervisor's start, ~100ms after the stop in the reporter's log.
		replacement := h.spawn(t, "mayor")

		rerr := h.outcome(t)
		if !errors.Is(rerr, agent.ErrRespawnAgentAlive) {
			t.Fatalf("precondition: the deferred respawn returned %v, want ErrRespawnAgentAlive — "+
				"the race this test is about did not happen", rerr)
		}
		if got := conds.restartFailed(); len(got) != 0 {
			t.Errorf("raised %v about an agent that is running — the #159/#217 false alarm", got)
		}
		if !containsString(conds.cleared, rowA6RestartPrefix+"mayor") {
			t.Errorf("cleared = %v; want %s cleared — the agent is alive, so any earlier "+
				"restart_failed for it is no longer true (Q1 = CLEAR)", conds.cleared, rowA6RestartPrefix+"mayor")
		}
		if !replacement.Alive() {
			t.Error("the replacement agent is not alive")
		}
	})

	t.Run("positive control: requested stop, restart genuinely fails", func(t *testing.T) {
		conds := &recordingConditions{}
		h := newRespawnHarness(t, conds, 300*time.Millisecond)
		h.spawn(t, "doomed")
		h.stop(t, "doomed")
		// Inside the backoff the registration disappears, so the respawn has
		// nothing to restart: a real failure, not a race the agent won.
		h.reg.Remove("doomed")

		rerr := h.outcome(t)
		if rerr == nil || errors.Is(rerr, agent.ErrRespawnAgentAlive) || agent.IsExpectedRespawnRefusal(rerr) {
			t.Fatalf("precondition: want a genuine respawn failure, got %v", rerr)
		}
		got := conds.restartFailed()
		if len(got) != 1 {
			t.Fatalf("restart_failed = %v; want exactly one — a genuine failure after a requested "+
				"stop must still reach the coordinator, or the fix is deleting the alarm", got)
		}
		c := conds.raised[0]
		for _, banned := range []string{"CRASHED", "that agent is gone", "unexpected exit", "exited unexpectedly"} {
			if strings.Contains(c.Subject, banned) || strings.Contains(c.Detail, banned) || strings.Contains(c.Body, banned) {
				t.Errorf("A6 after a REQUESTED stop says %q — a requested stop is not a crash (#159)", banned)
			}
		}
		if !strings.Contains(c.Subject, "stopped on request") || !strings.Contains(c.Body, "stop_cause=request") {
			t.Errorf("A6 after a requested stop does not say so:\nsubject: %s\nbody: %s", c.Subject, c.Body)
		}
	})
}

// TestCrewResetSelfStopIsSilent is the crew-reset silence control (mg-5b58d):
// an agent stops itself and nobody starts it. pogod's own respawn brings it
// back, so there is nothing to raise and the row is cleared — and, unlike the
// race above, this is the ordinary nil branch, so the fix must not have
// disturbed it.
func TestCrewResetSelfStopIsSilent(t *testing.T) {
	sandboxPogoHome(t)
	conds := &recordingConditions{}
	h := newRespawnHarness(t, conds, 100*time.Millisecond)
	before := h.spawn(t, "pm-pogo")
	h.stop(t, "pm-pogo")

	if rerr := h.outcome(t); rerr != nil {
		t.Fatalf("precondition: the self-stop's respawn failed: %v", rerr)
	}
	if got := conds.restartFailed(); len(got) != 0 {
		t.Errorf("a crew reset raised %v", got)
	}
	if !containsString(conds.cleared, rowA6RestartPrefix+"pm-pogo") {
		t.Errorf("cleared = %v; want %s", conds.cleared, rowA6RestartPrefix+"pm-pogo")
	}
	cur := h.reg.Get("pm-pogo")
	if cur == nil || !cur.Alive() || cur.PID == before.PID {
		t.Errorf("pm-pogo is not running as a fresh process after its reset (cur=%v)", cur)
	}
}

// TestLivenessIsRecheckedBeforeRaisingA6 covers the class rather than the one
// sentinel: ANY respawn error about an agent that is running by the time A6
// would say "that agent is gone" — an external supervisor, a park then a wake
// inside the backoff, a refusal nobody has classified yet — clears instead of
// raising. The control is the same error with the agent not running.
func TestLivenessIsRecheckedBeforeRaisingA6(t *testing.T) {
	unclassified := errors.New(`agent "architect": a refusal nobody has classified`)

	alive := &recordingConditions{}
	noteRespawnOutcome(alive, "mayor", respawnOutcome{
		Agent: "architect", Err: unclassified, AliveNow: func() bool { return true },
	}, time.Now())
	if len(alive.raised) != 0 {
		t.Errorf("raised %v about an agent that is running", alive.raised)
	}
	if !containsString(alive.cleared, rowA6RestartPrefix+"architect") {
		t.Errorf("cleared = %v; want the row cleared — a running agent is not gone", alive.cleared)
	}

	// Positive control: same error, agent down.
	dead := &recordingConditions{}
	noteRespawnOutcome(dead, "mayor", respawnOutcome{
		Agent: "architect", Err: unclassified, AliveNow: func() bool { return false },
	}, time.Now())
	if got := dead.restartFailed(); len(got) != 1 {
		t.Errorf("restart_failed = %v; want one — with the agent down the same error must still alarm", got)
	}
}

// TestRestartFailedWordingFollowsTheExit pins the wording split at the unit
// level: a crash says CRASHED (unchanged), a requested stop never does.
func TestRestartFailedWordingFollowsTheExit(t *testing.T) {
	boom := errors.New("pty start: boom")

	crash := &recordingConditions{}
	noteRespawnOutcome(crash, "mayor", respawnOutcome{Agent: "pa", Err: boom}, time.Now())
	if len(crash.raised) != 1 || !strings.Contains(crash.raised[0].Subject, "CRASHED") ||
		!strings.Contains(crash.raised[0].Detail, "unexpected exit") {
		t.Errorf("crash wording changed: %+v", crash.raised)
	}

	stop := &recordingConditions{}
	noteRespawnOutcome(stop, "mayor", respawnOutcome{Agent: "pa", Err: boom,
		StopRequested: true, StopCause: agent.StopCauseRequest}, time.Now())
	if len(stop.raised) != 1 {
		t.Fatalf("raised %v; want one", stop.raised)
	}
	c := stop.raised[0]
	if c.ID != rowA6RestartPrefix+"pa" || c.Row != "A6" {
		t.Errorf("requested-stop A6 is a different row: id=%s row=%s", c.ID, c.Row)
	}
	for _, banned := range []string{"CRASHED", "that agent is gone", "unexpected exit"} {
		if strings.Contains(c.Subject+c.Detail+c.Body, banned) {
			t.Errorf("requested-stop A6 says %q", banned)
		}
	}
}

// TestSupervisedCycleRaisesNoNewConditionAfterTheClear is #218's test, on the
// real annunciator and the real event log. The reporter measured the
// supervised cycle defeating the row's own rate limit: the operator's start
// CLEARED restart_failed:<name> (pogod_condition_cleared) and the deferred
// respawn re-raised it 1.84s later as reason=new, mailing `human` afresh on
// every cycle. After the fix: no pogod_condition reason=new for the row after
// the clear, and no row in the store afterwards.
//
// Positive control in the same harness: a genuine failure after the same kind
// of stop DOES produce reason=new and a stored row.
func TestSupervisedCycleRaisesNoNewConditionAfterTheClear(t *testing.T) {
	sandboxPogoHome(t)
	log := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(log)
	t.Cleanup(func() { events.SetLogPathForTesting(testEventLogPath) })

	rec := &condRecorder{}
	ann, path := newTestAnnunciator(t, rec.send, nil)
	h := newRespawnHarness(t, ann, 300*time.Millisecond)
	h.reg.SetOnStart(func(a *agent.Agent) { noteAgentStarted(ann, a.Name, time.Now()) })

	h.spawn(t, "architect")
	// The previous cycle's false row, still live — the reporter's state at the
	// start of every cycle after the first.
	stale := conditionRestartFailed("mayor", "architect", `agent "architect" is still running`)
	ann.Raise(stale, time.Now().Add(-3*time.Hour))
	ann.flush()

	h.stop(t, "architect")
	h.spawn(t, "architect")
	if rerr := h.outcome(t); !errors.Is(rerr, agent.ErrRespawnAgentAlive) {
		t.Fatalf("precondition: deferred respawn returned %v, want ErrRespawnAgentAlive", rerr)
	}

	evs := conditionEventsFor(t, log, stale.ID)
	clearedAt := -1
	for i, ev := range evs {
		if ev.EventType == conditionClearedEvent {
			clearedAt = i
		}
	}
	if clearedAt < 0 {
		t.Fatalf("precondition: no %s for %s — the start never cleared the row, so 'nothing after "+
			"the clear' cannot be judged (events: %v)", conditionClearedEvent, stale.ID, evs)
	}
	for _, ev := range evs[clearedAt+1:] {
		if ev.EventType == conditionEvent && ev.Details["reason"] == "new" {
			t.Errorf("pogod_condition reason=new for %s after the clear (%v) — the cycle re-raised the "+
				"false alarm and bypassed its own quiet window (#218)", stale.ID, ev.Details)
		}
	}
	if _, live := loadConditionNotices(path).Conditions[stale.ID]; live {
		t.Errorf("%s is still in the store after a supervised cycle", stale.ID)
	}

	// Positive control: a genuine failure after a requested stop.
	h.spawn(t, "doomed")
	h.stop(t, "doomed")
	h.reg.Remove("doomed")
	if rerr := h.outcome(t); rerr == nil {
		t.Fatal("precondition: the control's respawn was supposed to fail")
	}
	ctrl := rowA6RestartPrefix + "doomed"
	var sawNew bool
	for _, ev := range conditionEventsFor(t, log, ctrl) {
		if ev.EventType == conditionEvent && ev.Details["reason"] == "new" {
			sawNew = true
		}
	}
	if !sawNew {
		t.Errorf("control: a genuine failure produced no pogod_condition reason=new for %s — "+
			"the instrument above cannot see a raise, so its silence proves nothing", ctrl)
	}
	if _, live := loadConditionNotices(path).Conditions[ctrl]; !live {
		t.Errorf("control: %s is not in the store after a genuine failure", ctrl)
	}
}

func conditionEventsFor(t *testing.T, log, id string) []events.Event {
	t.Helper()
	var out []events.Event
	err := events.ScanFile(log, func(ev events.Event) {
		if (ev.EventType == conditionEvent || ev.EventType == conditionClearedEvent) && ev.Details["condition"] == id {
			out = append(out, ev)
		}
	})
	if err != nil {
		t.Fatalf("scan %s: %v", log, err)
	}
	return out
}

// TestRequestedStopThenStartKeepsTheMailCheck is #217's branch (b): the seats
// whose restart_on_crash is false. Their exit takes pogod's CLEANUP arm, which
// reaped mail-check-<agent> milliseconds after the stop — before the
// supervisor's start — so the restarted agent came up deaf. That arm can never
// raise A6 (it schedules no respawn), and the respawn arm never reaps: that is
// the reporter's 6/6 exclusivity, and restart_on_crash is the seat-scoped state
// that selected it.
//
// Both orderings of the race the hold has to survive are covered: the GC sweep
// ticking between the stop and the start, and the hold's own end after the
// start. Controls: the same stop with no start reaps at the end of the hold,
// and an exit nobody asked for still reaps on the spot.
func TestRequestedStopThenStartKeepsTheMailCheck(t *testing.T) {
	sandboxPogoHome(t)
	// An on-demand seat: in the desired state, neither auto_start nor
	// restart_on_crash — the reporter's architect and secretary.
	writeCrewPrompt(t, "architect", false)
	writeCrewPrompt(t, "secretary", false)
	writeCrewPrompt(t, "scribe", false)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })

	now := time.Now()
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	holds := newMailCheckReapHolds()
	s.SetLiveness(registryLiveness{reg: reg, holds: holds})
	s.SetGCGate(func(time.Time) bool { return true })

	const grace = time.Hour // the hold must outlast the test; finish is called by hand
	finishes := make(chan func(time.Time), 8)
	reg.SetOnExit(func(a *agent.Agent, _ error) {
		// main.go's cleanup arm, in shape.
		reg.Remove(a.Name)
		requested, cause := a.StopRequested()
		finishes <- reapMailChecksAfterExit(s, holds, a.Name, a.EventAgent(), requested, cause,
			grace, time.Now(), registryAgentAlive(reg, a.Name))
	})
	start := func(name string, cmd ...string) {
		t.Helper()
		if _, err := reg.Spawn(agent.SpawnRequest{Name: name, Type: agent.TypeCrew, Command: cmd}); err != nil {
			t.Fatalf("Spawn %s: %v", name, err)
		}
	}
	mailCheck := func(name string) {
		t.Helper()
		if _, err := s.Add(scheduler.Entry{Agent: name, ID: scheduler.MailCheckIDPrefix + name, Cron: "*/10 * * * *"}, now); err != nil {
			t.Fatalf("Add mail-check %s: %v", name, err)
		}
	}
	has := func(name string) bool {
		for _, e := range s.List(name) {
			if e.ID == scheduler.MailCheckIDPrefix+name {
				return true
			}
		}
		return false
	}
	nextFinish := func() func(time.Time) {
		t.Helper()
		select {
		case f := <-finishes:
			return f
		case <-time.After(10 * time.Second):
			t.Fatal("exit hook never ran")
			return nil
		}
	}

	// --- architect: stop, sweep inside the window, start, end of hold.
	start("architect", "cat")
	mailCheck("architect")
	if err := reg.Stop("architect", 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	finish := nextFinish()
	if finish == nil {
		t.Fatal("a requested stop reaped eagerly instead of holding the mail-check")
	}
	if !has("architect") {
		t.Fatal("mail-check-architect was reaped at stop time — #217 branch (b)")
	}
	// Non-vacuity for the sweep ordering: without the hold, this sweep WOULD
	// reap — the registry has no architect and the desired state does not
	// expect one.
	if got := (registryLiveness{reg: reg}).AgentState("architect"); got != scheduler.AgentGone {
		t.Fatalf("precondition: unheld liveness for the stopped architect = %v, want GONE", got)
	}
	s.Tick(context.Background(), time.Now())
	if !has("architect") {
		t.Fatal("the GC sweep reaped mail-check-architect inside the hold, before its start")
	}
	start("architect", "cat") // the supervisor's start
	finish(time.Now().Add(2 * grace))
	if !has("architect") {
		t.Error("mail-check-architect did not survive a requested stop that was immediately " +
			"followed by a start — the restarted agent is deaf (#217 branch (b))")
	}
	s.Tick(context.Background(), time.Now())
	if !has("architect") {
		t.Error("the sweep reaped mail-check-architect from a running agent")
	}

	// --- secretary (control): the same requested stop, nobody starts it.
	start("secretary", "cat")
	mailCheck("secretary")
	if err := reg.Stop("secretary", 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	finish = nextFinish()
	if finish == nil || !has("secretary") {
		t.Fatal("precondition: secretary's stop did not hold its mail-check")
	}
	finish(time.Now().Add(2 * grace))
	if has("secretary") {
		t.Error("control: a requested stop with no restart kept the mail-check past the hold — " +
			"the hold must end in a reap, or a stopped agent is fired at forever")
	}

	// --- scribe (control): an exit nobody asked for still reaps on the spot.
	mailCheck("scribe")
	start("scribe", "true")
	if f := nextFinish(); f != nil {
		t.Error("an unrequested exit was held; only a requested stop holds")
	}
	if has("scribe") {
		t.Error("control: an unrequested exit no longer reaps its mail-check eagerly")
	}
}

// TestMailCheckReapHoldExpires pins that a hold is bounded on the liveness
// side too: once it ends, the GC sweep's answer is GONE again even if its
// finish never ran (pogod restarting, a panicking goroutine).
func TestMailCheckReapHoldExpires(t *testing.T) {
	sandboxPogoHome(t)
	writeCrewPrompt(t, "architect", false)
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	holds := newMailCheckReapHolds()
	l := registryLiveness{reg: reg, holds: holds}

	holds.hold(time.Now().Add(time.Hour), "architect", "crew-architect")
	for _, id := range []string{"architect", "crew-architect"} {
		if got := l.AgentState(id); got != scheduler.AgentUnknown {
			t.Errorf("held %s = %v, want UNKNOWN", id, got)
		}
	}
	if got := l.AgentState("architect-other"); got != scheduler.AgentGone {
		t.Errorf("unheld agent = %v, want GONE", got)
	}

	short := newMailCheckReapHolds()
	short.hold(time.Now().Add(20*time.Millisecond), "architect")
	time.Sleep(50 * time.Millisecond)
	if got := (registryLiveness{reg: reg, holds: short}).AgentState("architect"); got != scheduler.AgentGone {
		t.Errorf("after the hold expired: %v, want GONE", got)
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestAgentAliveRefusalClearsWithoutALivenessProbe pins the sentinel branch on
// its own. The liveness re-check would also catch the end-to-end race, so
// without this a regression in the classification would hide behind it.
// AliveNow is deliberately nil (cannot tell): the refusal itself is the
// evidence — at the moment of the respawn the agent was running. If that
// replacement dies afterwards, its own exit schedules its own respawn and its
// own outcome; this one has nothing left to say about it.
func TestAgentAliveRefusalClearsWithoutALivenessProbe(t *testing.T) {
	conds := &recordingConditions{}
	err := fmt.Errorf("agent %q %w", "mayor", agent.ErrRespawnAgentAlive)
	noteRespawnOutcome(conds, "mayor", respawnOutcome{Agent: "mayor", Err: err}, time.Now())
	if len(conds.raised) != 0 {
		t.Errorf("raised %v on ErrRespawnAgentAlive", conds.raised)
	}
	if !containsString(conds.cleared, rowA6RestartPrefix+"mayor") {
		t.Errorf("cleared = %v; want the row cleared", conds.cleared)
	}
	if conds.flushes == 0 {
		t.Error("the clear was not flushed")
	}
	if agent.IsExpectedRespawnRefusal(err) {
		t.Error("ErrRespawnAgentAlive must not join the guard refusals: those LEAVE the row, this clears it")
	}
}
