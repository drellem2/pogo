// Package enthdtest runs a sandboxed enthd for tests outside internal/vcs/enthvcs
// (enth-vcs slice 7, mg-1fe2b: the refinery's land tests).
//
// It serves alice/proj, an enth-written repo whose trunk holds a.txt ("a1\na2\n")
// and src/b.txt, exactly as enthvcs's own in-package helper does. The enth
// binaries come from $POGO_ENTH_BIN_DIR, else PATH; an enth that predates
// enth #9 skips the test, and POGO_ENTH_REQUIRE=1 turns that skip into a
// failure. It must not import enthvcs: enthvcs's in-package tests may use it.
package enthdtest

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
)

// Server is a running sandboxed enthd.
type Server struct {
	// BinDir holds enth, enthd, enth-store, enth-change and enth-land.
	BinDir string
	Root   string
	URL    string
	Token  string
	Repo   string
	// RootID is the trunk's first (and, at start, only) Change.
	RootID string
}

var (
	hashRE   = regexp.MustCompile(`"hash":"([0-9a-f]+)"`)
	tokenRE  = regexp.MustCompile(`"token":"(enth_[0-9a-f]+)"`)
	addrRE   = regexp.MustCompile(`"addr":"([^"]+)"`)
	idRE     = regexp.MustCompile(`"id":"([0-9a-f]{64})"`)
	changeRE = regexp.MustCompile(`"change":"([0-9a-f]{64})"`)
)

// BinDir finds an enth #9 or later, or skips t.
func BinDir(t testing.TB) string {
	t.Helper()
	skip := func(why string) string {
		if os.Getenv("POGO_ENTH_REQUIRE") == "1" {
			t.Fatalf("POGO_ENTH_REQUIRE=1: %s", why)
		}
		t.Skip(why)
		return ""
	}
	dir := os.Getenv("POGO_ENTH_BIN_DIR")
	if dir == "" {
		p, err := exec.LookPath("enthd")
		if err != nil {
			return skip("no enthd on PATH and POGO_ENTH_BIN_DIR unset")
		}
		dir = filepath.Dir(p)
	}
	for _, b := range []string{"enth", "enthd", "enth-store", "enth-change", "enth-land"} {
		if _, err := os.Stat(filepath.Join(dir, b)); err != nil {
			return skip(fmt.Sprintf("%s has no %s", dir, b))
		}
	}
	help, _ := exec.Command(filepath.Join(dir, "enth"), "--help").CombinedOutput()
	if !bytes.Contains(help, []byte("status DIR")) {
		return skip(fmt.Sprintf("%s/enth predates enth #9 (no status verb)", dir))
	}
	land, _ := exec.Command(filepath.Join(dir, "enth"), "land", "--help").CombinedOutput()
	if !bytes.Contains(land, []byte("--expect-new")) {
		return skip(fmt.Sprintf("%s/enth land has no --expect-new (enth #9 item D)", dir))
	}
	return dir
}

