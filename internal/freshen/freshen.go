// Package freshen fast-forwards a long-lived checkout to its upstream, or
// says loudly why it would not.
//
// WHY THIS EXISTS. Agent workspaces such as ~/.pogo/agents/<name>/repo are
// long-lived linked worktrees that no refinery merge ever touches. The
// refinery fast-forwards the *submitting* checkout after a merge
// (internal/refinery/fastforward.go, gh #30), and freshly-spawned polecat
// worktrees are branched from current origin/main — so both of those stay
// current for free. The agent workspaces sit outside both paths and rot
// silently: one was found 129 commits / ~2 months behind main, by accident,
// during unrelated work (mg-d5fc).
//
// WHY NOT A STALENESS MONITOR. The reflex fix is a standing check that alerts
// when a checkout falls N commits behind. That is a guard that decays: it
// watches a number as a proxy for a question you can ask directly at the
// moment it matters, it needs a threshold nobody can justify, and it needs
// someone to keep watching it forever. This package instead answers the
// question at a lifecycle event nobody has to remember — see the call site in
// internal/agent (StartCrewAgent), which runs it before the agent's harness
// process exists.
//
// EXISTENCE IS NOT IDENTITY. Staleness is decided by comparing commit OIDs
// (rev-list HEAD..FETCH_HEAD), never by whether a path or a ref is present. A
// sweep that verified stale content with `git cat-file -e origin/main:<path>`
// once reported a clean 83/83 while one of the 83 blobs actually differed —
// presence cannot detect an unpublished or superseded file. Every verdict here
// is OID-derived.
//
// FRESHNESS OF A REF IS NOT FRESHNESS OF A CHECKOUT. In the incident that
// prompted this, `main` as a *local ref* was itself 129 behind origin/main,
// because the worktree holding `main` was the stale one — so anyone resolving
// `main` rather than `origin/main` read two-month-old content from *any*
// worktree in that repo. This package therefore fetches first and compares
// against FETCH_HEAD, never against a possibly-stale local remote-tracking
// ref, and fast-forwarding the branch repairs both at once.
package freshen

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Status is the verdict for one checkout. Exactly one is returned per call.
type Status string

const (
	// StatusUpdated: the checkout was behind and was fast-forwarded.
	StatusUpdated Status = "updated"
	// StatusAlreadyCurrent: HEAD already contained the upstream tip.
	StatusAlreadyCurrent Status = "already_current"
	// StatusDeclinedDirty: behind, but a tracked file is modified or staged.
	// Never clobbered — see the package doc's hard constraint.
	StatusDeclinedDirty Status = "declined_dirty"
	// StatusDeclinedDetached: HEAD is detached, so there is no branch to
	// advance and no upstream to advance it to.
	StatusDeclinedDetached Status = "declined_detached"
	// StatusDeclinedNoUpstream: the branch tracks nothing, so "behind" has no
	// referent. Reported rather than guessed at — a checkout parked on a local
	// branch is a deliberate state, not a fault.
	StatusDeclinedNoUpstream Status = "declined_no_upstream"
	// StatusDeclinedDiverged: behind AND ahead — a fast-forward is not
	// possible and a merge or rebase is a human's decision.
	StatusDeclinedDiverged Status = "declined_diverged"
	// StatusSkipped: not a working checkout (missing dir, bare repo, not a
	// repo at all). Not an anomaly: most agents have no repo/ at all.
	StatusSkipped Status = "skipped"
	// StatusFailed: git itself failed, so freshness is UNKNOWN. Deliberately
	// distinct from AlreadyCurrent — a check that cannot run must not read as
	// a clean bill of health.
	StatusFailed Status = "failed"
)

// Result is the outcome of one Checkout call.
type Result struct {
	Path   string `json:"path"`
	Status Status `json:"status"`
	Branch string `json:"branch,omitempty"`
	// Upstream is the tracking ref, e.g. "origin/main".
	Upstream string `json:"upstream,omitempty"`
	// Behind is how many commits upstream had that HEAD did not, measured
	// against the just-fetched tip. -1 means "not determined" (detached, no
	// upstream, or the fetch failed) — distinct from 0, which is a positive
	// finding that the checkout is current.
	Behind int `json:"behind"`
	// Ahead is commits HEAD had that upstream did not. -1 when not determined.
	Ahead int `json:"ahead"`
	// From and To are short OIDs; To is set only on StatusUpdated.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Detail carries the git error or the reason text for a decline.
	Detail string `json:"detail,omitempty"`
}

