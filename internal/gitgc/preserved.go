package gitgc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PreservedTree is one worktree that IS BEING RETAINED — kept rather than
// reclaimed because the removal guard refused it (a dirty tree, mg-ee02) or
// could not read it (mg-4d45).
//
// # It is a set of FACTS, and deliberately not a verdict (mg-f4c0)
//
// Every field here is something read off the tree. There is no "safe to
// reclaim", no "looks regenerable", no score — and the omission is the design,
// not a gap to fill in later.
//
// The evidence is specific. `~/.pogo/polecats/p687f` was preserved holding
// seven modified files, all of them `code/**/out_*.txt` — regenerated suite
// output, a pure function of repo state, reproducible in seconds. A reader
// sampled two of the seven, saw timing churn, and concluded "residue, safe to
// reclaim". The third file held three new registry entries and a count going
// 20 -> 23. A classifier over filenames, extensions, or diff shape would have
// reached that same wrong answer, and would have reached it SYSTEMATICALLY
// rather than once.
//
// So the question this record answers is "what is in this tree", and the
// question it refuses to answer is "may I delete it". The second one needs a
// human who read the files, and the whole cost this record removes is the cost
// of FINDING the trees and knowing which files to open — not the cost of
// deciding.
type PreservedTree struct {
	// Path is the worktree directory. Keyed `worktree` to match the
	// worktree_preserved event's detail key, so a listing row and a spine
	// event about the same tree join on the same field name.
	Path string `json:"worktree"`
	// Owner is the polecat the tree belongs to — the path basename, the only
	// sound key for "whose tree is this" (see PolecatNameForWorktree).
	Owner string `json:"owner"`
	// Repo is the repository this worktree is linked to, resolved from the
	// tree's own .git pointer rather than assumed. A listing that spans repos
	// has to carry it: the reclaim command is repo-scoped.
	Repo string `json:"repo,omitempty"`
	// RepoError is why Repo is empty, reported rather than omitted — the rule
	// emitWorktreePreserved applies to `branch_error`, for the same reason.
	RepoError string `json:"repo_error,omitempty"`

	Branch      string `json:"branch,omitempty"`
	BranchError string `json:"branch_error,omitempty"`

	// WorkItemID and TicketState are the owner's work item, resolved the way
	// the sweep resolves it (TicketIndex.OwnerState). Empty ID means the
	// directory name resolved to no work item at all.
	WorkItemID  string `json:"work_item_id,omitempty"`
	TicketState string `json:"ticket_state"`

	// Outcome is why the tree is retained, using worktree_preserved's
	// vocabulary exactly: "preserved" means uncommitted work was positively
	// read, "undetermined" means `git status` failed and we could not look.
	// Folding the second into the first would state a fact about the tree that
	// nobody established.
	Outcome string `json:"outcome"`

	// Total, Modified and Untracked are the dirty split, present only on the
	// "preserved" outcome — a count is meaningful only when the tree was
	// actually read. Untracked is the urgent half: such a path is on no
	// branch, in no stash and on no remote, so this tree is its only copy
	// anywhere on the machine.
	Total     int `json:"dirty_paths,omitempty"`
	Modified  int `json:"modified_paths,omitempty"`
	Untracked int `json:"untracked_paths,omitempty"`
	// Files is the FULL `git status --porcelain` list, never capped. The cap
	// belongs to the renderer; a capped record is a record that reproduces this
	// ticket's own defect — a partial read presented as a whole one.
	Files []string `json:"files,omitempty"`

	// StatusError is the `git status` failure on the "undetermined" outcome.
	StatusError string `json:"status_error,omitempty"`

	// UntouchedSeconds is how long the tree has gone unwritten, and
	// UntouchedKnown whether it could be established. It is a REPORT and
	// decides nothing — see UndeterminedWorktreeError.Untouched for why that
	// split is load-bearing rather than stylistic.
	UntouchedSeconds int  `json:"untouched_seconds,omitempty"`
	UntouchedKnown   bool `json:"untouched_known"`

	// Live is true when the OWNER is a running polecat. Such a tree is not
	// retained — it is in use — and it is reported separately so the headline
	// count is the population that needs an owner, not the population that has
	// one.
	Live bool `json:"live"`

	// ForceReclaims answers, for THIS tree, whether
	// `pogo gc --repo=<Repo> --apply --force` would actually take it: "yes",
	// "no", or "unknown" when the ticket index could not be loaded.
	//
	// It exists because --force is NOT the whole gate. The sweep checks the
	// owner's ticket state BEFORE it consults the dirty guard, so a retained
	// tree whose work item is still in flight survives --force untouched, and
	// an operator who reads --force as "reclaims everything listed" is wrong in
	// both directions.
	ForceReclaims string `json:"force_reclaims"`
}

