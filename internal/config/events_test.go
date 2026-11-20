package config

import (
	"os"
	"path/filepath"
	"testing"
)

// drellem2/pogo#307: the work-item tag -> corr mapping is CONFIGURATION. The
// shipped default is "mail:", another prefix replaces it, and an explicit empty
// value turns it off — all without a code change.

func TestCorrTagPrefixDefaultIsMail(t *testing.T) {
	// No config file at all, and an empty one: both get the vanilla default.
	if got := loadWithConfigDir(t, t.TempDir()).Events.CorrTagPrefix; got != "mail:" {
		t.Errorf("no config file: CorrTagPrefix = %q, want the shipped default %q", got, "mail:")
	}
	dir := t.TempDir()
	writeCapConfig(t, dir, "")
	if got := loadWithConfigDir(t, dir).Events.CorrTagPrefix; got != DefaultCorrTagPrefix {
		t.Errorf("empty config file: CorrTagPrefix = %q, want %q", got, DefaultCorrTagPrefix)
	}
}

func TestCorrTagPrefixNonDefault(t *testing.T) {
	dir := t.TempDir()
	writeCapConfig(t, dir, "[events]\ncorr_tag_prefix = \"src:\"\n")
	cfg := loadWithConfigDir(t, dir)
	if cfg.Events.CorrTagPrefix != "src:" || !cfg.Events.CorrEnabled() {
		t.Errorf("CorrTagPrefix = %q (enabled=%v), want %q", cfg.Events.CorrTagPrefix, cfg.Events.CorrEnabled(), "src:")
	}
}

// TestCorrTagPrefixEmptyIsAValueNotAnAbsence is the merge bug the "was it set"
// flag prevents: `corr_tag_prefix = ""` is how an install disables the mapping,
// and a merge gated on non-empty would restore "mail:".
func TestCorrTagPrefixEmptyIsAValueNotAnAbsence(t *testing.T) {
	dir := t.TempDir()
	writeCapConfig(t, dir, "[events]\ncorr_tag_prefix = \"\"\n")
	cfg := loadWithConfigDir(t, dir)
	if cfg.Events.CorrTagPrefix != "" || cfg.Events.CorrEnabled() {
		t.Fatalf("CorrTagPrefix = %q — an explicit disable was overwritten by the default", cfg.Events.CorrTagPrefix)
	}
}

// A later layer ($POGO_HOME/config.toml) overrides an earlier one
// (~/.config/pogo/config.toml) key by key, including back to "unset".
func TestCorrTagPrefixLayers(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg", "pogo")
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "config.toml"), []byte("[events]\ncorr_tag_prefix = \"src:\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadWithConfigDir(t, dir).Events.CorrTagPrefix; got != "src:" {
		t.Fatalf("lower layer alone: CorrTagPrefix = %q, want %q", got, "src:")
	}
	writeCapConfig(t, dir, "[events]\ncorr_tag_prefix = \"\"\n")
	if got := loadWithConfigDir(t, dir).Events.CorrTagPrefix; got != "" {
		t.Errorf("upper layer's empty value: CorrTagPrefix = %q, want unset", got)
	}
}

// drellem2/pogo#310: corr_is_msg_id is a claim about what the tag values MEAN,
// so it stays with the prefix it was made about. Vanilla: on. A layer naming
// its own prefix drops it unless the same layer restates it.
func TestCorrIsMsgIDFollowsThePrefixItWasMadeAbout(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"no config", "", true},
		{"non-default prefix alone", "[events]\ncorr_tag_prefix = \"src:\"\n", false},
		{"non-default prefix restated", "[events]\ncorr_tag_prefix = \"src:\"\ncorr_is_msg_id = true\n", true},
		// No prefix value is special-cased: writing the default explicitly
		// is still a layer stating its own convention.
		{"default prefix written explicitly", "[events]\ncorr_tag_prefix = \"mail:\"\n", false},
		{"switch off alone", "[events]\ncorr_is_msg_id = false\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCapConfig(t, dir, tc.body)
			ev := loadWithConfigDir(t, dir).Events
			if ev.CorrIsMsgID != tc.want || ev.PointerCorr() != tc.want {
				t.Errorf("CorrIsMsgID = %v, PointerCorr = %v; want %v", ev.CorrIsMsgID, ev.PointerCorr(), tc.want)
			}
		})
	}
}

// Across layers: a lower layer's prefix change drops the claim, and an upper
// layer that only restates corr_is_msg_id brings it back.
func TestCorrIsMsgIDLayers(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg", "pogo")
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "config.toml"), []byte("[events]\ncorr_tag_prefix = \"src:\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCapConfig(t, dir, "")
	if ev := loadWithConfigDir(t, dir).Events; ev.PointerCorr() {
		t.Fatalf("lower layer src: with empty upper layer: PointerCorr = true, want false (%+v)", ev)
	}
	writeCapConfig(t, dir, "[events]\ncorr_is_msg_id = true\n")
	if ev := loadWithConfigDir(t, dir).Events; !ev.PointerCorr() || ev.CorrTagPrefix != "src:" {
		t.Errorf("upper layer restating corr_is_msg_id: %+v, want src: with PointerCorr", ev)
	}
}

func TestCorrFromTags(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tags   []string
		prefix string
		want   string
	}{
		{"default prefix", []string{"gh-issue", "mail:123.45.678"}, "mail:", "123.45.678"},
		{"first matching tag wins", []string{"mail:first", "mail:second"}, "mail:", "first"},
		{"non-default prefix", []string{"mail:m1", "src:s1"}, "src:", "s1"},
		{"non-default prefix ignores mail:", []string{"mail:m1"}, "src:", ""},
		{"unset prefix emits nothing", []string{"mail:m1"}, "", ""},
		{"no tags", nil, "mail:", ""},
		{"bare prefix is not a value", []string{"mail:", "mail:real"}, "mail:", "real"},
		{"prefix must lead", []string{"xmail:m1"}, "mail:", ""},
	} {
		if got := CorrFromTags(tc.tags, tc.prefix); got != tc.want {
			t.Errorf("%s: CorrFromTags(%q, %q) = %q, want %q", tc.name, tc.tags, tc.prefix, got, tc.want)
		}
	}
}
