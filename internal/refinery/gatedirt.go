package refinery

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Gate side-effects on the refinery's own checkout.
//
// A quality gate is an arbitrary command run inside the refinery's private
// clone. Nothing stops it writing tracked files — a regenerated record, a
// lockfile, a coverage report, a checked-in generated fixture — and plenty of
// real gates do. The refinery then has to move that same checkout through
// `rebase`, `checkout <target>` and `merge --ff-only`, and git refuses all
// three on a tree with unstaged changes.
//
// Before mg-393f the failure surfaced as git's own message:
//
//	cannot rebase: You have unstaged changes.
//	Please commit or stash them.
//
// which is wrong twice over. It names a worktree the author cannot see (the
// refinery's clone, not their polecat worktree), and its advice is actively
// dangerous when followed by a coordinator guessing at whose changes those
// are: stashing "them" can stash the real diff and merge an empty change.
// The author has no reachable fix at all — they never wrote those bytes.
//
// The fix has two halves:
//
//   - discardGateSideEffects resets the tracked tree at the points where the
//     pipeline is about to need it clean (attempt entry, and after gates and
//     before the target checkout). Everything the merge needs comes from
//     origin, so discarding tracked modifications in this clone cannot lose
//     work. Untracked files are deliberately left alone: they do not block
//     git, and they are where gates keep build caches.
//   - gateDirtError re-reports a dirty-tree git failure that survives anyway,
//     naming the gates as the writer and the paths they wrote, and saying
//     nothing about committing or stashing.
//
// The detector sits DOWNSTREAM of every git step that can itself dirty the
// tree, and a dirty tree on its own does not say who wrote it. A CONFLICTED
// rebase leaves exactly the same evidence: modified tracked files, in the
// refinery's clone, at a step that just failed. Read naively, that is a gate
// write — and mg-eac0 is the case where it was read that way. The refinery
// reported "the quality gate modified 6 tracked files ... none of those paths
// are touched by the submitted branch" while quoting its own captured
// "CONFLICT (content)" two paragraphs below, about a branch whose sole commit
// touched those six paths and nothing else. Two instruments in one report,
// disagreeing.
//
// So the writer must be established, not assumed:
//
//   - conflictOwnsTheDirt suppresses this whole error when the failing step
//     reports a conflict or leaves rebase/merge state behind. A conflict
//     explains the dirt completely, and it is a fact about the BRANCH — it
//     must reach classifyFailure's conflict table and come out ClassDefect,
//     not ClassInfrastructure's "establishes nothing, resubmit", which loops
//     forever on a deterministic conflict.
//   - branchTouchedPaths asks origin about the SUBMITTED BRANCH rather than
//     diffing the worktree HEAD, which is not the branch tip mid-rebase, and
//     says so when it cannot answer instead of returning "touches nothing".
//   - the .gitignore suggestion is withheld for any path the branch modifies.
//     Followed, it untracks the production source the author is trying to
//     land.

