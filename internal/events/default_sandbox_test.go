package events

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// checkDefaultIsSandboxed is the acceptance check for the ratified test-safe
// default (ARCHITECTURE.md:433-447): under a test binary, with NO override set,
// resolution must not reach the live ~/.pogo/events.log.
//
// It is a function over a resolver rather than an inline assertion so that the
// SAME check can be run against the pre-fix resolver below. A check that has
// never been shown to fail is not a check — that is precisely how this defect
// survived two pollution incidents and one ratification.
//
// It returns the reason the check failed, or "" if it passed.
func checkDefaultIsSandboxed(t *testing.T, resolve func() (string, error)) string {
	t.Helper()

	got, err := resolve()
	if err != nil {
		return "resolution returned an error: " + err.Error()
	}
	live := livePath()
	if got == live {
		return "resolved to the LIVE log " + live
	}
	// Not just "not equal to the live file" — nothing under the pogo state dir
	// at all. A sibling of events.log in ~/.pogo is still operator state.
	home := config.PogoHome()
	if rel, relErr := filepath.Rel(home, got); relErr == nil && !strings.HasPrefix(rel, "..") {
		return "resolved inside the pogo state dir " + home + " (" + got + ")"
	}
	return ""
}

// preFixResolvePath is a verbatim replica of what resolvePath was before this
// fix: empty override falls through to the live log. It is the KNOWN-BAD
// FIXTURE. It exists only so the check above can be observed to fail, and if
// this ever stops failing the check has gone vacuous and the guard below is
// worthless.
func preFixResolvePath() (string, error) {
	overrideMu.RLock()
	p := overridePath
	overrideMu.RUnlock()
	if p != "" {
		return p, nil
	}
	return livePath(), nil
}

// TestDefaultLogPathIsSandboxedUnderTest is the mg-3f1b regression guard.
//
// Before the fix, `internal/events` was the one store that never adopted the
// default ratified at ARCHITECTURE.md:433 and implemented at
// internal/agent/witness.go:196 (and deliberately copied to
// internal/ghteardown/source.go:156): an empty override resolved to the
// then-exported DefaultLogPath(), so the zero value pointed at the operator's
// live audit log.
//
// The POSITIVE CONTROL runs first, against the pre-fix resolver, and asserts
// the check FAILS there. Only then is the real resolver's pass meaningful.
func TestDefaultLogPathIsSandboxedUnderTest(t *testing.T) {
	// No override — this is the whole point. Any test that sets one is
	// exercising isolation from other tests, not this default.
	SetLogPathForTesting("")

	// --- POSITIVE CONTROL --------------------------------------------------
	if reason := checkDefaultIsSandboxed(t, preFixResolvePath); reason == "" {
		t.Fatalf("positive control FAILED: the pre-fix resolver PASSED the sandbox check, " +
			"so the check cannot observe the defect it exists to catch and the assertion " +
			"below is vacuous")
	}

	// --- THE GUARD ---------------------------------------------------------
	if reason := checkDefaultIsSandboxed(t, resolvePath); reason != "" {
		live := livePath()
		t.Fatalf("REGRESSION (mg-3f1b): with no override set, events resolution %s; "+
			"under a test binary the live log (%s) must not be reachable from resolvePath at all",
			reason, live)
	}
}

// TestEmitWithNoOverrideLandsInTheSandbox is the end-to-end half: a caller that
// does nothing special — no SetLogPathForTesting, no EmitTo — must have its
// record land in this binary's sandbox.
//
// This asserts at RESOLUTION time and then reads back the record it wrote. It
// deliberately does NOT assert "the live log did not grow": that check sits
// downstream of the point where the pollution becomes indistinguishable from
// legitimate traffic, and it was measured against this very class and did not
// fire (twice). The choice is only visible where it is made.
func TestEmitWithNoOverrideLandsInTheSandbox(t *testing.T) {
	SetLogPathForTesting("")

	path, err := resolvePath()
	if err != nil {
		t.Fatalf("resolvePath: %v", err)
	}
	if reason := checkDefaultIsSandboxed(t, resolvePath); reason != "" {
		t.Fatalf("refusing to Emit: %s", reason)
	}

	Emit(context.Background(), Event{
		EventType: "test_sandbox_probe",
		Agent:     "cat-mg-3f1b",
		Details:   map[string]any{"probe": "mg-3f1b"},
	})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("sandbox log %s unreadable after Emit: %v", path, err)
	}
	if !strings.Contains(string(data), "test_sandbox_probe") {
		t.Fatalf("Emit with no override did not write its record to the sandbox log %s", path)
	}
}

// TestExplicitOverrideStillWins keeps the other half of the contract: the
// sandbox default must not have made `SetLogPathForTesting` a no-op. One test
// picking its own path is isolation from OTHER TESTS — a different and
// legitimate question the default does not answer.
func TestExplicitOverrideStillWins(t *testing.T) {
	want := filepath.Join(t.TempDir(), "chosen-events.log")
	SetLogPathForTesting(want)
	t.Cleanup(func() { SetLogPathForTesting("") })

	got, err := resolvePath()
	if err != nil {
		t.Fatalf("resolvePath: %v", err)
	}
	if got != want {
		t.Fatalf("explicit override ignored: resolvePath = %s, want %s", got, want)
	}
	if got == testDefaultLogPath() {
		t.Fatalf("explicit override was swallowed by the sandbox default (%s)", got)
	}
}

// TestTestDefaultLogPathIsStableAndOutsidePogoHome pins the two properties
// testDefaultLogPath owes its callers: one path for the life of the binary (so
// a writer and a reader in the same test agree), and never under PogoHome (so
// no failure mode inside it can land on operator state).
func TestTestDefaultLogPathIsStableAndOutsidePogoHome(t *testing.T) {
	first := testDefaultLogPath()
	if first != testDefaultLogPath() {
		t.Fatalf("testDefaultLogPath is not stable across calls: %s then %s", first, testDefaultLogPath())
	}
	if first == "" {
		t.Fatal("testDefaultLogPath returned an empty path")
	}
	home := config.PogoHome()
	if rel, err := filepath.Rel(home, first); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("testDefaultLogPath %s is inside the pogo state dir %s", first, home)
	}
}
