package service

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestRotateChainShiftsAndDropsOldest(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "pogod.log")
	writeFileT(t, logPath, "current")
	writeFileT(t, logPath+".1", "one")
	writeFileT(t, logPath+".2", "two")
	writeFileT(t, logPath+".3", "three") // oldest — must be dropped at keep=3

	if err := rotateChain(logPath, 3); err != nil {
		t.Fatalf("rotateChain: %v", err)
	}

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("pogod.log should have been renamed away, stat err = %v", err)
	}
	if got := readFileT(t, logPath+".1"); got != "current" {
		t.Errorf("pogod.log.1 = %q, want %q", got, "current")
	}
	if got := readFileT(t, logPath+".2"); got != "one" {
		t.Errorf("pogod.log.2 = %q, want %q", got, "one")
	}
	if got := readFileT(t, logPath+".3"); got != "two" {
		t.Errorf("pogod.log.3 = %q, want %q", got, "two")
	}
	if _, err := os.Stat(logPath + ".4"); !os.IsNotExist(err) {
		t.Errorf("pogod.log.4 must not exist (oldest dropped), stat err = %v", err)
	}
}

func TestRotateChainHandlesGaps(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "pogod.log")
	writeFileT(t, logPath, "current")
	writeFileT(t, logPath+".2", "two") // .1 missing — gap must not abort

	if err := rotateChain(logPath, 3); err != nil {
		t.Fatalf("rotateChain: %v", err)
	}
	if got := readFileT(t, logPath+".1"); got != "current" {
		t.Errorf("pogod.log.1 = %q, want %q", got, "current")
	}
	if got := readFileT(t, logPath+".3"); got != "two" {
		t.Errorf("pogod.log.3 = %q, want %q", got, "two")
	}
}

func TestRotateChainFirstRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "pogod.log")
	writeFileT(t, logPath, "current")

	if err := rotateChain(logPath, 3); err != nil {
		t.Fatalf("rotateChain: %v", err)
	}
	if got := readFileT(t, logPath+".1"); got != "current" {
		t.Errorf("pogod.log.1 = %q, want %q", got, "current")
	}
}

// setTestHome points logDir()'s HOME at a temp dir and pre-creates the
// Library/Logs/pogo tree, returning the pogod.log path inside it.
func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	d := filepath.Join(home, "Library", "Logs", "pogo")
	if err := os.MkdirAll(d, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", d, err)
	}
	return filepath.Join(d, "pogod.log")
}

// A big log must NOT be rotated when stderr is not pogod.log (dev run /
// pipe-captured spawn) — the same-inode gate is what keeps startup
// rotation safe to call unconditionally.
func TestRotatePogodLogSkipsWhenStderrIsNotTheLog(t *testing.T) {
	logPath := setTestHome(t)
	big := bytes.Repeat([]byte("x"), maxPogodLogSize+1)
	if err := os.WriteFile(logPath, big, 0644); err != nil {
		t.Fatalf("write big log: %v", err)
	}

	rotated, gotPath, err := RotatePogodLogIfNeeded()
	if err != nil {
		t.Fatalf("RotatePogodLogIfNeeded: %v", err)
	}
	if rotated {
		t.Fatal("rotated=true, but stderr is not pogod.log — gate failed")
	}
	if gotPath != logPath {
		t.Errorf("logPath = %q, want %q", gotPath, logPath)
	}
	if fi, err := os.Stat(logPath); err != nil || fi.Size() != int64(len(big)) {
		t.Errorf("pogod.log must be untouched: size=%v err=%v", fi.Size(), err)
	}
	if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
		t.Errorf("pogod.log.1 must not exist, stat err = %v", err)
	}
}

func TestRotatePogodLogMissingFileIsNoop(t *testing.T) {
	setTestHome(t)
	rotated, _, err := RotatePogodLogIfNeeded()
	if err != nil || rotated {
		t.Fatalf("missing log: rotated=%v err=%v, want false/nil", rotated, err)
	}
}

func TestSameFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	writeFileT(t, path, "hello")
	other := filepath.Join(dir, "b.log")
	writeFileT(t, other, "hello")

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	if !sameFile(f, path) {
		t.Error("sameFile(f, its own path) = false, want true")
	}
	if sameFile(f, other) {
		t.Error("sameFile(f, different file) = true, want false")
	}
	if sameFile(f, filepath.Join(dir, "missing.log")) {
		t.Error("sameFile(f, missing path) = true, want false")
	}
	if stderrIsSameFile(path) {
		t.Error("stderrIsSameFile(temp file) = true — test stderr should never be this file")
	}
}

func TestPogodLogPathMatchesPlistLogDir(t *testing.T) {
	t.Setenv("HOME", "/tmp/pogo-test-home")
	want := "/tmp/pogo-test-home/Library/Logs/pogo/pogod.log"
	if got := PogodLogPath(); got != want {
		t.Errorf("PogodLogPath() = %q, want %q (must match plist StandardOutPath)", got, want)
	}
}
