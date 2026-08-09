package main

import (
	"fmt"
	"os"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/verdictwatch"
)

// exitVerdictUsage is `pogo check-verdicts`' exit code for a malformed
// invocation, carried over from the verdictwatch this ports (0 clean, 1 dropped,
// 2 usage). It is deliberately not cli.ExitNotFound despite sharing the integer:
// a caller of THIS command reads 2 as "I asked the question wrong", and the two
// meanings never meet because nothing else in this command exits 2.
const exitVerdictUsage = 2

// sinceLooksLikeATimestamp matches an RFC3339 prefix — a date, optionally
// followed by a time.
//
// --since is a STRING PREFIX comparison against the landing stamp, which is what
// makes `--since 2026-08-05` work without a date parser. The same property makes
// `--since yesterday` sort ABOVE every stamp in the store, excluding everything
// and producing a scoped-empty report that exits 0. That is a silent wrong
// answer to a reasonable-looking invocation, so it is refused as usage instead.
var sinceLooksLikeATimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}([T ][\d:.+\-Z]*)?$`)

// newCheckVerdictsCmd builds `pogo check-verdicts` (mg-f5dd): the ninth
// report-only detector, and a port of macguffin's verdictwatch.py.
//
// It is a sibling of check-acks, check-commit-body, check-intake,
// check-mailloops, check-prompts, check-staleness, check-strandedmail and
// check-teardown. The family's membership criterion is a READ-ONLY DETECTOR
// THAT REPORTS A CONDITION AND TAKES NO ACTION — not which subsystem it reads:
// check-teardown already reads mg state and asks GitHub about it, check-intake
// reads GitHub and asks mg, check-strandedmail reads the macguffin mail tree.
// This one reads both halves of a verdict's delivery out of the macguffin store.
func newCheckVerdictsCmd(jsonOutput *bool) *cobra.Command {
	var (
		filer   string
		since   string
		root    string
		all     bool
		quiet   bool
		asProbe bool
	)
	cmd := &cobra.Command{
		Use:   "check-verdicts",
		Short: "Report work items that landed without their filer ever receiving a verdict (never files anything)",
		Long: `Report every work item that reached ` + "`done`" + ` or ` + "`archived`" + ` whose FILER never
received a verdict mail from the worker, ORDERED BY WHEN IT LANDED — so a
backlog can be RECOVERED, not merely alarmed about.

THE PREDICATE, and both halves come from macguffin's own store:

  the landing       work.done / work.archive in events.jsonl
  the filer         ` + "`creator:`" + ` in the item's own frontmatter
  the worker        ` + "`polecat-<name>`" + ` in the item's result sidecar
  the verdict mail  a message in the filer's mailbox whose From: is that worker

Findings come in three kinds, not two:

  DROPPED       landed, and the filer's mailbox holds nothing from the worker.
                The finding this exists to produce.
  DELIVERED     the filer holds a message from the worker. Archived mail counts:
                filing a verdict away is not losing it.
  UNDECIDABLE   the worker cannot be resolved from mg's own store, so NEITHER
                verdict can be reached. Counted and listed on its own rather
                than folded into either — a detector that silently absorbs what
                it cannot reach is the defect this lineage keeps rediscovering.

WHAT IT CANNOT SEE, stated here rather than in a footnote. A verdict delivered
by any channel other than macguffin mail — a commit subject, a docs file, a
spoken handover — is invisible and is reported as DROPPED. That is the intended
polarity: the complaint this was built for is precisely that a commit subject is
not delivery.

REPORTS ONLY. It never files the missing verdict, never mails on anyone's
behalf, and never edits an item. Re-sending a verdict would have to forge a
sender, which turns a recoverable gap into a message whose provenance is a lie.
If a future version should FILE the missing verdict, that is a DIFFERENT command
and it must not join this family.

A run that COULD NOT LOOK says so instead of exiting clean. Lose events.jsonl —
renamed, rotated, a root pointed one directory too high — and every item reads
as never landed, so a careless detector reports "0 dropped" over a fleet losing
every verdict it has. That case, an unreadable mail tree, an unresolvable store,
and an UNSCOPED scan that judged zero items are all reported as an
INSTRUMENT FAILURE, and exit ` + fmt.Sprint(exitInstrumentFailure) + `. A scan SCOPED by --filer or --since that matches
nothing is a different thing — it is an answer to the question asked — and exits
0 saying, in words, that it judged nothing.

