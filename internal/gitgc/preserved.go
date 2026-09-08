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

// PreservedTree is one worktree that HOLDS WORK THE FLEET CANNOT REACH FROM
// ANYWHERE ELSE — because the removal guard refused it (a dirty tree, mg-ee02),
// because it could not be read (mg-4d45), or because its HEAD reaches commits
// no origin ref holds and the integration branch has no equivalent of
// (mg-fcba, the Commits field).
//
// The first two are the population `pogo gc --list-preserved` enumerates: trees
// gc declined to reclaim. The third is NOT that population and the difference
// matters — a clean worktree whose polecat committed and never pushed is one gc
// would happily reap, and reaping it on a detached HEAD orphans the only copy
// of those commits. PreservedForItems reports all three because a DISPATCH must
// not be advertised over any of them; ScanPreserved lists the first two and
// annotates them with the third.
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
	// read, "undetermined" means `git status` failed and we could not look, and
	// "unpushed" means the tree is CLEAN and readable and holds commits that
	// exist nowhere else (mg-8d25). Folding any of these into another would
	// state a fact about the tree that nobody established.
	Outcome string `json:"outcome"`

	// Total, Modified and Untracked are the dirty split, present only on the
	// "preserved" outcome — a count is meaningful only when the tree was
	// actually read. Untracked is the urgent half: such a path is on no
	// branch, in no stash and on no remote, so this tree is the only copy of
	// its GIT OBJECTS.
	//
	// It is NOT necessarily the only copy of the CONTENT, and the two were
	// conflated here for long enough to mislead three agents in one day
	// (mg-11fa). Of one tree's 7 untracked paths, 3 were byte-identical to
	// origin/main and 4 more existed upstream at greater length; committing
	// them would have republished drafts that shipped work had overtaken.
	// The untracked marker orders these paths by RECOVERABILITY, never by
	// value, and a `cmp` against the upstream path is what separates them.
	Total     int `json:"dirty_paths,omitempty"`
	Modified  int `json:"modified_paths,omitempty"`
	Untracked int `json:"untracked_paths,omitempty"`
	// Files is the FULL `git status --porcelain` list, never capped. The cap
	// belongs to the renderer; a capped record is a record that reproduces this
	// ticket's own defect — a partial read presented as a whole one.
	Files []string `json:"files,omitempty"`

	// StatusError is the `git status` failure on the "undetermined" outcome.
	StatusError string `json:"status_error,omitempty"`

	// Commits is what this tree's HEAD holds that exists nowhere else — the
	// COMMITTED half of "work that only lives in a worktree", where every
	// field above it describes the uncommitted half (mg-fcba).
	//
	// The two halves are independent and a tree can be in either, both, or
	// neither. A polecat stopped before it committed leaves a dirty tree and no
	// commits; one stopped after it committed and before it pushed leaves a
	// CLEAN tree whose work is invisible to `git status` — and if that tree is
	// on a detached HEAD, invisible to every ref scan as well, because its
	// commits are reachable from the worktree's HEAD and from nothing else.
	//
	// Populated by PreservedForItems for every candidate tree, and by
	// ScanPreserved for every tree it lists. Nil means the question was not
	// asked or the tree's HEAD reached nothing at risk; AtRisk() on the finding
	// is what separates those from a finding.
	Commits *WorktreeCommitFinding `json:"commits,omitempty"`

	// UntouchedSeconds is how long the tree has gone unwritten, and
	// UntouchedKnown whether it could be established. It is a REPORT and
	// decides nothing — see UndeterminedWorktreeError.Untouched for why that
	// split is load-bearing rather than stylistic.
	UntouchedSeconds int  `json:"untouched_seconds,omitempty"`
	UntouchedKnown   bool `json:"untouched_known"`
	// UntouchedError is WHY the age is unknown, when it is (mg-1530).
	//
	// It exists because the bound added in that ticket created a second way to
	// have no age, and the two want different readers. "The tree could not be
	// listed" sends someone to a broken directory; "the walk was abandoned
	// after 60s" sends them to a tree that is merely enormous — and on a host
	// holding 196 retained trees (drellem2/pogo#162) the second is the common
	// case. Collapsing them would report a slow tree as a damaged one.
	UntouchedError string `json:"untouched_error,omitempty"`

	// Live is true when the OWNER is a running polecat. Such a tree is not
	// retained — it is in use — and it is reported separately so the headline
	// count is the population that needs an owner, not the population that has
	// one.
	Live bool `json:"live"`

	// ForceReclaims answers, for THIS tree, whether
	// `pogo gc --repo=<Repo> --apply --force` would actually take it: "yes",
	// "no", "no (detached)", or "unknown" when the ticket index could not be
	// loaded.
	//
	// It exists because --force is NOT the whole gate, and there are two
	// independent reasons it is not. The sweep checks the owner's ticket state
	// BEFORE it consults the dirty guard, so a retained tree whose work item is
	// still in flight survives --force untouched. And the sweep only ever
	// considers a worktree checked out on a polecat-* branch, so a DETACHED tree
	// — which `git worktree list` reports with no branch at all — is skipped
	// with or without the flag (mg-8d25, measured). An operator who reads
	// --force as "reclaims everything listed" is wrong in three directions.
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
		if t.UntouchedError != "" {
			return "age unknown — " + t.UntouchedError
		}
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
	// Target is the integration branch used to spare a rebase-landed tree a
	// false at-risk annotation. Empty resolves to DefaultTargetBranch. See
	// PreservedItemOptions.Target.
	Target string
	// Progress, when non-nil, receives every step of the scan AS IT HAPPENS
	// (mg-1530). See PreservedScanEvent — this is the whole of the streaming
	// mechanism, and the reason drellem2/pogo#158's symptom is no longer
	// reachable.
	Progress PreservedProgressFunc
	// AgeWalkBudget bounds the per-tree age walk. Zero uses
	// defaultAgeWalkBudget; the walk is never unbounded here.
	AgeWalkBudget time.Duration
}

