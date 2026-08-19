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
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

// PolecatNameForWorktree returns the name of the polecat that OWNS a worktree
// — the only fact a GC sweep may use to decide whether that worktree is in
// use. It is the path's basename, because a polecat's worktree is created at
// `<polecats dir>/<name>` at spawn time (internal/agent/api.go) and nothing
// ever moves it.
//
// # Why the path and not the branch (gh #94, mg-dd92)
//
// The sweep used to read liveness off `BranchSuffix(wt.Branch)`: the polecat
// name embedded in the branch CHECKED OUT INSIDE the worktree. Those two
// strings agree for a polecat that stays on its own branch and disagree the
// moment one does not — and checking out a foreign branch is something our own
// shipped review and QA roles are instructed to do. When they disagree, the
// live polecat is invisible to the liveness gate: its worktree is removed out
// from under it mid-task, and the freeing of its branch waives the phase-2
// guard so the ref goes too. The agent keeps running and `pogo agent list`
// keeps reporting it healthy; the work survives only if the commit is still
// loose in the shared object store.
//
// The branch is a fact about what the polecat is WORKING ON. The path is a
// fact about WHOSE TREE THIS IS. Only the second one answers "may I delete
// this directory", so only the second one is consulted here — the branch is
// deliberately not retained as a fallback, because an OR would re-admit
// exactly the signal that made a live tree look dead.
//
// The inverse failure is real too and is not traded away: a polecat that has
// exited is not in the live set under any key, so its tree is still reaped on
// the next sweep. See TestSweepKeepsLivePolecatOnForeignBranch, whose control
// is a normal exit collecting as it always did.
func PolecatNameForWorktree(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(path))
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
	// Untracked is how many of Total are untracked paths (`??` in porcelain
	// v1); Modified is the tracked changes making up the rest.
	//
	// THE SPLIT IS THE POINT OF THE COUNT, not a decoration on it (mg-d45b). A
	// modified tracked file still has its committed version in the object
	// store, so the worst case is losing an edit. An untracked file is on no
	// branch, in no stash and on no remote, and the preserved tree is its only
	// copy anywhere on the machine — `~/.pogo/polecats/qbe37` held an entire
	// 1450-line `internal/strandwatch/` package that way. A single
	// `dirty_paths: 16` cannot tell those apart, so a reader deciding whether a
	// preserved tree is urgent had to open the tree to find out, which is the
	// same "reconstruct it by hand" cost this ticket exists to remove.
	//
	// They are computed over the FULL porcelain list, never over Files, which
	// is capped at dirtyFileListCap. Deriving them from the capped list would
	// silently under-report every tree with more than ten changes — a number
	// reconstructed from a partial record, which is a smaller instance of the
	// exact defect being fixed here.
	Modified  int
	Untracked int
}

// countPorcelain splits `git status --porcelain` lines into tracked changes and
// untracked paths.
//
// Untracked entries are exactly those whose status code is `??`. Ignored
// entries (`!!`) cannot appear because WorktreeDirty does not pass --ignored,
// and anything else — modified, added, deleted, renamed, unmerged — is a change
// to a path git already tracks, so HEAD still holds a version of it.
func countPorcelain(files []string) (modified, untracked int) {
	for _, line := range files {
		if strings.HasPrefix(line, "??") {
			untracked++
			continue
		}
		modified++
	}
	return modified, untracked
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
	// Untouched is how long the tree has gone unwritten, and UntouchedKnown
	// whether it could be established at all.
	//
	// This refusal is PERMANENT and unconditional — nothing collects a tree it
	// could not read, under any ownership and at any age. So the age is a pure
	// REPORT: it is the signal a human deciding whether to reclaim asked for
	// ("untouched 30 days" turns a pinned worktree from a mystery into a
	// decision), and it decides nothing itself.
	//
	// That split is load-bearing rather than stylistic. Every attempt to let
	// this number ACT — a drain, then a veto — failed on the same rock, and the
	// second failure is the subtler one worth recording here: a veto that
	// expires does not resolve the contradiction it was built for, it just
	// stops being able to see it. Absence of evidence is not evidence of
	// absence. Because the age only ever prints, the worst it can now do is
	// print an unhelpful number; it cannot be blind in a way that costs files.
	Untouched      time.Duration
	UntouchedKnown bool
}

