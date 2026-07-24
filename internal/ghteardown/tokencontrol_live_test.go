package ghteardown

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/ghtoken"
)

// The live positive control for mg-03ea.
//
// # What it proves, and why nothing cheaper would
//
// The detector in this package is careful never to read a failed lookup as
// "closed" — it reports StateUnknown instead. That discipline is correct, and
// it is also what made the GH_TOKEN outage survivable-but-useless: under
// launchd's minimal environment pogod had no GH_TOKEN, every `gh issue view`
// exited non-zero, and the watcher returned INDETERMINATE for every carrier on
// every run. Blind, twice daily, indefinitely.
//
// Every unit test in this package injects a LookupFunc, so all of them pass
// just as happily when the real gh is unauthenticated. A detector that only
// ever returns indeterminate must not be trusted as passing, so the control
// that guards this fix has to do three things no unit test does:
//
//  1. call the REAL GHLookup against issues whose state is externally known —
//     drellem2/pogo#89 (closed) and #91 (open);
//  2. run under a faithful reproduction of the environment launchd hands pogod
//     (HOME and a bare PATH, and specifically NO GH_TOKEN), because the bug is
//     invisible from the ambient shell this test process runs in;
//  3. include the failing arm PERMANENTLY. The "raw" arm asserts the detector
//     really does go blind without the repair. Without it, a future regression
//     that made every arm indeterminate would still be green.
//
// It re-execs this test binary twice into that minimal environment: once
// without ghtoken.Ensure (expect indeterminate — the bug), once with it (expect
// real verdicts — the fix).
//
// Network and a real credential are required, so it is opt-in:
//
//	POGO_GH_TEARDOWN_CONTROL=1 go test ./internal/ghteardown/ -run TeardownTokenControl -v
//
// No secret is read, printed, or asserted on anywhere below: the arms are
// distinguished by ISSUE STATE, never by the token.
const (
	controlEnv = "POGO_GH_TEARDOWN_CONTROL"
	helperEnv  = "POGO_GHTD_HELPER"

	controlRepo   = "drellem2/pogo"
	closedIssue   = 89 // closed 2026-07-21, the miss that created this package
	openIssue     = 91 // open at time of writing
	helperArmRaw  = "raw"
	helperArmFixt = "repaired"
)

func TestTeardownTokenControl(t *testing.T) {
	if os.Getenv(helperEnv) != "" {
		return // this process is a helper; the helper test below does the work
	}
	if os.Getenv(controlEnv) != "1" {
		t.Skipf("set %s=1 to run the live gh-teardown token control (needs network and a gh credential)", controlEnv)
	}
	if runtime.GOOS != "darwin" {
		t.Skip("the launchd minimal-environment reproduction is macOS-specific")
	}

	ghPath, err := exec.LookPath("gh")
	if err != nil {
		t.Skipf("gh not on PATH: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("no home directory: %v", err)
	}

	// launchd's environment, reproduced. It hands pogod little more than HOME
	// and a minimal PATH; pogod's own pathenv.Ensure is what puts gh's directory
	// back, so including it here models the post-pathenv, pre-ghtoken state —
	// exactly the state the watcher ran in. There is deliberately no GH_TOKEN
	// and no GITHUB_TOKEN.
	minimal := []string{
		"HOME=" + home,
		"PATH=" + filepath.Dir(ghPath) + ":/usr/bin:/bin",
	}

	raw := runControlArm(t, helperArmRaw, minimal)
	t.Logf("raw (no GH_TOKEN repair): %s", raw)

	// If gh authenticates without GH_TOKEN on this host (a `gh auth login`
	// keyring entry, say), the premise of the control does not hold here and
	// asserting the failure would be asserting something false. Say so loudly
	// rather than passing quietly.
	if !strings.Contains(raw, "indeterminate") {
		t.Skipf("this host's gh authenticates without GH_TOKEN, so the blind-detector premise "+
			"cannot be reproduced here; raw arm reported: %s", raw)
	}
	if want := "89=indeterminate 91=indeterminate"; raw != want {
		t.Fatalf("raw arm: want %q (the bug), got %q", want, raw)
	}

	fixed := runControlArm(t, helperArmFixt, minimal)
	t.Logf("repaired (ghtoken.Ensure): %s", fixed)
	if want := "89=closed 91=miss"; fixed != want {
		t.Fatalf("repaired arm: want %q (real verdicts), got %q\n"+
			"The watcher is still blind under the launchd-minimal environment.", want, fixed)
	}
}

// runControlArm re-execs this test binary in the minimal environment, running
// only the helper test, and returns its verdict line.
func runControlArm(t *testing.T, arm string, env []string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot locate the test binary: %v", err)
	}
	cmd := exec.Command(self, "-test.run", "TestTeardownTokenControlHelper", "-test.v")
	cmd.Env = append(append([]string{}, env...), helperEnv+"="+arm)
	out, err := cmd.CombinedOutput()
	verdict := extractVerdict(string(out))
	if verdict == "" {
		t.Fatalf("helper arm %q produced no verdict (err=%v):\n%s", arm, err, out)
	}
	return verdict
}

const verdictPrefix = "VERDICT: "

func extractVerdict(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if s, ok := strings.CutPrefix(line, verdictPrefix); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// TestTeardownTokenControlHelper runs inside the re-exec'd minimal environment.
// It drives the REAL detector — Detect over GHLookup, the same two functions
// pogod's Watcher calls — over two synthetic carriers and prints one verdict
// line. It is a no-op in a normal test run.
func TestTeardownTokenControlHelper(t *testing.T) {
	arm := os.Getenv(helperEnv)
	if arm == "" {
		t.Skip("helper process for TestTeardownTokenControl; not a standalone test")
	}

	// The arm under test. Nothing else differs between the two runs.
	if arm == helperArmFixt {
		res := ghtoken.Ensure()
		// Source only — the value is never printed, and this output is captured
		// by the parent process.
		t.Logf("ghtoken.Ensure: %s", res)
	}

	carriers := []Carrier{
		{ID: "control-closed", Title: "known-closed control", Status: "done", Repo: controlRepo, Number: closedIssue},
		{ID: "control-open", Title: "known-open control", Status: "done", Repo: controlRepo, Number: openIssue},
	}
	rep := Detect(carriers, GHLookup)

	verdict := map[int]string{closedIssue: "closed", openIssue: "closed"} // clean unless classified
	for _, f := range rep.Misses {
		verdict[f.Carrier.Number] = "miss"
	}
	for _, f := range rep.Indeterminate {
		verdict[f.Carrier.Number] = "indeterminate"
	}
	for _, f := range rep.DeclaredOpen {
		verdict[f.Carrier.Number] = "declared_open"
	}

	fmt.Printf("%s%d=%s %d=%s\n", verdictPrefix, closedIssue, verdict[closedIssue], openIssue, verdict[openIssue])
}
