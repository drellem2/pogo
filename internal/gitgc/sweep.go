package gitgc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Options configures a single GC sweep.
type Options struct {
	// Repo is the git repository to sweep (the source repo whose .git
	// holds the polecat branches and worktree registrations).
	Repo string
	// TargetBranch is the merge target a *done* ticket's branch must be
	// merged into before deletion. Empty defaults to DefaultTargetBranch.
	TargetBranch string
	// LivePolecats is the do-not-touch set, keyed by polecat name (which
	// equals a branch's "polecat-" suffix and its worktree basename).
	// pogod fills this from its agent registry so a sweep never disturbs a
	// running polecat — even one whose worktree was unlinked at refinery
	// submit time and so is no longer git-checked-out.
	//
	// A NAME is not by itself an answer to "is this worktree in use" — see
	// LiveWorktrees. It still answers "is this BRANCH a live polecat's own
	// branch", and it names the worktree directory a live polecat owns, so
	// both uses survive.
	LivePolecats map[string]bool
	// LiveWorktrees is the do-not-touch set keyed by worktree PATH — the
	// directory a live agent is actually working in, as recorded at spawn
	// time (Agent.WorktreeDir). It is the authoritative in-use signal, and
	// the one that matters: a polecat's worktree belongs to the polecat, not
	// to whatever branch happens to be checked out inside it.
	//
	// # Why a path and not a branch name (mg-3b7c)
	//
	// The sweep used to decide a worktree was in use by taking its
	// CHECKED-OUT BRANCH, stripping "polecat-", and looking the remainder up
	// in LivePolecats. That silently assumed every polecat stays on its own
	// polecat-<name> branch for its whole life. It does not, and cannot: a
	// polecat dispatched to fix or rebase an EXISTING pull request must check
	// out that PR's head branch, because there is no other way to update a PR
	// in place. The moment it did, its worktree resolved to the OTHER
	// polecat's name, missed the live set, inherited that other ticket's
	// concluded state, and was removed — with the branch ref — out from under
	// a running agent. On 2026-07-29 that removed polecat caa65's worktree
	// five minutes after it was created; the work survived only because the
	// commit was still loose in the shared object store, which a
	// `--prune=now` would have reaped.
	//
	// Ownership is a property of the directory, so it is checked against the
	// directory. Paths are compared after symlink resolution, since git
	// canonicalizes worktree paths and /var vs /private/var would otherwise
	// read as two different trees on macOS.
	//
	// Empty is not "nothing is live": the name-derived fallbacks in
	// liveWorktreePaths still apply, and callers that supply neither get the
	// conservative behaviour of keeping anything they cannot classify.
	LiveWorktrees map[string]bool
	// Tickets, when non-nil, supplies work-item states directly. When nil,
	// Sweep loads them via LoadTicketIndex (`mg list`). Injecting a map
	// keeps Sweep unit-testable without the mg binary.
	Tickets TicketIndex
	// PolecatsDir, when set, is additionally scanned for orphan polecat
	// directories — dirs whose git worktree registration is gone but whose
	// files were never deleted, e.g. because pogod died before the polecat's
	// exit cleanup ran (gh #31). Such dirs are invisible to `git worktree
	// list`, so the worktree scan alone can never reclaim them. Empty means
	// skip the scan.
	//
	// New orphans of this shape stopped accruing when the submit-time unlink
	// was deleted (gh #88) — that hook was what stripped the registration
	// from a live polecat in the first place. The scan stays for the legacy
	// dirs it left behind, and for the pogod-died-mid-polecat case.
	PolecatsDir string
	// DryRun reports what would be done without deleting anything.
	DryRun bool
	// Force reclaims worktrees holding uncommitted work. Off by default: a
	// dirty worktree is KEPT and reported, because a concluded ticket is not
	// proof that everything in the tree made it into the merge (mg-ee02).
	// This is the operator's deliberate escape hatch — without it, a
	// preserved tree pins its branch indefinitely.
	Force bool
	// Logf, when set, receives a line per action for progress logging.
	Logf func(format string, args ...any)
}

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// canonicalPaths returns the spellings of p that a comparison should accept:
// the cleaned path, plus its symlink-resolved form when that differs and the
// path still exists. Both are kept rather than picking one, because the two
// sides of a comparison resolve differently — git canonicalizes the worktree
// paths it reports, while a path assembled from PolecatsDir has not been — and
// on macOS ~/.pogo under /var vs /private/var is exactly that mismatch.
func canonicalPaths(p string) []string {
	if p == "" {
		return nil
	}
	clean := filepath.Clean(p)
	out := []string{clean}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		if resolved = filepath.Clean(resolved); resolved != clean {
			out = append(out, resolved)
		}
	}
	return out
}

