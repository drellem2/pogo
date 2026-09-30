package refinery

import (
	"fmt"

	"github.com/drellem2/pogo/internal/config"
)

// RefusePogoHomeRepo refuses a merge request whose repo is $POGO_HOME (or a
// linked worktree of it). See config.RepoIsPogoHome for why the refinery path
// cannot serve this one repo, and for how the paths are compared.
//
// Submit calls it and is the authority. The CLI calls it too, before sending,
// because only the CLI knows the cwd a relative --repo was typed against.
func RefusePogoHomeRepo(repoPath string) error {
	if is, home := config.RepoIsPogoHome(repoPath); is {
		return fmt.Errorf("refusing to merge into %s: it is $POGO_HOME (%s), the live checkout pogod runs from. "+
			"A refinery merge lands on its ORIGIN, and the live checkout never pulls, so the merge would never be live — "+
			"it would fork a second history beside the live tree (decision on mg-feecc, mg-752a3). "+
			"Instead: %s",
			repoPath, home, config.PogoHomeLiveCommitPath)
	}
	return nil
}
