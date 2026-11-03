package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOriginRepo: `pogo origin-repo` prints origin's OWNER/REPO — not an
// upstream remote's — and refuses, with nothing on stdout, for an origin gh
// cannot be pinned to, so `R=$(pogo origin-repo) || …` cannot pin to "" (mg-42fe8).
func TestOriginRepo(t *testing.T) {
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	dir := t.TempDir()
	git(dir, "init", "-q")
	git(dir, "remote", "add", "origin", "git@github.com:fork-owner/pogo.git")
	git(dir, "remote", "add", "upstream", "https://github.com/drellem2/pogo.git")
	stdout, stderr, code := runPogoEnv(t, nil, nil, "origin-repo", dir)
	if code != 0 || strings.TrimSpace(stdout) != "fork-owner/pogo" {
		t.Errorf("origin-repo = (%q, %q, exit %d), want fork-owner/pogo", stdout, stderr, code)
	}

	alias := t.TempDir()
	git(alias, "init", "-q")
	git(alias, "remote", "add", "origin", "git@github-work:o/r.git")
	stdout, stderr, code = runPogoEnv(t, nil, []string{"GIT_CEILING_DIRECTORIES=" + filepath.Dir(alias)}, "origin-repo", alias)
	if code == 0 || stdout != "" || !strings.Contains(stderr, "not github.com") {
		t.Errorf("unpinnable origin: origin-repo = (%q, %q, exit %d), want a refusal with empty stdout", stdout, stderr, code)
	}
}
