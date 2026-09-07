package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/providers"
	"github.com/drellem2/pogo/internal/refusalstreak"
	"github.com/drellem2/pogo/internal/refusalwatch"
)

// refusalRow is one agent's reading, for rendering and --json.
type refusalRow struct {
	Agent   string               `json:"agent"`
	Workdir string               `json:"workdir,omitempty"`
	Report  refusalstreak.Report `json:"report"`
}

// refusalReport is the whole census.
type refusalReport struct {
	Rows []refusalRow `json:"rows"`
	// Blind carries every reason this run could not look somewhere. It is
	// separate from the rows because "we read six transcripts and none is
	// streaking" and "we could not read any" are opposite findings that a count
	// of zero renders identically.
	Blind []string `json:"blind,omitempty"`
}

func (r refusalReport) streaking() []refusalRow {
	var out []refusalRow
	for _, row := range r.Rows {
		if row.Report.Alarming() {
			out = append(out, row)
		}
	}
	return out
}

// InstrumentFailure is true when nothing could be judged at all. A census that
// judged nobody exits 3, never 0: on 2026-09-07 the difference between "the
// fleet is fine" and "I cannot see the fleet" was five and a half hours.
func (r refusalReport) InstrumentFailure() bool {
	for _, row := range r.Rows {
		if row.Report.State != refusalstreak.StateUnavailable {
			return false
		}
	}
	return true
}

func (r refusalReport) Render() string {
	var b strings.Builder
	if len(r.Rows) == 0 {
		b.WriteString("No agents to read. This is not a clean fleet — it is an empty scan.\n")
	}
	for _, row := range r.Rows {
		mark := "  ok  "
		switch row.Report.State {
		case refusalstreak.StateStreaking:
			mark = " STOP "
		case refusalstreak.StateUnwitnessed:
			mark = " ???? "
		case refusalstreak.StateUnavailable:
			mark = " ---- "
		}
		fmt.Fprintf(&b, "[%s] %-18s %s\n", mark, row.Agent, row.Report.State)
		switch {
		case row.Report.Alarming():
			fmt.Fprintf(&b, "                          %s\n", row.Report.Brief())
			if row.Report.Detail != "" {
				fmt.Fprintf(&b, "                          %s\n", row.Report.Detail)
			}
			fmt.Fprintf(&b, "                          %s\n", row.Report.Reason.Human())
		case row.Report.State == refusalstreak.StateUnwitnessed:
			fmt.Fprintf(&b, "                          %d failing turn(s) at the tail, below the alarm floor of %d — "+
				"NOT a claim of health\n", row.Report.Streak, row.Report.MinStreak)
		case row.Report.State == refusalstreak.StateUnavailable:
			fmt.Fprintf(&b, "                          %s\n", row.Report.Unavailable)
		}
	}
	for _, msg := range r.Blind {
		fmt.Fprintf(&b, "[ ---- ] %s\n", msg)
	}
	if n := len(r.streaking()); n > 0 {
		fmt.Fprintf(&b, "\n%d agent(s) have stopped completing turns. Nothing restarts this.\n", n)
	} else if !r.InstrumentFailure() {
		fmt.Fprintf(&b, "\nNo runs at or above %d consecutive failing turns.\n", refusalstreak.DefaultMinStreak)
	}
	return b.String()
}

