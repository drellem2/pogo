// Package strandedwork answers one question about a work item: does it have
// pushed work that nobody is going to merge?
//
// WHY IT EXISTS (mg-b468). Stopping a polecat releases its macguffin claim and
// returns the item to available/ (internal/agent/claimrelease.go). That path
// consults nothing about the polecat's BRANCH, so an item whose worker pushed
// finished commits before it wedged goes back into the pool describing itself as
// unstarted. Dispatch then hands it to a fresh worker, which starts from the
// target branch and re-derives work that is already sitting on a remote ref.
//
// On 2026-08-05 that happened twice in one day. mg-9a19's audit was complete and
// its refinery gate had PASSED — the merge failed only at stage=fetch on a
// transient ssh error — and the re-dispatch spent its life duplicating 1026
// lines. Five more items were one dispatch away from something worse; see
// DispositionPreRegistration.
//
// TWO DISPOSITIONS, BECAUSE THEY NEED OPPOSITE HANDLING. A stranded branch is
// not one situation:
//
//	RESUBMIT           the commits are ordinary finished work. Submit the branch
//	                   to the refinery. Do NOT dispatch a worker at it.
//	PRE-REGISTRATION   the branch carries commits that recorded predictions
//	                   BEFORE the analysis existed. A re-dispatch must branch
//	                   FROM that commit and must never amend it.
//
// Collapsing the two would be worse than not checking at all, because the second
// one fails silently: a worker that starts from the target writes its predictions
// after seeing the results, and the artifact it produces is INDISTINGUISHABLE
// from a valid one. The control the pre-registration exists to establish is gone
// and nothing downstream can tell.
//
// WHAT THIS PACKAGE DELIBERATELY DOES NOT KNOW. It has no notion of whether a
// polecat is running. That is not an omission — it is the finding doctor wrote
// into the ticket after missing three of the six affected items:
//
//	"a polecat is running" is NOT evidence that an item has no stranded pushed
//	work — it is the PRECONDITION for it, because the re-dispatch IS the
//	running polecat.
//
// A check that asks "why is the merge signal absent" accepts "still in flight"
// and stops looking. This package asks the other question — "does this item have
// pushed work its current dispatch is ignoring" — and there is no argument to it
// that a live worker could satisfy. Callers that know about running polecats must
// use that knowledge to ESCALATE a finding, never to suppress one.
package strandedwork

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// PreRegistrationPrefix is the commit-subject prefix that marks a
// pre-registration commit: predictions written down before the work that would
// confirm or refute them existed.
//
// It is a subject PREFIX rather than a trailer or a tag because the commit is
// made by a worker under time pressure, often as its first act, and the subject
// line is the one field it cannot forget to write. Matching is case-insensitive
// on the prefix and tolerant of leading whitespace; everything else about the
// subject is free text.
//
// ONE SPELLING, AND THIS CONSTANT IS WHERE THAT DECISION LIVES. All five
// pre-registration branches from the 2026-08-05 incident used exactly this
// lowercase-with-colon form — but that is five branches from one week and one
// author population, which is weak evidence for a convention and should not be
// cited as though it established one. Alternative spellings ("pre-registration:",
// "prereg:") are deliberately NOT accepted, and the reason is an asymmetry rather
// than confidence in the sample: a missed marker costs a resubmit verdict on a
// branch that needed the stronger one, while a false pre-registration verdict
// REFUSES a dispatch that should have proceeded — and false refusals are how a
// gate gets disarmed rather than obeyed. Widening buys a rarer catch at the price
// of a commoner false positive. A sixth branch spelling it differently is what
// should change that, and this line is where it would.
const PreRegistrationPrefix = "predictions:"

// Disposition is what a caller should DO about a branch. It is deliberately
// phrased as an action rather than a state ("resubmit", not "unmerged"), because
// the entire defect this package addresses was a correct state — the item is
// available — read as an action it does not license.
type Disposition string

const (
	// DispositionClean means every commit on the branch is already in the
	// target, by patch identity. Nothing is stranded. A re-dispatch is safe.
	DispositionClean Disposition = "clean"

	// DispositionResubmit means the branch carries finished work the target does
	// not have. Submit the branch to the refinery. Dispatching a worker at the
	// item duplicates work that already exists.
	DispositionResubmit Disposition = "resubmit"

	// DispositionPreRegistration means the branch carries an UNMERGED
	// pre-registration commit. A re-dispatch is not merely wasteful, it is
	// corrupting unless it branches from that commit and leaves it unamended.
	//
	// It outranks DispositionResubmit whenever both apply, and the precedence is
	// not a stylistic choice: resubmit advice followed against a
	// pre-registration branch loses nothing (the branch merges intact), while
	// pre-registration advice skipped in favour of resubmit advice loses the
	// control silently.
	DispositionPreRegistration Disposition = "pre_registration"
)

