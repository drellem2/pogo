package supervision

import (
	"strings"
	"testing"
)

// TestCheckVerdicts walks every state Check distinguishes. The two Unsupervised
// rows are the ones the package exists for and they are NOT redundant: the
// 2026-08-05 shape (loaded job, no live process, live lock holder) and the
// two-live-pogods shape reach the same verdict by different readings, and
// collapsing them would lose the reason line an operator acts on.
func TestCheckVerdicts(t *testing.T) {
	tests := []struct {
		name    string
		obs     Observation
		want    Verdict
		wantSub string // a fragment the reason must carry
	}{
		{
			name: "supervised: launchd pid and lock holder agree",
			obs: Observation{
				Label:     "com.pogo.daemon",
				JobLoaded: true, JobPID: 77880, JobPIDOK: true,
				LockPID: 77880, LockPIDOK: true,
			},
			want:    Supervised,
			wantSub: "same process",
		},
		{
			name: "unsupervised: the 2026-08-05 shape — job loaded, no live process, orphan owns the lock",
			obs: Observation{
				Label:     "com.pogo.daemon",
				JobLoaded: true, JobPIDOK: false,
				LockPID: 4368, LockPIDOK: true,
			},
			want:    Unsupervised,
			wantSub: "supervising nothing",
		},
		{
			name: "unsupervised: two live pogods, launchd holds the wrong one",
			obs: Observation{
				Label:     "com.pogo.daemon",
				JobLoaded: true, JobPID: 99999, JobPIDOK: true,
				LockPID: 4368, LockPIDOK: true,
			},
			want:    Unsupervised,
			wantSub: "does not own this POGO_HOME",
		},
		{
			name: "unknown: fleet owns it — a lock holder with no job loaded",
			obs: Observation{
				Label:     "com.pogo.daemon",
				JobLoaded: false,
				LockPID:   4368, LockPIDOK: true,
			},
			want:    Unknown,
			wantSub: "valid single-owner configuration",
		},
		{
			name: "unknown: job loaded, nothing holds the lock",
			obs: Observation{
				Label:     "com.pogo.daemon",
				JobLoaded: true, JobPID: 77880, JobPIDOK: true,
				LockPIDOK: false,
			},
			want:    Unknown,
			wantSub: "nothing holds the pogod lockfile",
		},
		{
			name:    "unknown: nothing loaded and nothing running",
			obs:     Observation{Label: "com.pogo.daemon"},
			want:    Unknown,
			wantSub: "no daemon here to supervise",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(tc.obs)
			if got.Verdict != tc.want {
				t.Fatalf("verdict = %q, want %q (reason: %s)", got.Verdict, tc.want, got.Reason)
			}
			if !strings.Contains(got.Reason, tc.wantSub) {
				t.Errorf("reason %q does not contain %q", got.Reason, tc.wantSub)
			}
		})
	}
}

// TestUnknownIsNotAPass is the mg-e605 shape stated as a test: the failure this
// package exists to remove is a check that goes green because it measured
// nothing, so every Unknown must fail OK() exactly as Unsupervised does.
func TestUnknownIsNotAPass(t *testing.T) {
	for _, obs := range []Observation{
		{Label: "com.pogo.daemon"},
		{Label: "com.pogo.daemon", LockPID: 1, LockPIDOK: true},
		{Label: "com.pogo.daemon", JobLoaded: true, JobPID: 1, JobPIDOK: true},
	} {
		res := Check(obs)
		if res.Verdict != Unknown {
			t.Fatalf("setup: expected Unknown for %+v, got %s", obs, res.Verdict)
		}
		if res.OK() {
			t.Errorf("OK() is true for %s — an unmeasured check must not report a pass", res.Verdict)
		}
	}
}

// TestPPIDNeverChangesTheVerdict pins the package's central warning as
// behaviour rather than prose. Three separate readings of mg-fa79 took ppid 1
// as showing launchd started the process; the 2026-08 displacer was setsid()
// from a CLI and had ppid 1 from its first instant. If ppid ever reaches the
// verdict, the check inherits the misreading it was written to replace.
func TestPPIDNeverChangesTheVerdict(t *testing.T) {
	base := Observation{
		Label:     "com.pogo.daemon",
		JobLoaded: true, JobPIDOK: false,
		LockPID: 4368, LockPIDOK: true,
	}
	launchdParented := base
	launchdParented.LockPPID, launchdParented.LockPPIDOK = 1, true

	if a, b := Check(base), Check(launchdParented); a.Verdict != b.Verdict || a.Reason != b.Reason {
		t.Fatalf("ppid changed the judgement: %s / %q vs %s / %q", a.Verdict, a.Reason, b.Verdict, b.Reason)
	}
}

