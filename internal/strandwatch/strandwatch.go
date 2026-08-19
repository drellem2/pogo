// Package strandwatch answers, for every OPEN work item, whether the work it is
// still asking for already exists on a branch.
//
// WHY IT EXISTS (mg-be37). A spawn-time guard already refuses to dispatch a
// polecat at an item whose branch carries pushed, unmerged work
// (internal/agent/strandedgate.go). It is a good guard and it prevented real
// loss. But it is TRIGGERED BY DISPATCH, so it can only fire when somebody tries
// to work the item — and an item nobody dispatches at is never checked. On the
// night of 2026-08-09 four branches were stranded across three repos. One was
// caught by that guard. One was caught by a person reconciling something else.
// Two were caught by the accident of somebody looking next door.
//
// From both directions the state is invisible: the board reads `available`,
// indistinguishable from never started; the repo holds finished work; and the
// polecat that did it is dead, so nothing is going to mail anybody. Meanwhile
// priority-wake advertises the item, and the action it recommends — dispatch — is
// the one that re-derives the work. mg-9a19 lost 1026 lines exactly that way.
//
// TWO ROW TYPES, NOT ONE, AND THE SECOND IS WORSE. The mayor appended it to the
// ticket after the first four:
//
//	STRANDED           item open, the branch has commits the target lacks.
//	                   Remedy: `pogo refinery submit`.
//	LANDED-NOT-CLOSED  item open, the branch is fully merged. The work is on
//	                   main and the ticket is still asking for it.
//	                   Remedy: `mg done`.
//
// The second is worse because nothing blocks it. While a branch is unmerged the
// spawn-time guard refuses the dispatch. The moment it merges the guard correctly
// stops refusing — and the item is still `available`. priority-wake nagged the
// mayor to "claim or dispatch now: mg-6c90" four minutes after that branch merged
// with 1116 insertions already on main. The window where the advice is
// destructive and nothing stops it opens at merge and never closes.
//
// The upstream repair for that row lives in pogod (cmd/pogod/reap.go): the work
// item is now closed at merge whatever submitted the branch, so the row should
// be empty in steady state. It is reported anyway, because a repair that is only
// in the merge path cannot see an item stranded before it shipped, or one whose
// `mg done` was refused.
//
// IT IS ITEM-DRIVEN, NOT BRANCH-DRIVEN, and that is what makes it readable. A
// branch-first sweep of this repo's origin finds 57 branches with unmerged
// patches, of which 48 belong to archived items and 2 to no item at all: a wall
// of rows whose actionable fraction is a couple of percent. Walking the ~115 open
// items instead and asking each one whether it has a branch produced THREE rows
// on the same store, one of them a live instance nothing else had found
// (mg-65d2: merged as 0640bc7, item still `available`). Same underlying facts,
// two orders of magnitude less noise, and it is what the ticket asked for —
// "rank on item status, not on branch count".
//
// THE POPULATION IS ITEMS, SO EVERY FAILURE HAS TO BE COUNTED IN ITEMS
// (mg-8baa). The sweep groups items by repository to pay the fetch and the
// branch listing once, and a repository whose listing fails used to be recorded
// on the RepoCoverage and nowhere else. That states the failure in the wrong
// units: the report's rows, its exit code and its closing sentence are all about
// items, and the items behind a failed repo crossed none of them. Three of 112
// open items were silently unchecked on 2026-08-14 while the header counted them
// as scanned and the run exited 0. Every such item is now a KindRepoUnreadable
// row, and the header states checked-of-population rather than population alone.
//
// AND THE POPULATION MOST AT RISK IS THE ONE THE JOIN EXCLUDES (mg-ded2). Every
// paragraph above is about the join, and the join is on OPEN items, so three
// things could not appear in this report at all:
//
//	a branch whose item is CLOSED   the item join has no row for it. `polecat-pc-rev-c5d5a10`
//	                               held a commit on no remote ref inside a repository
//	                               the sweep covered cleanly, and was absent.
//	a repo NO OPEN ITEM NAMES      no row, no error, no count. /Users/daniel/dev/macguffin
//	                               held a polecat worktree and appeared nowhere, and the
//	                               output was indistinguishable from a full sweep.
//	a --repo that matched NOTHING  a bare name, an abbreviation and a fictional repository
//	                               printed the same all-clear as a clean repo, and exited 0.
//
// A polecat whose item is still open has an owner, a queue entry and an agent
// that may yet push. A polecat whose item closed months ago has none of those,
// is the likeliest thing on the box to be forgotten, and is precisely what
// git-gc reaps. The instrument's population was the inverse of the risk.
//
// THE FIX IS NOT "ALSO SCAN CLOSED ITEMS" — that population is unbounded and
// abandoned by design, at 435 polecat branches in one repository. It is the
// WORKTREES PRESENT ON THIS HOST, which are bounded (46 on 2026-08-19), are the
// exact population git-gc reaps, and are enumerable with no dependence on item
// state at all. See KindOrphanBranch and Options.Worktrees.
//
// AND THE CHEAPEST HALF HAS THE WIDEST CATCH: the report now STATES ITS OWN
// BOUNDARY (BuildFrame). Neither row-level fix would have caught the macguffin
// case — that worktree is clean, so no stranded row fires on it — but a frame
// naming "repositories no open item names are outside this report" makes the
// omission visible without needing the tree to be dirty. An instrument that names
// its boundary is checkable; one that does not gets read as a census, which is
// what happened on 2026-08-19 when a coordinator read 2 findings over 121 items
// as the population of at-risk work and acted on it.
//
// REPORTS ONLY. It never submits and never closes. Submitting is cheap by hand
// once you know; a wrong auto-submit lands unreviewed work, and a wrong auto-done
// discards a branch.
package strandwatch

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/strandedwork"
)

// Item is the slice of a work item this detector needs.
type Item struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Title    string `json:"title"`
	Repo     string `json:"repo"`
	Assignee string `json:"assignee"`
	Priority string `json:"priority"`
}

// OpenStatuses are the work-item statuses this sweep covers: the ones where the
// board is still asking for the work.
//
// `done` and `archived` are deliberately absent. An unmerged branch on an
// archived item is mostly harmless — nobody is going to be told to work it — and
// including them is what turns a three-row report into a fifty-seven-row one. The
// ticket's own bound says so: "the number I will defend is 2 actionable", out of
// 59 branches. `shelved` is out for the same reason.
//
// THE SCOPING IS THIS SWEEP'S ALONE (mg-5ec6). A branch whose item was closed by
// a SIBLING's merge is outside this list by construction, and that was raised as
// a possible hole in the whole detector family. It is not: the spawn-time refusal
// and the release-time reporter in internal/agent/strandedgate.go read no item
// status at all — the first is keyed on the dispatch, the second on the agent
// being stopped — so the sibling-closed shape is visible to both. The
// open-item bound here is a NOISE bound on a periodic report, not a definition of
// what counts as stranded.
var OpenStatuses = []string{"available", "claimed", "pending"}

