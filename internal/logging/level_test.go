package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
)

// TestLevelFromParsesOperatorInput covers the values an operator can plausibly
// put in POGO_LOG_LEVEL. The two that matter most are the empty string and
// garbage: hclog.LevelFromString answers NoLevel for both, and NoLevel is not
// a quiet threshold — a logger built with it drops everything — so passing it
// through would turn an unset or mistyped variable into a silent daemon.
func TestLevelFromParsesOperatorInput(t *testing.T) {
	cases := []struct {
		in   string
		want hclog.Level
	}{
		{"", DefaultLevel},
		{"   ", DefaultLevel},
		{"trace", hclog.Trace},
		{"debug", hclog.Debug},
		{"info", hclog.Info},
		{"warn", hclog.Warn},
		{"error", hclog.Error},
		{"off", hclog.Off},
		// hclog is case- and whitespace-insensitive; so are we, because
		// nothing else in the daemon documents a casing for this.
		{"DEBUG", hclog.Debug},
		{"  Warn  ", hclog.Warn},
		// A typo costs the operator the level they asked for, not the daemon.
		{"verbose", DefaultLevel},
		{"2", DefaultLevel},
		{"nolevel", DefaultLevel},
	}
	for _, c := range cases {
		if got := LevelFrom(c.in); got != c.want {
			t.Errorf("LevelFrom(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestLevelReadsTheEnvironment checks the variable name itself, since that
// name is the whole public interface of this package and was published on
// gh#111 before the code existed.
func TestLevelReadsTheEnvironment(t *testing.T) {
	t.Setenv(EnvLogLevel, "debug")
	if got := Level(); got != hclog.Debug {
		t.Errorf("Level() = %v with %s=debug, want debug", got, EnvLogLevel)
	}
	t.Setenv(EnvLogLevel, "")
	if got := Level(); got != DefaultLevel {
		t.Errorf("Level() = %v with %s unset, want %v", got, EnvLogLevel, DefaultLevel)
	}
}

// TestDefaultLevelMatchesThePreviousHardcodedValue is the compatibility guard:
// before POGO_LOG_LEVEL existed, every logger in pogo was built with
// hclog.Info. An operator who sets nothing must see exactly what they saw
// before — the level split is the only change to their output.
func TestDefaultLevelMatchesThePreviousHardcodedValue(t *testing.T) {
	if DefaultLevel != hclog.Info {
		t.Errorf("DefaultLevel = %v, want info", DefaultLevel)
	}
}

// TestLevelDrivesARealLogger closes the loop: a resolved level, handed to
// hclog the way the three call sites hand it over, actually filters. A unit
// test on the parse alone would still pass if NoLevel leaked through, because
// NoLevel only misbehaves once a logger is built from it.
func TestLevelDrivesARealLogger(t *testing.T) {
	for _, c := range []struct {
		env       string
		wantDebug bool
		wantInfo  bool
	}{
		{env: "", wantDebug: false, wantInfo: true},
		{env: "debug", wantDebug: true, wantInfo: true},
		{env: "nonsense", wantDebug: false, wantInfo: true},
		{env: "warn", wantDebug: false, wantInfo: false},
	} {
		t.Setenv(EnvLogLevel, c.env)
		var buf bytes.Buffer
		l := hclog.New(&hclog.LoggerOptions{
			Level:      Level(),
			Output:     &buf,
			JSONFormat: true,
		})
		l.Debug("a debug line")
		l.Info("an info line")

		gotDebug, gotInfo := false, false
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("%s=%q produced a non-JSON line %q: %v", EnvLogLevel, c.env, line, err)
			}
			switch rec["@level"] {
			case "debug":
				gotDebug = true
			case "info":
				gotInfo = true
			}
		}
		if gotDebug != c.wantDebug || gotInfo != c.wantInfo {
			t.Errorf("%s=%q: debug=%v info=%v, want debug=%v info=%v",
				EnvLogLevel, c.env, gotDebug, gotInfo, c.wantDebug, c.wantInfo)
		}
	}
}