// liveWorktreePaths returns every worktree directory a live agent owns, in all
// spellings a comparison might see. It unions two sources because neither is
// complete alone:
//
//   - Options.LiveWorktrees — the paths pogod read straight off its agent
//     registry (Agent.WorktreeDir). Authoritative, but empty for a polecat
//     that outlived the pogod which spawned it: the registry has no reattach
//     path, so a restart leaves only the on-disk witness, which records names
//     and not paths.
//   - <PolecatsDir>/<name> for each live NAME — the convention every polecat
//     worktree is created under (internal/agent calls DefaultPolecatsDir and
//     joins the polecat's name; that function's doc names itself the single
//     source of truth for the location). This is what covers the
//     witness-only survivor, and it is why the fix does not depend on a
//     pogod restart to take effect.
func (o Options) liveWorktreePaths() map[string]bool {
	set := map[string]bool{}
	add := func(p string) {
		for _, c := range canonicalPaths(p) {
			set[c] = true
		}
	}
	for p := range o.LiveWorktrees {
		add(p)
	}
	if o.PolecatsDir != "" {
		for name := range o.LivePolecats {
			add(filepath.Join(o.PolecatsDir, name))
		}
	}
	return set
}

// polecatDirOwner returns the name of the polecat that owns worktreeDir, when
// that is knowable: a directory sitting directly under polecatsDir is a
// polecat worktree and its basename is the polecat's name, because that is
// precisely how internal/agent builds the path at spawn. Anywhere else the
// answer is ("", false) — there is no naming convention to read an owner out
// of, and guessing one would be worse than admitting ignorance.
func polecatDirOwner(polecatsDir, worktreeDir string) (string, bool) {
	if polecatsDir == "" || worktreeDir == "" {
		return "", false
	}
	name := filepath.Base(filepath.Clean(worktreeDir))
	for _, root := range canonicalPaths(polecatsDir) {
		for _, wt := range canonicalPaths(worktreeDir) {
			if filepath.Dir(wt) == root {
				return name, true
			}
		}
	}
	return "", false
}

// ownedByLive reports whether a live agent owns worktreeDir. The question is
// asked of the DIRECTORY and answered without ever consulting the branch
// checked out inside it — that conflation is the mg-3b7c defect.
//
// The basename fallback is deliberate belt-and-braces: a polecat's worktree
// directory is named for the polecat by construction, so a live name matching
// the directory's basename is direct evidence of ownership even when neither
// LiveWorktrees nor PolecatsDir was supplied (the `pogo gc` CLI path, where
// the live set comes from pogod's HTTP agent list).
func (o Options) ownedByLive(livePaths map[string]bool, worktreeDir string) bool {
	if worktreeDir == "" {
		return false
	}
	for _, c := range canonicalPaths(worktreeDir) {
		if livePaths[c] {
			return true
		}
	}
	return o.LivePolecats[filepath.Base(filepath.Clean(worktreeDir))]
}

// BranchAction records the GC decision for one polecat branch.
type BranchAction struct {
	Branch string
	ID     string // resolved work-item ID; "" when unknown
	State  TicketState
	Reason string
}

// WorktreeAction records the GC decision for one polecat worktree.
type WorktreeAction struct {
	Path   string
	Branch string
	Reason string
}

// Result is the outcome of a sweep.
type Result struct {
	Repo             string
	DryRun           bool
	BranchesDeleted  []BranchAction
	BranchesKept     []BranchAction
	WorktreesRemoved []WorktreeAction
	WorktreesKept    []WorktreeAction
	PruneOutput      string
	Errors           []string
}