// Kind is what a row IS, and therefore what to do about it.
type Kind string

const (
	// KindStranded is an open item whose branch carries commits the target does
	// not have. Resubmit the branch; do not dispatch.
	KindStranded Kind = "stranded"

	// KindLandedNotClosed is an open item whose branch is fully merged. The work
	// is done and on the target; the item is what is out of date. Close it.
	KindLandedNotClosed Kind = "landed_not_closed"

	// KindConflictSuspect is an open item whose branch `git cherry` calls
	// unmerged while the target already holds essentially every line it adds —
	// the signature of a rebase that lost the commit's patch identity while
	// landing it. See internal/strandedwork/content.go for the measured controls.
	//
	// THE NAME IS NARROWER THAN THE ROW, and is kept only because it is on the
	// wire (mg-5ec6). A CONFLICT is the one mechanism that cannot produce this:
	// the refinery aborts its rebase and fails the merge request on conflict, so
	// no conflicted branch ever lands. The two measured mechanisms are both
	// ordinary CLEAN rebases — one that replayed a hunk into moved context, and
	// one that dropped a hunk the target had already made. Read it as
	// "landed-under-another-sha suspect".
	//
	// IT RECOMMENDS NEITHER ACTION, and that is the point. Both instruments are
	// heuristics, they disagree here, and each of the two remedies is destructive
	// if the other instrument is the right one: resubmitting a landed branch is
	// noise, but closing an unmerged one throws the work away. A row that says
	// "look at this" is the honest output of two disagreeing measurements.
	KindConflictSuspect Kind = "conflict_suspect"

	// KindUnjudged is an open item whose branch COULD NOT BE READ. Git failed,
	// the target would not resolve, the ref was unreadable — the question was
	// asked and no answer came back.
	//
	// IT IS A ROW AND NOT A FOOTNOTE, and that is the whole reason it exists. The
	// natural way to write this predicate is `git cherry <target> <branch> |
	// grep -q '^+'`, and that shape answers LANDED WHENEVER GIT FAILS: a failed
	// git prints nothing, grep finds no `+`, and "no output" is exactly how the
	// predicate spells clean. Measured against an unresolvable ref by mg-b6d1,
	// which returns `landed`.
	//
	// For a sweep that is the dangerous direction — `landed` is the answer that
	// means "this branch needs nothing", so one transient failure converts a
	// stranded branch into a clean row and the whole run reports all-clear over
	// work sitting unmerged. Folding it into stranded instead cries wolf on every
	// network blip, and the fleet has measured ~40-minute connectivity waves. So
	// neither fold is acceptable and the third cell is not optional.
	//
	// It is ACTIONABLE: an unjudged branch on an open item might be a strand, and
	// the reader has to be the one who finds out.
	KindUnjudged Kind = "unjudged"

	// KindRepoUnreadable is an open item whose REPOSITORY could not be listed, so
	// no branch was ever looked for. It is one step further out than
	// KindUnjudged: there, a branch was found and could not be read; here the
	// question was never asked.
	//
	// IT IS A ROW BECAUSE THE ALTERNATIVE WAS MEASURED AND IS SILENT (mg-8baa).
	// The repo error was already recorded per-REPOSITORY, but the population this
	// sweep reports on is ITEMS, and nothing carried the failure across that join:
	// the items in an unlistable repo were dropped without appearing in any
	// column. On 2026-08-14 that produced, verbatim,
	//
	//	112 open work item(s) scanned across 9 repo(s)
	//	  ...
	//	  onethird_program — COULD NOT LIST BRANCHES: ... No such file or directory
	//	  riemann — COULD NOT LIST BRANCHES: ... No such file or directory
	//	...
	//	No open work item has work already sitting on a branch.
	//
	// exiting 0. Three of those 112 items were never checked, the header counted
	// them as scanned anyway, the closing sentence is a flat all-clear over them,
	// and no item id appears anywhere a reader could chase. The mayor caught the
	// missed item only by having swept statuses by hand independently.
	//
	// THE PREDICATE IS "THE REPO COULD NOT BE LISTED", NOT "THE REPO FIELD IS
	// RELATIVE. The ticket's hypothesis was the bare relative name — `repo:
	// onethird_program` rather than `/Users/daniel/research/onethird_program` —
	// and that is a real instance, but it is a subset. The third item in the run
	// above was `/Users/daniel/.claude`: absolute, existing, and not a git
	// repository. Keying the row on the shape of the string would have kept that
	// one silent, so it is keyed on the failure instead.
	//
	// AN ITEM NAMING NO REPO AT ALL IS THE SAME ROW. That case was already
	// counted (ItemsWithoutRepo) and printed as a line, but it was likewise not a
	// row and so likewise did not survive into the exit code — a report that
	// states a gap in prose and then exits 0 is read by a schedule as clean.
	//
	// IT IS ACTIONABLE for the same reason KindUnjudged is, and the direction of
	// the error is the dangerous one: an item whose repo was never listed may be
	// carrying a stranded branch, and the failure is on the side that reports
	// nothing.
	KindRepoUnreadable Kind = "repo_unreadable"

	// KindOrphanBranch is a polecat WORKTREE still present on this host whose
	// branch holds commits that no remote ref contains, and which no OPEN work
	// item names. It is the only row in this report whose subject is a branch
	// rather than an item, and it exists because the item join cannot produce it
	// (mg-ded2).
	//
	// THE POPULATION THIS ROW COVERS IS THE INVERSE OF THE ITEM JOIN'S, AND IT IS
	// THE ONE MOST AT RISK. A polecat whose item is still open has an owner, a
	// queue entry, and an agent that may yet push; every other row in this report
	// is about one of those. A polecat whose item closed months ago has none of
	// them, is the likeliest thing on the box to be forgotten, and is precisely
	// what git-gc reaps. Joining on open items excluded exactly that population by
	// construction: `polecat-pc-rev-c5d5a10` held a commit on no remote ref inside
	// a repository the sweep covered cleanly, and did not appear in the report.
	//
	// IT IS BOUNDED BY THE WORKTREE, NOT BY THE BRANCH, and that is what makes it
	// shippable. "Also scan closed items" is not the fix: doctor measured 435
	// polecat branches in one repository alone, a population that is unbounded and
	// abandoned by design, and a report that grew by 435 rows would be strictly
	// worse than one that omits them. The at-risk population is the worktrees
	// PRESENT ON THIS HOST — 46 on 2026-08-19, enumerable from disk, exactly what
	// git-gc reaps — of which one held the only copy of anything. A branch with no
	// worktree has no local-only copy to lose.
	//
	// THE PREDICATE IS "ON NO REMOTE REF", NOT "UNMERGED" (see
	// strandedwork.LocalOnlyCommits). Unmerged-vs-target is the right question for
	// a branch somebody still intends to merge, and it is what every other row
	// here asks; it is the wrong question for a branch nobody owns, because its
	// answer says nothing about what a `git gc` would destroy.
	//
	// THE REMEDY IS NOT `refinery submit`, and that is why this is its own Kind
	// rather than a stranded row with an empty item. There is no owner to ask and
	// no open item to submit under: the only safe first move is to get the object
	// onto a remote, and the judgement about what it was for stays with a reader.
	KindOrphanBranch Kind = "orphan_branch"
)

