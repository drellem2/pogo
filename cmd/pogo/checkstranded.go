package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/strandwatch"
)

// exitStrandedUsage is `pogo check-stranded`'s exit code for a malformed
// invocation, matching the rest of the check-* family (0 clean, 1 finding, 2
// usage, 3 measured nothing).
const exitStrandedUsage = 2

// newCheckStrandedCmd builds `pogo check-stranded` (mg-be37): the periodic half
// of the spawn-time stranded-work guard.
//
// It joins the check-* family — check-acks, check-commit-body, check-intake,
// check-mailloops, check-orphans, check-prompts, check-staleness,
// check-strandedmail, check-teardown, check-verdicts — whose membership
// criterion is A READ-ONLY DETECTOR THAT REPORTS A CONDITION AND TAKES NO
// ACTION.
func newCheckStrandedCmd(jsonOutput *bool) *cobra.Command {
	var (
		repos   []string
		target  string
		noFetch bool
		all     bool
	)
	cmd := &cobra.Command{
		Use:   "check-stranded",
		Short: "Report open work items whose work already exists on a branch (never submits, never closes)",
		Long: `Report every OPEN work item — available, claimed or pending — whose work already
exists on a polecat branch, either unmerged or already landed.

WHY IT IS PERIODIC. A spawn-time guard already refuses to dispatch a polecat at
an item with pushed, unmerged work, and it prevented real loss. But it is
TRIGGERED BY DISPATCH, so it can only fire when somebody tries to work the item.
On 2026-08-09 four branches were stranded across three repos: one was caught by
that guard, one by a person reconciling something else, and two by the accident
of somebody looking next door. From both directions the state is invisible — the
board reads ` + "`available`" + `, the repo holds finished work, and the polecat that did it
is dead so nothing is going to mail anybody. Meanwhile priority-wake advertises
the item, and the action it advertises re-derives the work. mg-9a19 lost 1026
lines that way.

TWO ROW TYPES, WITH OPPOSITE REMEDIES:

  stranded           the branch has commits the target does not.
                     -> ` + "`pogo refinery submit`" + `. Do NOT dispatch.
  landed_not_closed  the branch is fully merged and the item is still asking for
                     the work. -> ` + "`mg done`" + `.
  conflict_suspect   the two instruments below DISAGREE. -> read it yourself.
  unjudged           the branch could not be READ. -> re-run; this is not clean.
  repo_unreadable    the item's REPO could not be listed, so no branch was ever
                     looked for. -> fix the item's repo field; not a clean row.
  orphan_branch      a polecat WORKTREE on this host whose branch holds commits no
                     remote ref has, and NO open item names it. -> push it; there
                     is no owner to ask and nothing to submit it under.

The second row is the worse one and it needed its own repair: while a branch is
unmerged the spawn-time guard refuses the dispatch, but the moment it merges the
guard correctly stops refusing and the item is STILL available. pogod now closes
an author's item at merge whatever submitted the branch, so this row should be
empty in steady state — it is reported anyway, because that repair cannot see an
item stranded before it shipped or one whose close was refused.

THE INSTRUMENT, AND ITS MEASURED BLIND SPOT.
` + "`git rev-list --count main..<branch>`" + ` DOES NOT WORK and will report every healthy
merged branch as stranded forever: the refinery merges by rebasing, so a landed
branch's commits are all present under different shas. ` + "`git cherry`" + ` compares patch
ids and gets that right — but a patch id covers the diff's CONTEXT LINES, and the
refinery rebases into its own copy without force-pushing the branch. So origin
keeps the commit as written, the target gets it as replayed, and if the target
moved anywhere nearby the two hash differently and the branch reads unmerged
forever. NO CONFLICT IS REQUIRED, only a neighbouring change — the refinery in
fact ABORTS on a rebase conflict (mg-eac0) and never merges through one.

Measured on this repo on 2026-08-10: of 57 branches ` + "`git cherry`" + ` called unmerged, at
least five are on main under another sha. polecat-79dc is the exact control —
77e012c and 1e1292f have identical --stat, byte-identical added and removed
lines, and different patch ids.

So a second, content-level instrument runs on every stranded candidate: what
fraction of the substantive lines the unmerged commits ADD does the target
already contain? Context drift moves the lines around a change without touching
the change. A branch whose target already holds ≥95% of them is reported as
` + "`conflict_suspect`" + ` — which recommends NEITHER remedy, because both instruments are
heuristics and closing an unmerged branch throws the work away.

AND A FAILED READ IS ITS OWN ROW. The natural predicate — ` + "`git cherry … | grep -q`" + `
— answers CLEAN whenever git FAILS, because a failed git prints nothing. On a
sweep that silently turns a stranded branch into an all-clear. Anything that
could not be read is reported as ` + "`unjudged`" + `, counted separately, and exits 1.

IT IS ITEM-DRIVEN, NOT BRANCH-DRIVEN. A branch-first sweep of this repo finds 57
branches, 48 of them on archived items and 2 on no item at all. Walking the open
items instead produced three rows on the same store, one a live instance nothing
else had found. Rank is on ITEM STATUS — ` + "`available`" + ` first, because that is the
status priority-wake advertises — not on branch count.

EXCLUSIONS, all counted and all nameable with --all:

  running polecat   a live worker's branch has unmerged commits on a claimed item
                    because that is what work in progress looks like.
  refinery queue    the remedy for a stranded branch is to submit it, and it is
                    already submitted.
  pointer branch    a REVIEWER reviews by checking the branch under review out,
                    so its own branch points at the builder's head. Every commit
                    on it is the builder's, and this sweep's remedy would submit
                    them a second time under the reviewer's authorship. Excluded
                    only when another branch both CARRIES and OWNS them, because
                    containment alone is symmetric and would silence the builder
                    (mg-1af2).

THE HEADER STATES COVERAGE, NOT POPULATION (mg-8baa). It reads "N of M open work
item(s) CHECKED", and any shortfall is named on that same line. It used to read
"M open work item(s) scanned", which is the population enumerated rather than the
population looked at: on 2026-08-14 that printed 112 over a board three of whose
items sat in repos the sweep could not list — two named by a bare relative path
(` + "`repo: onethird_program`" + `) and one absolute but not a git repository. Those items
were dropped from the join without appearing in any column, the closing line read
"No open work item has work already sitting on a branch", and the command exited
0. Every unchecked item is now a ` + "`repo_unreadable`" + ` row with its own id and its own
error, so the shortfall reaches the exit code as well as the page.

THE UNIT OF REPORT USED TO BE THE OPEN WORK ITEM, AND THAT EXCLUDED THE
POPULATION MOST AT RISK (mg-ded2). Everything above is the item join, so three
things could not appear in this report at all: a branch whose item is already
CLOSED, a repository NO OPEN ITEM NAMES, and a ` + "`--repo`" + ` that MATCHED NOTHING.
Measured on 2026-08-19 — ` + "`polecat-pc-rev-c5d5a10`" + ` held a commit on no remote ref
inside a repository the sweep covered cleanly and was absent; a repository holding
a polecat worktree appeared nowhere at all, not even as an error; and
` + "`--repo one_third_width_three`" + `, ` + "`--repo this-repo-does-not-exist-anywhere`" + ` and a clean
repository printed byte-identical all-clears and exited 0.

Three repairs, and the cheapest has the widest catch:

  the FRAME       the report now states its own boundary, unconditionally and on
                  every run. Neither row-level fix below would have caught the
                  missing repository — its worktree is clean — but a frame naming
                  "repositories no open item names are outside this report" makes
                  the omission visible without needing the tree to be dirty. An
                  instrument that names its boundary is checkable; one that does
                  not gets read as a census.
  orphan_branch   a polecat WORKTREE still on this host whose branch holds commits
                  NO REMOTE REF CONTAINS and which no open item names. Bounded by
                  the worktree and not by the branch: ` + "`git cherry`" + ` calls 435 polecat
                  branches unmerged in one repository here, while 1 of the 46
                  worktrees present held a commit no remote had. "Also scan closed
                  items" is NOT the fix — that population is unbounded and
                  abandoned by design.
  --repo          a value naming no repository this sweep can see now exits
                  ` + fmt.Sprint(exitInstrumentFailure) + ` and says so. A wrong command that prints an all-clear does
                  not merely fail to check a claim, it manufactures support for
                  whichever claim it was quoted to support.

A run that COULD NOT LOOK says so instead of exiting clean. An unreachable agent
registry is fatal (exit ` + fmt.Sprint(exitInstrumentFailure) + `): without it every running polecat in the fleet
looks like a strand, and a detector that fires on healthy input teaches its
readers to skip the line. An unreadable refinery queue is stated in the report
rather than fatal — the queue being down is common and the rest of the answer is
still worth having.

REPORTS ONLY. It never submits and never closes. Both remedies are one command
by hand once you know, and each is destructive in the wrong direction.

Exit status: 0 nothing found, 1 at least one finding, ` + fmt.Sprint(exitStrandedUsage) + ` usage error, ` + fmt.Sprint(exitInstrumentFailure) + ` this
run measured nothing.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				fmt.Fprintf(os.Stderr, "check-stranded takes no positional arguments (got %q)\n", args[0])
				os.Exit(exitStrandedUsage)
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			rep, err := strandwatch.Scan(strandwatch.Options{
				Items:          openWorkItems,
				LiveAgents:     liveAgentNames,
				QueuedBranches: queuedRefineryBranches,
				Worktrees:      polecatWorktrees,
				Repos:          repos,
				Target:         target,
				Fetch:          !noFetch,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — this run measured nothing: %v\n", err)
				os.Exit(exitInstrumentFailure)
			}
			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(strandwatch.Render(rep, all))
			}
			// ORDER MATTERS: blindness outranks findings. A run that resolved no
			// repository cannot have found anything, so the two cannot both be true —
			// but if they ever could, "this measured nothing" is the answer a caller
			// must not be able to miss (mg-ded2).
			if why := rep.Blind(); why != "" {
				fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — %s\n", why)
				os.Exit(exitInstrumentFailure)
			}
			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().StringSliceVar(&repos, "repo", nil,
		"Restrict to these ABSOLUTE repository paths (default: every repo the open items name, "+
			"plus every repo a polecat worktree on this host points at). A value matching none of "+
			"those exits "+fmt.Sprint(exitInstrumentFailure)+" rather than printing an all-clear")
	cmd.Flags().StringVar(&target, "target", "",
		"Ref to compare branches against (default: each repo's default branch)")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false,
		"Skip the remote refresh; answer from the refs this clone last saw")
	cmd.Flags().BoolVar(&all, "all", false, "Also list the exclusions by name")

	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintf(os.Stderr, "check-stranded: %v\n\n%s", err, c.UsageString())
		os.Exit(exitStrandedUsage)
		return nil
	})
	return cmd
}

// openWorkItems lists the work items still asking for work, by shelling out to
// mg.
//
// It reads mg rather than internal/workitem because internal/workitem cannot see
// `pending` items' repo field consistently and, more importantly, because a
// claim renames the file to `<id>.md.<pid>` — the store's own layout is mg's
// business, and every other detector in this family that reimplemented it has
// had to be corrected once.
func openWorkItems() ([]strandwatch.Item, error) {
	var out []strandwatch.Item
	seen := map[string]bool{}
	for _, status := range strandwatch.OpenStatuses {
		raw, err := exec.Command("mg", "list", "--status="+status, "--json").Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
				return nil, fmt.Errorf("mg list --status=%s: %s", status, strings.TrimSpace(string(exitErr.Stderr)))
			}
			return nil, fmt.Errorf("mg list --status=%s: %w", status, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var it strandwatch.Item
			if err := json.Unmarshal([]byte(line), &it); err != nil {
				return nil, fmt.Errorf("parsing mg list --status=%s output: %w", status, err)
			}
			if it.ID == "" || seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			out = append(out, it)
		}
	}
	return out, nil
}

// liveAgentNames asks pogod which agents are running.
//
// `restarting` counts as alive for the same reason it does in check-orphans:
// that agent is coming back on the same work item, and its branch is not
// stranded. The error is propagated rather than swallowed — see
// strandwatch.ErrNoLiveness for why this one is fatal.
func liveAgentNames() (map[string]bool, error) {
	agents, err := client.ListAgents()
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(agents))
	for _, a := range agents {
		if a.Status == agent.StatusRunning || a.Status == agent.StatusRestarting {
			live[a.Name] = true
		}
	}
	return live, nil
}

// queuedRefineryBranches returns the branches already awaiting merge.
//
// Keyed by (repo, branch): a branch name alone is ambiguous across the three
// repos this fleet works, and polecat branch names are derived from work-item
// ids that are only 4 hex digits wide.
func queuedRefineryBranches() (map[string]bool, error) {
	queue, err := client.GetRefineryQueue()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(queue))
	for _, mr := range queue {
		out[strandwatch.QueueKey(mr.RepoPath, mr.Branch)] = true
	}
	return out, nil
}

// polecatWorktrees enumerates the polecat worktrees present on this host, from
// disk.
//
// IT READS THE DIRECTORY AND NOT THE BOARD, and that is the whole point
// (mg-ded2). Every other source of repositories in this command is downstream of
// the open work items, so none of them can name a repository whose polecat work
// all belongs to closed items — which is exactly how a repository holding a
// polecat worktree came to appear NOWHERE in a report that read as a full sweep.
//
// The REPO is taken from git's own answer (`rev-parse --git-common-dir`) rather
// than from the agent registry, for the reason gitgc.PolecatNameForWorktree
// gives about the inverse case: a fact about whose tree this is must come from
// the tree. A registry entry can be gone while the directory is still on disk —
// that is the state this enumerator exists to find.
//
// A directory that is not a live worktree is SKIPPED and not an error. 19 of the
// 58 entries under the polecats dir on 2026-08-19 were reaped shells whose git
// registration is gone; they hold no branch and therefore no commit. Reclaiming
// those directories is gitgc's orphan-dir scan, not this sweep's.
func polecatWorktrees() ([]strandwatch.Worktree, error) {
	dir, err := gitgc.DefaultPolecatsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// No polecats dir is a ZERO and not a failure: there are no worktrees.
			// The distinction reaches the frame, which says "the question failed"
			// only for a real error.
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []strandwatch.Worktree
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		hasGit := false
		if _, serr := os.Stat(filepath.Join(path, ".git")); serr == nil {
			hasGit = true
		}
		common, cerr := exec.Command("git", "-C", path,
			"rev-parse", "--path-format=absolute", "--git-common-dir").Output()
		if cerr != nil {
			if !hasGit {
				continue // a reaped shell: no .git, no branch, nothing to lose
			}
			// A DIRECTORY THAT HAS A .git AND STILL WOULD NOT ANSWER is not a
			// reaped shell, and collapsing the two would be this ticket's own
			// defect committed by its own repair: the population would lose a
			// member silently and the count would go on reading as coverage.
			// Reported with an empty Repo, which the sweep renders as unreadable
			// rather than dropping.
			out = append(out, strandwatch.Worktree{
				Path:  path,
				Error: fmt.Sprintf("has a .git but git would not answer: %v", cerr),
			})
			continue
		}
		gitDir := strings.TrimSpace(string(common))
		if filepath.Base(gitDir) != ".git" {
			out = append(out, strandwatch.Worktree{
				Path: path,
				Error: fmt.Sprintf("git-common-dir %q is not a conventional worktree layout, "+
					"so the repository it belongs to could not be named", gitDir),
			})
			continue
		}
		wt := strandwatch.Worktree{Path: path, Repo: filepath.Dir(gitDir)}
		br, berr := exec.Command("git", "-C", path, "symbolic-ref", "--short", "HEAD").Output()
		if berr != nil {
			// A DETACHED HEAD holds commits exactly as a branch does. It cannot be
			// pushed by name and this sweep has no ref to measure, so it is stated
			// rather than skipped — a worktree that drops out of the population
			// without appearing anywhere is the shape being repaired.
			wt.Error = "no branch is checked out (detached HEAD), so no ref could be measured"
		} else {
			wt.Branch = strings.TrimSpace(string(br))
		}
		out = append(out, wt)
	}
	return out, nil
}