// Commit is one commit on a branch, as `git cherry -v` reports it.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// IsPreRegistration reports whether c records predictions made in advance.
func (c Commit) IsPreRegistration() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.Subject)), PreRegistrationPrefix)
}

// Finding is the verdict on one branch.
type Finding struct {
	// Repo is the git repository the branch was read from.
	Repo string `json:"repo"`
	// Branch is the short branch name (e.g. "polecat-9a19").
	Branch string `json:"branch"`
	// Ref is the ref the commits were actually read from — the remote-tracking
	// ref when one exists, otherwise the local head. Reported because "the work
	// is on origin" and "the work is only in a local worktree git-gc is about to
	// reap" are different emergencies.
	Ref string `json:"ref"`
	// Pushed is true when Ref was a remote-tracking ref.
	Pushed bool `json:"pushed"`
	// Target is the ref the branch was compared against.
	Target string `json:"target"`
	// Found is false when neither a remote-tracking ref nor a local head exists
	// for Branch. Disposition is DispositionClean in that case, and it means
	// "there is no branch", not "the branch is merged".
	Found bool `json:"found"`

	// Disposition is what to do. See the constants.
	Disposition Disposition `json:"disposition"`

	// Unmerged lists the commits the target does not have, oldest first.
	Unmerged []Commit `json:"unmerged,omitempty"`
	// Equivalent counts commits already present in the target under a different
	// SHA. The refinery merges by rebase, so a branch that landed cleanly has
	// every commit rewritten; counting those as stranded would fire this check
	// on every healthy branch in the repo. See Inspect for why `git cherry`.
	Equivalent int `json:"equivalent"`

	// PreRegistration is the OLDEST unmerged pre-registration commit, and it is
	// the ref a re-dispatch must branch from. Nil unless Disposition is
	// DispositionPreRegistration.
	PreRegistration *Commit `json:"pre_registration,omitempty"`

	// WorkItemID is the id recovered from the unmerged commit subjects, when one
	// is present (commit convention: a trailing "(mg-xxxx)"). Best-effort: it is
	// how a scan attributes an orphaned branch to an item, and it is empty for a
	// branch whose commits never named one.
	WorkItemID string `json:"work_item_id,omitempty"`
}

// Stranded reports whether the branch holds work the target does not.
func (f Finding) Stranded() bool {
	return f.Disposition == DispositionResubmit || f.Disposition == DispositionPreRegistration
}