// Rank orders the kinds within an item status. Stranded first: it is the row
// whose item is about to be dispatched at. Unjudged sits immediately behind it,
// because an unjudged row MIGHT be a stranded one and nothing in the report can
// say it is not.
//
// repo_unreadable sits third, behind unjudged and ahead of the two rows that
// know what they are looking at. Both of the first two are "we do not know", and
// unjudged leads because it at least FOUND a branch: something exists on that
// item. A repo that could not be listed might hold a strand or might hold
// nothing, and the report cannot say which — but it must not rank below a row
// that has already been judged harmless.
func (k Kind) Rank() int {
	switch k {
	case KindStranded:
		return 0
	case KindUnjudged:
		return 1
	case KindRepoUnreadable:
		return 2
	case KindConflictSuspect:
		return 3
	case KindOrphanBranch:
		return 4
	default:
		return 5
	}
}

// statusRank orders rows by how destructive the next likely action is.
//
// `available` is worst and it is not close: priority-wake advertises those items
// by name, and the action it advertises is the one that destroys the branch.
// `pending` is next — an item whose gate is one dependency away from opening.
// `claimed` is last: somebody is already on it, so a human is in the loop.
func statusRank(status string) int {
	switch strings.ToLower(status) {
	case "available":
		return 0
	case "pending":
		return 1
	case "claimed":
		return 2
	case "":
		// A KindOrphanBranch row has no item and therefore no imminent dispatch:
		// nothing is going to be told to work it, which is both why it was
		// forgotten and why it ranks below every row that IS about to be acted on.
		// The risk it carries is passive — git-gc reaps the worktree — so it sorts
		// last and is named in the header count instead, where it cannot be missed
		// by a reader who stops at the first row.
		return 3
	default:
		return 4
	}
}

// Row is one finding: an open work item joined to a branch that has something to
// say about it.
type Row struct {
	Item   Item   `json:"item"`
	Branch string `json:"branch"`
	// Ref is what the commits were read from — the remote-tracking ref when one
	// exists, otherwise the local head. "The work is on origin" and "the work is
	// only in a worktree git-gc is about to reap" are different emergencies.
	Ref    string `json:"ref"`
	Pushed bool   `json:"pushed"`
	Target string `json:"target"`
	Kind   Kind   `json:"kind"`

	// Unmerged is how many commits `git cherry` says the target lacks.
	Unmerged int `json:"unmerged"`
	// Equivalent is how many of the branch's commits the target already has under
	// a different sha — the refinery's rebase merge rewrites every one.
	Equivalent int `json:"equivalent"`

	// Presence is the content-level second opinion. Populated only when there
	// were unmerged commits to measure.
	Presence strandedwork.Presence `json:"presence"`

	// PreRegistration is the oldest unmerged pre-registration commit, when the
	// branch carries one. A re-dispatch must branch FROM it and never amend it.
	PreRegistration *strandedwork.Commit `json:"pre_registration,omitempty"`

	// Subjects are the unmerged commit subjects, so a reader can recognise the
	// work without a git round-trip.
	Subjects []string `json:"subjects,omitempty"`

	// Error is why a KindUnjudged row could not be answered. Empty otherwise.
	Error string `json:"error,omitempty"`
}

// Subject is what this row is ABOUT, in the words a reader can chase: the work
// item when there is one, the branch when there is not.
//
// A KindOrphanBranch row has no item by construction — that is what makes it the
// row the item join could not produce — so every reader that prints "the item
// id" needs one string that is never empty. An empty column reads as a rendering
// bug and gets skipped; the branch name is the thing a reader can actually run
// git against.
func (r Row) Subject() string {
	if r.Item.ID != "" {
		return r.Item.ID
	}
	if r.Branch != "" {
		return r.Branch
	}
	return "(unnamed)"
}

// StatusLabel is the row's item status, or the words that say there is no item.
func (r Row) StatusLabel() string {
	if r.Item.Status != "" {
		return r.Item.Status
	}
	if r.Kind == KindOrphanBranch {
		return "NO ITEM"
	}
	return "-"
}

// Remedy is the one command to run, or the one question to answer.
//
// THE STRANDED ROW'S COMMAND DEPENDS ON r.Pushed (mg-bfe0). The renderer already
// labels the branch line "LOCAL ONLY — not on origin, and git-gc reaps the
// worktree", but the remedy underneath it was the bare submit line for both
// cases — and `pogo refinery submit` REFUSES a branch that is not on origin
// (mg-586d, the merge worker checks it out as origin/<branch>). A prose warning
// two lines above a runnable command loses to the command, so the push is built
// into the command via strandedwork.SubmitRemedy, which is the same rule the
// dispatch refusal prints.
func (r Row) Remedy() string {
	switch r.Kind {
	case KindStranded:
		return fmt.Sprintf("%s   # do NOT dispatch at %s",
			strandedwork.SubmitRemedy(r.Item.Repo, r.Branch, r.Item.ID, r.Pushed), r.Item.ID)
	case KindLandedNotClosed:
		return fmt.Sprintf("mg done %s --result='{\"branch\": \"%s\", \"note\": \"landed before the item was closed\"}'",
			r.Item.ID, r.Branch)
	case KindUnjudged:
		return fmt.Sprintf("git -C %s cherry %s %s   # re-run; this branch was NOT judged either way",
			r.Item.Repo, orDefault(r.Target, "origin/main"), orDefault(r.Ref, r.Branch))
	case KindRepoUnreadable:
		// The remedy is on the ITEM's repo field, not on this sweep, in all three
		// shapes — which is why none of them prints a `refinery submit` or an `mg
		// done`. Nothing here knows whether this item has a branch at all.
		switch {
		case r.Item.Repo == "":
			return fmt.Sprintf("mg show %s   # names NO repo, so no branch was ever looked for", r.Item.ID)
		case !filepath.IsAbs(r.Item.Repo):
			return fmt.Sprintf("mg show %s   # repo is the bare name %q — this sweep has no base "+
				"directory to resolve it against; set an absolute path", r.Item.ID, r.Item.Repo)
		default:
			return fmt.Sprintf("git -C %s for-each-ref 'refs/heads/%s*'   # reproduce the failure; "+
				"this sweep can only answer for a git repository", r.Item.Repo, strandedwork.BranchPrefix)
		}
	case KindOrphanBranch:
		// NOT `refinery submit`: there is no open item to submit under and no
		// owner to ask, and submit takes an --author. The one move that is safe
		// without knowing what the branch was for is getting the object onto a
		// remote, because that is the only thing the reap destroys.
		return fmt.Sprintf("git -C %s push origin %s   # %d commit(s) on NO remote ref; "+
			"then `git -C %s log %s` and decide — no open item names this branch",
			r.Item.Repo, r.Branch, r.Unmerged, r.Item.Repo, r.Branch)
	default:
		return fmt.Sprintf("git -C %s log --oneline %s..%s   # then submit OR close; do neither blind",
			r.Item.Repo, r.Target, r.Ref)
	}
}

