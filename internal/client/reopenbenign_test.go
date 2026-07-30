package client

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fakeMGReopen makes `mg reopen <id>` print out and exit non-zero, standing in
// for mg's refusal.
func fakeMGReopen(t *testing.T, out string) {
	t.Helper()
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "printf '%s' \"$1\"; exit 4", "sh", out)
	}
	t.Cleanup(func() { execCommand = old })
}

// TestReopenMGWorkItemAlreadyClaimedIsNotAFailure covers the second measured
// benign outcome (mg-5d3f): the refinery reopens a work item after a failed
// merge, and a live polecat's item is still claimed, so mg refuses. 18 of these
// landed in one log, all of them this outcome, all of them reading
// "failed to reopen work item".
//
// The point of the sentinel is that the refusal is the item ALREADY being in
// the state the reopen wanted, not a failure to get it there.
func TestReopenMGWorkItemAlreadyClaimedIsNotAFailure(t *testing.T) {
	fakeMGReopen(t, "Error: mg-5d3f: not done — it is already claimed (in progress).")

	err := ReopenMGWorkItem("mg-5d3f")
	if err == nil {
		t.Fatal("expected an error so the caller can still see the outcome")
	}
	if !errors.Is(err, ErrMGWorkItemNotDone) {
		t.Errorf("the measured benign refusal is not classified benign: %v", err)
	}
	// mg's own words survive so a reader can check the classification.
	if !strings.Contains(err.Error(), "already claimed (in progress)") {
		t.Errorf("error dropped mg's output: %v", err)
	}
}

// TestReopenMGWorkItemOtherRefusalsStayFailures is the reopen half of the
// acceptance criterion: a DIFFERENT mg reopen refusal must still read as a
// failure. Every case here would be swallowed by matching the command, the
// leading "Error: <id>:", or a substring of the benign wording.
func TestReopenMGWorkItemOtherRefusalsStayFailures(t *testing.T) {
	cases := []struct {
		name string
		out  string
	}{
		{"item does not exist", "Error: mg-5d3f: not found"},
		{"store is unreadable", "Error: mg-5d3f: cannot read work item: permission denied"},
		{"same shape, different state", "Error: mg-5d3f: not done — it is available (unclaimed)."},
		{"benign wording for a DIFFERENT id", "Error: mg-9999: not done — it is already claimed (in progress)."},
		{"benign wording plus a second problem",
			"Error: mg-5d3f: not done — it is already claimed (in progress).\nError: claim pid is stale"},
		{"mg is missing entirely", "sh: mg: command not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeMGReopen(t, tc.out)
			err := ReopenMGWorkItem("mg-5d3f")
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, ErrMGWorkItemNotDone) {
				t.Errorf("classified benign, so it would be demoted: %v", err)
			}
			if !strings.Contains(err.Error(), "mg reopen failed") {
				t.Errorf("failure lost its reporting: %v", err)
			}
		})
	}
}

// TestReopenMGWorkItemSuccessIsSilent guards the ordinary path: a successful
// reopen returns nil, so the demotion cannot have turned success into an error.
func TestReopenMGWorkItemSuccessIsSilent(t *testing.T) {
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = old })

	if err := ReopenMGWorkItem("mg-5d3f"); err != nil {
		t.Fatalf("ReopenMGWorkItem: %v", err)
	}
}
