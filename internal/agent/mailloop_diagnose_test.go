package agent

import (
	"os"
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

// mailLoopCrewAgent builds a crew agent that is by every OTHER measure fine:
// running, producing output, not stalled, not rate-limited. That is the point.
// The mg-de08 outage was invisible precisely because such an agent diagnosed
// "healthy" while nothing could reach it.
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

	t.Run("agent not in desired state", func(t *testing.T) {
		sandboxDesiredState(t, "lurker", false)
		reg, err := NewRegistry(shortSocketDir(t))
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})
		// A crew agent pogod does not auto-start is not owed a mail loop —
		// it was started by hand and may not want one.
		if diag := reg.diagnose(mailLoopCrewAgent("lurker", now)); diag.MailCheckMissing {
			t.Error("MailCheckMissing = true for an agent that is not in the desired state")
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
