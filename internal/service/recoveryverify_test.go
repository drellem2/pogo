package service

// The tier-3 recovery agent's post-restart revision check (mg-ed4a).
//
// pogo-recovery.sh is a shell script that runs under launchd with a minimal
// environment, so it is exercised the way launchd runs it: as a subprocess,
// with stubbed `launchctl` and `pogo` binaries on a controlled PATH. Testing
// the Go side alone would leave the actual artifact — the file launchd execs —
// unasserted, which is the shape of gap this whole ticket is about.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// recoveryScriptPath locates the in-repo script from the package directory.
func recoveryScriptPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "scripts", "launchd", "pogo-recovery.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("recovery script not found at %s: %v", p, err)
	}
	return p
}

// writeStub drops an executable bash stub and returns its path.
func writeStub(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/bash\n"+body), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}

// pogoStub renders a `pogo` that knows the subcommand and returns the given
// verdict code from `service verify-revision`.
func pogoStub(verdictCode string) string {
	return `
if [ "$1" = "service" ] && [ "$2" = "verify-revision" ]; then
    for a in "$@"; do
        if [ "$a" = "--help" ]; then echo "usage: pogo service verify-revision"; exit 0; fi
    done
    echo "revision check from stub"
    exit ` + verdictCode + `
fi
echo "unknown command" >&2
exit 1
`
}

type recoveryRun struct {
	code   int
	output string
	dir    string
}

// runRecovery executes the script with one queued request and the given
// environment overrides, and returns its exit code and combined output.
func runRecovery(t *testing.T, env map[string]string) recoveryRun {
	t.Helper()

	root := t.TempDir()
	recoveryDir := filepath.Join(root, "recovery")
	queue := filepath.Join(recoveryDir, "queue")
	if err := os.MkdirAll(queue, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queue, "test.req"), []byte("requester=test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	// A launchctl that accepts the kickstart, unless the caller overrides it.
	launchctl := writeStub(t, binDir, "launchctl", "echo \"stub launchctl $*\"\nexit 0\n")

	cmd := exec.Command("/bin/bash", recoveryScriptPath(t))
	cmd.Env = append(os.Environ(),
		"POGO_RECOVERY_DIR="+recoveryDir,
		"LAUNCHCTL="+launchctl,
		// A PATH with only the stub dir plus the system essentials the script
		// genuinely uses (date, find, mv, touch). `pogo` is deliberately NOT
		// here unless a case puts it here.
		"PATH="+binDir+":/usr/bin:/bin",
		"HOME="+root,
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Give tests the bin dir so they can drop their own stubs.
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running recovery script: %v\n%s", err, out)
	}
	return recoveryRun{code: code, output: string(out), dir: recoveryDir}
}

// runRecoveryWithPogo is runRecovery with a `pogo` stub installed.
func runRecoveryWithPogo(t *testing.T, stubBody string, env map[string]string) recoveryRun {
	t.Helper()
	dir := t.TempDir()
	pogo := writeStub(t, dir, "pogo", stubBody)
	if env == nil {
		env = map[string]string{}
	}
	env["POGO_CLI"] = pogo
	return runRecovery(t, env)
}

// The happy path: the kickstart lands AND the daemon that came back is the
// revision launchd was configured to exec.
func TestRecoveryDrainAgreesWhenTheRightRevisionCameBack(t *testing.T) {
	run := runRecoveryWithPogo(t, pogoStub("0"), nil)

	if run.code != 0 {
		t.Fatalf("exit %d, want 0\n%s", run.code, run.output)
	}
	if !strings.Contains(run.output, "revision check AGREES") {
		t.Fatalf("output does not state the verdict:\n%s", run.output)
	}
	assertArchived(t, run, "processed")
}

// THE POINT OF THE TICKET. The kickstart succeeds, the requests drain, the
// daemon comes back — running the wrong code. Before mg-ed4a this drain exited
// 0 and logged "kickstart succeeded" with nothing after it.
func TestRecoveryDrainFailsWhenTheRestartPutTheWrongCodeBack(t *testing.T) {
	run := runRecoveryWithPogo(t, pogoStub("1"), nil)

	if run.code != 1 {
		t.Fatalf("exit %d, want 1 — a restart that reinstated the wrong revision must not exit 0\n%s", run.code, run.output)
	}
	if !strings.Contains(run.output, "revision check DIFFERS") {
		t.Fatalf("output does not name the verdict:\n%s", run.output)
	}
	if !strings.Contains(run.output, "WRONG CODE BACK") {
		t.Fatalf("the DIFFERS line does not say what happened in words:\n%s", run.output)
	}
	// The kickstart DID happen and the request WAS serviced, so it is archived
	// as processed and the rate limit is stamped. Re-queueing on a bad revision
	// would loop against an artifact a restart cannot fix.
	assertArchived(t, run, "processed")
	if _, err := os.Stat(filepath.Join(run.dir, "last_restart")); err != nil {
		t.Fatalf("last_restart not written after a real kickstart: %v — without it the rate limit is disarmed and this can loop", err)
	}
}

// No `pogo` at all: UNKNOWN, said out loud, with a distinct exit code. Not a
// pass, and not the DIFFERS code either — the two owe different actions.
func TestRecoveryDrainReportsUnknownWhenItCannotAsk(t *testing.T) {
	// A name that cannot resolve anywhere. Leaving it as the default "pogo"
	// would let a real CLI on the runner's PATH answer, and the test would
	// then be probing the live daemon — nondeterministic, and not what this
	// case is about.
	run := runRecovery(t, map[string]string{"POGO_CLI": "pogo-that-is-not-installed"})

	if run.code != 3 {
		t.Fatalf("exit %d, want 3 (UNKNOWN)\n%s", run.code, run.output)
	}
	if !strings.Contains(run.output, "revision check UNKNOWN") {
		t.Fatalf("an unmeasurable check produced no UNKNOWN line:\n%s", run.output)
	}
	if strings.Contains(run.output, "DIFFERS") {
		t.Fatalf("a check that could not run reported a disagreement:\n%s", run.output)
	}
	assertArchived(t, run, "processed")
}

// A `pogo` predating this ticket exits 1 on the unknown subcommand — the same
// code that means DIFFERS. Reading that as "your daemon is running the wrong
// code" would be a confidently wrong alarm from the one check that must not
// produce them.
func TestAnOldCLIIsUnknownNotDisagreement(t *testing.T) {
	old := `
echo "Error: unknown command \"verify-revision\" for \"pogo service\"" >&2
exit 1
`
	run := runRecoveryWithPogo(t, old, nil)

	if run.code != 3 {
		t.Fatalf("exit %d, want 3 (UNKNOWN) — an old CLI is not a stale daemon\n%s", run.code, run.output)
	}
	if !strings.Contains(run.output, "predates mg-ed4a") {
		t.Fatalf("output does not explain that the CLI is the problem:\n%s", run.output)
	}
	if strings.Contains(run.output, "WRONG CODE BACK") {
		t.Fatalf("an old CLI was reported as a daemon running the wrong revision:\n%s", run.output)
	}
}

// The escape hatch is opt-OUT and it announces itself. A skipped check that
// looked like a passing one would be the original defect with an extra step.
func TestSkippingTheCheckSaysSoRatherThanPassingQuietly(t *testing.T) {
	run := runRecovery(t, map[string]string{"POGO_RECOVERY_VERIFY_REVISION": "0"})

	if run.code != 0 {
		t.Fatalf("exit %d, want 0 when the check is explicitly disabled\n%s", run.code, run.output)
	}
	if !strings.Contains(run.output, "revision check SKIPPED") {
		t.Fatalf("a disabled check left no trace:\n%s", run.output)
	}
	if !strings.Contains(run.output, "did NOT establish") {
		t.Fatalf("the SKIPPED line does not say what was not established:\n%s", run.output)
	}
}

// Regression guard on the pre-existing contract: a kickstart that FAILS still
// archives to failed/ and exits with the kickstart's code, and does not reach
// the revision check — there is no daemon to ask about.
func TestAFailedKickstartStillArchivesToFailedAndSkipsTheRevisionCheck(t *testing.T) {
	root := t.TempDir()
	recoveryDir := filepath.Join(root, "recovery")
	queue := filepath.Join(recoveryDir, "queue")
	if err := os.MkdirAll(queue, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queue, "test.req"), []byte("requester=test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	launchctl := writeStub(t, binDir, "launchctl", "echo 'stub: kickstart refused' >&2\nexit 5\n")
	pogo := writeStub(t, binDir, "pogo", pogoStub("0"))

	cmd := exec.Command("/bin/bash", recoveryScriptPath(t))
	cmd.Env = append(os.Environ(),
		"POGO_RECOVERY_DIR="+recoveryDir,
		"LAUNCHCTL="+launchctl,
		"POGO_CLI="+pogo,
		"PATH="+binDir+":/usr/bin:/bin",
		"HOME="+root,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running recovery script: %v\n%s", err, out)
	}

	if code != 5 {
		t.Fatalf("exit %d, want 5 (the kickstart's own code)\n%s", code, out)
	}
	if strings.Contains(string(out), "revision check") {
		t.Fatalf("the revision check ran after a kickstart that never happened:\n%s", out)
	}
	assertArchived(t, recoveryRun{dir: recoveryDir, output: string(out)}, "failed")
}

func assertArchived(t *testing.T, run recoveryRun, where string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(run.dir, where))
	if err != nil {
		t.Fatalf("reading %s/: %v", where, err)
	}
	if len(entries) != 1 {
		t.Fatalf("%s/ holds %d entries, want 1\n%s", where, len(entries), run.output)
	}
	if left, _ := os.ReadDir(filepath.Join(run.dir, "queue")); len(left) != 0 {
		names := make([]string, 0, len(left))
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Fatalf("queue still holds %v after the drain", names)
	}
}
