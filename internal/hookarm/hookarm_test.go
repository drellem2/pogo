package hookarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSettings puts a settings.local.json in dir with the given hook command
// registered under PostToolUse, mirroring what internal/claude installs.
func writeSettings(t *testing.T, dir, command string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` + command + `"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, SettingsRelPath), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveReportsOffWhenNothingIsRegistered(t *testing.T) {
	dir := t.TempDir()
	state, why := Resolve(dir, time.Now())
	if state != StateOff {
		t.Fatalf("state = %q, want %q (%s)", state, StateOff, why)
	}
	if !strings.Contains(why, "mg-d924") {
		t.Errorf("the reason does not say what the agent loses: %q", why)
	}
}

// TestResolveWillNotCallARegistrationArmed is the property the whole ticket is
// about. A settings file naming the hook says the TREE has the control; it says
// nothing about whether the live process ever loaded it. mg-385f found the same
// substitution by another route, and mg-503d exists because the fleet's true
// reading was "registered nowhere, armed nowhere" while every instrument was
// quiet.
func TestResolveWillNotCallARegistrationArmed(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "/usr/local/bin/pogo hook mail-recipient")

	state, why := Resolve(dir, time.Now())
	if state != StatePending {
		t.Fatalf("state = %q, want %q — a registration alone must never read as armed (%s)", state, StatePending, why)
	}
}

func TestResolveIsArmedOnlyOnAStampNewerThanTheProcess(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "/usr/local/bin/pogo hook mail-recipient")
	start := time.Now()

	if err := RecordFire(dir); err != nil {
		t.Fatal(err)
	}
	// RecordFire stamps with the wall clock; force the mtime past start so the
	// test does not depend on filesystem timestamp granularity.
	future := start.Add(time.Minute)
	if err := os.Chtimes(StampPath(dir), future, future); err != nil {
		t.Fatal(err)
	}

	state, why := Resolve(dir, start)
	if state != StateArmed {
		t.Fatalf("state = %q, want %q (%s)", state, StateArmed, why)
	}
	if !strings.Contains(why, "this session") {
		t.Errorf("the reason does not say the hook ran in this session: %q", why)
	}
}

// TestAStampFromAnEarlierAgentIsNotEvidence guards the one way this report
// could claim a live control from a dead one's leftovers. A crew agent's
// working directory outlives its process — ~/.pogo/agents/architect is the same
// directory across every restart — so yesterday's stamp is still sitting there
// when today's process starts. Reading it as armed would be exactly the mistake
// the report exists to catch, committed by the report.
func TestAStampFromAnEarlierAgentIsNotEvidence(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "/usr/local/bin/pogo hook mail-recipient")
	if err := RecordFire(dir); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(StampPath(dir), old, old); err != nil {
		t.Fatal(err)
	}

	state, why := Resolve(dir, time.Now())
	if state != StatePending {
		t.Fatalf("state = %q, want %q — a stamp predating the process belongs to an earlier agent (%s)",
			state, StatePending, why)
	}
	if !strings.Contains(why, "earlier agent") {
		t.Errorf("the reason does not name the stale-stamp case: %q", why)
	}
}

// TestUnreadableSettingsAreUnknownNotOff: "off" is a definite claim (nothing is
// registered). A check that could not read the file has measured nothing, and
// must not answer with a definite claim in either direction.
func TestUnreadableSettingsAreUnknownNotOff(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SettingsRelPath), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	state, _ := Resolve(dir, time.Now())
	if state != StateUnknown {
		t.Fatalf("state = %q, want %q", state, StateUnknown)
	}
}

func TestNoWorkingDirectoryIsUnknown(t *testing.T) {
	state, why := Resolve("", time.Now())
	if state != StateUnknown {
		t.Fatalf("state = %q, want %q (%s)", state, StateUnknown, why)
	}
}

// TestStampLandsBesideTheSettingsFile pins the location, because two processes
// have to agree on it: the hook writes it and pogod reads it, and they are
// different binaries that can be at different revisions.
func TestStampLandsBesideTheSettingsFile(t *testing.T) {
	dir := t.TempDir()
	if err := RecordFire(dir); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".claude", StampName)
	if got := StampPath(dir); got != want {
		t.Fatalf("StampPath = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("RecordFire did not write %s: %v", want, err)
	}
	// .claude/ is gitignored in this repo and in every worktree; a stamp
	// written anywhere else would turn up in an agent's diff.
	if filepath.Base(filepath.Dir(want)) != ".claude" {
		t.Errorf("stamp is not inside .claude/: %s", want)
	}
}

// TestRegisteredIgnoresSomebodyElsesPostToolUseHook: an agent's working
// directory can be a real repository whose settings hold a human's own hooks.
func TestRegisteredIgnoresSomebodyElsesPostToolUseHook(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "/usr/local/bin/some-other-tool --lint")
	if _, ok, err := Registered(dir); err != nil || ok {
		t.Fatalf("Registered = (%v, %v), want (false, nil)", ok, err)
	}
}
