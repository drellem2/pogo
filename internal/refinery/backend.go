package refinery

import (
	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/gitvcs"
)

// The refinery's VCS backend (docs/design/enth-vcs.md §3, slice 1, mg-a18a3).
//
// Every git operation of the land pipeline (attemptMerge), the post-merge tag,
// the branch reap, the default-branch lookup and --auto-create-target goes
// through a vcs.Lander / vcs.Refs. Only the git backend exists, and selection
// by repo is slice 4, so backendFor ignores its argument today.
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

// backendFor returns the backend for the repo at repoPath. Always git until
// slice 4 adds selection from the project record.
func backendFor(repoPath string) landBackend {
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
