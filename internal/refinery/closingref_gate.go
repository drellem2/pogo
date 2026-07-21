package refinery

import (
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/closingref"
)

// bodySeparator is a NUL byte. Commit bodies are multi-line, unbounded prose
// containing blank lines and arbitrary punctuation, so every printable
// delimiter is a delimiter the body could legitimately contain. NUL is the one
// byte git guarantees a commit message cannot hold.
const bodySeparator = "\x00"

// checkClosingRefs rejects a branch whose commit messages contain a GitHub
// closing-keyword adjacency — `closed` + whitespace + `owner/repo#N`, newlines
// included — unless the author acknowledged it per reference.
//
// WHY HERE (mg-2627). Two hosts were available and the choice is not free:
//
//   - The commit-msg hook (hooks/commit-msg) is earlier and cheaper: it fails
//     at `git commit`, when the message is still in the editor buffer and
//     costs nothing to fix. But it protects ONLY people who ran
//     `git config core.hooksPath hooks`. A polecat spawned into a fresh
//     worktree, a contributor cloning for the first time, or anyone passing
//     --no-verify is unprotected, and the hook cannot know it was skipped.
//
//   - The refinery is the chokepoint: every merge into main passes through
//     attemptMerge, no exceptions, regardless of who authored the branch or
//     what their local git config says. It is later and more expensive — the
//     author has already pushed and has to amend and re-push — but it is the
//     only placement that covers everyone.
//
// We ship both, and this one is the load-bearing half. The hook is an early
// warning, not the guarantee.
//
// This runs on the REBASED branch, after rebase and before quality gates,
// and — unlike the gates — it is never skipped by [gates] skip_on_retry.
// Skipping is justified for gates because retries re-test near-identical code;
// it is not justified here, because the commit messages under inspection are
// exactly what the retry is about to push. A check the retry path can bypass
// is a check the retry path will eventually bypass on the commit that matters.
//
// The error is deliberately NOT wrapped in retryableError: the message is
// fixed text in a pushed commit, so every retry would fail identically and
// burn the attempt budget before reporting a cause the author can act on.
func checkClosingRefs(wtDir, targetRef, branch string) error {
	// %B is the raw body; %x00 terminates each entry. Range excludes commits
	// already on the target — we judge what this branch ADDS, not the history
	// it inherits. Rewriting a landed commit's message is not on the table,
	// and flagging one would wedge every subsequent MR behind it.
	out, err := gitCmdOutput(wtDir, "log", "--format=%H%x00%B%x00",
		"origin/"+targetRef+".."+branch)
	if err != nil {
		// A failed enumeration is NOT a clean bill of health. Same reasoning
		// as ghteardown's "indeterminate" class: an unreadable answer and an
		// all-clear are indistinguishable to a careless check, so we refuse
		// to treat one as the other.
		return fmt.Errorf("could not read commit messages for %s..%s: %s: %w",
			targetRef, branch, strings.TrimSpace(out), err)
	}

	fields := strings.Split(out, bodySeparator)
	var report strings.Builder
	found := 0
	// Entries come in (sha, message) pairs.
	for i := 0; i+1 < len(fields); i += 2 {
		sha := strings.TrimSpace(fields[i])
		message := strings.TrimLeft(fields[i+1], "\n")
		if sha == "" {
			continue
		}
		findings := closingref.Check(message)
		if len(findings) == 0 {
			continue
		}
		found += len(findings)
		short := sha
		if len(short) > 8 {
			short = short[:8]
		}
		report.WriteString(closingref.Report("commit "+short, findings))
		report.WriteString("\n")
	}

	if found == 0 {
		return nil
	}
	return fmt.Errorf("closing-keyword reference in commit message — merging would close issues on GitHub:\n\n%s", report.String())
}
