package selfdrift

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepoWithBinary creates a throwaway Go module inside a real git repo,
// commits it, and builds it. It returns the built binary's path and the
// commit it was built from.
//
// This is deliberately a REAL build rather than a fixture: the whole claim of
// BinaryRev is that debug/buildinfo.ReadFile recovers the vcs stamp a
// consumer's `go install` baked in, WITHOUT a Go toolchain at read time. A
// hand-written fixture would assert the parser against bytes we made up.
func gitRepoWithBinary(t *testing.T) (repo, bin, rev string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain to build a stamped binary with")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git to stamp a build from")
	}
	repo = t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("go.mod", "module example.com/stamped\n\ngo 1.25\n")
	write("main.go", "package main\n\nfunc main() {}\n")

	git := func(args ...string) string {
		t.Helper()
		full := append([]string{"-C", repo,
			"-c", "user.email=selfdrift@test",
			"-c", "user.name=selfdrift test",
			"-c", "commit.gpgsign=false"}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "stamped")
	rev = git("rev-parse", "HEAD")

	bin = filepath.Join(t.TempDir(), "stamped")
	// -buildvcs=true rather than the default `auto`: auto declines silently
	// when it cannot read the VCS, which would make the stamped assertions
	// below fail for a reason that has nothing to do with the code under test.
	build := exec.Command("go", "build", "-buildvcs=true", "-o", bin, ".")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return repo, bin, rev
}

// TestBinaryRevReadsTheRealStamp is the load-bearing claim of the installed
// axis: the revision comes out of the binary itself, with no `go version -m`
// and no toolchain in the path a consumer takes.
func TestBinaryRevReadsTheRealStamp(t *testing.T) {
	_, bin, rev := gitRepoWithBinary(t)
	got := BinaryRev(bin)
	if got != rev {
		t.Fatalf("BinaryRev = %q, want the commit it was built from %q", got, rev)
	}
}

// TestBinaryRevAbsences pins the three kinds of "no revision" apart. Fusing
// them is the defect this package inherited a warning about: MISSING owes a
// build and a build fixes it; UNSTAMPED owes an investigation and a build does
// NOT fix it, because the rebuild is unstamped too and the drift never clears.
func TestBinaryRevAbsences(t *testing.T) {
	dir := t.TempDir()

	if got := BinaryRev(filepath.Join(dir, "nope")); got != RevMissing {
		t.Errorf("BinaryRev(absent) = %q, want %q", got, RevMissing)
	}
	if got := BinaryRev(""); got != RevMissing {
		t.Errorf("BinaryRev(\"\") = %q, want %q", got, RevMissing)
	}
	if got := BinaryRev(dir); got != RevMissing {
		t.Errorf("BinaryRev(dir) = %q, want %q", got, RevMissing)
	}

	// A file that exists but is not a readable Go binary has told us nothing
	// about its provenance. That is UNSTAMPED, not MISSING — calling it
	// missing would owe a build that cannot fix it.
	notABinary := filepath.Join(dir, "pogod")
	if err := os.WriteFile(notABinary, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := BinaryRev(notABinary); got != RevUnstamped {
		t.Errorf("BinaryRev(not-a-go-binary) = %q, want %q", got, RevUnstamped)
	}
}

// TestBinaryRevUnstampedGoBinary: a genuine Go binary built outside any VCS
// carries no stamp. This is the post-worktree-move world — polecat worktrees
// outside the repo make Go find no VCS and stamp nothing — and it must read as
// UNKNOWN rather than as "behind main".
func TestBinaryRevUnstampedGoBinary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	src := t.TempDir() // not a git repo
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/bare\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "bare")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if got := BinaryRev(bin); got != RevUnstamped {
		t.Errorf("BinaryRev(no-vcs build) = %q, want %q", got, RevUnstamped)
	}
}

// TestInstalledBinPrecedence: POGO_GOBIN wins, then $PATH — what would
// ACTUALLY run if you typed the name — then the go install default.
func TestInstalledBinPrecedence(t *testing.T) {
	gobin := t.TempDir()
	t.Setenv("POGO_GOBIN", gobin)
	if got := InstalledBin("pogod"); got != filepath.Join(gobin, "pogod") {
		t.Errorf("POGO_GOBIN ignored: got %q", got)
	}

	t.Setenv("POGO_GOBIN", "")
	pathDir := t.TempDir()
	onPath := filepath.Join(pathDir, "pogod-selfdrift-test")
	if err := os.WriteFile(onPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	if got := InstalledBin("pogod-selfdrift-test"); got != onPath {
		t.Errorf("PATH lookup ignored: got %q, want %q", got, onPath)
	}

	// Nothing on PATH: fall back to where `go install` writes, so a binary
	// installed outside PATH is still reported rather than shrugged at.
	fakeGopath := t.TempDir()
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", fakeGopath)
	want := filepath.Join(fakeGopath, "bin", "pogod-selfdrift-absent")
	if got := InstalledBin("pogod-selfdrift-absent"); got != want {
		t.Errorf("GOPATH fallback: got %q, want %q", got, want)
	}
	t.Setenv("GOBIN", filepath.Join(fakeGopath, "explicit"))
	want = filepath.Join(fakeGopath, "explicit", "pogod-selfdrift-absent")
	if got := InstalledBin("pogod-selfdrift-absent"); got != want {
		t.Errorf("GOBIN fallback: got %q, want %q", got, want)
	}
}

// TestRunningRev covers the four things the daemon can do, because three of
// them are not "clean" and must not collapse into one another.
func TestRunningRev(t *testing.T) {
	t.Run("reports the revision it self-reports", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/version" {
				t.Errorf("probed %s, want /version", r.URL.Path)
			}
			w.Write([]byte(`{"revision":"deadbeef","go_version":"go1.25.0"}`))
		}))
		defer srv.Close()
		rev, url := runningRev(srv.URL)
		if rev != "deadbeef" {
			t.Errorf("rev = %q, want deadbeef", rev)
		}
		if !strings.HasSuffix(url, "/version") {
			t.Errorf("url = %q, want it to name the probe target", url)
		}
	})

	t.Run("a daemon that answers without a revision is UNSTAMPED, not unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"go_version":"go1.25.0"}`))
		}))
		defer srv.Close()
		if rev, _ := runningRev(srv.URL); rev != RevUnstamped {
			t.Errorf("rev = %q, want %q", rev, RevUnstamped)
		}
	})

	t.Run("a pogod too old to serve /version is UNREACHABLE", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()
		if rev, _ := runningRev(srv.URL); rev != RevUnreachable {
			t.Errorf("rev = %q, want %q", rev, RevUnreachable)
		}
	})

	t.Run("nothing listening is UNREACHABLE", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close() // free the port, then probe it
		if rev, _ := runningRev(url); rev != RevUnreachable {
			t.Errorf("rev = %q, want %q", rev, RevUnreachable)
		}
	})

	t.Run("a trailing slash does not produce //version", func(t *testing.T) {
		var path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			w.Write([]byte(`{"revision":"abc"}`))
		}))
		defer srv.Close()
		runningRev(srv.URL + "/")
		if path != "/version" {
			t.Errorf("probed %q, want /version", path)
		}
	})
}

