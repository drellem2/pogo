package config

import (
	"os"
	"path/filepath"
	"testing"
)

// loadPairingConfig writes a config.toml containing body and Loads it, isolating
// XDG_CONFIG_HOME the way the rest of this package's file tests do.
func loadPairingConfig(t *testing.T, body string) *Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	pogoDir := filepath.Join(dir, "pogo")
	if err := os.MkdirAll(pogoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load()
}

// TestPairingCoversIsPathAware pins the boundary a string-prefix test would get
// wrong. `onethird_program` must not cover `onethird_program_v2`: a sibling
// directory silently inheriting another program's policy is the kind of coverage
// nobody would ever notice was wrong.
func TestPairingCoversIsPathAware(t *testing.T) {
	repos := []string{"/r/onethird_program", "/r/other/"}
	tests := []struct {
		repo string
		want bool
	}{
		{"/r/onethird_program", true},
		{"/r/onethird_program/", true},
		{"/r/onethird_program/sub/dir", true},
		{"/r/onethird_program_v2", false},
		{"/r/onethird_programme", false},
		{"/r/other", true},
		{"/r/elsewhere", false},
		{"", false},
	}
	for _, tt := range tests {
		if _, got := PairingCovers(tt.repo, repos); got != tt.want {
			t.Errorf("PairingCovers(%q) = %v, want %v", tt.repo, got, tt.want)
		}
	}
	if _, got := PairingCovers("/r/onethird_program", nil); got {
		t.Error("an empty repo list covered something; the gate must be inert by default")
	}
}

// TestPairingOwedDefaultsOn is the fail-closed-on-the-repo ruling, pinned: with
// no require_tags, EVERY item in a covered repo owes a pair. Requiring a
// positive act to create the obligation is what failed twice.
func TestPairingOwedDefaultsOn(t *testing.T) {
	cfg := DispatchPairingConfig{
		Repos:      []string{"/r/prog"},
		PairTags:   []string{"independent-audit"},
		WaiverTags: []string{"audit-waived"},
	}
	tests := []struct {
		name string
		repo string
		tags []string
		want bool
	}{
		{"untagged item in a covered repo", "/r/prog", nil, true},
		{"arbitrary tags in a covered repo", "/r/prog", []string{"research", "geometry"}, true},
		{"item outside the covered repo", "/r/elsewhere", []string{"research"}, false},
		{"item with no repo at all", "", []string{"research"}, false},
		{"the pair itself is exempt", "/r/prog", []string{"independent-audit"}, false},
		{"a visibly waived item is exempt", "/r/prog", []string{"research", "audit-waived"}, false},
		{"tag matching is case/space insensitive", "/r/prog", []string{" Independent-Audit "}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := PairingOwed(tt.repo, tt.tags, cfg); got != tt.want {
				t.Errorf("PairingOwed(%q, %v) = %v, want %v", tt.repo, tt.tags, got, tt.want)
			}
		})
	}
}

// TestPairingOwedNarrowedByRequireTags — a deployment that wants the older
// repo+tag shape rather than fail-closed-on-the-repo can still have it.
func TestPairingOwedNarrowedByRequireTags(t *testing.T) {
	cfg := DispatchPairingConfig{
		Repos:       []string{"/r/prog"},
		RequireTags: []string{"research"},
		PairTags:    []string{"independent-audit"},
	}
	if _, owed := PairingOwed("/r/prog", []string{"research"}, cfg); !owed {
		t.Error("a research item in a covered repo does not owe a pair")
	}
	if _, owed := PairingOwed("/r/prog", []string{"ops"}, cfg); owed {
		t.Error("require_tags did not narrow the obligation")
	}
}

// TestPairingOwedInertWithoutPairTags — a rule that names repos but no pair tag
// can never be satisfied by any item, so it must not create an obligation. The
// gate logs the misconfiguration; the predicate simply declines to fire.
func TestPairingOwedInertWithoutPairTags(t *testing.T) {
	cfg := DispatchPairingConfig{Repos: []string{"/r/prog"}}
	if _, owed := PairingOwed("/r/prog", []string{"research"}, cfg); owed {
		t.Error("an unsatisfiable rule created an obligation; it would deadlock the repo")
	}
}