--probe runs the CONSTRUCTIVE probe instead of the census: it builds a throwaway
macguffin store, drives the real mg binary through new/claim/done, drops one
verdict on purpose and delivers its matched control, and reports whether this
detector went RED on the first and GREEN on the second. Use it when you are
looking at a clean census and want to know whether to believe it. The same probe
runs in ` + "`go test ./...`" + `, so the gate exercises it on every merge — the original
verdictwatch's probes were dead for two days while its census stayed green, and
that is the failure this flag and that test exist to prevent.

Exit status: 0 no dropped verdicts, 1 at least one, ` + fmt.Sprint(exitVerdictUsage) + ` usage error, ` + fmt.Sprint(exitInstrumentFailure) + `
this run measured nothing.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				fmt.Fprintf(os.Stderr, "check-verdicts takes no positional arguments (got %q); scope it with --filer/--since\n", args[0])
				os.Exit(exitVerdictUsage)
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			if asProbe {
				runVerdictProbe(*jsonOutput)
				return
			}
			if since != "" && !sinceLooksLikeATimestamp.MatchString(since) {
				fmt.Fprintf(os.Stderr,
					"--since %q is not an RFC3339 prefix. It is compared as a STRING against the landing\n"+
						"stamp, so a value like this sorts above every stamp in the store and would silently\n"+
						"exclude everything. Use a date (2026-08-05) or a full stamp.\n", since)
				os.Exit(exitVerdictUsage)
			}

			rep, err := verdictwatch.Scan(verdictwatch.Options{Root: root, Filer: filer, Since: since})
			if err != nil {
				// A store that could not be read is not "no findings". This exits
				// as an instrument failure rather than an ordinary error for the
				// same reason the report has a blind state at all: "found
				// something" and "could not see" demand opposite responses.
				fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — this run measured nothing: %v\n", err)
				os.Exit(exitInstrumentFailure)
			}

			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(rep.Render(all, quiet))
			}

			if rep.InstrumentFailure() {
				os.Exit(exitInstrumentFailure)
			}
			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().StringVar(&filer, "filer", "", "Only items filed by this agent (default: every filer)")
	cmd.Flags().StringVar(&since, "since", "", "Only items that landed on/after this RFC3339 prefix, e.g. 2026-08-05")
	cmd.Flags().StringVar(&root, "root", "", "macguffin store root (default: $MG_ROOT, then ~/.macguffin)")
	cmd.Flags().BoolVar(&all, "all", false, "Also list the DELIVERED and UNDECIDABLE rows, not just the drops")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Counts only; suppress the row tables")
	cmd.Flags().BoolVar(&asProbe, "probe", false,
		"Run the constructive probe instead of the census: can this detector still fire?")

	// A flag this command does not understand is a usage error and exits 2 with
	// the rest of them, rather than falling through to cobra's global exit 1.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintf(os.Stderr, "check-verdicts: %v\n\n%s", err, c.UsageString())
		os.Exit(exitVerdictUsage)
		return nil
	})
	return cmd
}

// runVerdictProbe runs the constructive probe and exits on its verdict.
//
// A probe that could not be BUILT exits as an instrument failure, never as a
// pass: that state — the fixture no longer constructible while the census still
// renders green — is exactly where the original verdictwatch spent two days.
func runVerdictProbe(jsonOutput bool) {
	res := verdictwatch.Probe()
	if jsonOutput {
		cli.PrintJSON(map[string]interface{}{
			"store":              res.Store,
			"mg":                 res.MG,
			"arms":               res.Arms,
			"passed":             res.Passed(),
			"instrument_failure": res.InstrumentFailure(),
			"blind":              res.Blind,
		})
	} else {
		fmt.Print(res.Render())
	}
	if res.InstrumentFailure() {
		os.Exit(exitInstrumentFailure)
	}
	if !res.Passed() {
		os.Exit(cli.ExitError)
	}
}
