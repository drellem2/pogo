package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/auditwatch"
	"github.com/drellem2/pogo/internal/config"
)

func lineCfg() config.AuditSuccessorConfig {
	return config.AuditSuccessorConfig{
		Repos:            []string{"/Users/daniel/research/onethird_program"},
		AuditTags:        []string{"independent-audit"},
		CleanVerdictTags: []string{"audit-clean"},
	}
}

// TestAuditSuccessorLine_DisabledDoesNotReadAsClean is the whole reason this
// renderer exists as its own function. A detector for a failure mode of SILENCE
// cannot report its own silence as a clean result — a reader skimming the
// checklist has to be able to tell "every audit was answered" from "nobody
// configured me".
func TestAuditSuccessorLine_DisabledDoesNotReadAsClean(t *testing.T) {
	status, detail := auditSuccessorLine(auditwatch.Report{}, nil, config.AuditSuccessorConfig{}, time.Now())
	if status != "pass" {
		t.Errorf("status = %q, want pass — an unconfigured detector is not a broken host", status)
	}
	if !strings.Contains(detail, "not configured") {
		t.Errorf("detail = %q, want it to say it is not configured", detail)
	}
	if !strings.Contains(detail, "not a report that every merged audit was answered") {
		t.Errorf("detail = %q, must disclaim the clean reading outright", detail)
	}
}

func TestAuditSuccessorLine_NamesTheMissingConfigHalf(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.AuditSuccessorConfig
		want string
	}{
		{"neither", config.AuditSuccessorConfig{}, "no repos and no audit_tags"},
		{"no repos", config.AuditSuccessorConfig{AuditTags: []string{"a"}}, "no repos"},
		{"no tags", config.AuditSuccessorConfig{Repos: []string{"/r"}}, "no audit_tags"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, detail := auditSuccessorLine(auditwatch.Report{}, nil, tc.cfg, time.Now())
			if !strings.Contains(detail, tc.want) {
				t.Errorf("detail = %q, want it to name %q", detail, tc.want)
			}
		})
	}
}

// TestAuditSuccessorLine_UnreadableStoreSaysSo: same rule as above, one level
// down. "I could not look" must not render as "nothing to report".
func TestAuditSuccessorLine_UnreadableStoreSaysSo(t *testing.T) {
	status, detail := auditSuccessorLine(auditwatch.Report{}, os.ErrPermission, lineCfg(), time.Now())
	if status != "pass" {
		t.Errorf("status = %q, want pass — an unreadable macguffin store is not a pogo health failure", status)
	}
	if !strings.Contains(detail, "NOT CHECKED") {
		t.Errorf("detail = %q, want it to say the check did not run", detail)
	}
	if !strings.Contains(detail, "not a report that every merged audit was answered") {
		t.Errorf("detail = %q, must disclaim the clean reading", detail)
	}
}

func TestAuditSuccessorLine_CleanReportsThePopulation(t *testing.T) {
	rep := auditwatch.Report{
		Enabled: true, Window: 4 * time.Hour,
		Merged: 27, Answered: 23, CleanVerdict: 0, Waiting: 2, Undated: 2,
	}
	status, detail := auditSuccessorLine(rep, nil, lineCfg(), time.Now())
	if status != "pass" {
		t.Fatalf("status = %q, want pass", status)
	}
	// A bare "no silent audits" is indistinguishable from a scan that examined
	// nothing. The population is what tells a reader the detector was looking at
	// something.
	for _, want := range []string{"27 merged audit(s) examined", "23 answered", "2 still inside the 4h window", "2 with no recorded completion time"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to contain %q", detail, want)
		}
	}
}

func TestAuditSuccessorLine_WarnsAndNamesEachAudit(t *testing.T) {
	// A non-UTC zone on purpose: the rendered stamp is labelled Z, so it must be
	// converted rather than printed off the host clock. This instant is
	// 06:43Z, and a renderer that skipped the conversion would print 07:43Z.
	merged := time.Date(2026, 7, 30, 7, 43, 0, 0, time.FixedZone("BST", 3600))
	rep := auditwatch.Report{
		Enabled: true, Window: 4 * time.Hour, Merged: 7, Answered: 3, Waiting: 2,
		Silent: []auditwatch.SilentAudit{
			{ID: "mg-f1b2", MergedAt: merged, Silence: 12 * time.Hour},
			{ID: "mg-3c24", MergedAt: merged.Add(38 * time.Minute), Silence: 11*time.Hour + 22*time.Minute},
		},
	}
	status, detail := auditSuccessorLine(rep, nil, lineCfg(), time.Now())
	if status != "warn" {
		t.Fatalf("status = %q, want warn", status)
	}
	for _, want := range []string{"mg-f1b2", "mg-3c24", "silent 12h", "2026-07-30 06:43Z"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to contain %q", detail, want)
		}
	}
	// Both remedies, and the honest caveat on the cheaper one. Limit 1 of the
	// ticket says a reader must be able to see that a clean verdict is cheap to
	// produce FROM THE REPORT, rather than rediscovering it.
	if !strings.Contains(detail, "audit-clean") {
		t.Errorf("detail = %q, want it to name the configured clean-verdict tag", detail)
	}
	if !strings.Contains(detail, "DETECTOR, not a gate") {
		t.Errorf("detail = %q, want it to say it blocks nothing", detail)
	}
	if !strings.Contains(detail, "without reading the audit") {
		t.Errorf("detail = %q, want it to state that a clean verdict is cheap", detail)
	}
}

