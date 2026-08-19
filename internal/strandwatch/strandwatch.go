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
// AND THE STRANDED REMEDY WAS UNCONDITIONAL WITH RESPECT TO WHETHER THE BRANCH
// WAS EVER BUILT (mg-aed4). Branches carrying RESCUE commits — five named in the
// ticket, six on the full sweep of 2026-08-19 — work
// recovered from dead polecats' worktrees and committed with `--no-verify` on
// purpose, because a rescue of possibly-partial work must not be gated on whether
// that work compiles — were rendered identically to a branch genuinely ready to
// submit, each under a paste-ready `pogo refinery submit`, while each of those
// items' own body said in bold not to submit it. The report's closing caveat was
// already there and was not the missing piece: it applies to every row equally, so
// a reader who trusts it still cannot tell WHICH rows it is about. See
// KindRescueUnbuilt.
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
	"time"

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

	// KindRescueUnbuilt is a stranded row whose unmerged work is a RESCUE commit:
	// work recovered out of a dead polecat's preserved worktree and committed
	// with the pre-commit hook bypassed. It is KindStranded in every respect
	// except the one that matters — WHAT TO DO — and it exists because that
	// difference could not be seen (mg-aed4).
	//
	// THE TOOL AND THE TICKETS CONTRADICTED, AND THE TOOL IS THE RUNNABLE ONE. On
	// 2026-08-19 this sweep printed, for five branches,
	//
	//	-> pogo refinery submit polecat-p516e --repo=... --author=mg-516e   # do NOT dispatch at mg-516e
	//
	// while each of those five items' own body said, in bold, "Do not `pogo
	// refinery submit` this branch. It has never been built." Both artifacts were
	// correct about what they knew: the tickets knew the commits came from the
	// mg-51bf rescue and bypassed the hook DELIBERATELY, because a rescue of
	// possibly-partial work must not be gated on whether that work compiles; this
	// sweep knew only that commits existed on a branch and not on the target,
	// which is true. A prose caveat in a ticket does not reach the reader of a
	// paste-ready command.
	//
	// THE FAILURE MODE IS THE GATE PASSING, NOT THE GATE FAILING. A wasted submit
	// costs a gate run and a `failed` row somebody later has to interpret. A
	// PASSING gate merges half-implemented, never-reviewed rescue code to the
	// target on the authority of a command a detector printed — and a rescue
	// branch is precisely the population where "the gate passed" is the weakest
	// evidence available, because the commit deliberately bypassed the hook that
	// would have had an opinion.
	//
	// THE REPORT'S GENERAL CAVEAT WAS NOT THE MISSING PIECE. It already ended
	// with "This command submitted nothing and closed nothing... both are
	// destructive in the wrong direction, so they stay with a reader", and it
	// still does. What it lacked was any PER-BRANCH signal: the five unbuildable
	// rescue branches rendered identically to a branch genuinely ready to submit,
	// so a reader who trusted the caveat had nothing telling them WHICH rows it
	// applied to. That is what a separate Kind buys — a distinct label, a
	// distinct count in the findings header, and a distinct remedy.
	//
	// IT IS NOT mg-441f AND IT IS NOT mg-ba32. mg-441f is the remedy not
	// consulting REFINERY HISTORY, so it prints submit for a branch the refinery
	// ALREADY REFUSED — a different cause. The mayor measured that it is not what
	// is happening here: none of the five branches has ever been submitted at all,
	// against a positive control on the history query. That figure is theirs and is
	// not re-derived here. mg-ba32 is the SPAWN-time guard having no rescue cell —
	// a different instrument. This is the third claim: the remedy was unconditional
	// with respect to whether the branch was ever BUILT.
	//
	// THE REMEDY IS DELIBERATELY NOT A PASTE-READY SUBMIT, and withholding that
	// one string is the repair. See Row.Remedy.
	KindRescueUnbuilt Kind = "rescue_unbuilt"

	// KindRefusedBefore is a stranded row whose branch HAS ALREADY BEEN THROUGH
	// THE REFINERY and was refused, with a failure the refinery's own record
	// commits to reproducing. Resubmitting it unchanged re-runs the same refusal.
	//
	// THE REMEDY WAS COMPUTED FROM r.Pushed AND NOTHING ELSE (mg-441f). For
	// mg-5058 this sweep printed
	//
	//	-> pogo refinery submit polecat-p5058 ...
	//
	// as its single recommended action, for a branch the refinery had already
	// failed at stage=rebase on a content conflict, classed `defect`, and
	// explicitly declined to retry — its recorded reason being, verbatim,
	// "Resubmitting unchanged re-runs the same conflict forever; the branch has to
	// be rebased and the conflict resolved by hand before it can land". The tool
	// printed, with confidence and as the one thing to do, the one command that
	// provably cannot work. (That observation is the mayor's, 2026-08-14, and is
	// not re-derived here. A fresh instance WAS measured for this change: mg-6b2d,
	// open on 2026-08-19, whose branch polecat-p6b2d carries the same
	// stage=rebase/class=defect record and which the pre-change build printed a
	// bare submit line for.)
	//
	// IT IS THE THIRD CORRECTION TO ONE LINE AND THEY DO NOT IMPLY EACH OTHER.
	// mg-bfe0 taught it that a branch not on origin needs a push first, because
	// submit refuses it. mg-aed4 taught it that a RESCUE branch has never been
	// built, so no submit may be printed at all. This is a third external fact the
	// remedy depends on and did not read — and it catches neither of the others:
	// the five mg-51bf rescue branches have no refinery history whatsoever, and a
	// refused branch is usually perfectly well pushed.
	//
	// THE SEVERITY IS LOWER THAN mg-aed4'S AND SAYING SO IS PART OF THE RECORD. A
	// rescue branch that a gate PASSES merges never-reviewed code; a refused branch
	// resubmitted unchanged costs a gate run and a second `failed` row for somebody
	// to interpret. The remedy is still withheld, for mg-bfe0's reason rather than
	// mg-aed4's: a prose caveat beside a runnable command loses to the command, and
	// here the runnable command is known-futile rather than merely unqualified.
	//
	// WHAT IT REQUIRES, AND EACH TERM IS LOAD-BEARING:
	//
	//   - a COMPLETED merge request for this (repo, branch), failed;
	//   - whose class COMMITS to repeating — see
	//     refinery.FailureClass.ResubmitUnchangedRepeats. An infrastructure or
	//     contention failure establishes nothing about the branch and its correct
	//     remedy IS the bare resubmit, so those rows stay `stranded` and merely
	//     say that a prior attempt failed;
	//   - submitted AT OR AFTER the branch's tip commit. A branch that was refused,
	//     fixed and pushed again is an ordinary resubmit, and suppressing its
	//     remedy would be this ticket's own defect in the opposite direction — a
	//     remedy computed from a fact that has expired. See Row.PriorStale.
	//
	// WHEN ANY OF THOSE CANNOT BE ESTABLISHED the row stays `stranded` and says on
	// its own line WHY the history question has no conclusive answer, rather than
	// falling back silently to the bare submit. That is mg-8baa's lesson applied
	// here before it could be re-learned: "no record" and "record aged out of the
	// retained window" must not render alike. See Row.HistoryGap.
	KindRefusedBefore Kind = "refused_before"

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
	case KindRescueUnbuilt:
		// AHEAD OF STRANDED, on the same precedence argument DispositionPreRegistration
		// makes: when both descriptions fit, the row whose ordinary remedy is
		// destructive has to be the one the reader reaches first. A rescue row read
		// as an ordinary stranded row gets a submit it must not have; an ordinary
		// stranded row can never be read as a rescue one, because the marker is on
		// the commit.
		return 0
	case KindRefusedBefore:
		// AHEAD OF STRANDED on the same argument, one notch weaker. A refused row
		// read as an ordinary stranded row gets a command that cannot work; an
		// ordinary stranded row can never be read as a refused one, because the
		// marker is in the refinery's record. It sits BEHIND rescue_unbuilt because
		// a wasted gate run is not a merge of unreviewed code.
		return 1
	case KindStranded:
		return 2
	case KindUnjudged:
		return 3
	case KindRepoUnreadable:
		return 4
	case KindConflictSuspect:
		return 5
	case KindOrphanBranch:
		return 6
	default:
		return 7
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

