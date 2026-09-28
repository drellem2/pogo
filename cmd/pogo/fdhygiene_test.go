package main

// mg-fbe7b: TestShippedPromptsMatchTheCLISurface failed in the refinery gate,
// twice in three days on branches that never touched cmd/pogo, with
// `fork/exec <binary>: bad file descriptor`. The cause was in a DIFFERENT test
// in this package: it silenced cobra with os.NewFile(0, os.DevNull), which does
// not open /dev/null — it wraps whatever is at fd 0 (stdin) and names it
// "/dev/null". Every *os.File carries a finalizer that closes its fd, so after
// a GC the first wrapper closed stdin and the other three closed whatever the
// process had opened next at the freed number. exec.Cmd opens /dev/null for a
// child's nil Stdin and gets the lowest free fd — 0 — so a finalizer landing
// between that open and the fork failed the exec with EBADF. Nothing in the
// failing test was wrong, and the test that was wrong always passed.
//
// This guard is lexical on purpose: the defect is invisible at runtime until
// the GC and an exec line up, which is exactly why it read as a flake.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reNewFileLiteralFd matches os.NewFile called with a literal descriptor
// number, optionally wrapped in uintptr(...). A computed fd (f.Fd(), a dup's
// result) is fine: its owner is the *os.File being made.
var reNewFileLiteralFd = regexp.MustCompile(`os\.NewFile\(\s*(?:uintptr\(\s*)?[0-9]+\s*\)?\s*,`)

func TestNewFileLiteralFdPatternMatchesTheShippedDefect(t *testing.T) {
	// Positive control: without it, a regex that matches nothing would make
	// the repo-wide scan below pass vacuously.
	for _, line := range []string{
		`root.SetOut(os.NewFile(0, os.DevNull))`,
		`os.NewFile(uintptr(1), "stdout")`,
		`os.NewFile( 2 , "x")`,
	} {
		if !reNewFileLiteralFd.MatchString(line) {
			t.Errorf("pattern misses %q", line)
		}
	}
	for _, line := range []string{
		`os.NewFile(uintptr(fd), name)`,
		`os.NewFile(f.Fd(), "dup")`,
		`root.SetOut(io.Discard)`,
	} {
		if reNewFileLiteralFd.MatchString(line) {
			t.Errorf("pattern flags legitimate %q", line)
		}
	}
}

func TestNoGoFileWrapsALiteralFdInOsNewFile(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected the module root at %s: %v", root, err)
	}
	self, _ := filepath.Abs("fdhygiene_test.go")
	scanned := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "_testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || path == self {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if reNewFileLiteralFd.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d: os.NewFile with a literal fd — its finalizer closes that descriptor "+
					"(stdin/stdout/stderr, or whatever reuses the number) when GC'd; use io.Discard or "+
					"os.Open(os.DevNull) instead (mg-fbe7b)\n    %s", rel, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d .go files under %s; the walk is not seeing the module", scanned, root)
	}
}
