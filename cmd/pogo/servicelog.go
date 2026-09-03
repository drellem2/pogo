package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/logliveness"
	"github.com/drellem2/pogo/internal/service"
)

// newServiceLogCmd builds `pogo service log` — the command a diagnostician runs
// BEFORE grepping pogod.log, and the one mayor.md's refinery-log procedure now
// cites.
//
// It exists because the documented procedure was safe against a MISSING file
// and not against a DETACHED one, and the second is the state that actually
// occurred. See internal/logliveness for the measurements.
func newServiceLogCmd(jsonOutput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "log",
		Short: "Is the file the service names as pogod's log a live record of the running pogod?",
		Long: `Compare where the running pogod's stderr actually points against the path its
installed service definition names, and say whether that file is a record of
this daemon at all.

WHY THIS IS A QUESTION (mg-a19a). Measured on this box 2026-09-03: pogod pid
6610 had been up 57h45m, holding the lock, serving :10000, delivering scheduled
fires and running the refinery gate. Its parent was Emacs, its fd 2 was
/dev/ttys007, it held no descriptor on pogod.log, and it had written ZERO lines
there in its entire life. Every diagnostic on the box that grepped pogod.log —
including the refinery-log procedure in the mayor and polecat prompts — was
reading a file written by other processes.

The cost is not a missing log. It is wrong answers that look right. On
2026-09-03 an architect grepped that file for a fleet-stop window, found 51
modal-wedge lines and 1,875 animation lines, and built a specific and plausible
root cause on them; every one of those lines predated the window by days, and
the real cause was an entitlement refusal (mg-6616). A frozen log does not look
empty. It looks rich, current in format, and correct.

WHY NOT JUST CHECK THAT THE FILE IS FRESH. Because that reads GREEN in the
worst case. From 2026-09-01 12:08 to 2026-09-02 01:05 launchd respawned a pogod
every ~10 seconds; each refused the lock held by 6610, exited 1, and wrote two
lines on the way out — runs = 4639, and the file holds exactly 4,639 lock
refusals. For thirteen hours the file the live daemon never touched had an
mtime under ten seconds old. So mtime, size and content are printed here as
CONTEXT, under a line saying what they do not prove, and none of them reaches
the verdict. The verdict is an identity comparison: the descriptor, against the
path.

EXIT CODES — three, because "could not tell" is a real answer:

  0  LIVE      the running daemon's stderr IS that file. An empty grep against
               it is a genuine negative result.
  1  DETACHED  the daemon writes somewhere else, or the file is absent. An
               empty grep proves nothing, and a NON-empty one is about some
               other process.
  3  UNKNOWN   a reading was missing — nothing holds the lockfile, or the
               daemon's fd 2 could not be inspected. NOT a pass.

REPORT-ONLY. This command never starts, stops or restarts anything, and never
writes to the log it judges.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// The job's path is the one a human following mayor.md derives and
			// greps, so it is what gets judged. The template default is used
			// only when no job is installed to name one — and the report says
			// which it was, because they are different claims.
			jobPath, haveJob := service.InstalledLogPath()
			judged := jobPath
			if !haveJob {
				judged = service.PogodLogPath()
			}
			res := logliveness.CheckHost(judged, jobPath)

			if *jsonOutput {
				cli.PrintJSON(res)
			} else {
				fmt.Print(res.Text())
				if !haveJob {
					fmt.Printf("  note            : no installed launchd job names a log path; judged this build's default. On Linux the unit sets no redirect at all and the output is in the journal, not a file.\n")
				}
			}

			switch res.Verdict {
			case logliveness.Live:
				os.Exit(cli.ExitSuccess)
			case logliveness.Detached:
				os.Exit(cli.ExitError)
			default:
				os.Exit(cli.ExitUnknown)
			}
		},
	}
}
