package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/synthfail"
)

// mg-c058. `pogo agent diagnose` printed `Health: failing_turns` and nothing
// about the window that value summarises. Two agents read that line on
// 2026-08-14, both took it for a present-tense claim about capacity, and both
// were wrong in the same direction: the flagged agents were completing turns,
// and the nine-minute span the counter reported was a two-hour intermittent
// network fault sampled through a 30-minute window.

func blip() *synthfail.Report {
	return &synthfail.Report{
		State:         synthfail.StateFailing,
		Reason:        synthfail.ReasonServerError,
		Count:         2,
		First:         time.Date(2026, 8, 14, 2, 24, 50, 0, time.UTC),
		Last:          time.Date(2026, 8, 14, 2, 33, 27, 0, time.UTC),
		Detail:        "API Error: Can't reach the API server — check your internet or DNS (ENOTFOUND)",
		WindowSeconds: 1800,
		ScannedAt:     time.Date(2026, 8, 14, 2, 44, 0, 0, time.UTC),
	}
}

func TestHealthLine_CarriesTheDetailWhenThereIsOne(t *testing.T) {
	got := healthLine("failing_turns", "2 errors in 30m, 2026-08-14T02:24:50Z–02:33:27Z, last 14m ago")
	if !strings.Contains(got, "failing_turns (2 errors in 30m") {
		t.Fatalf("healthLine = %q, want the window beside the token", got)
	}
	// A health value that needs no qualification must not grow empty parens.
	if got := healthLine("healthy", ""); got != "Health:         healthy\n" {
		t.Fatalf("healthLine = %q, want the bare row for an unqualified value", got)
	}
}

func TestDiagnoseHealthSection_SaysWhatTheCountIsAndIsNot(t *testing.T) {
	var b strings.Builder
	diagnoseHealthSection(&b, &agent.DiagnoseInfo{
		Health:          "failing_turns",
		HealthDetail:    "2 errors in 30m, 2026-08-14T02:24:50Z–02:33:27Z, last 14m ago",
		TranscriptCheck: blip(),
	})
	out := b.String()

	// The reading, on the line a reader actually looks at.
	if !strings.Contains(out, "Health:         failing_turns (2 errors in 30m") {
		t.Errorf("health row does not carry the window:\n%s", out)
	}
	// What was counted, over what window, ending when.
	for _, part := range []string{
		"trailing 30m window",
		"ending 2026-08-14T02:44:00Z",
		"2026-08-14T02:24:50Z to 2026-08-14T02:33:27Z",
		"server_error",
	} {
		if !strings.Contains(out, part) {
			t.Errorf("detail block missing %q:\n%s", part, out)
		}
	}
	// The two misreadings, negated.
	if !strings.Contains(out, "COUNT, NOT A RATE") {
		t.Errorf("nothing says the count is not a rate — the flagged mayor ran the query:\n%s", out)
	}
	if !strings.Contains(out, "NARROWER") {
		t.Errorf("nothing says the window can be narrower than the fault:\n%s", out)
	}
	if !strings.Contains(out, "no instrument failures") {
		t.Errorf("nothing says what establishing recovery takes; every probe that night was clean:\n%s", out)
	}
	// The restraint that predates this ticket and must survive it.
	if !strings.Contains(out, "DO NOT RESTART") {
		t.Errorf("the restart restraint is gone:\n%s", out)
	}
}

// The block is a claim about a moment: without the scan time, a reader dates it
// to when they ran the command — and for a FAILING agent pogod answers out of
// synthwatch's cache, which can be minutes old.
func TestDiagnoseHealthSection_DatesTheScan(t *testing.T) {
	var b strings.Builder
	diagnoseHealthSection(&b, &agent.DiagnoseInfo{
		Health: "failing_turns", HealthDetail: "x", TranscriptCheck: blip(),
	})
	if !strings.Contains(b.String(), blip().ScannedAt.UTC().Format(time.RFC3339)) {
		t.Errorf("detail block does not state when the scan was taken:\n%s", b.String())
	}
}

// A quiet or unavailable transcript gets no failing-turns block at all.
func TestDiagnoseHealthSection_NoBlockWithoutAFailingVerdict(t *testing.T) {
	for name, tc := range map[string]*synthfail.Report{
		"quiet":       {State: synthfail.StateQuiet, Files: 3},
		"unavailable": {State: synthfail.StateUnavailable, Unavailable: "no transcript"},
		"absent":      nil,
	} {
		var b strings.Builder
		diagnoseHealthSection(&b, &agent.DiagnoseInfo{Health: "stalled", TranscriptCheck: tc})
		if strings.Contains(b.String(), "FAILING TURNS") {
			t.Errorf("%s: rendered a failing-turns block:\n%s", name, b.String())
		}
	}
}
