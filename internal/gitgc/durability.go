package gitgc

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// DurabilityVerdict answers ONE question about a local branch ref: would
// deleting it lose commits that exist nowhere else?
//
// It is a fact about the BRANCH. It is deliberately not derived from, and does
// not consult, the state of the work item the branch was created for — that
// separation is the whole point of mg-0a43. A ticket being archived says the
// work concluded; it says nothing whatever about where the commits are.
//
// The three verdicts, and the fact that "we could not ask" is one of them
// rather than being folded into `durable`, are lifted from the deploy drain's
// `durability_of` (scripts/pogo-self-deploy, gh#134 / mg-fd94). That predicate
// and this one answer the same question about the same objects, so they agree
// by construction rather than by coincidence; see BranchDurable for the one
// place they deliberately differ.
type DurabilityVerdict int

const (
	// DurabilityNotChecked is the zero value: no durability question was asked
	// about this branch. It is NOT a synonym for "unknown" — a branch kept
	// because its polecat is live was never asked, and reporting that as a
	// failed measurement would put it in the at-risk report alongside branches
	// git genuinely could not answer for.
	DurabilityNotChecked DurabilityVerdict = iota
	// DurabilityDurable: every commit on the branch exists somewhere the
	// deletion of this local ref cannot reach past.
	DurabilityDurable
	// DurabilityLocalOnly: the branch holds commits that exist ONLY on this
	// local ref. Deleting it orphans them.
	DurabilityLocalOnly
	// DurabilityUnknown: git could not answer. Treated exactly like
	// DurabilityLocalOnly by every caller, and reported as ITSELF — a question
	// we failed to ask is not an answer of "durable", and the subsystem this
	// predicate came from has a documented history of that collapse costing
	// files (mg-65b2, mg-0b77, mg-76e5).
	DurabilityUnknown
)

func (v DurabilityVerdict) String() string {
	switch v {
	case DurabilityDurable:
		return "durable"
	case DurabilityLocalOnly:
		return "local-only"
	case DurabilityUnknown:
		return "unknown"
	default:
		return "not-checked"
	}
}

// AtRisk reports whether deleting the ref might lose commits. Both local-only
// and unknown are at risk; only a positive `durable` clears a deletion.
func (v DurabilityVerdict) AtRisk() bool {
	return v == DurabilityLocalOnly || v == DurabilityUnknown
}

// MarshalJSON renders the verdict as its word rather than its ordinal. `pogo gc
// --json` prints a Result verbatim, and a bare `3` in that output is a number
// the reader has to come here to decode — for the one field that says whether a
// branch is holding the only copy of some commits.
func (v DurabilityVerdict) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(v.String())), nil
}

// UnmarshalJSON accepts what MarshalJSON produces, so a Result survives a
// round trip. An unrecognised word decodes to DurabilityUnknown rather than
// erroring — a verdict we cannot read is exactly the case that must not read as
// durable.
func (v *DurabilityVerdict) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return err
	}
	switch s {
	case "durable":
		*v = DurabilityDurable
	case "local-only":
		*v = DurabilityLocalOnly
	case "not-checked":
		*v = DurabilityNotChecked
	default:
		*v = DurabilityUnknown
	}
	return nil
}

