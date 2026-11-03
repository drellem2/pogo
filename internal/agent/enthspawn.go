package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// Polecat spawn onto an enth repo (docs/design/enth-vcs.md §4, slice 5,
// mg-cc5cd).
//
// A repo whose project record says `"vcs": "enth"` gets an enth checkout at
// the polecat's directory instead of a git worktree:
//
//	enth --remote URL --repo NAME checkout <target ref> DIR [workspace_paths...]
//
// There is no polecat-<name> branch, so none of the git spawn's branch
// machinery (leftover reclaim, the branch rollback, the base fallback chain)
// runs. What stays the same is the directory discipline: a directory this
// spawn did not create is never removed on failure (drellem2/pogo#167).

// enthWorkerEnv is what an enth polecat's environment adds (§4,
// "Environment"): the repo's enthd endpoint and repo name, so every `enth`
// verb the worker runs goes to enthd and every Change is durable there at
// creation (P4), and the token pogod itself holds, when it holds one in
// $ENTH_TOKEN. Without it enth falls back to the token `enth login` saved for
// the remote, which the worker reads from the same HOME. The token is the
// user's, as GH_TOKEN is today; it is passed through and never logged.
//
// ENTH_TRUNK is the ref `enth land` defaults to. The worker does not land, but
// a verb that reads it should read this repo's trunk, not enth's guess.
func enthWorkerEnv(e vcs.EnthRepo) []string {
	env := []string{"ENTH_REMOTE=" + e.Remote, "ENTH_REPO=" + e.Repo}
	if e.Trunk != "" {
		env = append(env, "ENTH_TRUNK="+e.Trunk)
	}
	if tok := os.Getenv("ENTH_TOKEN"); tok != "" {
		env = append(env, "ENTH_TOKEN="+tok)
	}
	return env
}

// enthSelectionPrelude is the prompt block for a NARROWED enth workspace
// (§4, "the escape hatch"): the checkout holds only the record's
// workspace_paths, an edit elsewhere is refused by enth as outside_selection
// when the worker makes its Change, and `enth checkout --add` is the remedy.
// A whole-repo workspace (the default) gets nothing: there is no selection to
// fall outside.
func enthSelectionPrelude(e vcs.EnthRepo, dir string) string {
	if len(e.WorkspacePaths) == 0 {
		return ""
	}
	return fmt.Sprintf("> **Your workspace is a NARROWED enth checkout.** It holds only: %s.\n"+
		"> A new file outside those paths is refused when you make your Change (`outside_selection`).\n"+
		"> Before you touch anything else, widen the checkout: `enth checkout --add <PATH> %s`.\n\n",
		strings.Join(quoted(e.WorkspacePaths), ", "), dir)
}

func quoted(ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = "`" + p + "`"
	}
	return out
}

// enthAdoptRefusal is the spawn refusal for --stranded-adopt on an enth repo.
// Adopting enth work means checking out the stranded worker's top unlanded
// Change, found by its Pogo-Item trailer (§4, W7) — slice 6's listing.
func enthAdoptRefusal(repo string) string {
	return fmt.Sprintf("--stranded-adopt is not supported on %s yet: it is an enth repo, and adopting enth work "+
		"(checking out the stranded worker's top unlanded Change) is enth-vcs slice 6. Nothing was dispatched", repo)
}

// createEnthPolecatWorkspace makes the enth checkout at dir for a polecat on
// sourceRepo aimed at target. preexisted says dir was there before this
// spawn: enth refuses an existing directory anyway, and this refuses first so
// the failure path has nothing to decide about removing it. A failed
// checkout is rolled back only when the directory is the spawn's own.
func createEnthPolecatWorkspace(e vcs.EnthRepo, sourceRepo, dir, target string, preexisted bool) (vcs.Workspace, string, error) {
	b := enthvcs.Backend{Repo: e}
	ctx := context.Background()
	ref := b.ResolveBase(ctx, vcs.Repo{Path: sourceRepo}, target)
	if preexisted {
		return vcs.Workspace{}, ref, fmt.Errorf("%s already exists, and an enth checkout is made only into a new "+
			"directory. It was not created by this spawn and was not touched; establish who owns it "+
			"(`pogo agent list`; `enth status %s`) before reclaiming it (drellem2/pogo#167)", dir, dir)
	}
	ws, err := b.Create(ctx, vcs.WorkspaceSpec{Repo: vcs.Repo{Path: sourceRepo}, Dir: dir, Base: ref})
	if err != nil {
		cause, out := commandErrorParts(err)
		_ = b.Rollback(ctx, vcs.Repo{Path: sourceRepo}, dir)
		return vcs.Workspace{}, ref, fmt.Errorf("enth checkout of %s (%s on %s) failed: %v\n%s", ref, e.Repo, e.Remote, cause, out)
	}
	return ws, ref, nil
}
