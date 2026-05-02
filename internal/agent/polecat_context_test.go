package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo initializes a fresh git repo in a temp dir and returns its path.
// Commits a sequence of files so log/file helpers have something to read.
func initTestRepo(t *testing.T, commits []struct{ File, Subject string }) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for _, c := range commits {
		path := filepath.Join(dir, c.File)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(c.Subject), 0644); err != nil {
			t.Fatal(err)
		}
		run("add", c.File)
		run("commit", "-q", "-m", c.Subject)
	}
	return dir
}

func TestCaptureRecentCommits(t *testing.T) {
	repo := initTestRepo(t, []struct{ File, Subject string }{
		{"a.txt", "first commit (mg-1111)"},
		{"b.txt", "second commit (mg-2222)"},
		{"c.txt", "third commit (mg-3333)"},
	})

	got := captureRecentCommits(repo, 5)
	if got == "" {
		t.Fatal("expected commits, got empty string")
	}
	for _, want := range []string{"first commit", "second commit", "third commit", "mg-1111", "mg-3333"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output:\n%s", want, got)
		}
	}
	// `git log` shows newest first — third commit precedes first in output.
	if strings.Index(got, "third commit") > strings.Index(got, "first commit") {
		t.Errorf("expected newest-first ordering:\n%s", got)
	}

	// n=1 caps the result.
	one := captureRecentCommits(repo, 1)
	if strings.Count(one, "\n") != 0 {
		t.Errorf("expected single-line output for n=1, got:\n%s", one)
	}
	if !strings.Contains(one, "third commit") {
		t.Errorf("expected newest commit only, got:\n%s", one)
	}
}

func TestCaptureRecentCommitsHandlesBadInput(t *testing.T) {
	cases := map[string]struct {
		repo string
		n    int
	}{
		"empty repo path": {"", 5},
		"non-git dir":     {t.TempDir(), 5},
		"zero n":          {".", 0},
		"negative n":      {".", -1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := captureRecentCommits(tc.repo, tc.n); got != "" {
				t.Errorf("expected empty string, got %q", got)
			}
		})
	}
}

func TestCaptureRecentFiles(t *testing.T) {
	repo := initTestRepo(t, []struct{ File, Subject string }{
		{"alpha/x.go", "x"},
		{"beta/y.go", "y"},
		{"alpha/x.go", "x again"}, // duplicate path — must dedupe
		{"gamma/z.go", "z"},
	})

	got := captureRecentFiles(repo, 10, 30)
	if got == "" {
		t.Fatal("expected files, got empty string")
	}
	lines := strings.Split(got, "\n")
	want := map[string]bool{"alpha/x.go": false, "beta/y.go": false, "gamma/z.go": false}
	for _, line := range lines {
		if _, ok := want[line]; ok {
			want[line] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("expected %q in files output:\n%s", path, got)
		}
	}
	// Sorted output: alpha < beta < gamma.
	if !(strings.Index(got, "alpha/x.go") < strings.Index(got, "beta/y.go") &&
		strings.Index(got, "beta/y.go") < strings.Index(got, "gamma/z.go")) {
		t.Errorf("expected sorted output, got:\n%s", got)
	}
	// Dedupe: alpha/x.go must appear exactly once.
	if c := strings.Count(got, "alpha/x.go"); c != 1 {
		t.Errorf("expected alpha/x.go once, got %d times:\n%s", c, got)
	}
}

func TestCaptureRecentFilesTruncates(t *testing.T) {
	commits := make([]struct{ File, Subject string }, 0, 5)
	for i := 0; i < 5; i++ {
		commits = append(commits, struct{ File, Subject string }{
			File:    string(rune('a'+i)) + ".txt",
			Subject: "c" + string(rune('0'+i)),
		})
	}
	repo := initTestRepo(t, commits)

	got := captureRecentFiles(repo, 10, 2)
	if !strings.Contains(got, "more)") {
		t.Errorf("expected truncation marker in output:\n%s", got)
	}
	// 2 file lines + 1 marker line = 3 newlines worth.
	if n := strings.Count(got, "\n"); n != 2 {
		t.Errorf("expected 2 newlines (2 files + marker), got %d:\n%s", n, got)
	}
}

func TestCaptureRecentFilesHandlesBadInput(t *testing.T) {
	cases := map[string]struct {
		repo           string
		n, maxFiles    int
		expectNonEmpty bool
	}{
		"empty repo":   {"", 5, 30, false},
		"non-git dir":  {t.TempDir(), 5, 30, false},
		"zero n":       {".", 0, 30, false},
		"zero max":     {".", 5, 0, false},
		"negative max": {".", 5, -1, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := captureRecentFiles(tc.repo, tc.n, tc.maxFiles)
			if (got != "") != tc.expectNonEmpty {
				t.Errorf("got %q (nonempty=%v), want nonempty=%v", got, got != "", tc.expectNonEmpty)
			}
		})
	}
}
