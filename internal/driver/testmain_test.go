package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandboxHome is the throwaway $HOME TestMain installs for the whole package.
var sandboxHome string

// TestMain clears any ambient POGO_HOME (e.g. exported by the developer's
// shell) before the package's tests run. Since mg-3dc3 every pogo state path
// derives from $POGO_HOME (falling back to $HOME/.pogo), and most tests
// isolate themselves by re-pointing HOME at a temp dir — an inherited
// POGO_HOME would defeat that isolation and leak test writes into the real
// state dir. Tests that exercise POGO_HOME semantics set it explicitly via
// t.Setenv.
//
// Clearing POGO_HOME alone was not enough, and that gap is what mg-5336
// closes. With POGO_HOME unset the state root becomes $HOME/.pogo, so a test
// that never re-points HOME reads the DEVELOPER'S LIVE state dir. Init() calls
// resolvePluginPath(), which falls back to config.PogoHome()/plugin when
// POGO_PLUGIN_PATH is unset, and hands it to discoverExternalPlugins — which
// stats that directory and go-plugin-launches every "pogo*" binary it finds
// there. setUp in driver_test.go calls Init() with no env isolation, so on a
// machine with a populated ~/.pogo/plugin the suite would execute the
// operator's real plugin binaries as a side effect of running `go test`, and
// TestPluginsLoad's "expected 2 builtins" assertion would fail for a reason
// that has nothing to do with the code under test. TestMain therefore
// re-points HOME at a throwaway tree, making hermeticity the package default
// rather than something each new test has to remember. Same structural fix as
// mg-6092 and mg-e8e7 in internal/agent.
//
// TestInitIgnoresCwdWithPogoBinaries sets POGO_HOME explicitly via t.Setenv
// and is unaffected by this; it keeps asserting its own thing.
func TestMain(m *testing.M) {
	os.Unsetenv("POGO_HOME")

	home, err := os.MkdirTemp("", "pogo-driver-home")
	if err != nil {
		panic("create temp home dir: " + err.Error())
	}
	sandboxHome = home
	os.Setenv("HOME", home)

	code := m.Run()

	os.RemoveAll(home)
	os.Exit(code)
}

// TestPluginPathIsSandboxed is the positive control for the isolation above:
// with POGO_PLUGIN_PATH unset — the configuration every unisolated test in
// this package runs under — the discovered plugin directory must resolve under
// the throwaway tree, never under the real ~/.pogo/plugin. Without this the
// isolation is an unverified claim: dropping the Setenv would leave every
// other test in the package green while the suite went back to scanning (and
// launching from) the operator's live plugin dir.
func TestPluginPathIsSandboxed(t *testing.T) {
	t.Setenv("POGO_PLUGIN_PATH", "")

	got := filepath.Clean(resolvePluginPath())
	if !strings.HasPrefix(got, sandboxHome+string(filepath.Separator)) {
		t.Errorf("resolvePluginPath() = %s, want a path under the sandbox home "+
			"%s; Init() would scan and launch plugins from the real ~/.pogo",
			got, sandboxHome)
	}
}