// PreservedScanEvent is one step of the scan, handed to
// PreservedScanOptions.Progress the moment it happens.
//
// # This is what drellem2/pogo#158 was actually about (mg-1530)
//
// The command wrote its first byte only after the ENTIRE scan finished. Every
// intermediate fact — how many directories there are, which one is being read,
// what the last twelve resolved to — existed in memory and reached nobody, so a
// scan that was merely slow was indistinguishable from a hung one, and neither
// told the operator which tree to go and look at. The reporter saw zero bytes
// and no return, and had no way to learn more except to kill it.
//
// Note what the events carry and what they do not. There is no percentage, no
// ETA and no rate: the scan's cost per tree varies by orders of magnitude (a
// node_modules worktree walk was measured at 21.6-28.1s in drellem2/pogo#168
// against ~0.03s for a small tree), so any projection from the trees already
// read would be a confident wrong number. Index, total, and the NAME of the
// tree currently being read are facts; "43% done" would not be.
type PreservedScanEvent struct {
	// Phase is "start" once, then "enter"/"done" per directory.
	//
	// An "enter" is emitted BEFORE any subprocess runs or any directory is
	// walked for that tree, which is the property that makes a stalled scan
	// name its culprit: the last "enter" with no matching "done" IS the tree
	// that is hanging.
	Phase string
	// Index is the 1-based position of this directory, and Total how many
	// directories the scan will visit. Total is set on every event, including
	// "start", where Index is 0.
	Index, Total int
	// Owner is the directory name and Path its full path. Empty on "start".
	Owner, Path string
	// Disposition, on "done", is what the directory resolved to:
	// "retained", "in-use", "clean", "not-a-worktree" or "other-repo".
	Disposition string
	// Note is the text of a "note" event: a caveat established during the
	// scan that changes how the rows should be read — an unreadable work-item
	// index, say, which makes every `--force` column below it "unknown". It
	// is delivered WHEN IT IS ESTABLISHED rather than held to the end,
	// because a caveat that arrives after the rows it qualifies has already
	// failed at its job.
	Note string
	// Tree is the resolved record on the "retained" and "in-use"
	// dispositions, and nil otherwise. It is the FULL record, not a summary:
	// a streaming consumer renders the same block the final report would, so
	// a scan read halfway is a partial inventory rather than a teaser for one.
	Tree *PreservedTree
}

// PreservedProgressFunc receives scan events. It is called synchronously from
// the scan goroutine, in order, and must not block for long — every millisecond
// it spends is a millisecond the scan is not scanning.
type PreservedProgressFunc func(PreservedScanEvent)

