package main

import (
	"fmt"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/gitgc"
)

// runGCListPreserved prints the standing list of retained polecat worktrees —
// `pogo gc --list-preserved`.
//
// # Why this command exists (mg-f4c0)
//
// pogod preserves a polecat's worktree when it exits holding uncommitted work,
// and every half of that mechanism works: the guard refuses, the coordinator is
// mailed, a worktree_preserved event lands on the spine. What did not exist was
// anything on the READ side. The mail fires once into a busy inbox and is never
// repeated; the event is a stream, not a population; and so the trees
// accumulated — six when this was filed, twenty-three by the time it was fixed —
// each pinning a branch that cannot be deleted, and each posing a question
// ("is this uncommitted work worth rescuing?") that no one was assigned to ask.
// The only way to see the population at all was `ls ~/.pogo/polecats`.
//
// This command changes nothing and reclaims nothing, and that restraint is the
// design. Reclaiming is one already-existing command and was never the hard
// part. Knowing which of the trees can safely take it is, and that is a
// question about the files inside them — see the preamble the report prints,
// and PreservedTree's doc comment for the case that settled it.
func runGCListPreserved(jsonOutput bool, repoFilter string) {
	polecatsDir, err := gitgc.DefaultPolecatsDir()
	if err != nil {
		cli.ExitWithError(jsonOutput, fmt.Sprintf("cannot resolve the polecats dir: %v", err), cli.ExitError)
	}

	// The live set is read the same way the sweep reads it — registry unioned
	// with the persisted witness — so a running polecat's dirty tree is
	// reported as IN USE rather than swelling the count of trees that need an
	// owner. Unlike the sweep this listing removes nothing, so an unreadable
	// witness is not fatal here; it is reported and the scan proceeds, because
	// a listing that refuses to print is strictly worse than one that prints
	// with a caveat.
	live, notes, lerr := gcLivePolecats()
	if lerr != nil {
		notes = append(notes, fmt.Sprintf(
			"warning: the polecat witness could not be read (%v); a running polecat's tree may\n"+
				"         be listed below as retained. This listing changes nothing, so the risk is a\n"+
				"         wrong line rather than a lost file — but check `pogo agent list` before acting.", lerr))
		live = nil
	}

	rep, err := gitgc.ScanPreserved(gitgc.PreservedScanOptions{
		PolecatsDir:  polecatsDir,
		Repo:         repoFilter,
		LivePolecats: live,
	})
	if err != nil {
		cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
	}

	if jsonOutput {
		// The notes ride in the payload rather than going to stdout beside it,
		// so `--json` stays parseable.
		rep.Notes = append(notes, rep.Notes...)
		cli.PrintJSON(rep)
		return
	}
	for _, n := range notes {
		fmt.Println(n)
	}
	if len(notes) > 0 {
		fmt.Println()
	}
	fmt.Print(rep.Summary())
}
