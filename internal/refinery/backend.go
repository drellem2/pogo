package refinery

import (
	"sync/atomic"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
	"github.com/drellem2/pogo/internal/vcs/gitvcs"
)

// The refinery's VCS backend (docs/design/enth-vcs.md §3, slice 1, mg-a18a3).
//
// Every git operation of the land pipeline (attemptMerge), the post-merge tag,
// the branch reap, the default-branch lookup and --auto-create-target goes
// through a vcs.Lander / vcs.Refs. backendFor picks the backend from the
// repo's project record (slice 4, mg-160e8) through the lookup pogod installs
// with SetRepoVCS. A repo whose record says enth gets the inert enth backend,
// which refuses every step with vcs.ErrUnsupported; every other repo — no
// record, no vcs key, or no lookup installed — gets git, exactly as before.
//
// The refinery's other git calls (recovery, prune, gate-dirt inspection, the
// closing-ref scan, submit validation, the source-checkout fast-forward) still
// run git directly through gitCmdOutput — which is now the git backend's own
// runner, so they share its prompt-off, identity and credential handling.
// They belong to the Workspaces and Work groups (slices 2 and 3).

// landBackend is the part of vcs.Backend the refinery uses.
type landBackend interface {
	vcs.Lander
	vcs.Refs
}

type gitLandBackend struct {
	gitvcs.Lander
	gitvcs.Refs
}

// gitRunner runs the refinery's git commands; its log lines read
// "refinery: git [...] failed: ...", as they always have.
var gitRunner = gitvcs.Runner{LogPrefix: "refinery"}

var theGitBackend landBackend = gitLandBackend{
	Lander: gitvcs.Lander{Runner: gitRunner},
	Refs:   gitvcs.Refs{Runner: gitRunner},
}

// RepoVCS is the backend lookup by repo: the kind and enth settings recorded
// for the repo at repoPath. pogod passes project.VCSOf.
type RepoVCS func(repoPath string) (vcs.Kind, *vcs.EnthRepo)

var repoVCS atomic.Pointer[RepoVCS]

// SetRepoVCS installs the lookup backendFor consults. nil uninstalls it, and
// every repo is then git.
func SetRepoVCS(f RepoVCS) {
	if f == nil {
		repoVCS.Store(nil)
		return
	}
	repoVCS.Store(&f)
}

// backendFor returns the backend for the repo at repoPath.
func backendFor(repoPath string) landBackend {
	f := repoVCS.Load()
	if f == nil {
		return theGitBackend
	}
	kind, enth := (*f)(repoPath)
	if kind == vcs.Enth {
		b := enthvcs.Backend{}
		if enth != nil {
			b.Repo = *enth
		}
		return b
	}
	return theGitBackend
}

// refineryCommitterName is the committer a rebase replay records when no
// ambient git identity exists (ia-1428, gh #7).
const refineryCommitterName = gitvcs.CommitterName

// gitCmdOutput runs a git command in the given directory and captures
// combined stdout/stderr output. Returns the output and any error.
// This ensures git error messages (e.g. push rejection reasons) are
// available for logging and stored in MergeRequest.Error, rather than
// being lost to pogod's stdout/stderr. See gitvcs.Runner.Run.
func gitCmdOutput(dir string, args ...string) (string, error) {
	return gitRunner.Run(dir, args...)
}

// benignGitOutcome reports whether git output is a known expected outcome
// rather than a failure, returning the description to log in its place.
// See gitvcs.BenignOutcome (mg-5d3f).
func benignGitOutcome(out string) (string, bool) {
	return gitvcs.BenignOutcome(out)
}
