// Package gitgc garbage-collects stale polecat git artifacts: orphaned
// `polecat-<id>` branches and leaked git worktrees left behind when a
// polecat exits abnormally (force-stop, crash, stall) or when pogod itself
// dies mid-polecat.
//
// The package is deliberately self-contained — a library of pure-ish
// functions with no dependency on pogod, the agent registry, or any
// long-running state. Its only external dependencies are the `git` and
// `mg` executables. That lets the same logic drive three callers:
//
//   - pogod's startup sweep + periodic ticker (cmd/pogod),
//   - the manual `pogo gc` command (cmd/pogo),
//   - and the one-time cleanup of historically-accumulated cruft.
//
// Safety is biased toward keeping: a branch or worktree is only ever
// deleted when its work item is positively classified as concluded
// (done/archived) and the owning polecat is not live. Anything that
// cannot be classified is kept.
package gitgc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/config"
)

// BranchPrefix is the prefix every polecat branch carries. A polecat
// spawned for work item mg-XXXX gets branch `polecat-<name>` where
// <name> is the polecat's registry name (see internal/agent/api.go).
const BranchPrefix = "polecat-"

// DefaultTargetBranch is the branch a polecat branch must be merged into
// before a *done* (but not archived) ticket's branch becomes deletable.
const DefaultTargetBranch = "main"

// DefaultPolecatsDir returns the directory polecat worktrees live under
// ($POGO_HOME/polecats, default ~/.pogo/polecats) — the value callers pass as
// Options.PolecatsDir to enable the orphan-dir scan. Must match the worktree
// path chosen at spawn time in internal/agent (which calls this). The error
// return is kept for call-site compatibility; it is always nil.
func DefaultPolecatsDir() (string, error) {
	return filepath.Join(config.PogoHome(), "polecats"), nil
}

