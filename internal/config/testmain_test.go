package config

import (
	"os"
	"testing"
)

// TestMain clears any ambient POGO_HOME (e.g. exported by the developer's
// shell) before the package's tests run. Since mg-3dc3 every pogo state path
// derives from $POGO_HOME (falling back to $HOME/.pogo), and most tests
// isolate themselves by re-pointing HOME at a temp dir — an inherited
// POGO_HOME would defeat that isolation and leak test writes into the real
// state dir. Tests that exercise POGO_HOME semantics set it explicitly via
// t.Setenv.
//
// The same goes for every POGO_* variable Load applies over the config files:
// a test that asserts a file-derived or default value (port, autostart,
// extra_path, ...) reads the environment instead when the shell exports one.
// pogod exports POGO_PORT to every agent since mg-0c1a5 (mg-14a79). Tests that
// exercise an override set it themselves.
func TestMain(m *testing.M) {
	for _, k := range []string{
		"POGO_HOME",
		"POGO_PORT",
		"POGO_BIND",
		"POGO_AGENT_COMMAND",
		"POGO_AGENT_PROVIDER",
		"POGO_EXTRA_PATH",
		"POGO_AGENT_AUTOSTART",
	} {
		os.Unsetenv(k)
	}
	os.Exit(m.Run())
}