// Summary renders the finding as one line an agent or a person can act on
// without reading this package. It always names the remedy, because a report
// that only names the problem is what the pre-registration case cannot survive.
func (f Finding) Summary() string {
	switch f.Disposition {
	case DispositionPreRegistration:
		return fmt.Sprintf(
			"%s has %d unmerged commit(s) on %s, and %s is a PRE-REGISTRATION commit (%q). "+
				"Do NOT dispatch a worker that branches from %s: it would write its predictions after "+
				"seeing the results, and the artifact would be indistinguishable from a valid one. "+
				"Either resubmit %s to the refinery, or dispatch FROM %s and leave that commit unamended",
			f.Branch, len(f.Unmerged), f.Target, shortSHA(f.PreRegistration.SHA), f.PreRegistration.Subject,
			f.Target, f.Branch, shortSHA(f.PreRegistration.SHA))
	case DispositionResubmit:
		return fmt.Sprintf(
			"%s has %d unmerged commit(s) on %s (%s). Resubmit the branch to the refinery "+
				"(`pogo refinery submit %s --repo=%s`); do NOT dispatch a worker at this item, it would "+
				"re-derive work that is already pushed",
			f.Branch, len(f.Unmerged), f.Target, shortSHA(f.Unmerged[0].SHA), f.Branch, f.Repo)
	default:
		if !f.Found {
			return fmt.Sprintf("no branch %s exists in %s", f.Branch, f.Repo)
		}
		return fmt.Sprintf("%s has nothing the target %s does not already have", f.Branch, f.Target)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// workItemRe matches the trailing work-item id in a commit subject, per the
// repo's convention: "fix(x): thing (mg-b468)".
var workItemRe = regexp.MustCompile(`\(((?:mg|gh)-[A-Za-z0-9]{3,})\)`)

// Inspect classifies one branch in repo against target.
//
// target may be empty, in which case the repository's default branch is
// resolved (see ResolveTarget). A target that cannot be resolved is an ERROR and
// never a clean verdict: "the comparison could not be made" and "there is
// nothing stranded" are different facts, and reporting the first as the second
// is the whole shape of the defect this package exists to close.
//
// WHY `git cherry` AND NOT `rev-list target..branch`. The refinery merges by
// rebasing the branch onto the target and fast-forwarding, so a branch that
// merged successfully has every one of its commits present upstream under a
// DIFFERENT sha. `rev-list target..branch` counts all of them as absent, which
// would report every healthy merged branch in the repo as stranded work. A
// detector that fires on healthy input is worse than no detector: it teaches its
// readers to skip the line, and this is the line the real stranding surfaces on.
// `git cherry` compares by patch id, so a rebase-rewritten commit reports as
// present.
func Inspect(repo, branch, target string) (Finding, error) {
	f := Finding{Repo: repo, Branch: branch, Disposition: DispositionClean}

	targetRef, err := ResolveTarget(repo, target)
	if err != nil {
		return f, err
	}
	f.Target = targetRef

	ref, pushed, found, err := resolveBranchRef(repo, branch)
	if err != nil {
		return f, err
	}
	if !found {
		return f, nil
	}
	f.Ref, f.Pushed, f.Found = ref, pushed, true

	unmerged, equivalent, err := cherry(repo, targetRef, ref)
	if err != nil {
		return f, err
	}
	f.Unmerged, f.Equivalent = unmerged, equivalent
	if len(unmerged) == 0 {
		return f, nil
	}

	f.Disposition = DispositionResubmit
	f.WorkItemID = workItemID(unmerged)

	// Only an UNMERGED pre-registration commit forces the pre-registration
	// disposition. One that already landed on the target is safe: a worker
	// branching from the target inherits it, cannot amend it, and the control
	// stands. The distinction matters — a branch that merged its predictions and
	// then pushed follow-up work is an ordinary resubmit.
	for i := range unmerged {
		if unmerged[i].IsPreRegistration() {
			c := unmerged[i]
			f.PreRegistration = &c
			f.Disposition = DispositionPreRegistration
			break
		}
	}
	return f, nil
}

// FetchTimeout bounds the best-effort refresh in Fetch. It is short because
// Fetch sits in front of a dispatch decision: a check that can hang for minutes
// is a check somebody removes.
const FetchTimeout = 20 * time.Second

// Fetch refreshes repo's remote-tracking refs, best-effort, and reports whether
// the refs that follow are fresh.
//
// WHY BEST-EFFORT AND NEVER FATAL. Stale remote-tracking refs make this package
// wrong in both directions — a target behind origin reports merged work as
// stranded, and a branch pushed from elsewhere is invisible — so the refresh is
// worth doing. But the incident that produced this check was a NETWORK OUTAGE,
// and a check that refuses to answer when the network is down is a check that is
// off during precisely the window it exists for. So a failed fetch degrades to
// "answer from what is on disk, and say the answer may be stale" rather than to
// no answer at all.
//
// The bool is returned rather than swallowed because the caller has to be able
// to say which of the two it got. "Checked against fresh refs" and "checked
// against whatever this clone last saw" are different claims.
func Fetch(repo string) (fresh bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), FetchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--quiet", "origin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("fetch origin in %s: %s: %w", repo, strings.TrimSpace(string(out)), err)
	}
	return true, nil
}

// Scan classifies every polecat branch in repo — local heads and
// remote-tracking refs alike, deduplicated by branch name.
//
// It returns findings for stranded branches only. Clean branches are the
// overwhelming majority in any repo with history, and a report that listed them
// would bury the ones that matter.
//
// An individual branch whose inspection fails does not abort the scan: its error
// is collected and returned alongside the findings, so one unreadable ref cannot
// turn a scan of forty branches into a single error the caller renders as
// "nothing stranded".
func Scan(repo, target string) ([]Finding, []error) {
	branches, err := polecatBranches(repo)
	if err != nil {
		return nil, []error{err}
	}
	var findings []Finding
	var errs []error
	for _, b := range branches {
		f, err := Inspect(repo, b, target)
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect %s: %w", b, err))
			continue
		}
		if f.Stranded() {
			findings = append(findings, f)
		}
	}
	return findings, errs
}

// BranchPrefix is the prefix every polecat branch carries. It duplicates
// gitgc.BranchPrefix by value rather than by import: this package is a leaf that
// internal/agent depends on, and internal/agent already imports internal/gitgc,
// so importing it back would close a cycle. The frozen-identifier test keeps the
// two literals equal.
const BranchPrefix = "polecat-"

// polecatBranches lists every polecat branch name in repo, from both local heads
// and origin's remote-tracking refs, sorted and deduplicated.
//
// BOTH, and not just origin. A polecat that committed but never pushed has work
// that exists only in its worktree's branch — and the worktree is exactly what
// git-gc reaps once the agent is gone, so local-only work is the more urgent of
// the two, not the lesser one.
func polecatBranches(repo string) ([]string, error) {
	out, err := git(repo, "for-each-ref", "--format=%(refname)",
		"refs/heads/"+BranchPrefix+"*", "refs/remotes/origin/"+BranchPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("list polecat branches in %s: %w", repo, err)
	}
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "refs/heads/"):
			line = strings.TrimPrefix(line, "refs/heads/")
		case strings.HasPrefix(line, "refs/remotes/origin/"):
			line = strings.TrimPrefix(line, "refs/remotes/origin/")
		default:
			continue
		}
		// origin/HEAD resolves into this namespace on some clones; it is a
		// symref, not a branch.
		if line == "HEAD" || seen[line] {
			continue
		}
		seen[line] = true
		names = append(names, line)
	}
	return names, nil
}

