//go:build !windows

package service

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// withStdio points this process's fds (1 and/or 2) at target for the
// duration of fn, exactly the way launchd's StandardOutPath/StandardErrorPath
// redirect does, and restores them before returning — before any assertion
// output is written.
func withStdio(t *testing.T, target *os.File, fds []int, fn func()) {
	t.Helper()
	saved := make(map[int]int, len(fds))
	restore := func() {
		for fd, s := range saved {
			dupFd(s, fd)
			unix.Close(s)
			delete(saved, fd)
		}
	}
	defer restore()
	for _, fd := range fds {
		s, err := unix.Dup(fd)
		if err != nil {
			restore()
			t.Fatalf("dup(%d): %v", fd, err)
		}
		saved[fd] = s
		if err := dupFd(int(target.Fd()), fd); err != nil {
			restore()
			t.Fatalf("dup2 onto %d: %v", fd, err)
		}
	}
	fn()
}

// redirectOntoFile opens path for append and runs the startup rotation with
// fds 1/2 on it, writing a marker through os.Stderr (what the log package
// uses) while still redirected.
func redirectOntoFile(t *testing.T, path string) (LogRotation, error) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var r LogRotation
	var rotErr error
	withStdio(t, f, []int{1, 2}, func() {
		r, rotErr = RotatePogodLogIfNeeded()
		os.Stderr.WriteString("post-rotation marker\n")
	})
	return r, rotErr
}

// TestRotatePogodLogEndToEnd simulates the launchd environment without
// touching the live daemon: an oversized pogod.log at the default path is
// fds 1/2, and the rotation moves it to pogod.log.1 while subsequent stderr
// writes land in the fresh pogod.log.
func TestRotatePogodLogEndToEnd(t *testing.T) {
	logPath := setTestHome(t)
	oldContent := bytes.Repeat([]byte("prior-run evidence\n"), maxPogodLogSize/19+1)
	if err := os.WriteFile(logPath, oldContent, 0644); err != nil {
		t.Fatalf("write big log: %v", err)
	}

	r, rotErr := redirectOntoFile(t, logPath)
	if rotErr != nil {
		t.Fatalf("RotatePogodLogIfNeeded: %v", rotErr)
	}
	if !r.Rotated() {
		t.Fatalf("not rotated (%s), want rotated (stderr was pogod.log and file exceeded the cap)", r.Summary())
	}
	if !sameName(t, r.Path, logPath) {
		t.Errorf("Path = %q, want %q", r.Path, logPath)
	}

	rotatedContent := readFileT(t, logPath+".1")
	if !strings.HasPrefix(rotatedContent, "prior-run evidence") {
		t.Errorf("pogod.log.1 does not hold the prior run's content: %.40q", rotatedContent)
	}
	if int64(len(rotatedContent)) != int64(len(oldContent)) {
		t.Errorf("pogod.log.1 size = %d, want %d", len(rotatedContent), len(oldContent))
	}

	fresh := readFileT(t, logPath)
	if !strings.Contains(fresh, "post-rotation marker") {
		t.Errorf("fresh pogod.log missing post-rotation stderr write, got: %.80q", fresh)
	}
	if strings.Contains(fresh, "prior-run evidence") {
		t.Error("fresh pogod.log still contains prior-run content — rename did not happen")
	}
}

// sameName compares two paths after resolving symlinks — the kernel reports
// darwin's /var temp dirs as /private/var.
func sameName(t *testing.T, a, b string) bool {
	t.Helper()
	ra, errA := filepath.EvalSymlinks(filepath.Dir(a))
	rb, errB := filepath.EvalSymlinks(filepath.Dir(b))
	if errA != nil || errB != nil {
		return a == b
	}
	return filepath.Join(ra, filepath.Base(a)) == filepath.Join(rb, filepath.Base(b))
}

// nonDefaultFixture is drellem2/pogo#104's host: the installed plist sends
// stderr to a file that is NOT this build's PogodLogPath (the reporter's was
// ~/.local/share/pogo/logs/pogo.err.log). Both files are oversized.
type nonDefaultFixture struct {
	custom, def string
	customSize  int
	defSize     int
}

