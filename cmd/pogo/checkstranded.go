package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
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

EXCLUSIONS, both counted and both nameable with --all:

  running polecat   a live worker's branch has unmerged commits on a claimed item
                    because that is what work in progress looks like.
  refinery queue    the remedy for a stranded branch is to submit it, and it is
                    already submitted.

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
			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().StringSliceVar(&repos, "repo", nil,
		"Restrict to these repositories (default: every repo the open items name)")
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