// Cmd runs one of the enth binaries with the server's token and a fixed
// author/committer, and fails t on error.
func (s *Server) Cmd(t testing.TB, name string, args ...string) string {
	t.Helper()
	out, err := s.Try(name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

// Try is Cmd without the failure.
func (s *Server) Try(name string, args ...string) (string, error) {
	out, err := s.Command(name, args...).CombinedOutput()
	return string(out), err
}

// Command is one of the enth binaries, unstarted, with the server's token and
// a fixed author/committer: for a caller that wants stdout apart from stderr,
// as a worker reading `enth land`'s answer does.
func (s *Server) Command(name string, args ...string) *exec.Cmd {
	c := exec.Command(filepath.Join(s.BinDir, name), args...)
	c.Env = append(os.Environ(), "ENTH_TOKEN="+s.Token,
		"ENTH_AUTHOR_NAME=worker", "ENTH_AUTHOR_EMAIL=worker@example.com",
		"ENTH_COMMITTER_NAME=worker", "ENTH_COMMITTER_EMAIL=worker@example.com")
	return c
}

// Remote is the global-flag prefix that sends an enth verb to this server.
func (s *Server) Remote() []string { return []string{"--remote", s.URL, "--repo", s.Repo} }

// EnthRepo is the project-record setting for this server's repo.
func (s *Server) EnthRepo() vcs.EnthRepo {
	return vcs.EnthRepo{Remote: s.URL, Repo: s.Repo, Trunk: vcs.DefaultEnthTrunk}
}

// Enth is the enth executable.
func (s *Server) Enth() string { return filepath.Join(s.BinDir, "enth") }

func (s *Server) put(t testing.TB, store, file string) string {
	t.Helper()
	m := hashRE.FindStringSubmatch(s.Cmd(t, "enth-store", "--store", store, "put", file))
	if m == nil {
		t.Fatalf("enth-store put %s: no hash", file)
	}
	return m[1]
}

// Start creates alice/proj and serves it until t ends. ENTH_TOKEN is set in
// the test's environment, as pogod's children get it; ENTH_REMOTE and
// ENTH_REPO are cleared so nothing reaches a default remote.
func Start(t testing.TB) *Server {
	t.Helper()
	s := &Server{BinDir: BinDir(t), Root: filepath.Join(t.TempDir(), "R"), Repo: "alice/proj"}
	m := tokenRE.FindStringSubmatch(s.Cmd(t, "enthd", "--root", s.Root, "user", "add", "alice", "--email", "alice@example.com"))
	if m == nil {
		t.Fatal("enthd user add printed no token")
	}
	s.Token = m[1]
	s.Cmd(t, "enthd", "--root", s.Root, "repo", "create", s.Repo, "--owner", "alice")
	store := filepath.Join(s.Root, "repos", "alice~proj")
	w := t.TempDir()
	os.WriteFile(filepath.Join(w, "a"), []byte("a1\na2\n"), 0o644)
	os.WriteFile(filepath.Join(w, "b"), []byte("b1\n"), 0o644)
	ha, hb := s.put(t, store, filepath.Join(w, "a")), s.put(t, store, filepath.Join(w, "b"))
	spec := fmt.Sprintf(`{"parents":[],"author":{"name":"ann","email":"ann@example.com","time":1700000000,"tz":"+0000"},"committer":{"name":"ann","email":"ann@example.com","time":1700000000,"tz":"+0000"},"message":"root\n","ops":[{"path":"a.txt","op":"put","blob":"%s","mode":"file"},{"path":"src/b.txt","op":"put","blob":"%s","mode":"file"}]}`, ha, hb)
	os.WriteFile(filepath.Join(w, "spec"), []byte(spec), 0o644)
	s.Cmd(t, "enth-change", "build", "--record", filepath.Join(w, "rec"), filepath.Join(w, "spec"))
	s.RootID = s.put(t, store, filepath.Join(w, "rec"))
	s.Cmd(t, "enth-land", "--store", store, "--user", "operator", s.RootID)

	srv := exec.Command(filepath.Join(s.BinDir, "enthd"), "--root", s.Root, "serve", "--listen", "127.0.0.1:0", "--follow-interval", "0")
	stdout, err := srv.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Process.Kill(); srv.Wait() })
	addr := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if m := addrRE.FindStringSubmatch(sc.Text()); m != nil {
				addr <- m[1]
				break
			}
		}
		for sc.Scan() {
		}
	}()
	select {
	case a := <-addr:
		s.URL = "http://" + a
	case <-time.After(20 * time.Second):
		t.Fatal("enthd did not report listening")
	}
	t.Setenv("ENTH_TOKEN", s.Token)
	t.Setenv("ENTH_REMOTE", "")
	t.Setenv("ENTH_REPO", "")
	return s
}

// Checkout checks rev out of the server into dir and returns the Change it
// checked out.
func (s *Server) Checkout(t testing.TB, rev, dir string) string {
	t.Helper()
	out := s.Cmd(t, "enth", append(s.Remote(), "checkout", rev, dir)...)
	m := changeRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("enth checkout %s: no change in %q", rev, out)
	}
	return m[1]
}

// Change makes a Change of dir's edits through the server, as a worker
// would, and returns its id.
func (s *Server) Change(t testing.TB, dir, msg string) string {
	t.Helper()
	out := s.Cmd(t, "enth", "change", "-m", msg, dir)
	m := idRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("enth change: no id in %q", out)
	}
	return m[1]
}

// Work checks the trunk out into a fresh dir, writes files (path → content),
// and makes one Change carrying the item's trailers. It returns the Change id
// and the checkout.
func (s *Server) Work(t testing.TB, item, worker, subject string, files map[string]string) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "w")
	s.Checkout(t, vcs.DefaultEnthTrunk, dir)
	for p, c := range files {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	msg := subject + "\n\n" + vcs.TrailerItem + ": " + item + "\n"
	if worker != "" {
		msg += vcs.TrailerWorker + ": " + worker + "\n"
	}
	return s.Change(t, dir, msg), dir
}

// Land lands ids on the trunk directly (another merge winning a race).
func (s *Server) Land(t testing.TB, ids ...string) string {
	t.Helper()
	return s.Cmd(t, "enth", append(append(s.Remote(), "land"), ids...)...)
}

// Tip is the trunk's tip now.
func (s *Server) Tip(t testing.TB) string {
	t.Helper()
	out := s.Cmd(t, "enth", append(s.Remote(), "query", "show", vcs.DefaultEnthTrunk)...)
	m := changeRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("enth query show: no change in %q", out)
	}
	return m[1]
}

// RequireRef skips t unless s's enth has enth #9 item K (mg-40d5e): `enth
// tag` and `enth ref create` through enthd. POGO_ENTH_REQUIRE=1 turns the
// skip into a failure, as BinDir does.
func (s *Server) RequireRef(t testing.TB) {
	t.Helper()
	help, _ := exec.Command(filepath.Join(s.BinDir, "enth"), "ref", "--help").CombinedOutput()
	if _, err := os.Stat(filepath.Join(s.BinDir, "enth-ref")); err != nil || !bytes.Contains(help, []byte("create refs/heads/NAME")) {
		why := fmt.Sprintf("%s has no enth-ref with `ref create` (enth #9 item K, mg-40d5e)", s.BinDir)
		if os.Getenv("POGO_ENTH_REQUIRE") == "1" {
			t.Fatalf("POGO_ENTH_REQUIRE=1: %s", why)
		}
		t.Skip(why)
	}
}