// Excluded is a branch/item pair this sweep deliberately did not report, and
// why. Counted and nameable rather than silently dropped: a suppression nobody
// can see is indistinguishable from a detector that missed something.
type Excluded struct {
	ItemID string `json:"item_id"`
	Branch string `json:"branch"`
	Reason string `json:"reason"`
}

// Worktree is one polecat worktree PRESENT ON THIS HOST, as enumerated from
// disk rather than inferred from the board.
//
// It exists so the sweep can discover repositories no open work item names
// (mg-ded2). `/Users/daniel/dev/macguffin` held a polecat worktree on
// 2026-08-19 and appeared NOWHERE in the report — not in repos[], not as an
// error, not as a count — because the only source of repositories was the open
// items' repo fields. The output was indistinguishable from one where every
// repository on the box had been scanned, and macguffin is where mg-e7ff's fix
// later merged.
type Worktree struct {
	// Path is the worktree directory.
	Path string `json:"path"`
	// Repo is the repository it belongs to — the one holding the shared .git.
	Repo string `json:"repo"`
	// Branch is the branch checked out inside it. Empty for a detached head.
	Branch string `json:"branch"`
	// Error is why this worktree could not be fully read: no repository could be
	// named for it, or it has no branch. A worktree that is UNREADABLE is not a
	// worktree that holds nothing, and the enumerator must not be able to collapse
	// the two — that is the defect this whole type exists to repair, committed one
	// level down.
	Error string `json:"error,omitempty"`
}

// RepoCoverage records what happened to one repository, so the report can state
// its own reach rather than implying it saw everything.
type RepoCoverage struct {
	Repo     string `json:"repo"`
	Items    int    `json:"items"`
	Branches int    `json:"branches"`
	Fetched  bool   `json:"fetched"`
	Error    string `json:"error,omitempty"`
	// Worktrees is how many polecat worktrees on this host point at this repo.
	Worktrees int `json:"worktrees"`
	// NamedByOpenItem is false when this repository entered the sweep ONLY
	// because a polecat worktree on this host points at it, or because --repo
	// asked for it — i.e. when no open work item names it. Such a repository used
	// to produce no row at all, which is the one outcome a coverage report must
	// not be able to produce (mg-ded2).
	NamedByOpenItem bool `json:"named_by_open_item"`
}

// UnmatchedRepo is a --repo argument that resolved to nothing: no open work item
// names it, no polecat worktree on this host points at it, and it could not be
// listed as a git repository either.
//
// IT IS A ROW-CLASS FACT AND NOT A NOTE, because the alternative was measured
// and it FABRICATES AGREEMENT (mg-ded2, gap 3). Before this,
//
//	pogo check-stranded --repo one_third_width_three              -> exit 0, all clear
//	pogo check-stranded --repo this-repo-does-not-exist-anywhere  -> BYTE-IDENTICAL, exit 0
//	pogo check-stranded --repo /Users/daniel/research/one_third_width_three
//	                                                              -> 1 finding, exit 1
//
// A bare name, an abbreviation and a fictional repository were indistinguishable
// from a clean repository: same prose, same exit code. That is worse than a
// missing check, because a wrong command that prints an all-clear does not merely
// fail to check a claim — it manufactures support for whichever claim it was
// quoted to support. This one was found inside evidence that had been mailed
// arguing a conclusion the command appeared to confirm.
type UnmatchedRepo struct {
	Repo  string `json:"repo"`
	Error string `json:"error,omitempty"`
}