// PriorSubmission is one COMPLETED merge request the refinery still remembers
// for a branch: the record that says this branch has already been through the
// queue and how that came out.
//
// IT IS A PLAIN STRUCT AND NOT refinery.MergeRequest, for the reason every other
// external fact in this package is injected: the detector has to be exercisable
// against a synthetic refinery, and importing the refinery would make the sweep
// testable only against a real one. The two fields that carry the refinery's
// JUDGEMENT rather than its data — Triage and Repeats — are computed at the
// injection site from the refinery's own table, so a class added there flows
// through without this package changing. See
// refinery.FailureClass.ResubmitUnchangedRepeats.
type PriorSubmission struct {
	MR     string `json:"mr"`
	Status string `json:"status"`
	// Stage is the step that refused — "rebase", "build", "fetch". Taken from
	// the LAST recorded attempt, which is the one that ended the request.
	Stage string `json:"stage,omitempty"`
	// Class is the refinery's failure classification.
	Class string `json:"class,omitempty"`
	// Target is the ref that request was aimed at.
	//
	// IT IS REPORTED RATHER THAN MATCHED ON, and that is a stated bound rather
	// than an oversight. This sweep keys a branch by (repo, branch) — the same
	// identity the refinery queue exclusion uses — so a refusal recorded against
	// `main` is attached to a row that may be comparing the branch against some
	// other --target. Comparing the two would mean reconciling a submitter's short
	// ref with a resolved remote-tracking ref, and a comparison that got that
	// wrong would silently drop a real refusal. Naming the target on the row lets
	// a reader see the mismatch in the one case it arises; a rule that guessed
	// would hide it in all of them.
	Target string `json:"target,omitempty"`
	// Reason is the refinery's own not-retried reason, VERBATIM. It is the most
	// useful string in this record and it is never summarised: for a rebase
	// conflict it already reads "Resubmitting unchanged re-runs the same conflict
	// forever; the branch has to be rebased and the conflict resolved by hand
	// before it can land", which is the whole of what a reader of this row needs.
	Reason string `json:"reason,omitempty"`
	// Triage is the class's own triage note, VERBATIM.
	Triage string `json:"triage,omitempty"`
	// Error is the terminal error text.
	Error string `json:"error,omitempty"`
	// Attempts is how many attempts the request took.
	Attempts int `json:"attempts,omitempty"`
	// SubmittedAt is when the request entered the queue; FinishedAt when it
	// resolved. The first is what dates the record against the branch's tip —
	// what was submitted is what existed when it was submitted.
	SubmittedAt time.Time `json:"submitted_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	// Repeats is the refinery's commitment that an UNCHANGED resubmit gets the
	// same answer. It is NOT "was it retried": a retry budget that ran out leaves
	// a retryable failure un-retried, and reading that as futility would suppress
	// the one remedy that works.
	Repeats bool `json:"repeats"`
}

// Refuses reports whether this record stands against submitting the branch again
// unchanged.
func (p *PriorSubmission) Refuses() bool {
	return p != nil && p.Status == "failed" && p.Repeats
}

// RefineryHistory is the sweep's view of the refinery's completed merge
// requests: what it remembers, and HOW FAR BACK it remembers.
//
// THE WINDOW IS THE HALF THAT IS EASY TO DROP (mg-8baa's lesson, applied before
// it could be re-learned here). The refinery prunes completed requests
// destructively past a count cap and an age cap, so "this branch has no record"
// and "this branch's record has been deleted" are the same absence from the same
// map. A check that read only the map would answer "never submitted" for a branch
// refused a fortnight ago and print the bare submit line with the same confidence
// as for a branch genuinely ready to go.
//
// Floor is what separates them, and it makes the answer conclusive far more often
// than a bare "the window is truncated" flag would: a branch whose TIP is newer
// than the floor cannot have been submitted-since-that-tip outside the window,
// because every such submission would have completed inside it. So a truncated
// window still answers conclusively for every branch committed to since the
// window opened, which on this fleet is most of them.
type RefineryHistory struct {
	// Latest maps QueueKey(repo, branch) to the MOST RECENT completed merge
	// request for that branch. Only the most recent one bears on what to do now:
	// a branch that failed and was later merged is not refused, and a branch with
	// three old failures and one recent one is described by the recent one.
	Latest map[string]PriorSubmission `json:"latest,omitempty"`
	// Floor is the completion time of the OLDEST retained record — the moment
	// this window starts observing. Zero when the window holds nothing, which is
	// NOT "it observes everything": it observes nothing, and Covers says so.
	Floor time.Time `json:"floor,omitempty"`
	// Retention is the daemon's own description of its retention bound, for the
	// frame. Empty when it could not be asked.
	Retention string `json:"retention,omitempty"`
	// Records is how many completed requests the window holds.
	Records int `json:"records"`
}

// Covers reports whether this window observes every submission that could have
// been made after t. It is false for an unknown t and for an empty window: the
// strong claim needs evidence, and neither supplies any.
func (h RefineryHistory) Covers(t time.Time) bool {
	return !h.Floor.IsZero() && !t.IsZero() && !t.Before(h.Floor)
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

	// Rescue is the oldest unmerged RESCUE commit, when the branch carries one:
	// the evidence that this work was committed with the pre-commit hook bypassed
	// and has never been built. See KindRescueUnbuilt.
	//
	// IT OUTLIVES THE KIND, and only the stranded cell becomes KindRescueUnbuilt.
	// A conflict_suspect row keeps its own Kind because its own remedy is already
	// the right one — and it carries something the rescue row cannot, that the
	// target may ALREADY hold this work. But "this branch is unreviewed rescue
	// work" is worth saying on any row it is true of, so the field is set
	// regardless and the renderer prints it either way.
	//
	// A landed_not_closed row can never carry it: this is populated from UNMERGED
	// commits and that row has none, which is the same rule that keeps the label
	// from becoming permanent once a rescue branch actually merges.
	Rescue *strandedwork.Commit `json:"rescue,omitempty"`

	// Declared is the oldest unmerged commit whose SUBJECT declares its content
	// unreviewed, when the branch carries one (mg-0c37).
	//
	// IT IS NOT THE SAME PREDICATE AS Rescue AND IT IS NOT REDUNDANT AGAINST IT.
	// Rescue matches the `RESCUE(` marker, which is a convention about HOW a
	// commit was made; this matches the words an author wrote about whether
	// anybody has read the result. The two coincide on today's rescue commits and
	// come apart in both directions: the mg-11fa rescue spelling carries no
	// UNREVIEWED token (27 of the 32 rescue commits in the fleet's repos), and an
	// ordinary hand-made commit can declare itself unreviewed without being a
	// rescue at all. A row is marked when EITHER is true.
	Declared *strandedwork.Commit `json:"declared_unreviewed,omitempty"`

	// Remainder is the oldest unmerged commit whose BODY names a specific
	// artifact that was NOT produced — the half of a rescue commit that no
	// version of this report printed, and the half that should become a successor
	// ticket (mg-0c37). See strandedwork.Commit.RemainderNote for why a hedge is
	// not one.
	Remainder *strandedwork.Commit `json:"remainder,omitempty"`

	// BodiesUnread is why the commit bodies could not be read, and "" when they
	// were. A row carrying it has NOT been checked for a remainder, and the
	// renderer says so rather than letting the absent flag read as a clean one.
	BodiesUnread string `json:"bodies_unread,omitempty"`

	// Subjects are the unmerged commit subjects, so a reader can recognise the
	// work without a git round-trip.
	Subjects []string `json:"subjects,omitempty"`

	// TipTime is when this branch was last committed to, and it is what dates
	// Prior. Zero when it could not be read.
	TipTime time.Time `json:"tip_time,omitempty"`

	// Prior is the most recent COMPLETED merge request the refinery remembers for
	// this branch, when it remembers one (mg-441f).
	//
	// IT OUTLIVES THE KIND, on the same rule as Rescue: only the stranded cell
	// becomes KindRefusedBefore, because that is the one cell whose remedy is a
	// paste-ready submit. A conflict_suspect row that was already refused keeps
	// its own Kind — its remedy already recommends neither action — but a reader
	// deciding what to do about it still needs to know the refinery has an
	// opinion, so the field is set and the renderer prints it either way.
	Prior *PriorSubmission `json:"prior,omitempty"`

	// PriorStale is true when Prior was submitted BEFORE this branch's tip
	// commit: the refinery refused content this branch no longer has, so the
	// record does not stand against submitting what is on it now.
	//
	// IT IS THE TERM THAT KEEPS THIS FIX FROM BECOMING ITS OWN DEFECT. Suppressing
	// the submit line on a stale refusal would withhold the correct remedy from a
	// branch that had already been fixed — a remedy computed from an expired fact,
	// which is the same error as a remedy computed from no fact at all.
	PriorStale bool `json:"prior_stale,omitempty"`

	// HistoryGap is why the refinery's records give no conclusive answer for this
	// branch, and "" when they do.
	//
	// It is a string and not a bool because the reasons are not interchangeable —
	// not consulted, unreadable, aged out of the retained window, and "the branch
	// could not be dated" are four different things a reader can act on
	// differently, and collapsing them is exactly the shape mg-8baa was filed
	// about.
	HistoryGap string `json:"history_gap,omitempty"`

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
	return withDeclaration(r.remedy(), r.DeclarationNote())
}

// DeclarationNote is the marker that rides ON THE REMEDY LINE, and "" when this
// row's commits declare nothing.
//
// IT GOES ON THE REMEDY BECAUSE THE REMEDY IS WHAT GETS COPIED (mg-0c37). The
// context lines above it are what gets skimmed: a batch of six branches was
// submitted on this report's recommendation without a commit message being read,
// and the declaration was on disk, in the commits being submitted, before the
// submit. Naming the fact one line higher had already failed by then — the
// subject line carrying it was clipped to `— UNREVI…`, which is the worst
// available outcome for a marker because it carries the fact and defeats it.
//
// It names a runnable command rather than saying "read the commit first",
// because "read it first" is the instruction that lost. The `git log -1` is the
// one command that shows both halves — the subject's declaration and the body's
// remainder — and it is short enough to sit inside a shell comment.
func (r Row) DeclarationNote() string {
	sha, what := "", ""
	switch {
	case r.Declared != nil && r.Remainder != nil:
		sha, what = r.Remainder.SHA, "COMMIT DECLARES ITSELF UNREVIEWED AND NAMES A REMAINDER"
	case r.Remainder != nil:
		sha, what = r.Remainder.SHA, "COMMIT BODY NAMES A REMAINDER"
	case r.Declared != nil:
		sha, what = r.Declared.SHA, "COMMIT DECLARES ITSELF UNREVIEWED"
	case r.Rescue != nil && r.Kind != KindRescueUnbuilt:
		// The mg-11fa rescue spelling — 27 of the 32 rescue commits in the fleet's
		// repos — carries no UNREVIEWED token at all, so nothing above would mark
		// it. This is the fact that has to travel with the command.
		//
		// NOT ON A KindRescueUnbuilt ROW, and only there is it withheld: that row's
		// own remedy already IS this sentence in stronger words ("READ IT, THEN
		// BUILD IT … has NEVER been built"), and repeating it in the same shell
		// comment spends the marker's attention on a line that already had it. A
		// conflict_suspect row carrying a rescue commit has no such sentence.
		sha, what = r.Rescue.SHA, "RESCUE COMMIT — NEVER BUILT, NEVER REVIEWED"
	default:
		return ""
	}
	repo := r.Item.Repo
	if repo == "" {
		repo = "."
	}
	return fmt.Sprintf("%s — read `git -C %s log -1 %s` FIRST", what, repo, short(sha))
}

// withDeclaration folds note into remedy's shell comment, AHEAD of whatever else
// that comment already says.
//
// Ahead, and not appended, because the comment is read left to right after a
// long command and the existing text is a different kind of caution — "do NOT
// dispatch at mg-xxxx" is about the work item, this is about whether the command
// should be run at all. Folding into the SAME comment rather than adding a
// second line keeps the remedy one copyable line, which is the property that
// made it the thing readers act on.
func withDeclaration(remedy, note string) string {
	if note == "" {
		return remedy
	}
	cmd, rest, found := strings.Cut(remedy, "   # ")
	if !found {
		return remedy + "   # " + note
	}
	return cmd + "   # " + note + "; " + rest
}

func (r Row) remedy() string {
	switch r.Kind {
	case KindRescueUnbuilt:
		// NO SUBMIT LINE, AND THAT OMISSION IS THE WHOLE REPAIR (mg-aed4). Every
		// other instrument in this package's blast radius learned from mg-bfe0 that
		// a prose caveat beside a runnable command loses to the command, and the fix
		// there was to CHAIN the missing prerequisite with `&&` so it could not be
		// skipped. That fix is unavailable here: the prerequisite is "somebody
		// builds and reads this", the build command is repo-specific, and there is
		// no string that makes a human review runnable. So the prerequisite is made
		// unskippable the only other way — the paste-ready submit is not printed at
		// all, for the one population where pasting it merges never-reviewed work.
		//
		// It is not withheld as a secret. The row above it names the branch, the
		// repo and the target, and the submit command is the ordinary stranded one
		// which this report prints on every other stranded row. What a reader cannot
		// do is paste it from HERE without having read the branch first, and a
		// reader who has read the branch does not need it printed.
		//
		// AND THE WITHHOLDING IS COMPLETE, INCLUDING IN THE EXPLANATION. The first
		// draft of this line said "NO `refinery submit` line is printed for this row
		// on purpose", which put the command's own name on the row it was being kept
		// off — this ticket's defect committed by its own repair, in miniature. The
		// text names no submittable command at all.
		sha := ""
		if r.Rescue != nil {
			sha = " " + short(r.Rescue.SHA)
		}
		return fmt.Sprintf("git -C %s log -p %s..%s   # READ IT, THEN BUILD IT. Rescue commit%s "+
			"bypassed the pre-commit hook and has NEVER been built; NO submit command is printed "+
			"for this row on purpose — if the gate passed it would merge unreviewed work to %s",
			r.Item.Repo, r.Target, orDefault(r.Ref, r.Branch), sha, r.Target)
	case KindRefusedBefore:
		// NO PASTE-READY SUBMIT, for mg-bfe0's reason rather than mg-aed4's. There
		// the withheld string was a command that would RUN and do damage; here it
		// is a command that runs and cannot work, and the fix mg-bfe0 used —
		// chaining the missing prerequisite with `&&` — is unavailable for the same
		// reason it was unavailable to mg-aed4: the prerequisite is a person
		// resolving a conflict or fixing what a gate found, and no string makes
		// that runnable. So the command printed is the one that shows the reader
		// what the refinery objected to, and the sequence that CAN work is stated
		// in words, unchained, because its middle step is not a command.
		//
		// The refinery's own not-retried reason is quoted verbatim and is the
		// substance of this line: for a rebase conflict it already says what has to
		// happen and says it better than a paraphrase would.
		return fmt.Sprintf("pogo refinery show %s   # ALREADY SUBMITTED and FAILED %s: %s "+
			"No submit command is printed for this row on purpose — the branch has to CHANGE "+
			"first (rebase onto %s, fix what that names, push), and only then is it a different "+
			"request",
			r.Prior.MR, r.priorWhere(), r.priorWhy(), r.Target)
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

// priorWhere names where and when the prior request came out, as a phrase that
// already carries its own leading preposition.
//
// The preposition has to move with the content because the phrase is used after
// two different verbs and a MERGED request has no stage: "FAILED at stage=rebase
// (class=defect) on ..." and "MERGED against main on ..." are both grammatical,
// while a fixed "at" would produce "MERGED at an unrecorded stage" — a sentence
// that invents a failure out of a missing field.
func (r Row) priorWhere() string {
	if r.Prior == nil {
		return "in an earlier merge request"
	}
	where := ""
	switch {
	case r.Prior.Stage != "" && r.Prior.Class != "":
		where = "at stage=" + r.Prior.Stage + " (class=" + r.Prior.Class + ")"
	case r.Prior.Stage != "":
		where = "at stage=" + r.Prior.Stage
	case r.Prior.Class != "":
		where = "with class=" + r.Prior.Class
	}
	if r.Prior.Target != "" {
		where = strings.TrimSpace(where + " against " + r.Prior.Target)
	}
	if !r.Prior.SubmittedAt.IsZero() {
		where = strings.TrimSpace(where + " on " + r.Prior.SubmittedAt.UTC().Format("2006-01-02T15:04Z"))
	}
	if where == "" {
		return "with nothing else recorded"
	}
	return where
}

// priorWhy is the refinery's own account of the failure, preferred in the order
// it was written to be read: the not-retried reason states what re-running would
// establish, the triage note states what the class commits to, and the raw error
// is the fallback when neither was recorded.
func (r Row) priorWhy() string {
	if r.Prior == nil {
		return ""
	}
	switch {
	case r.Prior.Reason != "":
		return ensureStop(r.Prior.Reason)
	case r.Prior.Triage != "":
		return ensureStop(r.Prior.Triage)
	case r.Prior.Error != "":
		return ensureStop(truncate(r.Prior.Error, 160))
	}
	return "the refinery recorded no reason."
}

func ensureStop(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
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
	// History is what the refinery remembered, and how far back. Zero when it was
	// not consulted; HistoryUnreadable is set when it was and failed.
	History RefineryHistory `json:"history"`
	// HistoryConsulted distinguishes "not asked" from "asked and empty". An empty
	// window and no window at all are the same zero value otherwise, and they
	// license different readings of every stranded row below.
	HistoryConsulted bool `json:"history_consulted"`
	// HistoryUnreadable is set when the refinery's history could not be read, so
	// no row could be checked against whether its branch was already refused.
	// Stated, not fatal — the queue's rule, for the same reason.
	HistoryUnreadable string `json:"history_unreadable,omitempty"`
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

	// History returns the refinery's completed merge requests, so a stranded
	// row's remedy can be checked against whether that branch has ALREADY been
	// submitted and refused. Optional: nil means "not consulted", an error means
	// "could not be consulted", and every stranded row says which — a remedy that
	// falls back silently to the bare submit is the defect mg-441f was filed for,
	// one level down.
	History func() (RefineryHistory, error)

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

	// The refinery's memory of what has ALREADY been submitted (mg-441f). Read
	// once for the whole sweep, like the queue, and never fatal: the rest of the
	// answer is worth having without it, and every row that needed it says so.
	hv := historyView{consulted: opts.History != nil}
	if opts.History != nil {
		h, herr := opts.History()
		if herr != nil {
			hv.err = herr.Error()
			rep.HistoryUnreadable = herr.Error()
		} else {
			hv.h = h
			rep.History = h
		}
	}
	rep.HistoryConsulted = hv.consulted

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
				row, carried, rerr := classify(repo, branch, it, opts.Target, hv)
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
func classify(repo, branch string, it Item, target string, hv historyView) (*Row, string, error) {
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
		Rescue:          f.Rescue,
		Declared:        f.FirstUnreviewed(),
		Remainder:       f.FirstRemainder(),
		BodiesUnread:    f.BodiesUnread,
		TipTime:         f.TipTime,
	}
	row.Prior, row.PriorStale, row.HistoryGap = hv.forBranch(repo, branch, f.TipTime, f.TipTimeError)
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
	// THE RESCUE KIND DISPLACES ONLY KindStranded, and the narrowness is the
	// point (mg-aed4). KindStranded is the one whose remedy is a paste-ready
	// `refinery submit`, so it is the one cell where an unbuilt branch is handed
	// a destructive command. conflict_suspect already recommends NEITHER remedy
	// and says so; overwriting it here would trade a correct "the two instruments
	// disagree, go and look" for a narrower statement, and would lose the fact
	// that the target may ALREADY hold this work. Row.Rescue stays set either
	// way, so the renderer says "unreviewed rescue work" on both.
	// THE REFUSED KIND DISPLACES ONLY KindStranded, on exactly the argument
	// KindRescueUnbuilt's promotion makes (mg-441f). KindStranded is the one cell
	// whose remedy is a paste-ready submit, so it is the one cell where a branch
	// the refinery has already refused is handed a command that cannot work.
	// conflict_suspect recommends neither remedy and says why; overwriting it here
	// would trade a correct "two instruments disagree, go and look" for a narrower
	// statement and would lose the fact that the target may already hold the work.
	// Row.Prior stays set either way, so the renderer reports the refusal on both.
	if row.Kind == KindStranded && row.Prior.Refuses() && !row.PriorStale {
		row.Kind = KindRefusedBefore
	}
	// AND RESCUE DISPLACES BOTH. The two causes are independent — a rescue branch
	// has typically never been submitted at all, and a refused branch is ordinary
	// built work — but where they coincide the rescue label is the one that must
	// survive: its failure mode is a PASSING gate merging unreviewed code, which
	// is worse than a wasted gate run, and its remedy (read it, build it) is a
	// prerequisite of the refused row's remedy anyway.
	if (row.Kind == KindStranded || row.Kind == KindRefusedBefore) && row.Rescue != nil {
		row.Kind = KindRescueUnbuilt
	}
	return &row, "", nil
}

// historyView is the sweep's answer source for "has this branch already been
// through the refinery", together with everything needed to say when it CANNOT
// answer.
type historyView struct {
	consulted bool
	err       string
	h         RefineryHistory
}

// forBranch returns the record that bears on submitting this branch as it now
// stands, whether that record is stale, and the reason there is no conclusive
// answer.
//
// EVERY BRANCH THAT RETURNS WITHOUT A CONCLUSIVE ANSWER GETS A REASON. That is
// the whole of mg-8baa's lesson carried into this instrument: the natural way to
// write this is a map lookup whose miss means "never submitted", and that answer
// is indistinguishable from "the record was pruned last Tuesday". Both produce
// the bare submit line, one of them correctly.
func (v historyView) forBranch(repo, branch string, tip time.Time, tipErr string) (*PriorSubmission, bool, string) {
	if !v.consulted {
		return nil, false, "the refinery's merge history was NOT consulted on this run, so nothing " +
			"here knows whether this branch has already been submitted and refused"
	}
	if v.err != "" {
		return nil, false, fmt.Sprintf("the refinery's merge history COULD NOT BE READ (%s), so "+
			"whether this branch has already been submitted and refused is unknown", v.err)
	}
	p, found := v.h.Latest[QueueKey(repo, branch)]

	// A branch that cannot be DATED cannot have a record dated against it. The
	// record is still reported — it is real and a reader wants it — but it cannot
	// be told from an expired one, so it does not promote the row.
	if tip.IsZero() {
		gap := "this branch's tip could not be dated"
		if tipErr != "" {
			gap += " (" + tipErr + ")"
		}
		gap += ", so a refinery record could not be told from one that predates the branch's " +
			"current commits"
		if found {
			return &p, false, gap
		}
		return nil, false, gap
	}

	if !found {
		if v.h.Covers(tip) {
			// The one conclusive negative: the window opened before this branch was
			// last written to, so any submission of what is on it now would be in
			// the window, and there is none.
			return nil, false, ""
		}
		if v.h.Records == 0 {
			return nil, false, "the refinery's retained merge history is EMPTY, so it observes no " +
				"submission of this branch either way"
		}
		return nil, false, fmt.Sprintf("the refinery's retained merge history begins %s, AFTER this "+
			"branch's last commit (%s) — a refusal of this branch in between has been pruned and "+
			"cannot be seen from here",
			v.h.Floor.UTC().Format("2006-01-02T15:04Z"), tip.UTC().Format("2006-01-02T15:04Z"))
	}

	// STALE means the refinery refused content this branch no longer has. The
	// record is reported and explicitly does NOT stand: withholding the remedy on
	// an expired fact is the same error as computing it from no fact at all, which
	// is what this whole check repairs.
	if p.SubmittedAt.IsZero() {
		// A record with no submit time is not a STALE record — it is an undated
		// one, and calling it stale would quietly clear a branch on the strength of
		// a missing field. Reported, with the gap that says so.
		return &p, false, fmt.Sprintf("the refinery's record for this branch (%s) carries no submit "+
			"time, so it could not be dated against the branch's own commits", p.MR)
	}
	if p.SubmittedAt.Before(tip) {
		return &p, true, ""
	}
	return &p, false, ""
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
		// An orphan branch is the row with NO OWNER TO ASK, so a self-declaration on
		// it is worth at least as much as one on an owned row: the reader deciding
		// what to do with it has no work item, no author and no ticket to read.
		if berr := strandedwork.LoadBodies(repo, commits); berr != nil {
			row.BodiesUnread = berr.Error()
		}
		row.Declared = firstDeclaring(commits, strandedwork.Commit.DeclaresUnreviewed)
		row.Remainder = firstDeclaring(commits, strandedwork.Commit.DeclaresRemainder)
		for _, c := range commits {
			row.Subjects = append(row.Subjects, c.Subject)
		}
		rows = append(rows, row)
	}
	return rows
}

// firstDeclaring returns the first commit satisfying pred, or nil. The
// orphan-branch path has no Finding to ask, so it asks the commits directly.
func firstDeclaring(commits []strandedwork.Commit, pred func(strandedwork.Commit) bool) *strandedwork.Commit {
	for i := range commits {
		if pred(commits[i]) {
			c := commits[i]
			return &c
		}
	}
	return nil
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
	// THE REFINERY'S MEMORY IS A WINDOW AND THE FRAME HAS TO SAY SO (mg-441f). A
	// stranded row's remedy is checked against whether that branch was already
	// refused, and that check can only see as far back as the refinery still
	// remembers. A report that used the check without stating its reach would
	// invite exactly the reading mg-8baa was filed about: a bounded answer read as
	// a census.
	switch {
	case !rep.HistoryConsulted:
		frame = append(frame,
			"The refinery's merge history was NOT consulted on this run, so no row here knows "+
				"whether its branch has already been submitted and refused.")
	case rep.HistoryUnreadable != "":
		frame = append(frame,
			fmt.Sprintf("The refinery's merge history COULD NOT BE READ (%s), so no row here knows "+
				"whether its branch has already been submitted and refused.", rep.HistoryUnreadable))
	default:
		floor := "nothing — the window is EMPTY"
		if !rep.History.Floor.IsZero() {
			floor = "back to " + rep.History.Floor.UTC().Format("2006-01-02T15:04Z")
		}
		retention := ""
		if rep.History.Retention != "" {
			retention = fmt.Sprintf(" (retention: %s)", rep.History.Retention)
		}
		frame = append(frame,
			fmt.Sprintf("The refinery's merge history was read and observes %s%s, over %d completed "+
				"request(s). A branch refused before that has been PRUNED and this report cannot see "+
				"it; every row whose answer depends on that says so on its own line.",
				floor, retention, rep.History.Records))
	}
	if len(opts.Repos) > 0 {
		frame = append(frame,
			fmt.Sprintf("--repo restricted this run to %s; %d open item(s) elsewhere were not looked at.",
				strings.Join(opts.Repos, ", "), rep.ItemsOutOfScope))
	}
	return frame
}
