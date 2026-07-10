package reconcile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// inode returns the file's inode number so a test can prove an atomic replace
// did (or did not) swap the underlying file. Unix-only, which matches where the
// reconcile step actually runs (launchctl is macOS).
func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestAtomicReplace_WritesWhenDifferent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "script.sh")

	changed, err := AtomicReplace(target, []byte("new\n"), 0o755)
	if err != nil {
		t.Fatalf("AtomicReplace: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true for a fresh write")
	}
	if got := read(t, target); got != "new\n" {
		t.Fatalf("target contents = %q, want %q", got, "new\n")
	}
	fi, _ := os.Stat(target)
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestAtomicReplace_NoopWhenIdentical(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "script.sh")
	writeFile(t, target, "same\n", 0o755)

	// Capture the inode so we can prove a no-op did NOT rename a new file over
	// it — the whole point is that an idle interpreter's inode is undisturbed.
	before, _ := os.Stat(target)
	beforeIno := inode(before)

	changed, err := AtomicReplace(target, []byte("same\n"), 0o755)
	if err != nil {
		t.Fatalf("AtomicReplace: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false when bytes are identical")
	}
	after, _ := os.Stat(target)
	if inode(after) != beforeIno {
		t.Fatal("no-op replace changed the inode; must leave the file untouched")
	}
}

func TestAtomicReplace_LeavesNoTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "script.sh")
	if _, err := AtomicReplace(target, []byte("x"), 0o755); err != nil {
		t.Fatalf("AtomicReplace: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "script.sh" {
			t.Fatalf("stray file left behind: %s", e.Name())
		}
	}
}

func TestReconcile_ReplacesAndKickstartsOnChange(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src.sh")
	target := filepath.Join(dir, "dst.sh")
	writeFile(t, source, "#!/bin/bash\necho hardened\n", 0o755)

	kicked := ""
	kick := func(label string) (int, error) { kicked = label; return 4242, nil }

	res := Reconcile(Mirror{Name: "poller", Source: source, Target: target, Label: "com.pogo.poller"}, kick, Deps{})
	if res.Err != nil {
		t.Fatalf("Reconcile err: %v", res.Err)
	}
	if !res.Changed {
		t.Fatal("expected Changed=true")
	}
	if !res.Kickstarted || kicked != "com.pogo.poller" {
		t.Fatalf("expected kickstart of com.pogo.poller, got kicked=%q kickstarted=%v", kicked, res.Kickstarted)
	}
	if res.NewPID != 4242 {
		t.Fatalf("NewPID = %d, want 4242", res.NewPID)
	}
	if read(t, target) != "#!/bin/bash\necho hardened\n" {
		t.Fatal("target not copied from source")
	}
}

func TestReconcile_NoKickstartWhenAlreadyCurrentAndProcessFresh(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src.sh")
	target := filepath.Join(dir, "dst.sh")
	writeFile(t, source, "same\n", 0o755)
	writeFile(t, target, "same\n", 0o755)

	kicks := 0
	kick := func(label string) (int, error) { kicks++; return 1, nil }

	// Process started AFTER the file's mtime → fresh, no restart needed.
	deps := Deps{
		RunningPID: func(string) (int, bool) { return 99, true },
		ProcStart:  func(int) (time.Time, bool) { return time.Now().Add(time.Hour), true },
		FileMtime:  func(string) (time.Time, bool) { return time.Now(), true },
	}
	res := Reconcile(Mirror{Name: "p", Source: source, Target: target, Label: "com.pogo.p"}, kick, deps)
	if res.Err != nil {
		t.Fatalf("err: %v", res.Err)
	}
	if res.Changed {
		t.Fatal("expected Changed=false")
	}
	if kicks != 0 {
		t.Fatalf("expected no kickstart when file current and process fresh, got %d", kicks)
	}
}

