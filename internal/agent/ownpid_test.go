package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// pidCaptureCommand writes "<shell pid> <POGO_PID> <POGO_PID seen by a child>"
// to path. The child is the stand-in for the harness's tool shell, which is
// where `mg reclaim` actually runs.
func pidCaptureCommand(path string) []string {
	return []string{"/bin/sh", "-c", `echo "$$ $POGO_PID $(/bin/sh -c 'echo $POGO_PID')" > "$1"`, "sh", path}
}

func readPIDCapture(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid capture %s: %v", path, err)
	}
	f := strings.Fields(string(data))
	if len(f) != 3 {
		t.Fatalf("pid capture %q: want 3 fields", data)
	}
	return f
}

// An agent's $POGO_PID is its own pid — the pid pogod records as Agent.PID —
// on spawn and again on restart (mg-4a941). Before this it was unset, so mg
// stamped every claim taken from inside an agent with mg's own pid, which is
// dead a moment later.
//
// The inherited environment and the caller's --env both carry a wrong value,
// so the assertion only passes if the trampoline assigns it last.
func TestSpawnExportsTheAgentsOwnPID(t *testing.T) {
	testsandbox.Isolate(t)
	t.Setenv(OwnPIDEnv, "1")
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	dir := t.TempDir()
	spawnCapture := filepath.Join(dir, "spawn.pid")
	respawnCapture := filepath.Join(dir, "respawn.pid")

	a, err := reg.Spawn(SpawnRequest{
		Name:    "own-pid",
		Type:    TypePolecat,
		Command: pidCaptureCommand(spawnCapture),
		Env:     []string{OwnPIDEnv + "=2"},
		BaseEnv: []string{OwnPIDEnv + "=3"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitDone(t, a)
	a.Command = pidCaptureCommand(respawnCapture)
	b, err := reg.Respawn("own-pid")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	waitDone(t, b)

	for what, c := range map[string]struct {
		path string
		pid  int
	}{"spawn": {spawnCapture, a.PID}, "respawn": {respawnCapture, b.PID}} {
		got := readPIDCapture(t, c.path)
		want := strconv.Itoa(c.pid)
		if got[0] != want {
			t.Errorf("%s: the harness ran as pid %s but pogod recorded %s — the trampoline did not exec in place", what, got[0], want)
		}
		if got[1] != want {
			t.Errorf("%s: POGO_PID = %q, want %s (the agent's own pid)", what, got[1], want)
		}
		if got[2] != want {
			t.Errorf("%s: a child of the harness saw POGO_PID = %q, want %s — a tool shell's `mg reclaim` would not stamp the agent", what, got[2], want)
		}
	}
	if a.Command[0] != "/bin/sh" || strings.Contains(strings.Join(a.Command, " "), ownPIDTrampoline) {
		t.Errorf("the stored command was rewritten (%q); the trampoline belongs to the exec only, or a restart would wrap twice", a.Command)
	}
}

// A command that does not resolve must still fail the spawn. Wrapping it would
// start a shell that exits 127: a spawn that reports success for an agent that
// can never run.
func TestSpawnOfAMissingBinaryStillFails(t *testing.T) {
	testsandbox.Isolate(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	if _, err := reg.Spawn(SpawnRequest{
		Name:    "own-pid-missing",
		Type:    TypeCrew,
		Command: []string{"/nonexistent/pogo-no-such-binary-4a941"},
	}); err == nil {
		t.Fatal("Spawn of an absolute path that does not exist returned no error")
	}
}

// exportOwnPID runs the program exec.Command resolved, with its argv intact.
func TestExportOwnPIDKeepsTheResolvedProgramAndArgs(t *testing.T) {
	cmd := exec.Command("sh", "-c", "true", "x y")
	resolved := cmd.Path
	exportOwnPID(cmd)
	if cmd.Path != "/bin/sh" {
		t.Fatalf("Path = %q, want /bin/sh", cmd.Path)
	}
	want := []string{"sh", "-c", ownPIDTrampoline, "sh", resolved, "-c", "true", "x y"}
	if strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("Args = %q, want %q", cmd.Args, want)
	}

	for _, name := range []string{"pogo-no-such-binary-4a941", "/nonexistent/pogo-no-such-binary-4a941"} {
		missing := exec.Command(name)
		exportOwnPID(missing)
		if missing.Path == "/bin/sh" {
			t.Errorf("%s: an unrunnable command was wrapped; its spawn error would be lost", name)
		}
	}
}
