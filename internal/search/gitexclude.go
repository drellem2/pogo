package search

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// pogoExcludePattern is the gitignore pattern appended to a repo's
// .git/info/exclude so the .pogo/ index directory never shows up as an
// untracked directory in git status. info/exclude is repo-local and never
// committed, so the fix is invisible to the working tree. See gh #40.
const pogoExcludePattern = ".pogo/"

const pogoExcludeComment = "# pogo search index (added automatically by pogo)"

var (
	gitExcludeMu   sync.Mutex
	gitExcludeDone = make(map[string]bool)
)

// ensurePogoGitExcluded makes sure the repo at root gitignores the .pogo/
// index directory via .git/info/exclude. It runs at most once per root per
// process, only acts when root itself is a git repo root (a .git entry is
// directly under it — never a parent repo's), and never fails the caller:
// index writing proceeds even when the exclude can't be updated.
func ensurePogoGitExcluded(root string) {
	gitExcludeMu.Lock()
	if gitExcludeDone[root] {
		gitExcludeMu.Unlock()
		return
	}
	gitExcludeDone[root] = true
	gitExcludeMu.Unlock()

	// Only repo roots qualify. A project dir without its own .git (e.g. a
	// plain directory inside some other repo) must not edit that outer
	// repo's exclude file.
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return
	}

	excludePath := gitExcludePath(root)
	if excludePath == "" {
		return
	}

	data, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case ".pogo", ".pogo/", "/.pogo", "/.pogo/":
			return // already ignored
		}
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		return
	}
	f, err := os.OpenFile(excludePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	var b strings.Builder
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		b.WriteString("\n")
	}
	b.WriteString(pogoExcludeComment + "\n" + pogoExcludePattern + "\n")
	f.WriteString(b.String())
}

// gitExcludePath resolves the info/exclude file for the repo at root.
// `git rev-parse --git-path` handles every layout — plain repo, linked
// worktree (where info/exclude lives in the shared common dir), submodule —
// so we don't hand-parse gitdir/commondir indirection.
//
// The toplevel is cross-checked against root because git skips over an
// invalid .git entry (e.g. an empty dir) and resolves to an enclosing repo —
// whose exclude file we must not touch.
func gitExcludePath(root string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel", "--git-path", "info/exclude")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return ""
	}
	toplevel, exclude := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	if toplevel == "" || exclude == "" || !samePath(toplevel, root) {
		return ""
	}
	if !filepath.IsAbs(exclude) {
		exclude = filepath.Join(root, exclude)
	}
	return exclude
}

// samePath reports whether two paths name the same directory, tolerating
// trailing separators and symlinks (macOS /tmp vs /private/tmp).
func samePath(a, b string) bool {
	return resolvePath(a) == resolvePath(b)
}

func resolvePath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
