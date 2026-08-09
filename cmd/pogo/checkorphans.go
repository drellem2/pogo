package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/orphanwatch"
)

// exitOrphanUsage is `pogo check-orphans`' exit code for a malformed
// invocation, matching the rest of the check-* family (0 clean, 1 finding, 2
// usage, 3 measured nothing).
const exitOrphanUsage = 2

// newCheckOrphansCmd builds `pogo check-orphans` (mg-4518): a read-only detector
// for compute that outlived the polecat which started it.
//
// It joins the check-* family — check-acks, check-commit-body, check-intake,
// check-mailloops, check-prompts, check-staleness, check-strandedmail,
// check-teardown, check-verdicts — whose membership criterion is A READ-ONLY
// DETECTOR THAT REPORTS A CONDITION AND TAKES NO ACTION. This one reads the host
// process table rather than the macguffin store, which is new for the family and
// changes nothing about the contract: it never signals a process.
func newCheckOrphansCmd(jsonOutput *bool) *cobra.Command {
	var (
		root    string
		window  time.Duration
		floor   float64
		all     bool
		asProbe bool
	)
	cmd := &cobra.Command{
		Use:   "check-orphans",
		Short: "Report compute processes whose owning polecat is gone (never kills anything)",
		Long: `Report every process doing real compute out of a polecat's directory whose owning
polecat is no longer running — work that survived its agent's reap and is now
burning cores nobody is reading the output of.

THE PREDICATE, and the order of the three terms is the whole design:

  cwd        the process's working directory, which names the owning polecat
  owner      that polecat, looked up in pogod's agent registry
  rate       CPU-seconds per wall second, differenced across a sampling window

An ORPHAN is a process above the rate floor whose cwd names a polecat that the
registry does not have running. Anything else is left alone.

IT DOES NOT KEY ON ppid, AND MUST NOT. ` + "`ppid=1`" + ` is not the signature of a leak; it
is the signature of ANY polecat starting background work, because ` + "`nohup ... &`" + `
from a tool-call shell that then exits reparents every worker it launches. On
2026-08-07 four workers belonging to ONE RUNNING polecat all showed ppid=1 at
60-68% CPU. A sweep keyed on that would have destroyed all four mid-computation.
Owner liveness is the discriminator; cwd is what makes the owner knowable after
reparenting has destroyed ppid.

NOR ON ` + "`ps %cpu`" + `. That column is a lifetime average and understated a live
instance of this defect by about 3x — two reads of the same population disagreed
by a factor of three within minutes. The rate here is cumulative CPU time
differenced across a window, which measures work actually performed in it.

THE RATE FLOOR SEPARATES TWO DEFECTS, it is not a severity filter. A
pogo-deploy.sh blocked forever in an unbounded ` + "`git fetch`" + ` ran 31h39m, correctly
parented and reported by nothing, at ~0% CPU. That is a stuck process and it
routes elsewhere. This reports detached COMPUTE.

WHAT IT CANNOT SEE, stated here rather than in a footnote:

  UNATTRIBUTABLE  a busy process whose cwd carries no polecat marker — a worker
                  that chdir'd out of its tree looks exactly like every
                  unrelated program on the machine. Counted, never convicted.
  CWD UNREADABLE  a busy process whose working directory the kernel would not
                  disclose. An instrument limit, counted separately.

Both fail CLOSED. Unattributable is not orphan.

REPORTS ONLY. It never signals a process. The rule above is a strong heuristic
with the blind spots named, and a killer built on a heuristic destroys live work
on its false positives — here, a polecat's parallel search dying mid-computation.
Kill by PID after reading the report, never by pattern: an unanchored ` + "`pkill -f`" + `
has taken this box out before by matching the fleet's own pollers.

A run that COULD NOT LOOK says so instead of exiting clean. An unreachable agent
registry is the one that matters: without it every attributable process has a
dead-looking owner, so a detector that shrugged and carried on would name all of
them. That, an unreadable process table, and a window too short for this host's
CPU-time resolution all exit ` + fmt.Sprint(exitInstrumentFailure) + `.

--probe runs the CONSTRUCTIVE probe instead of the census: it builds a throwaway
polecats tree, starts two real CPU burners in it, detaches them so they genuinely
reparent, and checks that this detector goes RED on the one whose owner is dead
and GREEN on the one whose owner is alive. Use it when you are looking at a clean
census and want to know whether to believe it. The same probe runs in ` + "`go test ./...`" + `,
so the gate exercises the failing arm on every merge.

Exit status: 0 no orphans, 1 at least one, ` + fmt.Sprint(exitOrphanUsage) + ` usage error, ` + fmt.Sprint(exitInstrumentFailure) + ` this run
measured nothing.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				fmt.Fprintf(os.Stderr, "check-orphans takes no positional arguments (got %q)\n", args[0])
				os.Exit(exitOrphanUsage)
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			if asProbe {
				runOrphanProbe(*jsonOutput)
				return
			}
			rep, err := orphanwatch.Scan(orphanwatch.Options{
				PolecatsRoot: root,
				Window:       window,
				Floor:        floor,
				LiveOwners:   liveOwnersFromRegistry,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — this run measured nothing: %v\n", err)
				os.Exit(exitInstrumentFailure)
			}
			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(renderOrphanReport(rep, all))
			}
			if len(rep.Orphans) > 0 {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "Polecats tree (default: $POGO_HOME/polecats, then ~/.pogo/polecats)")
	cmd.Flags().DurationVar(&window, "window", orphanwatch.DefaultWindow, "CPU sampling window")
	cmd.Flags().Float64Var(&floor, "floor", orphanwatch.DefaultFloor, "Rate in cores at or above which a process is a candidate")
	cmd.Flags().BoolVar(&all, "all", false, "Also list the SPARED and unattributable counts in full")
	cmd.Flags().BoolVar(&asProbe, "probe", false,
		"Run the constructive probe instead of the census: can this detector still fire?")

	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintf(os.Stderr, "check-orphans: %v\n\n%s", err, c.UsageString())
		os.Exit(exitOrphanUsage)
		return nil
	})
	return cmd
}

// liveOwnersFromRegistry asks pogod which agents are running.
//
// An error is propagated rather than swallowed, and that is the safety margin —
// see orphanwatch.ErrNoLiveness. `restarting` counts as alive: that agent is
// coming back on the same work item, and its workers are not orphans.
func liveOwnersFromRegistry() (map[string]bool, error) {
	agents, err := client.ListAgents()
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(agents))
	for _, a := range agents {
		if a.Status == agent.StatusRunning || a.Status == agent.StatusRestarting {
			live[a.Name] = true
		}
	}
	return live, nil
}

// renderOrphanReport writes the human form. The counts come first and are
// printed even when there are no findings, because "nothing to report" and
// "nothing looked at" are the two readings this has to keep apart.
func renderOrphanReport(rep orphanwatch.Report, all bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "orphaned compute — polecats root %s\n", rep.PolecatsRoot)
	fmt.Fprintf(&b, "  source %s\n", rep.Source)
	fmt.Fprintf(&b, "  window %s, floor %.2f cores\n", rep.Window, rep.Floor)
	fmt.Fprintf(&b, "  %d processes sampled, %d above the floor\n", rep.Sampled, rep.Busy)
	fmt.Fprintf(&b, "  %d spared (owner still running), %d unattributable, %d cwd unreadable\n",
		rep.LiveOwner, rep.Unattributable, rep.CwdUnreadable)

	if len(rep.Orphans) == 0 {
		b.WriteString("\nNo orphaned compute.\n")
		if rep.Busy == 0 {
			b.WriteString("Note: nothing on this host was above the floor, so nothing was attributed.\n" +
				"That is a clean host OR a floor set too high — the counts above tell you which.\n")
		}
		if all {
			b.WriteString("\nThe spared count is the positive control: those processes carry the same\n" +
				"ppid=1, high-CPU signature as an orphan and were left alone because their\n" +
				"owner is alive.\n")
		}
		return b.String()
	}

	fmt.Fprintf(&b, "\n%d ORPHANED PROCESS(ES), %.2f cores total:\n\n", len(rep.Orphans), rep.TotalCores())
	for _, o := range rep.Orphans {
		fmt.Fprintf(&b, "  pid %-7d %5.2f cores  cpu %-10s owner %s (not running)\n",
			o.PID, o.Cores, o.CPU.Round(time.Second), o.Owner)
		fmt.Fprintf(&b, "            ppid %-6d pgid %-7d %s\n", o.PPID, o.PGID, o.Cwd)
	}
	b.WriteString("\nThis command did not signal any of them. To act, kill BY PID:\n")
	pids := make([]string, 0, len(rep.Orphans))
	for _, o := range rep.Orphans {
		pids = append(pids, fmt.Sprint(o.PID))
	}
	sort.Strings(pids)
	fmt.Fprintf(&b, "  kill %s\n", strings.Join(pids, " "))
	b.WriteString("Never `pkill -f` — an unanchored pattern matches the fleet's own pollers.\n")
	b.WriteString("Re-read the owner's status before killing: the registry answer above is the\n" +
		"whole safety margin, and it was taken seconds ago.\n")
	return b.String()
}

// runOrphanProbe runs the constructive probe and exits on its verdict.
//
// A probe that could not be BUILT exits as an instrument failure, never as a
// pass. A detector reported as healthy on the strength of a fixture that no
// longer constructs is the failure this whole family keeps rediscovering.
func runOrphanProbe(jsonOutput bool) {
	dir, err := os.MkdirTemp("", "orphanprobe")
	if err != nil {
		fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — probe could not be built: %v\n", err)
		os.Exit(exitInstrumentFailure)
	}
	defer os.RemoveAll(dir)

	res := orphanwatch.Probe(dir)
	if jsonOutput {
		cli.PrintJSON(map[string]interface{}{
			"dead_owner":   res.DeadOwner,
			"live_owner":   res.LiveOwner,
			"orphan_pid":   res.OrphanPID,
			"orphan_ppid":  res.DetachedPPID,
			"orphan_cores": res.OrphanCores,
			"control_pid":  res.ControlPID,
			"control_ppid": res.ControlPPID,
			"reported":     res.Reported,
			"spared":       res.Spared,
			"passed":       res.Passed(),
			"report":       res.Report,
		})
	} else {
		fmt.Println(res.Summary())
		if res.Passed() {
			fmt.Println("\nPASS — the detector fires on a constructed orphan and spares an identical-looking\n" +
				"process whose owner is alive. Both arms, on real processes.")
		}
	}
	if res.Err != nil {
		fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — probe could not be conducted: %v\n", res.Err)
		os.Exit(exitInstrumentFailure)
	}
	if !res.Passed() {
		os.Exit(cli.ExitError)
	}
}
