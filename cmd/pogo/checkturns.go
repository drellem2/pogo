package main

// `pogo check-turns`: the reader half of the turn-completion artifact
// (mg-a270), and the newest member of the check-* family — read-only, reports a
// condition, takes no action.
//
// It is the first check in this repo whose evidence is written by the AGENTS
// rather than by pogod, and that is its only interesting property. Every other
// liveness-adjacent signal on this machine describes something the daemon did.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/turnlog"
)

// exitTurnsUsage matches the check-* family's convention: 0 clean, 1 finding,
// 2 usage, 3 this run measured nothing.
const exitTurnsUsage = 2

func newCheckTurnsCmd(jsonOutput *bool) *cobra.Command {
	var (
		maxAge   time.Duration
		allTypes bool
		asProbe  bool
	)
	cmd := &cobra.Command{
		Use:   "check-turns",
		Short: "Report agents that are present but have completed no recent turn (never acts)",
		Long: `Report every running agent whose last COMPLETED TURN is missing or stale.

THE TEST THIS IS BUILT TO PASS (mg-a270, architect's wording):

  A liveness check must name an artifact THAT ONLY A COMPLETED TURN COULD HAVE
  WRITTEN, with a timestamp after the bounce. If it would read green over a
  fleet that is present and doing nothing, it is a presence check wearing a
  liveness label — whatever it is named.

The artifact is ` + turnlog.Dir() + `/<agent>.log, one line per completed
turn, written by the agent itself via ` + "`pogo turn-done`" + `. Nothing but a finished
turn produces it. That is the whole difference between this check and the ones
that read green for twenty-two hours on 2026-08-10/11 while the fleet did no
work: process present, schedule registered, 140 nudges delivered, revision
current — every one of them true, every one of them about the daemon.

THE JOIN DIRECTION IS THE DESIGN, and it is worth stating because it is the
easy thing to get backwards:

  population   pogod's agent registry — who is PRESENT
  evidence     the turnlog files      — who FINISHED A TURN

It iterates the population and looks up the evidence. It never lists the
turnlog directory to decide who to report on. An agent that has never written a
line has no file, so a check built the other way would score the silent agents
as nothing at all — and "mayor, pa and architect write nothing" was the entire
finding of the ticket that produced this command.

An unreachable registry therefore exits ` + fmt.Sprint(exitInstrumentFailure) + `, never 0: without the population
this run measured nothing, and it says so instead of reporting a clean fleet.

VERDICTS:

  live        completed a turn within --max-age
  stale       has completed turns, none recently
  silent      present, and has NEVER written one — the mg-a270 state
  unreadable  a turnlog exists and could not be read or parsed

SCOPE. Crew agents only by default. Polecat prompts do not carry the
turn-completion clause — a polecat's work is evidenced by its claim re-stamp,
its branch and its merge, and it is stopped minutes after finishing — so
including them would report a permanent red that means nothing. --all-types
includes them anyway, which is how you point this at an agent you know has not
written a line.

A FRESHLY DEPLOYED FLEET READS RED, AND THAT READING IS TRUE. The clause reaches
an agent when its prompt is rendered, i.e. at its next start. Every crew agent
running from before this shipped will read ` + "`silent`" + ` until it is bounced. That is
not a false positive — none of them has written the artifact — but it is also
not the fault this is watching for, so bounce the fleet after deploying and read
it again.

--probe runs the POSITIVE CONTROL instead of the census. It builds a throwaway
turnlog tree holding an agent that just completed a turn, one that completed its
last long ago, and one that never completed any, and requires this same check to
report the last two and leave the first alone. Run it when you are looking at a
clean census and want to know whether to believe it — a liveness check that has
never been observed failing is a presence check until proven otherwise. The same
probe runs in ` + "`go test ./...`" + `, so every merge exercises the failing arm.

Exit status: 0 no findings, 1 at least one, ` + fmt.Sprint(exitTurnsUsage) + ` usage error, ` + fmt.Sprint(exitInstrumentFailure) + ` this run
measured nothing.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				fmt.Fprintf(os.Stderr, "check-turns takes no positional arguments (got %q)\n", args[0])
				os.Exit(exitTurnsUsage)
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			if asProbe {
				runTurnProbe(*jsonOutput)
				return
			}
			rep, err := turnlog.Scan(turnlog.Options{
				MaxAge:     maxAge,
				Population: func() ([]turnlog.Present, error) { return presentAgents(allTypes) },
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — this run measured nothing: %v\n", err)
				os.Exit(exitInstrumentFailure)
			}
			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(renderTurnReport(rep, allTypes))
			}
			if rep.Findings > 0 {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().DurationVar(&maxAge, "max-age", turnlog.DefaultMaxAge,
		"Age at which a last completed turn stops counting as live")
	cmd.Flags().BoolVar(&allTypes, "all-types", false,
		"Include polecats in the population (their prompts carry no turn-completion clause)")
	cmd.Flags().BoolVar(&asProbe, "probe", false,
		"Run the positive control instead of the census: can this check still go red?")

	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintf(os.Stderr, "check-turns: %v\n\n%s", err, c.UsageString())
		os.Exit(exitTurnsUsage)
		return nil
	})
	return cmd
}

// presentAgents asks pogod which agents are present.
//
// The error is propagated rather than swallowed — see turnlog.ErrNoPopulation.
// `restarting` counts as present: that agent is coming back, and a check that
// dropped it from the population would go quiet about an agent mid-bounce,
// which is one of the two moments this instrument is for.
func presentAgents(allTypes bool) ([]turnlog.Present, error) {
	agents, err := client.ListAgents()
	if err != nil {
		return nil, err
	}
	var out []turnlog.Present
	for _, a := range agents {
		if a.Status != agent.StatusRunning && a.Status != agent.StatusRestarting {
			continue
		}
		if !allTypes && a.Type != agent.TypeCrew {
			continue
		}
		out = append(out, turnlog.Present{Name: a.Name, Type: string(a.Type)})
	}
	return out, nil
}

// renderTurnReport writes the human form. The population count is printed even
// when there are no findings, because "every present agent completed a turn"
// and "no agent was examined" are the two readings this has to keep apart —
// they are the same green otherwise, and the second one is what twenty-two
// hours of this fleet looked like.
func renderTurnReport(rep turnlog.Report, allTypes bool) string {
	var b strings.Builder
	scope := "crew"
	if allTypes {
		scope = "all types"
	}
	fmt.Fprintf(&b, "turn completion — %s\n", rep.Dir)
	fmt.Fprintf(&b, "  population %d agent(s) present (%s, from pogod's registry), max-age %s\n",
		len(rep.Agents), scope, rep.MaxAge)
	fmt.Fprintf(&b, "  %d live, %d stale, %d silent, %d unreadable\n",
		rep.Live, rep.Stale, rep.Silent, rep.Bad)

	if len(rep.Agents) == 0 {
		b.WriteString("\nNo agent was examined. This is NOT a clean fleet — it is an empty\n" +
			"population, and nothing here says any agent completed a turn.\n")
		return b.String()
	}

	b.WriteString("\n")
	for _, s := range rep.Agents {
		age := "never"
		if !s.Last.IsZero() {
			age = shortDur(s.Age()) + " ago"
		}
		fmt.Fprintf(&b, "  %-10s %-20s %s\n", s.Verdict, s.Agent, age)
		if s.Note != "" {
			fmt.Fprintf(&b, "             last: %s\n", s.Note)
		}
		if s.Detail != "" {
			fmt.Fprintf(&b, "             %s\n", s.Detail)
		}
	}

	if rep.Findings == 0 {
		b.WriteString("\nEvery present agent has completed a turn within the window.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "\n%d AGENT(S) PRESENT WITHOUT A RECENT COMPLETED TURN.\n", rep.Findings)
	if rep.Silent > 0 {
		b.WriteString("A `silent` agent has written no turn-completion line at all. Either it has\n" +
			"not completed a turn since it started, or it is running a prompt rendered\n" +
			"before this artifact existed — check its uptime before concluding which.\n")
	}
	b.WriteString("This command took no action. Diagnose before restarting anything:\n" +
		"  pogo agent diagnose <name> --json | jq '{health, health_detail, restart_suppressed, transcript_check}'\n" +
		"An agent failing every turn in 10ms is not wedged, and restarting it destroys\n" +
		"the transcript that makes the condition diagnosable.\n")
	return b.String()
}

// shortDur renders a duration the way a person reads an age.
func shortDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

// runTurnProbe runs the positive control and exits on its verdict.
//
// A probe that could not be BUILT exits as an instrument failure, never as a
// pass — the check-* family keeps rediscovering that a detector reported healthy
// on the strength of a fixture that no longer constructs is worse than one
// reported broken.
// The os.Exit calls live HERE and the temp directory lives in the callee, so
// the callee's defer is reached on every verdict — deferred functions do not run
// on os.Exit, and until mg-60eb this function exited past its own
// `defer os.RemoveAll` on both failure arms, leaking the probe's store exactly
// when the probe failed.
func runTurnProbe(jsonOutput bool) {
	if code := turnProbeVerdict(jsonOutput); code != 0 {
		os.Exit(code)
	}
}

// turnProbeVerdict conducts the probe and returns the exit code its verdict
// calls for, 0 meaning pass. It never calls os.Exit, which is the whole point.
func turnProbeVerdict(jsonOutput bool) int {
	dir, err := os.MkdirTemp("", "turnprobe")
	if err != nil {
		fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — probe could not be built: %v\n", err)
		return exitInstrumentFailure
	}
	defer os.RemoveAll(dir)

	res, err := turnlog.Probe(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — probe could not be built: %v\n", err)
		return exitInstrumentFailure
	}
	if jsonOutput {
		cli.PrintJSON(res)
	} else {
		fmt.Printf("positive control for check-turns\n")
		fmt.Printf("  probe-live    (completed a turn just now)   -> %s\n", res.LiveVerdict)
		fmt.Printf("  probe-stale   (last turn 48h ago)           -> %s\n", res.StaleVerdict)
		fmt.Printf("  probe-silent  (present, never wrote a line) -> %s\n", res.SilentVerdict)
		fmt.Printf("\n%s: %s\n", map[bool]string{true: "PASS", false: "FAIL"}[res.Passed], res.Detail)
	}
	if !res.Passed {
		return cli.ExitError
	}
	return 0
}
