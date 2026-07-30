package config

import (
	"testing"
	"time"
)

func TestAuditSuccessorEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  AuditSuccessorConfig
		want bool
	}{
		{"zero value ships inert", AuditSuccessorConfig{}, false},
		{"repos alone check nothing", AuditSuccessorConfig{Repos: []string{"/r"}}, false},
		{"tags alone would check the whole world", AuditSuccessorConfig{AuditTags: []string{"a"}}, false},
		{"both", AuditSuccessorConfig{Repos: []string{"/r"}, AuditTags: []string{"a"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AuditSuccessorEnabled(tc.cfg); got != tc.want {
				t.Errorf("AuditSuccessorEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAuditItem(t *testing.T) {
	cfg := AuditSuccessorConfig{
		Repos:     []string{"/x/research/onethird_program"},
		AuditTags: []string{"independent-audit"},
	}
	cases := []struct {
		name string
		repo string
		tags []string
		want bool
	}{
		{"covered repo, audit tag", "/x/research/onethird_program", []string{"onethird", "independent-audit"}, true},
		{"subdirectory of a covered repo", "/x/research/onethird_program/sub", []string{"independent-audit"}, true},
		// The sibling-prefix trap PairingCovers exists to avoid: a plain string
		// prefix test would have covered _v2, and the detector would report a
		// second program's audits under the first program's rule.
		{"sibling path is not covered", "/x/research/onethird_program_v2", []string{"independent-audit"}, false},
		{"uncovered repo", "/x/dev/pogo", []string{"independent-audit"}, false},
		{"covered repo, no audit tag", "/x/research/onethird_program", []string{"onethird", "research"}, false},
		{"tag matching is case- and space-insensitive", "/x/research/onethird_program", []string{" Independent-Audit "}, true},
		{"no repo on the item", "", []string{"independent-audit"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			covering, got := AuditItem(tc.repo, tc.tags, cfg)
			if got != tc.want {
				t.Fatalf("AuditItem = %v, want %v", got, tc.want)
			}
			if got && covering != cfg.Repos[0] {
				t.Errorf("coveringRepo = %q, want %q — a report has to name what put the item in scope", covering, cfg.Repos[0])
			}
		})
	}
}

func TestSucceedsItem(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		tags    []string
		depends []string
		target  string
		want    bool
	}{
		{"depends channel", "mg-succ", nil, []string{"mg-aud"}, "mg-aud", true},
		{"followup tag channel", "mg-succ", []string{"mg-aud-followup"}, nil, "mg-aud", true},
		{"both, as the real store writes them", "mg-succ", []string{"audit-repair", "mg-aud-followup"}, []string{"mg-aud"}, "mg-aud", true},
		// No pair tag is required, and this is the difference from
		// PairingSatisfiedBy: a repair ticket carries no audit marker, so
		// requiring one would report every real successor as absent.
		{"a plain repair ticket with no audit marker still succeeds", "mg-succ", []string{"audit-repair"}, []string{"mg-aud"}, "mg-aud", true},
		{"an item never succeeds itself", "mg-aud", []string{"mg-aud-followup"}, []string{"mg-aud"}, "mg-aud", false},
		{"self-reference is case-insensitive", "MG-AUD", []string{"mg-aud-followup"}, nil, "mg-aud", false},
		{"unrelated item", "mg-other", []string{"audit-repair"}, []string{"mg-zzzz"}, "mg-aud", false},
		{"empty target matches nothing", "mg-succ", []string{"anything"}, []string{"anything"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SucceedsItem(tc.id, tc.tags, tc.depends, tc.target); got != tc.want {
				t.Errorf("SucceedsItem = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPairingSatisfiedByStillRequiresThePairTag guards the refactor that pulled
// referencesItem out of PairingSatisfiedBy for SucceedsItem to share. The gate's
// extra requirement — the candidate must itself be a marked pair — must not have
// come out with it.
func TestPairingSatisfiedByStillRequiresThePairTag(t *testing.T) {
	if PairingSatisfiedBy("mg-succ", []string{"audit-repair"}, []string{"mg-aud"}, "mg-aud", []string{"independent-audit"}) {
		t.Error("PairingSatisfiedBy accepted a candidate carrying no pair tag; the gate and the detector must not have merged into one predicate")
	}
	if !PairingSatisfiedBy("mg-pair", []string{"independent-audit"}, []string{"mg-aud"}, "mg-aud", []string{"independent-audit"}) {
		t.Error("PairingSatisfiedBy rejected a correctly tagged pair")
	}
	if PairingSatisfiedBy("mg-pair", []string{"independent-audit"}, []string{"mg-aud"}, "mg-aud", nil) {
		t.Error("PairingSatisfiedBy with no pair tags configured must satisfy nothing")
	}
}

func TestAuditCleanVerdict(t *testing.T) {
	cfg := AuditSuccessorConfig{CleanVerdictTags: []string{"audit-clean"}}
	if !AuditCleanVerdict([]string{"independent-audit", "audit-clean"}, cfg) {
		t.Error("a tagged clean verdict was not recognised")
	}
	if AuditCleanVerdict([]string{"independent-audit"}, cfg) {
		t.Error("an untagged audit was read as carrying a clean verdict")
	}
	if AuditCleanVerdict([]string{"audit-clean"}, AuditSuccessorConfig{}) {
		t.Error("with no clean_verdict_tags configured there is no clean-verdict channel at all")
	}
}

func TestAuditWindowDefault(t *testing.T) {
	if got := (AuditSuccessorConfig{}).AuditWindow(); got != DefaultAuditSuccessorWindow {
		t.Errorf("unset window = %v, want %v", got, DefaultAuditSuccessorWindow)
	}
	if got := (AuditSuccessorConfig{Window: 90 * time.Minute}).AuditWindow(); got != 90*time.Minute {
		t.Errorf("configured window = %v, want 90m", got)
	}
	// A negative window is not a "no cap" spelling here — there is nothing to
	// cap — so it falls back rather than reporting every merged audit at once.
	if got := (AuditSuccessorConfig{Window: -time.Hour}).AuditWindow(); got != DefaultAuditSuccessorWindow {
		t.Errorf("negative window = %v, want the default", got)
	}
}

// TestDefaultWindowClearsTheCalibrationData pins the constant against the
// measurement it was derived from. 2026-07-30's slowest observed audit-to-
// successor lag was 2h05m; a default at or below that would report a healthy
// audit as silent. The comment on DefaultAuditSuccessorWindow states the data —
// this makes the statement fail if the number drifts away from it.
func TestDefaultWindowClearsTheCalibrationData(t *testing.T) {
	const slowestObserved = 125 * time.Minute // mg-f7bc, 2026-07-30
	if DefaultAuditSuccessorWindow <= slowestObserved {
		t.Fatalf("DefaultAuditSuccessorWindow = %v, which is not above the slowest successor actually observed (%v). Recalibrate against a real day before lowering it",
			DefaultAuditSuccessorWindow, slowestObserved)
	}
}

// TestLoadAuditSuccessorFromFile pins the config.toml surface end to end,
// through the same layered loader production uses.
func TestLoadAuditSuccessorFromFile(t *testing.T) {
	cfg := loadPairingConfig(t, `
[audit_successor]
repos = ["/r/prog", "/r/other"]
audit_tags = ["independent-audit"]
clean_verdict_tags = ["audit-clean"]
window = "90m"
`)
	as := cfg.AuditSuccessor
	if len(as.Repos) != 2 || as.Repos[0] != "/r/prog" || as.Repos[1] != "/r/other" {
		t.Errorf("repos = %v", as.Repos)
	}
	if len(as.AuditTags) != 1 || as.AuditTags[0] != "independent-audit" {
		t.Errorf("audit_tags = %v", as.AuditTags)
	}
	if len(as.CleanVerdictTags) != 1 || as.CleanVerdictTags[0] != "audit-clean" {
		t.Errorf("clean_verdict_tags = %v", as.CleanVerdictTags)
	}
	if as.Window != 90*time.Minute {
		t.Errorf("window = %v, want 90m", as.Window)
	}
}

// TestLoadWithoutAuditSuccessorSectionIsInert — the section is absent from every
// existing config.toml on every consumer, and that must keep meaning "no audit
// is checked", not "every merged item is".
func TestLoadWithoutAuditSuccessorSectionIsInert(t *testing.T) {
	cfg := loadPairingConfig(t, "[server]\nport = 10000\n")
	if AuditSuccessorEnabled(cfg.AuditSuccessor) {
		t.Errorf("a config with no [audit_successor] armed the detector: %+v", cfg.AuditSuccessor)
	}
}

// TestLoadAuditSuccessorBadWindowFallsBack: a typo'd duration must not parse to
// zero. Zero would mean "report every merged audit the instant it lands", so the
// loudest possible reading of a typo would also be the silent one.
func TestLoadAuditSuccessorBadWindowFallsBack(t *testing.T) {
	cfg := loadPairingConfig(t, `
[audit_successor]
repos = ["/r/prog"]
audit_tags = ["independent-audit"]
window = "4 hours"
`)
	if got := cfg.AuditSuccessor.AuditWindow(); got != DefaultAuditSuccessorWindow {
		t.Errorf("unparseable window yielded %v, want the calibrated default %v", got, DefaultAuditSuccessorWindow)
	}
}
