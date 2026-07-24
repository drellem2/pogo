package agent

import (
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/synthfail"
)

// Tests for mg-8cdb: diagnose reports the synthetic-failure-turn class, and the
// respawn gate refuses to restart an agent in it.
//
// The pairing in every test here is the point. The class and an ordinary wedge
// are opposites at the file level, and pogo's response to them is opposite too
// — restart the wedge, never restart the class — so each assertion that the
// detector FIRES is matched by one that it STAYS SILENT on the other mode.

// fixedScanner returns a canned verdict for every agent.
type fixedScanner struct {
	rep   synthfail.Report
	calls int
}

func (f *fixedScanner) ScanTranscript(name, workdir string) synthfail.Report {
	f.calls++
	return f.rep
}

func failingReport() synthfail.Report {
	return synthfail.Report{
		State:  synthfail.StateFailing,
		Reason: synthfail.ReasonAuthFailed,
		Count:  143,
		First:  time.Date(2026, 7, 21, 23, 10, 26, 0, time.UTC),
		Last:   time.Date(2026, 7, 22, 22, 30, 12, 0, time.UTC),
		Detail: "Login expired · Please run /login",
	}
}

// ---------------------------------------------------------------------------
// diagnose
// ---------------------------------------------------------------------------

func TestDiagnose_FailingTurnsOutranksStalled(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	// An agent in this class looks STALLED to every existing check: it writes
	// nothing to its PTY because it has nothing to say. The transcript is the
	// only thing that can tell the operator which of the two they have.
	a := stalledCrewAgent(now, 25*time.Minute)
	rep := failingReport()

	diag := diagnoseAgentAt(a, now, nil, mailLoopUnknown, &rep)

	if diag.Health != "failing_turns" {
		t.Fatalf("Health = %q, want %q — otherwise the operator sees 'stalled' and restarts, which is the one thing that cannot help", diag.Health, "failing_turns")
	}
	if !diag.RestartSuppressed {
		t.Error("RestartSuppressed = false for an agent failing every turn")
	}
	if diag.TranscriptCheck == nil || diag.TranscriptCheck.Reason != synthfail.ReasonAuthFailed {
		t.Error("diagnose did not carry the reason through to the caller")
	}
}

func TestDiagnose_QuietTranscriptLeavesStalledIntact(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	a := stalledCrewAgent(now, 25*time.Minute)
	rep := synthfail.Report{State: synthfail.StateQuiet, Files: 3}

	diag := diagnoseAgentAt(a, now, nil, mailLoopUnknown, &rep)

	// A transcript that was read and holds no failures means this really is an
	// ordinary wedge — and restart really is the right answer for it.
	if diag.Health != "stalled" {
		t.Fatalf("Health = %q, want %q: a silent transcript is a WEDGE, and the existing handling must be untouched", diag.Health, "stalled")
	}
	if diag.RestartSuppressed {
		t.Fatal("RestartSuppressed = true for a genuinely wedged agent — this would disable the one remediation that works on a wedge")
	}
}

func TestDiagnose_UnavailableTranscriptChangesNothing(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	a := stalledCrewAgent(now, 25*time.Minute)
	unavailable := synthfail.Report{Unavailable: "this harness declares no session transcript path"}

	withScan := diagnoseAgentAt(a, now, nil, mailLoopUnknown, &unavailable)
	without := diagnoseAgentAt(a, now, nil, mailLoopUnknown, nil)

	// Byte-for-byte the same verdict as before the detector existed. This is
	// the degradation contract: pogo loses a detector when the harness changes
	// its internals, it does not gain a false reading.
	if withScan.Health != without.Health {
		t.Fatalf("Health = %q with an unreadable transcript, %q without — degradation must be a no-op", withScan.Health, without.Health)
	}
	if withScan.Stalled != without.Stalled {
		t.Error("Stalled differs between the unavailable and no-scanner paths")
	}
	if withScan.RestartSuppressed {
		t.Error("RestartSuppressed = true on no evidence")
	}
	// But it must SAY it could not look, rather than staying silent about it.
	if withScan.TranscriptCheck == nil || withScan.TranscriptCheck.Unavailable == "" {
		t.Error("diagnose did not report WHY the transcript check was unavailable")
	}
}

func TestDiagnose_ExitedAndDeadStillOutrankFailingTurns(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	rep := failingReport()

	a := stalledCrewAgent(now, 25*time.Minute)
	a.Status = StatusExited
	if got := diagnoseAgentAt(a, now, nil, mailLoopUnknown, &rep).Health; got != "exited" {
		t.Errorf("Health = %q for an exited agent, want %q: a process that is gone is the more immediate fact", got, "exited")
	}
}

// ---------------------------------------------------------------------------
// the respawn gate — the enforcement half
// ---------------------------------------------------------------------------

func respawnableAgent(name string) *Agent {
	return &Agent{Name: name, Type: TypeCrew, RestartOnCrash: true, Dir: "/tmp/agents/" + name}
}