func TestReconcile_SelfHealsStaleProcessEvenWhenBytesMatch(t *testing.T) {
	// pa's case: the file is already correct on disk, but the running process
	// started before it was written, so it parsed old bytes. A re-run must
	// kickstart to heal it, even though AtomicReplace reports no byte change.
	dir := t.TempDir()
	source := filepath.Join(dir, "src.sh")
	target := filepath.Join(dir, "dst.sh")
	writeFile(t, source, "hardened\n", 0o755)
	writeFile(t, target, "hardened\n", 0o755)

	kicks := 0
	kick := func(label string) (int, error) { kicks++; return 7, nil }

	fileTime := time.Now()
	deps := Deps{
		RunningPID: func(string) (int, bool) { return 50, true },
		ProcStart:  func(int) (time.Time, bool) { return fileTime.Add(-time.Hour), true }, // started before write
		FileMtime:  func(string) (time.Time, bool) { return fileTime, true },
	}
	res := Reconcile(Mirror{Name: "p", Source: source, Target: target, Label: "com.pogo.p"}, kick, deps)
	if res.Err != nil {
		t.Fatalf("err: %v", res.Err)
	}
	if res.Changed {
		t.Fatal("expected Changed=false (bytes already match)")
	}
	if kicks != 1 || !res.Kickstarted {
		t.Fatalf("expected a self-healing kickstart, got kicks=%d kickstarted=%v", kicks, res.Kickstarted)
	}
}

func TestCheckDrift_CleanWhenEverythingMatches(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src.sh")
	target := filepath.Join(dir, "dst.sh")
	writeFile(t, source, "ok\n", 0o755)
	writeFile(t, target, "ok\n", 0o755)

	deps := Deps{
		LoadedProgram: func(string) (string, bool) { return target, true },
		RunningPID:    func(string) (int, bool) { return 10, true },
		ProcStart:     func(int) (time.Time, bool) { return time.Now(), true },
		FileMtime:     func(string) (time.Time, bool) { return time.Now().Add(-time.Hour), true },
	}
	d := CheckDrift(Mirror{Name: "p", Source: source, Target: target, Label: "com.pogo.p"}, deps)
	if !d.Clean() {
		t.Fatalf("expected clean, got %+v", d)
	}
	if d.Report() != "" {
		t.Fatalf("clean mirror should render empty report, got %q", d.Report())
	}
}

func TestCheckDrift_FileModified(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src.sh")
	target := filepath.Join(dir, "dst.sh")
	writeFile(t, source, "new\n", 0o755)
	writeFile(t, target, "old\n", 0o755)

	d := CheckDrift(Mirror{Name: "p", Source: source, Target: target}, Deps{})
	if d.FileDrift == "" {
		t.Fatal("expected FileDrift for differing bytes")
	}
	if d.Clean() {
		t.Fatal("expected not clean")
	}
}

func TestCheckDrift_FileMissing(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src.sh")
	target := filepath.Join(dir, "dst.sh") // never created
	writeFile(t, source, "new\n", 0o755)

	d := CheckDrift(Mirror{Name: "p", Source: source, Target: target}, Deps{})
	if d.FileDrift == "" {
		t.Fatal("expected FileDrift=MISSING when target absent")
	}
}

func TestCheckDrift_PathDrift_LoadedJobExecsOldPath(t *testing.T) {
	// The recovery-plist case: file bytes match, but the LOADED job execs a
	// different (old) path. This is drift even though `cmp` is clean.
	dir := t.TempDir()
	source := filepath.Join(dir, "src.sh")
	target := filepath.Join(dir, "dst.sh")
	writeFile(t, source, "same\n", 0o755)
	writeFile(t, target, "same\n", 0o755)

	deps := Deps{
		LoadedProgram: func(string) (string, bool) { return "/Users/daniel/old/path.sh", true },
	}
	d := CheckDrift(Mirror{Name: "p", Source: source, Target: target, Label: "com.pogo.p"}, deps)
	if d.FileDrift != "" {
		t.Fatalf("file should be clean, got %q", d.FileDrift)
	}
	if d.PathDrift == "" {
		t.Fatal("expected PathDrift when loaded job execs a different path")
	}
	if d.Clean() {
		t.Fatal("path drift must make the mirror not clean")
	}
}