// BranchDurable reports whether every commit on branch survives the deletion of
// that local ref, and the detail line an operator reads out of the GC log.
//
// target is the integration branch name (Options.TargetBranch, normally
// "main"). Test (1) below never mentions it, so a branch that was pushed is
// durable whatever it targets; the later tests consult it only to spare the
// rebase case a false at-risk verdict.
//
// # Why not BranchMerged (mg-1539/gh#134, and mayor's measurement on mg-0a43)
//
// Ancestry is the wrong instrument and it is wrong in a way that hides:
// THE REFINERY REBASES BEFORE MERGING (internal/refinery/merge.go), so a branch
// whose work landed perfectly is not an ancestor of main afterwards. Measured
// in /Users/daniel/dev/pogo: 146 polecat branches, 71 of them `--no-merged
// main`, and sampling those 71 found branches whose work is demonstrably on
// origin/main. So `--no-merged` is not an exposure count and must never be
// quoted as one. Ancestry fails CLOSED — it keeps branches that landed — which
// is why the done arm has never hurt anyone and why nobody noticed it was
// measuring the wrong thing.
//
// The question that actually answers this is "does ANY origin ref hold this
// head". It survives a rebase, it survives the branch being renamed, and it is
// a fact about the branch rather than an inference about the ticket.
//
// # The tests, in order, and why that order
//
//	(1) SOME ref under refs/remotes/origin/ contains the head. Not
//	    origin/<branch> specifically — a branch reachable under another name is
//	    durable without being merged, which is the whole of gh#134. Seated first
//	    because it is the test this predicate exists for, and because it NAMES
//	    the carrying ref, so a deletion can be audited rather than trusted.
//	(2) The head is already an ancestor of the integration ref. Subsumed by (3)
//	    — an ancestor's commits are all `-` in `git cherry` — and kept anyway
//	    because it is an order of magnitude cheaper and its detail says
//	    something (3) cannot: contained, not merely patch-equivalent.
//	(3) THE REBASE CASE. Compare patch ids, not SHAs. Reached only when both
//	    containment tests have already failed, because it is the only permissive
//	    test here: a genuine patch-id twin — every commit on the branch matching
//	    one already on the integration ref since the merge base — would clear a
//	    deletion. Without it a rebase-landed branch is kept forever, and the
//	    measured population of those is large.
//
// Anything else is `unknown`, including "no integration ref resolves". A branch
// whose head cannot even be resolved is unknown too, not durable.
//
// # Where this deliberately differs from the drain's durability_of
//
// The drain distinguishes `origin/<branch> holds it` from `some OTHER origin
// ref holds it`, and marks the first `awaiting the refinery`. That distinction
// is about whether the refinery still owes this worktree something, which is a
// question about a RUNNING polecat. Nothing here is running — the branch's
// polecat is gone and its ticket is concluded — so the two shapes collapse into
// one and the marker would be false for every caller of this function. The
// drain's comment at scripts/pogo-self-deploy already says so, naming mg-0a43.
//
// # Bound, stated rather than glossed
//
// Every origin ref read here is a local remote-tracking ref: it reflects what
// this clone last pushed or fetched, not what the server holds now. A SHA
// origin has since force-pushed away still has a stale tracking ref pointing at
// it and would be called durable. A force-push does not unpublish — the commits
// stay servable by SHA — so the residual is "harder to find", not "lost". This
// is the same bound the drain accepts, inherited deliberately rather than
// re-derived.
func BranchDurable(repo, branch, target string) (DurabilityVerdict, string) {
	head, err := resolveCommit(repo, branch)
	if err != nil {
		return DurabilityUnknown, fmt.Sprintf("cannot resolve %s to a commit: %v", branch, err)
	}

	// (1) Any origin ref.
	holder, err := originRefContaining(repo, head)
	if err != nil {
		return DurabilityUnknown, fmt.Sprintf(
			"cannot list the refs under refs/remotes/origin/ holding %s: %v", branch, err)
	}
	if holder != "" {
		return DurabilityDurable, fmt.Sprintf("%s holds every commit on %s", holder, branch)
	}

	// No origin ref holds it. Everything below needs something to compare
	// against, so say plainly when there is nothing rather than guessing.
	tname, tcommit, err := resolveIntegrationRef(repo, target)
	if err != nil {
		return DurabilityUnknown, fmt.Sprintf(
			"no ref under refs/remotes/origin/ holds %s, and no integration ref resolves for %q (%v) "+
				"— cannot tell whether anything on this branch is durable", branch, target, err)
	}

	// (2) Contained in the integration ref.
	merged, err := BranchMerged(repo, head, tcommit)
	if err != nil {
		return DurabilityUnknown, fmt.Sprintf("cannot compare %s against %s: %v", branch, tname, err)
	}
	if merged {
		return DurabilityDurable, fmt.Sprintf("%s already contains every commit on %s", tname, branch)
	}

	// (3) The rebase case.
	ahead, err := cherryAhead(repo, tcommit, head)
	if err != nil {
		return DurabilityUnknown, fmt.Sprintf(
			"git cherry could not compare %s against %s: %v", branch, tname, err)
	}
	if ahead == 0 {
		return DurabilityDurable, fmt.Sprintf(
			"every commit on %s has a patch-equivalent already in %s (rebased and landed)", branch, tname)
	}
	return DurabilityLocalOnly, fmt.Sprintf(
		"%d commit(s) on %s exist ONLY on this local ref — no ref under refs/remotes/origin/ holds them "+
			"and none has an equivalent in %s", ahead, branch, tname)
}

