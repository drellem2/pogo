package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// The fallback socket dir, end to end through main() (mg-a997).
//
// The unit tests for the two halves live in internal/config (which path is
// derived) and internal/agent (what is recorded and what is swept). This file
// exists because neither of them can fail for the reason this defect actually
// had: the arrangement was correct in the pieces and main() never called them.
//
// It also runs against a REAL pogod boot because that is the only thing that
// produced the leak. Measured 2026-08-12 with $TMPDIR pinned, three directories
// per `go test ./...`, and all three were pogod binaries these tests build:
//
//	pogo-agents-716cd851   TestStallWatchGate_BootDirections/unconfigured
//	pogo-agents-f3a69e8e   TestStallWatchGate_BootDirections/configured
//	pogo-agents-aa9d4b5c   TestUpgradeBoot_AutoStartsCoordinatorAsMayor
//
// Every one of those POGO_HOMEs was a t.TempDir() the testing package removed
// when the test ended. That is the fact the sweep is built on, and it is also
// what settles the option mg-a997 was filed to decide: shortening the
// TEST-SIDE POGO_HOME — rooting internal/testsandbox at /tmp — would have fixed
// none of these three, because none of them comes from a testsandbox root.

// fallbackSocketLine is the line main() logs when POGO_HOME cannot hold its own
// sockets. The path it names is the assertion target: reading it back beats
// re-deriving it here, which would only prove this file agrees with itself.
var fallbackSocketLine = regexp.MustCompile(`agent attach sockets live in (\S+) instead`)

// deepPogoHome returns an existing directory too deep to hold
// "<root>/agents/sockets" under sun_path, so a pogod rooted there must fall
// back. It nests until that is true rather than assuming t.TempDir() is long:
// it is on darwin (/var/folders/...) and is not on linux.
func deepPogoHome(t *testing.T) string {
	t.Helper()
	// 58 is the largest POGO_HOME that still fits: 103 (sun_path) - 30
	// (config's reserved "/<24-byte name>.sock") - 15 ("/agents/sockets").
	// Spelled out rather than imported because config keeps the arithmetic
	// unexported, and a copy that drifts makes this test nest one component too
	// few and quietly stop exercising the fallback — which the socket-dir
	// assertion below would catch.
	dir := t.TempDir()
	for len(dir) <= 58 {
		dir = filepath.Join(dir, "deeper")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	return dir
}

// bootFallbackPogod boots bin with POGO_HOME too deep for its own sockets and
// $TMPDIR pinned to tmpdir, waits for the fallback line, shuts the daemon down
// and returns the socket dir it named plus the POGO_HOME it used.
func bootFallbackPogod(t *testing.T, bin, tmpdir string) (socketDir, pogoHome string) {
	t.Helper()

	sb := deepPogoHome(t)
	state := filepath.Join(sb, "state")
	ws := filepath.Join(sb, "ws")
	for _, d := range []string{state, ws, filepath.Join(sb, ".config")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	logPath := filepath.Join(sb, "pogod.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	cmd := exec.Command(bin, "-port", strconv.Itoa(freePort(t)))
	cmd.Dir = ws
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(),
		"HOME="+sb,
		"XDG_CONFIG_HOME="+filepath.Join(sb, ".config"),
		"POGO_HOME="+state,
		"TMPDIR="+tmpdir,
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
	}()

	if !waitForLog(t, logPath, "agent attach sockets live in", 60*time.Second) {
		t.Fatalf("pogod never reported the sun_path fallback for POGO_HOME %s (%d bytes)\n--- log ---\n%s",
			state, len(state), readFile(t, logPath))
	}
	// SIGTERM and WAIT before returning: the assertions below read the tree this
	// daemon writes, and a still-running pogod is a second writer.
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_, _ = cmd.Process.Wait()
	stopped = true

	m := fallbackSocketLine.FindStringSubmatch(readFile(t, logPath))
	if m == nil {
		t.Fatalf("could not read the socket dir out of pogod's own log\n--- log ---\n%s", readFile(t, logPath))
	}
	return m[1], state
}

// TestFallbackSocketDirIsNestedAndClaimed is the shape of the fix: one entry in
// $TMPDIR whatever happens, and a leaf inside it that says whose it is.
func TestFallbackSocketDirIsNestedAndClaimed(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real pogod; skipped under -short")
	}
	bin := buildPogodUnderTest(t)
	tmpdir := shortTmpdir(t)
	socketDir, pogoHome := bootFallbackPogod(t, bin, tmpdir)

	if want := filepath.Join(tmpdir, "pogo-agents"); filepath.Dir(socketDir) != want {
		t.Errorf("pogod put its fallback sockets in %s; want a leaf under the single nest %s",
			socketDir, want)
	}
	entries, err := os.ReadDir(tmpdir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "pogo-agents" {
		t.Errorf("a pogod boot left %v in $TMPDIR, want exactly [pogo-agents] — the "+
			"leak was one TOP-LEVEL entry per POGO_HOME, 3,883 of them measured", names)
	}

	marker := filepath.Join(socketDir, agent.FallbackHomeMarker)
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("pogod did not record which POGO_HOME owns %s: %v", socketDir, err)
	}
	if strings.TrimSpace(string(got)) != pogoHome {
		t.Errorf("%s records %q, want the daemon's POGO_HOME %q", marker, got, pogoHome)
	}
}

// TestFallbackSocketDirReapsARootThatIsGone is the leak closed at the level it
// leaked: a pogod whose POGO_HOME has since been deleted leaves a leaf that the
// NEXT pogod removes. Before mg-a997 nothing removed it, ever, and each test
// binary's random POGO_HOME made one more.
func TestFallbackSocketDirReapsARootThatIsGone(t *testing.T) {
	if testing.Short() {
		t.Skip("boots two real pogods; skipped under -short")
	}
	bin := buildPogodUnderTest(t)
	tmpdir := shortTmpdir(t)

	firstDir, firstHome := bootFallbackPogod(t, bin, tmpdir)
	if _, err := os.Stat(firstDir); err != nil {
		t.Fatalf("the first daemon's socket dir is not there to begin with: %v", err)
	}

	// What t.TempDir() does at the end of every test that boots a daemon.
	if err := os.RemoveAll(firstHome); err != nil {
		t.Fatal(err)
	}

	secondDir, _ := bootFallbackPogod(t, bin, tmpdir)
	if secondDir == firstDir {
		t.Fatalf("test bug: both daemons derived the same socket dir %s", secondDir)
	}
	if _, err := os.Stat(firstDir); err == nil {
		t.Errorf("%s survived a later boot; its POGO_HOME %s is gone, so nothing will "+
			"ever look for it again and nothing else will ever remove it", firstDir, firstHome)
	}
	if _, err := os.Stat(secondDir); err != nil {
		t.Errorf("the live daemon's own socket dir %s is missing: %v", secondDir, err)
	}
}

// shortTmpdir returns a private $TMPDIR short enough that the fallback stays
// under it rather than degrading to /tmp — which is what makes "the nest is the
// only entry" measurable at all. Under /tmp rather than t.TempDir() for the
// same reason: darwin's per-user $TMPDIR is 48 bytes before anything is joined
// onto it.
func shortTmpdir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pgtd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// buildPogodUnderTest compiles the pogod in this working tree. The two tests
// above assert on main()'s own wiring, so nothing short of the real binary
// would be measuring the thing that broke.
func buildPogodUnderTest(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "pogod-under-test")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building pogod: %v\n%s", err, out)
	}
	return bin
}