// Report is one sweep.
type Report struct {
	Rows     []Row          `json:"rows"`
	Excluded []Excluded     `json:"excluded"`
	Repos    []RepoCoverage `json:"repos"`
	// ItemsScanned is the open population this sweep enumerated — the
	// DENOMINATOR, not the coverage. It counts an item whose repo could not be
	// listed exactly as it counts one that was fully checked, which is why it
	// must never be rendered on its own: on the run that produced mg-8baa it read
	// 112 over a population three of whose items were never looked at.
	ItemsScanned int `json:"items_scanned"`
	// ItemsChecked is how many of those items were actually joined against a
	// SUCCESSFUL branch listing. This is the coverage numerator, and the pair
	// (ItemsChecked, ItemsScanned) is the bound the header states.
	//
	// The split follows internal/ghintake, whose ItemsScanned counts successful
	// body reads for the same reason: "a failed issue list and a repo with no
	// open issues are indistinguishable to a careless check". The two commands
	// disagreeing about that was itself worth removing.
	ItemsChecked int `json:"items_checked"`
	// ItemsWithoutRepo counts open items naming no repository. They cannot be
	// checked at all, and that is a coverage gap rather than a clean verdict.
	// Each one also produces a KindRepoUnreadable row: until mg-8baa this count
	// was printed as prose and then dropped, so the run still exited 0.
	ItemsWithoutRepo int `json:"items_without_repo"`
	// ItemsOutOfScope counts open items dropped by the --repo restriction. NOT a
	// coverage failure — the caller asked for the narrower sweep — but it is
	// still the difference between the denominator and what was looked at, and a
	// header that omits it overstates its own reach exactly as the unreadable-repo
	// case did.
	ItemsOutOfScope int `json:"items_out_of_scope"`
	// InspectErrors are branches that could not be read. Collected rather than
	// fatal: one unreadable ref must not turn a sweep of a hundred items into a
	// single error a reader renders as "nothing stranded".
	InspectErrors []string `json:"inspect_errors,omitempty"`
	// QueueUnreadable is set when the refinery queue could not be consulted, so
	// branches already awaiting merge could not be excluded. Stated, not fatal.
	QueueUnreadable string `json:"queue_unreadable,omitempty"`
	// ReposRequested echoes Options.Repos, so a reader of the JSON can see what
	// the run was ASKED about as well as what it found.
	ReposRequested []string `json:"repos_requested,omitempty"`
	// ReposUnmatched are --repo arguments that resolved to nothing. See
	// UnmatchedRepo: this is the field that makes a fictional repository
	// distinguishable from a clean one.
	ReposUnmatched []UnmatchedRepo `json:"repos_unmatched,omitempty"`
	// WorktreesSeen is how many polecat worktrees on this host were enumerated.
	WorktreesSeen int `json:"worktrees_seen"`
	// WorktreesInScope is how many of those actually entered this sweep. It is a
	// DIFFERENT number from WorktreesSeen under --repo, and the frame must state
	// the second rather than the first — quoting the enumeration count as if it
	// were the coverage count is mg-8baa's defect in worktree units.
	WorktreesInScope int `json:"worktrees_in_scope"`
	// WorktreesUnreadableList names the individual worktrees that could not be
	// read, so a member of the population never leaves it silently.
	WorktreesUnreadableList []Worktree `json:"worktrees_unreadable_list,omitempty"`
	// WorktreesUnreadable is set when the worktree enumeration itself failed, and
	// ReposDiscovered is therefore 0 for a reason that is not "there are none".
	WorktreesUnreadable string `json:"worktrees_unreadable,omitempty"`
	// ReposDiscovered counts repositories that entered the sweep WITHOUT an open
	// work item naming them.
	ReposDiscovered int `json:"repos_discovered"`
	// Frame states this report's own boundary, in the report. See BuildFrame.
	Frame []string `json:"frame"`
}

// Actionable reports whether this sweep found anything worth acting on.
func (r Report) Actionable() bool { return len(r.Rows) > 0 }

// Blind names why this run measured nothing, or "" when it measured something.
//
// A RUN THAT RESOLVED NOTHING MUST NOT EXIT CLEAN (mg-ded2, gap 3). The
// command's own help already defines exit 3 as "this run measured nothing", and
// a --repo restriction that matched no repository is that case by the tool's own
// definition. It returned 0 = "nothing found" instead, one line below a
// `0 repo(s)` count it printed and then contradicted. The information existed and
// lost to the reassuring sentence next to it — which is the same defect as the
// two coverage gaps rather than a different one: the output did not say loudly
// enough that something had dropped out of the population.
//
// AN UNMATCHED --repo IS BLINDNESS EVEN WHEN OTHERS MATCHED. The caller named a
// repository and got no answer about it; answering about the subset and exiting
// on the subset's verdict is exactly the "bounded answer read as a census" shape
// this whole ticket is about, and a typo is the commonest way to produce one.
func (r Report) Blind() string {
	if len(r.ReposUnmatched) > 0 {
		var names []string
		for _, u := range r.ReposUnmatched {
			names = append(names, fmt.Sprintf("%q", u.Repo))
		}
		return fmt.Sprintf("--repo %s matched no repository: no open work item names it, "+
			"no polecat worktree on this host points at it, and it could not be listed as a "+
			"git repository. This run says NOTHING about it — it is not a clean verdict",
			strings.Join(names, ", "))
	}
	if len(r.Repos) == 0 {
		return "zero repositories were scanned, so nothing was measured — this is a blind run, not a clean one"
	}
	if r.ItemsScanned == 0 && r.WorktreesSeen == 0 {
		return "zero open work items and zero polecat worktrees were enumerated, so nothing was measured"
	}
	return ""
}

// Count returns how many rows are of one kind.
func (r Report) Count(k Kind) int {
	n := 0
	for _, row := range r.Rows {
		if row.Kind == k {
			n++
		}
	}
	return n
}

// ErrNoLiveness is returned when the set of running agents could not be
// established.
//
// IT IS FATAL TO THE SWEEP, and the asymmetry with the other error paths is
// deliberate. Every running polecat has, by construction, a branch with unmerged
// commits on a claimed item — that is what a polecat in flight looks like. With
// no liveness answer the sweep cannot tell a mid-flight polecat from a strand,
// so it would name every live worker in the fleet and tell the reader to
// resubmit its half-finished branch. A detector that fires on healthy input is
// worse than no detector: the first thing it teaches its readers is to skip the
// line it prints, and this is the line the real stranding surfaces on.
var ErrNoLiveness = errors.New("could not establish which agents are running")

// Options configures a sweep. Every external fact is injected, so the detector
// can be exercised against real git repositories and a synthetic board without
// pogod, mg, or a refinery.
type Options struct {
	// Items lists the open work items. Required.
	Items func() ([]Item, error)

	// LiveAgents returns the names of agents currently running. Required; a
	// failure is ErrNoLiveness.
	LiveAgents func() (map[string]bool, error)

	// QueuedBranches returns the branches already awaiting merge, keyed by
	// QueueKey(repo, branch). Optional: nil means "not consulted", an error means
	// "could not be consulted" — the report distinguishes them.
	QueuedBranches func() (map[string]bool, error)

	// Worktrees enumerates the polecat worktrees present on this host. Optional:
	// nil means "not consulted", and the report's frame says so rather than
	// implying there are none.
	//
	// IT IS THE SECOND SOURCE OF REPOSITORIES, and without it the sweep's repo set
	// is exactly "the repositories the open items name" — a set that cannot
	// contain a repository whose only polecat work belongs to a closed item
	// (mg-ded2). It is also the population bound for KindOrphanBranch: worktrees
	// are enumerable from disk and there were 46 of them on 2026-08-19, against
	// 435 polecat branches in a single repository.
	Worktrees func() ([]Worktree, error)

	// Repos restricts the sweep to these repositories.
	//
	// Empty means every repository the open items name UNIONED WITH every
	// repository a polecat worktree on this host points at. A non-empty value that
	// matches none of those is not a narrower sweep, it is a blind one — see
	// Report.Blind and UnmatchedRepo.
	Repos []string

	// Fetch refreshes each repository's remote-tracking refs first. Stale refs
	// make the answer wrong in BOTH directions — a target behind origin reports
	// merged work as stranded, and a branch pushed from another clone is
	// invisible — so it is on by default in the CLI. A failed fetch never stops
	// the sweep; it is recorded on the RepoCoverage.
	Fetch bool

	// Target overrides the ref branches are compared against. Empty resolves each
	// repository's default branch.
	Target string
}

