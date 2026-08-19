package agent

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/testtmp"
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

// TestTheImportQueryDoesNotDependOnTheDownloadPath is the control for a claim
// the two tests above make silently, and which mg-a9d8's changelog was read as
// making on their behalf: that they answer a STRUCTURAL question and therefore
// cannot be turned red by the network.
//
// They can reach the network, and on this fleet they do. TestMain pins HOME
// under a throwaway root (internal/testsandbox), Go resolves GOMODCACHE off
// $HOME, so the `go list` above runs against an EMPTY module cache and tries to
// download every external module in the graph. Measured on the gate's own
// package-test chain (test.sh:64, `tmpdir-leak-guard.sh` -> `go-test-budget.sh
// ./...`) against a local counting proxy: 37 module requests per gate run, ALL
// of them from this package, at the ambient GOPROXY — which on that path is
// still the default `proxy.golang.org,direct`. mg-a9d8's pin closes the
// download path inside `scripts/pogo-sandbox`, which this path does not go
// through.
//
// What saves it is narrower than "the cache is warm", and it is the thing worth
// pinning: `.Imports` is read from the main module's own source files, so the
// failed downloads are logged to stderr and change neither the answer nor the
// exit status. Measured: with the resolver dead (GOPROXY at a name that cannot
// resolve) the whole test.sh:64 chain still exits 0 with zero FAILs.
//
// GOPROXY=off rather than an unresolvable host: it closes the same path with no
// DNS and no egress at all, so this control cannot itself become weather. The
// comparison is against a live ambient run rather than a checked-in list,
// because the point is that the two answers are the SAME, not what either says.
func TestTheImportQueryDoesNotDependOnTheDownloadPath(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not available")
	}

	const pkg = "github.com/drellem2/pogo/internal/agent"

	// Separate streams, not CombinedOutput: `go: downloading ...` goes to
	// stderr, and folding it into stdout would compare the noise this test
	// exists to tolerate instead of the import list.
	list := func(extraEnv ...string) (string, string, error) {
		cmd := exec.Command("go", "list", "-f", "{{ join .Imports \"\\n\" }}", pkg)
		cmd.Env = append(os.Environ(), extraEnv...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}

	ambient, _, err := list()
	if err != nil {
		t.Fatalf("go list with the ambient download path: %v", err)
	}

	// A HOME of this test's own, not TestMain's: the two tests above have
	// already run `go list` under the package sandbox with the download path
	// OPEN, which downloads for real and leaves the sandbox cache warm. Reusing
	// it would make this control pass by never reaching the path it claims to
	// close, and it would pass in whichever order the file is read.
	//
	// testtmp.Dir + testtmp.RemoveAll rather than t.TempDir(), and this test is
	// exactly the case that rule was written for (mg-60eb): a fake $HOME that a
	// `go` invocation may populate is a tree Go writes READ-ONLY, and
	// os.RemoveAll — which is all t.TempDir's cleanup is — stops at the first
	// such file. GOPROXY=off should mean nothing is written here, but "should"
	// is how 148 undeletable sandbox roots and 120 MB of $TMPDIR happened, and
	// this file runs inside the gate's own leak guard.
	home, err := testtmp.Dir("agent-importquery-home")
	if err != nil {
		t.Fatalf("private HOME for the closed-path run: %v", err)
	}
	t.Cleanup(func() { testtmp.RemoveAll(home) })

	closed, closedErr, err := list("HOME="+home, "GOPROXY=off")
	if err != nil {
		t.Fatalf("go list with the download path CLOSED failed: %v\n"+
			"That is the regression this test exists for: these structural import "+
			"assertions would then be decided by whether a module fetch succeeded, "+
			"and a resolver blip would land as a defect in this package.\nstderr:\n%s",
			err, closedErr)
	}

	if closed != ambient {
		t.Errorf("go list returned a different import set with the download path closed.\n"+
			"open:\n%s\nclosed:\n%s\nstderr:\n%s", ambient, closed, closedErr)
	}
	if strings.TrimSpace(closed) == "" {
		t.Error("go list returned NO imports with the download path closed, so the two " +
			"assertions above would pass without asserting anything")
	}

	// Not an assertion: whether a fetch is even attempted is a property of the
	// sandbox HOME, and removing that exposure would be an improvement this
	// control must not report as a failure. Logged so the state is visible to
	// whoever reads a run of this file.
	if strings.Contains(closedErr, "go: downloading") {
		t.Logf("this package's `go list` reaches the module download path under the "+
			"sandbox HOME — the answer is unaffected, but the traffic is real:\n%s", closedErr)
	}
}
