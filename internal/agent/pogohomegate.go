package agent

import (
	"fmt"

	"github.com/drellem2/pogo/internal/config"
)

// pogoHomeSpawnRefusal returns the refusal for a polecat whose worktree would
// be cut from $POGO_HOME, or "" (mg-752a3, decision on mg-feecc).
//
// Such a worker's only exit is a refinery submit, and the refinery refuses
// $POGO_HOME: a merge lands on origin, and the live checkout never pulls. So
// the dispatch is refused before a worker is spent on it. Only a WORKTREE
// dispatch: an in-place one (--no-worktree, or a template with worktree =
// false) edits the live tree directly, which is the sanctioned write path.
func pogoHomeSpawnRefusal(repo string, createWorktree bool) string {
	if !createWorktree {
		return ""
	}
	is, home := config.RepoIsPogoHome(repo)
	if !is {
		return ""
	}
	return fmt.Sprintf(
		"refusing to spawn a worktree polecat on %s: it is $POGO_HOME (%s), the live checkout pogod runs from. "+
			"The worker's only exit is a refinery submit, and the refinery refuses $POGO_HOME — a merge lands on its "+
			"origin, which the live checkout never pulls. Nothing was dispatched. Instead: %s. "+
			"To dispatch an in-place edit there, pass --no-worktree",
		repo, home, config.PogoHomeLiveCommitPath)
}