// UntouchedText renders the tree's age as a bare phrase for a listing row.
//
// It is never empty — the rule untouchedClause applies to a refusal line, for
// the same reason: on a permanent retention the reader needs the age or an
// explicit statement that there isn't one, because a silently missing clause
// reads as "recent" to some people and "old" to others.
func (t PreservedTree) UntouchedText() string {
	if !t.UntouchedKnown {
		return "age unknown — the tree could not be listed"
	}
	return "untouched " + humanAge(time.Duration(t.UntouchedSeconds)*time.Second)
}

// UntrackedPaths returns the untracked entries with their porcelain code
// stripped — the paths that exist in this tree and nowhere else.
func (t PreservedTree) UntrackedPaths() []string {
	var out []string
	for _, line := range t.Files {
		if strings.HasPrefix(line, "??") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "??")))
		}
	}
	return out
}

// ModifiedEntries returns the tracked-change entries as raw porcelain lines,
// code included. The code is kept because M, A, D and R are different facts
// about what happens if the tree goes, and stripping it to a bare path would
// lose the one that matters most (D — a deletion staged but never committed).
func (t PreservedTree) ModifiedEntries() []string {
	var out []string
	for _, line := range t.Files {
		if !strings.HasPrefix(line, "??") {
			out = append(out, line)
		}
	}
	return out
}

// PreservedScanOptions configures ScanPreserved.
type PreservedScanOptions struct {
	// PolecatsDir is the directory to scan — $POGO_HOME/polecats in
	// production (DefaultPolecatsDir). Required.
	//
	// The scan is rooted HERE rather than at a repo, and that is the point.
	// Preserved trees accumulate across every repo the fleet works; the
	// population that matters is "all of them", and a repo-scoped listing
	// would report a fraction of it while looking complete.
	PolecatsDir string
	// Repo, when non-empty, filters the listing to trees linked to that
	// repository.
	Repo string
	// LivePolecats is the set of running polecat names, keyed the way the
	// sweep keys it (by owner). A tree whose owner is live is reported as in
	// use, not as retained.
	LivePolecats map[string]bool
	// Tickets, when non-nil, supplies work-item states directly. When nil the
	// scan loads them via LoadTicketIndex and DEGRADES rather than failing if
	// that does not work: an unavailable `mg` costs the ticket column, and the
	// tree listing — which is the part nothing else can produce — still lands.
	Tickets TicketIndex
}

