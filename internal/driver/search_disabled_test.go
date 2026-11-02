package driver

import (
	"testing"
)

// TestInitLeavesSearchPluginOutWhenDisabled covers the mg-cabf4 default:
// with search indexing off, Init must not register pogo-plugin-search, so
// /plugins (GetPluginPaths) does not list it and no ProcessProject fan-out —
// the startup index pass, `pogo visit` registration — ever reaches it.
// GetPlugin must also refuse it rather than fall through to startPlugin, which
// would try to exec the name as an external plugin binary.
func TestInitLeavesSearchPluginOutWhenDisabled(t *testing.T) {
	t.Setenv("POGO_PLUGIN_PATH", t.TempDir())
	prev := SearchEnabled()
	t.Cleanup(func() { SetSearchEnabled(prev) })

	SetSearchEnabled(false)
	Init()
	defer Kill()

	for _, p := range GetPluginPaths() {
		if IsSearchPlugin(p) {
			t.Errorf("search plugin %q registered with search indexing disabled; paths=%v", p, GetPluginPaths())
		}
	}
	if Interfaces["pogo-plugin-diagnostics"] == nil {
		t.Errorf("diagnostics plugin missing: disabling search must not drop other builtins; paths=%v", GetPluginPaths())
	}
	if got := GetPlugin(SearchPluginName); got != nil {
		t.Errorf("GetPlugin(%q) = non-nil with search disabled", SearchPluginName)
	}
	if _, started := clients[SearchPluginName]; started {
		t.Errorf("GetPlugin(%q) tried to start it as an external plugin", SearchPluginName)
	}

	// Positive control: the same instrument sees the plugin when enabled, so
	// the absence above is the gate and not a broken lookup.
	SetSearchEnabled(true)
	Init()
	found := false
	for _, p := range GetPluginPaths() {
		if IsSearchPlugin(p) {
			found = true
		}
	}
	if !found {
		t.Fatalf("control: search plugin not registered with search enabled; paths=%v", GetPluginPaths())
	}
	if GetPlugin(SearchPluginName) == nil {
		t.Fatalf("control: GetPlugin(%q) nil with search enabled", SearchPluginName)
	}
}

func TestIsSearchPlugin(t *testing.T) {
	cases := map[string]bool{
		"pogo-plugin-search":                 true,
		"/usr/local/lib/pogo-plugin-search":  true,
		"pogo-plugin-diagnostics":            false,
		"/opt/pogo-plugin-search-dir/pogo-x": false,
	}
	for path, want := range cases {
		if got := IsSearchPlugin(path); got != want {
			t.Errorf("IsSearchPlugin(%q) = %v, want %v", path, got, want)
		}
	}
}
