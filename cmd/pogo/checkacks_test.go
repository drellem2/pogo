package main

// Process-level tests for `pogo check-acks` (mg-1935). The detector itself is
// unit-tested in internal/ackwatch against fixtures; what can only be checked
// here is the CLI contract an operator or a cron entry scripts against — the
// exit code, which is set by os.Exit and invisible to a unit test.
//
// The schedules come from a stub pogod, never from the developer's live
// ~/.pogo (see runPogo, and mg-6092 / mg-e8e7 / mg-5336).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/scheduler"
)

// scheduleFixture builds a mail-check entry with counters old enough to be
// judged. Times are relative to now because the detector's freshness gate is
// relative — a hardcoded date would rot the fixture the moment it aged past the
// settle window (the mg-2894 / mg-4e12 class).
func scheduleFixture(agent string, completed, delivered int) scheduler.Entry {
	now := time.Now()
	return scheduler.Entry{
		ID: "mail-check-" + agent, Agent: agent,
		Kind: scheduler.KindMailCheck, Cron: "*/10 * * * *",
		NextFire:       now.Add(5 * time.Minute),
		CreatedAt:      now.Add(-72 * time.Hour),
		FiresDelivered: delivered, FiresCompleted: completed,
	}
}

func serveSchedules(t *testing.T, entries []scheduler.Entry) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(entries); err != nil {
			t.Errorf("encoding schedules: %v", err)
		}
	}
}

// The 2026-07-29 reading, end to end: the deficit is reported and the command
// exits nonzero so it can gate a schedule or CI step.
func TestCheckAcks_DeficitExitsNonzero(t *testing.T) {
	stdout, stderr, code := runPogo(t, serveSchedules(t, []scheduler.Entry{
		scheduleFixture("architect", 751, 757),
		scheduleFixture("pa", 753, 757),
		scheduleFixture("pm-onethird", 751, 757),
		scheduleFixture("pm-pogo", 270, 757),
	}), "check-acks")

	if code == 0 {
		t.Errorf("exit code = 0, want nonzero on a finding\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(stdout, "COMPLETION DEFICIT") {
		t.Errorf("stdout missing the finding:\n%s", stdout)
	}
	if !strings.Contains(stdout, "mail-check-pm-pogo") {
		t.Errorf("stdout does not name the schedule:\n%s", stdout)
	}
	// The remedy must travel with the finding: a default nudge cannot reach a
	// spinning agent, which is the whole reason this was hard to catch.
	if !strings.Contains(stdout, "--immediate") {
		t.Errorf("stdout does not say how to actually reach the agent:\n%s", stdout)
	}
}

func TestCheckAcks_HealthyFleetExitsZero(t *testing.T) {
	stdout, stderr, code := runPogo(t, serveSchedules(t, []scheduler.Entry{
		scheduleFixture("architect", 751, 757),
		scheduleFixture("pa", 753, 757),
		scheduleFixture("pm-onethird", 751, 757),
	}), "check-acks")

	if code != 0 {
		t.Errorf("exit code = %d, want 0 on a healthy fleet\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "no completion deficit") {
		t.Errorf("stdout:\n%s", stdout)
	}
}

func TestCheckAcks_JSONCarriesTheFinding(t *testing.T) {
	stdout, _, _ := runPogo(t, serveSchedules(t, []scheduler.Entry{
		scheduleFixture("architect", 751, 757),
		scheduleFixture("pa", 753, 757),
		scheduleFixture("pm-onethird", 751, 757),
		scheduleFixture("pm-pogo", 270, 757),
	}), "check-acks", "--json")

	var got struct {
		Suppressed bool `json:"suppressed"`
		Eligible   int  `json:"eligible"`
		Deficits   []struct {
			Kind       string  `json:"kind"`
			Agent      string  `json:"agent"`
			ID         string  `json:"id"`
			Rate       float64 `json:"rate"`
			PeerMedian float64 `json:"peer_median"`
		} `json:"deficits"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decoding --json output: %v\n%s", err, stdout)
	}
	if got.Eligible != 4 {
		t.Errorf("eligible = %d, want 4", got.Eligible)
	}
	if len(got.Deficits) != 1 {
		t.Fatalf("deficits = %d, want 1: %s", len(got.Deficits), stdout)
	}
	if got.Deficits[0].ID != "mail-check-pm-pogo" {
		t.Errorf("deficit id = %q", got.Deficits[0].ID)
	}
	if got.Deficits[0].Rate >= got.Deficits[0].PeerMedian {
		t.Errorf("rate %.3f is not below the peer median %.3f", got.Deficits[0].Rate, got.Deficits[0].PeerMedian)
	}
}

// A daemon that cannot be read is not a clean scan. Failing loudly here matters
// more than usual: silence would be this detector reproducing, inside itself,
// the failure it exists to catch.
func TestCheckAcks_UnreadableSchedulerFailsLoudly(t *testing.T) {
	stdout, stderr, code := runPogo(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "scheduler unavailable", http.StatusServiceUnavailable)
	}, "check-acks")

	if code == 0 {
		t.Errorf("exit code = 0 on an unreadable scheduler\nstdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "cannot read scheduler state") {
		t.Errorf("stderr should say it could not look, not report a clean scan:\n%s", stderr)
	}
	if strings.Contains(stdout, "no completion deficit") {
		t.Error("an unreadable scheduler was reported as a clean scan")
	}
}

// A freshly re-registered fleet — the state every crew agent is in for hours
// after the nightly redeploy — must produce nothing at all. This is the
// false-positive storm the detector is designed around, checked through the
// real CLI rather than only at the detector's unit boundary.
func TestCheckAcks_FreshlyReRegisteredFleetIsQuiet(t *testing.T) {
	now := time.Now()
	fresh := func(agent string) scheduler.Entry {
		e := scheduleFixture(agent, 0, 0)
		e.CreatedAt = now.Add(-2 * time.Minute)
		return e
	}
	stdout, _, code := runPogo(t, serveSchedules(t, []scheduler.Entry{
		fresh("architect"), fresh("pa"), fresh("pm-onethird"), fresh("pm-pogo"),
	}), "check-acks")

	if code != 0 {
		t.Errorf("exit code = %d, want 0 right after a bounce\n%s", code, stdout)
	}
	// ...and the silence must be explained, not implied.
	if !strings.Contains(stdout, "Not judged") {
		t.Errorf("a report that evaluated nothing must say so:\n%s", stdout)
	}
}
