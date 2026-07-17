package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeMailChecks is a MailCheckProvider backed by a set of agent identities
// that have a mail-check schedule.
type fakeMailChecks struct{ have map[string]bool }

func (f fakeMailChecks) HasMailCheck(identity string) bool { return f.have[identity] }

// sandboxDesiredState points HOME/POGO_HOME at a temp dir and declares a crew
// agent in (or out of) pogod's desired state, so the tests below exercise the
// REAL IsExpectedAgent predicate rather than a stand-in. Sharing the predicate
// is the point of PART C — a diagnose that judged expectedness its own way
// could drift out of agreement with the reap, and then one of them would be
// wrong about who is owed a mail loop.
func sandboxDesiredState(t *testing.T, name string, autoStart bool) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	flag := "false"
	if autoStart {
		flag = "true"
	}
	path := filepath.Join(CrewPromptDir(), name+".md")
	if err := os.WriteFile(path, []byte("+++\nauto_start = "+flag+"\n+++\n# "+name+"\n"), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
}

// deadProcess returns the pid of a process that has run and been reaped, and is
// therefore genuinely gone. A real reaped pid rather than a made-up number
// keeps the "not there" case honest: the kernel agrees this pid answers
// nothing.
func deadProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	pid := cmd.Process.Pid
	if pidAlive(pid) {
		t.Skipf("pid %d was recycled between reap and probe; cannot stage a dead pid", pid)
	}
	return pid
}

// mailLoopCrewAgent builds a crew agent that is by every OTHER measure fine:
// running, producing output, not stalled, not rate-limited. That is the point.
// The mg-de08 outage was invisible precisely because such an agent diagnosed
// "healthy" while nothing could reach it.
//
// Its PID is 0, so it is NOT alive by pidAlive: callers that need the agent to
// be running as a matter of process EVIDENCE — the discriminator mg-738f turns
// on — must set one (see liveProcess).
func mailLoopCrewAgent(name string, now time.Time) *Agent {
	buf := NewRingBuffer(1024)
	buf.Write([]byte("working"))
	buf.mu.Lock()
	buf.lastWrite = now
	buf.mu.Unlock()
	return &Agent{
		Name:      name,
		Type:      TypeCrew,
		Status:    StatusRunning,
		StartTime: now.Add(-time.Hour),
		outputBuf: buf,
		done:      make(chan struct{}),
	}
}

// TestDiagnose_ExpectedAgentWithNoMailLoopIsRed is PART C's acceptance: an
// agent pogod expects to be running, with no mail-check schedule, must NOT be
// able to look healthy.
//
// The negative control is the second half — the SAME agent with its mail-check
// present diagnoses healthy. Without it this test would also pass on a build
// that reported every agent RED unconditionally.
func TestDiagnose_ExpectedAgentWithNoMailLoopIsRed(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	now := time.Now()

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	a := mailLoopCrewAgent("pm-pogo", now)

	// The post-reap state: expected, running, reachable by mail — and nothing
	// will ever wake it to read that mail.
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})
	diag := reg.diagnose(a)
	if !diag.MailCheckMissing {
		t.Error("MailCheckMissing = false for an expected agent with no mail-check schedule")
	}
	if diag.Health != "no_mail_loop" {
		t.Errorf("Health = %q, want %q — an agent nothing can wake must not diagnose clean (mg-de08)", diag.Health, "no_mail_loop")
	}

	// Positive control: restore the mail loop and the same agent is healthy.
	// This is what makes the assertion above meaningful.
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{"crew-pm-pogo": true}})
	diag = reg.diagnose(a)
	if diag.MailCheckMissing {
		t.Error("positive control: MailCheckMissing = true despite a registered mail-check")
	}
	if diag.Health != "healthy" {
		t.Errorf("positive control: Health = %q, want %q — the check must fire on a MISSING loop, not on every agent", diag.Health, "healthy")
	}
}

