package main

import (
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/stallwatch"
)

// newStallPreserved lets the stall watcher ask which available work items
// already have their work written and UNCOMMITTED in a retained worktree, so an
// item whose only copy sits in a preserved tree is not advertised as ready to
// dispatch (mg-836c).
//
// It closes over gitgc.PreservedForItems — the same scan the spawn-time gate
// uses and the same removal guard `pogo gc` and the polecat exit hook consult —
// rather than re-deriving "is this tree retained" here, for the reason
// newStallWorkers closes over WorkItemsInFlight: a second implementation would
// drift from the first, and the drift would show up as advice about a fleet
// pogod sees differently.
//
// # Why this is affordable on a 30s tick
//
// gitgc.ScanPreserved is explicitly "an operator command rather than something
// to put on a tick" — it walks every retained tree to age it. PreservedForItems
// is the narrow form: candidates are resolved from DIRECTORY NAMES against the
// caller's item ids, no age is measured, and `git status` runs only on a tree
// that names one of those items. In the steady state — no preserved tree for
// any available item — that is one os.ReadDir per tick and nothing else.
func newStallPreserved() stallwatch.Preserved {
	return stallwatch.PreservedFunc(func(ids []string) (stallwatch.PreservedWork, bool) {
		dir, err := gitgc.DefaultPolecatsDir()
		if err != nil {
			// No polecats dir could be resolved: nothing could be established.
			// known=false puts every check back to its pre-mg-836c behaviour,
			// which is loud rather than silent — see Preserved.Retained.
			return stallwatch.PreservedWork{}, false
		}
		rep, err := gitgc.PreservedForItems(gitgc.PreservedItemOptions{
			PolecatsDir: dir,
			Items:       ids,
		})
		if err != nil {
			return stallwatch.PreservedWork{}, false
		}
		return stallPreservedFrom(rep), true
	})
}

// stallPreservedFrom is the translation, split out from the closure so the
// mapping — including which gitgc outcome becomes `Unread` — is testable
// without a polecats directory on disk.
//
// The scan is NOT repo-filtered. The watcher reads one macguffin work queue that
// spans every repo the fleet works, so filtering to one repository would report
// a fraction of the population while looking complete — the same failure shape
// the finding this fixes was filed about.
//
// A tree that produced a non-fatal read error is still REPORTED, and the errors
// ride along as Uncertain rather than suppressing the finding. An incomplete
// snapshot can only cause a held item to be missed, never invented.
func stallPreservedFrom(rep gitgc.PreservedItemReport) stallwatch.PreservedWork {
	held := stallwatch.PreservedWork{Items: make(map[string][]stallwatch.PreservedTree, len(rep.Trees))}
	for id, trees := range rep.Trees {
		for _, t := range trees {
			held.Items[id] = append(held.Items[id], stallwatch.PreservedTree{
				Path:      t.Path,
				Branch:    t.Branch,
				Modified:  t.Modified,
				Untracked: t.Untracked,
				// Anything that is not a positively-read dirty tree is a tree
				// whose contents nobody established. Reporting it as "0
				// modified, 0 untracked" would be a claim about it, and the
				// wrong one.
				Unread: t.Outcome != "preserved",
			})
		}
	}
	if len(rep.Errors) > 0 {
		held.Uncertain = "some retained worktrees could not be fully read (" + rep.Errors[0] + ")"
	}
	return held
}
