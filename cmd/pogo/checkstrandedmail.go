package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/scheduler"
	"github.com/drellem2/pogo/internal/strandedmail"
)

// newCheckStrandedMailCmd builds `pogo check-strandedmail` (mg-aa96): the sweep
// for mail sitting in a mailbox no live mail-check reads.
//
// Sibling of check-mailloops. That one finds an agent nothing can WAKE; this one
// finds mail nothing will ever READ — the residue a repointed mail-check leaves
// behind.
func newCheckStrandedMailCmd(jsonOutput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "check-strandedmail",
		Short: "Report mail sitting in a mailbox no live mail-check reads (never acts)",
		Long: `Report every mailbox that holds unread mail which no live mail-check polls.

Each mail-check schedule carries two identities: the agent it is FOR, and the
work item its id is KEYED on. Before mg-aa96 the message body derived the
mailbox from the second, while correspondents address the first
(--from=$POGO_AGENT_NAME). This sweep takes each live mail-check, works out the
mailbox it WOULD have read under that derivation, and asks mg whether anything
is sitting in it.

WHY REPOINTING WAS NOT ENOUGH. Fixing a mail-check only changes where the agent
looks NEXT. Everything already delivered to the abandoned box stays there, and
the repoint turns a misdelivery into an ORPHAN — a silent cutover with the same
shape as the bug it fixes: mail exists, nobody reads it, nothing says so. The
2026-08-05 sweep of the live fleet found one, box "b468" holding 1 unread while
agent "wb468" polled "wb468". It was an urgent correction to a builder
mid-flight.

CORRECTIONS ARE THE TRAFFIC AT RISK, which is why the report names senders and
subjects rather than counts. A correction is sent off-cadence to an agent
already working — exactly what a scheduled poll handles worst — and both
near-misses that day were retractions of a wrong premise to a builder in the
middle of building on it. Losing one of those costs more than losing a status
ping.

REPORTS ONLY — it never moves, forwards, or archives mail. Re-delivering would
have to forge a sender (mg mail send writes a new message with a new From),
which turns a recoverable orphan into a message whose provenance is a lie. The
report prints the exact ` + "`mg mail read`" + ` for each stranded message instead.

A sweep with no mail-check schedules to judge says so in as many words rather
than printing an all-clear: "nothing is wrong" and "I could not look" rendering
identically is the exact failure this whole lineage is about.

Exit status is 0 when nothing is stranded, 1 when any mailbox holds mail nobody
reads (so it can gate a schedule or CI step).`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			entries, err := client.ListSchedules("")
			if err != nil {
				cli.ExitWithError(*jsonOutput, fmt.Sprintf("cannot read schedules from pogod: %v", err), cli.ExitError)
			}
			var checks []strandedmail.MailCheck
			for _, e := range entries {
				if e.Kind != scheduler.KindMailCheck {
					continue
				}
				// Parse the mailbox with the SAME function the registration
				// guard uses, so "where does this agent actually look?" has one
				// answer in this tree rather than two that can drift.
				polled, _ := scheduler.MailCheckMailbox(e.Message)
				checks = append(checks, strandedmail.MailCheck{
					Agent:      e.Agent,
					ScheduleID: e.ID,
					Polled:     polled,
				})
			}

			boxes, err := strandedmail.EnumerateMailboxes()
			if err != nil {
				cli.ExitWithError(*jsonOutput, fmt.Sprintf("cannot enumerate mailboxes: %v", err), cli.ExitError)
			}

			rep := strandedmail.Detect(checks, boxes, strandedmail.ListMessages)
			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(rep.Render())
			}
			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}
}