// The tests below call isolateParkState (testmain_test.go) because the respawn
// gate's FIRST guard is Agent.ShouldRespawn — RestartOnCrash && !IsParked —
// and they name real crew agents. Without it they read the developer's LIVE
// park flags: parking pm-dealdesk for real turned
// TestShouldRespawnAgent_WedgedAgentStillRespawns red on an unchanged tree,
// and it failed with "refused to respawn a genuinely wedged agent" while the
// scanner had never run at all (mg-6092). The scannerCalls assertion in each
// test is what turns that short-circuit from a misleading message into a
// visible one: a gate that never reached the scanner has not exercised the
// fixture verdict, whatever its boolean answer happens to be.

func TestShouldRespawnAgent_SuppressedWhenFailingEveryTurn(t *testing.T) {
	isolateParkState(t)
	r := &Registry{}
	scanner := &fixedScanner{rep: failingReport()}
	r.SetTranscriptScanner(scanner)
	a := respawnableAgent("pm-pogo")

	// Sanity: without the gate this agent WOULD be respawned. That is the
	// behaviour mg-18d0 costed at ~66 restarts against a dead credential.
	if !a.ShouldRespawn() {
		t.Fatal("test setup: agent is not respawnable, so the gate proves nothing")
	}

	respawn, by := r.ShouldRespawnAgent(a)

	if respawn {
		t.Fatal("respawned an agent that is failing every turn: the replacement session inherits the same dead credential and the restart destroys the transcript the diagnosis rests on")
	}
	if by.Reason != synthfail.ReasonAuthFailed {
		t.Errorf("suppression reason = %q, want %q — a silent suppression is indistinguishable from a missing one", by.Reason, synthfail.ReasonAuthFailed)
	}
	// Suppressed for the RIGHT reason: the transcript was actually read. A
	// short-circuit on a stray park flag also returns respawn=false, and would
	// look identical here without this.
	if scanner.calls != 1 {
		t.Errorf("scanner calls = %d, want 1: the suppression must come from the transcript verdict, not from a guard that ran before it", scanner.calls)
	}
}

func TestShouldRespawnAgent_WedgedAgentStillRespawns(t *testing.T) {
	isolateParkState(t)
	r := &Registry{}
	scanner := &fixedScanner{rep: synthfail.Report{State: synthfail.StateQuiet, Files: 2}}
	r.SetTranscriptScanner(scanner)

	respawn, by := r.ShouldRespawnAgent(respawnableAgent("pm-dealdesk"))

	if !respawn {
		t.Fatal("refused to respawn a genuinely wedged agent — restart is the correct remediation for a wedge, and suppressing it here would trade one outage for another")
	}
	if by.Reason != "" {
		t.Errorf("reported a suppression reason (%q) for an agent that was not suppressed", by.Reason)
	}
	// The yes has to come from the SCANNER. Before isolateParkState this test
	// answered no with scanner.calls == 0, because the host had a real .parked
	// flag for pm-dealdesk and the gate never got past ShouldRespawn — the
	// StateQuiet fixture was never on the code path the failure blamed.
	if scanner.calls != 1 {
		t.Errorf("scanner calls = %d, want 1: the fixture verdict never reached the gate, so this test proved nothing about StateQuiet", scanner.calls)
	}
}

func TestShouldRespawnAgent_DegradesWithoutEvidence(t *testing.T) {
	cases := []struct {
		name    string
		scanner TranscriptScanner
	}{
		{"no scanner installed", nil},
		{"transcript unavailable", &fixedScanner{rep: synthfail.Report{Unavailable: "no transcript"}}},
		{"zero-value report", &fixedScanner{rep: synthfail.Report{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateParkState(t)
			r := &Registry{}
			if tc.scanner != nil {
				r.SetTranscriptScanner(tc.scanner)
			}

			respawn, _ := r.ShouldRespawnAgent(respawnableAgent("architect"))

			if !respawn {
				t.Fatal("withheld a restart on no evidence — every harness without a transcript would lose crash recovery")
			}
		})
	}
}

func TestShouldRespawnAgent_DoesNotOverrideParkOrRestartOnCrash(t *testing.T) {
	// Isolated for the same reason as its siblings, and here it also keeps the
	// scanner.calls == 0 assertion honest: a stray park flag short-circuits the
	// gate too, so the test would pass without RestartOnCrash=false ever being
	// the thing that stopped it.
	isolateParkState(t)
	r := &Registry{}
	// A scanner that would suppress, to prove the gate is ADDITIVE: it can only
	// ever turn a yes into a no, never a no into a yes.
	scanner := &fixedScanner{rep: synthfail.Report{State: synthfail.StateQuiet}}
	r.SetTranscriptScanner(scanner)

	a := &Agent{Name: "polecat-x", Type: TypePolecat, RestartOnCrash: false}
	if respawn, _ := r.ShouldRespawnAgent(a); respawn {
		t.Error("respawned an agent with restart_on_crash=false")
	}
	if scanner.calls != 0 {
		t.Error("read a transcript for an agent that was never going to be respawned; the gate must short-circuit")
	}

	if respawn, _ := r.ShouldRespawnAgent(nil); respawn {
		t.Error("respawned a nil agent")
	}
}
