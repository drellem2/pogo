package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDispatchCapShipsArmed(t *testing.T) {
	cfg := DefaultDispatchCapConfig()
	if !cfg.Armed() {
		t.Fatal("the shipped default is disarmed — the load incident this gate exists for would recur untouched")
	}
	if cfg.MaxPolecatsPerRepo != DefaultMaxPolecatsPerRepo {
		t.Errorf("MaxPolecatsPerRepo = %d, want %d", cfg.MaxPolecatsPerRepo, DefaultMaxPolecatsPerRepo)
	}
	if cfg.RefineryReserve != DefaultRefineryReserve {
		t.Errorf("RefineryReserve = %d, want %d", cfg.RefineryReserve, DefaultRefineryReserve)
	}
}

func TestEffectiveCap(t *testing.T) {
	for _, tc := range []struct {
		name            string
		cfg             DispatchCapConfig
		refineryHasWork bool
		want            int
	}{
		{"disarmed stays disarmed", DispatchCapConfig{}, true, 0},
		{"idle refinery holds nothing", DispatchCapConfig{MaxPolecatsPerRepo: 3, RefineryReserve: 1}, false, 3},
		{"busy refinery holds one", DispatchCapConfig{MaxPolecatsPerRepo: 3, RefineryReserve: 1}, true, 2},
		{"zero reserve holds nothing", DispatchCapConfig{MaxPolecatsPerRepo: 3}, true, 3},
		{"reserve equal to cap floors at one", DispatchCapConfig{MaxPolecatsPerRepo: 3, RefineryReserve: 3}, true, 1},
		{"reserve larger than cap floors at one", DispatchCapConfig{MaxPolecatsPerRepo: 2, RefineryReserve: 9}, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.EffectiveCap(tc.refineryHasWork); got != tc.want {
				t.Errorf("EffectiveCap(%v) = %d, want %d", tc.refineryHasWork, got, tc.want)
			}
		})
	}
}

// TestSameRepo covers the spellings a dispatcher actually produces. It is not
// a filesystem question — see SameRepo's comment for why no stat happens here.
func TestSameRepo(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"/dev/pogo", "/dev/pogo", true},
		{"/dev/pogo/", "/dev/pogo", true},
		{"/dev/pogo//", "/dev/pogo", true},
		{"/dev/pogo/.", "/dev/pogo", true},
		{" /dev/pogo ", "/dev/pogo", true},
		{"/dev/pogo", "/dev/pogonut", false},
		{"/dev/pogo", "/dev/pogo/sub", false},
		{"", "", false},
		{"", "/dev/pogo", false},
		{".", "/dev/pogo", false},
	} {
		if got := SameRepo(tc.a, tc.b); got != tc.want {
			t.Errorf("SameRepo(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestDispatchCapZeroIsAValueNotAnAbsence is the merge bug this section's
// "was it set" flags exist to prevent: `max_polecats_per_repo = 0` is how an
// operator disarms the gate, and a merge gated on `> 0` would silently restore
// the shipped 3 and leave them watching a daemon refuse spawns they turned the
// cap off to allow.
func TestDispatchCapZeroIsAValueNotAnAbsence(t *testing.T) {
	dir := t.TempDir()
	writeCapConfig(t, dir, "[dispatch]\nmax_polecats_per_repo = 0\nrefinery_reserve = 0\n")
	cfg := loadWithConfigDir(t, dir)
	if cfg.DispatchCap.MaxPolecatsPerRepo != 0 {
		t.Errorf("MaxPolecatsPerRepo = %d, want 0 — an explicit disarm was overwritten by the default",
			cfg.DispatchCap.MaxPolecatsPerRepo)
	}
	if cfg.DispatchCap.RefineryReserve != 0 {
		t.Errorf("RefineryReserve = %d, want 0", cfg.DispatchCap.RefineryReserve)
	}
	if cfg.DispatchCap.Armed() {
		t.Error("a config that disarmed the cap produced an armed one")
	}
}

func TestDispatchCapOverride(t *testing.T) {
	dir := t.TempDir()
	writeCapConfig(t, dir, "[dispatch]\nmax_polecats_per_repo = 6\n")
	cfg := loadWithConfigDir(t, dir)
	if cfg.DispatchCap.MaxPolecatsPerRepo != 6 {
		t.Errorf("MaxPolecatsPerRepo = %d, want 6", cfg.DispatchCap.MaxPolecatsPerRepo)
	}
	// The key the file did NOT name keeps its default — this config is merged
	// key by key, not file by file (mg-cf9e).
	if cfg.DispatchCap.RefineryReserve != DefaultRefineryReserve {
		t.Errorf("RefineryReserve = %d, want the default %d — an unnamed key was reset",
			cfg.DispatchCap.RefineryReserve, DefaultRefineryReserve)
	}
}

// TestNoConfigFileStillArmsTheCap. A daemon on a box with no config.toml must
// still enforce this, or the control is one missing file away from absent —
// the class of gap mg-da48 and mg-6c4b were both about.
func TestNoConfigFileStillArmsTheCap(t *testing.T) {
	cfg := loadWithConfigDir(t, t.TempDir())
	if !cfg.DispatchCap.Armed() {
		t.Fatal("an unconfigured daemon has no per-repo cap")
	}
	if cfg.DispatchCap.MaxPolecatsPerRepo != DefaultMaxPolecatsPerRepo {
		t.Errorf("MaxPolecatsPerRepo = %d, want %d", cfg.DispatchCap.MaxPolecatsPerRepo, DefaultMaxPolecatsPerRepo)
	}
}

// TestNegativeCapIsClampedToUnlimited. The two readings of `-1` are "no limit"
// and "refuse everything", and only one of those is recoverable without a
// second config edit by someone who can still dispatch.
func TestNegativeCapIsClampedToUnlimited(t *testing.T) {
	dir := t.TempDir()
	writeCapConfig(t, dir, "[dispatch]\nmax_polecats_per_repo = -1\nrefinery_reserve = -4\n")
	cfg := loadWithConfigDir(t, dir)
	if cfg.DispatchCap.MaxPolecatsPerRepo != 0 {
		t.Errorf("MaxPolecatsPerRepo = %d, want 0", cfg.DispatchCap.MaxPolecatsPerRepo)
	}
	if cfg.DispatchCap.RefineryReserve != 0 {
		t.Errorf("RefineryReserve = %d, want 0", cfg.DispatchCap.RefineryReserve)
	}
}

func writeCapConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// loadWithConfigDir points POGO_HOME at dir and loads. HOME and XDG_CONFIG_HOME
// go with it: POGO_HOME alone does not isolate config.toml, because the two
// layers are read from both locations (mg-cf9e), and a test that forgot the
// second would be reading Daniel's real config.
func loadWithConfigDir(t *testing.T, dir string) *Config {
	t.Helper()
	// POGO_HOME must not EQUAL HOME: that pair is the legacy shell-integration
	// spelling and PogoHome normalizes it to $HOME/.pogo (mg-3dc3), which would
	// silently look for the config somewhere the test never wrote it.
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("POGO_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	return Load()
}
