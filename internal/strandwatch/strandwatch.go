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
	default:
		return 4
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
	default:
		return 3
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

// RepoCoverage records what happened to one repository, so the report can state
// its own reach rather than implying it saw everything.
type RepoCoverage struct {
	Repo     string `json:"repo"`
	Items    int    `json:"items"`
	Branches int    `json:"branches"`
	Fetched  bool   `json:"fetched"`
	Error    string `json:"error,omitempty"`
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
}

// Actionable reports whether this sweep found anything worth acting on.
func (r Report) Actionable() bool { return len(r.Rows) > 0 }

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

	// Repos restricts the sweep to these repositories. Empty means every
	// repository the open items name.
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
	byRepo := map[string][]Item{}
	var order []string
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
		if _, seen := byRepo[it.Repo]; !seen {
			order = append(order, it.Repo)
		}
		byRepo[it.Repo] = append(byRepo[it.Repo], it)
	}
	sort.Strings(order)

	for _, repo := range order {
		cov := RepoCoverage{Repo: repo, Items: len(byRepo[repo])}
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
	}

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