// Sweep runs one GC pass over opts.Repo:
//
//  1. Remove worktrees of concluded, non-live polecats, then `git worktree
//     prune` any registration whose directory has vanished.
//  2. Delete `polecat-*` branches whose ticket is concluded — archived
//     unconditionally, done only once merged into the target branch —
//     skipping any branch that is live or still checked out.
//
// Worktrees are handled before branches so that removing a worktree frees
// its branch for deletion in the same pass. Sweep is conservative: an
// unresolvable ticket, an in-flight ticket, or a live polecat is always
// kept. Errors on individual items are collected into Result.Errors and do
// not abort the sweep.
func Sweep(opts Options) (Result, error) {
	if opts.TargetBranch == "" {
		opts.TargetBranch = DefaultTargetBranch
	}
	tickets := opts.Tickets
	if tickets == nil {
		loaded, err := LoadTicketIndex()
		if err != nil {
			return Result{}, fmt.Errorf("load ticket index: %w", err)
		}
		tickets = loaded
	}

	res := Result{Repo: opts.Repo, DryRun: opts.DryRun}

	// --- Phase 1: worktrees ---------------------------------------------
	worktrees, err := ListWorktrees(opts.Repo)
	if err != nil {
		return res, fmt.Errorf("list worktrees: %w", err)
	}

	// Branches whose worktree was removed this pass — they become
	// un-checked-out and thus deletable in phase 2.
	freed := map[string]bool{}
	// Branches held by a worktree a live agent owns, mapped to that worktree.
	// Phase 2 refuses to delete these and says whose tree is holding them.
	heldByLive := map[string]string{}

	livePaths := opts.liveWorktreePaths()

	for _, wt := range worktrees {
		if wt.Main || wt.Bare {
			continue
		}
		// Ownership is decided FIRST and by PATH, before the branch is even
		// looked at. A live agent's worktree is off limits whatever it has
		// checked out — its own polecat branch, another polecat's PR head, or
		// a plain upstream branch (mg-3b7c).
		if opts.ownedByLive(livePaths, wt.Path) {
			reason := "live polecat owns this worktree"
			if wt.Branch != "" {
				heldByLive[wt.Branch] = wt.Path
				if BranchSuffix(wt.Branch) != filepath.Base(filepath.Clean(wt.Path)) {
					// Worth naming: this is precisely the shape that used to be
					// mistaken for an orphan.
					reason += " (working on branch " + wt.Branch + ")"
				}
			}
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: wt.Path, Branch: wt.Branch, Reason: reason,
			})
			continue
		}
		if !wt.IsPolecat() {
			continue
		}
		name := BranchSuffix(wt.Branch)
		if opts.LivePolecats[name] {
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: wt.Path, Branch: wt.Branch, Reason: "live polecat",
			})
			continue
		}
		// A branch that is not this directory's own is not evidence about this
		// directory. mg-3b7c is the liveness half of that conflation; this is
		// the other half — a polecat that ended on someone else's PR head
		// would otherwise have its tree reclaimed on the strength of THAT
		// ticket's conclusion, which says nothing about whether this tree is
		// finished with. Keep and report; a leaked directory is recoverable
		// and a deleted one is not.
		//
		// Only asked where the answer is knowable: under PolecatsDir a
		// worktree's basename IS its owning polecat's name (internal/agent
		// joins exactly that at spawn). Anywhere else there is no owner to
		// infer, so the branch remains the only classifier available.
		if owner, ok := polecatDirOwner(opts.PolecatsDir, wt.Path); ok && owner != name {
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: wt.Path, Branch: wt.Branch,
				Reason: fmt.Sprintf("worktree of polecat %s holds another polecat's branch %s — "+
					"that branch's ticket does not classify this tree", owner, wt.Branch),
			})
			opts.logf("kept worktree %s: holds foreign branch %s (owner %s)", wt.Path, wt.Branch, owner)
			continue
		}
		_, state := tickets.BranchState(wt.Branch)
		if !state.Concluded() {
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: wt.Path, Branch: wt.Branch, Reason: "ticket " + state.String(),
			})
			continue
		}
		// A concluded ticket says the WORK was accepted; it does not say the
		// tree is empty. Uncommitted files here are unmerged by definition,
		// so keep them and say so rather than GC-ing them away (mg-ee02).
		// Checked before the dry-run branch so a dry run reports the same
		// decision an apply would make.
		if !opts.Force {
			if isDirty, files, derr := WorktreeDirty(wt.Path); derr == nil && isDirty {
				res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
					Path: wt.Path, Branch: wt.Branch,
					Reason: fmt.Sprintf("%d uncommitted change(s) — rerun with --force to discard", len(files)),
				})
				opts.logf("kept worktree %s (%s): %d uncommitted change(s)", wt.Path, wt.Branch, len(files))
				continue
			}
		}
		action := WorktreeAction{Path: wt.Path, Branch: wt.Branch, Reason: "ticket " + state.String()}
		if opts.DryRun {
			opts.logf("would remove worktree %s (%s)", wt.Path, wt.Branch)
		} else {
			if err := RemoveWorktreeForce(opts.Repo, wt.Path); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("remove worktree %s: %v", wt.Path, err))
				continue
			}
			opts.logf("removed worktree %s (%s)", wt.Path, wt.Branch)
		}
		// Marked only once the removal has actually succeeded (or was a dry
		// run, where nothing was attempted). It used to be set before the
		// attempt, so a FAILED removal still told phase 2 the branch was free
		// — deleting the ref out from under a worktree that was still there.
		freed[wt.Branch] = true
		res.WorktreesRemoved = append(res.WorktreesRemoved, action)
	}

	// --- Phase 1b: orphan polecat dirs ------------------------------------
	// This shape of orphan dates from when a polecat's worktree was unlinked
	// from git at refinery-submit time (its .git pointer removed, its
	// registration pruned) so the branch stopped being "checked out" while the
	// polecat kept polling; that submit-time unlink was deleted (gh #88), so
	// new orphans of this shape no longer accrue. Normally the polecat's exit
	// cleanup RemoveAll's the directory, but if pogod dies first the dir
	// survives with no .git and no registration — orphaned files that the
	// worktree scan above can never see (gh #31). Scan
	// PolecatsDir for such dirs and rm -rf the concluded ones; there is no
	// registration left for `git worktree remove --force` to act on, so
	// RemoveAll is the whole job.
	if opts.PolecatsDir != "" {
		registered := map[string]bool{}
		for _, wt := range worktrees {
			registered[wt.Path] = true
		}
		sweepOrphanDirs(opts, tickets, registered, livePaths, &res)
	}

	// Drop registrations whose directory is already gone.
	if pruneOut, err := PruneWorktrees(opts.Repo, opts.DryRun); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("worktree prune: %v", err))
	} else {
		res.PruneOutput = pruneOut
	}

	// --- Phase 2: branches ----------------------------------------------
	branches, err := ListPolecatBranches(opts.Repo)
	if err != nil {
		return res, fmt.Errorf("list branches: %w", err)
	}
	checkedOut, err := CheckedOutBranches(opts.Repo)
	if err != nil {
		return res, fmt.Errorf("list checked-out branches: %w", err)
	}

	for _, br := range branches {
		name := BranchSuffix(br)
		id, state := tickets.BranchState(br)
		action := BranchAction{Branch: br, ID: id, State: state}

		if opts.LivePolecats[name] {
			action.Reason = "live polecat"
			res.BranchesKept = append(res.BranchesKept, action)
			continue
		}
		// A live agent is working in a tree that has this branch checked out.
		// The branch is not that agent's own polecat-<name> branch — that case
		// is caught above — which is exactly why this arm has to exist: a
		// polecat sent to fix an existing PR holds a branch that looks, by
		// name, like some other concluded polecat's (mg-3b7c).
		if wtPath, held := heldByLive[br]; held {
			action.Reason = "checked out in live polecat's worktree " + wtPath
			res.BranchesKept = append(res.BranchesKept, action)
			continue
		}
		// Never delete a branch that is checked out in a worktree that still
		// exists — git refuses, and we should not want to. `checkedOut` is
		// read AFTER phase 1, so a worktree successfully removed above is
		// already absent from it; `freed` supplies the same answer for a dry
		// run, where nothing was actually removed. Consulting `freed` only in
		// dry-run mode is what keeps a failed removal from licensing the
		// deletion of a still-checked-out ref.
		if checkedOut[br] && !(opts.DryRun && freed[br]) {
			action.Reason = "checked out in a worktree"
			res.BranchesKept = append(res.BranchesKept, action)
			continue
		}
		if !state.Concluded() {
			action.Reason = "ticket " + state.String()
			res.BranchesKept = append(res.BranchesKept, action)
			continue
		}
		// Done (but not archived) tickets must be merged before deletion;
		// archived tickets are deleted regardless — the work has concluded.
		if state == TicketDone {
			merged, err := BranchMerged(opts.Repo, br, opts.TargetBranch)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("merge check %s: %v", br, err))
				action.Reason = "merge check failed"
				res.BranchesKept = append(res.BranchesKept, action)
				continue
			}
			if !merged {
				action.Reason = "ticket done but branch not merged into " + opts.TargetBranch
				res.BranchesKept = append(res.BranchesKept, action)
				continue
			}
			action.Reason = "ticket done, merged"
		} else {
			action.Reason = "ticket archived"
		}

		if opts.DryRun {
			opts.logf("would delete branch %s (%s)", br, action.Reason)
		} else {
			if err := DeleteBranch(opts.Repo, br); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("delete branch %s: %v", br, err))
				continue
			}
			opts.logf("deleted branch %s (%s)", br, action.Reason)
		}
		res.BranchesDeleted = append(res.BranchesDeleted, action)
	}

	return res, nil
}

