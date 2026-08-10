package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TestCheckStrandedUsageExit pins the one thing this file can assert without a
// board and a fleet: a malformed invocation is exit 2, distinct from both "no
// findings" (0) and "found something" (1).
//
// Collapsing usage into 0 is how a schedule reports a detector as green while it
// has not run since somebody renamed a flag. The rest of the command's behaviour
// is exercised in internal/strandwatch against real git repositories, where the
// interesting inputs — an item that is available while its work is on main, a
// polecat that is running right now — can actually be constructed.
func TestCheckStrandedUsageExit(t *testing.T) {
	bin := checkVerdictsBinary(t)

	for _, args := range [][]string{
		{"check-stranded", "a-positional-argument"},
		{"check-stranded", "--no-such-flag"},
	} {
		out, err := exec.Command(bin, args...).CombinedOutput()
		code := exitCodeOf(t, err)
		if code != exitStrandedUsage {
			t.Errorf("%v: exit %d, want %d (usage). A usage error read as 0 makes a schedule "+
				"report a detector that never ran as green.\n%s", args, code, exitStrandedUsage, out)
		}
	}
}

// TestCheckStrandedHelpNamesTheBlindSpot. The `git cherry` patch-id blind spot
// cost the mayor 65 false positives from the wrong instrument and nearly cost a
// second reader the same, from the right one. The next person to extend this
// command reads --help before the source, so the warning has to survive there.
func TestCheckStrandedHelpNamesTheBlindSpot(t *testing.T) {
	bin := checkVerdictsBinary(t)
	out, err := exec.Command(bin, "check-stranded", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("check-stranded --help: %v\n%s", err, out)
	}
	for _, want := range []string{
		"rev-list",   // the instrument that does not work
		"git cherry", // the one that mostly does
		"conflict",   // and where it stops working
		"mg done",    // the second row's remedy, which is not the first's
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("--help does not mention %q:\n%s", want, out)
		}
	}
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("not an exit error: %v", err)
	}
	return exitErr.ExitCode()
}
