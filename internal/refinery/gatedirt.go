package refinery

import (
	"fmt"
	"log"
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

	if len(e.BranchPaths) == 0 {
		b.WriteString(" This is NOT your change: none of those paths are touched by the submitted branch.")
	} else {
		fmt.Fprintf(&b, " The submitted branch also modifies %s, so the gate may be rewriting output your change alters.",
			strings.Join(e.BranchPaths, ", "))
	}

	b.WriteString(" Nothing in the author's worktree needs committing or stashing, and doing so cannot fix this" +
		" — fix the gate so it does not write tracked files (or have it restore them), or add the paths to .gitignore.")

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
func (r *Refinery) classifyGateDirt(wtDir string, mr *MergeRequest, stage, gitOutput string) *gateDirtError {
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
		e.BranchPaths = intersect(dirty, branchTouchedPaths(wtDir, mr))
	}
	return e
}

// branchTouchedPaths lists the paths the submitted branch changes relative to
// its merge base with the target. Best-effort: an unanswerable probe returns
// nil, which reads as "the branch touches none of the dirty paths" — the
// conservative direction, since the message then declines to blame the author.
func branchTouchedPaths(wtDir string, mr *MergeRequest) []string {
	out, err := gitCmdOutput(wtDir, "diff", "--name-only", "origin/"+mr.TargetRef+"...HEAD")
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
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
