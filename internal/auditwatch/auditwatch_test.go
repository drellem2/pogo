package auditwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

const auditRepo = "/Users/daniel/research/onethird_program"

func testCfg() config.AuditSuccessorConfig {
	return config.AuditSuccessorConfig{
		Repos:            []string{auditRepo},
		AuditTags:        []string{"independent-audit"},
		CleanVerdictTags: []string{"audit-clean"},
	}
}

// store builds a throwaway macguffin store and returns its root.
func store(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"available", "claimed", "done", "pending", "shelved"} {
		if err := os.MkdirAll(filepath.Join(root, "work", d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return root
}

type item struct {
	id       string
	status   string
	repo     string
	tags     []string
	depends  []string
	title    string
	mergedAt time.Time // done items only; zero writes no result.json
}

func write(t *testing.T, root string, it item) {
	t.Helper()
	repo := it.repo
	if repo == "" {
		repo = auditRepo
	}
	body := fmt.Sprintf(`---
id: %s
type: task
created: 2026-07-30T00:00:00Z
creator: pm-onethird
depends: [%s]
tags: [%s]
repo: %s
assignee: pm-onethird
priority: high
---

# %s
body
`, it.id, joinList(it.depends), joinList(it.tags), repo, orDefault(it.title, "untitled "+it.id))

	dir := filepath.Join(root, "work", it.status)
	path := filepath.Join(dir, it.id+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	if it.status == "done" && !it.mergedAt.IsZero() {
		// The result file mg writes at completion. Its MTIME is the merge
		// instant — the item file's own mtime is not, because `mg done` renames
		// rather than rewrites and a rename carries filing time along.
		rp := filepath.Join(dir, it.id+".result.json")
		if err := os.WriteFile(rp, []byte(`{"branch":"polecat-`+it.id+`","completed_by":"refinery"}`), 0o644); err != nil {
			t.Fatalf("writing %s: %v", rp, err)
		}
		if err := os.Chtimes(rp, it.mergedAt, it.mergedAt); err != nil {
			t.Fatalf("chtimes %s: %v", rp, err)
		}
	}
}

func joinList(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func silentIDs(rep Report) []string {
	out := make([]string, 0, len(rep.Silent))
	for _, s := range rep.Silent {
		out = append(out, s.ID)
	}
	return out
}

// TestDetectorFires is the load-bearing test, and it is first on purpose. The
// ticket's instruction was to PROVE THE DETECTOR CAN FIRE before trusting that
// it stayed quiet — a detector that always returns an empty report passes every
// other test in this file, and would have passed silently in exactly the way
// the thing it detects fails silently.
func TestDetectorFires(t *testing.T) {
	root := store(t)
	merged := time.Date(2026, 7, 30, 6, 43, 39, 0, time.UTC)
	write(t, root, item{
		id: "mg-f1b2", status: "done", mergedAt: merged,
		tags:  []string{"onethird", "audit", "independent-audit", "mg-8a12-followup"},
		title: "INDEPENDENT AUDIT of whatever mg-8a12 produces",
	})

	rep, err := Scan(root, testCfg(), merged.Add(12*time.Hour))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := silentIDs(rep); len(got) != 1 || got[0] != "mg-f1b2" {
		t.Fatalf("Silent = %v, want exactly [mg-f1b2] — a detector that cannot fire is not a detector", got)
	}
	s := rep.Silent[0]
	if !s.MergedAt.Equal(merged) {
		t.Errorf("MergedAt = %v, want %v (the result.json mtime, not the item file's)", s.MergedAt, merged)
	}
	if s.Silence < 12*time.Hour {
		t.Errorf("Silence = %v, want >= 12h", s.Silence)
	}
	if s.CoveringRepo != auditRepo {
		t.Errorf("CoveringRepo = %q, want the configured entry %q", s.CoveringRepo, auditRepo)
	}
	if s.Title == "" {
		t.Error("Title is empty; a report that cannot name the audit sends its reader to the store")
	}
	if rep.Merged != 1 || rep.Answered != 0 {
		t.Errorf("population: Merged=%d Answered=%d, want 1/0", rep.Merged, rep.Answered)
	}
}

// TestCalibration_2026_07_30 replays the day the window was calibrated against.
//
// Every row is a REAL audit from that store: the merge time is the audit's
// result.json mtime, and lagMinutes is the interval to its first successor's
// `created:`. The two-hour outliers are the reason the window is four hours and
// not one.
//
// The test asserts the calibration DIRECTLY rather than asserting the constant:
// scanned at the last instant before its successor was filed, no audit on this
// day may be reported. Shrinking the window below the slowest observed lag
// fails here, with the row that broke it named — which is what "state what you
// calibrated against" has to mean if it is to survive someone tuning the number
// later.
func TestCalibration_2026_07_30(t *testing.T) {
	day := func(h, m int) time.Time { return time.Date(2026, 7, 30, h, m, 0, 0, time.UTC) }
	rows := []struct {
		id         string
		merged     time.Time
		lagMinutes float64
	}{
		{"mg-5630", day(2, 39), 3.2},
		{"mg-5ad1", day(3, 19), 4.4},
		{"mg-fcf1", day(3, 38), 15.8},
		{"mg-f7bc", day(3, 37), 124.9}, // slowest observed
		{"mg-bd53", day(7, 25), 7.9},
		{"mg-d39d", day(5, 23), 8.6},
		{"mg-6653", day(7, 48), 124.4}, // second slowest
		{"mg-66a6", day(10, 12), 19.1},
		{"mg-e720", day(11, 17), 14.9},
		{"mg-6a2f", day(12, 50), 2.7},
		{"mg-446b", day(12, 24), 8.3},
		{"mg-d673", day(13, 50), 11.8},
		{"mg-a7b4", day(14, 18), 3.4},
		{"mg-bd41", day(13, 41), 10.3},
		{"mg-2216", day(14, 35), 7.0},
		{"mg-3b51", day(15, 2), 10.3},
		{"mg-0a11", day(16, 9), 7.7},
		{"mg-6ad0", day(15, 56), 4.9},
		{"mg-babf", day(15, 21), 11.2},
		{"mg-218d", day(16, 21), 9.8},
		{"mg-a61f", day(17, 25), 6.6},
		{"mg-5644", day(17, 18), 12.3},
	}

	cfg := testCfg()
	for _, r := range rows {
		t.Run(r.id, func(t *testing.T) {
			root := store(t)
			// The successor is deliberately ABSENT: this reproduces the window
			// between the merge and the moment the repair ticket was filed,
			// which is the only interval in which the detector could have been
			// wrong about a healthy audit.
			write(t, root, item{
				id: r.id, status: "done", mergedAt: r.merged,
				tags: []string{"onethird", "audit", "independent-audit"},
			})
			lag := time.Duration(r.lagMinutes * float64(time.Minute))
			rep, err := Scan(root, cfg, r.merged.Add(lag))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(rep.Silent) != 0 {
				t.Fatalf("%s took %.1f min to produce its successor on 2026-07-30 and the %v window reported it as silent at %.1f min. The window is now BELOW an observed healthy lag; recalibrate against a real day or restore it",
					r.id, r.lagMinutes, cfg.AuditWindow(), r.lagMinutes)
			}
			if rep.Waiting != 1 {
				t.Errorf("Waiting = %d, want 1 (inside the window is not a verdict)", rep.Waiting)
			}
		})
	}
}

// TestCalibration_SilentOnesStandOutAgainstTheDay is the other half of the
// positive control. Against the same day's pattern, the four audits that
// produced no successor must be the only things reported — the signal has to
// stand out against the day's own behaviour rather than against a threshold.
func TestCalibration_SilentOnesStandOutAgainstTheDay(t *testing.T) {
	root := store(t)
	day := func(h, m int) time.Time { return time.Date(2026, 7, 30, h, m, 0, 0, time.UTC) }
	auditTags := []string{"onethird", "audit", "independent-audit"}

	answered := []struct {
		audit, successor string
		merged           time.Time
	}{
		{"mg-6ad0", "mg-41aa", day(15, 56)},
		{"mg-0a11", "mg-a893", day(16, 9)},
		{"mg-218d", "mg-bee1", day(16, 21)},
	}
	for _, a := range answered {
		write(t, root, item{id: a.audit, status: "done", mergedAt: a.merged, tags: auditTags})
		write(t, root, item{
			id: a.successor, status: "done", mergedAt: a.merged.Add(time.Hour),
			tags: []string{"onethird", "audit-repair", a.audit + "-followup"}, depends: []string{a.audit},
		})
	}
	// The four that produced nothing. Two merged in the morning (well past any
	// window), two in the last hour of the day.
	write(t, root, item{id: "mg-f1b2", status: "done", mergedAt: day(6, 43), tags: auditTags})
	write(t, root, item{id: "mg-3c24", status: "done", mergedAt: day(7, 21), tags: auditTags})
	write(t, root, item{id: "mg-5800", status: "done", mergedAt: day(17, 30), tags: auditTags})
	write(t, root, item{id: "mg-c6bc", status: "done", mergedAt: day(17, 40), tags: auditTags})

	rep, err := Scan(root, testCfg(), day(18, 40))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := silentIDs(rep)
	want := []string{"mg-f1b2", "mg-3c24"} // oldest silence first
	if len(got) != len(want) {
		t.Fatalf("Silent = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Silent = %v, want %v (oldest silence first)", got, want)
		}
	}
	if rep.Merged != 7 || rep.Answered != 3 || rep.Waiting != 2 {
		t.Errorf("population: Merged=%d Answered=%d Waiting=%d, want 7/3/2 — mg-5800 and mg-c6bc merged inside the window and are not yet a verdict either way",
			rep.Merged, rep.Answered, rep.Waiting)
	}
}

func TestSuccessorChannels(t *testing.T) {
	merged := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	now := merged.Add(24 * time.Hour)

	cases := []struct {
		name       string
		successor  item
		wantSilent bool
	}{
		{
			name:      "depends only",
			successor: item{id: "mg-succ", status: "available", depends: []string{"mg-aud"}, tags: []string{"audit-repair"}},
		},
		{
			name:      "followup tag only",
			successor: item{id: "mg-succ", status: "available", tags: []string{"mg-aud-followup"}},
		},
		{
			// A successor parked behind an unmet depends is FILED and will run.
			// pending/ counting is the same call the pairing gate makes about a
			// pre-filed pair, and for the same reason.
			name:      "pending successor counts",
			successor: item{id: "mg-succ", status: "pending", depends: []string{"mg-aud"}},
		},
		{
			name:      "claimed successor counts",
			successor: item{id: "mg-succ", status: "claimed", depends: []string{"mg-aud"}},
		},
		{
			// A shelved successor is a DROPPED one. Counting it would let an
			// audit be answered by abandoning the answer — the same asymmetry
			// the pairing gate draws between pending/ and shelved/.
			name:       "shelved successor does not count",
			successor:  item{id: "mg-succ", status: "shelved", depends: []string{"mg-aud"}},
			wantSilent: true,
		},
		{
			name:       "unrelated item does not count",
			successor:  item{id: "mg-other", status: "available", depends: []string{"mg-zzzz"}, tags: []string{"audit-repair"}},
			wantSilent: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := store(t)
			write(t, root, item{id: "mg-aud", status: "done", mergedAt: merged, tags: []string{"independent-audit"}})
			write(t, root, tc.successor)
			rep, err := Scan(root, testCfg(), now)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if gotSilent := len(rep.Silent) > 0; gotSilent != tc.wantSilent {
				t.Errorf("silent = %v, want %v (Silent=%v)", gotSilent, tc.wantSilent, silentIDs(rep))
			}
		})
	}
}

// TestAuditNeverSucceedsItself: an audit whose own tags contain its id (mg
// writes `mg-1234-followup` on the PAIR, but a hand-edited item can carry
// anything) must not answer itself.
func TestAuditNeverSucceedsItself(t *testing.T) {
	root := store(t)
	merged := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	write(t, root, item{
		id: "mg-aud", status: "done", mergedAt: merged,
		tags: []string{"independent-audit", "mg-aud-followup"}, depends: []string{"mg-aud"},
	})
	rep, err := Scan(root, testCfg(), merged.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Silent) != 1 {
		t.Fatalf("Silent = %v, want the audit reported — it cannot be its own successor", silentIDs(rep))
	}
}

// TestCleanVerdictSilencesIt, and the comment is the ticket's own limit 1: this
// tag is an artifact anyone can produce without reading the audit. The test
// exists to pin the behaviour, NOT to suggest the verdict was earned.
func TestCleanVerdictSilencesIt(t *testing.T) {
	root := store(t)
	merged := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	write(t, root, item{
		id: "mg-aud", status: "done", mergedAt: merged,
		tags: []string{"independent-audit", "audit-clean"},
	})
	rep, err := Scan(root, testCfg(), merged.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Silent) != 0 {
		t.Errorf("Silent = %v, want none", silentIDs(rep))
	}
	if rep.CleanVerdict != 1 || rep.Answered != 0 {
		t.Errorf("CleanVerdict=%d Answered=%d, want 1/0 — a clean verdict is the WEAKER artifact and must be counted apart from a real successor",
			rep.CleanVerdict, rep.Answered)
	}
}

// TestUndatedIsNotAVerdict: a done audit with no result.json has no recorded
// completion time, so its silence cannot be aged. It must be counted as its own
// thing rather than folded into either answer — the alternatives are reporting
// an unknown as clean, or crying wolf on every audit mg completed without a
// result.
func TestUndatedIsNotAVerdict(t *testing.T) {
	root := store(t)
	write(t, root, item{id: "mg-aud", status: "done", tags: []string{"independent-audit"}})
	rep, err := Scan(root, testCfg(), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Silent) != 0 {
		t.Errorf("Silent = %v, want none — an unaged audit is not a known failure", silentIDs(rep))
	}
	if rep.Undated != 1 {
		t.Errorf("Undated = %d, want 1 — the blind spot has to be visible", rep.Undated)
	}
}

func TestScopeIsNotWidened(t *testing.T) {
	merged := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	now := merged.Add(24 * time.Hour)
	cases := []struct {
		name string
		it   item
	}{
		{"non-audit item in a covered repo", item{id: "mg-x", status: "done", mergedAt: merged, tags: []string{"onethird", "research"}}},
		{"audit-tagged item in an uncovered repo", item{id: "mg-x", status: "done", mergedAt: merged, repo: "/Users/daniel/dev/pogo", tags: []string{"independent-audit"}}},
		{"covered repo prefix must not match a sibling", item{id: "mg-x", status: "done", mergedAt: merged, repo: auditRepo + "_v2", tags: []string{"independent-audit"}}},
		{"an audit that has not merged", item{id: "mg-x", status: "claimed", tags: []string{"independent-audit"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := store(t)
			write(t, root, tc.it)
			rep, err := Scan(root, testCfg(), now)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if rep.Merged != 0 || len(rep.Silent) != 0 {
				t.Errorf("Merged=%d Silent=%v, want 0/none — the signal is specific to a merged ticket whose deliverable is findings",
					rep.Merged, silentIDs(rep))
			}
		})
	}
	t.Run("a covered subdirectory IS in scope", func(t *testing.T) {
		root := store(t)
		write(t, root, item{id: "mg-x", status: "done", mergedAt: merged, repo: auditRepo + "/sub", tags: []string{"independent-audit"}})
		rep, err := Scan(root, testCfg(), now)
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if len(rep.Silent) != 1 {
			t.Errorf("Silent = %v, want the item reported", silentIDs(rep))
		}
	})
}

func TestInertWithoutConfig(t *testing.T) {
	root := store(t)
	merged := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	write(t, root, item{id: "mg-aud", status: "done", mergedAt: merged, tags: []string{"independent-audit"}})

	for _, tc := range []struct {
		name string
		cfg  config.AuditSuccessorConfig
	}{
		{"zero value", config.AuditSuccessorConfig{}},
		{"repos but no audit_tags", config.AuditSuccessorConfig{Repos: []string{auditRepo}}},
		{"audit_tags but no repos", config.AuditSuccessorConfig{AuditTags: []string{"independent-audit"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := Scan(root, tc.cfg, merged.Add(24*time.Hour))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if rep.Enabled {
				t.Error("Enabled = true; an under-configured detector must report itself disabled, not clean")
			}
			if rep.Merged != 0 || len(rep.Silent) != 0 {
				t.Errorf("Merged=%d Silent=%v, want a scan that examined nothing", rep.Merged, silentIDs(rep))
			}
		})
	}
}

// TestUnreadableStoreIsAnError: "no silent audits" and "I could not look" must
// not be the same return value. A store that cannot be read is the one case
// where reporting clean would certify a state never observed.
func TestUnreadableStoreIsAnError(t *testing.T) {
	root := t.TempDir()
	workDone := filepath.Join(root, "work", "done")
	if err := os.MkdirAll(workDone, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(workDone, 0o000); err != nil {
		t.Skipf("cannot remove read permission here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(workDone, 0o755) })
	if _, err := os.ReadDir(workDone); err == nil {
		t.Skip("running with permission to read a 0000 directory (root?)")
	}

	if _, err := Scan(root, testCfg(), time.Now()); err == nil {
		t.Fatal("Scan on an unreadable store returned no error; a detector that reports an unread store as clean is worse than absent")
	}
}

func TestWindowIsConfigurable(t *testing.T) {
	if got := (config.AuditSuccessorConfig{}).AuditWindow(); got != config.DefaultAuditSuccessorWindow {
		t.Errorf("unset window = %v, want the calibrated default %v", got, config.DefaultAuditSuccessorWindow)
	}
	root := store(t)
	merged := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	write(t, root, item{id: "mg-aud", status: "done", mergedAt: merged, tags: []string{"independent-audit"}})

	cfg := testCfg()
	cfg.Window = 30 * time.Minute
	rep, err := Scan(root, cfg, merged.Add(45*time.Minute))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Silent) != 1 {
		t.Errorf("Silent = %v with a 30m window at 45m, want the audit reported", silentIDs(rep))
	}
	if rep.Window != 30*time.Minute {
		t.Errorf("Report.Window = %v, want the configured 30m so a reader can see which threshold produced this", rep.Window)
	}
}
