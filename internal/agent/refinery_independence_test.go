package agent

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
// They COULD reach the network, and until mg-117e they did. TestMain pins HOME
// under a throwaway root (internal/testsandbox), Go resolves GOMODCACHE off
// $HOME, so the `go list` above ran against an EMPTY module cache and tried to
// download every external module in the graph. Measured on the gate's own
// package-test chain (test.sh:64, `tmpdir-leak-guard.sh` -> `go-test-budget.sh
// ./...`) against a local counting proxy: 37 module requests per gate run, ALL
// of them from this package, at the ambient GOPROXY — which on that path was
// still the default `proxy.golang.org,direct`. mg-a9d8's pin closes the
// download path inside `scripts/pogo-sandbox`, which this path does not go
// through; mg-117e closes it in internal/testsandbox, which this path DOES go
// through, and TestTheImportQueryDoesNotReachTheDownloadPath below is that
// closure's assertion.
//
// This test survives the fix and is not made redundant by it, because the two
// answer different questions: that one says the path is closed, this one says
// the ANSWER does not depend on whether it is — which is what keeps the two
// structural assertions above from being decided by a resolver.
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

	// A HOME *and an empty GOMODCACHE* of this test's own, not TestMain's.
	//
	// The HOME half is original: reusing the package sandbox's would make this
	// control pass by never reaching the path it claims to close. The GOMODCACHE
	// half is mg-117e's, and without it this test QUIETLY STOPPED CONTROLLING
	// ANYTHING the moment the pin landed — the sandbox now exports GOMODCACHE
	// explicitly, so it survives a HOME override and the "closed" run would read
	// the same warm real cache as the ambient one. The cold cache is the
	// condition this test's whole claim is about, and it has to be stated now
	// that it is no longer a side effect of moving HOME.
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

	cold := filepath.Join(home, "cold-modcache")
	if err := os.MkdirAll(cold, 0o755); err != nil {
		t.Fatalf("staging an empty module cache for the closed-path run: %v", err)
	}

	closed, closedErr, err := list("HOME="+home, "GOMODCACHE="+cold, "GOPROXY=off")
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

	// The closed run must actually have WANTED a module, or "the answer does not
	// depend on the download path" is a claim about a run that never consulted
	// it. `go: downloading` is logged even under GOPROXY=off — Go decides it
	// needs the module before it discovers it cannot have it — so this is an
	// assertion about the control's own validity and it costs no network.
	if !strings.Contains(closedErr, "go: downloading") {
		t.Errorf("the closed-path run reached no module at all, so the comparison above "+
			"proves nothing about the download path.\nIt is probably reading a warm "+
			"cache: $GOMODCACHE was set to %s for that run.\nstderr:\n%s", cold, closedErr)
	}
}

// TestTheImportQueryDoesNotReachTheDownloadPath is mg-117e's assertion, in the
// package where the exposure was measured.
//
// The two structural tests above shell out to `go list`. Under TestMain's
// throwaway HOME that used to mean an empty GOMODCACHE and a real fetch from
// proxy.golang.org on every gate run — 37 module requests, all from here
// (mg-48d4). internal/testsandbox now pins GOMODCACHE at the developer's real
// cache with GOPROXY=off, and this is where that stops being a property of
// another package's implementation and becomes something this package checks.
//
// It asserts the pin at the PACKAGE level rather than deferring to
// testsandbox.Verify, because the pin FAILS OPEN by design: a box with no cache
// to share gets the old behaviour, and Verify passes vacuously there. This
// package is the one that measurably downloads, so it is the one that should
// say so out loud rather than inherit a silence.
func TestTheImportQueryDoesNotReachTheDownloadPath(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not available")
	}
	if !sandbox.ModulePinned() {
		t.Skipf("the package sandbox pinned no module cache — the pin fails open by "+
			"design and there is no cache on this box to share (sandbox root %s)",
			sandbox.Root)
	}

	cmd := exec.Command("go", "list", "-f", "{{ join .Imports \"\\n\" }}",
		"github.com/drellem2/pogo/internal/agent")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list under the package sandbox: %v\nstderr:\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "go: downloading") {
		t.Errorf("this package's `go list` reached the module download path.\n"+
			"That is mg-117e verbatim: TestMain moves $HOME, Go resolves GOMODCACHE off "+
			"$HOME, and the cache is empty — 37 real requests to proxy.golang.org per "+
			"gate run, every one of them from this package.\n"+
			"$GOMODCACHE=%s $GOPROXY=%s\nstderr:\n%s",
			os.Getenv("GOMODCACHE"), os.Getenv("GOPROXY"), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Error("go list returned no imports, so the check above observed nothing")
	}
}
