package main

// `pogo check-progress`: the on-demand half of the fleet-productivity detector
// (mg-516e), and the only check in this family whose subject is the FLEET's
// output rather than any agent's state.
//
// It exists because of how the incident it is named for was found. On
// 2026-08-14 mayor ran three checks by hand — `pogo agent list`, worktree
// mtimes, `pogo host load` — and noticed they disagreed: seven polecats
// PTY-active within 4 minutes, none having written a file in 15, the fleet
// holding 0.10 of 10 cores, nothing merged in half an hour. Each reading alone
// was unremarkable. This command is those readings in one place, taken at one
// instant, with the conjunction already evaluated.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/progresswatch"
)

// exitProgressUsage matches the check-* family's convention: 0 clean, 1
// finding, 2 usage, 3 this run measured nothing.
const exitProgressUsage = 2

func newCheckProgressCmd(jsonOutput *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check-progress",
		Short: "Report whether the fleet is GETTING ANYTHING DONE (never acts)",
		Long: `Report the four measurements that separate a fleet which is working from one
that is merely awake, and whether they hold together.

THE STATE THIS IS BUILT FOR (mg-516e). On 2026-08-14 the fleet reached this:

  7 polecats PTY-active within 4 minutes    so: not wedged
  none had written a file in 15 minutes     so: not working
  fleet holding 0.10 of 10 cores            so: not computing
  no merge landed in ~30 minutes            so: not producing

No alarm fired and none could have. Every other signal answers "is it dead?" or
"is it erroring?", and a worker blocked on a slow or unreachable API is neither
— it is ALIVE, NOT FAILING, and getting nothing done. It was found because a
routine liveness check came back CONFUSING rather than red.

WHAT IT MEASURES, all four at one instant:

  awake      per-worker PTY last-write, from pogod's registry
  writing    the newest file mtime anywhere in each worker's worktree
  computing  each worker's PROCESS SUBTREE, not the worker process — a parent
             reads 0.0% while its children burn cores (mg-eb47), and a worker
             running the gate is silent on PTY and worktree by construction
  landing    the refinery's merges and submissions, and closed work items

Any ONE of those is ordinary on its own: an agent thinking, a read-only task, a
quiet minute, a long gate. The conjunction is not, and it has one ordinary
explanation — everyone is waiting on the same remote. So the first thing to
check on a finding is whether the model API and the git remote are reachable
from this host.

IT PRINTS THE NUMBERS, NOT A VERDICT WORD. That is deliberate (mg-c058): a
state token invites a present-tense over-reading that the measurement cannot,
and "AGENTS ARE FAILING EVERY TURN" once paged a sleeping human over 2 errors
in a trailing 30 minutes. The numbers are printed on a CLEAN reading too —
which of them rescued the fleet is the part a coordinator chasing a hunch needs.

A MEASUREMENT THAT COULD NOT BE TAKEN IS A THIRD ANSWER. An unreadable worktree
is not an unwritten one, and an unresolvable CPU sample reports zeros that mean
"this host cannot tell". Any such gap suppresses the finding and is printed
under NOT MEASURED — and exits ` + fmt.Sprint(exitInstrumentFailure) + `, because that run measured nothing.

WITH --json, SWITCH ON "verdict", NOT ON "stalled". "stalled" is the
conjunction and is false for a clean reading AND for a blind one, so a consumer
that checks it alone cannot tell a healthy fleet from a run that measured
nothing (mg-e75b). "verdict" is always present and is one of:

  clean      the reading was taken and found nothing
  stalled    the fleet is alive and landing nothing
  blind      a member of the conjunction could not be measured
  unknown    no reading was taken at all

They correspond one-to-one with the exit codes below, and none of the four is
a prefix of another, so grep and equality agree.

The standing detector is the same code on pogod's heartbeat, reading the same
source with the same thresholds, so this command cannot disagree with the mail
that arrives at 03:00.

Exit status: 0 no finding, 1 the fleet is landing nothing, ` + fmt.Sprint(exitProgressUsage) + ` usage error,
` + fmt.Sprint(exitInstrumentFailure) + ` this run measured nothing.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				fmt.Fprintf(os.Stderr, "check-progress takes no positional arguments (got %q)\n", args[0])
				os.Exit(exitProgressUsage)
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			reading, err := client.GetFleetProgress()
			if err != nil {
				// Includes the 503 for a daemon where the detector is not
				// armed. "Not armed" is not "fleet is fine", and this is the
				// exit code that keeps the two apart.
				cli.ExitWithError(*jsonOutput,
					fmt.Sprintf("INSTRUMENT FAILURE — this run measured nothing: %v", err),
					exitInstrumentFailure)
			}
			if *jsonOutput {
				cli.PrintJSON(reading)
			} else {
				fmt.Print(renderProgressReading(*reading))
			}
			// One switch for both output modes, on the same tri-state the JSON
			// carries, so the exit code cannot disagree with the field a
			// consumer read (mg-e75b).
			switch reading.Verdict() {
			case progresswatch.VerdictBlind, progresswatch.VerdictUnknown:
				os.Exit(exitInstrumentFailure)
			case progresswatch.VerdictStalled:
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintf(os.Stderr, "check-progress: %v\n\n%s", err, c.UsageString())
		os.Exit(exitProgressUsage)
		return nil
	})
	return cmd
}

// renderProgressReading writes the human form: the four measurements as a
// table, then the verdict, then whatever held it back.
//
// The table is printed on every reading, clean ones included. A check that
// prints its inputs only when it fires cannot be used to chase a hunch, and
// chasing a hunch is exactly how the state this reports was found.
func renderProgressReading(r progresswatch.Reading) string {
	var b strings.Builder
	fmt.Fprintf(&b, "fleet progress — %s\n\n", r.Now.UTC().Format("2006-01-02T15:04:05Z"))

	fmt.Fprintf(&b, "  awake+silent  %d of %d judged worker(s) PTY-active and writing nothing\n",
		r.Blocked, r.Judged)
	if r.LiveWorkers != r.Judged {
		fmt.Fprintf(&b, "                %d live, %d too young to judge (under %s)\n",
			r.LiveWorkers, r.LiveWorkers-r.Judged, r.Thresholds.MinWorkerAge)
	}
	if len(r.BlockedNames) > 0 {
		fmt.Fprintf(&b, "                %s\n", strings.Join(r.BlockedNames, ", "))
		fmt.Fprintf(&b, "                stalest PTY write %s ago, freshest file write %s ago\n",
			r.MaxPTYIdle.Round(time.Second), r.MinWriteIdle.Round(time.Second))
	}
	if r.CoresKnown {
		fmt.Fprintf(&b, "  computing     worker subtrees at %.2f of %d cores (floor %.2f)\n",
			r.WorkerCores, r.HostCores, r.Thresholds.IdleCores)
	} else {
		fmt.Fprintf(&b, "  computing     NOT MEASURED\n")
	}
	switch {
	case !r.ProgressKnown:
		fmt.Fprintf(&b, "  landing       NOT READ\n")
	case r.LastProgressWhat != "":
		fmt.Fprintf(&b, "  landing       nothing in %s — last was %s\n",
			r.SinceProgress.Round(time.Second), r.LastProgressWhat)
	default:
		fmt.Fprintf(&b, "  landing       nothing in the %s observed\n", r.SinceProgress.Round(time.Second))
	}
	if r.InFlight != "" {
		fmt.Fprintf(&b, "  in flight     merge %s, holding the refinery slot for %s\n",
			r.InFlight, r.InFlightFor.Round(time.Second))
	}

	b.WriteString("\n")
	// The human render deliberately prints no verdict TOKEN (mg-c058): a state
	// word invites a present-tense over-reading the measurement cannot support.
	// The tri-state it switches on is the same one --json names, so the two
	// modes cannot disagree about which state this is — only about how loudly
	// they say it.
	switch r.Verdict() {
	case progresswatch.VerdictBlind, progresswatch.VerdictUnknown:
		b.WriteString("NOT MEASURED — the conjunction has a member this run could not read:\n")
		for _, x := range r.Blind {
			fmt.Fprintf(&b, "  - %s\n", x)
		}
		if len(r.Blind) == 0 {
			// VerdictUnknown: a Reading that was never evaluated. It has no
			// blind list to name because no measurement was attempted, and it
			// still must not fall through to the clean paragraph.
			b.WriteString("  - no reading was taken at all\n")
		}
		// NOT "No finding is possible" — that is the CLEAN render's own headline
		// with a qualifier bolted on, and the qualifier is the half a skimmer
		// drops and a grep never sees. The blind lead token is NOT MEASURED
		// everywhere else this reading is rendered (Reading.String), and a
		// state whose words are a prefix of the healthy state's words is the
		// false-green this whole command exists to make impossible.
		b.WriteString("\nNo reading is possible from this run, clean or otherwise.\n")
		b.WriteString("That is not a clean fleet — it is a fleet nobody measured.\n")
	case progresswatch.VerdictStalled:
		b.WriteString("FLEET IS ALIVE AND LANDING NOTHING.\n\n")
		b.WriteString("All four measurements hold together, and they have one ordinary\n")
		b.WriteString("explanation: every worker is waiting on the same remote. Check whether\n")
		b.WriteString("the model API and the git remote are reachable from this host.\n")
	default:
		b.WriteString("No finding. What rules it out:\n")
		for _, h := range r.Held {
			fmt.Fprintf(&b, "  - %s\n", h)
		}
	}
	return b.String()
}