// TestPairingSatisfiedBy covers both reference channels the store actually uses,
// and the ways a candidate must NOT count.
func TestPairingSatisfiedBy(t *testing.T) {
	pair := []string{"independent-audit"}
	tests := []struct {
		name    string
		id      string
		tags    []string
		depends []string
		want    bool
	}{
		{
			name: "depends channel", id: "mg-aaaa",
			tags: []string{"independent-audit"}, depends: []string{"mg-target"}, want: true,
		},
		{
			name: "followup-tag channel", id: "mg-aaaa",
			tags: []string{"independent-audit", "mg-target-followup"}, want: true,
		},
		{
			name: "references the target but is not a pair", id: "mg-aaaa",
			tags: []string{"followup"}, depends: []string{"mg-target"}, want: false,
		},
		{
			name: "a pair that references something else", id: "mg-aaaa",
			tags: []string{"independent-audit"}, depends: []string{"mg-other"}, want: false,
		},
		{
			name: "an item cannot pair with itself", id: "mg-target",
			tags: []string{"independent-audit"}, depends: []string{"mg-target"}, want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PairingSatisfiedBy(tt.id, tt.tags, tt.depends, "mg-target", pair)
			if got != tt.want {
				t.Errorf("PairingSatisfiedBy(%v) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestDispatchPairingShipsNoProgramPolicy is the platform/configuration split,
// asserted rather than described. The audit stage is explicitly onethird-only
// and must not be baked into what every consumer runs, so the zero value — the
// value every unconfigured deployment gets — must place no obligation on
// anything.
func TestDispatchPairingShipsNoProgramPolicy(t *testing.T) {
	var zero DispatchPairingConfig
	if len(zero.Repos) != 0 || len(zero.PairTags) != 0 ||
		len(zero.RequireTags) != 0 || len(zero.WaiverTags) != 0 {
		t.Fatalf("the shipped default carries policy: %+v", zero)
	}
	for _, repo := range []string{
		"/Users/daniel/research/onethird_program",
		"/Users/daniel/dev/pogo",
		"/anything",
	} {
		if _, owed := PairingOwed(repo, []string{"research"}, zero); owed {
			t.Errorf("the shipped default puts an obligation on %q", repo)
		}
	}
}

// TestLoadDispatchPairingFromFile pins the config.toml surface end to end,
// through the same layered loader production uses.
func TestLoadDispatchPairingFromFile(t *testing.T) {
	cfg := loadPairingConfig(t, `
[dispatch_pairing]
repos = ["/r/prog", "/r/other"]
require_tags = ["research"]
pair_tags = ["independent-audit"]
waiver_tags = ["audit-waived"]
`)
	dp := cfg.DispatchPairing
	if len(dp.Repos) != 2 || dp.Repos[0] != "/r/prog" || dp.Repos[1] != "/r/other" {
		t.Errorf("repos = %v", dp.Repos)
	}
	if len(dp.RequireTags) != 1 || dp.RequireTags[0] != "research" {
		t.Errorf("require_tags = %v", dp.RequireTags)
	}
	if len(dp.PairTags) != 1 || dp.PairTags[0] != "independent-audit" {
		t.Errorf("pair_tags = %v", dp.PairTags)
	}
	if len(dp.WaiverTags) != 1 || dp.WaiverTags[0] != "audit-waived" {
		t.Errorf("waiver_tags = %v", dp.WaiverTags)
	}
}

// TestLoadWithoutDispatchPairingSectionIsInert — the section is absent from
// every existing config.toml on every consumer, and that must keep meaning
// "nothing is required".
func TestLoadWithoutDispatchPairingSectionIsInert(t *testing.T) {
	cfg := loadPairingConfig(t, "[server]\nport = 10000\n")
	if repos := cfg.DispatchPairing.Repos; len(repos) != 0 {
		t.Errorf("a config with no [dispatch_pairing] armed the gate: %v", repos)
	}
}