// git runs a git subcommand against repo and returns combined output. A
// non-zero exit is turned into an error carrying the trimmed output.
func git(repo string, args ...string) ([]byte, error) {
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// ListPolecatBranches returns every local branch whose name starts with
// BranchPrefix, sorted by git's default ordering.
func ListPolecatBranches(repo string) ([]string, error) {
	out, err := git(repo, "branch", "--list", BranchPrefix+"*", "--format=%(refname:short)")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

// BranchSuffix returns the part of a polecat branch name after the
// "polecat-" prefix, or "" if branch is not a polecat branch. For branch
// `polecat-30d5` this is `30d5`, which is also the polecat's registry
// name and worktree directory basename.
func BranchSuffix(branch string) string {
	if !strings.HasPrefix(branch, BranchPrefix) {
		return ""
	}
	return strings.TrimPrefix(branch, BranchPrefix)
}

// Worktree is one entry of `git worktree list`.
type Worktree struct {
	Path     string // absolute path of the worktree directory
	Branch   string // short branch name; "" when detached or bare
	Bare     bool   // the bare main repository
	Detached bool   // checked out at a commit, not a branch
	Prunable bool   // git considers the registration stale (dir gone, etc.)
	Main     bool   // the first entry — the primary worktree
}

// IsPolecat reports whether the worktree holds a polecat branch.
func (w Worktree) IsPolecat() bool {
	return strings.HasPrefix(w.Branch, BranchPrefix)
}

// ListWorktrees parses `git worktree list --porcelain` for repo. The
// first entry is flagged Main.
func ListWorktrees(repo string) ([]Worktree, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var (
		worktrees []Worktree
		cur       Worktree
		have      bool
	)
	flush := func() {
		if have {
			cur.Main = len(worktrees) == 0
			worktrees = append(worktrees, cur)
		}
		cur = Worktree{}
		have = false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			cur.Path = val
			have = true
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "bare":
			cur.Bare = true
		case "detached":
			cur.Detached = true
		case "prunable":
			cur.Prunable = true
		}
	}
	flush()
	return worktrees, nil
}

// CheckedOutBranches returns the set of branches currently checked out in
// any worktree of repo. git refuses to delete such branches, so the GC
// must skip them.
func CheckedOutBranches(repo string) (map[string]bool, error) {
	wts, err := ListWorktrees(repo)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, w := range wts {
		if w.Branch != "" {
			set[w.Branch] = true
		}
	}
	return set, nil
}

// BranchMerged reports whether branch is an ancestor of target (i.e. its
// commits are already contained in target).
func BranchMerged(repo, branch, target string) (bool, error) {
	cmd := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", branch, target)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	// Exit code 1 is the well-defined "not an ancestor" answer; anything
	// else (bad ref, code 128) is a real error.
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("merge-base --is-ancestor %s %s: %w", branch, target, err)
}

// DeleteBranch force-deletes a local branch (`git branch -D`). Force is
// required because an archived ticket's branch may never have been merged.
func DeleteBranch(repo, branch string) error {
	_, err := git(repo, "branch", "-D", branch)
	return err
}

// PruneWorktrees runs `git worktree prune`, which drops registrations
// whose working directory has gone missing. When dryRun is set nothing is
// removed; the verbose output describes what would be pruned.
func PruneWorktrees(repo string, dryRun bool) (string, error) {
	args := []string{"worktree", "prune", "-v"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	out, err := git(repo, args...)
	return strings.TrimSpace(string(out)), err
}

// DirtyWorktreeError reports a removal refused because the worktree held
// uncommitted work. Files lists the `git status --porcelain` entries that
// caused the refusal, capped at dirtyFileListCap for legibility.
type DirtyWorktreeError struct {
	Path  string
	Files []string
	// Total is the full count of dirty entries, which may exceed len(Files).
	Total int
}

func (e *DirtyWorktreeError) Error() string {
	shown := strings.Join(e.Files, ", ")
	if e.Total > len(e.Files) {
		shown = fmt.Sprintf("%s (+%d more)", shown, e.Total-len(e.Files))
	}
	return fmt.Sprintf("worktree %s has %d uncommitted change(s), refusing to remove: %s",
		e.Path, e.Total, shown)
}

// dirtyFileListCap bounds how many paths a refusal names. The operator needs
// enough to recognise the work, not a full status dump in a log line.
const dirtyFileListCap = 10

// UndeterminedWorktreeError reports a removal refused because dirtiness could
// not be DETERMINED — `git status` failed — rather than because the tree was
// known dirty. The two are separate types on purpose: reporting a status
// failure as "dirty" would be a false claim about what is in the tree, and an
// operator acting on it would go looking for uncommitted files that may not
// exist. Cannot-tell is its own answer, the same shape as mg-dcb1's
// RelationUnknown one subsystem over.
type UndeterminedWorktreeError struct {
	Path string
	// Err is the underlying status failure, kept so the operator can see
	// WHICH way git broke — a corrupt .git reads differently from EACCES.
	Err error
}

func (e *UndeterminedWorktreeError) Error() string {
	return fmt.Sprintf("worktree %s: cannot determine whether it holds uncommitted work, "+
		"refusing to remove: %v", e.Path, e.Err)
}

func (e *UndeterminedWorktreeError) Unwrap() error { return e.Err }

// WorktreeOwner is what the CALLER has established about whether anything
// still owns a worktree. It is the discriminator for the cannot-tell case,
// and it has to come from the caller: gitgc is deliberately free of any
// dependency on the agent registry (see the package comment), and liveness
// lives there.
//
// The zero value is the conservative arm. A caller that establishes nothing
// gets preservation, which is the direction that cannot lose files.
type WorktreeOwner int

const (
	// OwnerUnproven: the caller has NOT established that this tree is
	// unowned. It may hold the in-flight work of a real agent.
	OwnerUnproven WorktreeOwner = iota
	// OwnerGone: liveness has been positively excluded — no live agent owns
	// this tree and none is coming back for it. Only pass this when you have
	// actually checked; it licenses destroying files that cannot be read.
	OwnerGone
)

func (o WorktreeOwner) String() string {
	if o == OwnerGone {
		return "owner-gone"
	}
	return "owner-unproven"
}

// WorktreeDirty reports whether worktreeDir holds uncommitted work — modified
// tracked files OR untracked new ones. The untracked half is not an
// afterthought: the loss that produced mg-ee02 was a brand-new 201-line test
// file, so a check that only noticed tracked modifications would miss exactly
// the case this exists to prevent. Files ignored via .gitignore do not count;
// a polecat's ./bin build output is not work, and counting it would make every
// worktree refuse to reap.
//
// A non-nil error means dirtiness could not be determined. That population is
// WIDER than it looks: an absent directory and a stripped .git pointer are in
// it, but so is any transient failure — lock contention, an I/O blip, EACCES,
// an interrupted call. Callers must decide what an unclassifiable tree
// deserves; RemoveWorktree decides on ownership (see its doc comment).
func WorktreeDirty(worktreeDir string) (bool, []string, error) {
	if worktreeDir == "" {
		return false, nil, fmt.Errorf("empty worktree path")
	}
	if _, err := os.Stat(worktreeDir); err != nil {
		return false, nil, fmt.Errorf("stat worktree %s: %w", worktreeDir, err)
	}
	out, err := git(worktreeDir, "status", "--porcelain")
	if err != nil {
		return false, nil, fmt.Errorf("status %s: %w", worktreeDir, err)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return len(files) > 0, files, nil
}

// RemoveWorktree removes a git worktree registration and deletes its
// directory from disk — unless the worktree holds uncommitted work, in which
// case it refuses and returns a *DirtyWorktreeError naming what it preserved.
// It is safe to call when the registration, the directory, or both are
// already gone.
//
// This is the cleanup invoked on every polecat exit — normal and abnormal
// alike (see the onExit hook in cmd/pogod) — and during a GC sweep.
//
// # Why it refuses (mg-ee02)
//
// This function used to pass --force and then os.RemoveAll unconditionally,
// and it destroyed a live polecat's uncommitted work: a stopped mid-flight
// agent's tree, including a new 201-line race test, went with it. `git
// worktree remove` refuses a dirty worktree BY DEFAULT — that guard exists
// precisely for this — and --force opted out of it. Worse, the RemoveAll
// behind it ran even when git declined, so restoring git's refusal alone
// would have changed nothing observable. Both had to go; the dirty check now
// sits in front of both, which is the only placement that actually holds.
//
// The operation was safe exactly when the agent had finished, which is when
// it is normally called — so the destructive case and the common case were
// disjoint, and nobody noticed. The clean case still reaps, unchanged.
//
// # The cannot-tell case (mg-4d45): ownership decides
//
// mg-ee02 folded cannot-tell into clean and PROCEEDED, on the reasoning that
// refusing would strand every gh #31 orphan forever, since nothing can ever
// prove one clean. That reasoning is sound and this is a revision of it, not
// a repair of an oversight.
//
// What was wrong was the BOUNDARY, not the direction. The doc comment bounded
// the hole to "trees git can no longer see"; the predicate bounded it to "any
// status error". Those are different populations. A lock contention, an I/O
// blip, a slow filesystem, an interrupted call — none of those is a tree git
// can no longer see, and all of them landed in the destructive arm.
//
// That miswidening matters more than a fail-open usually would, because the
// check's failure is CORRELATED with the value at risk. `git status` does not
// fail at random; it fails when .git is damaged, the disk is unhappy, or
// permissions are broken — exactly when the working files are least
// reproducible. A guard whose failure mode coincides with the case it exists
// to protect is worse than no guard, because it also looks like one.
//
// The fix is not to invert the default globally; blanket fail-closed would
// re-open gh #31. The discriminator is OWNERSHIP, which is knowable
// independently of git:
//
//   - OwnerUnproven — cannot-tell REFUSES, with an *UndeterminedWorktreeError
//     that names the status failure. The files may be someone's in-flight
//     work, and a pinned worktree is recoverable by a human where deleted
//     files are not.
//   - OwnerGone — cannot-tell RECLAIMS, exactly as before. Nobody is coming
//     back for an orphan's files, and leaking worktrees is its own defect.
//
// An ABSENT directory is not cannot-tell: there are no files to protect, so
// removal proceeds under either ownership and the registration still gets
// dropped.
//
// Dropping the registration is load-bearing, not incidental: it is what frees
// the polecat's branch for deletion (git refuses to delete a branch checked
// out in a worktree), which is why Sweep processes worktrees before branches.
// TestRemoveWorktreeFreesCheckedOutBranch guards it.
func RemoveWorktree(sourceRepo, worktreeDir string, owner WorktreeOwner) error {
	if worktreeDir == "" {
		return nil
	}
	// An absent directory holds nothing to protect. Checked BEFORE the status
	// call so it never reaches the cannot-tell arm: "there are no files" and
	// "there may be files I cannot read" are different facts, and only the
	// second one deserves a refusal.
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		return RemoveWorktreeForce(sourceRepo, worktreeDir)
	}

	isDirty, files, err := WorktreeDirty(worktreeDir)
	switch {
	case err == nil && isDirty:
		shown := files
		if len(shown) > dirtyFileListCap {
			shown = shown[:dirtyFileListCap]
		}
		return &DirtyWorktreeError{Path: worktreeDir, Files: shown, Total: len(files)}
	case err != nil && owner != OwnerGone:
		// Cannot tell, and nothing has established that this tree is unowned.
		// Refuse, and say WHY — reporting a status failure as "dirty" would
		// be a different and false claim.
		return &UndeterminedWorktreeError{Path: worktreeDir, Err: err}
	}
	return RemoveWorktreeForce(sourceRepo, worktreeDir)
}

// RemoveWorktreeForce removes a worktree regardless of uncommitted work. It is
// the deliberate override behind RemoveWorktree's refusal, and the escape
// hatch that keeps preservation from becoming an unbounded leak: without a way
// to reclaim a dirty tree, a refused worktree would pin its branch forever.
//
// Callers must have a positive reason to discard work. Two do: a spawn that
// failed before the agent ever ran (internal/agent), and an operator who
// asked for it explicitly (`pogo gc --apply --force`).
//
// The `git worktree remove` step is best-effort: it drops the registration
// when the worktree is still linked (the normal case since the submit-time
// unlink was deleted in gh #88), and fails harmlessly on a legacy worktree
// whose .git pointer that hook already removed. os.RemoveAll is the backstop
// that reclaims the directory either way. The returned error reflects only
// whether the directory is gone, which is the outcome callers care about.
func RemoveWorktreeForce(sourceRepo, worktreeDir string) error {
	if worktreeDir == "" {
		return nil
	}
	if sourceRepo != "" {
		// Best-effort: ignore the error. --force handles a worktree with a
		// dirty or locked state; an already-unlinked worktree just fails.
		exec.Command("git", "-C", sourceRepo, "worktree", "remove", "--force", worktreeDir).Run()
	}
	if err := os.RemoveAll(worktreeDir); err != nil {
		return fmt.Errorf("remove worktree dir %s: %w", worktreeDir, err)
	}
	return nil
}