// QueueKey is the identity of a branch awaiting merge: a branch name alone is
// ambiguous across repositories.
func QueueKey(repo, branch string) string { return repo + "\x00" + branch }

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// Scan performs one sweep.
func Scan(opts Options) (Report, error) {
	var rep Report
	if opts.Items == nil {
		return rep, fmt.Errorf("no work-item source configured")
	}
	if opts.LiveAgents == nil {
		return rep, fmt.Errorf("%w: no liveness source configured", ErrNoLiveness)
	}
	live, err := opts.LiveAgents()
	if err != nil {
		return rep, fmt.Errorf("%w: %v", ErrNoLiveness, err)
	}
	items, err := opts.Items()
	if err != nil {
		return rep, fmt.Errorf("listing open work items: %w", err)
	}

	queued := map[string]bool{}
	if opts.QueuedBranches != nil {
		q, qerr := opts.QueuedBranches()
		if qerr != nil {
			rep.QueueUnreadable = qerr.Error()
		} else {
			queued = q
		}
	}

	// Group by repository: the branch listing and the fetch are per-repo costs
	// paid once, not once per item.
	only := map[string]bool{}
	for _, r := range opts.Repos {
		only[r] = true
	}
	rep.ReposRequested = append([]string(nil), opts.Repos...)

	byRepo := map[string][]Item{}
	inSweep := map[string]bool{}
	var order []string
	addRepo := func(repo string) {
		if !inSweep[repo] {
			inSweep[repo] = true
			order = append(order, repo)
		}
	}
	for _, it := range items {
		rep.ItemsScanned++
		if it.Repo == "" {
			// A ROW, not just a count (mg-8baa). This branch used to increment and
			// `continue`, which put the item in a prose line and nowhere else: it
			// never reached Rows, so Actionable() stayed false and the command
			// exited 0 over an item nothing had looked at.
			rep.ItemsWithoutRepo++
			rep.Rows = append(rep.Rows, Row{
				Item: it, Kind: KindRepoUnreadable, Target: opts.Target,
				Error: "the work item names no repository",
			})
			continue
		}
		if len(only) > 0 && !only[it.Repo] {
			rep.ItemsOutOfScope++
			continue
		}
		addRepo(it.Repo)
		byRepo[it.Repo] = append(byRepo[it.Repo], it)
	}
	namedByItem := map[string]bool{}
	for r := range byRepo {
		namedByItem[r] = true
	}

	// THE SECOND SOURCE OF REPOSITORIES (mg-ded2). Discovering repositories only
	// from the open items' repo fields means a repository whose polecat work all
	// belongs to CLOSED items produces no row at all — not in repos[], not as an
	// error, not as a count — and the output is indistinguishable from one that
	// scanned every repository on the box. Worktrees are the other end of the same
	// fact, they are enumerable from disk, and they are bounded.
	byWorktree := map[string][]Worktree{}
	if opts.Worktrees != nil {
		wts, werr := opts.Worktrees()
		if werr != nil {
			// STATED, NOT FATAL, and stated in the frame as well as here: with the
			// enumeration missing, "0 repositories discovered" means "the question
			// failed" and not "there are none", and those must not render alike.
			rep.WorktreesUnreadable = werr.Error()
		}
		for _, w := range wts {
			rep.WorktreesSeen++
			if w.Repo == "" || w.Branch == "" {
				// STATED, NOT DROPPED. A worktree whose repository could not be
				// named, or that has no branch to measure, is precisely a member of
				// the population leaving the report without appearing in it — the
				// shape this ticket is about, one level down and inside its own fix.
				rep.WorktreesUnreadableList = append(rep.WorktreesUnreadableList, Worktree{
					Path: w.Path, Repo: w.Repo, Branch: w.Branch,
					Error: orDefault(w.Error, "no repository could be named for this worktree"),
				})
				continue
			}
			if len(only) > 0 && !only[w.Repo] {
				continue
			}
			rep.WorktreesInScope++
			byWorktree[w.Repo] = append(byWorktree[w.Repo], w)
			addRepo(w.Repo)
		}
	}

	// A --repo argument the sweep has not otherwise heard of is probed directly,
	// so `--repo <a real repository with no open items>` is a narrower sweep and
	// `--repo <a bare name or a typo>` is a blind one. Before this both were the
	// same all-clear (see UnmatchedRepo).
	for _, r := range opts.Repos {
		if inSweep[r] {
			continue
		}
		if _, perr := strandedwork.PolecatBranches(r); perr != nil {
			rep.ReposUnmatched = append(rep.ReposUnmatched, UnmatchedRepo{Repo: r, Error: perr.Error()})
			continue
		}
		addRepo(r)
	}
	sort.Strings(order)

	for _, repo := range order {
		cov := RepoCoverage{
			Repo:            repo,
			Items:           len(byRepo[repo]),
			Worktrees:       len(byWorktree[repo]),
			NamedByOpenItem: namedByItem[repo],
		}
		if !cov.NamedByOpenItem {
			rep.ReposDiscovered++
		}
		if opts.Fetch {
			if _, ferr := strandedwork.Fetch(repo); ferr == nil {
				cov.Fetched = true
			}
		}
		branches, berr := strandedwork.PolecatBranches(repo)
		if berr != nil {
			// THE JOIN THIS SWEEP REPORTS ON IS ITEMS, AND THE FAILURE HAS TO CROSS
			// IT (mg-8baa). Recording the error on the RepoCoverage — which is all
			// this used to do — states the failure in the units the sweep does not
			// report in. Every item in this repo then vanished from the population
			// without appearing in any column, while the header went on counting
			// them as scanned. One row per item, each nameable and each carrying
			// the repo's own error.
			cov.Error = berr.Error()
			rep.Repos = append(rep.Repos, cov)
			for _, it := range byRepo[repo] {
				rep.Rows = append(rep.Rows, Row{
					Item: it, Kind: KindRepoUnreadable, Target: opts.Target,
					Error: berr.Error(),
				})
			}
			continue
		}
		cov.Branches = len(branches)
		rep.ItemsChecked += len(byRepo[repo])
		rep.Repos = append(rep.Repos, cov)

		for _, it := range byRepo[repo] {
			for _, branch := range branches {
				if !strandedwork.BranchMatchesItem(branch, it.ID) {
					continue
				}
				if reason := excludedBecause(branch, it, live, queued, repo); reason != "" {
					rep.Excluded = append(rep.Excluded, Excluded{ItemID: it.ID, Branch: branch, Reason: reason})
					continue
				}
				row, carried, rerr := classify(repo, branch, it, opts.Target)
				if rerr != nil {
					// NOT skipped, and not folded into either verdict — see
					// KindUnjudged. A branch nobody could read is a row.
					rep.InspectErrors = append(rep.InspectErrors,
						fmt.Sprintf("%s (%s): %v", branch, it.ID, rerr))
					rep.Rows = append(rep.Rows, Row{
						Item: it, Branch: branch, Kind: KindUnjudged,
						Target: opts.Target, Error: rerr.Error(),
					})
					continue
				}
				if carried != "" {
					rep.Excluded = append(rep.Excluded, Excluded{ItemID: it.ID, Branch: branch, Reason: carried})
					continue
				}
				if row != nil {
					rep.Rows = append(rep.Rows, *row)
				}
			}
		}

		rep.Rows = append(rep.Rows, orphanBranchRows(repo, byWorktree[repo], items, live)...)
	}

	rep.Frame = BuildFrame(rep, opts)

	sort.SliceStable(rep.Rows, func(i, j int) bool {
		a, b := rep.Rows[i], rep.Rows[j]
		if sa, sb := statusRank(a.Item.Status), statusRank(b.Item.Status); sa != sb {
			return sa < sb
		}
		if a.Kind.Rank() != b.Kind.Rank() {
			return a.Kind.Rank() < b.Kind.Rank()
		}
		if a.Item.ID != b.Item.ID {
			return a.Item.ID < b.Item.ID
		}
		return a.Branch < b.Branch
	})
	return rep, nil
}