// sweepOrphanDirs scans opts.PolecatsDir for orphaned polecat directories
// and removes the eligible ones (see the phase 1b comment in Sweep).
// Eligibility mirrors the worktree phase: never a live polecat, never an
// in-flight or unclassifiable ticket. Removal is scoped strictly to
// directories directly under PolecatsDir whose basename resolves to a
// concluded work item; anything with a .git entry is still a linked
// worktree and is left to the registered-worktree scan of its owning repo.
func sweepOrphanDirs(opts Options, tickets TicketIndex, registered, livePaths map[string]bool, res *Result) {
	entries, err := os.ReadDir(opts.PolecatsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			res.Errors = append(res.Errors, fmt.Sprintf("read polecats dir %s: %v", opts.PolecatsDir, err))
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(opts.PolecatsDir, name)
		// A dir that is a registered worktree, or still carries a .git
		// pointer (possibly into a repo outside this sweep), is not an
		// orphan — the registered-worktree scan owns it.
		if registered[path] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			continue
		}
		branch := BranchPrefix + name
		if opts.ownedByLive(livePaths, path) {
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: path, Branch: branch, Reason: "orphan dir, live polecat",
			})
			continue
		}
		_, state := tickets.BranchState(branch)
		if !state.Concluded() {
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: path, Branch: branch, Reason: "orphan dir, ticket " + state.String(),
			})
			continue
		}
		action := WorktreeAction{Path: path, Branch: branch, Reason: "orphan dir, ticket " + state.String()}
		if opts.DryRun {
			opts.logf("would remove orphan dir %s (%s)", path, branch)
		} else {
			if err := os.RemoveAll(path); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("remove orphan dir %s: %v", path, err))
				continue
			}
			opts.logf("removed orphan dir %s (%s)", path, branch)
		}
		res.WorktreesRemoved = append(res.WorktreesRemoved, action)
	}
}