// defaultAgeWalkBudget bounds ONE tree's age walk.
//
// Chosen against a hang, not against slowness, and the difference decides the
// value. drellem2/pogo#168 measured a legitimate node_modules worktree walk at
// 21.6-28.1s, so a tight bound would report "age unknown" for trees whose age
// is perfectly readable — and the age is the field an operator uses to decide
// whether a permanent pin can go. 60s clears the largest measured legitimate
// walk with room to spare while still converting an unresponsive filesystem
// from an unbounded wait into one row that says so.
//
// Making the walk CHEAP is drellem2/pogo#156/#168's work, not this one's. This
// listing owns degrading legibly; those own the cost.
var defaultAgeWalkBudget = 60 * time.Second

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
	// The counts below are DERIVED from Retained and InUse, and they are
	// carried in the payload anyway. That redundancy is deliberate and it has
	// one named consumer: the nightly redeployer (scripts/pogo-self-deploy)
	// reads JSON with sed and no jq, on purpose — one fewer dependency in the
	// path that must work when everything else is broken. A single-line regex
	// cannot count an array of nested objects, so a reader without jq that had
	// to derive `len(retained)` itself would either grow a fragile counter or,
	// far more likely, not ask at all. It did not ask at all for 25 nights
	// (mg-e621): the deploy printed "no polecat holds unpushed work" beside
	// seven retained trees on this box, three of them holding untracked files
	// (the mayor's count, 2026-09-06; re-derived 2026-09-07 the seven held and
	// the untracked half read four), because the instrument that CAN see them
	// is this one and nothing joined the two populations up.
	//
	// Summary() renders from these same fields (applyCounts runs on its own
	// copy first), so the human listing and the machine listing cannot report
	// different populations — which is the failure this whole family is about.
	RetainedCount int `json:"retained_count"`
	// RetainedUncommitted, RetainedUndetermined and RetainedUnpushed are the
	// Outcome split, in Summary()'s order and with Summary()'s meanings.
	RetainedUncommitted  int `json:"retained_uncommitted"`
	RetainedUndetermined int `json:"retained_undetermined"`
	RetainedUnpushed     int `json:"retained_unpushed"`
	// RetainedUntracked is how many retained trees hold at least one UNTRACKED
	// path — the urgent subset, because such a path is on no branch, in no
	// stash and on no remote, so the tree is the only copy of its git objects.
	// It is a count of TREES and not of paths: the question a reader asks of a
	// headline is "how many of these am I one `rm -rf` away from losing", and
	// that is a question about trees.
	RetainedUntracked int `json:"retained_untracked"`
	InUseCount        int `json:"in_use_count"`
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

	progress := opts.Progress
	if progress == nil {
		progress = func(PreservedScanEvent) {}
	}
	note := func(text string) {
		rep.Notes = append(rep.Notes, text)
		progress(PreservedScanEvent{Phase: "note", Note: text})
	}

	entries, err := os.ReadDir(opts.PolecatsDir)
	if err != nil {
		return rep, fmt.Errorf("read polecats dir %s: %w", opts.PolecatsDir, err)
	}
	var dirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		}
	}

	ageBudget := opts.AgeWalkBudget
	if ageBudget <= 0 {
		ageBudget = defaultAgeWalkBudget
	}
	// The denominator is emitted BEFORE the first tree is touched, because it
	// is the one number that makes a slow scan bearable: an operator watching
	// "12 of 196" knows to wait, and an operator watching "12 of 13" knows
	// something is wrong. It is also the only figure available for free.
	progress(PreservedScanEvent{Phase: "start", Total: len(dirs)})

	tickets := opts.Tickets
	rep.TicketsLoaded = tickets != nil
	if tickets == nil {
		loaded, err := LoadTicketIndex()
		if err != nil {
			// Degrade rather than fail. The listing's irreplaceable half is the
			// set of trees and the files in them; the ticket column is a
			// convenience that `mg show` can supply by hand.
			note(fmt.Sprintf(
				"work-item states unavailable (%v) — ticket state reads \"unknown\" for every "+
					"tree below, and whether `--force` would reclaim one cannot be computed.", err))
			tickets = TicketIndex{}
		} else {
			tickets = loaded
			rep.TicketsLoaded = true
		}
	}

	for i, e := range dirs {
		path := filepath.Join(opts.PolecatsDir, e.Name())
		index := i + 1
		// Emitted before ANY work on this tree — no subprocess, no walk, not
		// even the .git stat. A scan that stops leaves this line as its last
		// output, which is how the operator learns which tree stopped it.
		progress(PreservedScanEvent{
			Phase: "enter", Index: index, Total: len(dirs), Owner: e.Name(), Path: path,
		})
		done := func(disposition string, tree *PreservedTree) {
			progress(PreservedScanEvent{
				Phase: "done", Index: index, Total: len(dirs), Owner: e.Name(), Path: path,
				Disposition: disposition, Tree: tree,
			})
		}
		// No .git entry at all means this is not a linked worktree — it is a
		// phase-1b orphan dir, which has no index and no HEAD, so
		// "uncommitted" is not a property it has. Counted, never listed:
		// putting it in a list of trees holding uncommitted work would be a
		// claim about it that nobody can make.
		if _, lerr := os.Lstat(filepath.Join(path, ".git")); lerr != nil {
			rep.NotWorktreeCount++
			done("not-a-worktree", nil)
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
			done("other-repo", nil)
			continue
		}

		// The same guard the sweep and the exit hook consult, called rather
		// than re-implemented, so the listing cannot claim a tree is retained
		// that gc would happily reap.
		chk := checkWorktreeRemoval(path, tree.Repo, opts.Target)
		if chk.Refusal == nil {
			rep.CleanCount++
			done("clean", nil)
			continue
		}

		var dwe *DirtyWorktreeError
		var uwe *UndeterminedWorktreeError
		var oce *OrphanCommitsError
		switch {
		case errors.As(chk.Refusal, &oce):
			// Clean by `git status`, detached, and holding commits nothing else
			// holds (mg-8d25). The same word PreservedForItems uses for the same
			// state: not "preserved", which asserts uncommitted work was read,
			// and not "undetermined", which asserts git failed. Neither happened.
			//
			// The COMMITS block below is what this tree's entry is FOR, and it is
			// filled by the annotation further down rather than from the refusal,
			// so the listing and the guard read the tree independently.
			tree.Outcome = "unpushed"
			setTreeAge(&tree, path, ageBudget)
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
			setTreeAge(&tree, path, ageBudget)
		case errors.As(chk.Refusal, &uwe):
			tree.Outcome = "undetermined"
			tree.StatusError = uwe.Err.Error()
			tree.UntouchedSeconds = int(uwe.Untouched.Seconds())
			tree.UntouchedKnown = uwe.UntouchedKnown
			// The guard already walked this tree (bounded), so the listing
			// takes its answer rather than walking a second time — including
			// the reason it has no answer, when it has none.
			if !uwe.UntouchedKnown {
				tree.UntouchedError = untouchedReason(uwe.UntouchedErr, defaultAgeWalkBudget)
			}
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

		// Annotate — never filter — with the COMMITTED half (mg-fcba). A tree
		// already listed here is retained for a reason `git status` gave; this
		// says additionally whether its HEAD reaches commits no origin ref
		// holds, which is the fact that separates "reclaiming this loses an
		// edit" from "reclaiming this loses the only copy of some commits".
		//
		// # What the listed POPULATION now is, and what changed (mg-8d25)
		//
		// This used to read "the listed population is unchanged", on the ground
		// that a clean tree holding unpushed commits is not a tree gc refused.
		// That ground is gone: the removal guard now refuses a CLEAN tree on a
		// DETACHED HEAD whose commits exist nowhere else, so such a tree IS a
		// tree gc refused and it is listed above with outcome "unpushed". The
		// listing did not choose to widen — it calls the guard, and the guard's
		// answer changed. That is the point of calling it rather than
		// re-implementing it.
		//
		// A clean tree on a BRANCH holding unpushed commits is still NOT listed,
		// and still deliberately: removing it drops the tree and the branch ref
		// keeps the objects, so gc does not refuse it. The dispatch guard is
		// where that tree matters and PreservedForItems reports it there.
		if find, ferr := WorktreeCommitsAtRisk(path, tree.Repo, opts.Target); ferr != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf(
				"could not check whether %s holds commits that exist nowhere else: %v", path, ferr))
		} else if find.AtRisk() {
			f := find
			tree.Commits = &f
		}

		id, state := tickets.OwnerState(tree.Owner)
		tree.WorkItemID, tree.TicketState = id, state.String()

		if opts.LivePolecats[tree.Owner] {
			tree.Live = true
			// A live owner's tree is never gc's to take, whatever --force says.
			tree.ForceReclaims = "no"
			rep.InUse = append(rep.InUse, tree)
			done("in-use", &tree)
			continue
		}
		// Detachment is asked with the probe rather than inferred from the
		// branch reading "HEAD" — that string is what `rev-parse --abbrev-ref`
		// prints, and inferring a state from it is the shape of instrument this
		// whole ticket is about. An unreadable answer leaves the column alone.
		detached, derr := WorktreeDetached(path)
		tree.ForceReclaims = forceReclaims(rep.TicketsLoaded, tickets, tree.Owner, tree.Branch,
			detached && derr == nil)
		rep.Retained = append(rep.Retained, tree)
		done("retained", &tree)
	}

	sortPreserved(rep.Retained)
	sortPreserved(rep.InUse)
	rep.applyCounts()
	return rep, nil
}