// excludedBecause names the reason a matched branch is not reported, or "".
//
// The two exclusions are the ones the ticket named, and each is a state that
// looks IDENTICAL to a strand from the outside:
//
//   - a running polecat's branch. It has unmerged commits on a claimed item
//     because that is what work in progress is. polecat-qfa70 was mid-flight
//     during the mayor's manual sweep and was indistinguishable from a strand.
//   - a branch already in the refinery queue. The remedy for a stranded branch is
//     "submit it", and it is already submitted.
//
// Both match on the BRANCH, not on the item, with one addition: a branch whose
// item is claimed by a live polecat is also in flight even when the branch name
// does not match that polecat's agent name (an agent renamed around a collision).
// Over-excluding here costs a missed report on an item somebody is actively
// working; under-excluding costs a false alarm on every live worker.
func excludedBecause(branch string, it Item, live, queued map[string]bool, repo string) string {
	name := strings.TrimPrefix(branch, strandedwork.BranchPrefix)
	if live[name] {
		return fmt.Sprintf("polecat %s is running on this branch", name)
	}
	for agentName := range live {
		if strandedwork.BranchMatchesItem(strandedwork.BranchPrefix+agentName, it.ID) {
			return fmt.Sprintf("polecat %s is running on work item %s", agentName, it.ID)
		}
	}
	if queued[QueueKey(repo, branch)] {
		return "already in the refinery queue"
	}
	return ""
}

// classify turns one (repo, branch, item) into a row, or nil when there is
// nothing to say.
//
// NOTHING TO SAY has exactly two shapes and neither is a finding:
//
//   - the branch does not exist (Found is false). The item names no work.
//   - the branch exists and is EMPTY relative to the target. A polecat that was
//     spawned and pushed nothing leaves such a branch; so does the ordinary
//     start of every polecat's life. It is not "landed", because nothing landed.
//
// The second is why the landed-not-closed row requires Equivalent > 0 rather
// than merely Unmerged == 0. Without that term every open item that ever had a
// polecat spawned at it — including one spawned thirty seconds ago — would be
// reported as work waiting to be closed.
//
// AND THAT TERM HAS A KNOWN, IRREDUCIBLE BLIND SPOT, stated here rather than
// discovered later. A branch that merged while NOTHING ELSE LANDED IN BETWEEN
// leaves the two refs pointing at the same commit: the rebase was a no-op and the
// fast-forward moved the target onto the branch's own sha. `git cherry` then
// reports neither an unmerged commit nor an equivalent one, because no commit is
// unique to either side, so this row stays silent. That state is
// INDISTINGUISHABLE FROM A BRANCH CREATED AND NEVER COMMITTED ON — both are "the
// branch ref equals the target ref" — so no rule over refs alone can separate
// them, and a rule that guessed would report every freshly spawned polecat. The
// case is narrow (it needs a quiet target at the moment of merge) and the
// upstream repair in cmd/pogod/reap.go closes the item without consulting git at
// all, which is the other reason that repair is the primary one.
// A THIRD SHAPE IS AN EXCLUSION RATHER THAN A ROW, and it is why this returns a
// reason string as well (mg-1af2). A reviewer reviews by checking the branch
// under review out, so its own worktree branch is a POINTER at the builder's
// head — every commit on it is work the target does not have, all of it owned
// and carried by the builder's branch. Reported as `stranded`, its remedy
// (`refinery submit <reviewer branch>`) submits the builder's work a second time
// under the wrong authorship. It goes in Excluded rather than being dropped,
// because a suppression nobody can see is indistinguishable from a miss.
func classify(repo, branch string, it Item, target string) (*Row, string, error) {
	f, err := strandedwork.Inspect(repo, branch, target)
	if err != nil {
		return nil, "", err
	}
	if !f.Found {
		return nil, "", nil
	}
	if f.Disposition == strandedwork.DispositionCarried {
		return nil, fmt.Sprintf("branch is a pointer at %s, which owns these commits (%s)",
			f.Carrier, f.WorkItemID), nil
	}
	row := Row{
		Item: it, Branch: branch, Ref: f.Ref, Pushed: f.Pushed, Target: f.Target,
		Unmerged: len(f.Unmerged), Equivalent: f.Equivalent,
		PreRegistration: f.PreRegistration,
	}
	for _, c := range f.Unmerged {
		row.Subjects = append(row.Subjects, c.Subject)
	}

	if len(f.Unmerged) == 0 {
		if f.Equivalent == 0 {
			return nil, "", nil
		}
		row.Kind = KindLandedNotClosed
		return &row, "", nil
	}

	// The content-level second opinion, and a failure to take it is NOT a clean
	// verdict: an unmeasurable branch stays STRANDED, not unjudged and not
	// landed. `git cherry` already answered the primary question here — the
	// finding stands with or without the second opinion, and the only thing lost
	// is the chance to downgrade it.
	if p, perr := strandedwork.MeasurePresence(repo, f); perr == nil {
		row.Presence = p
	}
	row.Kind = KindStranded
	if row.Presence.SuggestsLanded() {
		row.Kind = KindConflictSuspect
	}
	return &row, "", nil
}

