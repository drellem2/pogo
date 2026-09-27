package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/wakewatch"
)

// newCheckWakewatchCmd is the phase-2 gate for wakewatch (mg-5496, mg-e00c).
// REPORT-ONLY: it reads two logs and prints a join; it nudges, mails and
// changes nothing.
func newCheckWakewatchCmd(jsonOutput *bool) *cobra.Command {
	var since time.Duration
	var storeRoot string
	cmd := &cobra.Command{
		Use:   "check-wakewatch",
		Short: "Report timer-driven mail reads that no wakewatch pointer announced (phase-2 gate)",
		Long: `Joins every mail a TIMER-DRIVEN mail-check turn read against the wakewatch
pointers sent before it, and prints the misses: mail read with no pointer
sent (mg-5496 phase 1, SHADOW).

A read is timer-driven when it lands in one of the boxes a mail-check-* fire
sent its agent to (the agent's name, and for mail-check-mg-<id> the work-item
box) within 10 minutes of that fire. Each such read is classed:

  COVERED         a pointer (or re-nudge) was delivered or queued before the read
  MISS            no pointer was sent for it at all
  MISS-LATE       the only pointer came after the read
  MISS-FAILED     a pointer was attempted before the read and not delivered
  BOUNCED         its recipient was not running when it was sent
  SKIPPED         deliberately not pointed at (self-sent, not an agent's box)
  PRE-ARM         sent before wakewatch armed — nothing to judge
  NO-SEND-RECORD  no mail.sent for it in the store — undecidable

Only the MISS classes fail the gate. Sources: pogod's events.log (rotations
included) and macguffin's events.jsonl. A missing events.jsonl is reported as
BLIND — no data, never "no misses".

Exit codes: 0 no misses, 1 misses found, 3 blind (could not judge).`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			now := time.Now()
			floor := now.Add(-since)
			logPath, err := events.LogPath()
			if err != nil {
				cli.ExitWithError(*jsonOutput, "resolve events log: "+err.Error(), exitInstrumentFailure)
			}
			win, err := events.ReadWindow(logPath, events.Filter{SinceMin: floor})
			if err != nil {
				cli.ExitWithError(*jsonOutput, "read events log: "+err.Error(), exitInstrumentFailure)
			}
			root := storeRoot
			if root == "" {
				root = agent.MacguffinStoreRoot()
			}
			rep := wakewatch.Check(win.Events, filepath.Join(root, "events.jsonl"), floor, now)
			if win.Truncated {
				rep.Blind = append(rep.Blind, fmt.Sprintf("the window starts before retained pogod history at %s", win.Floor.UTC().Format(time.RFC3339)))
			}
			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				rep.Render(os.Stdout)
			}
			switch {
			case len(rep.Misses()) > 0:
				os.Exit(cli.ExitError)
			case len(rep.Blind) > 0:
				os.Exit(exitInstrumentFailure)
			}
		},
	}
	cmd.Flags().DurationVar(&since, "since", 24*time.Hour, "How far back to judge")
	cmd.Flags().StringVar(&storeRoot, "root", "", "macguffin store root (default: $MG_ROOT, then ~/.macguffin)")
	return cmd
}
