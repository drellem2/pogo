package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/refinery"
)

// newLandCmd builds `pogo land`: an enth worker records its own land, and the
// work item closes as a refinery merge closes one (docs/design/enth-vcs.md §6,
// mg-5b904).
func newLandCmd(jsonOutput *bool) *cobra.Command {
	var item, repo, landedFile, requestID, target, author, verdictInline, verdictFile, postMergeTag string
	cmd := &cobra.Command{
		Use:   "land --item=<id> --repo=<repo> --landed-file=<file>",
		Short: "Record an enth worker's own land, closing its work item as a merge would",
		Long: `On an enth repo there is no merge queue: the worker lands its own work
with enth, then records it here.

  pogo presubmit --item=mg-a3f --print-stack        # the stack, bottom first
  enth land <stack> --request-id mg-a3f/<attempt> > land.json
  pogo land --item=mg-a3f --repo=/path/to/enth-repo --landed-file=land.json \
      --request-id=mg-a3f/<attempt> --verdict-file=verdict.json

The polecat template's self_land function runs all three, with the retries:
stale_tip is re-landed under a new request id, an unknown outcome is re-issued
under the SAME request id (a new one would land the same Changes twice), and a
conflict stops so the work can be re-made on the new trunk.

pogod does not take the worker's word for the land. It parses enth's one
"landed" line, checks that it moved the target ref, and asks enthd that every
Change in it carries Pogo-Item=<item> and has landed exactly as the line says.
Then it records the land as a merged MR in the refinery's history (slice 8's
"enth" land record, "self_landed": true, refinery_merged with landed {enth, id})
and closes the work item exactly as a refinery merge closes one: done, the
result sidecar naming the landed id and carrying --verdict, the filer notice,
the coordinator's MERGED mail, and the worker stopped. The record is written
through BEFORE any of that runs.

Recording the same land again is safe and answers the first MR
("already_recorded"); re-run this command if its answer was lost. Never land
again to recover a lost record.

No refinery gate runs: the merged result is not tested before it lands. That is
the accepted trade-off of enth-vcs §6; post-land CI on the mirror is the check.

--post-merge-tag=<tag> tags what the land landed as (enth tag <tag> <new>),
before the item is closed: the self-land form of 'pogo refinery submit
--post-merge-tag', for a release cut onto release/<version>. An enth tag is
immutable. A tag already at that Change is success, so a re-run converges; a
tag at anything else fails. A failed tag does not unrecord the land, but the
work item is NOT completed and the coordinator is mailed, as on git; this
command then exits 6 (the land is recorded: do not land again). In the
template, pass it to self_land: self_land --post-merge-tag=v1.2.0.

--landed-file is enth land's stdout ("-" reads stdin). On a git repo this is
refused: git work merges through 'pogo refinery submit'.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if item == "" || repo == "" || landedFile == "" {
				cli.ExitWithError(*jsonOutput, "--item, --repo and --landed-file are required", cli.ExitError)
			}
			if err := refinery.RefusePogoHomeRepo(repo); err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}
			landed, err := readLandedFile(landedFile)
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}
			verdict, err := readSubmitVerdict(verdictInline, verdictFile)
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}
			resp, err := client.RecordLand(refinery.LandRequest{RepoPath: repo, Item: item, Author: author,
				TargetRef: target, Landed: landed, RequestID: requestID, Verdict: verdict, PostMergeTag: postMergeTag})
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error()+
					"\nIf enth land succeeded, the work IS landed and only this record is missing: re-run this same command. Do NOT run enth land again under a new request id — it would land the same Changes twice.",
					cli.ExitError)
			}
			if *jsonOutput {
				cli.PrintJSON(resp)
				if resp.PostMergeError != "" {
					os.Exit(cli.ExitPostMergeFailed)
				}
				return
			}
			again := ""
			if resp.AlreadyRecorded {
				again = " (already recorded: nothing fired again)"
			}
			if l := resp.Landed; l != nil {
				fmt.Printf("LANDED and recorded as MR %s%s: %s entry %d, new %s (%d Change(s)). pogod closes %s now.\n",
					resp.ID, again, l.Ref, l.Entry, l.New, len(l.Changes), item)
			} else {
				fmt.Printf("LANDED and recorded as MR %s%s. pogod closes %s now.\n", resp.ID, again, item)
			}
			switch {
			case resp.PostMergeError != "":
				fmt.Fprintf(os.Stderr, "TAG NOT CREATED: %s — %s\nThe land IS recorded; the work item is deliberately NOT done, and the coordinator has been mailed.\n",
					resp.PostMergeTag, resp.PostMergeError)
				os.Exit(cli.ExitPostMergeFailed)
			case resp.PostMergeTag != "":
				fmt.Printf("Tagged %s on what the land landed as.\n", resp.PostMergeTag)
			}
		},
	}
	cmd.Flags().StringVar(&item, "item", "", "Work item the land completes (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository path, as the project record names it (required)")
	cmd.Flags().StringVar(&landedFile, "landed-file", "", `enth land's stdout: its one "landed" line ("-" reads stdin; required)`)
	cmd.Flags().StringVar(&requestID, "request-id", "", "The --request-id the land was issued under (recorded on the land record)")
	cmd.Flags().StringVar(&target, "target", "", "Target ref the land moved (default: the repo's trunk, as its project record names it)")
	cmd.Flags().StringVar(&author, "author", "", "Author recorded on the MR, which the reap closes (default: --item)")
	cmd.Flags().StringVar(&verdictInline, "verdict", "", "YOUR OWN result for the work item, as a non-empty JSON object, written into the item's result sidecar under \"verdict\"")
	cmd.Flags().StringVar(&postMergeTag, "post-merge-tag", "", "Tag what the land landed as (enth tag), before the item is closed; a failure blocks completion and mails the coordinator")
	cmd.Flags().StringVar(&verdictFile, "verdict-file", "", "Read --verdict from this file, or from stdin when it is \"-\"")
	return cmd
}

func readLandedFile(path string) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("--landed-file: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("--landed-file %s is empty: enth land printed nothing, so there is no land to record", path)
	}
	return string(data), nil
}