// Stale reports whether this checkout is known to be behind and was NOT
// brought current — i.e. the exact condition the caller must be loud about.
// A StatusFailed result is not Stale (freshness is unknown, not known-bad);
// callers surface that separately rather than crying wolf.
func (r Result) Stale() bool {
	return r.Behind > 0 && r.Status != StatusUpdated && r.Status != StatusAlreadyCurrent
}

// Declined reports whether freshen could have acted but deliberately did not.
func (r Result) Declined() bool {
	return strings.HasPrefix(string(r.Status), "declined_")
}

// String renders a one-line log-ready summary.
func (r Result) String() string {
	switch r.Status {
	case StatusUpdated:
		return fmt.Sprintf("%s: fast-forwarded %s %d commit(s) to %s (%s..%s)",
			r.Path, r.Branch, r.Behind, r.Upstream, r.From, r.To)
	case StatusAlreadyCurrent:
		return fmt.Sprintf("%s: %s already current with %s", r.Path, r.Branch, r.Upstream)
	case StatusSkipped:
		return fmt.Sprintf("%s: skipped (%s)", r.Path, r.Detail)
	case StatusFailed:
		return fmt.Sprintf("%s: FRESHNESS UNKNOWN — %s", r.Path, r.Detail)
	default:
		return fmt.Sprintf("%s: DECLINED (%s) — %s behind %s: %s",
			r.Path, r.Status, behindText(r.Behind), r.Upstream, r.Detail)
	}
}

func behindText(n int) string {
	if n < 0 {
		return "an undetermined number of commits"
	}
	return fmt.Sprintf("%d commit(s)", n)
}

