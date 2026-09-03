package logliveness

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestProcessStderrPathReadsARealDescriptor is the POSITIVE CONTROL for the one
// reading this package's verdict rests on.
//
// On the live host the check returns DETACHED, which is a negative result — and
// a negative result from an instrument nobody has fired at a known-positive
// case says nothing. If lsof's output format changed, or its field flags were
// wrong, processStderrPath would return an error, Observe would set StderrOK
// false, and the verdict would be a permanent UNKNOWN that looks like a careful
// answer. So: spawn a child whose fd 2 is a file we chose, and require the
// reader to name that file.
func TestProcessStderrPathReadsARealDescriptor(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skipf("lsof unavailable: %v", err)
	}

	dir := t.TempDir()
	want := filepath.Join(dir, "child-stderr.log")
	f, err := os.Create(want)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	// `sleep` rather than a Go helper process: no build step, and its fd 2 is
	// whatever we hand it and nothing else.
	cmd := exec.Command("sleep", "30")
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Armed before the reading, not after it: a t.Fatalf below must not leave
	// a sleep behind for thirty seconds.
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	got, gerr := processStderrPath(cmd.Process.Pid)
	if gerr != nil {
		t.Fatalf("processStderrPath on a process with a known fd 2 failed: %v — the instrument is broken, and every DETACHED/UNKNOWN it produces elsewhere is uninterpretable", gerr)
	}
	if resolve(got) != resolve(want) {
		t.Errorf("processStderrPath = %q, want %q", got, want)
	}
}

// TestProcessStderrPathDistinguishesTheTwoWorldStates. The control above proves
// the reader can see a file. This proves it does not see the SAME file
// regardless — which is what an instrument that cannot separate the two states
// would do, and the reason mtime was rejected as the verdict input.
func TestProcessStderrPathDistinguishesTheTwoWorldStates(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skipf("lsof unavailable: %v", err)
	}

	dir := t.TempDir()
	logFile := filepath.Join(dir, "pogod.log")
	if err := os.WriteFile(logFile, []byte("pretend daemon output\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()

	// A process writing SOMEWHERE ELSE while the named log exists and has
	// content: the shape of the 2026-09 state, in miniature.
	cmd := exec.Command("sleep", "30")
	cmd.Stderr = devnull
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	stderrPath, gerr := processStderrPath(cmd.Process.Pid)
	if gerr != nil {
		t.Fatalf("processStderrPath: %v", gerr)
	}

	obs := Observation{
		LogPath: resolve(logFile), DaemonPID: cmd.Process.Pid, DaemonPIDOK: true,
		StderrPath: resolve(stderrPath), StderrOK: true,
		LogExists: true, LogSize: 22, LogMtime: time.Now(), Now: time.Now(),
	}
	if res := Check(obs); res.Verdict != Detached {
		t.Errorf("a process writing to %s while %s exists and is FRESH reads %s, want DETACHED — freshness won over identity", os.DevNull, logFile, res.Verdict)
	}
}

// TestProcessStartReadsThisProcess is the positive control for the context
// reading. It never reaches a verdict, but a start time silently misparsed
// would make the "has written nothing for its entire life" line fire or stay
// silent for the wrong reason.
func TestProcessStartReadsThisProcess(t *testing.T) {
	got, ok := processStart(os.Getpid())
	if !ok {
		t.Skip("ps -o lstart= unavailable or unparseable on this platform")
	}
	if d := time.Since(got); d < 0 || d > 24*time.Hour {
		t.Errorf("processStart(self) = %v, %v ago — outside any plausible range for a test process", got, d)
	}
}

// TestResolveLeavesNonFilesAlone. Blanking an unresolvable path would turn the
// legible mismatch "/dev/ttys007" into an empty string, and an empty string on
// one side of a comparison is how a mismatch becomes unreadable.
func TestResolveLeavesNonFilesAlone(t *testing.T) {
	for _, p := range []string{"/dev/ttys007", "/no/such/path/here.log", ""} {
		if got := resolve(p); got != p {
			t.Errorf("resolve(%q) = %q, want it unchanged", p, got)
		}
	}
}

// TestObserveOnAnEmptySandboxIsUnknown. With no pogod holding the sandbox's
// lockfile there is nothing to compare, and the answer must be UNKNOWN rather
// than a confident verdict about a file nobody owns.
func TestObserveOnAnEmptySandboxIsUnknown(t *testing.T) {
	res := Check(Observe(filepath.Join(t.TempDir(), "pogod.log"), "", time.Now()))
	if res.Verdict != Unknown {
		t.Errorf("Observe with no lock holder reads %s, want UNKNOWN", res.Verdict)
	}
	if res.OK() {
		t.Error("a sandbox with no daemon reports OK()")
	}
}
