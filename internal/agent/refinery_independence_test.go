package agent

import (
	"os/exec"
	"strings"
	"testing"
)

// TestAgentPackageDoesNotImportRefinery is a structural regression test:
// the agent package must not depend on the refinery package, so that
// pogod can run with [refinery] enabled = false without dragging refinery
// code into the agent lifecycle.
//
// If you find yourself wanting to add an import to refinery here, push the
// coupling up to cmd/pogod/main.go (which orchestrates both) instead.
func TestAgentPackageDoesNotImportRefinery(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not available")
	}

	out, err := exec.Command("go", "list", "-f", "{{ join .Imports \"\\n\" }}", "github.com/drellem2/pogo/internal/agent").CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, string(out))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(line, "drellem2/pogo/internal/refinery") {
			t.Errorf("agent package must not import refinery (found: %s)", line)
		}
	}
}

// TestWorkitemPackageDoesNotImportRefinery enforces the same separation for
// the workitem package: a refinery-less pogod must still be able to manage
// work items.
func TestWorkitemPackageDoesNotImportRefinery(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not available")
	}

	out, err := exec.Command("go", "list", "-f", "{{ join .Imports \"\\n\" }}", "github.com/drellem2/pogo/internal/workitem").CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, string(out))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(line, "drellem2/pogo/internal/refinery") {
			t.Errorf("workitem package must not import refinery (found: %s)", line)
		}
	}
}