func newNonDefaultFixture(t *testing.T) nonDefaultFixture {
	t.Helper()
	def := setTestHome(t)
	custom := filepath.Join(os.Getenv("HOME"), ".local", "share", "pogo", "logs", "pogo.err.log")
	if err := os.MkdirAll(filepath.Dir(custom), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	customContent := bytes.Repeat([]byte("custom-path evidence\n"), maxPogodLogSize/21+1)
	defContent := bytes.Repeat([]byte("d"), maxPogodLogSize+7)
	if err := os.WriteFile(custom, customContent, 0644); err != nil {
		t.Fatalf("write custom: %v", err)
	}
	if err := os.WriteFile(def, defContent, 0644); err != nil {
		t.Fatalf("write default: %v", err)
	}
	return nonDefaultFixture{custom: custom, def: def, customSize: len(customContent), defSize: len(defContent)}
}

// problems lists every way the rotation failed to rotate the file fd 2
// actually wrote to, or touched the default path it should have left alone.
func (fx nonDefaultFixture) problems(t *testing.T, r LogRotation, rotErr error) []string {
	t.Helper()
	var p []string
	if rotErr != nil {
		p = append(p, fmt.Sprintf("error: %v", rotErr))
	}
	if !r.Rotated() {
		p = append(p, fmt.Sprintf("not rotated: %s", r.Summary()))
	}
	if !sameName(t, r.Path, fx.custom) || r.Source != logPathFromFd {
		p = append(p, fmt.Sprintf("judged %q (from %s), want %q from fd 2", r.Path, r.Source, fx.custom))
	}
	if fi, err := os.Stat(fx.custom + ".1"); err != nil || fi.Size() != int64(fx.customSize) {
		p = append(p, fmt.Sprintf("%s.1 missing or wrong size (err=%v)", fx.custom, err))
	}
	if b, err := os.ReadFile(fx.custom); err != nil || !strings.Contains(string(b), "post-rotation marker") || strings.Contains(string(b), "custom-path evidence") {
		p = append(p, fmt.Sprintf("fresh %s does not hold exactly the post-rotation output (err=%v)", fx.custom, err))
	}
	if fi, err := os.Stat(fx.def); err != nil || fi.Size() != int64(fx.defSize) {
		p = append(p, fmt.Sprintf("default %s was touched (err=%v)", fx.def, err))
	}
	if _, err := os.Stat(fx.def + ".1"); !os.IsNotExist(err) {
		p = append(p, fmt.Sprintf("default %s.1 exists", fx.def))
	}
	return p
}

// drellem2/pogo#104: stderr redirected to a non-default path rotates THAT
// file, and PogodLogPath is left untouched.
func TestRotatePogodLogRotatesTheFileStderrWritesTo(t *testing.T) {
	fx := newNonDefaultFixture(t)
	r, rotErr := redirectOntoFile(t, fx.custom)
	for _, p := range fx.problems(t, r, rotErr) {
		t.Error(p)
	}
}

// Positive control for the test above: with the resolver forced back to the
// pre-#104 behaviour (the path this build computes), the same fixture must
// FAIL its checks — otherwise the test above could pass without the fix.
func TestRotatePogodLogNonDefaultPathControl(t *testing.T) {
	fx := newNonDefaultFixture(t)
	stubResolver(t, func() (string, bool, error) { return PogodLogPath(), true, nil })
	r, rotErr := redirectOntoFile(t, fx.custom)
	if len(fx.problems(t, r, rotErr)) == 0 {
		t.Fatal("control passed: the old PogodLogPath resolver rotated the non-default file, so the fixture cannot tell the fix from the bug")
	}
	if r.Reason != RotationPathMissing {
		t.Errorf("control Reason = %q, want %q", r.Reason, RotationPathMissing)
	}
}

// stderr on a pipe (a captured spawn, a dev run piped to a pager) is not a
// log: not-redirected, and nothing is rotated.
func TestRotatePogodLogPipeIsNotRedirected(t *testing.T) {
	def := setTestHome(t)
	if err := os.WriteFile(def, bytes.Repeat([]byte("x"), maxPogodLogSize+1), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()

	var r LogRotation
	var rotErr error
	withStdio(t, pw, []int{2}, func() { r, rotErr = RotatePogodLogIfNeeded() })

	if rotErr != nil || r.Reason != RotationNotRedirected {
		t.Fatalf("got %+v err=%v, want not-redirected", r, rotErr)
	}
	if _, err := os.Stat(def + ".1"); !os.IsNotExist(err) {
		t.Errorf("default log rotated while stderr was a pipe")
	}
}

// stderr on a file that has since been unlinked: fd 2 writes somewhere no
// path names. path-missing, not a silent no-op.
func TestRotatePogodLogUnlinkedStderrIsPathMissing(t *testing.T) {
	setTestHome(t)
	p := filepath.Join(t.TempDir(), "unlinked.log")
	writeFileT(t, p, "x")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if err := os.Remove(p); err != nil {
		t.Fatalf("remove: %v", err)
	}

	var r LogRotation
	var rotErr error
	withStdio(t, f, []int{2}, func() { r, rotErr = RotatePogodLogIfNeeded() })

	if rotErr != nil || r.Reason != RotationPathMissing {
		t.Fatalf("got %+v err=%v, want path-missing", r, rotErr)
	}
}

// Below the threshold the file is left alone, and the reason carries its size.
func TestRotatePogodLogBelowThreshold(t *testing.T) {
	def := setTestHome(t)
	writeFileT(t, def, "small\n")
	r, rotErr := redirectOntoFile(t, def)
	if rotErr != nil || r.Reason != RotationBelowThreshold || r.Size != int64(len("small\n")) {
		t.Fatalf("got %+v err=%v, want below-threshold size 6", r, rotErr)
	}
	if !strings.HasPrefix(r.Summary(), "below-threshold 6 < ") {
		t.Errorf("Summary() = %q", r.Summary())
	}
	if _, err := os.Stat(def + ".1"); !os.IsNotExist(err) {
		t.Error("below-threshold log was rotated")
	}
}

// stderrLogPath names the file fd 2 is on, at a path that is not a default.
func TestStderrLogPathResolvesFd2(t *testing.T) {
	p := filepath.Join(t.TempDir(), "somewhere-else.log")
	writeFileT(t, p, "")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	var got string
	var redirected bool
	var resErr error
	withStdio(t, f, []int{2}, func() { got, redirected, resErr = stderrLogPath() })

	if resErr != nil || !redirected || !sameName(t, got, p) {
		t.Fatalf("stderrLogPath() = %q, %v, %v; want %q, true, nil", got, redirected, resErr, p)
	}
}
