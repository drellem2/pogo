package main

import (
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/project"
)

// newRepoResolver gives the per-repo worker cap a way to read a work item whose
// `repo` field is a bare NAME rather than a path (mg-cd4a).
//
// The index is reached through a THUNK, not a snapshot: it grows as
// repositories are visited, and a resolver closed over the list as it stood at
// startup would go on failing to resolve a repository pogo learned about an
// hour ago — the same reason SetRefineryActivity takes a thunk over *mergeQueue.
//
// # Why pogo's OWN checkouts are dropped first
//
// This is not a tidiness filter, it is the difference between resolving the two
// largest bare populations in the item store and refusing to. Measured against
// this machine's live index on 2026-08-20 (35 entries, `lsp`), three names have
// TWO candidates each, and in every case the second is a refinery worktree
// pogo made itself:
//
//	one_third_width_three  ~/research/... and ~/.pogo/refinery/worktrees/...
//	union_closed           ~/research/... and ~/.pogo/refinery/worktrees/...
//	one_third              ~/research/... and ~/.pogo/refinery/worktrees/...
//
// 108 items name the first of those by bare name and 47 the second — so without
// this filter MatchRepoName would call them ambiguous and every one of them
// would report an occupancy nobody could determine. A derived checkout is never
// a work item's target and never a polecat's SourceRepo, so it is not a rival
// answer; it is the same repository seen from pogo's own working area.
//
// It removes candidates only. A name left with no candidate at all still
// resolves to nothing, and RepoOccupancyFor says so rather than guessing —
// `riemann` is the live example, indexed twice OUTSIDE POGO_HOME
// (~/files/riemann and ~/research/riemann), and it stays ambiguous on purpose.
func newRepoResolver(projects func() []project.Project) agent.RepoResolver {
	return agent.RepoResolverFunc(func(name string) (string, bool) {
		home := config.NormalizeRepo(config.PogoHome())
		var paths []string
		for _, p := range projects() {
			if home != "" && underDir(p.Path, home) {
				continue
			}
			paths = append(paths, p.Path)
		}
		return agent.MatchRepoName(name, paths)
	})
}

// underDir reports whether path sits inside dir, comparing on path components
// so that `/x/.pogo-other` is not read as being under `/x/.pogo`.
func underDir(path, dir string) bool {
	p := config.NormalizeRepo(path)
	if p == "" || p == dir {
		return false
	}
	return strings.HasPrefix(p, dir+string(filepath.Separator))
}
