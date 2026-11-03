package agent

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// ghPRCall matches a gh pr invocation inside a shell line. It is anchored on a
// word boundary so "pogh pr" or prose like "the gh project" cannot fire it.
var ghPRCall = regexp.MustCompile(`(^|[\s($"'])gh\s+pr\s+[a-z]`)

// ghPRPinned is a call that names its repository: -R "$ORIGIN_REPO" in a code
// block that derived it with ORIGIN_REPO=$(pogo origin-repo) || exit 1 (the
// derivation pogod's own lookups use, mg-42fe8), or --repo with an explicit
// slug — the PM template's open-PR pass, which walks several repos and derives
// each slug from that repo's origin itself.
var ghPRPinned = regexp.MustCompile(`-R "\$ORIGIN_REPO"|--repo "?\$`)

// originDerivation is the line a block must carry before -R "$ORIGIN_REPO"
// counts. The `|| exit 1` is the point: gh reads `-R ""` as no -R at all
// (measured, gh 2.x: `gh pr list -R ""` lists the default repo, exit 0), so a
// derivation that can fail silently re-creates the unpinned call.
var originDerivation = regexp.MustCompile(`ORIGIN_REPO=\$\(pogo origin-repo\) \|\| exit 1`)

// unpinnedGHPRCalls lists each gh pr call in a fenced code block of a shipped
// prompt that does not name its repository. Left to choose, gh prefers a
// `gh repo set-default` or an `upstream` remote over origin, so a PR a polecat
// opens unpinned can land in a repository pogod's pinned probe never asks —
// a false stranded-work alert for work that is on an open PR (drellem2/pogo#259).
func unpinnedGHPRCalls(root fs.FS) ([]string, error) {
	var problems []string
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		inFence, derived := false, false
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence, derived = !inFence, false
				continue
			}
			if !inFence {
				continue
			}
			if originDerivation.MatchString(line) {
				derived = true
			}
			if !ghPRCall.MatchString(line) {
				continue
			}
			switch {
			case !ghPRPinned.MatchString(line):
				problems = append(problems, fmt.Sprintf("%s:%d runs gh pr without -R \"$ORIGIN_REPO\": %q",
					path, i+1, strings.TrimSpace(line)))
			case strings.Contains(line, `-R "$ORIGIN_REPO"`) && !derived:
				problems = append(problems, fmt.Sprintf("%s:%d pins to $ORIGIN_REPO but its block never runs "+
					"`ORIGIN_REPO=$(pogo origin-repo) || exit 1` first — an empty -R is no -R: %q",
					path, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	sort.Strings(problems)
	return problems, err
}

// TestShippedPromptsPinEveryGHPRCall is the standing guard: no fenced gh pr call
// in any shipped prompt lets gh choose the repository.
func TestShippedPromptsPinEveryGHPRCall(t *testing.T) {
	problems, err := unpinnedGHPRCalls(promptTreeFS(t))
	if err != nil {
		t.Fatalf("walking prompts: %v", err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// TestGHPRPinScannerFiresOnTheOriginalDefect is the positive control: the exact
// create line polecat-build-pr.md shipped before mg-42fe8 must be flagged, and
// its replacement must not be — or the guard above proves nothing.
func TestGHPRPinScannerFiresOnTheOriginalDefect(t *testing.T) {
	tree := fstest.MapFS{
		"old.md": {Data: []byte("```bash\n   gh pr create --base main --head \"$BRANCH\" \\\n   PR_BRANCH=\"$(gh pr view 7 --json headRefName)\"\n```\n")},
		"new.md": {Data: []byte("```bash\n   ORIGIN_REPO=$(pogo origin-repo) || exit 1\n   gh pr create -R \"$ORIGIN_REPO\" --base main --head \"$BRANCH\" \\\n```\nprose: never `gh pr merge`\n")},
		// The pin with no refusing derivation in its block: the variable is
		// empty in this shell, and gh treats -R "" as unpinned.
		"nodef.md": {Data: []byte("```bash\n   ORIGIN_REPO=$(pogo origin-repo)\n```\n```bash\n   gh pr list -R \"$ORIGIN_REPO\" --head b\n```\n")},
	}
	problems, err := unpinnedGHPRCalls(tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 3 || !strings.HasPrefix(problems[0], "nodef.md:5 ") ||
		!strings.HasPrefix(problems[1], "old.md:2 ") || !strings.HasPrefix(problems[2], "old.md:3 ") {
		t.Errorf("scanner = %q, want exactly nodef.md:5, old.md:2 and old.md:3 flagged", problems)
	}
}