// TestDiagnose_OffByDefaultAgentTurnedOnWithNoMailLoopIsRed is mg-738f's
// acceptance: an agent that pogod does NOT auto-start, which someone turned ON,
// is a DEAF SURVIVOR that nothing flagged. Its mail loop dies and diagnose said
// UNKNOWN — never MISSING — because mailLoopFor asked "is this agent in the
// desired state?" and returned before it could reach the question.
//
// This is mg-de08's exact pathology (an agent with no mail loop and every
// health signal green) in the one population de08's acceptance criterion could
// not see: de08's bar was "diagnose goes RED for an EXPECTED agent with no mail
// loop", and this agent is definitionally not expected. The bar was met and the
// hole stayed open. `doctor` and `pm-lineara` ship auto_start=false today.
//
// The fix is mg-8677's rule, one consumer over: EVIDENCE BEATS EXPECTATION. The
// reap learned it (registryLiveness consults the registry before the desired
// state); diagnose never did. "Not in the desired state" answers "should this
// agent be running?" — the wrong question for an agent that IS running. A
// running process is observable, and a running process nothing can wake is a
// fault whatever its auto_start flag says.
//
// Three controls, because a detector that cannot distinguish "not there" from
// "there and deaf" is the defect this fleet spent 2026-07-17 on:
//
//   - the RED: turned on, no mail loop            -> MISSING
//   - the positive control: same agent, loop back -> healthy (not hard-wired RED)
//   - the conditional control: same agent, NOT    -> UNKNOWN ("not there" is
//     running                                        still not a fault)
func TestDiagnose_OffByDefaultAgentTurnedOnWithNoMailLoopIsRed(t *testing.T) {
	sandboxDesiredState(t, "doctor", false)
	now := time.Now()

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Turned ON: a real live process, so "it is running" is EVIDENCE rather
	// than a status field we set ourselves.
	a := mailLoopCrewAgent("doctor", now)
	a.PID = liveProcess(t)

	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})
	diag := reg.diagnose(a)
	if !diag.MailCheckMissing {
		t.Error("MailCheckMissing = false for a RUNNING auto_start=false agent with no mail-check: " +
			"it answers nothing and every health signal stays clean (mg-738f)")
	}
	if diag.Health != "no_mail_loop" {
		t.Errorf("Health = %q, want %q — an agent nothing can wake must not diagnose clean, "+
			"whatever its auto_start flag says", diag.Health, "no_mail_loop")
	}

	// Positive control: restore the loop and the same agent is healthy. Without
	// this the test would pass on a build that reported every agent RED.
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{"crew-doctor": true}})
	if diag := reg.diagnose(a); diag.MailCheckMissing || diag.Health != "healthy" {
		t.Errorf("positive control: MailCheckMissing = %v, Health = %q; want false, %q — "+
			"the check must fire on a MISSING loop, not on every off-by-default agent",
			diag.MailCheckMissing, diag.Health, "healthy")
	}

	// Conditional control: the SAME not-expected agent, not running. UNKNOWN is
	// the right answer for an agent that isn't there — nothing is owed a mail
	// loop it has no process to answer with. This is what keeps the new RED
	// conditional on evidence rather than hard-wired to auto_start=false.
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})
	off := mailLoopCrewAgent("doctor", now)
	off.PID = deadProcess(t)
	if diag := reg.diagnose(off); diag.MailCheckMissing {
		t.Error("MailCheckMissing = true for an off-by-default agent that is NOT running; " +
			"a detector that cannot tell \"not there\" from \"there and deaf\" is the defect, not the fix")
	}
}

// TestDiagnose_MailLoopUnknownCases asserts diagnose stays silent where it has
// no basis to judge. Each of these would be a false RED, and a health signal
// that cries wolf gets ignored — which is how the fleet ends up back where
// mg-de08 started.
func TestDiagnose_MailLoopUnknownCases(t *testing.T) {
	now := time.Now()

	t.Run("no provider installed", func(t *testing.T) {
		sandboxDesiredState(t, "pm-pogo", true)
		reg, err := NewRegistry(shortSocketDir(t))
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		// Scheduler failed to load, or a bare registry in tests: no claim.
		if diag := reg.diagnose(mailLoopCrewAgent("pm-pogo", now)); diag.MailCheckMissing {
			t.Error("MailCheckMissing = true with no MailCheckProvider installed; diagnose must not judge without evidence")
		}
	})

	// NOTE (mg-738f): this subtest used to read "agent not in desired state",
	// and it asserted that a not-expected agent is never judged — with the
	// rationale "it was started by hand and may not want one". That rationale
	// was the hole. It reasoned from the agent's CONFIG (auto_start=false) when
	// the load-bearing fact is its PROCESS: an agent someone turned on is
	// running, and a running agent that cannot be woken is a fault regardless of
	// what its frontmatter wants. The surviving case is narrower and honest —
	// not-expected AND not running.
	t.Run("agent not in desired state and not running", func(t *testing.T) {
		sandboxDesiredState(t, "lurker", false)
		reg, err := NewRegistry(shortSocketDir(t))
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})
		// pogod does not auto-start it and nobody turned it on: there is no
		// process to be deaf. Nothing to judge.
		a := mailLoopCrewAgent("lurker", now)
		a.PID = deadProcess(t)
		if diag := reg.diagnose(a); diag.MailCheckMissing {
			t.Error("MailCheckMissing = true for an agent that is neither expected nor running")
		}
	})

	t.Run("polecat", func(t *testing.T) {
		sandboxDesiredState(t, "pm-pogo", true)
		reg, err := NewRegistry(shortSocketDir(t))
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})
		p := mailLoopCrewAgent("de08", now)
		p.Type = TypePolecat
		// RUNNING, deliberately: mg-738f widened the judged set to running
		// agents, so a polecat with no process would pass this on liveness and
		// prove nothing about the polecat exclusion itself. A live pid forces
		// the exclusion to hold on its own merits — that is the very trap this
		// ticket is about (a control filtered to exclude its own counterexample).
		p.PID = liveProcess(t)
		// Polecats register their own loop at spawn (mg-e633) and escalate on
		// failure (mg-6fe0); one between spawn and registration is not a fault.
		if diag := reg.diagnose(p); diag.MailCheckMissing {
			t.Error("MailCheckMissing = true for a polecat; polecats own their registration path")
		}
	})
}

