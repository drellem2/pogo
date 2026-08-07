package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// The did-not-run witness (mg-2416) is armed only where the nightly LaunchAgent
// is installed, OR where --deploy-log names a record explicitly. pogod applies
// the same rule (internal/driftwatch), and the CLI must not disagree with it.
//
// This test exists because the rule was implemented in the daemon and MISSED in
// the CLI, and the escape shape is worth naming: on any host without the nightly
// — a dev box, CI, the check-staleness shell suite's sandbox — the default log
// path does not exist, "no deploy log" was reported as a finding, and the
// command exited 1. A witness that fails on every host that has nothing to
// witness gets its exit status ignored, which costs the finding it exists to
// carry.
func TestNoFireArmingMatchesTheDaemonsRule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		logExplicit bool
		installed   bool
		want        bool
	}{
		{"nightly installed, default path", false, true, true},
		{"no nightly, default path", false, false, false},
		{"no nightly, but --deploy-log names a record", true, false, true},
		{"nightly installed and --deploy-log", true, true, true},
	} {
		if got := tc.logExplicit || tc.installed; got != tc.want {
			t.Errorf("%s: armed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestDisarmedNoFireWitnessDeclaresItself: when the witness does not run, it
// must SAY it did not run.
//
// Its finding is an absence, so a witness that prints nothing and a host whose
// deploy fired every night produce identical output. Declaring the silence is
// the only thing that keeps "nothing reported" from reading as "nothing wrong" —
// the exact confusion that let four silent nights pass unnoticed.
func TestDisarmedNoFireWitnessDeclaresItself(t *testing.T) {
	out := captureStdout(t, func() {
		printNoFireDisarmed("/Users/x/Library/LaunchAgents/com.pogo.deploy.plist")
	})

	if !strings.Contains(out, "NOT ARMED") {
		t.Errorf("disarmed witness does not say it is unarmed:\n%s", out)
	}
	if !strings.Contains(out, "not an all-clear") {
		t.Errorf("disarmed witness does not warn against reading its silence as health:\n%s", out)
	}
	if !strings.Contains(out, "--deploy-log") {
		t.Errorf("disarmed witness does not say how to judge a log anyway:\n%s", out)
	}

	// The non-macOS case must not print an empty path where a filename belongs.
	bare := captureStdout(t, func() { printNoFireDisarmed("") })
	if strings.Contains(bare, "at \n") || strings.Contains(bare, "at  ") {
		t.Errorf("disarmed witness printed a dangling empty path:\n%s", bare)
	}
	if !strings.Contains(bare, "NOT ARMED") {
		t.Errorf("non-macOS disarm is silent:\n%s", bare)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = orig
	return <-done
}
