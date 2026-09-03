package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestObserveOwnLogDestinationSeesItsOwnDescriptor is the POSITIVE CONTROL for
// the comparison. The condition it feeds fires on a NEGATIVE (fd 2 is not the
// named file), and a negative from an instrument never fired at a known
// positive says nothing: if os.SameFile were being handed the wrong arguments,
// every daemon would read "not writing" and the alarm would be permanent noise.
//
// It exercises the comparison directly rather than through
// observeOwnLogDestination, which reads the machine's installed plist and is
// therefore not a thing a test may depend on.
func TestObserveOwnLogDestinationSeesItsOwnDescriptor(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "pogod.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	self, err := f.Stat()
	if err != nil {
		t.Fatalf("stat descriptor: %v", err)
	}
	named, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat path: %v", err)
	}
	if !os.SameFile(self, named) {
		t.Fatal("a descriptor on a file does not compare equal to that file's path — the whole comparison is broken, and every 'not writing' verdict it produces is uninterpretable")
	}

	// The negative half, in the same shape: a DIFFERENT file that exists and is
	// non-empty, which is what the real one looks like.
	other := filepath.Join(dir, "elsewhere.log")
	if err := os.WriteFile(other, []byte("rich, correctly formatted, irrelevant\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	otherInfo, err := os.Stat(other)
	if err != nil {
		t.Fatalf("stat other: %v", err)
	}
	if os.SameFile(self, otherInfo) {
		t.Error("two different files compare as the same descriptor")
	}
}

// TestObserveOwnLogDestinationIsUndeterminedWithNoInstalledJob. A box that never
// installed the service — every test sandbox, every Linux host, a fresh
// machine — has nothing to compare and must neither raise nor clear. A
// condition that is always true on a whole class of host is a standing alarm
// with no transition, which is the one thing the annunciation constraint
// forbids most directly (see the A12 declination in conditions_test.go).
func TestObserveOwnLogDestinationIsUndeterminedWithNoInstalledJob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, determined := observeOwnLogDestination(); determined {
		t.Error("a HOME with no installed plist reports a determined reading — the sandbox would annunciate")
	}
}

// TestDescribeStderrNeverClaimsAPath. os.Stderr.Name() is "/dev/stderr"
// regardless of where the descriptor actually goes. Printing that inside a
// notice about confident wrong answers would be the defect reproducing itself
// in its own alarm text.
func TestDescribeStderrNeverClaimsAPath(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	fi, err := devnull.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	got := describeStderr(fi)
	if !strings.Contains(got, "character device") {
		t.Errorf("describeStderr(%s) = %q, want it named as a character device", os.DevNull, got)
	}
	if strings.Contains(got, "/dev/stderr") {
		t.Errorf("describeStderr claims the path /dev/stderr, which is what os.Stderr.Name() says on every platform regardless of the real destination: %q", got)
	}
}

// TestConditionLogNotWrittenIsActionable. Same bar every condition in this
// daemon is held to, applied to one that is not in the A-row enumeration and so
// is not covered by conditions_test.go's sweep.
func TestConditionLogNotWrittenIsActionable(t *testing.T) {
	c := conditionLogNotWritten("mayor", logDestination{
		JobLogPath: "/Users/x/Library/Logs/pogo/pogod.log",
		Stderr:     "a character device — a terminal or /dev/null.",
	})

	if c.To == "human" || c.To == "" {
		t.Errorf("routed to %q — conditions address the agent that can act, never `human` (988 unread)", c.To)
	}
	if c.Row != "mg-a19a" {
		t.Errorf("Row = %q; a condition outside the enumeration carries its originating work item so the event alone says why it exists", c.Row)
	}
	if c.Wake {
		t.Error("asks for a PTY wake. Only A2 has the argument for one: this condition does not stop mail from being read")
	}
	for _, want := range []string{
		"WHAT IT COSTS WHILE UNFIXED",
		"WHAT TO DO",
		"WHY THIS IS MAIL",
		"pogo service log",
		"pogo service supervision",
		"mg-a19a",
	} {
		if !strings.Contains(c.Body, want) {
			t.Errorf("body is missing %q", want)
		}
	}
	// The reason this detector is worth having at all, stated in the notice:
	// its own channel is not the one the fault breaks.
	if !strings.Contains(c.Body, "event spine") {
		t.Error("the body does not say that this notice reaches a channel the reported fault does not break — which is the only argument for annunciating this condition rather than logging it")
	}
}

// TestConditionFingerprintIgnoresThePid. A daemon restarted into the same wrong
// destination is the same unfixed condition. Fingerprinting on the pid would
// re-mail on every bounce, and a condition that mails on every bounce gets
// filtered — which is how the channel dies.
func TestConditionFingerprintIgnoresThePid(t *testing.T) {
	d := logDestination{JobLogPath: "/l/pogod.log", Stderr: "a character device"}
	a := conditionLogNotWritten("mayor", d)
	b := conditionLogNotWritten("mayor", d)
	if a.Fingerprint != b.Fingerprint || a.Fingerprint == "" {
		t.Errorf("fingerprints differ (%q vs %q) for the same destination", a.Fingerprint, b.Fingerprint)
	}
	other := conditionLogNotWritten("mayor", logDestination{JobLogPath: "/l/pogod.log", Stderr: "a pipe"})
	if other.Fingerprint == a.Fingerprint {
		t.Error("a materially different destination shares a fingerprint and would be suppressed behind the first")
	}
}

// TestAnnunciateLogDestinationIsSilentWhenUndetermined. An UNDETERMINED reading
// must neither raise nor clear.
//
// Both halves matter and they fail differently. Raising would put a permanent
// standing alarm on every host that never installed the service — the A12
// shape, an always-true precondition that never clears, which is the fastest
// route to the whole channel being muted. Clearing would let a plist that goes
// missing silently resolve a LIVE condition, so the fault would be reported
// once and then quietly retracted by its own worsening.
func TestAnnunciateLogDestinationIsSilentWhenUndetermined(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var mailed []string
	a, _ := newTestAnnunciator(t, func(to, from, subject, body string) error {
		mailed = append(mailed, subject)
		return nil
	}, nil)

	// Seed a live condition, then take an undetermined reading over the top of
	// it. The seeded state must survive: silence, not a clear.
	a.Raise(conditionLogNotWritten("mayor", logDestination{
		JobLogPath: "/l/pogod.log", Stderr: "a character device",
	}), time.Now())
	a.flush()
	if len(mailed) != 1 {
		t.Fatalf("seeding the condition mailed %d notices, want 1", len(mailed))
	}

	annunciateLogDestination(a, "mayor", time.Now())
	a.flush()

	if len(mailed) != 1 {
		t.Errorf("an undetermined reading mailed again (%v) — a host with no installed job would annunciate forever", mailed)
	}
	if _, stillLive := a.mem[logDestinationConditionID]; !stillLive {
		t.Error("an undetermined reading CLEARED a live condition — a plist going missing must not retract the notice that the log is unwritten")
	}
}
