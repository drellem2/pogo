package synthwatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/synthfail"
)

// mg-e0d76: the respawn gate must not act on a cached failing verdict that a
// fresh reading shows the agent has since recovered from.

func TestCurrent_FreshSuccessAfterCachedFailuresLiftsTheVerdict(t *testing.T) {
	cached := failing(synthfail.ReasonWeeklyLimit)
	recovered := synthfail.Report{State: synthfail.StateQuiet, Files: 1, LastSuccess: cached.Last.Add(7 * time.Minute)}

	cases := []struct {
		name      string
		fresh     synthfail.Report
		wantState synthfail.State
	}{
		{"success newer than every cached failure lifts it", recovered, synthfail.StateQuiet},
		// Controls: only POSITIVE evidence of recovery replaces the cache.
		{"unreadable fresh read keeps the cache", synthfail.Report{}, synthfail.StateFailing},
		{"quiet with no success seen keeps the cache", synthfail.Report{State: synthfail.StateQuiet, Files: 1}, synthfail.StateFailing},
		{"success older than the cached failures keeps the cache",
			synthfail.Report{State: synthfail.StateQuiet, Files: 1, LastSuccess: cached.Last.Add(-time.Minute)}, synthfail.StateFailing},
		{"still failing keeps the cache", failing(synthfail.ReasonWeeklyLimit), synthfail.StateFailing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdicts := map[string]synthfail.Report{"/w/pm-poset": cached}
			globs, scan := scanByWorkdir(verdicts)
			rec := &recorder{}
			w := New(Options{
				Targets: func() []Target {
					return []Target{{Name: "pm-poset", Identity: "crew-pm-poset", Workdir: "/w/pm-poset"}}
				},
				Globs: globs, Scan: scan, Mail: rec.send, Emit: rec.emit,
			})
			t0 := time.Date(2026, 11, 9, 23, 57, 35, 0, time.UTC)
			w.Check(t0)
			if _, ok := w.Report("pm-poset"); !ok {
				t.Fatal("precondition: watcher holds no failing verdict after a failing scan")
			}

			verdicts["/w/pm-poset"] = tc.fresh
			got, ok := w.Current("pm-poset", "/w/pm-poset", t0.Add(5*time.Minute))
			if !ok {
				t.Fatal("Current reported no verdict while one is cached")
			}
			if got.State != tc.wantState {
				t.Errorf("state = %v, want %v (got %+v)", got.State, tc.wantState, got)
			}
			if got.SuppressRestart() != (tc.wantState == synthfail.StateFailing) {
				t.Errorf("SuppressRestart() = %v", got.SuppressRestart())
			}
		})
	}
}

func TestCurrent_NoCachedVerdictIsNotAVerdict(t *testing.T) {
	globs, scan := scanByWorkdir(map[string]synthfail.Report{})
	w := New(Options{Targets: func() []Target { return nil }, Globs: globs, Scan: scan})
	if _, ok := w.Current("pm-poset", "/w/pm-poset", time.Time{}); ok {
		t.Fatal("Current reported a verdict for an agent the watcher never saw failing")
	}
}

// TestCurrent_WeeklyReset_2026_11_10 replays the incident through the REAL
// reader: the watcher's last scan before pm-poset's exit (~23:57:35Z) caches 5
// weekly-limit failures; at the 00:02:26Z exit the gate must see the turns it
// completed after the 00:00Z reset and not suppress the respawn.
func TestCurrent_WeeklyReset_2026_11_10(t *testing.T) {
	const fixture = "weekly-reset-recovered-2026-11-10.jsonl"
	data, err := os.ReadFile(filepath.Join("..", "synthfail", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".harness", "projects", "pm-poset")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fixture), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Scan's per-file mtime filter must not drop the fixture at a 2026 clock.
	far := time.Date(2026, 11, 10, 0, 2, 26, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, fixture), far, far); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	w := New(Options{
		Home:    home,
		Targets: func() []Target { return []Target{{Name: "pm-poset", Identity: "crew-pm-poset", Workdir: "pm-poset"}} },
		Globs:   func(wd string) []string { return []string{filepath.Join(".harness", "projects", wd, "*.jsonl")} },
		Mail:    rec.send, Emit: rec.emit,
	})
	w.Check(time.Date(2026, 11, 9, 23, 57, 35, 0, time.UTC))
	cached, ok := w.Report("pm-poset")
	if !ok || cached.Count != 5 || cached.Reason != synthfail.ReasonWeeklyLimit {
		t.Fatalf("pre-recovery scan cached %+v (ok=%v), want 5 weekly_limit failures — the verdict pogod acted on", cached, ok)
	}

	exit := time.Date(2026, 11, 10, 0, 2, 26, 471292000, time.UTC)
	got, ok := w.Current("pm-poset", "pm-poset", exit)
	if !ok || got.SuppressRestart() {
		t.Fatalf("at the 00:02:26Z exit Current = %+v (ok=%v); want a quiet verdict — pm-poset had completed turns since 00:01:20Z", got, ok)
	}
	if w.RestartSuppressed("pm-poset", "crew-pm-poset", got, true) {
		t.Error("RestartSuppressed withheld the respawn on a recovered agent")
	}
	if n := rec.countType(EventRestartSuppressed); n != 0 {
		t.Errorf("%d %s events emitted, want 0", n, EventRestartSuppressed)
	}
}