// resolveBranchRef picks the ref to read a branch's commits from, preferring the
// pushed copy.
//
// The remote-tracking ref wins when both exist because it is the copy that
// survives the worktree being reaped, and it is what a resubmit would actually
// merge. The local head is the fallback rather than the primary for the same
// reason — but it is a fallback that must exist, since a polecat that never got
// as far as pushing still did work worth not throwing away.
func resolveBranchRef(repo, branch string) (ref string, pushed, found bool, err error) {
	for _, candidate := range []struct {
		ref    string
		pushed bool
	}{
		{"refs/remotes/origin/" + branch, true},
		{"refs/heads/" + branch, false},
	} {
		ok, verr := refExists(repo, candidate.ref)
		if verr != nil {
			return "", false, false, verr
		}
		if ok {
			return candidate.ref, candidate.pushed, true, nil
		}
	}
	return "", false, false, nil
}

// ResolveTarget resolves the ref a branch should be compared against.
//
// A named target is looked for on origin first and locally second, matching
// resolveBranchRef: the question is whether the work reached the SHARED history,
// and a local main that is behind origin would report merged work as stranded.
//
// An empty target asks the repository: origin/HEAD if it is set, then the
// conventional names. Failure to resolve is an error — see Inspect.
func ResolveTarget(repo, target string) (string, error) {
	if target != "" {
		for _, ref := range []string{"refs/remotes/origin/" + target, "refs/heads/" + target} {
			ok, err := refExists(repo, ref)
			if err != nil {
				return "", err
			}
			if ok {
				return ref, nil
			}
		}
		return "", fmt.Errorf("target %q resolves to no ref in %s (tried refs/remotes/origin/%s and refs/heads/%s)", target, repo, target, target)
	}

	if head, err := git(repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if head = strings.TrimSpace(head); head != "" {
			return head, nil
		}
	}
	for _, name := range []string{"main", "master"} {
		for _, ref := range []string{"refs/remotes/origin/" + name, "refs/heads/" + name} {
			ok, err := refExists(repo, ref)
			if err != nil {
				return "", err
			}
			if ok {
				return ref, nil
			}
		}
	}
	return "", fmt.Errorf("no default branch in %s: origin/HEAD is unset and neither main nor master exists", repo)
}

// cherry returns the commits on branchRef that targetRef does not have (oldest
// first) and the count of those it already has under a different sha.
//
// `git cherry -v <upstream> <head>` prints one line per commit on head:
//
//   - <sha> <subject>   no patch-equivalent commit upstream
//   - <sha> <subject>   an equivalent commit is already upstream
func cherry(repo, targetRef, branchRef string) ([]Commit, int, error) {
	out, err := git(repo, "cherry", "-v", targetRef, branchRef)
	if err != nil {
		return nil, 0, fmt.Errorf("git cherry %s %s in %s: %w", targetRef, branchRef, repo, err)
	}
	var unmerged []Commit
	equivalent := 0
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		mark, rest := line[0], strings.TrimSpace(line[1:])
		sha, subject, _ := strings.Cut(rest, " ")
		switch mark {
		case '+':
			unmerged = append(unmerged, Commit{SHA: sha, Subject: strings.TrimSpace(subject)})
		case '-':
			equivalent++
		}
	}
	return unmerged, equivalent, nil
}

// workItemID recovers the work item a branch's unmerged commits name, if any.
// The NEWEST mention wins: a branch that was rebased onto another item's work
// carries older ids that are not its own.
func workItemID(commits []Commit) string {
	for i := len(commits) - 1; i >= 0; i-- {
		if m := workItemRe.FindStringSubmatch(commits[i].Subject); m != nil {
			return m[1]
		}
	}
	return ""
}

// refExists reports whether ref names a commit in repo.
//
// `git rev-parse --verify --quiet` exits 1 for an absent ref, which exec reports
// as an error indistinguishable from "git is broken". show-ref is used instead
// precisely because its contract is narrower: an exit status of 1 means "no such
// ref" and nothing else, so a genuinely broken repository still surfaces as an
// error rather than as a clean absence.
func refExists(repo, ref string) (bool, error) {
	cmd := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", ref)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("show-ref %s in %s: %w", ref, repo, err)
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(exitErr.Stderr)), err)
		}
		return "", err
	}
	return string(out), nil
}