// TestAuditSuccessorLine_NoCleanVerdictTagsAdvisesOnlyWhatWorks: advising a tag
// the deployment never configured is an instruction that silently does nothing,
// which is the same defect class as a remedy that no-ops (mg-04ab).
func TestAuditSuccessorLine_NoCleanVerdictTagsAdvisesOnlyWhatWorks(t *testing.T) {
	cfg := lineCfg()
	cfg.CleanVerdictTags = nil
	rep := auditwatch.Report{
		Enabled: true, Window: 4 * time.Hour, Merged: 1,
		Silent: []auditwatch.SilentAudit{{ID: "mg-f1b2", MergedAt: time.Now().Add(-12 * time.Hour), Silence: 12 * time.Hour}},
	}
	_, detail := auditSuccessorLine(rep, nil, cfg, time.Now())
	if !strings.Contains(detail, "no clean_verdict_tags are configured") {
		t.Errorf("detail = %q, want it to say there is no clean-verdict option here", detail)
	}
}

func TestFormatWindow(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{4 * time.Hour, "4h"},
		{90 * time.Minute, "1h30m"},
		{30 * time.Minute, "30m"},
		{0, "0m"},
	}
	for _, tc := range cases {
		if got := formatWindow(tc.in); got != tc.want {
			t.Errorf("formatWindow(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDoctorCheck_AuditSuccessorsLineIsPresent runs the real compiled binary.
// The check has to appear on every run — including on machines where it is not
// configured — because a detector that renders nothing when unconfigured is
// invisible in exactly the way its subject fails.
func TestDoctorCheck_AuditSuccessorsLineIsPresent(t *testing.T) {
	line, ok := doctorChecks(t, nil)[auditSuccessorCheckName]
	if !ok {
		t.Fatalf("no %q row in doctor --check; the detector must be visible even when inert", auditSuccessorCheckName)
	}
	if !strings.HasPrefix(line, "pass\t") {
		t.Errorf("unconfigured detector line = %q, want pass", line)
	}
	if !strings.Contains(line, "not configured") {
		t.Errorf("line = %q, want it to say it is not configured", line)
	}
}

// TestDoctorCheck_AuditSuccessorsFiresEndToEnd is the end-to-end positive
// control: a real config.toml, a real store on disk, and the real binary. It
// exists because every other test in this file could pass against a renderer
// wired to nothing.
func TestDoctorCheck_AuditSuccessorsFiresEndToEnd(t *testing.T) {
	home := t.TempDir()
	mgRoot := filepath.Join(home, ".macguffin")
	done := filepath.Join(mgRoot, "work", "done")
	if err := os.MkdirAll(done, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	repo := filepath.Join(home, "research", "onethird_program")
	item := `---
id: mg-f1b2
type: task
created: 2026-07-30T03:54:48Z
depends: [mg-8a12]
tags: [onethird, audit, independent-audit, mg-8a12-followup]
repo: ` + repo + `
assignee: pm-onethird
---

# INDEPENDENT AUDIT of whatever mg-8a12 produces
body
`
	if err := os.WriteFile(filepath.Join(done, "mg-f1b2.md"), []byte(item), 0o644); err != nil {
		t.Fatalf("writing item: %v", err)
	}
	result := filepath.Join(done, "mg-f1b2.result.json")
	if err := os.WriteFile(result, []byte(`{"completed_by":"refinery"}`), 0o644); err != nil {
		t.Fatalf("writing result: %v", err)
	}
	old := time.Now().Add(-12 * time.Hour)
	if err := os.Chtimes(result, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	xdg := t.TempDir()
	cfgDir := filepath.Join(xdg, "pogo")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	cfgBody := "[audit_successor]\nrepos = [\"" + repo + "\"]\naudit_tags = [\"independent-audit\"]\nclean_verdict_tags = [\"audit-clean\"]\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfgBody), 0o644); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}

	env := []string{"MG_ROOT=" + mgRoot, "XDG_CONFIG_HOME=" + xdg}
	line := doctorChecks(t, env)[auditSuccessorCheckName]
	if !strings.HasPrefix(line, "warn\t") {
		t.Fatalf("line = %q, want warn — an audit merged 12h ago with nothing referencing it must be reported", line)
	}
	if !strings.Contains(line, "mg-f1b2") {
		t.Errorf("line = %q, want it to name the silent audit", line)
	}

	// The other direction, from the same store: file a successor and the line
	// goes quiet. Proving it can fire is only half; a detector that fires on
	// everything is the same as one that fires on nothing.
	succ := `---
id: mg-repair
type: task
created: 2026-07-30T04:10:00Z
depends: [mg-f1b2]
tags: [onethird, audit-repair, mg-f1b2-followup]
repo: ` + repo + `
assignee: pm-onethird
---

# Repair from mg-f1b2's audit
body
`
	avail := filepath.Join(mgRoot, "work", "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatalf("mkdir available: %v", err)
	}
	if err := os.WriteFile(filepath.Join(avail, "mg-repair.md"), []byte(succ), 0o644); err != nil {
		t.Fatalf("writing successor: %v", err)
	}
	line = doctorChecks(t, env)[auditSuccessorCheckName]
	if !strings.HasPrefix(line, "pass\t") {
		t.Fatalf("line = %q, want pass once a successor exists", line)
	}
	if !strings.Contains(line, "1 answered by a successor") {
		t.Errorf("line = %q, want the population to show the audit as answered", line)
	}
}
