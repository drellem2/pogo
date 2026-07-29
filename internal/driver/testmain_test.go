package driver

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any of them resolves onto the developer's live tree.
//
// This package does not merely read live state, it EXECUTES from it. Init()
// calls resolvePluginPath(), which falls back to config.PogoHome()/plugin when
// POGO_PLUGIN_PATH is unset, and hands it to discoverExternalPlugins — which
// stats that directory and go-plugin-launches every "pogo*" binary it finds
// there. setUp in driver_test.go calls Init() with no env isolation of its own,
// so on a machine with a populated ~/.pogo/plugin the suite ran the operator's
// real plugin binaries as a side effect of `go test`, and TestPluginsLoad's
// "expected 2 builtins" assertion failed for a reason that had nothing to do
// with the code under test. That is mg-5336.
//
// mg-5336 fixed it by hand: os.Unsetenv("POGO_HOME") plus os.Setenv("HOME",
// os.MkdirTemp(...)). Correct, and still isolation-by-remembering — nothing
// checked that the override took, that the value did not resolve back onto the
// real tree, or that the other two variables were pinned at all. mg-0941 moves
// the envelope behind testsandbox so the next test written here inherits it.
//
// TestInitIgnoresCwdWithPogoBinaries sets POGO_HOME explicitly via t.Setenv and
// is unaffected by this; it keeps asserting its own thing.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("driver")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestPluginPathIsSandboxed is the positive control for the isolation above:
// with POGO_PLUGIN_PATH unset — the configuration every unisolated test in this
// package runs under — the discovered plugin directory must resolve under the
// throwaway tree, never under the real ~/.pogo/plugin. Without this the
// isolation is an unverified claim: dropping it would leave every other test in
// the package green while the suite went back to scanning (and launching from)
// the operator's live plugin dir.
func TestPluginPathIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	t.Setenv("POGO_PLUGIN_PATH", "")

	if got := resolvePluginPath(); !sandbox.Contains(got) {
		t.Errorf("resolvePluginPath() = %s, want a path under the sandbox root %s; "+
			"Init() would scan and launch plugins from the real ~/.pogo", got, sandbox.Root)
	}
}
