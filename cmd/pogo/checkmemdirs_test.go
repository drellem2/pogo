package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckMemdirsUsageExit pins the family contract: a malformed invocation is
// exit 2, distinct from "nothing stranded" (0), "found something" (1) and
// "measured nothing" (3).
//
// Collapsing usage into 0 is how a schedule reports a detector as green while it
// has not run since somebody renamed a flag.
func TestCheckMemdirsUsageExit(t *testing.T) {
	bin := checkVerdictsBinary(t)

	for _, args := range [][]string{
		{"check-memdirs", "a-positional-argument"},
		{"check-memdirs", "--no-such-flag"},
	} {
		out, err := exec.Command(bin, args...).CombinedOutput()
		if code := exitCodeOf(t, err); code != exitMemdirsUsage {
			t.Errorf("%v: exit %d, want %d (usage)\n%s", args, code, exitMemdirsUsage, out)
		}
	}
}

// TestCheckMemdirsUnmeasuredIsNotClean is the clause that keeps this check
// honest, exercised end to end through the real binary rather than the library.
//
// An agent root with no agent directories probes nothing, and a run that probed
// nothing must NOT exit 0. Zero findings from a check that never looked renders
// identically to zero findings from a healthy fleet — and that is the same
// defect one level down from the one this command exists to report, since a
// stranded store is precisely a thing every instrument reads as fine.
func TestCheckMemdirsUnmeasuredIsNotClean(t *testing.T) {
	bin := checkVerdictsBinary(t)
	empty := t.TempDir()

	out, err := exec.Command(bin, "check-memdirs", "--agent-root", empty).CombinedOutput()
	code := exitCodeOf(t, err)
	if code != exitInstrumentFailure {
		t.Fatalf("exit %d, want %d (measured nothing)\n%s", code, exitInstrumentFailure, out)
	}
	if !strings.Contains(string(out), "MEASURED NOTHING") {
		t.Errorf("output does not say it measured nothing — a reader cannot tell this from "+
			"a clean run:\n%s", out)
	}
	// The clean run's own headline, which must not appear here. Matching the
	// bare word would false-positive on this report's "not a clean result".
	if strings.Contains(string(out), "check-memdirs: clean") {
		t.Errorf("output claims cleanliness on a run that probed nothing:\n%s", out)
	}
}

// TestCheckMemdirsCleanRunPrintsItsDenominator. A clean result has to carry how
// many store paths were probed, because that count is the only thing separating
// "checked and clean" from "the path model stopped matching and I found nothing
// to check". The number is the positive control a reader gets for free.
func TestCheckMemdirsCleanRunPrintsItsDenominator(t *testing.T) {
	bin := checkVerdictsBinary(t)
	root := t.TempDir()
	for _, name := range []string{"architect", "mayor"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	out, err := exec.Command(bin, "check-memdirs", "--agent-root", root).CombinedOutput()
	if code := exitCodeOf(t, err); code != 0 {
		t.Fatalf("exit %d, want 0 for agent dirs with no stores\n%s", code, out)
	}
	s := string(out)
	if !strings.Contains(s, "clean") {
		t.Errorf("clean run does not say so:\n%s", s)
	}
	if !strings.Contains(s, "probed") {
		t.Errorf("clean run does not print the number of paths probed, so a blind check and a "+
			"healthy one read identically:\n%s", s)
	}
}

// TestCheckMemdirsHelpNamesTheRemedyAndItsTrap. The next reader of this command
// arrives at --help before the source, and the single most expensive way to act
// on a finding is to move the notes wholesale into a loaded store: the batch
// that motivated this check contained a rule that had been refuted, and a bulk
// load would have delivered it reading as current. If the help does not say
// "triage, not copy", the obvious remedy is the harmful one.
func TestCheckMemdirsHelpNamesTheRemedyAndItsTrap(t *testing.T) {
	bin := checkVerdictsBinary(t)
	out, err := exec.Command(bin, "check-memdirs", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v\n%s", err, out)
	}
	s := string(out)
	for _, want := range []string{
		"AN EMPTY STORE IS NOT A FINDING",
		"CONSTRUCTED",
		"NOT AN AGE CHECK",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("--help does not carry %q:\n%s", want, s)
		}
	}
}
