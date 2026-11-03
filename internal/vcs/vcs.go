// Package vcs is the seam between pogo and the version-control system a repo
// is kept in. The design is docs/design/enth-vcs.md §3.
//
// The boundary is the QUESTION pogo asks ("land this MR", "what is the default
// branch", "tag this commit"), not the git command that answers it today: many
// git-side steps have no counterpart on the enth side, so a command-level shim
// would carry dead surface.
//
// # What exists so far (enth-vcs slice 1, mg-a18a3)
//
// The four groups are declared here as the design sketches them. Lander and
// Refs have an implementation, the git backend in internal/vcs/gitvcs, and
// only the refinery calls it.
//
// # Slice 2 (mg-cc7ae)
//
// Workspaces has its signatures fixed and a git implementation
// (gitvcs.Workspaces), called by internal/agent's polecat spawn, its rollback
// and leftover-branch reclaim, the injected-persona exclude, and the crew
// workspace refresh.
//
// # Slice 3 (mg-d72cb)
//
// Work has its signatures fixed and a git implementation, gitvcs.Work.
// internal/strandedwork and internal/gitgc read branches, cherry-judge them,
// and read commit bodies, paths and blobs through it.
//
// # Selection (slice 4, mg-160e8)
//
// A repo's backend is named on its project record in projects.json (§2): a
// `vcs` key, absent meaning git, and for enth an `enth` object (EnthRepo).
// Selecting enth today gives internal/vcs/enthvcs, which answers
// ErrUnsupported to everything until slices 5-7.
//
// # Slice 3b (mg-afad2)
//
// Workspaces.Remove, Dirty, Describe and Root, and Work.Fetch, have git
// implementations. gitgc removes worktrees and judges them dirty through
// them, `pogo check-stranded` names a polecat worktree's repo and branch with
// Describe, `pogo worktree-dirty` resolves its root with Root, and
// strandedwork refreshes remote refs with Fetch.
//
// # Slice 5 (mg-cc5cd)
//
// enthvcs implements Workspaces: a polecat on an enth repo gets an
// `enth --remote checkout` instead of a git worktree, and its tree is judged
// (Dirty via `enth status`), described, refreshed and removed through it.
// Callers pick the backend by repo at spawn and by the tree's marker
// (enthvcs.Detect) after that. EnthRepo.WorkspacePaths narrows the checkout.
//
// # Slice 6 (mg-30da0)
//
// enthvcs implements Work: the trailer listing (Pogo-Item / Pogo-Worker),
// Stacks and AmbiguousWorkError, State from land records (E3), and the
// trailer-less base signal. Submission carries Item, Worker and Changes for
// enth; the git backend answers exactly as before and leaves them empty.
//
// # Slice 7 (mg-1fe2b)
//
// enthvcs implements Lander: Prepare is the dry run plus a checkout of its
// result N, Land is the land bound to the dry run's T and N under a request
// id. MR carries Changes and RequestID, Prepared carries Tip, New and the
// plan, Landed carries enth's land line as a LandRecord. git leaves every one
// of them empty and does not read them.
//
// # Slice 9 (mg-2e9fc)
//
// The GitHub boundary (§11): a PR-flow spawn on an enth repo is refused, the
// stranded-work open-PR probe is replaced by the refinery queue there, and the
// closing-ref check for enth Changes reads their whole messages
// (enthvcs.Backend.FullMessages) and no PR body. `pogo doctor --check` checks
// for enth when a project is recorded as enth.
package vcs

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// Kind names a backend. git is the zero value's meaning: a repo with no
// recorded kind is a git repo.
type Kind string

const (
	Git  Kind = "git"
	Enth Kind = "enth"
)

// ParseKind reads a user-supplied backend name. The empty string is git.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case "", Git:
		return Git, nil
	case Enth:
		return Enth, nil
	}
	return "", fmt.Errorf("vcs: unknown backend %q (want %q or %q)", s, Git, Enth)
}

// DefaultEnthTrunk is the trunk ref an enth record gets when none is given.
const DefaultEnthTrunk = "refs/heads/main"

