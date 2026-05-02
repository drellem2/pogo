package agent

import (
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Defaults for recent-activity context surfaced into the polecat prompt.
// Sized to be useful without dominating the prompt — a polecat working the
// Nth ticket of a multi-ticket feature should see the prior N-1 commits
// (each carrying its mg-XXXX in the subject) plus the area of code those
// commits touched, then choose for themselves whether to dig deeper.
const (
	defaultRecentCommits = 20
	defaultRecentFiles   = 30
)

// captureRecentCommits returns up to n most recent commits on the source
// repo's checked-out branch in `git log --oneline` format. Returns an empty
// string on any error or when repo is empty: this is a best-effort context
// aid, not a critical path, so spawn must not fail when the helper does.
func captureRecentCommits(repo string, n int) string {
	if repo == "" || n <= 0 {
		return ""
	}
	out, err := exec.Command("git", "-C", repo, "log", "--oneline", "-n", strconv.Itoa(n)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// captureRecentFiles returns the unique set of files touched by the last n
// commits in the source repo, sorted alphabetically and joined with
// newlines. Up to maxFiles entries; the list is truncated with a trailing
// "(... <k> more)" marker when more files were touched. Returns "" on any
// error or when repo is empty.
func captureRecentFiles(repo string, n, maxFiles int) string {
	if repo == "" || n <= 0 || maxFiles <= 0 {
		return ""
	}
	out, err := exec.Command("git", "-C", repo, "log", "--name-only", "--pretty=format:", "-n", strconv.Itoa(n)).Output()
	if err != nil {
		return ""
	}
	seen := make(map[string]struct{})
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		files = append(files, line)
	}
	sort.Strings(files)
	if len(files) > maxFiles {
		extra := len(files) - maxFiles
		files = files[:maxFiles]
		return strings.Join(files, "\n") + "\n(... " + strconv.Itoa(extra) + " more)"
	}
	return strings.Join(files, "\n")
}
