package main

import (
	"fmt"
	"os"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/config"
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
		Short: "Report landed work items no checked channel carried a verdict to the filer for (never files anything)",
		Long: `Report every work item that reached ` + "`done`" + ` or ` + "`archived`" + ` that NONE OF THE
CHANNELS BELOW carried a verdict to its filer over, ORDERED BY WHEN IT LANDED —
so a backlog can be RECOVERED, not merely alarmed about.

THE PREDICATE, and every half comes from macguffin's own store:

  the landing       work.done / work.archive in events.jsonl
  the filer         ` + "`creator:`" + ` in the item's own frontmatter
  the worker        ` + "`polecat-<name>`" + ` in the item's result sidecar
  the channels      every way a verdict can arrive, ENUMERATED:
                      worker-mail   a message in the filer's mailbox whose
                                    From: is THAT WORKER
                      pogod-notify  pogod's own completion notice for THAT
                                    ITEM (From: pogod), added by mg-f120

Findings come in three kinds, not two:

  DROPPED       landed, and none of the channels above carried a verdict to the
                filer. The finding this exists to produce.
  DELIVERED     a channel carried it, AND THE REPORT NAMES WHICH. Archived mail
                counts: filing a verdict away is not losing it. "worker-mail"
                means a polecat did its job; "pogod-notify" means a backstop
                caught it. Those are different facts about system health and
                collapsing them destroys what this was built to watch.
  UNDECIDABLE   the worker cannot be resolved from mg's own store, so the worker
                channel cannot be checked at all. Counted and listed on its own
                rather than folded into either — a detector that silently
                absorbs what it cannot reach is the defect this lineage keeps
                rediscovering.

WHAT IT MEASURES IS THE NEAR END, and every sentence it prints says so. A
DROPPED row means the channels above carried nothing. It does NOT say a verdict
did not reach the filer: that is a claim about EVERY channel there is, the
channel set grew by one on 2026-08-12, and it will grow again. Until then this
command measured "the worker mailed the filer" and reported "no verdict reached
you" — so mg-f120's pogod notification, which is macguffin mail in the filer's
own mailbox about the right item and merely has a different SENDER, scored
DROPPED. The fix for verdict delivery was being measured by an instrument
structurally unable to register it working. When a channel is added, add it to
the list above; leaving it out does not narrow the report, it falsifies it.

A RELAY IS NOT A DELIVERY. A coordinator forwarding "your item is done" is not
counted, however plainly it names the item: the question is WHO DISCHARGED THE
OBLIGATION, and a predicate that cannot tell a verdict from a mention of one
counts talk about the thing alongside the thing. Each channel therefore matches
a SENDER plus that sender's own notice shape.

One thing is deliberately NOT a channel, named here so its absence is not read as
an oversight. The refinery's MERGED mail to the coordinator reports a merge —
branch, target, SHA — and says nothing about the outcome, so it is a relay by the
above test. That distinction used to cut the other way too: pogod SKIPPED its own
notice for a coordinator-filed item on the merge route because the refinery had
already written, so a coordinator's merge-route rows were dropped here by
construction, and the coordinator's mailbox held zero pogod notices while it filed
more items than anyone. mg-da12 removed that skip — the refinery mails a MERGE and
pogod mails a VERDICT — so those rows are now measured like every other filer's.

THE DROPPED ROWS SPLIT IN TWO, and the split is the difference between work you
can finish now and work that is gone:

  ROUTING   the verdict IS recorded in the item's result sidecar and no channel
            carried it. The report PRINTS THAT VERDICT — this command has the
            sidecar open anyway to resolve the worker, and a row that says
            "nobody was told" alongside what they should have been told is
            recoverable in one pass rather than one item at a time.
  LOST      no channel carried it and nothing is recorded either. The only
            class that is an actual loss.

A filer that CANNOT be reached is separated from one that was reachable and not
told (` + "`reach`" + ` on each row): no mailbox at all, or a notice pogod redirected to
the coordinator because the filer is not a live agent. "Nobody told them" and
"there was nobody to tell" are different findings.

THE VERDICT IS NOT ON THE WORK ITEM OBJECT. ` + "`mg show <id> --json`" + ` has no ` + "`result`" + `
key at all — not an empty one — so pulling one out of it yields null at exit 0,
and a missing key is indistinguishable from a blank verdict that way. That is how
one such recipe survived being handed to three agents. This command does not emit
a retrieval instruction it has not executed: it reads the item's OWN result
sidecar, by explicit path rather than by a glob across the lifecycle directories,
prints the verdict it found there, and prints the command that reproduces it.

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
macguffin store, drives the real mg binary through new/claim/done, and reports
whether this detector went RED on a verdict dropped on purpose and GREEN on each
of its matched controls — the worker mailing the filer, pogod's notice covering
an item the worker never mailed about, and a coordinator RELAY, which must stay
RED because a relayed headline is not a verdict. Use it when you are looking at
a clean census and want to know whether to believe it. The same probe
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

			// The coordinator is read from config rather than assumed, because it
			// is only used to recognise a notice pogod REDIRECTED to it — and a
			// fleet whose coordinator is not `mayor` would otherwise have every
			// redirected row read as a filer nobody tried to tell.
			rep, err := verdictwatch.Scan(verdictwatch.Options{
				Root: root, Filer: filer, Since: since,
				Coordinator: config.Load().Agents.CoordinatorName(),
			})
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