// orphanBranchRows reports the polecat worktrees on this host whose branch holds
// commits no remote ref contains and which no OPEN work item names.
//
// THE THREE FILTERS ARE ALL NARROWING, AND EACH ONE IS LOAD-BEARING:
//
//   - a LIVE polecat's branch is skipped. A running worker has unpushed commits
//     because that is what work in progress is, and a detector that fires on
//     healthy input teaches its readers to skip the line it prints. Same rule,
//     same reason, as excludedBecause.
//   - a branch some OPEN item names is skipped. That branch is the item join's
//     to report, and reporting it twice under two Kinds with two different
//     remedies is worse than reporting it once. The match is against EVERY open
//     item and not only this repository's, because an item whose repo field is a
//     bare name is in no repository's bucket and would otherwise be reported here
//     as ownerless.
//   - a branch with no local-only commits is skipped. That is the whole
//     difference between a bounded row and an unbounded one: `git cherry` calls
//     435 polecat branches unmerged in one repository on this box, while exactly
//     one of the 46 polecat worktrees present held a commit on no remote ref.
//
// A FAILED MEASUREMENT IS A ROW, for the reason KindUnjudged exists: the natural
// shape of this predicate answers "nothing local-only" whenever git fails, and
// that is the direction that converts an orphan into an all-clear.
func orphanBranchRows(repo string, worktrees []Worktree, items []Item, live map[string]bool) []Row {
	var rows []Row
	seen := map[string]bool{}
	for _, w := range worktrees {
		if w.Branch == "" || !strings.HasPrefix(w.Branch, strandedwork.BranchPrefix) {
			continue
		}
		if seen[w.Branch] {
			continue
		}
		seen[w.Branch] = true
		if live[strings.TrimPrefix(w.Branch, strandedwork.BranchPrefix)] {
			continue
		}
		if claimedByOpenItem(w.Branch, items) {
			continue
		}
		commits, err := strandedwork.LocalOnlyCommits(repo, w.Branch)
		if err != nil {
			rows = append(rows, Row{
				Item: Item{Repo: repo}, Branch: w.Branch, Kind: KindUnjudged,
				Ref: "refs/heads/" + w.Branch, Error: err.Error(),
			})
			continue
		}
		if len(commits) == 0 {
			continue
		}
		row := Row{
			Item: Item{Repo: repo}, Branch: w.Branch, Kind: KindOrphanBranch,
			Ref: "refs/heads/" + w.Branch, Unmerged: len(commits),
		}
		for _, c := range commits {
			row.Subjects = append(row.Subjects, c.Subject)
		}
		rows = append(rows, row)
	}
	return rows
}

// claimedByOpenItem reports whether any open item's id matches this branch, by
// the same naming rule the item join uses.
func claimedByOpenItem(branch string, items []Item) bool {
	for _, it := range items {
		if strandedwork.BranchMatchesItem(branch, it.ID) {
			return true
		}
	}
	return false
}

// BuildFrame states what this report DOES NOT COVER, in the report.
//
// IT IS THE CHEAPEST OF THIS TICKET'S THREE FIXES AND THE ONE WITH THE WIDEST
// CATCH (mg-ded2). On 2026-08-19 the mayor read a sweep of 121 open items across
// 8 repositories as the population of at-risk work, submitted what it named, and
// moved on. The output was correct and it was read correctly; the failure was
// entirely in what the output did not say about itself — it says what it found,
// not what it cannot see, and a bounded answer with no stated bound gets read as
// a census. Neither of the row-level fixes would have caught the macguffin case:
// that worktree is clean, so no stranded-work row fires on it either. A frame
// naming "repositories no open item names" makes the omission visible without
// needing the tree to be dirty at all.
//
// AN INSTRUMENT THAT NAMES ITS BOUNDARY IS CHECKABLE; ONE THAT DOES NOT GETS READ
// AS A CENSUS. So this is unconditional: it prints on a clean run, on a blind
// run, and in the JSON, and it states what was ACTUALLY done on this run rather
// than a fixed paragraph — a boundary that does not move with the run is a claim
// that can rot exactly the way a documented one does.
func BuildFrame(rep Report, opts Options) []string {
	frame := []string{
		fmt.Sprintf("This report is a JOIN ON OPEN WORK ITEMS (%s). %d item(s) across %d repo(s).",
			strings.Join(OpenStatuses, ", "), rep.ItemsScanned, len(rep.Repos)),
	}
	switch {
	case opts.Worktrees == nil:
		frame = append(frame,
			"Branches whose work item is done, archived or shelved are OUTSIDE this report: "+
				"polecat worktrees were not enumerated on this run, so nothing here says anything "+
				"about them.",
			"Repositories no open work item names are OUTSIDE this report, for the same reason.")
	case rep.WorktreesUnreadable != "":
		frame = append(frame,
			fmt.Sprintf("Polecat worktrees COULD NOT BE ENUMERATED (%s), so the repos discovered "+
				"from them is 0 because the question failed, not because there are none.",
				rep.WorktreesUnreadable),
			"Branches of closed items and repositories no open item names are therefore OUTSIDE "+
				"this report on this run.")
	default:
		frame = append(frame,
			fmt.Sprintf("Branches whose work item is done, archived or shelved are outside the join. "+
				"%d polecat worktree(s) were enumerated on this host and %d entered this sweep; one "+
				"is reported only when its branch holds commits NO REMOTE REF CONTAINS, so a branch "+
				"of a closed item that is fully pushed is outside this report.",
				rep.WorktreesSeen, rep.WorktreesInScope),
			fmt.Sprintf("Polecat branches with NO worktree on this host — hundreds of them — were "+
				"NOT looked at, by any instrument in this run. %d repo(s) entered this sweep named "+
				"by no open item.", rep.ReposDiscovered))
		if n := len(rep.WorktreesUnreadableList); n > 0 {
			frame = append(frame, fmt.Sprintf("%d polecat worktree(s) COULD NOT BE READ and were "+
				"measured by nothing here — see worktrees_unreadable_list; they are named, not "+
				"dropped.", n))
		}
	}
	if len(opts.Repos) > 0 {
		frame = append(frame,
			fmt.Sprintf("--repo restricted this run to %s; %d open item(s) elsewhere were not looked at.",
				strings.Join(opts.Repos, ", "), rep.ItemsOutOfScope))
	}
	return frame
}