// EnthRepo is the enth half of a project record (§2): where enthd is, which
// repo on it, and the ref work lands on. It is per-host configuration, which
// is why it lives on the project record and not in a committed file.
type EnthRepo struct {
	Remote string `json:"remote"`
	Repo   string `json:"repo"`
	Trunk  string `json:"trunk,omitempty"`
	// WorkspacePaths narrows every workspace pogo checks out of this repo to
	// these repo-relative paths (§4, "the escape hatch"). Empty, the default,
	// is the whole repo: an edit outside a narrowed selection is refused by
	// enth (outside_selection) and the worker must widen it with
	// `enth checkout --add PATH DIR`, so narrowing is for a repo too large to
	// check out whole and for nothing else.
	WorkspacePaths []string `json:"workspace_paths,omitempty"`
}

// Equal reports whether two enth settings are the same. EnthRepo holds a
// slice, so == does not compile; a nil and an empty WorkspacePaths are equal.
func (e EnthRepo) Equal(o EnthRepo) bool {
	if e.Remote != o.Remote || e.Repo != o.Repo || e.Trunk != o.Trunk || len(e.WorkspacePaths) != len(o.WorkspacePaths) {
		return false
	}
	for i := range e.WorkspacePaths {
		if e.WorkspacePaths[i] != o.WorkspacePaths[i] {
			return false
		}
	}
	return true
}

// IsZero reports whether no enth setting is given at all.
func (e EnthRepo) IsZero() bool { return e.Equal(EnthRepo{}) }