// PreservedReport is the population of retained worktrees, plus what the scan
// skipped and why.
type PreservedReport struct {
	PolecatsDir string `json:"polecats_dir"`
	RepoFilter  string `json:"repo_filter,omitempty"`
	// Retained is the population this whole command exists to make visible:
	// trees nothing will reclaim on its own, each pinning a branch.
	Retained []PreservedTree `json:"retained"`
	// InUse are dirty trees whose owner is still running. Reported so a reader
	// can see the scan saw them and did not count them — a headline number
	// that quietly includes live agents' work is a wrong number.
	InUse []PreservedTree `json:"in_use"`
	// CleanCount and NotWorktreeCount account for everything else under
	// PolecatsDir, so the listing is a partition of the directory rather than
	// a selection out of it.
	CleanCount       int `json:"clean_count"`
	NotWorktreeCount int `json:"not_worktree_count"`
	// OtherRepoCount is how many trees a --repo filter excluded. Reported so a
	// narrowed listing still says how much of the directory it is not showing;
	// a filtered report that looks like a full one is the same failure as a
	// truncated file list that does not say it truncated.
	OtherRepoCount int `json:"other_repo_count,omitempty"`
	// TicketsLoaded is false when the work-item index could not be read, which
	// is what makes ForceReclaims "unknown".
	TicketsLoaded bool     `json:"tickets_loaded"`
	Notes         []string `json:"notes,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

// ScanPreserved enumerates the retained worktrees under opts.PolecatsDir.
//
// # The consumer that did not exist (mg-f4c0)
//
// pogod preserves a polecat's worktree when it exits holding uncommitted work,
// mails the coordinator about it, and puts a worktree_preserved event on the
// spine. All three halves work. What never existed is anything that reads the
// population BACK: the mail fires once into a busy inbox and is never repeated,
// the event is a stream rather than a standing list, and so the trees
// accumulate — six when this was filed, twenty-three when it was fixed — each
// one pinning a branch that cannot be deleted and posing a question ("is this
// uncommitted work worth rescuing?") that nobody was assigned to ask.
//
// This is the read side. It reclaims nothing, refuses nothing, and blocks
// nothing; reclaiming is one already-existing command and was never the hard
// part. Knowing WHICH of twenty-three trees can safely take it is, and that is
// a question about the files inside them.
//
// # It is not cheap, and that is deliberate
//
// Each retained tree is walked to establish its age (newestWrite), on top of a
// `git status` per directory. That makes this an operator command rather than
// something to put on a tick. The alternative — stat the root — was measured
// and is blind to a live agent editing a nested file, so it would report
// "untouched 30 days" for exactly the tree a reader must not reclaim.
func ScanPreserved(opts PreservedScanOptions) (PreservedReport, error) {
	if opts.PolecatsDir == "" {
		return PreservedReport{}, fmt.Errorf("scan preserved worktrees: no polecats dir")
	}
	rep := PreservedReport{PolecatsDir: opts.PolecatsDir, RepoFilter: opts.Repo}

	tickets := opts.Tickets
	rep.TicketsLoaded = tickets != nil
	if tickets == nil {
		loaded, err := LoadTicketIndex()
		if err != nil {
			// Degrade rather than fail. The listing's irreplaceable half is the
			// set of trees and the files in them; the ticket column is a
			// convenience that `mg show` can supply by hand.
			rep.Notes = append(rep.Notes, fmt.Sprintf(
				"work-item states unavailable (%v) — ticket state reads \"unknown\" for every "+
					"tree below, and whether `--force` would reclaim one cannot be computed.", err))
			tickets = TicketIndex{}
		} else {
			tickets = loaded
			rep.TicketsLoaded = true
		}
	}

	entries, err := os.ReadDir(opts.PolecatsDir)
	if err != nil {
		return rep, fmt.Errorf("read polecats dir %s: %w", opts.PolecatsDir, err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(opts.PolecatsDir, e.Name())
		// No .git entry at all means this is not a linked worktree — it is a
		// phase-1b orphan dir, which has no index and no HEAD, so
		// "uncommitted" is not a property it has. Counted, never listed:
		// putting it in a list of trees holding uncommitted work would be a
		// claim about it that nobody can make.
		if _, lerr := os.Lstat(filepath.Join(path, ".git")); lerr != nil {
			rep.NotWorktreeCount++
			continue
		}

		tree := PreservedTree{Path: path, Owner: e.Name()}
		if repo, rerr := WorktreeSourceRepo(path); rerr == nil {
			tree.Repo = repo
		} else {
			tree.RepoError = rerr.Error()
		}
		// The filter excludes only trees that RESOLVED to a different
		// repository. A tree whose .git pointer could not be read might belong
		// to this one, and dropping it would reproduce this ticket's own defect
		// at the reporting layer: a retained tree absent from the list of
		// retained trees, absent silently, and absent exactly in the case where
		// something was already wrong with it.
		if opts.Repo != "" && tree.Repo != "" && tree.Repo != opts.Repo {
			rep.OtherRepoCount++
			continue
		}

		// The same guard the sweep and the exit hook consult, called rather
		// than re-implemented, so the listing cannot claim a tree is retained
		// that gc would happily reap.
		chk := checkWorktreeRemoval(path)
		if chk.Refusal == nil {
			rep.CleanCount++
			continue
		}

		var dwe *DirtyWorktreeError
		var uwe *UndeterminedWorktreeError
		switch {
		case errors.As(chk.Refusal, &dwe):
			tree.Outcome = "preserved"
			// Re-read the full porcelain list: DirtyWorktreeError.Files is
			// capped at dirtyFileListCap for legibility in a log line, and a
			// listing whose whole purpose is "which files are in here" must not
			// inherit a cap set for a different medium.
			if _, files, ferr := WorktreeDirty(path); ferr == nil {
				tree.Files = files
			} else {
				tree.Files = dwe.Files
				rep.Errors = append(rep.Errors, fmt.Sprintf(
					"re-read status of %s for the full file list: %v (showing the capped list)", path, ferr))
			}
			tree.Total, tree.Modified, tree.Untracked = dwe.Total, dwe.Modified, dwe.Untracked
			// The age is measured here for dirty trees; the guard only computes
			// it on the cannot-read path, where the refusal line needs it.
			if newest, werr := newestWrite(path); werr == nil {
				tree.UntouchedSeconds = int(time.Since(newest).Seconds())
				tree.UntouchedKnown = true
			}
		case errors.As(chk.Refusal, &uwe):
			tree.Outcome = "undetermined"
			tree.StatusError = uwe.Err.Error()
			tree.UntouchedSeconds = int(uwe.Untouched.Seconds())
			tree.UntouchedKnown = uwe.UntouchedKnown
		default:
			// No third refusal exists today. Report it rather than dropping the
			// tree: a retained worktree missing from the list of retained
			// worktrees is this ticket's defect, one layer down.
			tree.Outcome = "retained"
			tree.StatusError = chk.Refusal.Error()
		}

		if branch, berr := WorktreeBranch(path); berr == nil {
			tree.Branch = branch
		} else {
			tree.BranchError = berr.Error()
		}

		id, state := tickets.OwnerState(tree.Owner)
		tree.WorkItemID, tree.TicketState = id, state.String()

		if opts.LivePolecats[tree.Owner] {
			tree.Live = true
			// A live owner's tree is never gc's to take, whatever --force says.
			tree.ForceReclaims = "no"
			rep.InUse = append(rep.InUse, tree)
			continue
		}
		tree.ForceReclaims = forceReclaims(rep.TicketsLoaded, tickets, tree.Owner, tree.Branch)
		rep.Retained = append(rep.Retained, tree)
	}

	sortPreserved(rep.Retained)
	sortPreserved(rep.InUse)
	return rep, nil
}

// forceReclaims answers whether `pogo gc --apply --force` would take this tree.
//
// IT IS NOT "YES BECAUSE --FORCE". The sweep tests the owner's ticket state
// BEFORE it reaches the dirty guard, so --force only ever overrides the guard —
// never the state check. A retained tree whose work item is still in flight
// survives --force untouched, and an operator who reads the flag as "reclaims
// everything in the list" is wrong about exactly those trees.
//
// The classification is classifyTree's, called rather than restated, so this
// column cannot drift from what the sweep will actually do.
func forceReclaims(ticketsLoaded bool, tickets TicketIndex, owner, branch string) string {
	if !ticketsLoaded {
		return "unknown"
	}
	if state, _ := classifyTree(tickets, owner, branch); state.Concluded() {
		return "yes"
	}
	return "no"
}

func sortPreserved(in []PreservedTree) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Repo != in[j].Repo {
			return in[i].Repo < in[j].Repo
		}
		return in[i].Path < in[j].Path
	})
}

// WorktreeSourceRepo reports the repository a linked worktree belongs to.
//
// It reads the tree's own `.git` POINTER FILE first and only falls back to
// running git. That order is deliberate: half the population this resolves for
// is retained precisely because `git status` failed there, and a resolver that
// needs a working git would be blind in exactly the case where the operator
// most needs to know which repo to point the reclaim at.
//
// A linked worktree's pointer reads `gitdir: <repo>/.git/worktrees/<name>`, so
// the repository is the text before `/.git/worktrees/`.
func WorktreeSourceRepo(worktreeDir string) (string, error) {
	if worktreeDir == "" {
		return "", fmt.Errorf("empty worktree path")
	}
	dotgit := filepath.Join(worktreeDir, ".git")
	b, err := os.ReadFile(dotgit)
	if err == nil {
		line := strings.TrimSpace(string(b))
		gitdir, ok := strings.CutPrefix(line, "gitdir:")
		if !ok {
			return "", fmt.Errorf("%s: not a worktree pointer (%q)", dotgit, firstLine(line))
		}
		gitdir = strings.TrimSpace(gitdir)
		const marker = "/.git/worktrees/"
		if i := strings.Index(gitdir, marker); i >= 0 {
			return gitdir[:i], nil
		}
		return "", fmt.Errorf("%s: gitdir %q is not a linked-worktree admin dir", dotgit, gitdir)
	}
	// .git is a directory (a main worktree, not a linked one) or unreadable.
	// Ask git, which answers for the first case and fails honestly for the
	// second.
	out, gerr := git(worktreeDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if gerr != nil {
		return "", fmt.Errorf("read %s: %v; and %v", dotgit, err, gerr)
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return "", fmt.Errorf("%s: git reported an empty common dir", worktreeDir)
	}
	return filepath.Dir(common), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// preservedModifiedCap bounds how many tracked-change entries one tree renders.
//
// UNTRACKED PATHS ARE NEVER CAPPED, and the asymmetry is the whole point. A
// modified tracked file has a committed version in the object store, so the
// worst case of not naming it is a lost edit that git can still describe; an
// untracked path exists on no branch, in no stash and on no remote, and a
// listing that truncates those is a listing that hides the only copy of
// something. Every capped section names its own overflow and the command that
// shows the rest, because an unannounced truncation is how a reader concludes
// they have seen the tree.
const preservedModifiedCap = 20

// Summary renders the report for an operator.
func (r PreservedReport) Summary() string {
	var b strings.Builder

	preserved, undetermined := 0, 0
	for _, t := range r.Retained {
		if t.Outcome == "undetermined" {
			undetermined++
		} else {
			preserved++
		}
	}

	fmt.Fprintf(&b, "retained polecat worktrees under %s\n", r.PolecatsDir)
	if r.RepoFilter != "" {
		fmt.Fprintf(&b, "  (filtered to repo %s — %d tree(s) in other repositories not shown;\n"+
			"   a tree whose .git pointer could not be read is shown anyway, since it may be this one)\n",
			r.RepoFilter, r.OtherRepoCount)
	}
	fmt.Fprintf(&b, "  %d retained: %d holding uncommitted work, %d unreadable\n",
		len(r.Retained), preserved, undetermined)
	fmt.Fprintf(&b, "  %d dirty tree(s) in use by a live polecat — not retained, listed at the end\n",
		len(r.InUse))
	fmt.Fprintf(&b, "  %d clean, %d not linked worktrees (no .git — see `pogo gc` orphan dirs)\n",
		r.CleanCount, r.NotWorktreeCount)

	for _, n := range r.Notes {
		fmt.Fprintf(&b, "\n  note: %s\n", n)
	}

	if len(r.Retained) == 0 {
		fmt.Fprintf(&b, "\nNothing is retained. Every polecat worktree here is clean or in use.\n")
	} else {
		b.WriteString(preservedPreamble)
		for _, group := range groupByRepo(r.Retained) {
			writeRepoGroup(&b, group)
		}
		b.WriteString(preservedFooter)
	}

	if len(r.InUse) > 0 {
		fmt.Fprintf(&b, "\nin use by a live polecat (NOT retained — do not touch):\n")
		for _, t := range r.InUse {
			fmt.Fprintf(&b, "  %s  owner %s, branch %s, %d uncommitted\n",
				t.Path, t.Owner, branchOrNone(t), t.Total)
		}
	}

	if len(r.Errors) > 0 {
		fmt.Fprintf(&b, "\nerrors (%d):\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "  %s\n", e)
		}
	}
	return b.String()
}

// preservedPreamble is the report's refusal to do the reader's job for them.
//
// It is not a disclaimer. It is the finding that produced this command: a
// preserved tree's contents cannot be judged from metadata, and the one
// recorded attempt to do so — seven files that all looked like regenerated
// suite output — was wrong on the third file, which held real content. Anything
// cheaper than opening the files reproduces that error, and reproduces it for
// every tree at once rather than for one.
const preservedPreamble = `
NOTHING BELOW IS A VERDICT. Every line is a fact read off the tree; none of them
says whether a tree may be reclaimed. That question needs someone to READ the
files. A tree of seven files that all looked like regenerated suite output was
judged "residue, safe to reclaim" from a sample of two; the third held real
authored content, and a cheap classifier would have made that mistake for every
tree here at once rather than for one.
`

const preservedFooter = `
For each tree above, the two honest outcomes are: rescue what matters (it is a
live git worktree — ` + "`git -C <path> status`, `git -C <path> diff`" + `, and an
untracked file can be committed onto its branch or copied out), or reclaim it
deliberately. Keeping it forever is the third one, and it is what produced this
list.
`

// writeRepoGroup renders one repository's retained trees, headed by what the
// reclaim command for that repo would actually do.
//
// THE HEADER IS THE POINT OF THE GROUPING. `pogo gc --repo=<repo> --apply
// --force` is repo-scoped and forced: it acts on every eligible retained tree
// in that repository, not on the one the operator just inspected. A reader who
// takes the command out of a per-tree preservation notice — which is where it
// appears — has no way to see that from the notice. Grouping puts the blast
// radius above the trees it covers, and names the count.
func writeRepoGroup(b *strings.Builder, g repoGroup) {
	repo := g.Repo
	if repo == "" {
		fmt.Fprintf(b, "\nrepository UNRESOLVED (the tree's .git pointer could not be read)\n")
	} else {
		fmt.Fprintf(b, "\n%s\n", repo)
	}

	var eligible, held, unknown []string
	for _, t := range g.Trees {
		switch t.ForceReclaims {
		case "yes":
			eligible = append(eligible, t.Owner)
		case "unknown":
			unknown = append(unknown, t.Owner)
		default:
			held = append(held, t.Owner)
		}
	}
	if repo != "" {
		fmt.Fprintf(b, "  reclaiming ANY of these is `pogo gc --repo=%s --apply --force`,\n", repo)
		fmt.Fprintf(b, "  which is repo-scoped and forced — it takes ALL %d eligible tree(s) here at once,\n",
			len(eligible))
		fmt.Fprintf(b, "  not the one you inspected, and it DISCARDS whatever is still uncommitted.\n")
		if len(eligible) > 0 {
			fmt.Fprintf(b, "    it would reclaim: %s\n", strings.Join(eligible, ", "))
		}
		if len(held) > 0 {
			// Stated because --force reads as "everything". It is not: the
			// sweep checks the owner's ticket state first, so an unconcluded
			// item's tree survives the flag entirely.
			fmt.Fprintf(b, "    it would NOT touch (work item not concluded): %s\n", strings.Join(held, ", "))
		}
		if len(unknown) > 0 {
			fmt.Fprintf(b, "    unknown, work-item states could not be read: %s\n", strings.Join(unknown, ", "))
		}
	}

	for _, t := range g.Trees {
		writeTree(b, t)
	}
}

func writeTree(b *strings.Builder, t PreservedTree) {
	fmt.Fprintf(b, "\n  %s\n", t.Path)
	fmt.Fprintf(b, "    owner %s, branch %s, work item %s, %s\n",
		t.Owner, branchOrNone(t), workItemOrUnresolved(t), t.UntouchedText())

	if t.Outcome == "preserved" {
		fmt.Fprintf(b, "    %d uncommitted: %d modified, %d UNTRACKED   `--force` reclaims it: %s\n",
			t.Total, t.Modified, t.Untracked, t.ForceReclaims)
		if untracked := t.UntrackedPaths(); len(untracked) > 0 {
			fmt.Fprintf(b, "    untracked — on no branch, in no stash, on no remote; this tree is the only copy:\n")
			for _, p := range untracked {
				fmt.Fprintf(b, "      %s\n", p)
			}
		}
		if mod := t.ModifiedEntries(); len(mod) > 0 {
			fmt.Fprintf(b, "    modified:\n")
			shown := mod
			if len(shown) > preservedModifiedCap {
				shown = shown[:preservedModifiedCap]
			}
			for _, line := range shown {
				fmt.Fprintf(b, "      %s\n", line)
			}
			if len(mod) > len(shown) {
				fmt.Fprintf(b, "      ... and %d more — `git -C %s status` for the rest\n",
					len(mod)-len(shown), t.Path)
			}
		}
		return
	}

	fmt.Fprintf(b, "    UNREADABLE: git status failed (%s)\n", t.StatusError)
	fmt.Fprintf(b, "    what is in this tree is UNKNOWN — that is not a report of an empty tree.\n")
	fmt.Fprintf(b, "    `--force` reclaims it: %s\n", t.ForceReclaims)
}

func branchOrNone(t PreservedTree) string {
	if t.Branch != "" {
		return t.Branch
	}
	if t.BranchError != "" {
		return "UNREADABLE (" + t.BranchError + ")"
	}
	return "none"
}

func workItemOrUnresolved(t PreservedTree) string {
	if t.WorkItemID == "" {
		return fmt.Sprintf("UNRESOLVED (%q matches no work item)", t.Owner)
	}
	return fmt.Sprintf("%s (%s)", t.WorkItemID, t.TicketState)
}

type repoGroup struct {
	Repo  string
	Trees []PreservedTree
}

// groupByRepo groups an already-repo-sorted listing.
func groupByRepo(trees []PreservedTree) []repoGroup {
	var out []repoGroup
	for _, t := range trees {
		if n := len(out); n > 0 && out[n-1].Repo == t.Repo {
			out[n-1].Trees = append(out[n-1].Trees, t)
			continue
		}
		out = append(out, repoGroup{Repo: t.Repo, Trees: []PreservedTree{t}})
	}
	return out
}