// TestDiagnose_MailLoopRedDoesNotMaskRateLimit pins the precedence: a
// rate-limited agent still reports rate_limited (the more actionable truth),
// but the missing loop is still reported in the field, so the fact is never
// hidden by whichever label wins.
func TestDiagnose_MailLoopRedDoesNotMaskRateLimit(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	now := time.Now()

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})

	a := mailLoopCrewAgent("pm-pogo", now)
	a.RateLimited = true

	diag := reg.diagnose(a)
	if diag.Health != "rate_limited" {
		t.Errorf("Health = %q, want %q", diag.Health, "rate_limited")
	}
	if !diag.MailCheckMissing {
		t.Error("MailCheckMissing = false; the missing loop must still be reported even when another label wins")
	}
}

// TestIsConfiguredAgent covers mg-738f's predicate directly, and pins the GAP
// between it and IsExpectedAgent — the gap IS the fix. Every identity where the
// two disagree is an agent that can be running while pogod does not expect it:
// exactly the population whose dead mail loop diagnose could not report.
func TestIsConfiguredAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	write := func(dir, name, flag string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".md"),
			[]byte("+++\nauto_start = "+flag+"\n+++\n# "+name+"\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write(CrewPromptDir(), "pm-pogo", "true")
	write(CrewPromptDir(), "doctor", "false")
	write(CrewPromptDir(), "parked-pm", "true")
	// A polecat TEMPLATE is a scaffold, not a configured agent — same reason
	// IsExpectedAgent excludes it.
	write(TemplateDir(), "polecat", "true")

	parkPath := ParkFilePath("parked-pm")
	if err := os.MkdirAll(filepath.Dir(parkPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(parkPath, []byte(`{"name":"parked-pm"}`), 0644); err != nil {
		t.Fatalf("write park flag: %v", err)
	}

	cases := []struct {
		identity   string
		configured bool
		expected   bool
		why        string
	}{
		{"pm-pogo", true, true, "auto_start crew: both agree"},
		{"crew-pm-pogo", true, true, "event-identity form resolves the same"},
		// THE GAP. Configured but not expected: turn it on and it runs, outside
		// the desired state forever. diagnose must still judge it when it does.
		{"doctor", true, false, "auto_start=false: ours, but not wanted running"},
		{"parked-pm", true, false, "parked: ours, but not wanted running (mg-41e1)"},
		// Not ours at all — the populations that stay UNKNOWN.
		{"polecat", false, false, "a template is not a configured agent"},
		{"cat-de08", false, false, "a polecat has no prompt"},
		{"", false, false, "empty identity"},
	}
	for _, tc := range cases {
		if got := IsConfiguredAgent(tc.identity); got != tc.configured {
			t.Errorf("IsConfiguredAgent(%q) = %v, want %v — %s", tc.identity, got, tc.configured, tc.why)
		}
		if got := IsExpectedAgent(tc.identity); got != tc.expected {
			t.Errorf("IsExpectedAgent(%q) = %v, want %v — %s", tc.identity, got, tc.expected, tc.why)
		}
	}
}

// TestIsExpectedAgent covers the shared predicate directly — the one source of
// truth both the reap and diagnose read.
func TestIsExpectedAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	write := func(dir, name, flag string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".md"),
			[]byte("+++\nauto_start = "+flag+"\n+++\n# "+name+"\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write(CrewPromptDir(), "pm-pogo", "true")
	write(CrewPromptDir(), "lurker", "false")
	write(CrewPromptDir(), "parked-pm", "true")
	// A polecat TEMPLATE with auto_start=true must not make polecats expected:
	// templates are scaffolds, not crew. If this leaked, every polecat's
	// mail-check would become unreapable and the orphan nudges the reap exists
	// to prevent would come back.
	write(TemplateDir(), "polecat", "true")

	parkPath := ParkFilePath("parked-pm")
	if err := os.MkdirAll(filepath.Dir(parkPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(parkPath, []byte(`{"name":"parked-pm"}`), 0644); err != nil {
		t.Fatalf("write park flag: %v", err)
	}

	cases := []struct {
		identity string
		want     bool
	}{
		{"pm-pogo", true},
		{"crew-pm-pogo", true}, // event-identity form
		{"lurker", false},      // no auto_start
		{"parked-pm", false},   // park overrides auto_start (mg-41e1)
		{"polecat", false},     // a template is not a crew agent
		{"cat-de08", false},    // a polecat
		{"", false},
	}
	for _, tc := range cases {
		if got := IsExpectedAgent(tc.identity); got != tc.want {
			t.Errorf("IsExpectedAgent(%q) = %v, want %v", tc.identity, got, tc.want)
		}
	}

	// ExpectedAgents is the same set, enumerated.
	got := ExpectedAgents()
	if len(got) != 1 || got[0] != "pm-pogo" {
		t.Errorf("ExpectedAgents() = %v, want [pm-pogo]", got)
	}
}