func TestCheckDrift_StaleProcess(t *testing.T) {
	// pa's case: same path, but the process started before the file was written.
	dir := t.TempDir()
	source := filepath.Join(dir, "src.sh")
	target := filepath.Join(dir, "dst.sh")
	writeFile(t, source, "same\n", 0o755)
	writeFile(t, target, "same\n", 0o755)

	fileTime := time.Now()
	deps := Deps{
		LoadedProgram: func(string) (string, bool) { return target, true },
		RunningPID:    func(string) (int, bool) { return 321, true },
		ProcStart:     func(int) (time.Time, bool) { return fileTime.Add(-time.Hour), true },
		FileMtime:     func(string) (time.Time, bool) { return fileTime, true },
	}
	d := CheckDrift(Mirror{Name: "p", Source: source, Target: target, Label: "com.pogo.p"}, deps)
	if d.StaleProc == "" {
		t.Fatal("expected StaleProc when process predates the file write")
	}
	if d.PathDrift != "" {
		t.Fatalf("path should be clean, got %q", d.PathDrift)
	}
}

func TestCheckDrift_NilDepsSkipRunningChecks(t *testing.T) {
	// An absent signal must NOT masquerade as clean-or-drift: with nil deps the
	// running-reality dimensions are simply skipped, file drift still evaluated.
	dir := t.TempDir()
	source := filepath.Join(dir, "src.sh")
	target := filepath.Join(dir, "dst.sh")
	writeFile(t, source, "same\n", 0o755)
	writeFile(t, target, "same\n", 0o755)

	d := CheckDrift(Mirror{Name: "p", Source: source, Target: target, Label: "com.pogo.p"}, Deps{})
	if !d.Clean() {
		t.Fatalf("expected clean with nil deps and matching bytes, got %+v", d)
	}
}

func TestParseLaunchctlProgram_ProgramLine(t *testing.T) {
	out := `	path = /Users/daniel/Library/LaunchAgents/com.pogo.watchdog.plist
	state = running
	program = /Users/daniel/.pogo/pogo-reminders/bin/watchdog.sh
	arguments = {
		/Users/daniel/.pogo/pogo-reminders/bin/watchdog.sh
	}
	pid = 11947
`
	prog, ok := ParseLaunchctlProgram(out)
	if !ok {
		t.Fatal("expected ok")
	}
	if prog != "/Users/daniel/.pogo/pogo-reminders/bin/watchdog.sh" {
		t.Fatalf("prog = %q", prog)
	}
}

func TestParseLaunchctlProgram_ArgumentsFallback(t *testing.T) {
	out := `	state = running
	arguments = {
		/Users/daniel/.pogo/bin/poll.sh
		--flag
	}
	pid = 5
`
	prog, ok := ParseLaunchctlProgram(out)
	if !ok || prog != "/Users/daniel/.pogo/bin/poll.sh" {
		t.Fatalf("prog = %q ok = %v", prog, ok)
	}
}

func TestParseLaunchctlProgram_None(t *testing.T) {
	if _, ok := ParseLaunchctlProgram("Could not find service.\n"); ok {
		t.Fatal("expected ok=false for missing program")
	}
}

func TestParseLaunchctlPID(t *testing.T) {
	out := "	state = running\n	pid = 11947\n"
	pid, ok := ParseLaunchctlPID(out)
	if !ok || pid != 11947 {
		t.Fatalf("pid = %d ok = %v", pid, ok)
	}
	if _, ok := ParseLaunchctlPID("state = not running\n"); ok {
		t.Fatal("expected ok=false when no pid")
	}
}

func TestParsePsLstart(t *testing.T) {
	ts, ok := parsePsLstart("Wed Jul 10 15:50:52 2026\n")
	if !ok {
		t.Fatal("expected ok")
	}
	if ts.Year() != 2026 || ts.Month() != time.July || ts.Day() != 10 {
		t.Fatalf("parsed wrong date: %v", ts)
	}
	if _, ok := parsePsLstart(""); ok {
		t.Fatal("expected ok=false for empty")
	}
}
