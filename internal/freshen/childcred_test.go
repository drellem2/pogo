package freshen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/ghtoken"
)

// mg-37183: freshen's git wrapper hands the per-call credential to network
// ops only (fetch, ls-remote), never to a local op.
func TestFreshenGitNetworkChildGetsAPerCallCredential(t *testing.T) {
	dir := t.TempDir()
	runChildCredCases(t, []childCredCase{
		{name: "git", bin: "git",
			net: func(l string) bool { return ghtoken.GitNeedsCredential(strings.Fields(l)) },
			call: func(t *testing.T) {
				git(dir, "fetch", "origin", "main")
				git(dir, "ls-remote", "--exit-code", "--heads", "origin", "refs/heads/main")
				git(dir, "rev-parse", "HEAD")
			}},
	})
}

// childCredToken is a stand-in credential. Never a real one.
const childCredToken = "ghp_childcred_fake_000000000000000000000"

// installCredProbe puts a stub `name` first on PATH that appends one line per
// call to the returned log: its arguments, then "cred=yes" when $GH_TOKEN in
// ITS environment is childCredToken and "cred=no" otherwise. The value itself
// is never written. stdout is what the stub prints.
func installCredProbe(t *testing.T, name, stdout string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, name+".log")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$GH_TOKEN\" = %q ]; then c=yes; else c=no; fi\n"+
		"echo \"$* cred=$c\" >> %q\nprintf '%%s' %q\n", childCredToken, logPath, stdout)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// withChildCred runs fn with the process carrying NO credential and ghtoken's
// per-call sources replaced: yielding childCredToken when give is true, failing
// when false. It fails the test if fn leaves a credential in this process's
// own environment (mg-37183: the value goes to the child, never to os.Environ).
func withChildCred(t *testing.T, give bool, fn func()) {
	t.Helper()
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	fail := func() (string, error) { return "", errors.New("no credential in this arm") }
	shell := fail
	if give {
		shell = func() (string, error) { return childCredToken, nil }
	}
	restore := ghtoken.SetChildHarvestForTest(shell, fail)
	defer restore()
	fn()
	if os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "" {
		t.Fatal("a credential reached this process's own environment")
	}
}

// credCalls returns the probe log's lines.
func credCalls(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// childCredCase is one production call whose gh/git child must receive the
// per-call credential. net reports whether a recorded call line is one that
// should carry it; every other line (local git ops) must not.
type childCredCase struct {
	name   string
	bin    string // "gh" or "git"
	stdout string
	call   func(t *testing.T)
	net    func(line string) bool
}

func allCalls(string) bool { return true }

// runChildCredCases runs every case in two arms. "credential" is the property
// under test: each network child saw the token, no local child did, and this
// process never held it. "control" withholds the credential and requires the
// SAME probe to report cred=no, so a probe that always says yes cannot pass the
// first arm; and bypassing ghtoken.ChildEnv at a call site fails the first arm.
func runChildCredCases(t *testing.T, cases []childCredCase) {
	t.Helper()
	for _, c := range cases {
		for _, give := range []bool{true, false} {
			arm := map[bool]string{true: "credential", false: "control"}[give]
			t.Run(c.name+"/"+arm, func(t *testing.T) {
				logPath := installCredProbe(t, c.bin, c.stdout)
				withChildCred(t, give, func() { c.call(t) })
				lines := credCalls(t, logPath)
				nets := 0
				for _, l := range lines {
					want := "cred=no"
					if c.net(l) {
						nets++
						if give {
							want = "cred=yes"
						}
					}
					if !strings.HasSuffix(l, want) {
						t.Errorf("%s call %q: want %s", c.bin, l, want)
					}
				}
				if nets == 0 {
					t.Fatalf("no network %s call was made (calls: %q) — the case exercises nothing", c.bin, lines)
				}
			})
		}
	}
}
