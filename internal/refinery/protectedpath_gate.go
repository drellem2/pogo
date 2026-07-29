package refinery

import (
	"fmt"
	"path"
	"strings"
)

// protectedPathsFile is the repo-relative path of the protected-path list.
// The list is DATA, not code and not prose: adding a red line is an edit to
// this file, reviewable as a diff, rather than a paragraph appended to
// whichever prompt or ticket happens to be about the subject. That
// distribution — "red line" as a social fact spread across ticket bodies — is
// how the mg-1935 breach walked in (commit a3f0efa, +38 lines into
// internal/agent/prompts/mayor.md, merged by the refinery at 03:39Z with
// nothing objecting).
//
// It sits at the repo root, deliberately NOT under .pogo/. That directory is
// gitignored here (.gitignore line 5) and by convention in any repo pogo
// manages, because it holds runtime state — so a list placed there would be
// untracked, `git ls-tree origin/main` would find nothing, and the gate would
// report no protected paths while a file sat on disk saying otherwise. Silent
// zero protection that reads as protection is the exact defect this gate
// exists to remove, so the list lives where tracking is the default.
const protectedPathsFile = ".protected-paths"

// checkProtectedPaths refuses any branch whose merge would change a path
// listed in the target's protected-path file. It runs on the REBASED branch,
// beside checkClosingRefs — after rebase, before the quality gates, and never
// skipped by [gates] skip_on_retry.
//
// WHY HERE, AND NOT IN CI (mg-6c4b, mg-2a50).
//
// The obvious placement is a GitHub Actions job on `pull_request`. It is
// structurally wrong, and the reason is observable rather than theoretical.
// The refinery does not merge through pull requests: attemptMerge runs
// `git merge --ff-only` and `git push origin <target>` directly (merge.go),
// and `gh pr close` runs only afterwards, to tidy a PR that already exists.
// The merge that carried the breach, MR mr-d9kmdoqtjv1m5em5h9og on branch
// polecat-1935, logged `step=pr-close skipped: no PR for branch polecat-1935`
// — there was no pull request for a `pull_request` job to run on. So:
//
//   - `on: pull_request` never fires for this route. Not leaky — inapplicable.
//   - `on: push` fires after the write. That detects; it does not prevent.
//   - Branch protection would reject the refinery's own push, i.e. stop
//     merging outright.
//
// GitHub is downstream of a push that already happened. The refinery is the
// process that performs the write, so the write boundary is here.
//
// WHY NO BYPASS, AND WHY THAT IS NOT AN OVERSIGHT.
//
// There is deliberately no allow-flag, no label, no CODEOWNERS hook, no
// marker file, no environment variable. A protected path is refused
// unconditionally.
//
// An authorisation mechanism would have to distinguish "Daniel approved this"
// from "an agent claims Daniel approved this", and on this machine that
// distinction does not exist: every agent holds a GH_TOKEN authenticating as
// drellem2, so an agent can open, label, approve and satisfy CODEOWNERS as
// Daniel. Agents also run with --dangerously-skip-permissions, so any local
// marker file is equally producible. Anything an agent can read the rules for,
// it can produce — which converts a gate into an opt-out, and an opt-out gate
// routes back through somebody remembering, which is the control class that
// already failed.
//
// Daniel's route is not a bypass but a different mechanism: he pushes by hand,
// which never enters the refinery. Nothing is presented, so nothing can be
// forged. The stated cost is that prompt changes always require a manual push
// from him.
//
// A PreToolUse hook cannot carry this either. The existing scope guard's case
// statement covers Edit/Write/MultiEdit/NotebookEdit; `Bash` falls to the
// default branch and is allowed under a debug line asserting it "does not
// write", so a `cat >` heredoc walks past it regardless of polarity or
// wiring — and the tool set is open, so enumerating more tools does not close
// it. A gate reads the diff and does not care which tool produced it.
//
// The error is deliberately NOT wrapped in retryableError: the offending
// content is fixed in pushed commits, so every retry would fail identically
// and burn the attempt budget before reporting a cause the author can act on.
func checkProtectedPaths(wtDir, targetRef, branch string) error {
	patterns, err := loadProtectedPatterns(wtDir, targetRef)
	if err != nil {
		return err
	}

	changed, err := changedPaths(wtDir, targetRef, branch)
	if err != nil {
		return err
	}

	type hit struct{ file, pattern string }
	var hits []hit
	for _, f := range changed {
		for _, p := range patterns {
			if matchProtected(p, f) {
				hits = append(hits, hit{f, p})
				break
			}
		}
	}
	if len(hits) == 0 {
		return nil
	}

	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "  %s  (matches %q)\n", h.file, h.pattern)
	}
	return fmt.Errorf("protected path — the refinery will not merge this branch:\n\n%s\n"+
		"These paths are listed in %s on %s and are refused unconditionally.\n"+
		"There is no bypass flag, label, or approval that unlocks them: on this machine "+
		"every agent authenticates as the repo owner, so any token an agent could present "+
		"is one an agent could forge.\n\n"+
		"If the change is wanted, it lands by a route that is not the refinery — Daniel "+
		"pushes it by hand. Drop these paths from the branch and resubmit the rest.",
		b.String(), protectedPathsFile, targetRef)
}