// normalizeWorkspacePaths validates the narrowing list: each entry must be a
// clean, repo-relative path inside the repo. Duplicates are dropped, order is
// kept, and an empty result is nil (the whole repo, stored as no key).
func normalizeWorkspacePaths(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		c := path.Clean(strings.ReplaceAll(p, "\\", "/"))
		if path.IsAbs(c) || c == "." || c == ".." || strings.HasPrefix(c, "../") {
			return nil, fmt.Errorf("vcs: workspace path %q must be a path inside the repo, relative to its root", p)
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, nil
}

// NormalizeSelection validates a (kind, enth) pair for a project record and
// returns it in its stored form. git is stored as the empty kind with no enth
// object, so a git record serializes exactly as it did before selection
// existed (§2, "byte-identical git"). enth requires Remote and Repo; an empty
// Trunk becomes DefaultEnthTrunk.
func NormalizeSelection(kind Kind, enth *EnthRepo) (Kind, *EnthRepo, error) {
	k, err := ParseKind(string(kind))
	if err != nil {
		return "", nil, err
	}
	if k == Git {
		if enth != nil && !enth.IsZero() {
			return "", nil, fmt.Errorf("vcs: enth settings given for a git repo")
		}
		return "", nil, nil
	}
	if enth == nil || enth.Remote == "" || enth.Repo == "" {
		return "", nil, fmt.Errorf("vcs: an enth repo needs both a remote and a repo name")
	}
	e := *enth
	if e.Trunk == "" {
		e.Trunk = DefaultEnthTrunk
	}
	paths, err := normalizeWorkspacePaths(e.WorkspacePaths)
	if err != nil {
		return "", nil, err
	}
	e.WorkspacePaths = paths
	return Enth, &e, nil
}

// Backend is selected per repo from the project record (slice 4).
type Backend interface {
	Kind() Kind
	Workspaces
	Work
	Lander
	Refs
}

// Repo identifies a repository to a backend. For git it is a directory: the
// source checkout, a bare repo, or the refinery's private clone, depending on
// the caller.
type Repo struct {
	Path string
}

// ---- Workspaces (slice 2) ----

// WorkspaceSpec says what workspace to create.
type WorkspaceSpec struct {
	// Repo is the source repo the workspace is cut from.
	Repo Repo
	// Dir is where the workspace is created. It must not exist.
	Dir string
	// Branch is the workspace's own line of work. For git it is the branch
	// `worktree add -b` creates (polecat-<name>); enth has no such thing.
	Branch string
	// Base is what the workspace starts from. "" means the repo's local head
	// (git: HEAD of Repo), which is what ResolveBase returns when no usable
	// remote exists.
	Base string
}

// Workspace is a created workspace.
type Workspace struct {
	Dir    string
	Repo   Repo
	Branch string
	Base   string
}

// Workspaces: W2–W9, P1, C1, C3, C5, C6, S10, X2, X4, O1, O3.
//
// Slice 2 (mg-cc7ae) fixed the signatures of what the polecat spawn path, the
// injected-persona exclude and the crew workspace refresh use. Slice 3b
// (mg-afad2) fixed Remove, Dirty, Describe and Root, for gitgc,
// check-stranded and worktree-dirty.
//
// The methods taking ctx do not all honour it: the git backend reproduces
// what pogo did before the seam existed, and only the base-resolution fetch
// was ever bounded. Each method's doc says what bounds it.
type Workspaces interface {
	// ResolveBase returns what a new workspace targeting `target` should be
	// based on, refreshing remote state first (W4). "" means "the repo's local
	// head"; it is never an error, because a spawn must not fail on it.
	ResolveBase(ctx context.Context, repo Repo, target string) string
	// BranchExists reports whether the workspace branch exists in repo (W5).
	BranchExists(ctx context.Context, repo Repo, branch string) bool
	// BranchSpent reports whether every commit on branch is reachable from
	// base, i.e. deleting it loses nothing. Any error answers false (W5).
	BranchSpent(ctx context.Context, repo Repo, branch, base string) bool
	// DeleteBranch deletes a workspace branch (W5, W6). A failure is a
	// *CommandError carrying the command's verbatim output.
	DeleteBranch(ctx context.Context, repo Repo, branch string) error
	// Create creates the workspace (W2, W3, W7). A failure is a
	// *CommandError carrying the command's verbatim output; the backend does
	// NOT roll back what a failed create left behind, because only the caller
	// knows which of it was there before.
	Create(ctx context.Context, s WorkspaceSpec) (Workspace, error)
	// Rollback undoes the backend registration of a workspace this process
	// just created (W6). It removes the directory too when the backend can,
	// but the caller must not rely on that: git refuses some trees, and the
	// caller is the one that knows the directory is its own to delete.
	Rollback(ctx context.Context, repo Repo, dir string) error
	// Contains reports, as a nil error, that branch contains base: the check
	// that an adopting workspace really started from what it adopted (W7).
	Contains(ctx context.Context, repo Repo, base, branch string) error
	// Exclude keeps relPath (relative to the workspace root dir) out of the
	// VCS's view of the workspace, under a group header line, idempotently
	// (W8). It never touches a tracked file.
	Exclude(ctx context.Context, dir, group, relPath string) error
	// Refresh brings a long-lived workspace up to date with its upstream, or
	// says why it did not (O1, R19). It is total: every outcome is a
	// RefreshResult, never an error, because its caller must not fail on it.
	Refresh(ctx context.Context, dir string) RefreshResult

	// Remove deletes the workspace at dir, which was cut from repo (C6, C3).
	// It takes repo for the reason Rollback does: git removes a linked
	// worktree through the repository that registered it. A failure is a
	// *CommandError; whether it is fatal is the caller's call.
	Remove(ctx context.Context, repo Repo, dir string) error
	// Dirty lists dir's uncommitted entries, excluding the files pogod itself
	// writes into every agent's tree (P1, R15). Empty means clean. A non-nil
	// error means dirtiness could not be determined, never that it is clean.
	Dirty(ctx context.Context, dir string) ([]string, error)
	// Describe names the repo dir belongs to and the branch it has checked
	// out, read from the tree itself (W9, S10, X4, C5). Branch is empty when
	// there is none (git: a detached HEAD). A tree that answers but whose repo
	// cannot be named is an *UnnamedRepoError.
	Describe(ctx context.Context, dir string) (Workspace, error)
	// Root resolves a path anywhere inside a workspace to the workspace's top
	// directory (P1: worktree-dirty runs from wherever the worker's shell is).
	Root(ctx context.Context, dir string) (string, error)
}

// RefreshStatus is the verdict of one Refresh. Exactly one is returned per
// call. The values are recorded in events and mail (workspace_freshen), so
// they must not be renamed.
type RefreshStatus string

const (
	// RefreshUpdated: the workspace was behind and was fast-forwarded.
	RefreshUpdated RefreshStatus = "updated"
	// RefreshAlreadyCurrent: HEAD already contained the upstream tip.
	RefreshAlreadyCurrent RefreshStatus = "already_current"
	// RefreshDeclinedDirty: behind, but a tracked file is modified or staged.
	// Never clobbered.
	RefreshDeclinedDirty RefreshStatus = "declined_dirty"
	// RefreshDeclinedDetached: HEAD is detached, so there is no branch to
	// advance and no upstream to advance it to.
	RefreshDeclinedDetached RefreshStatus = "declined_detached"
	// RefreshDeclinedNoUpstream: the branch tracks nothing AND no remote
	// counterpart could be identified, so "behind" has no referent. See
	// internal/freshen for why this is narrowed (mg-036f).
	RefreshDeclinedNoUpstream RefreshStatus = "declined_no_upstream"
	// RefreshDeclinedDiverged: behind AND ahead — a fast-forward is not
	// possible and a merge or rebase is a human's decision.
	RefreshDeclinedDiverged RefreshStatus = "declined_diverged"
	// RefreshSkipped: not a working checkout (missing dir, bare repo, not a
	// repo at all). Not an anomaly: most agents have no repo/ at all.
	RefreshSkipped RefreshStatus = "skipped"
	// RefreshFailed: the VCS itself failed, so freshness is UNKNOWN.
	// Deliberately distinct from AlreadyCurrent — a check that cannot run must
	// not read as a clean bill of health.
	RefreshFailed RefreshStatus = "failed"
)

// RefreshResult is the outcome of one Refresh. It is internal/freshen's
// Result, moved here unchanged when Refresh became a backend method
// (freshen.Result is an alias of it), so its JSON shape is what pogo has
// always emitted.
type RefreshResult struct {
	Path   string        `json:"path"`
	Status RefreshStatus `json:"status"`
	Branch string        `json:"branch,omitempty"`
	// Upstream is the ref freshness was measured against, e.g. "origin/main".
	Upstream string `json:"upstream,omitempty"`
	// UpstreamInferred reports that Upstream was derived from the branch's
	// name and the remote's refs rather than read from tracking config.
	//
	// This is never silent. An inferred referent is a judgement pogo made on
	// the operator's behalf, and every rendering of this result says so, so
	// that "brought current" can never be mistaken for evidence that someone
	// had configured what current meant.
	UpstreamInferred bool `json:"upstream_inferred,omitempty"`
	// Behind is how many commits upstream had that HEAD did not, measured
	// against the just-fetched tip. -1 means "not determined" (detached, no
	// upstream, or the fetch failed) — distinct from 0, which is a positive
	// finding that the checkout is current.
	Behind int `json:"behind"`
	// Ahead is commits HEAD had that upstream did not. -1 when not determined.
	Ahead int `json:"ahead"`
	// From and To are short OIDs; To is set only on RefreshUpdated.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Detail carries the VCS error or the reason text for a decline.
	Detail string `json:"detail,omitempty"`
}

// Stale reports whether this checkout is known to be behind and was NOT
// brought current — i.e. the exact condition the caller must be loud about.
// A RefreshFailed result is not Stale (freshness is unknown, not known-bad);
// callers surface that separately rather than crying wolf.
func (r RefreshResult) Stale() bool {
	return r.Behind > 0 && r.Status != RefreshUpdated && r.Status != RefreshAlreadyCurrent
}

// Declined reports whether the refresh could have acted but deliberately did not.
func (r RefreshResult) Declined() bool {
	return strings.HasPrefix(string(r.Status), "declined_")
}

// String renders a one-line log-ready summary.
func (r RefreshResult) String() string {
	switch r.Status {
	case RefreshUpdated:
		return fmt.Sprintf("%s: fast-forwarded %s %s to %s%s (%s..%s)",
			r.Path, r.Branch, behindText(r.Behind), r.Upstream, inferredNote(r), r.From, r.To)
	case RefreshAlreadyCurrent:
		return fmt.Sprintf("%s: %s already current with %s%s",
			r.Path, r.Branch, r.Upstream, inferredNote(r))
	case RefreshSkipped:
		return fmt.Sprintf("%s: skipped (%s)", r.Path, r.Detail)
	case RefreshFailed:
		return fmt.Sprintf("%s: FRESHNESS UNKNOWN — %s", r.Path, r.Detail)
	default:
		return fmt.Sprintf("%s: DECLINED (%s) — %s behind %s: %s",
			r.Path, r.Status, behindText(r.Behind), r.Upstream, r.Detail)
	}
}

// inferredNote marks a verdict measured against a referent pogo chose rather
// than one the operator configured. It is appended to every rendering that
// names an upstream, so an inferred "current" always carries the caveat that
// nobody said what current meant.
func inferredNote(r RefreshResult) string {
	if r.UpstreamInferred {
		return " (upstream inferred from branch name; none configured)"
	}
	return ""
}

func behindText(n int) string {
	if n < 0 {
		return "an undetermined number of commits"
	}
	return fmt.Sprintf("%d commit(s)", n)
}

// ---- Work (slice 3) ----

// The Work types below were fixed by slice 3 (mg-d72cb) to fit the questions
// that internal/strandedwork and internal/gitgc already ask of git. Slice 6
// (enth work) may widen them. It must not change what the git backend answers.

// WorkQuery selects submissions. An empty query is every worker's work.
type WorkQuery struct {
	// Item selects by work item: on enth, the Pogo-Item trailer. The git
	// backend has no item index (a git branch is named for its worker, not
	// its item), so it answers ErrUnsupported. Callers attribute by commit
	// subject themselves (strandedwork.ItemCandidates).
	Item string
	// Worker selects one worker's submission by agent name. For git that is
	// the branch polecat-<Worker>; for enth, the Pogo-Worker trailer.
	Worker string
}

// Submission names one worker's output, or specific revisions of it.
type Submission struct {
	// Item and Worker are the work item and the worker the submission belongs
	// to, as its Pogo-Item / Pogo-Worker trailers name them (enth). git leaves
	// both empty: its submission is named by Branch.
	Item   string
	Worker string
	// Changes is an enth submission's stack, bottom first: each Change's Base
	// is the one before it, and the last is the top (§5, "What a submission
	// is"). git leaves it empty.
	Changes []Change
	// Branch is the worker's branch name (git: polecat-<name>).
	Branch string
	// Ref is where the backend read the submission from. For git it is a full
	// refname (refs/heads/<Branch> or refs/remotes/origin/<Branch>). When it is
	// set, State reads Ref rather than Branch.
	Ref string
	// Revs names specific commits, for Messages and Diff.
	Revs []string
}

// The trailers that tie an enth Change to pogo's work (§5). They replace the
// polecat-<name> branch as the durable link between a worker and its output.
const (
	TrailerItem   = "Pogo-Item"
	TrailerWorker = "Pogo-Worker"
)

// Trailer is one key/value trailer of a Change's message.
type Trailer struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Change is one enth Change as the trailer listing reports it (enth's
// `enth changes`, mg-10079).
type Change struct {
	ID string `json:"id"`
	// Base is the first parent; "" for a root Change.
	Base string `json:"base,omitempty"`
	// Entry is the log entry that stored the Change.
	Entry       int    `json:"entry"`
	AuthorName  string `json:"author_name,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
	// Created is the committer's time (S6: the age of the work).
	Created  time.Time `json:"created"`
	Trailers []Trailer `json:"trailers,omitempty"`
	// Landed is the id a land record says this Change landed as; "" when no
	// land record names it. It is read from land records, never inferred
	// from absence (E3).
	Landed string `json:"landed,omitempty"`
}

// Trailer returns the value of the first trailer named key (ASCII
// case-insensitive, as enth and git match trailer keys), or "".
func (c Change) Trailer(key string) string {
	for _, t := range c.Trailers {
		if strings.EqualFold(t.Key, key) {
			return t.Value
		}
	}
	return ""
}

// Stacks orders a listing of Changes into stacks: chains in which each
// Change's Base is the one before it, bottom first. A Change whose Base is
// not in the listing starts a stack. A Change with two listed children forks,
// and each branch of the fork is a stack of its own (sharing the prefix), so a
// listing is ONE submission exactly when Stacks returns one stack. Order is
// the listing's order (enth lists oldest first), so the answer is stable.
func Stacks(cs []Change) [][]Change {
	byID := make(map[string]int, len(cs))
	for i, c := range cs {
		if _, dup := byID[c.ID]; !dup {
			byID[c.ID] = i
		}
	}
	children := map[string][]int{}
	var roots []int
	for i, c := range cs {
		if byID[c.ID] != i {
			continue // a duplicate line names the same Change
		}
		if _, ok := byID[c.Base]; ok && c.Base != "" && c.Base != c.ID {
			children[c.Base] = append(children[c.Base], i)
		} else {
			roots = append(roots, i)
		}
	}
	var out [][]Change
	var walk func(i int, prefix []Change)
	walk = func(i int, prefix []Change) {
		path := append(append([]Change(nil), prefix...), cs[i])
		kids := children[cs[i].ID]
		if len(kids) == 0 {
			out = append(out, path)
			return
		}
		for _, k := range kids {
			walk(k, path)
		}
	}
	for _, r := range roots {
		walk(r, nil)
	}
	return out
}

// ErrNoWork means a listing found no unlanded work for the query: nothing
// would be delivered (the enth form of mg-c184d's "carries no commits ahead
// of the target").
var ErrNoWork = errors.New("vcs: no unlanded work")

// AmbiguousWorkError is a listing that forms more than one stack (§5): two
// workers on one item both produced work, or one worker forked. pogo does
// not guess which to land; the error names every stack so a human chooses.
type AmbiguousWorkError struct {
	Item   string
	Worker string
	Stacks []Submission
}

func (e *AmbiguousWorkError) Error() string {
	var b strings.Builder
	who := e.Item
	if e.Worker != "" {
		who += " (worker " + e.Worker + ")"
	}
	fmt.Fprintf(&b, "vcs: the unlanded Changes of %s form %d stacks, not one: refusing to guess which is the submission", who, len(e.Stacks))
	for i, s := range e.Stacks {
		worker := s.Worker
		if worker == "" {
			worker = "(no " + TrailerWorker + " trailer)"
		}
		top := ""
		if n := len(s.Changes); n > 0 {
			top = s.Changes[n-1].ID
		}
		fmt.Fprintf(&b, "\n  stack %d: worker %s, %d Change(s), top %s", i+1, worker, len(s.Changes), top)
	}
	return b.String()
}

// Commit is one commit of a submission as State reports it.
type Commit struct {
	Rev     string
	Subject string
	// Summary is the commit's one-line form exactly as the backend printed
	// it, untrimmed inside (git: `<sha> <subject>` from `git cherry -v`).
	// gitgc reports this string, and rebuilding it from Rev and Subject would
	// collapse a subject's leading whitespace.
	Summary string
}

// WorkState is a submission judged against a target.
type WorkState struct {
	// Ahead lists the submission's commits with no equivalent on the target,
	// oldest first. For git these are `git cherry -v`'s "+" lines.
	Ahead []Commit
	// Equivalent counts the submission's commits the target already has under
	// a different identity (git: `git cherry`'s "-" lines).
	Equivalent int
	// Landed reports that nothing in the submission is missing from the target.
	Landed bool
}

// Message is one commit's message body (git: %b, without the subject).
type Message struct {
	Rev  string
	Body string
}

// Diff is what a submission's revisions touched.
type Diff struct {
	Paths []string
}

// Work finds and judges a worker's output: W1, W5, P2, P5, R1, R9, R12, S2–S9, C4.
//
// The git backend returns the runner's error UNWRAPPED, so the caller wraps it
// in its own words. That is how strandedwork and gitgc kept their error texts
// byte for byte when they moved onto this interface.
type Work interface {
	// Find lists submissions, in the backend's stable order. git lists both the
	// local head and origin's copy of a branch, so a Branch can appear twice
	// with different Refs.
	Find(ctx context.Context, repo Repo, q WorkQuery) ([]Submission, error)
	// State judges the submission against target, which for git is any
	// revision (a full ref or a SHA).
	State(ctx context.Context, repo Repo, s Submission, target string) (WorkState, error)
	// Messages returns the bodies of s.Revs. On error it also returns the
	// messages it read before the failure.
	Messages(ctx context.Context, repo Repo, s Submission) ([]Message, error)
	// Diff returns the paths s.Revs touched.
	Diff(ctx context.Context, repo Repo, s Submission) (Diff, error)
	// Cat returns path's content at rev.
	Cat(ctx context.Context, repo Repo, rev, path string) ([]byte, error)
	// Fetch refreshes what Find and State read from the remote, bounded by
	// ctx (S2). git: fetch origin. A failure is a *CommandError, and callers
	// treat it as "answer from what is on disk", not as no answer.
	Fetch(ctx context.Context, repo Repo) error
}

// ---- Lander (slice 1) ----

// Step names one step of a backend's land pipeline. The values are the git
// backend's step names, and they are the names the refinery already records in
// its failure records (AttemptFailure.Step), so they must not be renamed.
type Step string

const (
	StepFetch          Step = "fetch"
	StepCheckoutBranch Step = "checkout-branch"
	StepRebase         Step = "rebase"
	StepFetchTarget    Step = "fetch-target"
	StepResetTarget    Step = "reset-target"
	StepMerge          Step = "merge-ff-only"
	StepPush           Step = "push"
	StepBranchDelete   Step = "branch-delete"
)

// LandHooks let the caller act between a backend's steps without knowing what
// they are. Both are optional.
//
// The refinery needs them because its pipeline interleaves its own work with
// the VCS's: it discards gate-written files after the fetch and before the
// checkout, and it must inspect a dirty tree BEFORE the backend cleans up a
// failed rebase, because the cleanup erases the evidence (mg-393f).
type LandHooks struct {
	// BeforeStep runs immediately before each step.
	BeforeStep func(Step)
	// StepFailed runs when a step fails, before the backend cleans up after it.
	// output is the step's combined output. A non-nil return is carried on the
	// resulting StepError as Override.
	StepFailed func(step Step, output string) error
}

// MR is one merge request as a backend sees it.
type MR struct {
	ID string
	// Repo is the repo the MR was submitted against.
	Repo Repo
	// Dir is the backend's working directory for this MR. For git it is the
	// refinery's private clone of Repo.
	Dir    string
	Branch string
	Target string
	Hooks  LandHooks
	// Changes is an enth MR's submitted stack, bottom first (§6.1: the
	// C1…Cn of `enth land`). git leaves it empty and lands Branch.
	Changes []string
	// RequestID is the id an enth land is issued under (§6.3:
	// "<MR id>/<attempt>"). The same id and Changes again answer what the
	// first land answered and land nothing, which is what makes a land
	// replayable after a crash. git ignores it.
	RequestID string
}

// Prepared is the result of Prepare: the work placed on top of the current
// target, in a directory the caller may gate.
type Prepared struct {
	MR      MR
	GateDir string
	// Tip and New are an enth dry run's old and new (§6.1): the target's tip
	// the work was placed on (T, "" for a ref that does not exist yet) and
	// the id the land would move it to (N). GateDir is a checkout of New, so
	// the tree that is gated is the tree Land lands or nothing lands. git
	// leaves both empty.
	Tip string
	New string
	// Plan is the dry run's per-Change ids (enth). Informational: Land binds
	// on Tip and New, not on Plan.
	Plan []LandedChange
}

// Landed is what Land moved the target to.
type Landed struct {
	// SHA is the target's new tip. For git it is `rev-parse HEAD`'s output,
	// read best-effort after the push. enth leaves it empty: a Change id is
	// not a git sha (§7), and Record carries the land instead.
	SHA string
	// Record is enth's `landed` line (§7). nil for git.
	Record *LandRecord
}

// LandedChange is one Change of an enth land: the id it was submitted as and
// the id it landed as (equal when its base was already the tip).
type LandedChange struct {
	Submitted string `json:"submitted"`
	Landed    string `json:"landed"`
}

// LandRecord is an enth land as enth reports it (§7): the fields of the
// `landed` line, stored as given. Old is "" when the land created the ref.
type LandRecord struct {
	Ref     string         `json:"ref"`
	Entry   int            `json:"entry"`
	Old     string         `json:"old"`
	New     string         `json:"new"`
	Changes []LandedChange `json:"changes"`
	// RequestID is the request id the land was issued under. Not part of
	// enth's line; recorded so a reader can tie the record to its attempt.
	RequestID string `json:"request_id,omitempty"`
}

// Covers reports whether every one of ids appears as a submitted Change of
// the record: the already-merged rule's test (§6.3).
func (r *LandRecord) Covers(ids []string) bool {
	if r == nil || len(ids) == 0 {
		return false
	}
	got := make(map[string]bool, len(r.Changes))
	for _, c := range r.Changes {
		got[c.Submitted] = true
	}
	for _, id := range ids {
		if !got[id] {
			return false
		}
	}
	return true
}

// Lander is the refinery pipeline: R2, R5–R11, R13, R14, R16, R17, R20–R23.
type Lander interface {
	// Prepare brings the submitted work on top of the current target and
	// returns the result to gate.
	Prepare(ctx context.Context, mr MR) (Prepared, error)
	// Land performs the one ref move that publishes the prepared result.
	Land(ctx context.Context, p Prepared) (Landed, error)
	// Cleanup removes what the MR leaves behind once landed (git: the
	// submitted branch on origin; enth: nothing).
	Cleanup(ctx context.Context, mr MR) error
}

// ---- Refs (slice 1) ----

// TagResult says how a Tag call converged.
type TagResult int

const (
	// TagCreated: the tag did not exist and was created and published.
	TagCreated TagResult = iota
	// TagAlreadyPresent: the tag already existed at the requested commit.
	TagAlreadyPresent
	// TagRacedPresent: publishing was refused, but the tag now exists at the
	// requested commit (someone else published the same tag concurrently).
	TagRacedPresent
)

// Refs: R3, R4, R18, L4.
type Refs interface {
	Default(ctx context.Context, repo Repo) (string, error)
	// CreateRef is §3's Refs.Create, renamed: Backend embeds both Workspaces
	// and Refs, and Go refuses two methods named Create in one interface.
	CreateRef(ctx context.Context, repo Repo, ref, at string) error
	// Tag publishes an annotated tag name at commit at. It is idempotent: a tag
	// already at `at` is success, a tag elsewhere is an error and never moved.
	Tag(ctx context.Context, repo Repo, name, at, msg string) (TagResult, error)
}

// ---- Errors ----

// ErrStale means the target moved under the operation; re-run from Prepare.
var ErrStale = errors.New("vcs: target moved; re-run from Prepare")

// ErrUnsupported means the backend has no such operation for this repo.
var ErrUnsupported = errors.New("vcs: unsupported by this backend")

// ConflictError reports that the work does not apply on the target.
type ConflictError struct {
	Paths []string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("vcs: conflict in %d path(s)", len(e.Paths))
}

// RefusedError is a backend's refusal with a stable code.
type RefusedError struct {
	Code string
	Msg  string
}

func (e *RefusedError) Error() string {
	if e.Msg == "" {
		return "vcs: refused: " + e.Code
	}
	return "vcs: refused: " + e.Code + ": " + e.Msg
}

// CommandError is a failed backend command whose verbatim output the caller
// reports. Output is NOT trimmed: callers that print it today print git's
// output byte for byte, and slice 2 changed none of that text.
type CommandError struct {
	// Args is the command that ran, without the program name.
	Args []string
	// Output is the command's combined output, verbatim.
	Output []byte
	// Err is the command's own error (for git, usually *exec.ExitError).
	Err error
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%v\n%s", e.Err, e.Output)
}

func (e *CommandError) Unwrap() error { return e.Err }

// UnnamedRepoError reports a workspace that answers but whose repo cannot be
// named from it (git: a common dir that is not a conventional `.git`). Reason
// is the backend's own words and is the whole message.
type UnnamedRepoError struct {
	Dir    string
	Reason string
}

func (e *UnnamedRepoError) Error() string { return e.Reason }

// StepError is a failed step of a land pipeline, carrying the command's
// verbatim output so the caller can classify the failure from it.
type StepError struct {
	Step Step
	// Args is the command that ran, without the program name (git: the git
	// argv). Recorded so a failure record can name what was attempted.
	Args []string
	// Output is the step's combined output, trimmed.
	Output string
	// Err is the step's own error.
	Err error
	// Override is what LandHooks.StepFailed returned for this failure, if
	// anything. A caller that set the hook should prefer it.
	Override error
}

func (e *StepError) Error() string {
	return fmt.Sprintf("%s: %s: %v", e.Step, e.Output, e.Err)
}

func (e *StepError) Unwrap() error { return e.Err }