// resolveCommit turns a ref into the commit SHA it names.
//
// Callers pass the SHA onward rather than the name. `git cherry` accepts no
// pathspec, so it would not have tripped on a name today — this is a standing
// guard rather than a repair, and the thing it guards against is measured: any
// git command that accepts BOTH revisions and paths aborts with "ambiguous
// argument 'main'" in a repository that also contains a path called `main`, and
// the caller reads an empty stream as an answer. That fired during the
// investigation behind mg-0a43 (`git log --oneline main | grep <id>` returned
// nothing, three times, in a repo where the ids were present) and it fires
// silently. A 40-hex SHA cannot be ambiguous, so nobody rewriting a step here
// in terms of `git log` or `rev-list` can reintroduce it.
func resolveCommit(repo, rev string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", rev+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", rev, err)
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("rev-parse %s: no such commit", rev)
	}
	return sha, nil
}

// resolveIntegrationRef picks the ref a landed branch would be contained in,
// returning its name (for the report) and its commit (for the comparison).
//
// refs/remotes/origin/<target> is preferred over the local <target> because the
// remote-tracking ref is where the refinery's merges actually land; a local
// `main` that has not been fetched lags it, and comparing against a lagging ref
// reports a landed branch as holding unique commits. That direction is safe —
// it keeps a branch it could have deleted — but it is still a wrong answer, and
// it would be a permanent one.
func resolveIntegrationRef(repo, target string) (name, commit string, err error) {
	if target == "" {
		target = DefaultTargetBranch
	}
	for _, ref := range []string{"refs/remotes/origin/" + target, target} {
		if sha, rerr := resolveCommit(repo, ref); rerr == nil {
			return strings.TrimPrefix(ref, "refs/remotes/"), sha, nil
		}
	}
	return "", "", fmt.Errorf("neither refs/remotes/origin/%s nor %s resolves in %s", target, target, repo)
}

// originRefContaining returns the first ref under refs/remotes/origin/ that
// contains commit, short-form ("origin/polecat-27d4"), or "" when none does.
//
// refs/remotes/origin/HEAD is excluded because it is a symref at the default
// branch: it would answer for the integration branch under a name that says
// nothing about who pushed. THIS CHANGES THE NAME IN THE VERDICT, NEVER THE
// VERDICT — whatever origin/HEAD points at is itself a ref under
// refs/remotes/origin/ and answers in its own name.
//
// Ordering across refs is git's own (`for-each-ref` sorts by refname), so the
// named holder is stable across sweeps rather than incidental.
func originRefContaining(repo, commit string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "for-each-ref",
		"--contains", commit, "--format=%(refname)", "refs/remotes/origin/").Output()
	if err != nil {
		return "", fmt.Errorf("for-each-ref --contains %s: %w", commit, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		ref := strings.TrimSpace(line)
		if ref == "" || ref == "refs/remotes/origin/HEAD" {
			continue
		}
		return strings.TrimPrefix(ref, "refs/remotes/"), nil
	}
	return "", nil
}

// cherryAhead counts the commits on head that have NO patch-equivalent in
// upstream — `git cherry`'s `+` lines.
//
// The exit status is checked rather than the output alone because an EMPTY
// cherry output is ambiguous: it is what both "nothing ahead" and "git refused
// the question" produce, and collapsing that pair is the mistake mg-65b2
// records one subsystem over.
func cherryAhead(repo, upstream, head string) (int, error) {
	out, err := exec.Command("git", "-C", repo, "cherry", upstream, head).Output()
	if err != nil {
		return 0, fmt.Errorf("cherry %s %s: %w", upstream, head, err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "+") {
			n++
		}
	}
	return n, nil
}
