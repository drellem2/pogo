package main

// `pogo check-heartbeats`: the reader half of the crew heartbeat (mg-d616), and
// the newest member of the check-* family — read-only, reports a condition,
// takes no action.
//
// The check itself is not new. It has been written down in mayor.md §3a since
// mg-60ca: list `~/.pogo/agents/pm/*/sweep.log`, read each mtime, nudge at 90
// minutes, restart at 120. What was new in mg-d616 is that it had exactly ONE
// executor and that executor was a step in the coordinator's own coordination
// loop, so it did not degrade when the coordinator stopped — it stopped, and
// two PMs sat 14 days at ~168x T_restart with nothing firing.
//
// This command and internal/heartwatch (pogod-resident) are the two executors
// that are not the coordinator.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/heartwatch"
)

// exitHeartbeatsUsage matches the check-* family's convention: 0 clean, 1
// finding, 2 usage, 3 this run measured nothing.
const exitHeartbeatsUsage = 2

func newCheckHeartbeatsCmd(jsonOutput *bool) *cobra.Command {
	var (
		stallAfter   time.Duration
		restartAfter time.Duration
		grace        time.Duration
		allTypes     bool
		asProbe      bool
	)
	cmd := &cobra.Command{
		Use:   "check-heartbeats",
		Short: "Report crew agents whose sweep.log heartbeat has gone late (never acts)",
		Long: `Report every running crew agent whose sweep.log heartbeat is late or missing.

WHY THIS COMMAND EXISTS, given that the check already did (mg-d616):

  The crew heartbeat check was a step in the coordinator's coordination loop
  and had no other executor. So it did not degrade when the coordinator
  stopped: it stopped. pm-onethird and pm-riemann sat 14 days at ~168x
  T_restart and nothing nudged or restarted either of them. That is not a
  threshold to tune — it is a detector routed through the one agent whose
  failure it would have to report.

Each crew agent refreshes a heartbeat line in its sweep.log on every
mail-check, a ten-minute cadence, so the file's mtime is this fleet's tightest
liveness signal. Two paths are searched per agent, because the tree grew two
shapes — ` + "`agents/pm/<name>/sweep.log`" + ` for a PM and ` + "`agents/<name>/sweep.log`" + `
for everyone else including the coordinator — and the NEWEST that exists wins.

THE JOIN DIRECTION IS THE DESIGN:

  population   pogod's agent registry — who is PRESENT
  evidence     the sweep.log mtimes   — who is still TICKING

It iterates the population and looks up the evidence. It never lists the
sweep.log tree to decide who to report on: this machine carries three sweep
logs whose agents have not existed for months, and a tree-first scan reports
those forever. A permanently red detector is one nobody reads.

An unreachable registry exits ` + fmt.Sprint(exitInstrumentFailure) + `, never 0: without the population this
run measured nothing, and it says so instead of reporting a clean fleet.

VERDICTS:

  fresh        heartbeat inside --stall-after (or inside the post-start grace)
  stale        --stall-after < age <= --restart-after; mayor.md nudges here
  restart_due  age > --restart-after; mayor.md restarts here, after diagnosing
  missing      present, and publishes no sweep.log under any known path
  unreadable   a sweep.log exists and could not be read, or its mtime is in
               the future

A ` + "`missing`" + ` row is never folded into fresh. "No file" and "a file nobody was
watching" are the two readings this whole ticket is about.

THIS COMMAND NEVER NUDGES OR RESTARTS, and neither does pogod's resident copy.
A stale heartbeat has two causes that look identical from outside and take
OPPOSITE responses: a wedged session (restart is right) and an agent failing
every turn in ~10ms on an expired credential or a spend cap (restart destroys
the transcript that diagnoses it, and the replacement inherits the credential).
On 2026-07-22 that distinction cost 23h30m. Diagnose first:

  pogo agent diagnose <name> --json | jq '{health, health_detail, restart_suppressed, transcript_check}'

--probe runs the POSITIVE CONTROL instead of the census. It builds a throwaway
agent tree holding an agent whose heartbeat is fresh, one past T_stall, one
past T_restart, and one that publishes none at all, and requires this same
check to report the last three and leave the first alone. Run it when you are
looking at a clean census and want to know whether to believe it — this check
spent its whole life never having been observed firing, and when it was finally
measured it had not fired for two agents across fourteen days.

Exit status: 0 no findings, 1 at least one, ` + fmt.Sprint(exitHeartbeatsUsage) + ` usage error, ` + fmt.Sprint(exitInstrumentFailure) + ` this run
measured nothing.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				fmt.Fprintf(os.Stderr, "check-heartbeats takes no positional arguments (got %q)\n", args[0])
				os.Exit(exitHeartbeatsUsage)
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			if asProbe {
				runHeartbeatProbe(*jsonOutput)
				return
			}
			rep, err := heartwatch.Scan(heartwatch.ScanOptions{
				StallAfter:   stallAfter,
				RestartAfter: restartAfter,
				Grace:        grace,
				Population:   func() ([]heartwatch.Present, error) { return presentHeartbeatAgents(allTypes) },
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — this run measured nothing: %v\n", err)
				os.Exit(exitInstrumentFailure)
			}
			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(renderHeartbeatReport(rep, allTypes))
			}
			if rep.Findings > 0 {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().DurationVar(&stallAfter, "stall-after", heartwatch.DefaultStallAfter,
		"Age at which a heartbeat stops counting as fresh (mayor.md's T_stall)")
	cmd.Flags().DurationVar(&restartAfter, "restart-after", heartwatch.DefaultRestartAfter,
		"Age at which the coordinator's own rules call the reading actionable (T_restart)")
	cmd.Flags().DurationVar(&grace, "grace", heartwatch.DefaultGrace,
		"Post-start window in which an agent owes no heartbeat yet")
	cmd.Flags().BoolVar(&allTypes, "all-types", false,
		"Include polecats in the population (their prompts carry no heartbeat clause)")
	cmd.Flags().BoolVar(&asProbe, "probe", false,
		"Run the positive control instead of the census: can this check still go red?")

	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintf(os.Stderr, "check-heartbeats: %v\n\n%s", err, c.UsageString())
		os.Exit(exitHeartbeatsUsage)
		return nil
	})
	return cmd
}

// presentHeartbeatAgents asks pogod which agents are present.
//
// The error is propagated rather than swallowed — see heartwatch.ErrNoPopulation.
// `restarting` counts as present: that agent is coming back, and a check that
// dropped it would go quiet about an agent mid-bounce, which is one of the two
// moments this instrument is for.
func presentHeartbeatAgents(allTypes bool) ([]heartwatch.Present, error) {
	agents, err := client.ListAgents()
	if err != nil {
		return nil, err
	}
	var out []heartwatch.Present
	for _, a := range agents {
		if a.Status != agent.StatusRunning && a.Status != agent.StatusRestarting {
			continue
		}
		if !allTypes && a.Type != agent.TypeCrew {
			continue
		}
		out = append(out, heartwatch.Present{
			Name: a.Name, Type: string(a.Type), StartedAt: a.StartTime,
		})
	}
	return out, nil
}

// renderHeartbeatReport writes the human form.
//
// The population count is printed even when there are no findings, because
// "every present agent is ticking" and "no agent was examined" are the two
// readings this has to keep apart. They are the same green otherwise, and the
// second one is the shape that hid a fourteen-day stall.
func renderHeartbeatReport(rep heartwatch.Report, allTypes bool) string {
	var b strings.Builder
	scope := "crew"
	if allTypes {
		scope = "all types"
	}
	fmt.Fprintf(&b, "crew heartbeats — %s\n", rep.Root)
	fmt.Fprintf(&b, "  population %d agent(s) present (%s, from pogod's registry)\n", rep.Examined, scope)
	fmt.Fprintf(&b, "  T_stall %s, T_restart %s\n", rep.StallAfter, rep.RestartAfter)
	fmt.Fprintf(&b, "  %d fresh, %d stale, %d past T_restart, %d missing, %d unreadable\n",
		rep.Fresh, rep.Stale, rep.RestartDue, rep.Missing, rep.Bad)

	if rep.Examined == 0 {
		b.WriteString("\nNo agent was examined. This is NOT a clean fleet — it is an empty\n" +
			"population, and nothing here says any agent has a live heartbeat.\n")
		return b.String()
	}

	b.WriteString("\n")
	for _, s := range rep.Agents {
		age := "never"
		if !s.Last.IsZero() {
			age = shortDur(s.Age()) + " ago"
		}
		fmt.Fprintf(&b, "  %-12s %-20s %s\n", s.Verdict, s.Agent, age)
		if s.Detail != "" {
			fmt.Fprintf(&b, "               %s\n", s.Detail)
		}
	}

	if rep.WakeSuppressed {
		fmt.Fprintf(&b, "\nA system_wake landed at %s. pogod's resident copy holds its ANNOUNCEMENT\n"+
			"after a wake — post-sleep schedule replay makes a stale heartbeat expected — but the\n"+
			"reading above stands either way, and this command does not suppress it.\n",
			rep.WokeAt.UTC().Format(time.RFC3339))
	}

	if rep.Findings == 0 {
		b.WriteString("\nEvery present crew agent has a fresh heartbeat.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "\n%d CREW HEARTBEAT(S) ARE NOT FRESH.\n", rep.Findings)
	if rep.Missing > 0 {
		b.WriteString("A `missing` agent publishes no sweep.log at all. Either its tier keeps no\n" +
			"heartbeat, or it is running a prompt rendered before the clause existed — check\n" +
			"its uptime before concluding which. It is not a healthy reading.\n")
	}
	b.WriteString("This command took no action. Diagnose before nudging or restarting anything:\n" +
		"  pogo agent diagnose <name> --json | jq '{health, health_detail, restart_suppressed, transcript_check}'\n" +
		"An agent failing every turn in ~10ms is not wedged, and restarting it destroys the\n" +
		"transcript that makes the condition diagnosable.\n")
	return b.String()
}

// runHeartbeatProbe runs the positive control and exits on its verdict.
//
// The os.Exit calls live HERE and the temp directory lives in the callee, so
// the callee's defer is reached on every verdict — deferred functions do not
// run on os.Exit, and mg-60eb is the bill for exiting past a probe's own
// cleanup on exactly the arm where the probe failed.
func runHeartbeatProbe(jsonOutput bool) {
	if code := heartbeatProbeVerdict(jsonOutput); code != 0 {
		os.Exit(code)
	}
}

// heartbeatProbeVerdict conducts the probe and returns the exit code its
// verdict calls for, 0 meaning pass. It never calls os.Exit.
func heartbeatProbeVerdict(jsonOutput bool) int {
	dir, err := os.MkdirTemp("", "heartprobe")
	if err != nil {
		fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — probe could not be built: %v\n", err)
		return exitInstrumentFailure
	}
	defer os.RemoveAll(dir)

	res, err := heartwatch.Probe(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — probe could not be built: %v\n", err)
		return exitInstrumentFailure
	}
	if jsonOutput {
		cli.PrintJSON(res)
	} else {
		fmt.Printf("positive control for check-heartbeats\n")
		fmt.Printf("  probe-fresh    (heartbeat a minute ago)      -> %s\n", res.FreshVerdict)
		fmt.Printf("  probe-stale    (heartbeat past T_stall)      -> %s\n", res.StaleVerdict)
		fmt.Printf("  probe-cold     (heartbeat past T_restart)    -> %s\n", res.ColdVerdict)
		fmt.Printf("  probe-missing  (present, publishes none)     -> %s\n", res.MissingVerdict)
		fmt.Printf("\n%s: %s\n", map[bool]string{true: "PASS", false: "FAIL"}[res.Passed], res.Detail)
	}
	if !res.Passed {
		return cli.ExitError
	}
	return 0
}