// setTreeAge fills a tree's age fields from a BOUNDED walk, recording why the
// age is missing when it is.
//
// The failure is written down rather than dropped (mg-1530). Before the bound
// there was exactly one way to have no age — the tree could not be enumerated —
// and the renderer could name it from the bool alone. There are now two, they
// send a reader to different places, and the one this ticket added is the one
// that will fire in the field: a host holding 196 retained trees
// (drellem2/pogo#162) has slow trees long before it has broken ones.
func setTreeAge(tree *PreservedTree, path string, budget time.Duration) {
	newest, werr := newestWriteWithin(path, budget)
	if werr == nil {
		tree.UntouchedSeconds = int(time.Since(newest).Seconds())
		tree.UntouchedKnown = true
		return
	}
	tree.UntouchedKnown = false
	tree.UntouchedError = untouchedReason(werr, budget)
}

// untouchedReason renders why a tree has no age, in the vocabulary
// untouchedClause uses for the same two shapes.
//
// The wording for an unenumerable tree is unchanged and stays unchanged
// deliberately: it is the sentence the refusal line has printed since mg-4d45,
// and two components describing one condition in two ways is the failure this
// listing family keeps being about.
func untouchedReason(werr error, budget time.Duration) string {
	if errors.Is(werr, ErrWalkBudget) {
		return fmt.Sprintf("the tree is too large to measure in %s, so the walk was "+
			"abandoned. The tree is fine; this is a COST, not damage", budget)
	}
	return "the tree could not be listed"
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
func forceReclaims(ticketsLoaded bool, tickets TicketIndex, owner, branch string, detached bool) string {
	if detached {
		// MEASURED, not reasoned (mg-8d25): the sweep's phase 1 skips any
		// worktree whose Branch does not start with "polecat-", and `git
		// worktree list --porcelain` reports an EMPTY branch for a detached
		// tree — so Worktree.IsPolecat() is false and the tree is never
		// considered, with or without --force. Phase 1b does not pick it up
		// either: it only scans directories with no registration, and this one
		// has one.
		//
		// Saying "yes" here was wrong before this ticket too (a dirty detached
		// tree has always read that way). It matters more now, because the
		// removal guard newly RETAINS this shape, so the listing prints the
		// column for a population it used to skip — and a reclaim column that
		// names a command which silently does nothing is how a pinned tree
		// becomes permanent while its owner believes they cleared it.
		return "no (detached)"
	}
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

// applyCounts fills the scalar count fields from Retained and InUse.
//
// It is the ONE derivation of those numbers. ScanPreserved calls it before
// returning and Summary() calls it on its own copy, so a hand-built report
// still renders correctly and — the part that matters — the human headline and
// the `--json` payload can never disagree about how many trees are retained.
// Two components observing different populations and only one of them speaking
// in universals is precisely the defect mg-e621 records.
func (r *PreservedReport) applyCounts() {
	r.RetainedCount = len(r.Retained)
	r.InUseCount = len(r.InUse)
	r.RetainedUncommitted, r.RetainedUndetermined, r.RetainedUnpushed, r.RetainedUntracked = 0, 0, 0, 0
	for _, t := range r.Retained {
		// Three counts, not two. "unpushed" trees hold no uncommitted work and
		// are perfectly readable, so folding them into either existing bucket
		// states a fact about them that nobody established — and the headline
		// count is the line a reader takes away (mg-8d25).
		switch t.Outcome {
		case "undetermined":
			r.RetainedUndetermined++
		case "unpushed":
			r.RetainedUnpushed++
		default:
			r.RetainedUncommitted++
		}
		if t.Untracked > 0 {
			r.RetainedUntracked++
		}
	}
}

// Summary renders the report for an operator, whole, once the scan is done.
//
// It is NOT what `pogo gc --list-preserved` prints any more — that command
// streams (StreamedHeader/StreamedTree/StreamedTail), because a report that
// exists only at the end is a report a slow scan never produces (mg-1530).
// Summary remains the rendering for a report already in hand: tests, and any
// caller holding a PreservedReport it did not watch being built. Both
// renderings derive from the same fields through the same helpers, which is
// what keeps them from disagreeing about the population.
func (r PreservedReport) Summary() string {
	var b strings.Builder

	// r is a COPY (value receiver), so this recomputes the counts for the
	// render without touching the caller's report.
	r.applyCounts()

	writeHeadline(&b, r)
	writeCounts(&b, r)

	for _, n := range r.Notes {
		fmt.Fprintf(&b, "\n  note: %s\n", n)
	}

	if len(r.Retained) == 0 {
		fmt.Fprintf(&b, "\nNothing is retained. Every polecat worktree here is clean or in use.\n")
	} else {
		b.WriteString(preservedPreamble)
		for _, group := range groupByRepo(r.Retained) {
			writeRepoHeader(&b, group)
			for _, t := range group.Trees {
				writeTree(&b, t)
			}
		}
		b.WriteString(preservedFooter)
	}

	writeInUse(&b, r)
	writeErrors(&b, r)
	return b.String()
}

// writeHeadline names the directory the listing covers.
//
// The --repo filter's accounting deliberately does NOT live here: it carries a
// COUNT, and a count is not known until the scan ends. It moved to writeCounts
// with the other numbers so that a streamed listing cannot print it early, at
// zero, and be believed.
func writeHeadline(b *strings.Builder, r PreservedReport) {
	fmt.Fprintf(b, "retained polecat worktrees under %s\n", r.PolecatsDir)
}

// writeCounts renders the population block — the numbers a reader carries away.
//
// Split out of Summary so the streamed listing renders the SAME block from the
// SAME fields at the end of its scan. Two renderings of one population that
// count it separately is the defect mg-e621 records; this is the guard
// applyCounts is, one layer up.
func writeCounts(b *strings.Builder, r PreservedReport) {
	if r.RepoFilter != "" {
		fmt.Fprintf(b, "  (filtered to repo %s — %d tree(s) in other repositories not shown;\n"+
			"   a tree whose .git pointer could not be read is shown anyway, since it may be this one)\n",
			r.RepoFilter, r.OtherRepoCount)
	}
	fmt.Fprintf(b, "  %d retained: %d holding uncommitted work, %d unreadable, "+
		"%d clean but holding commits that exist nowhere else\n",
		r.RetainedCount, r.RetainedUncommitted, r.RetainedUndetermined, r.RetainedUnpushed)
	// The untracked subset gets its own headline line rather than living only
	// in the per-tree rows below. It is the number a reader carries away, and
	// on 2026-09-06 it was the number that made a deploy's "no polecat holds
	// unpushed work" false rather than merely narrow: three of seven trees held
	// content in no branch, no stash and on no remote — the mayor's count that
	// day, four of seven when re-derived on 2026-09-07 (mg-e621).
	fmt.Fprintf(b, "  of those, %d hold UNTRACKED files — on no branch, in no stash, on no remote,\n"+
		"  so the tree is the only copy of those git objects\n", r.RetainedUntracked)
	fmt.Fprintf(b, "  %d dirty tree(s) in use by a live polecat — not retained, listed at the end\n",
		r.InUseCount)
	fmt.Fprintf(b, "  %d clean, %d not linked worktrees (no .git — see `pogo gc` orphan dirs)\n",
		r.CleanCount, r.NotWorktreeCount)
}

func writeInUse(b *strings.Builder, r PreservedReport) {
	if len(r.InUse) == 0 {
		return
	}
	fmt.Fprintf(b, "\nin use by a live polecat (NOT retained — do not touch):\n")
	for _, t := range r.InUse {
		fmt.Fprintf(b, "  %s  owner %s, branch %s, %d uncommitted\n",
			t.Path, t.Owner, branchOrNone(t), t.Total)
	}
}

func writeErrors(b *strings.Builder, r PreservedReport) {
	if len(r.Errors) == 0 {
		return
	}
	fmt.Fprintf(b, "\nerrors (%d):\n", len(r.Errors))
	for _, e := range r.Errors {
		fmt.Fprintf(b, "  %s\n", e)
	}
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

// writeRepoHeader renders one repository's heading — what the reclaim command
// for that repo would actually do, and to which of its trees.
//
// THE HEADER IS THE POINT OF THE GROUPING. `pogo gc --repo=<repo> --apply
// --force` is repo-scoped and forced: it acts on every eligible retained tree
// in that repository, not on the one the operator just inspected. A reader who
// takes the command out of a per-tree preservation notice — which is where it
// appears — has no way to see that from the notice. Grouping puts the blast
// radius above the trees it covers, and names the count.
func writeRepoHeader(b *strings.Builder, g repoGroup) {
	repo := g.Repo
	if repo == "" {
		fmt.Fprintf(b, "\nrepository UNRESOLVED (the tree's .git pointer could not be read)\n")
	} else {
		fmt.Fprintf(b, "\n%s\n", repo)
	}

	var eligible, held, unknown, detached []string
	for _, t := range g.Trees {
		switch t.ForceReclaims {
		case "yes":
			eligible = append(eligible, t.Owner)
		case "unknown":
			unknown = append(unknown, t.Owner)
		case "no (detached)":
			detached = append(detached, t.Owner)
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
		if len(detached) > 0 {
			// A SECOND reason --force does not mean "everything", and unlike the
			// ticket-state one it is not about the item at all: `pogo gc` only
			// ever considers a worktree checked out on a polecat-* branch, and a
			// detached tree reports no branch. The command below is what
			// actually reclaims one.
			fmt.Fprintf(b, "    it would NOT touch (DETACHED — `pogo gc` only sees worktrees on a "+
				"%s* branch): %s\n", BranchPrefix, strings.Join(detached, ", "))
			fmt.Fprintf(b, "      reclaim one of those with: git -C %s worktree remove --force <path>\n", repo)
		}
		if len(unknown) > 0 {
			fmt.Fprintf(b, "    unknown, work-item states could not be read: %s\n", strings.Join(unknown, ", "))
		}
	}
}

func writeTree(b *strings.Builder, t PreservedTree) { writeTreeRow(b, t, false) }

// writeTreeRow renders one tree, optionally naming the repository ON the row.
//
// The grouped report does not need that — writeRepoHeader has just named the
// repository above the trees it covers, and repeating it per tree would be
// noise. A STREAMED row does: it arrives in scan order, under no group header,
// and the reclaim command is repo-scoped, so a row that cannot say which
// repository it belongs to is a row an operator cannot act on. The same fact,
// carried differently because the surrounding structure differs.
func writeTreeRow(b *strings.Builder, t PreservedTree, showRepo bool) {
	fmt.Fprintf(b, "\n  %s\n", t.Path)
	if showRepo {
		if t.Repo != "" {
			fmt.Fprintf(b, "    repository %s\n", t.Repo)
		} else {
			fmt.Fprintf(b, "    repository UNRESOLVED (the tree's .git pointer could not be read: %s)\n",
				t.RepoError)
		}
	}
	fmt.Fprintf(b, "    owner %s, branch %s, work item %s, %s\n",
		t.Owner, branchOrNone(t), workItemOrUnresolved(t), t.UntouchedText())
	writeTreeCommits(b, t)

	if t.Outcome == "preserved" {
		fmt.Fprintf(b, "    %d uncommitted: %d modified, %d UNTRACKED   `--force` reclaims it: %s\n",
			t.Total, t.Modified, t.Untracked, t.ForceReclaims)
		if untracked := t.UntrackedPaths(); len(untracked) > 0 {
			fmt.Fprintf(b, "    untracked — on no branch, in no stash, on no remote, so this tree is the\n")
			fmt.Fprintf(b, "    only copy OF THE GIT OBJECTS. That is not a claim about the CONTENT: a file\n")
			fmt.Fprintf(b, "    of the same path may exist upstream, identical or newer. `git show\n")
			fmt.Fprintf(b, "    origin/main:<path> | cmp - <path>` settles it per file and costs nothing:\n")
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

	if t.Outcome == "unpushed" {
		// No file list, because there are no files to list — `git status` read
		// this tree and found it clean. Printing the dirty block's zeros here
		// would answer a question nobody asked and bury the one that matters,
		// which writeTreeCommits printed above (mg-8d25).
		fmt.Fprintf(b, "    CLEAN — nothing uncommitted. This tree is retained for its COMMITS above,\n")
		fmt.Fprintf(b, "    not for its files, so there is nothing here to `git add`. Push the branch,\n")
		fmt.Fprintf(b, "    or cherry-pick the commits somewhere that has a ref, before reclaiming.\n")
		fmt.Fprintf(b, "    `--force` reclaims it: %s\n", t.ForceReclaims)
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

// writeTreeCommits renders the COMMITTED half of what a tree holds (mg-fcba) —
// commits reachable from its HEAD that no ref under refs/remotes/origin/ holds
// and that the integration branch has no patch-equivalent of.
//
// It prints only on a finding, and it is placed ABOVE the uncommitted block on
// purpose. The two halves lose differently: an uncommitted edit to a tracked
// file still has its committed version in the object store, while these commits
// have no copy anywhere — and on a detached HEAD they have no REF anywhere
// either, so nothing but this worktree's own HEAD keeps them from being pruned.
// A reader who stops after the first section has read the worse half.
func writeTreeCommits(b *strings.Builder, t PreservedTree) {
	if t.Commits == nil {
		return
	}
	if t.Commits.Verdict == DurabilityUnknown {
		fmt.Fprintf(b, "    COMMITS: whether this tree's HEAD holds commits that exist nowhere else\n")
		fmt.Fprintf(b, "    could NOT be established (%s). That is not a report of none.\n", t.Commits.Detail)
		return
	}
	// The COUNT is only printed when it was read. A local-only verdict with an
	// unreadable list would otherwise render "0 COMMIT(S) EXIST ONLY HERE" —
	// true of the list and false of the tree, and the number is the half that
	// gets skimmed. A caveat two lines further down does not reach the reader
	// who has already taken the zero.
	if len(t.Commits.Commits) == 0 {
		fmt.Fprintf(b, "    COMMITS EXIST ONLY HERE, HOW MANY IS UNKNOWN: no ref under\n")
		fmt.Fprintf(b, "    refs/remotes/origin/ holds them and none has a patch-equivalent on the\n")
		fmt.Fprintf(b, "    integration branch; the list itself could not be read.\n")
	} else {
		fmt.Fprintf(b, "    %d COMMIT(S) EXIST ONLY HERE: no ref under refs/remotes/origin/ holds them\n",
			len(t.Commits.Commits))
		fmt.Fprintf(b, "    and none has a patch-equivalent on the integration branch.\n")
	}
	if t.Branch == "" || t.Branch == "HEAD" {
		// The detached case, stated whenever the branch reads that way rather
		// than only when git said so, because it changes what a rescuer can do:
		// there is no ref to fetch, cherry-pick or push FROM, and removing the
		// worktree drops the last thing making these commits reachable.
		fmt.Fprintf(b, "    This tree is on a DETACHED HEAD, so those commits are held by this\n")
		fmt.Fprintf(b, "    worktree's HEAD and by NO REF AT ALL. Removing the tree orphans them.\n")
	}
	if t.Commits.CommitsError != "" {
		fmt.Fprintf(b, "    (the commit list could not be read: %s)\n", t.Commits.CommitsError)
		return
	}
	for _, c := range t.Commits.Commits {
		fmt.Fprintf(b, "      %s\n", c)
	}
}
