package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/synthfail"
)

// mg-c058. `failing_turns` is a COUNT over a trailing window, rendered as a
// present-tense statement about an agent's capacity. On 2026-08-14, seven of
// nine agents diagnosed `failing_turns` off two server errors each while every
// one of them was completing turns — including the mayor, which was the agent
// that ran the query. The remedy is that the window travels with the token.

// blipReport is the incident's own verdict.
func blipReport() synthfail.Report {
	return synthfail.Report{
		State:         synthfail.StateFailing,
		Reason:        synthfail.ReasonServerError,
		Count:         2,
		First:         time.Date(2026, 8, 14, 2, 24, 50, 0, time.UTC),
		Last:          time.Date(2026, 8, 14, 2, 33, 27, 0, time.UTC),
		Detail:        "API Error: Can't reach the API server (ENOTFOUND)",
		WindowSeconds: 1800,
		ScannedAt:     time.Date(2026, 8, 14, 2, 44, 0, 0, time.UTC),
	}
}

func TestDiagnose_FailingTurnsCarriesItsWindow(t *testing.T) {
	now := time.Date(2026, 8, 14, 2, 47, 27, 0, time.UTC)
	a := stalledCrewAgent(now, 25*time.Minute)
	rep := blipReport()

	diag := diagnoseAgentAt(a, now, nil, mailLoopUnknown, &rep)

	if diag.HealthDetail == "" {
		t.Fatal("HealthDetail is empty for failing_turns — the bare token is what read as a fleet-wide capacity failure")
	}
	for _, part := range []string{"2 errors", "30m", "02:24:50Z", "02:33:27Z", "last 14m ago"} {
		if !strings.Contains(diag.HealthDetail, part) {
			t.Errorf("HealthDetail = %q, missing %q", diag.HealthDetail, part)
		}
	}
	// The scan behind a failing agent's diagnose is served from synthwatch's
	// cache and can be up to its scan interval old. Unsaid, it reads as live.
	if !strings.Contains(diag.HealthDetail, "scan 3m old") {
		t.Errorf("HealthDetail = %q, want the scan's own age", diag.HealthDetail)
	}
}

// The token is a machine contract: the mayor prompt, the doctor prompt, the
// respawn gate and every existing test compare against it by equality. Fixing
// the reading must not move it.
func TestDiagnose_HealthTokenIsUnchangedByTheDetail(t *testing.T) {
	now := time.Date(2026, 8, 14, 2, 47, 27, 0, time.UTC)
	a := stalledCrewAgent(now, 25*time.Minute)
	rep := blipReport()

	diag := diagnoseAgentAt(a, now, nil, mailLoopUnknown, &rep)
	if diag.Health != "failing_turns" {
		t.Fatalf("Health = %q, want exactly %q — folding the window into the token would break every equality consumer", diag.Health, "failing_turns")
	}

	// And it survives the wire, alongside the machine-readable window.
	b, err := json.Marshal(diag)
	if err != nil {
		t.Fatal(err)
	}
	var round DiagnoseInfo
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatal(err)
	}
	if round.Health != "failing_turns" || round.HealthDetail != diag.HealthDetail {
		t.Fatalf("round-trip lost the reading: health=%q detail=%q", round.Health, round.HealthDetail)
	}
	if round.TranscriptCheck == nil || round.TranscriptCheck.WindowSeconds != 1800 {
		t.Fatal("transcript_check did not carry window_seconds over the wire; a --json consumer cannot recompute the reading without it")
	}
	if round.TranscriptCheck.ScannedAt.IsZero() {
		t.Fatal("transcript_check did not carry scanned_at over the wire")
	}
}

// A health value that needs no qualification must not grow a parenthetical.
func TestDiagnose_NonFailingHealthHasNoDetail(t *testing.T) {
	now := time.Date(2026, 8, 14, 2, 47, 27, 0, time.UTC)
	for name, rep := range map[string]*synthfail.Report{
		"quiet":       {State: synthfail.StateQuiet, Files: 3, WindowSeconds: 1800},
		"unavailable": {State: synthfail.StateUnavailable, Unavailable: "no transcript", WindowSeconds: 1800},
		"no scanner":  nil,
	} {
		diag := diagnoseAgentAt(stalledCrewAgent(now, 25*time.Minute), now, nil, mailLoopUnknown, rep)
		if diag.HealthDetail != "" {
			t.Errorf("%s: HealthDetail = %q, want empty", name, diag.HealthDetail)
		}
	}
}
