package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// agentMarker is a distinctive agent command so the cleanup sweep below cannot
// reap an unrelated process.
const agentMarker = "sleep 61347"

// TestUpgradeBoot_AutoStartsCoordinatorAsMayor boots the real pogod binary
// against a genuine v0.3.0-era config.toml — [agents] present, coordinator and
// worker keys absent, autostart on (the shipped default) — and asserts the FIRST
// boot of the flipped build auto-starts the coordinator as "mayor".
//
// This is the regression the unit tests could not see (mg-bc47). The migration
// guard was always correct in isolation; the bug was that both binaries pushed
// role names resolved from the live Default* consts into process-wide state
// before running it. On boot 1 pogod therefore auto-started an agent literally
// named "ringmaster", armed the stall watcher on "ringmaster", and addressed
// refinery coordinator mail to a "ringmaster" mailbox nobody read — while
// writing coordinator = "mayor" into config.toml in the same second. Boot 2
// self-healed off the pinned config, stranding boot 1's coordinator as a stray
// agent with an orphaned mailbox.
//
// It runs the binary rather than main()'s pieces because the defect lived purely
// in statement order inside main().
func TestUpgradeBoot_AutoStartsCoordinatorAsMayor(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real pogod; skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	bin := filepath.Join(t.TempDir(), "pogod-under-test")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building pogod: %v\n%s", err, out)
	}

	sb := t.TempDir()
	state := filepath.Join(sb, "state")
	ws := filepath.Join(sb, "ws")
	for _, d := range []string{state, ws, filepath.Join(sb, ".config")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The config a v0.3.0 install carries: role keys never existed there.
	cfgPath := filepath.Join(state, "config.toml")
	cfgBody := fmt.Sprintf("[agents]\nautostart = true\ncommand = %q\n", agentMarker)
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(sb, "pogod.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	// Sandbox every state path so the live daemon on :10000 is untouched: HOME,
	// XDG_CONFIG_HOME and POGO_HOME are redirected and the port is private.
	cmd := exec.Command(bin, "-port", strconv.Itoa(freePort(t)))
	cmd.Dir = ws
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(),
		"HOME="+sb,
		"XDG_CONFIG_HOME="+filepath.Join(sb, ".config"),
		"POGO_HOME="+state,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting pogod: %v", err)
	}

	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_, _ = cmd.Process.Wait()
		}
		// pogod's StopAll should have reaped them; belt and braces.
		_ = exec.Command("pkill", "-f", agentMarker).Run()
	}()

	// Wait for the auto-start sweep to report, then shut down. Reading the log
	// as it grows keeps the test bounded even if the sweep never runs.
	if !waitForLog(t, logPath, "pogod: auto-started ", 60*time.Second) {
		t.Fatalf("pogod never auto-started an agent within the timeout\n--- log ---\n%s", readFile(t, logPath))
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_, _ = cmd.Process.Wait()
	stopped = true

	logs := readFile(t, logPath)

	// Bare literals throughout: comparing against config.DefaultCoordinator
	// would make this test follow a future default flip instead of catching it.
	if strings.Contains(logs, "auto-started ringmaster") {
		t.Errorf("boot 1 auto-started a coordinator named 'ringmaster' on an install pinned to 'mayor'"+
			" — role names resolved before the pin\n--- log ---\n%s", logs)
	}
	if !strings.Contains(logs, "auto-started mayor") {
		t.Errorf("boot 1 did not auto-start the coordinator as 'mayor'\n--- log ---\n%s", logs)
	}
	if strings.Contains(logs, "stall watcher enabled (agent=ringmaster") {
		t.Errorf("boot 1 armed the stall watcher on 'ringmaster' while pinning 'mayor'\n--- log ---\n%s", logs)
	}

	pinned := readFile(t, cfgPath)
	if !strings.Contains(pinned, `coordinator = "mayor"`) {
		t.Errorf("boot 1 did not pin coordinator = \"mayor\":\n%s", pinned)
	}
	if !strings.Contains(pinned, `worker = "polecat"`) {
		t.Errorf("boot 1 did not pin worker = \"polecat\":\n%s", pinned)
	}
}

// freePort returns a port that was free a moment ago. The listener is closed
// before pogod binds, so this races in principle; in practice the window is a
// few microseconds and the alternative (passing a listener down) would mean
// reworking pogod's flag surface for a test.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitForLog polls path until it contains want, or the deadline passes.
func waitForLog(t *testing.T, path, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), want) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