func (e *UndeterminedWorktreeError) Error() string {
	return fmt.Sprintf("worktree %s: cannot determine whether it holds uncommitted work%s, "+
		"refusing to remove: %v", e.Path, untouchedClause(e.Untouched, e.UntouchedKnown), e.Err)
}

// untouchedClause renders the reported age of a tree, or says plainly that it
// could not be established. It is never empty: on a permanent refusal the
// operator needs the age or an explicit statement that there isn't one, and a
// silently missing clause reads as "recent" to some people and "old" to others.
func untouchedClause(d time.Duration, known bool) string {
	if !known {
		return " (age unknown — the tree could not be listed)"
	}
	return " (untouched " + humanAge(d) + ")"
}

// humanAge renders a duration at the resolution an operator deciding whether to
// reclaim a worktree actually cares about. 30 days is the number that makes the
// decision; 720h0m0s is the number that makes them do arithmetic first.
func humanAge(d time.Duration) string {
	switch {
	case d < 0:
		// An mtime in the future. Say so rather than printing a negative age:
		// it means a clock step or a bad copy, and it is worth noticing.
		return "in the future — check the clock"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

func (e *UndeterminedWorktreeError) Unwrap() error { return e.Err }

// NewestWrite is newestWrite for callers outside this package, and it carries
// the whole of that function's contract — including the SCOPE note below, which
// binds them too: it is correct for an AGENT'S OWN worktree and has not been
// reasoned about for a main worktree.
//
// It is exported for internal/progresswatch, whose second conjunct is "no file
// written in N minutes" across the live workers. Re-implementing the walk there
// would have produced a second answer to the same question, differing in
// exactly the places this one is careful about — the .git skip, an unreadable
// directory being an error rather than a smaller maximum, and a vanished entry
// mid-walk not being either.
func NewestWrite(worktreeDir string) (time.Time, error) { return newestWrite(worktreeDir) }

// newestWrite reports the modification time of the most recently written thing
// inside worktreeDir, and an error when the tree cannot be ENUMERATED — which
// is a different and worse answer than "nothing has been written", and is why
// the two are separate returns.
//
// # It WALKS. It never stats the root (requirement, not an implementation note)
//
// The root directory's mtime is not a cheap approximation of the tree's; it is a
// different quantity answering a different question, and it was MEASURED to be
// blind to the case that matters:
//
//	root mtime before deep edit:      720h old
//	deep file mtime after edit:       0s old
//	root mtime after deep edit:       720h old   <- blind to active work
//	root mtime after ROOT-level add:  0s old     <- fires
//
// A live agent editing pkg/deep/work.go leaves root mtime untouched. Since the
// veto was withdrawn nothing DECIDES on this number, so the cost of getting it
// wrong is now a misleading report rather than a deleted file — but the report
// is the thing a human clears a permanent pin on, so "untouched 30 days" for a
// tree somebody is working in is still a wrong answer to a real question.
//
// # Two shapes that read alike and are not: EACCES on .git vs on the ROOT
//
// One keystroke apart, and they behave differently. Both names are written here
// so that nobody collapses them; pm-pogo's first draft of the cannot-enumerate
// ruling did, which is how the distinction got caught.
//
//	SHAPE                     WALK        AGE REPORT
//	damaged .git pointer      full        measured — and ACCURATE only because
//	                                      .git is excluded below; counting it
//	                                      reported `untouched 0s` for a
//	                                      month-cold tree
//	EACCES on the .git FILE   full        measured — .git is a pointer FILE in a
//	                                      linked worktree, and reading its mtime
//	                                      needs SEARCH on the parent, not READ on
//	                                      the file; the root is still 0755
//	truncated index           full        measured — the index lives in the admin
//	                                      dir, outside the worktree
//	EACCES on the worktree    root only   NOT MEASURABLE — reported as unknown
//	ROOT
//
// So "the filesystem is broken exactly when git is" is FALSE: in three of the
// four shapes the whole tree walks and the untracked file is visible. The fourth
// is not a second decision — the removal was already refused — only a line that
// has to admit it has no age to offer.
//
// # HAZARD: the signal is disturbed by the event it reports on
//
// pm-pogo required this hazard to live in the code rather than a mail archive,
// so that the next person reaching for "just check the timestamp" meets it
// here. That requirement got STRONGER when the veto was withdrawn, not weaker:
// the number used to be a second layer under a guard, and it is now the only
// signal an operator has about a pin nothing else will ever clear.
//
// The damage that makes `git status` fail can itself move mtime, unevenly.
// Corrupting a worktree's .git pointer WRITES A FILE INSIDE THE TREE. chmod
// damage moves ctime, not mtime. A truncated index lives outside the worktree
// entirely. So the same tree, abandoned for the same month, reads differently
// depending on how it broke — for reasons that have nothing to do with whether
// anyone is alive.
//
// Excluding .git (below) removes the one shape where that produced a wrong
// number. THE RESIDUE, a property rather than a defect to fix: this signal is
// perturbed by the event it reports on, and the perturbation is structural
// rather than filesystem-specific — a damage event that writes anywhere else
// inside the tree would land in the report the same way, and nothing here can
// distinguish "an agent wrote this" from "the failure wrote this". Anyone
// adding a second signal, a per-shape rule, or above all anyone proposing to
// let this number DECIDE something, needs that fact first — and should read
// RemoveWorktree on why two attempts to let it act were withdrawn.
//
// Measurement caveat kept rather than dropped: the chmod-moves-ctime row was
// measured on APFS/macOS only. POSIX agrees, but that is one platform. The
// pointer-damage row does NOT depend on it — that one is the damage event
// writing a file, which is platform-independent.
//
// .git is EXCLUDED from the walk, and the history of that decision is the
// reason to state it rather than leave it to the code:
//
//	counted  -> pointer damage reports `untouched 0s` for a month-cold tree
//	excluded -> every enumerable shape reports the truth; nothing else changes
//
// It was COUNTED while the veto existed, deliberately: there, a refreshed clock
// made the veto OVER-refuse, which was the safe direction, and a write arriving
// through git alone was exactly the kind a veto hunting a wrong death-verdict
// should not drop. That reasoning died with the veto. What is left is a number
// a human reads, and on the pointer-damage shape — the shape gh #97 was
// reproduced with — counting .git makes it wrong in the direction that
// DISCOURAGES reclamation: the operator sees `untouched 0s`, concludes someone
// is working, and leaves a dead tree pinned. It is the only signal they have.
//
// So this is not a trade. Measured across all four shapes, excluding .git is
// right on one row and identical on the other three
// (TestReportedAgeAcrossCannotTellShapes pins all four).
//
// SCOPE, so nobody widens this by accident: in a LINKED worktree — every
// polecat tree, and all the sweep ever walks — .git is a static pointer FILE,
// so skipping it discards nothing an agent wrote. In a MAIN worktree .git is a
// busy directory and this function cannot tell the difference. Skipping is
// correct for this caller; a future caller pointed at a main worktree is a
// different question and should be answered before reusing this.
func newestWrite(worktreeDir string) (time.Time, error) {
	var newest time.Time
	err := filepath.WalkDir(worktreeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory we cannot read. Report no age at all rather than a
			// maximum over the part we happened to see — a partial walk would
			// silently answer "untouched 30 days" about a tree whose recently
			// written half was unreadable.
			return err
		}
		if d.Name() == ".git" && path != worktreeDir {
			// git's own bookkeeping is not agent work, and its mtime moves for
			// reasons unrelated to whether anyone is alive — including the
			// damage event itself. See the SCOPE note above before widening.
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			// One entry that vanished or cannot be stat'ed mid-walk is not an
			// enumeration failure; the tree still lists. Skip it — a missing
			// mtime cannot raise the maximum, and refusing on it would pin a
			// tree for a race with its own teardown.
			return nil
		}
		if mt := info.ModTime(); mt.After(newest) {
			newest = mt
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return newest, nil
}

// worktreeRemovalCheck is the decision RemoveWorktree makes, computed WITHOUT
// removing anything. It is split out so the GC sweep can report in a dry run
// exactly what an apply would do, and can name the real reason in its log,
// while the decision itself still has exactly one implementation.
type worktreeRemovalCheck struct {
	// Refusal is the error RemoveWorktree would return, or nil to proceed.
	Refusal error
	// StatusErr is the `git status` failure whenever dirtiness could not be
	// determined — including when we proceed anyway on positive death evidence.
	// It is kept for the log: reporting only the ticket state on that path
	// names an innocent reason for a destructive act, and a forensic log that
	// names an innocent cause is worse than silence, because a missing line
	// prompts investigation and a plausible one ends it (gh #97).
	StatusErr error
}

// checkWorktreeRemoval applies RemoveWorktree's guard without acting on it.
// See RemoveWorktree for the reasoning behind each arm.
//
// It deliberately does NOT take an ownership verdict. It used to, and every arm
// stopped reading it when the cannot-tell arm was ruled away — a parameter no
// callee reads is the same defect as a doc naming a mechanism that does not
// exist, so it is gone here rather than threaded through. RemoveWorktree still
// accepts one because that is mg-4d45's public API; retiring it is a follow-up.
func checkWorktreeRemoval(worktreeDir string) worktreeRemovalCheck {
	if worktreeDir == "" {
		return worktreeRemovalCheck{}
	}
	// An absent directory holds nothing to protect. Checked BEFORE the status
	// call so it never reaches the cannot-tell arm: "there are no files" and
	// "there may be files I cannot read" are different facts, and only the
	// second one deserves a refusal.
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		return worktreeRemovalCheck{}
	}

	isDirty, files, err := WorktreeDirty(worktreeDir)
	if err == nil {
		if isDirty {
			shown := files
			if len(shown) > dirtyFileListCap {
				shown = shown[:dirtyFileListCap]
			}
			// Counted from `files`, the full list — see DirtyWorktreeError.
			modified, untracked := countPorcelain(files)
			return worktreeRemovalCheck{
				Refusal: &DirtyWorktreeError{
					Path: worktreeDir, Files: shown, Total: len(files),
					Modified: modified, Untracked: untracked,
				},
			}
		}
		return worktreeRemovalCheck{}
	}

	// --- cannot tell: ONE RULE, NO EXCEPTIONS, NO CLOCK -------------------
	//
	// If we could not read the tree, we do not act on it. Not under any
	// ownership, not at any age. See RemoveWorktree's doc for why the
	// discriminators this arm used to have — first death evidence, then an
	// mtime veto on top of it — were both withdrawn.
	//
	// The age is measured only to be REPORTED. A walk that fails is therefore
	// not a second kind of refusal: the answer was already "refuse", and all
	// that changes is that the line has to say the age could not be measured
	// rather than name one.
	newest, werr := newestWrite(worktreeDir)
	return worktreeRemovalCheck{
		Refusal: &UndeterminedWorktreeError{
			Path: worktreeDir, Err: err,
			Untouched: time.Since(newest), UntouchedKnown: werr == nil,
		},
		StatusErr: err,
	}
}

// WorktreeOwner is what the CALLER has established about whether anything
// still owns a worktree. It was the discriminator for the cannot-tell case.
//
// # It NO LONGER DISCRIMINATES ANYTHING (gh #97)
//
// Stated first, because a parameter whose documentation explains its importance
// while the code ignores it is precisely the failure this ticket is about.
// Cannot-tell now refuses unconditionally, so no arm of RemoveWorktree reads
// this value; every ownership reaches the same outcome on every path.
//
// It is retained rather than deleted because that is a separable decision:
// mg-4d45 shipped it as public API, cmd/pogod's exit hook passes it with its own
// reasoning attached, and removing it is a change to that ticket's surface
// rather than to this one's behaviour. Retiring it is a follow-up, and if you
// are reading this because you are about to add a caller — do not. There is
// nothing here for a new caller to influence.
type WorktreeOwner int

const (
	// OwnerUnproven: the caller has NOT established that this tree is
	// unowned. It may hold the in-flight work of a real agent.
	OwnerUnproven WorktreeOwner = iota
	// OwnerGone: liveness has been positively excluded — no live agent owns
	// this tree and none is coming back for it.
	//
	// DORMANT, AND UNREACHABLE IN PRODUCTION (gh #97). Nothing outside tests
	// constructs this value — both gitgc sweep call sites and cmd/pogod's exit
	// hook pass OwnerUnproven — and no arm of RemoveWorktree reads it, so
	// passing it would change nothing if they did.
	//
	// This used to license destroying files that could not be read. It does
	// not any more: positive evidence of death turned out to be exactly the
	// evidence a recent write CONTRADICTS, and the veto built to catch that
	// contradiction could only expire rather than resolve it. See
	// RemoveWorktree.
	//
	// The tests that still name it are deliberate controls, not callers: they
	// feed the harshest input available so that re-introducing an ownership
	// arm goes red instead of silently landing.
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
//
// # STDOUT ONLY. stderr is not the porcelain list (mg-f4c0)
//
// This used to read git's COMBINED output, and `git status --porcelain` writes
// warnings to stderr while exiting 0 — an unreadable subdirectory is the common
// one. Every such warning was then counted as a dirty entry:
//
//	$ git -C ~/.pogo/polecats/ca397 status --porcelain 2>&1
//	warning: could not open directory 'code/species_7d75/noread/': Permission denied
//	?? code/candidate_adjudication_a397/
//
// gc reported that tree as "2 uncommitted changes (1 modified, 1 untracked)"
// when it holds one untracked path and a warning; the "modified" file it named
// was the warning text. The failure is worse than a wrong count, because the
// guard above acts on it: a tree whose ONLY status output is such a warning
// reads as dirty, is preserved, pins its branch, and is never reclaimed — a
// silent producer of exactly the retained-worktree population mg-f4c0 exists to
// consume. It also mis-splits the count that decides urgency, since a warning
// has no `??` prefix and so lands in the tracked-changes half.
//
// stderr is still captured, and still reported when git FAILS — that is where
// "which way did git break" comes from, and cannot-tell needs it. What it is
// not is part of the file list.
func WorktreeDirty(worktreeDir string) (bool, []string, error) {
	if worktreeDir == "" {
		return false, nil, fmt.Errorf("empty worktree path")
	}
	if _, err := os.Stat(worktreeDir); err != nil {
		return false, nil, fmt.Errorf("stat worktree %s: %w", worktreeDir, err)
	}
	cmd := exec.Command("git", "-C", worktreeDir, "status", "--porcelain")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return false, nil, fmt.Errorf("status %s: %w: %s",
			worktreeDir, err, strings.TrimSpace(stderr.String()))
	}
	var files []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return len(files) > 0, files, nil
}

// WorktreeBranch reports the branch checked out in worktreeDir.
//
// It exists for the preservation record (mg-d45b): a retained worktree keeps
// its branch checked out, so that branch cannot be deleted and nothing else can
// claim it — which makes the branch name the first thing a rescuer needs and
// the one field the record could not supply.
//
// A detached HEAD comes back as the literal "HEAD", which is git's own answer
// passed through rather than normalised away. A preserved worktree on a
// detached head is a real and materially worse situation — there is no branch
// name to hand anyone — and flattening it to "" would make it indistinguishable
// from a read that failed.
func WorktreeBranch(worktreeDir string) (string, error) {
	if worktreeDir == "" {
		return "", fmt.Errorf("empty worktree path")
	}
	out, err := git(worktreeDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		// git exited 0 and said nothing. Report it rather than returning an
		// empty name that a caller would record as though it were read.
		return "", fmt.Errorf("rev-parse %s: empty branch name", worktreeDir)
	}
	return branch, nil
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
// re-open gh #31. The discriminator chosen was OWNERSHIP, which is knowable
// independently of git.
//
// # That discriminator was WITHDRAWN too (gh #97). Cannot-tell now always refuses
//
// mg-4d45's OwnerGone arm reclaimed an unreadable tree on positive evidence
// that its owner was dead. gh #97 pushed on that arm twice and it did not hold
// either time; both attempts are recorded because the second failure is subtle
// enough to be re-invented.
//
// FIRST: a proposed DRAIN — reclaim once dead AND old AND previously refused —
// which would have bounded the pin refusing creates. Withdrawn, because age is
// not emptiness: a 30-day-old irreplaceable.go is exactly as unrecoverable as a
// 30-second-old one, and "delete it anyway after T" does not resolve the
// uncertainty, it waits out our patience with it.
//
// SECOND: an mtime VETO, refusing even under OwnerGone when the tree had been
// written to recently. That one was well motivated — death evidence CAN be
// wrong, and a recent write is evidence against it, so the contradiction
// resolved in favour of the files. It was withdrawn on what the expiry means:
//
//	A tree written 25 hours ago and one written 23 hours ago are in the same
//	state. The only thing that changed is our window. Collecting at T+24h is
//	not "we established it was safe" — it is "we stopped being able to see the
//	objection." Absence of evidence is not evidence of absence, one layer in
//	from where that sentence killed the drain. Expiry is not resolution.
//
// So the rule is now singular, and every special case is a consequence of it
// rather than a clause in it:
//
//	readable tree + concluded ticket  -> collect, as ever (the common case)
//	CANNOT-TELL                       -> REFUSE AND REPORT, always
//
// The cannot-enumerate case needs no separate rule: "we cannot list the files
// so we cannot judge their timestamps" is subsumed by "we cannot read the tree
// so we do not act on it." Ownership needs no rule either — see WorktreeOwner,
// which no longer discriminates anything.
//
// mtime keeps exactly one job: it is REPORTED beside the refusal, so a human
// deciding whether to reclaim has the signal (`untouched 30 days`). It
// authorises nothing and vetoes nothing, so it cannot be blind in a way that
// costs files — the worst it can do is print an unhelpful number.
//
// THE COST, stated rather than glossed: more pinned worktrees, and they never
// self-clear. A pinned tree also pins its branch (phase 2 keeps a branch that is
// still checked out). That is the price of the position and it is paid
// knowingly — a visible pin a human can clear beats an invisible deletion they
// cannot, and `pogo gc --apply --force` is the way out.
//
// An ABSENT directory is not cannot-tell: there are no files to protect, so
// removal proceeds and the registration still gets dropped.
//
// Dropping the registration is load-bearing, not incidental: it is what frees
// the polecat's branch for deletion (git refuses to delete a branch checked
// out in a worktree), which is why Sweep processes worktrees before branches.
// TestRemoveWorktreeFreesCheckedOutBranch guards it.
func RemoveWorktree(sourceRepo, worktreeDir string, _ WorktreeOwner) error {
	if worktreeDir == "" {
		return nil
	}
	if chk := checkWorktreeRemoval(worktreeDir); chk.Refusal != nil {
		return chk.Refusal
	}
	return removeWorktreeFn(sourceRepo, worktreeDir)
}

// RemoveWorktreeForce removes a worktree regardless of uncommitted work. It is
// the deliberate override behind RemoveWorktree's refusal, and the escape
// hatch that keeps preservation from becoming an unbounded leak: without a way
// to reclaim a dirty tree, a refused worktree would pin its branch forever.
//
// # Who calls it, checked rather than remembered (gh #97)
//
// Two callers, and both are legitimate:
//
//   - RemoveWorktree itself, as its executor — reached only once the guard has
//     cleared the removal, or when the directory is already absent. That is not
//     an override; it is the doing half of a decision made above it.
//   - an operator who asked for it explicitly (`pogo gc --apply --force`),
//     which the GC sweep routes straight here.
//
// This comment used also to name internal/agent's failed-spawn cleanup. That
// was stale in both directions: cleanupFailedPolecatSpawn inlines its own `git
// worktree remove --force` plus os.RemoveAll and does not call this at all,
// while the sweep — which the comment did not name — used to call it directly,
// AROUND the guard, which is the defect gh #97 reports. A documented mechanism
// that does not exist is how the next reader concludes a call site is
// legitimate, so the list is now the checked one.
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
