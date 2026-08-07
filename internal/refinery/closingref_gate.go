package refinery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/drellem2/pogo/internal/closingref"
)

// bodySeparator is a NUL byte. Commit bodies are multi-line, unbounded prose
// containing blank lines and arbitrary punctuation, so every printable
// delimiter is a delimiter the body could legitimately contain. NUL is the one
// byte git guarantees a commit message cannot hold.
const bodySeparator = "\x00"

// checkClosingRefs rejects a branch whose commit messages OR whose pull request
// body contain a GitHub closing-keyword adjacency — `closed` + whitespace +
// `owner/repo#N`, newlines included — unless the author acknowledged it per
// reference.
//
// WHY TWO ARTIFACTS (mg-f9e0). This gate read commit messages only, while the
// shipped polecat-build-pr template told every builder to put
// "Resolves <owner>/<repo>#<n>" in the PR BODY. A closing keyword there closes
// the issue when the PR merges, and this gate never looked, so the fleet's
// default path ran through the one channel the guard was blind to. The default
// and the guard pointed at different artifacts. Both are inspected now; a guard
// that cannot fail on the default path is not a guard.
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
	commitReport, err := commitClosingRefs(wtDir, targetRef, branch)
	if err != nil {
		return err
	}
	prReport := prBodyClosingRefs(wtDir, branch)
	if commitReport == "" && prReport == "" {
		return nil
	}
	return fmt.Errorf("closing-keyword reference — merging would close issues on GitHub:\n\n%s%s",
		commitReport, prReport)
}

// commitClosingRefs renders the report for every unacknowledged adjacency in
// the commit messages this branch adds to targetRef. An empty string means
// clean; an error means the history could not be read, which is not the same
// thing.
func commitClosingRefs(wtDir, targetRef, branch string) (string, error) {
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
		return "", fmt.Errorf("could not read commit messages for %s..%s: %s: %w",
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
		report.WriteString(closingref.Report(closingref.CommitMessage, "commit "+short, findings))
		report.WriteString("\n")
	}

	if found == 0 {
		return "", nil
	}
	return report.String(), nil
}

// prBodyClosingRefs renders the report for every unacknowledged adjacency in
// the body of the pull request whose head is branch. An empty string means
// "nothing GitHub would act on" — which covers three genuinely different
// states, and the difference is why this function returns no error.
//
// A branch with NO pull request is clean: on the internal mg track that is
// every branch, and there is no body for GitHub to read.
//
// A pull request whose body cannot be READ is INDETERMINATE, and this function
// deliberately fails SOFT — it logs and returns clean. That is a weaker stance
// than commitClosingRefs takes about unreadable history, and the asymmetry is
// the point: `git log` failing on a local clone means something is wrong with
// this branch, while `gh` failing means gh is missing, unauthenticated, offline
// or pointed at a non-GitHub remote — none of which is a property of the branch
// under judgement. Hard-failing there would stop every merge on the machine,
// including the entire internal track that has no PRs at all, and trade a rare
// wrong auto-close for a total halt. The residual is real and stated rather
// than hidden: while gh is broken, a "Resolves #N" in a PR body is unguarded,
// and the log line is the only place that says so.
func prBodyClosingRefs(wtDir, branch string) string {
	num, body, err := lookupPRBody(wtDir, branch)
	if err != nil {
		log.Printf("refinery: closing-ref PR-body check INDETERMINATE for branch %s: %v "+
			"— proceeding on the commit-message check alone; a closing keyword in the PR body is NOT guarded on this merge", branch, err)
		return ""
	}
	if num == 0 {
		return ""
	}
	findings := closingref.Check(body)
	if len(findings) == 0 {
		return ""
	}
	return closingref.Report(closingref.PullRequestBody,
		fmt.Sprintf("pull request #%d body", num), findings) + "\n"
}

// lookupPRBody returns the number and body of the GitHub PR whose head is
// branch. A branch with no PR is reported as (0, "", nil); everything else that
// goes wrong is an error for the caller to classify. Sibling of lookupPR, kept
// separate because the two run at opposite ends of the merge (this one before
// it, lookupPR after) and a shared call would have to be cached across a window
// in which the PR's state genuinely changes.
func lookupPRBody(wtDir, branch string) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), prLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", branch, "--json", "number,body")
	cmd.Dir = wtDir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.TrimSpace(string(ee.Stderr))
			// gh exits 1 with this message when the branch simply has no
			// PR — the normal state for internal mg-track branches.
			if strings.Contains(strings.ToLower(stderr), "no pull requests found") {
				return 0, "", nil
			}
			return 0, "", fmt.Errorf("gh pr view %s: %s: %w", branch, stderr, err)
		}
		return 0, "", fmt.Errorf("gh pr view %s: %w", branch, err)
	}
	var pr struct {
		Number int    `json:"number"`
		Body   string `json:"body"`
	}
	if jerr := json.Unmarshal(out, &pr); jerr != nil {
		return 0, "", fmt.Errorf("gh pr view %s: unreadable JSON: %w", branch, jerr)
	}
	if pr.Number == 0 {
		// gh answered with no number: output drift, not a clean bill of
		// health. Say so rather than reporting "no PR".
		return 0, "", fmt.Errorf("gh pr view %s: no PR number in %q", branch, strings.TrimSpace(string(out)))
	}
	return pr.Number, pr.Body, nil
}
