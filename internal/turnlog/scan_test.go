package turnlog

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func pop(names ...string) func() ([]Present, error) {
	return func() ([]Present, error) {
		out := make([]Present, 0, len(names))
		for _, n := range names {
			out = append(out, Present{Name: n, Type: "crew"})
		}
		return out, nil
	}
}

// TestScanReportsThePresentAndSilent is the whole ticket in one assertion.
//
// Three agents are PRESENT. One completed a turn. One completed its last long
// ago. One has never written a line and therefore has no file in the turnlog
// directory at all — the state mayor, pa and architect were in while a 22-hour
// outage read green everywhere else. All three must appear, and only the first
// may read live.
func TestScanReportsThePresentAndSilent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	if err := AppendIn(root, "pm-pogo", "sweep", now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := AppendIn(root, "pm-onethird", "sweep", now.Add(-22*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rep, err := Scan(Options{Root: root, Now: now, MaxAge: time.Hour,
		Population: pop("mayor", "pm-pogo", "pm-onethird")})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Verdict{
		"pm-pogo":     VerdictLive,
		"pm-onethird": VerdictStale,
		"mayor":       VerdictSilent,
	}
	if len(rep.Agents) != len(want) {
		t.Fatalf("reported %d agents, want %d: %+v", len(rep.Agents), len(want), rep.Agents)
	}
	for _, s := range rep.Agents {
		if s.Verdict != want[s.Agent] {
			t.Errorf("%s: verdict %s, want %s", s.Agent, s.Verdict, want[s.Agent])
		}
	}
	if rep.Findings != 2 || rep.Live != 1 || rep.Stale != 1 || rep.Silent != 1 {
		t.Errorf("counts wrong: %+v", rep)
	}
}

// TestScanIteratesThePopulationNotTheDirectory pins the join direction, which
// is the load-bearing decision in this package. An agent that never wrote a
// line has no file; a scan built off the directory would be structurally
// incapable of reporting exactly the agents this exists to find.
func TestScanIteratesThePopulationNotTheDirectory(t *testing.T) {
	root := t.TempDir() // empty: nobody has ever written a turnlog
	rep, err := Scan(Options{Root: root, Now: time.Now(),
		Population: pop("mayor", "pa", "architect", "pm-pogo", "pm-onethird")})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Agents) != 5 || rep.Silent != 5 || rep.Findings != 5 {
		t.Fatalf("a fleet that has written nothing must read red for every present agent, got %+v", rep)
	}
}

// TestScanWithoutAPopulationIsNotAPass. Losing the registry is the one failure
// that must never render as a clean fleet: without it the only available list
// is the directory, and that list omits the silent agents by construction.
func TestScanWithoutAPopulationIsNotAPass(t *testing.T) {
	root := t.TempDir()
	if _, err := Scan(Options{Root: root}); !errors.Is(err, ErrNoPopulation) {
		t.Errorf("nil population gave %v, want ErrNoPopulation", err)
	}
	boom := func() ([]Present, error) { return nil, fmt.Errorf("connection refused") }
	if _, err := Scan(Options{Root: root, Population: boom}); !errors.Is(err, ErrNoPopulation) {
		t.Errorf("failed population lookup gave %v, want ErrNoPopulation", err)
	}
}

// TestScanReportsAnUnreadableArtifactSeparately: a turnlog full of garbage has
// measured nothing about that agent, and must not be scored the same as one
// that shows a recent turn.
func TestScanReportsAnUnreadableArtifactSeparately(t *testing.T) {
	root := t.TempDir()
	if err := writeFile(PathIn(root, "architect"), "this is not a turnlog line\n"); err != nil {
		t.Fatal(err)
	}
	rep, err := Scan(Options{Root: root, Now: time.Now(), Population: pop("architect")})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Agents[0].Verdict != VerdictUnreadable || rep.Findings != 1 {
		t.Errorf("garbage turnlog scored as %s, want %s", rep.Agents[0].Verdict, VerdictUnreadable)
	}
}

// TestAnEmptyPopulationIsNotGreen. Zero agents examined produces zero findings,
// which is arithmetically correct and operationally the reading that hid a
// 22-hour outage. The count is carried on the report so a caller can tell the
// two apart; this pins that it is there to be read.
func TestAnEmptyPopulationIsNotGreen(t *testing.T) {
	rep, err := Scan(Options{Root: t.TempDir(), Now: time.Now(),
		Population: func() ([]Present, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Findings != 0 {
		t.Fatalf("expected zero findings over an empty population, got %d", rep.Findings)
	}
	if len(rep.Agents) != 0 {
		t.Fatalf("expected an empty agent list, got %+v", rep.Agents)
	}
}
