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
// The four groups are declared here as the design sketches them. Only Lander
// and Refs have an implementation, the git backend in internal/vcs/gitvcs, and
// only the refinery calls it. Workspaces (slice 2) and Work (slice 3) are
// declared with placeholder types: their slices fix the signatures, as §3
// says ("Signatures are sketches. The build slices fix them."). Selection by
// repo (slice 4) does not exist yet, so nothing returns a Backend.
package vcs

import (
	"context"
	"errors"
	"fmt"
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

// WorkspaceSpec and Workspace are placeholders until slice 2 fixes them.
type WorkspaceSpec struct {
	Repo   Repo
	Dir    string
	Branch string
	Base   string
}

type Workspace struct {
	Dir    string
	Repo   Repo
	Branch string
	Base   string
}

// Workspaces: W2–W9, P1, C1, C3, C5, C6, S10, X2, X4, O1, O3.
type Workspaces interface {
	Create(ctx context.Context, s WorkspaceSpec) (Workspace, error)
	Remove(ctx context.Context, dir string) error
	Dirty(ctx context.Context, dir string) ([]string, error)
	Describe(ctx context.Context, dir string) (Workspace, error)
	Refresh(ctx context.Context, dir string) error
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