// Summary renders a human-readable multi-line report of a sweep result.
func (r Result) Summary() string {
	var b strings.Builder
	verb := "removed"
	if r.DryRun {
		verb = "would remove"
	}
	delVerb := "deleted"
	if r.DryRun {
		delVerb = "would delete"
	}
	fmt.Fprintf(&b, "git GC sweep of %s%s\n", r.Repo, dryRunTag(r.DryRun))
	fmt.Fprintf(&b, "  worktrees: %s %d, kept %d\n", verb, len(r.WorktreesRemoved), len(r.WorktreesKept))
	fmt.Fprintf(&b, "  branches:  %s %d, kept %d\n", delVerb, len(r.BranchesDeleted), len(r.BranchesKept))

	if len(r.WorktreesRemoved) > 0 {
		fmt.Fprintf(&b, "  worktrees %s:\n", verb)
		for _, w := range sortedWorktrees(r.WorktreesRemoved) {
			fmt.Fprintf(&b, "    %s (%s)\n", w.Path, w.Branch)
		}
	}
	if len(r.BranchesDeleted) > 0 {
		fmt.Fprintf(&b, "  branches %s:\n", delVerb)
		for _, br := range sortedBranches(r.BranchesDeleted) {
			fmt.Fprintf(&b, "    %s — %s\n", br.Branch, br.Reason)
		}
	}
	if r.PruneOutput != "" {
		fmt.Fprintf(&b, "  worktree prune: %s\n", strings.ReplaceAll(r.PruneOutput, "\n", "; "))
	}
	if len(r.Errors) > 0 {
		fmt.Fprintf(&b, "  errors (%d):\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "    %s\n", e)
		}
	}
	return b.String()
}

func dryRunTag(dry bool) string {
	if dry {
		return " (dry run)"
	}
	return ""
}

func sortedBranches(in []BranchAction) []BranchAction {
	out := append([]BranchAction(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Branch < out[j].Branch })
	return out
}

func sortedWorktrees(in []WorktreeAction) []WorktreeAction {
	out := append([]WorktreeAction(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