// Checkout fetches the upstream of repoPath's current branch and
// fast-forwards onto it when — and only when — that is unambiguously safe.
//
// It never merges, rebases, resets, or stashes, and it never touches a tree
// with staged or unstaged changes to tracked files. Untracked files do not
// block: `merge --ff-only` aborts rather than overwriting one, so git itself
// enforces that boundary (same posture as internal/refinery/fastforward.go).
//
// It is total — every path returns a Result, never an error — because the
// caller (agent start) must not fail on a workspace problem. The Result
// carries the bad news instead, and a StatusFailed is explicitly NOT a clean
// verdict.
func Checkout(repoPath string) Result {
	res := Result{Path: repoPath, Behind: -1, Ahead: -1}

	if fi, err := os.Stat(repoPath); err != nil || !fi.IsDir() {
		res.Status = StatusSkipped
		res.Detail = "no such directory"
		return res
	}

	// Must be a working checkout. Bare repos have no tree to refresh.
	if out, err := git(repoPath, "rev-parse", "--is-inside-work-tree"); err != nil || out != "true" {
		res.Status = StatusSkipped
		res.Detail = "not a working git checkout"
		return res
	}

	// A detached HEAD has no branch to advance. Report it rather than
	// guessing a target: a detached checkout is usually deliberate.
	branch, err := git(repoPath, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || branch == "" {
		res.Status = StatusDeclinedDetached
		res.Detail = "HEAD is detached; no branch to fast-forward"
		return res
	}
	res.Branch = branch

	// Ask the branch what it tracks rather than assuming origin/main. A
	// checkout deliberately parked on a feature branch must be measured
	// against ITS upstream, not against main — otherwise every parked
	// checkout reads as catastrophically stale and the signal is worthless.
	upstream, err := git(repoPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil || upstream == "" {
		res.Status = StatusDeclinedNoUpstream
		res.Detail = fmt.Sprintf("branch %q tracks no upstream; freshness has no referent", branch)
		return res
	}
	res.Upstream = upstream

	remote, remoteBranch, ok := splitUpstream(upstream)
	if !ok {
		res.Status = StatusDeclinedNoUpstream
		res.Detail = fmt.Sprintf("upstream %q is not in <remote>/<branch> form", upstream)
		return res
	}

	// Fetch BEFORE measuring. Measuring against the local remote-tracking ref
	// would ask a stale question: that ref is exactly as old as the last
	// fetch, which in the incident this package exists for was two months.
	if out, err := git(repoPath, "fetch", remote, remoteBranch); err != nil {
		res.Status = StatusFailed
		res.Detail = fmt.Sprintf("fetch %s %s failed: %s", remote, remoteBranch, firstLine(out))
		return res
	}

	// OID-derived verdict. Not "does the ref exist", not "does the file
	// exist" — how many commits separate the two tips, in both directions.
	behind, ahead, err := countDivergence(repoPath)
	if err != nil {
		res.Status = StatusFailed
		res.Detail = fmt.Sprintf("could not compare HEAD to FETCH_HEAD: %v", err)
		return res
	}
	res.Behind, res.Ahead = behind, ahead
	res.From, _ = git(repoPath, "rev-parse", "--short", "HEAD")

	if behind == 0 {
		res.Status = StatusAlreadyCurrent
		return res
	}

	// Behind AND ahead: no fast-forward exists. Reconciling is a human's call.
	if ahead > 0 {
		res.Status = StatusDeclinedDiverged
		res.Detail = fmt.Sprintf("also %d commit(s) ahead; fast-forward impossible", ahead)
		return res
	}

	// THE HARD CONSTRAINT. One of the two checkouts that prompted this ticket
	// held 83 staged adds on an abandoned branch. An automatic refresh that
	// clobbers a dirty tree converts a silent staleness bug into silent data
	// loss, which is strictly worse. Detect and decline, loudly.
	status, err := git(repoPath, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		res.Status = StatusFailed
		res.Detail = fmt.Sprintf("status failed: %s", firstLine(status))
		return res
	}
	if status != "" {
		res.Status = StatusDeclinedDirty
		res.Detail = fmt.Sprintf("%d tracked path(s) modified or staged; commit or stash, then 'git pull'",
			len(strings.Split(status, "\n")))
		return res
	}

	if out, err := git(repoPath, "merge", "--ff-only", "FETCH_HEAD"); err != nil {
		res.Status = StatusFailed
		res.Detail = fmt.Sprintf("ff-only merge failed: %s", firstLine(out))
		return res
	}

	res.Status = StatusUpdated
	res.To, _ = git(repoPath, "rev-parse", "--short", "HEAD")
	return res
}

// countDivergence returns (behind, ahead) between HEAD and FETCH_HEAD using a
// single rev-list, which reports both counts from one walk.
func countDivergence(repoPath string) (behind, ahead int, err error) {
	out, err := git(repoPath, "rev-list", "--left-right", "--count", "HEAD...FETCH_HEAD")
	if err != nil {
		return 0, 0, fmt.Errorf("%s", firstLine(out))
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	// --left-right with HEAD...FETCH_HEAD puts left (HEAD-only = ahead) first.
	ahead, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("unparseable ahead count %q", fields[0])
	}
	behind, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("unparseable behind count %q", fields[1])
	}
	return behind, ahead, nil
}

func splitUpstream(upstream string) (remote, branch string, ok bool) {
	i := strings.Index(upstream, "/")
	if i <= 0 || i == len(upstream)-1 {
		return "", "", false
	}
	return upstream[:i], upstream[i+1:], true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// CommandTimeout bounds every individual git invocation.
//
// This is load-bearing on the agent-start path: Checkout performs a network
// fetch, and an unreachable or throttled remote must not hold an agent's start
// open indefinitely. On timeout the verdict is StatusFailed — freshness
// UNKNOWN — which is the honest answer and never reads as "current".
var CommandTimeout = 60 * time.Second

// git runs one git command in dir and returns its trimmed combined output.
//
// GIT_TERMINAL_PROMPT=0 is load-bearing: pogod runs under launchd with no TTY,
// and a credential prompt on fetch would otherwise hang the agent start that
// called us. Same reasoning as internal/refinery's runner.
func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), CommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Sprintf("git %s timed out after %s", args[0], CommandTimeout), ctx.Err()
	}
	return strings.TrimSpace(string(out)), err
}