// dirtyTrackedPaths returns the tracked paths with uncommitted modifications in
// dir, sorted. Untracked files are excluded — they do not block rebase,
// checkout or ff-merge, and clearing them would delete gate build caches.
func dirtyTrackedPaths(dir string) ([]string, error) {
	// core.quotepath=false keeps non-ASCII paths readable instead of
	// backslash-escaped, so a path we print is a path someone can look up.
	out, err := gitCmdOutput(dir, "-c", "core.quotepath=false", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return nil, fmt.Errorf("git status: %s: %w", out, err)
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		// Porcelain v1 is "XY <path>", with "XY orig -> new" for renames. The
		// status column is parsed by finding the first space rather than by
		// slicing a fixed offset: gitCmdOutput trims the whole output, which
		// eats the leading space of an unstaged-only status (" M path") on the
		// first line and shifts every fixed offset by one.
		l := strings.TrimLeft(line, " ")
		i := strings.Index(l, " ")
		if i < 0 {
			continue
		}
		entry := strings.TrimSpace(l[i+1:])
		// A rename's post-rename name is the one that exists in the tree.
		if j := strings.Index(entry, " -> "); j >= 0 {
			entry = strings.TrimSpace(entry[j+4:])
		}
		entry = strings.Trim(entry, `"`)
		if entry != "" {
			paths = append(paths, entry)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// discardGateSideEffects makes the tracked tree in wtDir clean so the next git
// step cannot fail on gate output, and returns the paths it discarded.
//
// A nil/empty return means the tree was already clean, which is the common
// case: this is a no-op on every repo whose gates have no write side-effects.
// A status probe that itself fails is reported, because a clone we cannot read
// the state of is not one to keep pushing commits through.
func discardGateSideEffects(wtDir string) ([]string, error) {
	dirty, err := dirtyTrackedPaths(wtDir)
	if err != nil {
		return nil, err
	}
	if len(dirty) == 0 {
		return nil, nil
	}
	if out, rerr := gitCmdOutput(wtDir, "reset", "--hard", "HEAD"); rerr != nil {
		return dirty, fmt.Errorf("discard gate side-effects on %s: %s: %w", strings.Join(dirty, ", "), out, rerr)
	}
	return dirty, nil
}

// discardGateSideEffectsAt is discardGateSideEffects with the pipeline logging
// the refinery emits everywhere else. phase names the step that was about to
// run, so a log reader can tell "debris inherited from a previous attempt"
// from "this attempt's gate just wrote these".
//
// Returns the discarded paths. A failure to discard is logged and swallowed:
// the following git step will fail on its own and gateDirtError turns that
// into the message this exists to produce, which is more informative than an
// error raised from the cleanup itself.
func (r *Refinery) discardGateSideEffectsAt(wtDir string, mr *MergeRequest, attempt int, phase string) []string {
	discarded, err := discardGateSideEffects(wtDir)
	id := "?"
	if mr != nil {
		id = mr.ID
	}
	if err != nil {
		log.Printf("refinery: MR %s step=discard-gate-writes phase=%s attempt=%d FAILED: %v", id, phase, attempt, err)
		return discarded
	}
	if len(discarded) > 0 {
		log.Printf("refinery: MR %s step=discard-gate-writes phase=%s attempt=%d discarded %s written in the refinery's checkout: %s",
			id, phase, attempt, plural(len(discarded), "tracked path"), strings.Join(discarded, ", "))
	}
	return discarded
}

// gateDirtError reports a git step that refused to run because the refinery's
// own checkout had uncommitted changes.
//
// It exists to replace git's advice, not to add to it. git tells the reader to
// commit or stash, which is correct for a human in their own worktree and
// wrong for everyone who reads this: the dirty paths are in a clone private to
// the refinery, and were written by the gate that just ran. The author has
// nothing to commit and must not be told to stash.
type gateDirtError struct {
	Stage       string   // git step that refused: "rebase", "checkout target", "merge --ff-only"
	GitOutput   string   // what git said; quoted with its advice lines redacted
	DirtyPaths  []string // tracked paths dirty in the refinery's checkout
	BranchPaths []string // subset of DirtyPaths the branch itself also modifies
	Gates       []string // gate commands configured for this repo
	WorktreeDir string   // the refinery's clone — NOT the author's worktree
	// BranchPathsKnown records whether the branch's file list was actually
	// read. Without it, "the probe failed" and "the branch touches none of
	// these" are the same empty slice, and the message asserted the second
	// from the first — the false sentence in mg-eac0.
	BranchPathsKnown bool
}

func (e *gateDirtError) Error() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s refused: the quality gate modified %s in the refinery's own checkout (%s): %s.",
		e.Stage, plural(len(e.DirtyPaths), "tracked file"), e.WorktreeDir, strings.Join(e.DirtyPaths, ", "))

	// Naming the writer is the whole point — a reader who does not know which
	// gate writes has to reconstruct it from timestamps, which is what this
	// failure cost once already.
	if len(e.Gates) > 0 {
		quoted := make([]string, 0, len(e.Gates))
		for _, g := range e.Gates {
			quoted = append(quoted, fmt.Sprintf("%q", g))
		}
		fmt.Fprintf(&b, " The gate for this repo is %s; one of those commands wrote these paths.", strings.Join(quoted, ", "))
	}

	// Three cases, because two of them used to be one. "The branch touches
	// none of these" is a claim, and it is only sayable when the branch's own
	// file list was actually read; when the probe could not answer, the
	// message says that rather than asserting the negative it cannot support.
	switch {
	case len(e.BranchPaths) > 0:
		fmt.Fprintf(&b, " The submitted branch also modifies %s, so the gate may be rewriting output your change alters.",
			strings.Join(e.BranchPaths, ", "))
	case e.BranchPathsKnown:
		b.WriteString(" This is NOT your change: none of those paths are touched by the submitted branch.")
	default:
		b.WriteString(" Whether the submitted branch touches these paths could not be determined" +
			" — the branch's own file list could not be read from origin, so this does not clear the branch or implicate it.")
	}

	b.WriteString(" Nothing in the author's worktree needs committing or stashing, and doing so cannot fix this" +
		" — fix the gate so it does not write tracked files (or have it restore them)")
	// The .gitignore suggestion is withheld the moment any dirty path belongs
	// to the branch, and when ownership is unknown. Untracking a path the
	// branch modifies deletes production source from version control, and the
	// paths it would name are exactly the ones the author is trying to land.
	if len(e.BranchPaths) == 0 && e.BranchPathsKnown {
		b.WriteString(", or add the paths to .gitignore")
	}
	b.WriteString(".")

	if out := redactGitDirtAdvice(e.GitOutput); out != "" {
		fmt.Fprintf(&b, "\n\ngit said (its advice lines removed — they address the wrong worktree):\n%s", out)
	}
	return b.String()
}

// redactGitDirtAdvice drops the lines of git's dirty-tree complaint that tell
// the reader to commit or stash, keeping the rest — which includes the file
// list, the most useful part.
//
// Quoting git verbatim would reintroduce the exact instruction this error
// exists to withdraw. "Please commit or stash them" applied to a gate's output
// by someone guessing whose changes they are can stash the real diff and merge
// an empty change; a coordinator followed it once already. The lines are
// removed rather than rebutted so a skim cannot pick up the wrong half.
func redactGitDirtAdvice(gitOutput string) string {
	var kept []string
	for _, line := range strings.Split(gitOutput, "\n") {
		l := strings.ToLower(line)
		switch {
		// Matched on the advice itself rather than on a whole sentence: git
		// words this at least four ways across steps and versions ("Please
		// commit or stash them.", "Please commit your changes or stash them
		// before you switch branches.", "... before you merge.", and an older
		// form with a comma after "Please"). Matching the fragments survives
		// the next rewording.
		case strings.Contains(l, "commit or stash"),
			strings.Contains(l, "commit your changes"),
			strings.Contains(l, "stash them"),
			strings.Contains(l, "git stash"),
			strings.HasPrefix(strings.TrimSpace(l), "hint:"):
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// classifyGateDirt turns a failed git step into a gateDirtError when the
// refinery's checkout is dirty, and returns nil when it is not.
//
// The decision is made from the tree, not from git's wording: the message text
// varies by git version and by step ("cannot rebase: You have unstaged
// changes", "Your local changes to the following files would be overwritten
// by checkout", "Your local changes ... would be overwritten by merge"). If
// tracked files are dirty in a clone only the refinery writes to, the gate is
// the writer whatever git chose to say.
// The one thing it must NOT do is make that decision on a tree the failing
// step itself dirtied — see conflictOwnsTheDirt.
func (r *Refinery) classifyGateDirt(wtDir string, mr *MergeRequest, stage, gitOutput string) *gateDirtError {
	if conflictOwnsTheDirt(wtDir, gitOutput) {
		id := "?"
		if mr != nil {
			id = mr.ID
		}
		log.Printf("refinery: MR %s step=%s gate-dirt SUPPRESSED: the tree is dirty because this step conflicted, not because a gate wrote — classifying as a branch failure", id, stage)
		return nil
	}
	dirty, err := dirtyTrackedPaths(wtDir)
	if err != nil || len(dirty) == 0 {
		return nil
	}
	e := &gateDirtError{
		Stage:       stage,
		GitOutput:   gitOutput,
		DirtyPaths:  dirty,
		WorktreeDir: wtDir,
	}
	if mr != nil {
		e.Gates = r.loadGateConfig(wtDir, mr.RepoPath)
		branch, known := branchTouchedPaths(wtDir, mr)
		e.BranchPaths = intersect(dirty, branch)
		e.BranchPathsKnown = known
	}
	return e
}

// conflictOwnsTheDirt reports whether the failing git step is itself the reason
// the tracked tree is modified, which makes the dirt useless as evidence about
// the gate.
//
// Two independent readings, because either alone can be absent. The text
// reading uses the same conflict vocabulary classifyFailure matches on, so the
// two cannot drift into disagreeing about what a conflict is — one report
// containing both "CONFLICT (content)" and "failed(infrastructure)" is the bug
// this function exists to make unrepresentable. The state reading catches a
// conflict whose wording we do not recognise: a rebase or merge that left its
// state directory behind is unfinished, and every modification in the tree
// belongs to it.
//
// Deliberately NOT symmetric: a clean-worded, state-free failure on a dirty
// tree is still reported as gate dirt. The refinery discards gate writes at
// attempt entry and again after the gates, so a tree that is dirty with no
// conflict to explain it is the case gateDirtError was written for.
func conflictOwnsTheDirt(wtDir, gitOutput string) bool {
	return outputReportsConflict(gitOutput) || mergeStateInProgress(wtDir)
}

// mergeStateInProgress reports whether wtDir is sitting in an unfinished
// rebase, merge, cherry-pick or revert. Asked of git rather than by stat'ing
// .git/ so it stays correct in a linked worktree, where the state lives under
// the common dir and .git is a file.
func mergeStateInProgress(wtDir string) bool {
	for _, name := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		p, err := gitCmdOutput(wtDir, "rev-parse", "--git-path", name)
		if err != nil {
			continue
		}
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(wtDir, p)
		}
		if _, statErr := os.Stat(p); statErr == nil {
			return true
		}
	}
	return false
}

// branchTouchedPaths lists the paths the submitted branch changes relative to
// its merge base with the target, and reports whether the probe could answer
// at all.
//
// Both refs are named explicitly, and neither is HEAD. The old probe diffed
// `origin/<target>...HEAD`, which is only the submitted branch when the
// worktree happens to be sitting on its tip — and the moment this function
// matters most, it is not: a conflicted rebase leaves HEAD detached at the
// target with the branch's commits not yet applied, so the diff comes back
// EMPTY and the caller printed "none of those paths are touched by the
// submitted branch" about a branch that touched every one of them (mg-eac0).
// origin/<branch> is the thing that was submitted, whatever the worktree is
// doing.
//
// The bool is the other half. A probe that could not answer used to be
// indistinguishable from a branch that touches nothing, and the caller turned
// that silence into an assertion; now it says it does not know.
func branchTouchedPaths(wtDir string, mr *MergeRequest) ([]string, bool) {
	out, err := gitCmdOutput(wtDir, "diff", "--name-only",
		"origin/"+mr.TargetRef+"...origin/"+mr.Branch)
	if err != nil {
		return nil, false
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, true
}

// intersect returns the members of a that also appear in b, order preserved.
func intersect(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	var both []string
	for _, s := range a {
		if _, ok := set[s]; ok {
			both = append(both, s)
		}
	}
	return both
}

// gateWriteNote is the line appended to an MR's gate output when the gate left
// tracked files modified. The merge still succeeds — the point is that the
// record says the gate writes, so the next person to see a dirty-tree failure
// in this repo does not have to derive it from timestamps.
func gateWriteNote(discarded []string) string {
	if len(discarded) == 0 {
		return ""
	}
	return fmt.Sprintf("\nNOTE: the gate modified %s in the refinery's checkout and the changes were discarded "+
		"before merging (they are gate output, not part of the branch): %s\n",
		plural(len(discarded), "tracked file"), strings.Join(discarded, ", "))
}