// TestResolveRepo: an explicit --repo wins and a bad one is an ERROR, never a
// silent fall-through to a guess — a check that quietly compares against a
// repo the operator did not name is worse than one that refuses.
func TestResolveRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo, _, _ := gitRepoWithBinary(t) // a git repo, but NOT the pogo module
	t.Setenv("POGO_REPO", "")

	t.Run("a git repo that is not pogo is refused", func(t *testing.T) {
		got, note := ResolveRepo(repo)
		if got != "" {
			t.Errorf("resolved %q, want refusal", got)
		}
		if !strings.Contains(note, "not the pogo module") {
			t.Errorf("note = %q, want it to say why", note)
		}
	})

	t.Run("a non-repo path is refused", func(t *testing.T) {
		got, note := ResolveRepo(filepath.Join(t.TempDir(), "nowhere"))
		if got != "" || !strings.Contains(note, "not a git repository") {
			t.Errorf("got %q / %q, want a refusal naming the reason", got, note)
		}
	})

	t.Run("a pogo checkout is accepted", func(t *testing.T) {
		makePogoModule(t, repo)
		got, note := ResolveRepo(repo)
		if got != repo {
			t.Errorf("resolved %q, want %q (note: %s)", got, repo, note)
		}
		if note != "from --repo" {
			t.Errorf("note = %q", note)
		}
	})

	t.Run("POGO_REPO is honored, and a bad one says so", func(t *testing.T) {
		t.Setenv("POGO_REPO", repo)
		if got, _ := ResolveRepo(""); got != repo {
			t.Errorf("POGO_REPO ignored: got %q", got)
		}
		t.Setenv("POGO_REPO", filepath.Join(t.TempDir(), "nope"))
		got, note := ResolveRepo("")
		if got != "" || !strings.Contains(note, "not a pogo checkout") {
			t.Errorf("got %q / %q", got, note)
		}
	})

	t.Run("no checkout is a normal answer with a stated reason", func(t *testing.T) {
		t.Setenv("POGO_REPO", "")
		t.Chdir(t.TempDir())
		got, note := ResolveRepo("")
		if got != "" {
			t.Fatalf("resolved %q from an unrelated cwd", got)
		}
		for _, frag := range []string{"--repo", "POGO_REPO"} {
			if !strings.Contains(note, frag) {
				t.Errorf("note %q does not tell the reader about %s", note, frag)
			}
		}
	})
}

// makePogoModule rewrites a scratch repo's go.mod so it claims the pogo module
// path — the only thing ResolveRepo uses to decide a directory is the source.
func makePogoModule(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte(pogoModule+"\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestMainRevAndRevInRepo exercise the third axis and the foreign-stamp test
// against a real git repo.
func TestMainRevAndRevInRepo(t *testing.T) {
	repo, _, rev := gitRepoWithBinary(t)

	if got := MainRev(repo, "HEAD"); got != rev {
		t.Errorf("MainRev = %q, want %q", got, rev)
	}
	if got := MainRev(repo, "no-such-ref"); got != "" {
		t.Errorf("MainRev(bad ref) = %q, want empty so the axis reports unavailable", got)
	}
	if !RevInRepo(repo, rev) {
		t.Errorf("RevInRepo says the repo lacks its own HEAD")
	}
	// A real-looking commit this repo has never heard of: FOREIGN, and that is
	// a different finding from "behind".
	if RevInRepo(repo, "0123456789012345678901234567890123456789") {
		t.Errorf("RevInRepo accepted a commit that does not exist")
	}
	if RevInRepo("", rev) || RevInRepo(repo, RevUnstamped) {
		t.Errorf("RevInRepo must refuse an empty repo or a sentinel")
	}
}

// TestHostDepsIsWired: every field must be set, or Check panics at the first
// nil call on a consumer's machine and the drift report becomes a crash.
func TestHostDepsIsWired(t *testing.T) {
	d := HostDeps("")
	if d.RunningRev == nil || d.InstalledBin == nil || d.BinaryRev == nil ||
		d.ResolveRepo == nil || d.MainRev == nil || d.RevInRepo == nil {
		t.Fatalf("HostDeps left a dependency nil: %+v", d)
	}
}