// newCheckRefusalsCmd builds `pogo check-refusals` (mg-6f3d).
func newCheckRefusalsCmd(jsonOutput *bool) *cobra.Command {
	var (
		workdirs []string
		asProbe  bool
		minRun   int
	)
	cmd := &cobra.Command{
		Use:   "check-refusals",
		Short: "Report agents whose transcripts show N consecutive failing assistant turns (never acts)",
		Long: `Read each running agent's harness session transcript and report the TRAILING RUN
of consecutive failing assistant turns — turns the harness answered locally,
spending no tokens, flagged as an API error.

WHY A RUN AND NOT A RATE. An agent that cannot complete a turn is
indistinguishable from an idle one by every other instrument on this box:
` + "`pogo agent list`" + ` reports it running with correct uptime, the process does not
die so restart_on_crash never fires, and the scheduler keeps logging successful
deliveries. ` + "`pogo agent diagnose`" + ` counts failing turns in a trailing 30-minute
window, which is bounded by how busy the agent is — the longest run in this
fleet's history, 661 turns over 2026-08-14..08-19, reads out of a 30m window as
"2 errors in 30m". A run has no such ceiling.

THE FOUR STATES, AND ONLY ONE OF THEM IS HEALTH:

  ok      the most recent turn is ESTABLISHED work — a tool call, or tokens
          spent on a reply that says nothing about failing.
  STOP    ` + fmt.Sprint(refusalstreak.DefaultMinStreak) + ` or more consecutive failing turns with no work between them.
  ????    turns were read, the tail is not established work, and the run has
          not reached the floor. NOT a claim of health.
  ----    nothing could be judged: no declared transcript path, no readable
          file, or a file with no assistant turns in it. NOT a claim of health.

The ???? and ---- states exist because mg-6616's classifier defaulted everything
it did not recognise to "work" and scored 386 failures as healthy turns, turning
one entirely dead day into "41 work". There is no default bucket here.

--probe answers a different question, and it is the one to ask when this census
comes back clean: would the alarm REACH ANYBODY? It builds a throwaway macguffin
store with no agents in it at all — the fleet-down condition — delivers the alarm
by the same code path pogod uses, and confirms the bytes are in the maildir the
out-of-process notifier polls. Its matched control sends to a mailbox nobody
registered and demands a refusal, so the probe cannot be green because it is
unable to see. A notification mechanism verified against a healthy fleet is
verified in the one condition where it is not needed.

REPORTS ONLY. It never restarts, nudges, or mails: no member of this class is
fixable by restarting, and every nudge path runs through an agent, which is the
population that has stopped.

Exit status: 0 no runs at or above the floor, 1 at least one, ` + fmt.Sprint(exitInstrumentFailure) + ` this run
measured nothing.`,
		Run: func(cmd *cobra.Command, args []string) {
			if asProbe {
				runRefusalProbe(*jsonOutput)
				return
			}
			rep := scanRefusals(workdirs, minRun)
			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(rep.Render())
			}
			if rep.InstrumentFailure() {
				os.Exit(exitInstrumentFailure)
			}
			if len(rep.streaking()) > 0 {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().StringArrayVar(&workdirs, "workdir", nil,
		"Scan this working directory's transcripts (repeatable; default: every running agent)")
	cmd.Flags().IntVar(&minRun, "min-run", 0,
		fmt.Sprintf("Consecutive failing turns that count as stopped (default %d)", refusalstreak.DefaultMinStreak))
	cmd.Flags().BoolVar(&asProbe, "probe", false,
		"Run the fleet-down delivery probe instead of the census: would the alarm reach anybody?")
	return cmd
}

// scanRefusals builds the census.
//
// With no --workdir it asks pogod for the running agents and resolves each one's
// working directory from the fleet layout. A name that resolves to no directory
// is reported as a BLIND line rather than skipped: an agent this command cannot
// look at is not an agent it has cleared.
func scanRefusals(workdirs []string, minRun int) refusalReport {
	rep := refusalReport{}
	home, err := os.UserHomeDir()
	if err != nil {
		rep.Blind = append(rep.Blind, fmt.Sprintf("no home directory to resolve transcript paths against: %v", err))
		return rep
	}
	opts := refusalstreak.Options{MinStreak: minRun}

	if len(workdirs) > 0 {
		for _, wd := range workdirs {
			rep.Rows = append(rep.Rows, refusalRow{
				Agent:   filepath.Base(wd),
				Workdir: wd,
				Report:  refusalstreak.Scan(home, providers.SessionTranscriptGlobs(wd), opts),
			})
		}
		return rep
	}

	agents, err := client.ListAgents()
	if err != nil {
		rep.Blind = append(rep.Blind, fmt.Sprintf(
			"could not ask pogod for the agent roster (%v) — pass --workdir to scan a directory directly", err))
		return rep
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	for _, a := range agents {
		if a.Status != agent.StatusRunning {
			continue
		}
		wd := resolveAgentWorkdir(a.Name)
		if wd == "" {
			rep.Blind = append(rep.Blind, fmt.Sprintf(
				"%s: no working directory found under %s; its transcripts were NOT read", a.Name, config.PogoHome()))
			continue
		}
		rep.Rows = append(rep.Rows, refusalRow{
			Agent:   a.Name,
			Workdir: wd,
			Report:  refusalstreak.Scan(home, providers.SessionTranscriptGlobs(wd), opts),
		})
	}
	return rep
}

// resolveAgentWorkdir maps an agent name to the directory it runs in, trying the
// two roots pogod spawns into. It returns "" when neither exists, which the
// caller reports as blind — a reconstruction that has rotted must produce "I
// could not look", never "nothing found".
func resolveAgentWorkdir(name string) string {
	home := config.PogoHome()
	for _, root := range []string{"agents", "polecats"} {
		p := filepath.Join(home, root, name)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return ""
}

// runRefusalProbe runs the fleet-down delivery probe and exits on its verdict.
//
// A probe that could not be BUILT exits as an instrument failure, never as a
// pass. "The channel is fine" and "I could not test the channel" demand opposite
// responses, and only one of them is the state this whole item exists to end.
func runRefusalProbe(jsonOutput bool) {
	res := refusalwatch.Probe()
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
