package main

import (
	"fmt"
	"sort"

	"github.com/drellem2/pogo/internal/stallwatch"
	"github.com/drellem2/pogo/internal/strandedwork"
)

// newStallStranded lets the stall watcher ask which available work items already
// have their work finished and sitting on a branch (mg-4bf1), so an item pogod
// has ALREADY mailed a `[stranded-push] ... do NOT dispatch` about is not
// advertised by priority-wake as ready in the same minute.
//
// It closes over internal/strandedwork — the same package the spawn-time gate
// (agent.GitStrandedWorkGate) and the `pogo check-stranded` sweep read — rather
// than re-deriving "does this item have unmerged work" here, for the reason
// newStallPreserved closes over gitgc and newStallWorkers over the registry: a
// second implementation would drift from the first, and the drift would show up
// as advice about a fleet pogod sees differently. The whole finding this fixes
// is two components of pogod disagreeing about one item; answering it with a
// third opinion would be that finding, committed by its own repair.
//
// # Why this is affordable on a 30s tick
//
// The expensive call is strandedwork.Inspect — `git cherry` plus a ref walk per
// branch — and it is paid ONLY for a branch whose NAME matches one of the
// caller's item ids (strandedwork.BranchMatchesItem). Everything before that is
// one `git for-each-ref` per repository over the polecat namespace. In the
// steady state — no available item has a polecat branch — that is one
// for-each-ref per repo per tick and nothing else. It is the same narrowing
// gitgc.PreservedForItems makes, for the same reason: the item-driven sweep this
// package already ships is explicitly an operator command, and the tick form has
// to be the join, not the scan.
//
// # Why it does NOT fetch
//
// strandedwork.Fetch has a 20s timeout and talks to the network; both the
// dispatch gate and the release-time reporter call it, and both run at most once
// per event. A 30s tick cannot. The omission is affordable for a specific
// reason rather than by hope: a polecat's worktree SHARES the source repo's
// object store, so its own `git push` updates refs/remotes/origin/<branch> in
// this very clone. The population this check exists for — a polecat stopped
// after pushing — is therefore visible with no network at all. What a missing
// fetch costs is a branch pushed from ANOTHER clone, which stays invisible until
// something else fetches; that is the loud direction (the item keeps being
// advertised, exactly as before this fix) and the spawn gate, which does fetch,
// still refuses it.
func newStallStranded() stallwatch.Stranded {
	return stallwatch.StrandedFunc(func(items []stallwatch.StrandedItem) (stallwatch.StrandedWork, bool) {
		byRepo := map[string][]string{}
		noRepo := 0
		for _, it := range items {
			if it.ID == "" {
				continue
			}
			if it.Repo == "" {
				noRepo++
				continue
			}
			byRepo[it.Repo] = append(byRepo[it.Repo], it.ID)
		}

		work := stallwatch.StrandedWork{Items: map[string][]stallwatch.StrandedBranch{}}
		var gaps []string
		repos := make([]string, 0, len(byRepo))
		for repo := range byRepo {
			repos = append(repos, repo)
		}
		sort.Strings(repos)
		for _, repo := range repos {
			branches, err := strandedwork.PolecatBranches(repo)
			if err != nil {
				// A repository that could not be listed is a GAP, not a clean
				// verdict — the mg-8baa lesson, applied before it could be
				// re-learned here. Its items keep being advertised (the loud
				// direction) and the dispatch notices say so.
				gaps = append(gaps, fmt.Sprintf("%s could not be listed (%v)", repo, err))
				continue
			}
			for _, branch := range branches {
				for _, id := range byRepo[repo] {
					if !strandedwork.BranchMatchesItem(branch, id) {
						continue
					}
					f, ierr := strandedwork.Inspect(repo, branch, "")
					if ierr != nil {
						// "Could not read" must never be recorded as "nothing was
						// stranded". Same direction as the listing failure above.
						gaps = append(gaps, fmt.Sprintf("%s in %s could not be read (%v)", branch, repo, ierr))
						continue
					}
					// Stranded() is resubmit OR pre-registration, and it
					// deliberately excludes DispositionCarried: a reviewer polecat's
					// branch points at the builder's head every single time, so
					// reporting those would fire this check on every review item in
					// the fleet (mg-1af2).
					if !f.Stranded() {
						continue
					}
					b := stallwatch.StrandedBranch{
						Branch:   f.Branch,
						Ref:      f.Ref,
						Pushed:   f.Pushed,
						Unmerged: len(f.Unmerged),
						Target:   f.Target,
						Repo:     repo,
					}
					if f.PreRegistration != nil {
						b.PreRegistration = f.PreRegistration.SHA
					}
					work.Items[id] = append(work.Items[id], b)
				}
			}
		}
		if noRepo > 0 {
			gaps = append(gaps, fmt.Sprintf("%d available item(s) name no repo, so no branch was looked for", noRepo))
		}
		if len(gaps) > 0 {
			work.Uncertain = gaps[0]
			if len(gaps) > 1 {
				work.Uncertain += fmt.Sprintf(" (and %d other gap(s))", len(gaps)-1)
			}
		}
		// known=true even when nothing was found and even when some repos failed:
		// the failures are REPORTED as Uncertain rather than collapsing the whole
		// snapshot, because known=false would discard the repos that answered
		// cleanly along with the one that did not.
		return work, true
	})
}
