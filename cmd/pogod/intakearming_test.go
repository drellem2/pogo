package main

import (
	"os"
	"strings"
	"testing"
)

// The precondition table, enumerated. Every combination has a stated outcome, so
// a change that drops one has to edit this table to keep compiling — the same
// argument conditions_test.go makes about the enumeration rows.
func TestIntakeArmingPreconditions(t *testing.T) {
	for _, tc := range []struct {
		name         string
		ghOnPath     bool
		credentialOK bool
		want         intakeArming
	}{
		{"gh and a credential", true, true, intakeArmed},
		{"gh but no credential", true, false, intakeBlockedNoCredential},
		{"no gh, credential somehow ok", false, true, intakeBlockedNoGH},
		{"neither", false, false, intakeBlockedNoGH},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideIntakeArming(tc.ghOnPath, tc.credentialOK); got != tc.want {
				t.Errorf("decideIntakeArming(%t, %t) = %q, want %q",
					tc.ghOnPath, tc.credentialOK, got, tc.want)
			}
		})
	}
}

// The ordering case gets its own test because it is the one that can be got
// wrong without anything looking wrong.
//
// With no `gh` on PATH, `gh auth token` cannot run either, so the credential
// predicate is false as a CONSEQUENCE of the missing binary. Reporting the
// credential would hand the operator `gh auth login` — a command they do not
// have — for a fault that is a plist PATH edit. That is the downstream symptom
// reported as the cause: this ticket's own defect, one level up, in its fix.
func TestAMissingBinaryIsNotReportedAsAMissingCredential(t *testing.T) {
	if got := decideIntakeArming(false, false); got != intakeBlockedNoGH {
		t.Fatalf("a host with neither gh nor a credential must be told about gh, got %q", got)
	}
	// And the two notices must not offer each other's remedy.
	noGH := conditionIntakeNotArmed("mayor", "exec: gh not found")
	noCred := conditionIntakeNoCredential("mayor", "GH_TOKEN: ABSENT (source=none)")
	if strings.Contains(noGH.Body, "gh auth login") {
		t.Error("the PATH notice offers `gh auth login`, which does not fix a PATH")
	}
	if !strings.Contains(noCred.Body, "gh auth login") {
		t.Error("the credential notice does not name its remedy")
	}
	if !strings.Contains(noCred.Body, "restart") && !strings.Contains(noCred.Body, "Restart") {
		t.Error("the credential notice does not say a restart is needed; the credential is " +
			"read once at startup, so a login alone changes nothing until then")
	}
}

// The gate is only worth having if main() actually consults it. main() is not
// callable from a test — it opens sockets, spawns agents and installs launchd
// jobs — so the wiring is asserted against the source, which is the same
// technique promptcli/surface.go uses for the same reason. A control that is
// present, correct and unreferenced is the mg-a03d shape, and the whole point of
// pulling this decision out of main() was to stop being unable to check it.
func TestMainConsultsTheArmingDecision(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	body := string(src)

	if !strings.Contains(body, "decideIntakeArming(") {
		t.Fatal("main.go does not call decideIntakeArming — the gate is unwired, so the " +
			"credential precondition exists only in a test")
	}
	for _, want := range []string{
		"intakeBlockedNoGH",
		"intakeBlockedNoCredential",
		"conditionIntakeNoCredential(",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("main.go never references %s, so that branch cannot fire in production", want)
		}
	}
	// The credential must reach the report as well as the gate. Without this the
	// watcher would arm correctly and still mail the old unclassified message.
	if !strings.Contains(body, "ghintake.CredentialFor(") {
		t.Error("main.go does not pass the credential predicate into the intake report; the " +
			"detector would arm on it and then render as if nothing had been checked")
	}
}
