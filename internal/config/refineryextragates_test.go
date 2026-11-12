package config

import (
	"reflect"
	"testing"
)

// mg-fb7df: [refinery.repos."<repo>"] extra_gates — host-local gates per repo.

func TestRefineryExtraGatesParse(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[refinery]\nenabled = true\n\n"+
		"[refinery.repos.\"pogo\"]\nextra_gates = [\"/opt/gates/a.sh\", \"/opt/gates/b.sh\"]\n\n"+
		"[refinery.repos.\"/srv/repos/other\"]\nextra_gates = [\"/opt/gates/c.sh\"]\n\n"+
		"[refinery]\nmax_concurrent_merges = 3\n")

	cfg := Load()
	rc := cfg.Refinery

	if got, want := rc.ExtraGatesFor("/anywhere/dev/pogo"), []string{"/opt/gates/a.sh", "/opt/gates/b.sh"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ExtraGatesFor(pogo by name) = %v, want %v", got, want)
	}
	if got, want := rc.ExtraGatesFor("/srv/repos/other/"), []string{"/opt/gates/c.sh"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ExtraGatesFor(by path) = %v, want %v", got, want)
	}
	if got := rc.ExtraGatesFor("/elsewhere/other"); got != nil {
		t.Errorf("a path key must not match by basename, got %v", got)
	}
	if got := rc.ExtraGatesFor("/dev/unlisted"); got != nil {
		t.Errorf("an unlisted repo has no extra gates, got %v", got)
	}
	// The [refinery] keys around the repo tables still parse as themselves.
	if !rc.Enabled || rc.MaxConcurrentMerges != 3 {
		t.Errorf("[refinery] keys lost around repo tables: %+v", rc)
	}
}

func TestRefineryExtraGatesPathKeyWinsOverName(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[refinery.repos.\"pogo\"]\nextra_gates = [\"by-name\"]\n"+
		"[refinery.repos.\"/dev/pogo\"]\nextra_gates = [\"by-path\"]\n")
	rc := Load().Refinery
	if got := rc.ExtraGatesFor("/dev/pogo"); !reflect.DeepEqual(got, []string{"by-path"}) {
		t.Errorf("path key should win, got %v", got)
	}
	if got := rc.ExtraGatesFor("/other/pogo"); !reflect.DeepEqual(got, []string{"by-name"}) {
		t.Errorf("name key should apply elsewhere, got %v", got)
	}
}

func TestRefineryExtraGatesDefaultIsEmpty(t *testing.T) {
	layeredSandbox(t)
	if g := Load().Refinery.ExtraGatesFor("/dev/pogo"); g != nil {
		t.Errorf("unconfigured install has extra gates: %v", g)
	}
}

// A later layer replaces the list for the repos it names and leaves the rest.
func TestRefineryExtraGatesLayering(t *testing.T) {
	xdg, home := layeredSandbox(t)
	write(t, xdg, "[refinery.repos.\"a\"]\nextra_gates = [\"base-a\"]\n[refinery.repos.\"b\"]\nextra_gates = [\"base-b\"]\n")
	write(t, home, "[refinery.repos.\"b\"]\nextra_gates = [\"home-b\"]\n")
	rc := Load().Refinery
	if got := rc.ExtraGatesFor("/x/a"); !reflect.DeepEqual(got, []string{"base-a"}) {
		t.Errorf("base layer's repo lost: %v", got)
	}
	if got := rc.ExtraGatesFor("/x/b"); !reflect.DeepEqual(got, []string{"home-b"}) {
		t.Errorf("higher layer should replace b's list: %v", got)
	}
}
