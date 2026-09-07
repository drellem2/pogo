package heartwatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture builds a throwaway agent tree and returns its root.
func fixture(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// beat writes a heartbeat at rel with the given mtime.
func beat(t *testing.T, root, rel string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("beat\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func pop(agents ...Present) func() ([]Present, error) {
	return func() ([]Present, error) { return agents, nil }
}

func rowsByName(rep Report) map[string]State {
	out := map[string]State{}
	for _, s := range rep.Agents {
		out[s.Agent] = s
	}
	return out
}

// TestVerdictsAcrossThresholds is the core reading: the two thresholds carried
// over from mayor.md §3a, plus the state that ticket was filed about.
func TestVerdictsAcrossThresholds(t *testing.T) {
	root := fixture(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	started := now.Add(-24 * time.Hour)

	beat(t, root, filepath.Join("pm", "pm-fresh", HeartbeatFile), now.Add(-9*time.Minute))
	beat(t, root, filepath.Join("pm", "pm-stale", HeartbeatFile), now.Add(-100*time.Minute))
	// The measured case: 14 days, ~168x T_restart.
	beat(t, root, filepath.Join("pm", "pm-onethird", HeartbeatFile), now.Add(-14*24*time.Hour))
	beat(t, root, filepath.Join("mayor", HeartbeatFile), now.Add(-5*time.Minute))

	rep, err := Scan(ScanOptions{
		Root: root,
		Now:  now,
		Population: pop(
			Present{Name: "pm-fresh", Type: "crew", StartedAt: started},
			Present{Name: "pm-stale", Type: "crew", StartedAt: started},
			Present{Name: "pm-onethird", Type: "crew", StartedAt: started},
			Present{Name: "mayor", Type: "crew", StartedAt: started},
			Present{Name: "pa", Type: "crew", StartedAt: started},
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := rowsByName(rep)

	want := map[string]Verdict{
		"pm-fresh":    VerdictFresh,
		"pm-stale":    VerdictStale,
		"pm-onethird": VerdictRestartDue,
		"mayor":       VerdictFresh,
		"pa":          VerdictMissing,
	}
	for name, v := range want {
		if rows[name].Verdict != v {
			t.Errorf("%s verdict = %q, want %q", name, rows[name].Verdict, v)
		}
	}
	if rep.Examined != 5 {
		t.Errorf("Examined = %d, want 5", rep.Examined)
	}
	if rep.Findings != 3 {
		t.Errorf("Findings = %d, want 3 (stale, restart_due, missing)", rep.Findings)
	}
	if got := rows["pm-onethird"].Age().Round(time.Hour); got != 14*24*time.Hour {
		t.Errorf("pm-onethird age = %s, want 336h", got)
	}
	// mayor's heartbeat is in its own home dir, not under pm/. If PathsIn ever
	// stopped covering that shape, mayor would read `missing` while its file is
	// on disk and fresh — the coordinator row, which is the one nothing else on
	// this machine can surface.
	if rows["mayor"].Path == "" || !strings.HasSuffix(rows["mayor"].Path, filepath.Join("mayor", HeartbeatFile)) {
		t.Errorf("mayor path = %q, want the agent-home-dir shape", rows["mayor"].Path)
	}
}

// TestMissingIsNeverFoldedIntoFresh guards the one rule this whole package exists for: an
// agent that publishes no heartbeat is NOT healthy.
func TestMissingIsNeverFoldedIntoFresh(t *testing.T) {
	root := fixture(t)
	now := time.Now().UTC()
	rep, err := Scan(ScanOptions{
		Root:       root,
		Now:        now,
		Population: pop(Present{Name: "ghost", Type: "crew", StartedAt: now.Add(-48 * time.Hour)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := rowsByName(rep)["ghost"]
	if s.Verdict != VerdictMissing {
		t.Fatalf("verdict = %q, want %q — an agent with no heartbeat must not read healthy",
			s.Verdict, VerdictMissing)
	}
	if !s.Verdict.Finding() {
		t.Error("VerdictMissing.Finding() = false; a missing heartbeat is a finding")
	}
	if len(s.Searched) == 0 {
		t.Error("Searched is empty; a `missing` row must say where it looked so a reader can check the claim")
	}
}

// TestGraceCoversAFreshSpawn: an agent ninety seconds old owes no heartbeat.
func TestGraceCoversAFreshSpawn(t *testing.T) {
	root := fixture(t)
	now := time.Now().UTC()
	rep, err := Scan(ScanOptions{
		Root:       root,
		Now:        now,
		Population: pop(Present{Name: "newborn", Type: "crew", StartedAt: now.Add(-90 * time.Second)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := rowsByName(rep)["newborn"]
	if s.Verdict != VerdictFresh {
		t.Errorf("verdict = %q, want %q inside the grace window", s.Verdict, VerdictFresh)
	}
	if rep.InGrace != 1 {
		t.Errorf("InGrace = %d, want 1", rep.InGrace)
	}
	if rep.Findings != 0 {
		t.Errorf("Findings = %d, want 0", rep.Findings)
	}
}

// TestGraceDoesNotCoverAnAgentWithAStaleFile. Grace answers "has not written
// one YET". An agent that HAS written one is past that question, and a grace
// that also covered a stale file would silence every fresh restart of a wedged
// agent for its whole window.
func TestGraceDoesNotCoverAnAgentWithAStaleFile(t *testing.T) {
	root := fixture(t)
	now := time.Now().UTC()
	beat(t, root, filepath.Join("pm", "bounced", HeartbeatFile), now.Add(-6*time.Hour))
	rep, err := Scan(ScanOptions{
		Root:       root,
		Now:        now,
		Population: pop(Present{Name: "bounced", Type: "crew", StartedAt: now.Add(-time.Minute)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowsByName(rep)["bounced"].Verdict; got != VerdictRestartDue {
		t.Errorf("verdict = %q, want %q", got, VerdictRestartDue)
	}
}

// TestScanIteratesThePopulationNotTheTree. This machine carries sweep.log files
// for three agents that have not existed for months. A tree-first scan reports
// them forever, and a permanently red detector is one nobody reads.
func TestScanIteratesThePopulationNotTheTree(t *testing.T) {
	root := fixture(t)
	now := time.Now().UTC()
	beat(t, root, filepath.Join("pm", "pm-live", HeartbeatFile), now.Add(-time.Minute))
	// Two abandoned trees, years stale, with no agent behind them.
	beat(t, root, filepath.Join("pm", "lineara", HeartbeatFile), now.Add(-120*24*time.Hour))
	beat(t, root, filepath.Join("pm", "pm-dealdesk", HeartbeatFile), now.Add(-40*24*time.Hour))

	rep, err := Scan(ScanOptions{
		Root:       root,
		Now:        now,
		Population: pop(Present{Name: "pm-live", Type: "crew", StartedAt: now.Add(-24 * time.Hour)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Examined != 1 || len(rep.Agents) != 1 {
		t.Fatalf("Examined = %d, rows = %d; want 1 and 1 — the scan must iterate the population",
			rep.Examined, len(rep.Agents))
	}
	if rep.Findings != 0 {
		t.Errorf("Findings = %d, want 0; the abandoned trees are not agents and must not be reported",
			rep.Findings)
	}
}

// TestNoPopulationIsAnError, not a clean fleet.
func TestNoPopulationIsAnError(t *testing.T) {
	if _, err := Scan(ScanOptions{Root: t.TempDir()}); err == nil {
		t.Fatal("Scan with no Population returned nil error; without a population this run measured nothing")
	}
	_, err := Scan(ScanOptions{
		Root:       t.TempDir(),
		Population: func() ([]Present, error) { return nil, os.ErrPermission },
	})
	if err == nil {
		t.Fatal("Scan propagated no error from a failing Population")
	}
	if !strings.Contains(err.Error(), "measured nothing") {
		t.Errorf("error = %q, want it to say the scan measured nothing", err)
	}
}

// TestEmptyPopulationIsReportedAsZeroExamined. Scan does not refuse an empty
// population — pogod's registry can legitimately be empty — but the report must
// carry the count so a caller cannot mistake zero findings for a clean fleet.
func TestEmptyPopulationIsReportedAsZeroExamined(t *testing.T) {
	rep, err := Scan(ScanOptions{Root: t.TempDir(), Population: pop()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Examined != 0 || rep.Findings != 0 {
		t.Fatalf("Examined = %d, Findings = %d; want 0 and 0", rep.Examined, rep.Findings)
	}
}

// TestFutureMtimeIsUnreadableNotFresh. A forged or skewed mtime must not buy an
// agent unlimited silence.
func TestFutureMtimeIsUnreadableNotFresh(t *testing.T) {
	root := fixture(t)
	now := time.Now().UTC()
	beat(t, root, filepath.Join("pm", "skewed", HeartbeatFile), now.Add(3*time.Hour))
	rep, err := Scan(ScanOptions{
		Root:       root,
		Now:        now,
		Population: pop(Present{Name: "skewed", Type: "crew", StartedAt: now.Add(-24 * time.Hour)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := rowsByName(rep)["skewed"]
	if s.Verdict != VerdictUnreadable {
		t.Errorf("verdict = %q, want %q", s.Verdict, VerdictUnreadable)
	}
	if !strings.Contains(s.Detail, "FUTURE") {
		t.Errorf("detail = %q, want it to name the clock fault", s.Detail)
	}
}

// TestNewestCandidateWins. An agent with heartbeats under both shapes is alive
// if either is fresh.
func TestNewestCandidateWins(t *testing.T) {
	root := fixture(t)
	now := time.Now().UTC()
	beat(t, root, filepath.Join("pm", "both", HeartbeatFile), now.Add(-30*24*time.Hour))
	fresh := beat(t, root, filepath.Join("both", HeartbeatFile), now.Add(-2*time.Minute))
	rep, err := Scan(ScanOptions{
		Root:       root,
		Now:        now,
		Population: pop(Present{Name: "both", Type: "crew", StartedAt: now.Add(-24 * time.Hour)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := rowsByName(rep)["both"]
	if s.Verdict != VerdictFresh {
		t.Errorf("verdict = %q, want %q", s.Verdict, VerdictFresh)
	}
	if s.Path != fresh {
		t.Errorf("path = %q, want the newest candidate %q", s.Path, fresh)
	}
}

// TestRestartThresholdBelowStallIsClamped. A misconfiguration must not remove a
// verdict from the answer space.
func TestRestartThresholdBelowStallIsClamped(t *testing.T) {
	root := fixture(t)
	now := time.Now().UTC()
	beat(t, root, filepath.Join("pm", "late", HeartbeatFile), now.Add(-100*time.Minute))
	rep, err := Scan(ScanOptions{
		Root:         root,
		Now:          now,
		StallAfter:   90 * time.Minute,
		RestartAfter: 10 * time.Minute,
		Population:   pop(Present{Name: "late", Type: "crew", StartedAt: now.Add(-24 * time.Hour)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowsByName(rep)["late"].Verdict; got != VerdictRestartDue {
		t.Errorf("verdict = %q, want %q", got, VerdictRestartDue)
	}
	if rep.RestartAfter != (90 * time.Minute).String() {
		t.Errorf("RestartAfter = %q, want it clamped up to StallAfter", rep.RestartAfter)
	}
}

// TestWakeSuppressionKeepsTheReading. Suppression gates ACTION, never the
// reading: a detector that goes blank after a host sleep goes blank at exactly
// the moment the fleet is most likely to be stuck.
func TestWakeSuppressionKeepsTheReading(t *testing.T) {
	root := fixture(t)
	now := time.Now().UTC()
	beat(t, root, filepath.Join("pm", "asleep", HeartbeatFile), now.Add(-5*time.Hour))
	rep, err := Scan(ScanOptions{
		Root:       root,
		Now:        now,
		WokeAt:     now.Add(-2 * time.Minute),
		Population: pop(Present{Name: "asleep", Type: "crew", StartedAt: now.Add(-24 * time.Hour)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.WakeSuppressed {
		t.Error("WakeSuppressed = false, want true inside the wake grace")
	}
	if rep.Findings != 1 {
		t.Errorf("Findings = %d, want 1 — suppression must not erase the reading", rep.Findings)
	}
	if got := rowsByName(rep)["asleep"].Verdict; got != VerdictRestartDue {
		t.Errorf("verdict = %q, want %q", got, VerdictRestartDue)
	}

	old, err := Scan(ScanOptions{
		Root:       root,
		Now:        now,
		WokeAt:     now.Add(-3 * time.Hour),
		Population: pop(Present{Name: "asleep", Type: "crew", StartedAt: now.Add(-24 * time.Hour)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if old.WakeSuppressed {
		t.Error("WakeSuppressed = true for a wake outside the grace window")
	}
}

// TestProbeGoesRedAndStaysGreen is the positive control's own control.
func TestProbeGoesRedAndStaysGreen(t *testing.T) {
	res, err := Probe(filepath.Join(t.TempDir(), "probe"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Fatalf("probe failed: %s (fresh=%s stale=%s cold=%s missing=%s)",
			res.Detail, res.FreshVerdict, res.StaleVerdict, res.ColdVerdict, res.MissingVerdict)
	}
	if res.Findings != 3 {
		t.Errorf("probe findings = %d, want 3", res.Findings)
	}
}

// TestUnreadableFileIsNotAnAbsence. "We looked and found nothing" and "we could
// not look" are two different facts, and folding the second into the first is
// how a detector reports a clean fleet it never read.
func TestUnreadableFileIsNotAnAbsence(t *testing.T) {
	root := fixture(t)
	now := time.Now().UTC()
	// A DIRECTORY where the heartbeat file should be: present, stat-able, and
	// not a heartbeat. It is the portable form of "there is something here I
	// cannot read" — a chmod 000 file is a no-op for a process running as root,
	// which a CI runner may be.
	dir := filepath.Join(root, "pm", "blocked", HeartbeatFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	rep, err := Scan(ScanOptions{
		Root:       root,
		Now:        now,
		Population: pop(Present{Name: "blocked", Type: "crew", StartedAt: now.Add(-24 * time.Hour)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := rowsByName(rep)["blocked"]
	if s.Verdict != VerdictUnreadable {
		t.Fatalf("verdict = %q, want %q", s.Verdict, VerdictUnreadable)
	}
	if s.Verdict == VerdictMissing {
		t.Error("an unreadable heartbeat was reported as an absence")
	}
	if rep.Bad != 1 || rep.Missing != 0 {
		t.Errorf("unreadable = %d, missing = %d; want 1 and 0", rep.Bad, rep.Missing)
	}
	if !s.Verdict.Finding() {
		t.Error("VerdictUnreadable.Finding() = false; an instrument that could not read is not a pass")
	}
}

// TestUnreadableSurvivesTheGraceWindow. Grace answers "has not written one
// yet". It must not swallow a file that is there and cannot be read — that
// would give a freshly bounced agent a free window of unmeasured silence.
func TestUnreadableSurvivesTheGraceWindow(t *testing.T) {
	root := fixture(t)
	now := time.Now().UTC()
	if err := os.MkdirAll(filepath.Join(root, "pm", "newblocked", HeartbeatFile), 0755); err != nil {
		t.Fatal(err)
	}
	rep, err := Scan(ScanOptions{
		Root:       root,
		Now:        now,
		Population: pop(Present{Name: "newblocked", Type: "crew", StartedAt: now.Add(-time.Minute)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowsByName(rep)["newblocked"].Verdict; got != VerdictUnreadable {
		t.Errorf("verdict = %q, want %q — grace must not cover a file that could not be read", got, VerdictUnreadable)
	}
}