// loadProtectedPatterns reads the protected-path list from the TARGET ref, not
// from the branch under merge, and this is the load-bearing detail. A list read
// from the branch would let a branch delete the list and merge itself; read
// from origin/<target> it is the state already on main, which the branch cannot
// reach. The list file's own path is protected implicitly (see below) so the
// only way to change the list is the same hand-push route as any other
// protected path.
//
// A missing list means no protected paths are declared, and merging proceeds —
// that is a configured absence, not an unreadable answer. A list that exists
// but cannot be read or parsed refuses the merge, on the same principle as
// checkClosingRefs: an unreadable answer and an all-clear must not be
// indistinguishable.
func loadProtectedPatterns(wtDir, targetRef string) ([]string, error) {
	ref := "origin/" + targetRef
	spec := ref + ":" + protectedPathsFile

	// ls-tree exits 0 with empty output when the path is absent, so absence
	// costs no error path and no misleading "git failed" log line.
	out, err := gitCmdOutput(wtDir, "ls-tree", "--name-only", ref, "--", protectedPathsFile)
	if err != nil {
		return nil, fmt.Errorf("could not list %s on %s: %s: %w", protectedPathsFile, ref, out, err)
	}
	if strings.TrimSpace(out) == "" {
		// No list on the target: the repo has declared no protected paths, so
		// there is nothing to protect — including the list's own path. The
		// self-protection entry deliberately does NOT apply here. Applying it
		// would mean no branch could ever introduce the list through the
		// refinery, making the mechanism installable only by hand-push, and it
		// would buy nothing: a repo with no list has no red line to defend.
		// The file becomes self-protecting the moment it exists on the target.
		return nil, nil
	}

	content, err := gitCmdOutput(wtDir, "show", spec)
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %s: %w", spec, content, err)
	}

	patterns, err := parseProtectedPaths(content)
	if err != nil {
		return nil, fmt.Errorf("%s on %s is unusable: %w", protectedPathsFile, ref, err)
	}
	// The list protects itself whether or not it lists itself. Removing the
	// self-entry from the file must not be a way to make the file removable.
	return append(patterns, protectedPathsFile), nil
}

// parseProtectedPaths parses the list file. One pattern per line; `#` starts a
// comment; blank lines are ignored. Supported forms:
//
//	internal/agent/prompts/**    the whole subtree
//	docs/design/frozen.md        one exact repo-relative path
//	hooks/*.sh                   a single path segment glob (path.Match)
//
// An unsupported pattern is a hard parse error rather than a skipped line. A
// red line that silently fails to match is worse than no red line, because it
// reads as protection while providing none — which is precisely the defect
// this gate exists to remove.
func parseProtectedPaths(content string) ([]string, error) {
	var patterns []string
	for i, line := range strings.Split(content, "\n") {
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if err := validateProtectedPattern(line); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		patterns = append(patterns, line)
	}
	return patterns, nil
}

// validateProtectedPattern rejects anything matchProtected cannot honour
// exactly. See parseProtectedPaths for why this refuses rather than skips.
func validateProtectedPattern(p string) error {
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("pattern %q must be repo-relative (no leading slash)", p)
	}
	if p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") {
		return fmt.Errorf("pattern %q must not contain %q", p, "..")
	}
	if strings.Contains(p, "**") && (!strings.HasSuffix(p, "/**") || strings.Count(p, "**") != 1) {
		return fmt.Errorf("pattern %q: %q is supported only as a trailing %q (a whole subtree)", p, "**", "/**")
	}
	// path.Match reports a malformed pattern (unclosed [, trailing \) only
	// while matching, so probe it against a throwaway name.
	probe := strings.TrimSuffix(p, "/**")
	if _, err := path.Match(probe, "probe"); err != nil {
		return fmt.Errorf("pattern %q is not a valid path pattern: %w", p, err)
	}
	return nil
}

// matchProtected reports whether a repo-relative path is covered by a pattern.
func matchProtected(pattern, file string) bool {
	if strings.HasSuffix(pattern, "/**") {
		// "internal/agent/prompts/**" covers everything below the directory
		// at any depth. The trailing "*" is trimmed, not the whole "/**", so
		// the prefix keeps its separator and cannot match a sibling named
		// "internal/agent/promptsX".
		return strings.HasPrefix(file, strings.TrimSuffix(pattern, "**"))
	}
	if pattern == file {
		return true
	}
	ok, err := path.Match(pattern, file)
	return err == nil && ok
}

// changedPaths returns every repo-relative path the branch changes relative to
// the target, one per line.
//
// --no-renames is deliberate. With rename detection on, moving a protected
// file OUT of its protected directory reports only the destination path, and a
// rename away from a red line would read as a change to an unprotected path.
// Disabled, the same move reports as a delete plus an add, so the protected
// source path is named and the refusal fires.
//
// The three-dot form asks what the branch ADDS relative to the merge base,
// which is what the push will land. It is also correct if the branch has
// somehow not been rebased: two-dot would report the target's own commits as
// branch changes and refuse merges the branch had no part in.
func changedPaths(wtDir, targetRef, branch string) ([]string, error) {
	out, err := gitCmdOutput(wtDir, "diff", "--name-only", "--no-renames",
		"origin/"+targetRef+"..."+branch)
	if err != nil {
		// Same reasoning as checkClosingRefs: a failed enumeration is not a
		// clean bill of health, and must not be mistaken for one.
		return nil, fmt.Errorf("could not enumerate paths changed by %s against %s: %s: %w",
			branch, targetRef, strings.TrimSpace(out), err)
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}
