package gitgc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PolecatTreeRepos returns the repositories the linked worktrees under
// polecatsDir belong to, each derived from the tree itself with
// `git rev-parse --git-common-dir` (drellem2/pogo#162).
//
// # Why pogod needs this
//
// pogod's sweep set used to be the configured [gitgc] repos unioned with the
// source repo of every REGISTERED agent — and registered means running. With no
// [gitgc] section, which is the default, a repository left the sweep set the
// moment its last polecat exited, so every tree that polecat left behind sat
// under the polecats dir with nothing that would ever sweep it. Triage counted
// 18 of 42 trees in that state (mg-b292). The orphan-dir scan cannot reach them:
// it handles only directories with NO .git, and these have one.
//
// The repository is the tree's own answer, not the registry's, for the reason
// PolecatNameForWorktree gives about owners: a fact about a tree must come from
// the tree, because the registry entry is exactly what is gone.
//
// # What it returns
//
// repos is deduplicated and sorted. skipped names every directory that HAS a
// .git and still yielded no repository, with why: a tree git would not answer
// for, an unconventional layout, or a tree that is its own repository. Those are
// reported rather than dropped so pogod can log them; a directory with no .git
// at all is a reaped shell, the orphan-dir scan's business, and is skipped
// silently. A missing polecats dir is no trees, not an error.
func PolecatTreeRepos(polecatsDir string) (repos, skipped []string, err error) {
	if polecatsDir == "" {
		return nil, nil, nil
	}
	entries, err := os.ReadDir(polecatsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read polecats dir %s: %w", polecatsDir, err)
	}
	root := filepath.Clean(polecatsDir)
	if real, rerr := filepath.EvalSymlinks(root); rerr == nil {
		root = real
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(polecatsDir, e.Name())
		if _, serr := os.Lstat(filepath.Join(path, ".git")); serr != nil {
			continue
		}
		out, gerr := git(path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if gerr != nil {
			skipped = append(skipped, fmt.Sprintf("%s: has a .git but git would not name its repository: %v",
				path, gerr))
			continue
		}
		common := filepath.Clean(strings.TrimSpace(string(out)))
		if filepath.Base(common) != ".git" {
			skipped = append(skipped, fmt.Sprintf("%s: git-common-dir %q is not a conventional "+
				"<repo>/.git layout, so the repository could not be named", path, common))
			continue
		}
		repo := filepath.Dir(common)
		// A directory under the polecats dir that is its OWN repository (a full
		// clone, not a linked worktree) is not a source repo to sweep: sweeping
		// it would treat a polecat's tree as the integration repo.
		if within(repo, root) || within(repo, filepath.Clean(polecatsDir)) {
			skipped = append(skipped, fmt.Sprintf("%s: its repository %s is under the polecats dir itself, "+
				"not a source repository", path, repo))
			continue
		}
		if !seen[repo] {
			seen[repo] = true
			repos = append(repos, repo)
		}
	}
	sort.Strings(repos)
	return repos, skipped, nil
}

// within reports whether p is dir or lies beneath it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