// TestLastExitReasonNeverChangesTheVerdict is the same guarantee for launchd's
// exit bookkeeping. `runs` and `last exit reason` are lifetime fields that stay
// set across a repair — this box read runs=24991 and OS_REASON_CODESIGNING on a
// daemon that was healthy and current — so they are reportable context and
// never inputs.
func TestLastExitReasonNeverChangesTheVerdict(t *testing.T) {
	base := Observation{
		Label:     "com.pogo.daemon",
		JobLoaded: true, JobPID: 77880, JobPIDOK: true,
		LockPID: 77880, LockPIDOK: true,
	}
	withReason := base
	withReason.LastExitReason = "OS_REASON_CODESIGNING"

	got := Check(withReason)
	if got.Verdict != Supervised {
		t.Fatalf("a previous instance's exit reason downgraded a healthy verdict to %s", got.Verdict)
	}
	if got.Reason != Check(base).Reason {
		t.Errorf("exit reason leaked into the reason line: %q", got.Reason)
	}
}

// TestTextCarriesTheDisclaimers checks that the two report-only fields never
// appear naked. Printing "holder's ppid: 1" with no qualifier is exactly the
// line a reader turns into "launchd started it", which is the misreading this
// package documents.
func TestTextCarriesTheDisclaimers(t *testing.T) {
	res := Check(Observation{
		Label:     "com.pogo.daemon",
		JobLoaded: true, JobPID: 77880, JobPIDOK: true,
		LockPID: 77880, LockPIDOK: true,
		LockPPID: 1, LockPPIDOK: true,
		LastExitReason: "OS_REASON_CODESIGNING",
	})
	text := res.Text()
	for _, want := range []string{"NOT EVIDENCE", "PREVIOUS instance"} {
		if !strings.Contains(text, want) {
			t.Errorf("Text() omits %q:\n%s", want, text)
		}
	}
}

// TestTextNamesAnUnloadedJob covers the render for a host that never installed
// the service: a blank pid line would read as "launchd has no process", which
// is a different and much more alarming statement than "there is no job".
func TestTextNamesAnUnloadedJob(t *testing.T) {
	text := Check(Observation{Label: "com.pogo.daemon", LockPID: 4368, LockPIDOK: true}).Text()
	if !strings.Contains(text, "is not loaded") {
		t.Errorf("Text() does not distinguish an unloaded job:\n%s", text)
	}
}

func TestParseLastExitReason(t *testing.T) {
	// Real `launchctl print gui/501/com.pogo.daemon` shape, trimmed.
	const printed = `com.pogo.daemon = {
	path = /Users/daniel/Library/LaunchAgents/com.pogo.daemon.plist
	state = running
	program = /Users/daniel/go/bin/pogod
	runs = 24991
	pid = 77880
	last exit reason = OS_REASON_CODESIGNING
	properties = keepalive | runatload
}`
	if got := parseLastExitReason(printed); got != "OS_REASON_CODESIGNING" {
		t.Errorf("parseLastExitReason = %q, want OS_REASON_CODESIGNING", got)
	}
	if got := parseLastExitReason("state = running\npid = 1\n"); got != "" {
		t.Errorf("parseLastExitReason on output with no reason line = %q, want empty", got)
	}
	// `last exit code` is a DIFFERENT field and must not be mistaken for the
	// reason — it is the lifetime counter's companion and reads -9 on a
	// deliberately bounced daemon.
	if got := parseLastExitReason("last exit code = -9\n"); got != "" {
		t.Errorf("parseLastExitReason matched the exit CODE line: %q", got)
	}
}

// TestObserveIsSoftOnAMissingJob exercises the host path inside the sandbox.
// The label cannot exist, so this asserts the shape a caller depends on: a
// reading that could not find a job is not an error and not a crash, and the
// absent job does not masquerade as a loaded one.
func TestObserveIsSoftOnAMissingJob(t *testing.T) {
	obs := Observe("com.pogo.supervision-test-no-such-label")
	if obs.JobLoaded {
		t.Errorf("a label that cannot exist reported JobLoaded=true")
	}
	if obs.JobPIDOK {
		t.Errorf("an unloaded job reported a pid: %d", obs.JobPID)
	}
	// Inside the sandbox POGO_HOME is a throwaway root with no daemon, so no
	// lock holder is the expected reading. Asserting it also proves the
	// envelope is doing its job: without it this would find the live pogod.
	if obs.LockPIDOK {
		t.Errorf("found a lock holder (pid %d) inside the sandbox — the test envelope is not isolating POGO_HOME", obs.LockPID)
	}
	if res := Check(obs); res.Verdict != Unknown {
		t.Errorf("verdict = %s, want UNKNOWN for a host with neither a job nor a daemon", res.Verdict)
	}
}
