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
// only the refinery calls it. Work (slice 3) is declared with placeholder
// types: its slice fixes the signatures, as §3 says ("Signatures are sketches.
// The build slices fix them."). Selection by repo (slice 4) does not exist
// yet, so nothing returns a Backend.
//
// # Slice 2 (mg-cc7ae)
//
// Workspaces has its signatures fixed and a git implementation
// (gitvcs.Workspaces), called by internal/agent's polecat spawn, its rollback
// and leftover-branch reclaim, the injected-persona exclude, and the crew
// workspace refresh. Remove, Dirty and Describe stay slice 3's.
package vcs

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Kind names a backend. git is the zero value's meaning: a repo with no
// recorded kind is a git repo.
type Kind string

const (
	Git  Kind = "git"
	Enth Kind = "enth"
)

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
// injected-persona exclude and the crew workspace refresh use. Remove, Dirty
// and Describe are slice 3's (gitgc, worktree-dirty) and are still the
// sketches slice 1 declared.
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

	// Slice 3's, still sketches.
	Remove(ctx context.Context, dir string) error
	Dirty(ctx context.Context, dir string) ([]string, error)
	Describe(ctx context.Context, dir string) (Workspace, error)
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
		return fmt.Sprintf("%s: fast-forwarded %s %d commit(s) to %s%s (%s..%s)",
			r.Path, r.Branch, r.Behind, r.Upstream, inferredNote(r), r.From, r.To)
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

// WorkQuery, Submission, WorkState, Message and Diff are placeholders until
// slice 3 fixes them.
type WorkQuery struct {
	Item   string
	Worker string
}

type Submission struct {
	Branch string
}

type WorkState struct {
	Landed bool
}

type Message struct {
	Rev  string
	Body string
}

type Diff struct {
	Paths []string
}

// Work finds and judges a worker's output: W1, W5, P2, P5, R1, R9, R12, S2–S9, C4.
type Work interface {
	Find(ctx context.Context, repo Repo, q WorkQuery) ([]Submission, error)
	State(ctx context.Context, repo Repo, s Submission, target string) (WorkState, error)
	Messages(ctx context.Context, repo Repo, s Submission) ([]Message, error)
	Diff(ctx context.Context, repo Repo, s Submission) (Diff, error)
	Cat(ctx context.Context, repo Repo, rev, path string) ([]byte, error)
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
}

// Prepared is the result of Prepare: the work placed on top of the current
// target, in a directory the caller may gate.
type Prepared struct {
	MR      MR
	GateDir string
}

// Landed is what Land moved the target to.
type Landed struct {
	// SHA is the target's new tip. For git it is `rev-parse HEAD`'s output,
	// read best-effort after the push.
	SHA string
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
