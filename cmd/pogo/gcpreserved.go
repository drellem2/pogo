package main

import (
	"fmt"
	"os"
	"time"

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
//
// # Why it STREAMS (mg-1530, drellem2/pogo#158)
//
// It used to buffer: the whole scan ran, then the whole report printed. With
// every external call unbounded and a full working-tree walk per retained tree,
// one slow tree gave the reporter zero bytes and no return, with nothing to say
// which tree — indistinguishable from a hang, and their only move was to kill
// it. Now the header lands before the first tree is touched, each retained tree
// prints as it resolves, and the counts follow at the end.
//
// The progress trace goes to STDERR and the listing to STDOUT, which buys two
// things at once: `--json` keeps a single parseable document on stdout WHILE a
// human watching the terminal still sees where the scan is, and a redirected
// `> inventory.txt` captures the listing without the trace running through it.
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

	// These notes are printed HERE, before the scan, rather than held until it
	// returns (mg-1530). They were established before a single tree was read
	// and they qualify every row that follows; a caveat delivered after the
	// rows it qualifies has already failed at its job, and on a slow scan it
	// may never be delivered at all.
	if !jsonOutput {
		for _, n := range notes {
			fmt.Println(n)
		}
		if len(notes) > 0 {
			fmt.Println()
		}
	}

	var (
		total          int
		treeStarted    time.Time
		printedRowHead bool
	)
	progress := func(ev gitgc.PreservedScanEvent) {
		switch ev.Phase {
		case "start":
			total = ev.Total
			if !jsonOutput {
				fmt.Print(gitgc.StreamedHeader(polecatsDir, repoFilter, ev.Total))
			}
			fmt.Fprintf(os.Stderr, "scanning %d director(ies) under %s\n", ev.Total, polecatsDir)
		case "note":
			// stderr only: the note is in the report and the tail prints it to
			// stdout. Printing it to both would put one caveat in the listing
			// twice, which reads as two.
			fmt.Fprintf(os.Stderr, "note: %s\n", ev.Note)
		case "enter":
			treeStarted = time.Now()
			fmt.Fprintf(os.Stderr, "  [%*d/%d] %s ...", digits(total), ev.Index, total, ev.Owner)
		case "done":
			// The elapsed time is printed only when it is worth reading. The
			// whole question this command failed to answer was WHICH TREE is
			// slow, and a second column of "0.0s" on every fast tree buries
			// the one row that answers it.
			elapsed := ""
			if d := time.Since(treeStarted); d >= time.Second {
				elapsed = fmt.Sprintf(" (%.1fs)", d.Seconds())
			}
			fmt.Fprintf(os.Stderr, " %s%s\n", ev.Disposition, elapsed)
			if jsonOutput || ev.Disposition != "retained" || ev.Tree == nil {
				return
			}
			if !printedRowHead {
				fmt.Print(gitgc.PreservedPreamble())
				printedRowHead = true
			}
			fmt.Print(gitgc.StreamedTree(*ev.Tree))
		}
	}

	rep, err := gitgc.ScanPreserved(gitgc.PreservedScanOptions{
		PolecatsDir:  polecatsDir,
		Repo:         repoFilter,
		LivePolecats: live,
		Progress:     progress,
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
	fmt.Print(rep.StreamedTail())
}

// digits is the column width for a scan counter, so "[  7/196]" lines up with
// "[196/196]" and the trace reads as a column rather than as ragged text.
func digits(n int) int {
	w := 1
	for n >= 10 {
		n /= 10
		w++
	}
	return w
}
