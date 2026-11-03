package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The git half of `pogo presubmit` must be the shell it replaced (enth-vcs §16
// row 6: "git presubmit equal to today's shell"). This test does not restate
// the shell: it runs presubmit_check as the polecat templates carried it until
// mg-71014 (kept verbatim in testdata/presubmit_check.sh), and `pogo
// presubmit`, on the same trees, and requires the same stdout and the same
// exit status in every case — each refusal, and the pass.

type presubmitFixture struct {
	t      *testing.T
	root   string
	origin string
	work   string
	env    []string
}

func (f *presubmitFixture) git(dir string, args ...string) string {
	f.t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = f.env
	out, err := c.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *presubmitFixture) commit(file, text string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.work, file), []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(f.work, "add", file)
	f.git(f.work, "commit", "-q", "-m", "c "+file)
}

// newPresubmitFixture: a bare origin with main, and a polecat clone on
// polecat-x branched from it.
func newPresubmitFixture(t *testing.T) *presubmitFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	os.MkdirAll(home, 0o755)
	os.MkdirAll(bin, 0o755)
	// The shell calls `pogo worktree-dirty`: the binary under test.
	if err := os.Symlink(pogoBin, filepath.Join(bin, "pogo")); err != nil {
		t.Fatal(err)
	}
	f := &presubmitFixture{t: t, root: root, origin: filepath.Join(root, "origin.git"), work: filepath.Join(root, "work")}
	f.env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "POGO_HOME=",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
		"BRANCH=", "POGO_AGENT_NAME=")
	f.git(root, "init", "-q", "--bare", "--initial-branch=main", f.origin)
	f.git(root, "clone", "-q", f.origin, f.work)
	f.git(f.work, "checkout", "-q", "-b", "main")
	f.commit("README", "r\n")
	f.git(f.work, "push", "-q", "origin", "main")
	f.git(f.work, "checkout", "-q", "-b", "polecat-x")
	return f
}

type presubmitRun struct {
	stdout string
	code   int
}

func runIn(t *testing.T, dir string, env []string, name string, args ...string) presubmitRun {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Env = env
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &bytes.Buffer{}
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return presubmitRun{out.String(), code}
}

func TestPresubmitGitEqualsTheTemplateShell(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	cases := []struct {
		name   string
		target string
		setup  func(f *presubmitFixture)
		wantOK bool
		want   string // a fragment of the expected stdout (a control on the case itself)
	}{
		{"pushed, one commit ahead", "main", func(f *presubmitFixture) {
			f.commit("a", "a\n")
			f.git(f.work, "push", "-q", "origin", "polecat-x")
		}, true, "pre-submit OK: 1 commit(s) ahead of origin/main"},
		{"pushed, two commits ahead", "main", func(f *presubmitFixture) {
			f.commit("a", "a\n")
			f.commit("b", "b\n")
			f.git(f.work, "push", "-q", "origin", "polecat-x")
		}, true, "pre-submit OK: 2 commit(s)"},
		{"only pogod's own files untracked", "main", func(f *presubmitFixture) {
			f.commit("a", "a\n")
			f.git(f.work, "push", "-q", "origin", "polecat-x")
			os.MkdirAll(filepath.Join(f.work, ".claude"), 0o755)
			os.WriteFile(filepath.Join(f.work, ".claude", "settings.local.json"), []byte("{}"), 0o644)
		}, true, "pre-submit OK"},
		{"uncommitted work", "main", func(f *presubmitFixture) {
			f.commit("a", "a\n")
			f.git(f.work, "push", "-q", "origin", "polecat-x")
			os.WriteFile(filepath.Join(f.work, "stray"), []byte("s"), 0o644)
			os.WriteFile(filepath.Join(f.work, "README"), []byte("edited\n"), 0o644)
		}, false, "uncommitted work in this worktree"},
		{"nothing ahead of the target", "main", func(f *presubmitFixture) {
			f.git(f.work, "push", "-q", "origin", "polecat-x")
		}, false, "carries no commits ahead of origin/main"},
		{"not pushed", "main", func(f *presubmitFixture) {
			f.commit("a", "a\n")
		}, false, "origin/polecat-x is not your HEAD"},
		{"pushed, then a new commit", "main", func(f *presubmitFixture) {
			f.commit("a", "a\n")
			f.git(f.work, "push", "-q", "origin", "polecat-x")
			f.commit("b", "b\n")
		}, false, "is not your HEAD"},
		{"target does not exist", "nope", func(f *presubmitFixture) {
			f.commit("a", "a\n")
			f.git(f.work, "push", "-q", "origin", "polecat-x")
		}, false, "git rev-list origin/nope..HEAD failed"},
		{"fetch fails", "main", func(f *presubmitFixture) {
			f.commit("a", "a\n")
			f.git(f.work, "push", "-q", "origin", "polecat-x")
			f.git(f.work, "remote", "set-url", "origin", filepath.Join(f.root, "gone.git"))
		}, false, "git fetch origin failed"},
	}
	shell := presubmitFunc(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newPresubmitFixture(t)
			c.setup(f)
			script := shell + "\nBRANCH=$(git rev-parse --abbrev-ref HEAD)\npresubmit_check \"$1\""
			sh := runIn(t, f.work, f.env, "bash", "-c", script, "presubmit", c.target)
			// The verb never re-fetches anything the shell left: run it on
			// the same tree straight after.
			verb := runIn(t, f.work, f.env, pogoBin, "presubmit", "--item=mg-x", "--target="+c.target)
			if (sh.code == 0) != c.wantOK || !strings.Contains(sh.stdout, c.want) {
				t.Fatalf("control: the template shell itself answered exit %d:\n%s\nwant ok=%v and %q", sh.code, sh.stdout, c.wantOK, c.want)
			}
			if sh.stdout != verb.stdout || (sh.code == 0) != (verb.code == 0) {
				t.Errorf("pogo presubmit differs from the template's shell.\nshell (exit %d):\n%s\npogo presubmit (exit %d):\n%s",
					sh.code, sh.stdout, verb.code, verb.stdout)
			}
		})
	}
}

// `pogo refinery submit --item` (mg-30da0): the item reaches pogod, a branch
// is no longer required beside it, and neither one is refused locally.
func TestRefinerySubmitItemFlag(t *testing.T) {
	var body string
	handler := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"mr-1"}`))
	}
	out, stderr, code := runPogo(t, handler, "refinery", "submit", "--item=mg-7", "--repo=/some/repo", "--author=mg-7")
	if code != 0 || !strings.Contains(out, "Submitted mg-7 to merge queue (id=mr-1)") {
		t.Fatalf("submit --item: exit %d\n%s\n%s", code, out, stderr)
	}
	if !strings.Contains(body, `"item":"mg-7"`) || !strings.Contains(body, `"branch":""`) {
		t.Errorf("request body = %s; want the item and no branch", body)
	}
	// The git control: a branch alone is today's request, with no item key.
	if _, _, code := runPogo(t, handler, "refinery", "submit", "polecat-x", "--repo=/some/repo"); code != 0 || strings.Contains(body, `"item"`) {
		t.Errorf("plain submit: exit %d, body %s", code, body)
	}
	if _, stderr, code := runPogo(t, handler, "refinery", "submit", "--repo=/some/repo"); code == 0 || !strings.Contains(stderr, "a branch or --item is required") {
		t.Errorf("neither branch nor item: exit %d, %s", code, stderr)
	}
}
