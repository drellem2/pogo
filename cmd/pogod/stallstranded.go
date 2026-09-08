package main

import (
	"fmt"
	"sort"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/refinery"
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
func newStallStranded(queue func() *refinery.Refinery) stallwatch.Stranded {
	return stallwatch.StrandedFunc(func(items []stallwatch.StrandedItem) (stallwatch.StrandedWork, bool) {
		// The refinery queue, snapshotted ONCE per probe for probeStranded's own
		// reason: a branch must not read as in-flight to one item and free to the
		// next within one sample. Reached through the THUNK rather than a captured
		// pointer, like every other refinery reader in this file's neighbours — an
		// orchestration restart replaces *mergeQueue, and a closure over the old
		// one would answer "nothing is queued" from a refinery nobody is using,
		// which is this lookup's silent direction.
		inQueue, queueConsulted := refineryQueueByBranch(queue)

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
					// Whether this branch's merge is ALREADY RUNNING (mg-64bb).
					// It does not change whether the item is withheld from
					// dispatch — it is, either way, and that is the exclusion
					// mg-4bf1 shipped — it changes what the notice may tell the
					// reader to do about it.
					if q, ok := queuedFor(inQueue, repo, f.Branch); ok {
						b.Queued = &q
					}
					work.Items[id] = append(work.Items[id], b)
				}
			}
		}
		work.QueueConsulted = queueConsulted
		// The gap is recorded only when a branch was actually found. An unasked
		// queue is a defect in a REMEDY, and with nothing found there is no
		// remedy to be wrong about — a snapshot of an idle fleet stays silent,
		// which is what makes the note worth reading when it does appear.
		if !queueConsulted && len(work.Items) > 0 {
			// Not folded into Uncertain: an unlisted repository makes the
			// snapshot INCOMPLETE, while an unasked queue makes it complete and
			// wrongly REMEDIED. They travel to different readers and say
			// different things, so they stay different fields.
			gaps = append(gaps, "the refinery queue could not be consulted, so a branch already awaiting merge cannot be told from one nobody submitted")
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

// refineryQueueByBranch snapshots the merge requests still in flight, keyed by
// (repo, branch), and says whether the queue could be READ AT ALL.
//
// The second return is not decoration. With no refinery — the daemon started
// with `[refinery] enabled = false`, or an orchestration restart between the
// thunk and this call — every branch comes back unqueued, which is exactly what
// a genuinely empty queue looks like. Recorded rather than assumed, because the
// consequence of the confusion is a paste-ready submit against a running merge
// (mg-8baa is the general form; mg-64bb is this instance).
//
// PROCESSING IS INCLUDED, not just pending: QueueWithProcessing returns the
// in-flight request alongside the queued ones, and the in-flight one is the
// likeliest to be the branch being asked about — it is the one whose gate run is
// holding the queue up. `pogo check-stranded` reads the same endpoint for the
// same reason.
func refineryQueueByBranch(queue func() *refinery.Refinery) (map[string][]refinery.MergeRequest, bool) {
	if queue == nil {
		return nil, false
	}
	q := queue()
	if q == nil {
		return nil, false
	}
	mrs := q.QueueWithProcessing()
	out := make(map[string][]refinery.MergeRequest, len(mrs))
	for _, mr := range mrs {
		out[mr.Branch] = append(out[mr.Branch], mr)
	}
	return out, true
}

// queuedFor finds this repo's queued request for a branch.
//
// KEYED ON BOTH REPO AND BRANCH, and the branch half alone would be wrong: a
// branch NAME is not unique across repositories, so `polecat-ta932` queued in
// another clone must not answer for this one. That direction is the dangerous
// one, because what it suppresses is the submit line for a branch nobody has
// actually submitted.
//
// The repo half is config.SameRepo, which is filepath.Clean equality and NOT a
// name resolver — "pogo" does not match "/Users/daniel/dev/pogo" here. It does
// not have to: an item spelling its repo as a bare name (42 of them do, against
// 883 spelling a path — mg-cd4a, pm-pogo's count, not re-derived) never reaches
// this line, because strandedwork.PolecatBranches fails on it first and the
// repository is recorded as a gap. What SameRepo buys is agreement over the
// spellings that DO both resolve — a trailing slash, an unclean path — which
// exact string keying would silently treat as different repositories.
func queuedFor(inQueue map[string][]refinery.MergeRequest, repo, branch string) (stallwatch.QueuedMerge, bool) {
	for _, mr := range inQueue[branch] {
		if !config.SameRepo(mr.RepoPath, repo) {
			continue
		}
		return stallwatch.QueuedMerge{MR: mr.ID, Status: string(mr.Status)}, true
	}
	return stallwatch.QueuedMerge{}, false
}
