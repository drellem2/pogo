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
		root           string
		window         time.Duration
		floor          float64
		candidateFloor float64
		all            bool
		asProbe        bool
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

An ORPHAN is a process whose cwd names a polecat that the registry does not have
running, and whose owner's processes TOGETHER clear the rate floor. Anything else
is left alone.

THE FLOOR IS PER OWNER, NOT PER PROCESS, and that is not a refinement (mg-c675).
A polecat orphaned 52 busy-loops holding 8.7 of this host's 10 cores for 41
minutes; fed that population, the per-process form of this detector reported a
clean host, having examined none of them. 8.7 cores shared by 52 contending
processes is 0.167 each, under a 0.20 floor calibrated on orphans that came one
at a time. Processes contending for a fixed number of cores get capacity/N, so a
per-process floor GOES BLINDER AS THE LEAK GETS WORSE — the leak big enough to
saturate the host is the one it cannot see. Summing per owner removes that,
because subdividing the same compute changes no term of the comparison.

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
routes elsewhere. This reports detached COMPUTE. Keeping those apart is now
--candidate-floor's job (default ` + fmt.Sprintf("%.2f", orphanwatch.DefaultCandidateFloor) + ` cores): it decides what gets ATTRIBUTED,
never what gets REPORTED. Setting it at or above --floor is REFUSED rather than
accepted, because it reinstates the per-process rule above.

WHAT IT CANNOT SEE, stated here rather than in a footnote:

  UNATTRIBUTABLE  a busy process whose cwd carries no polecat marker — a worker
                  that chdir'd out of its tree looks exactly like every
                  unrelated program on the machine. Counted, never convicted.
  CWD UNREADABLE  a busy process whose working directory the kernel would not
                  disclose. An instrument limit, counted separately.
  UNDER 0.02      a dead owner's swarm escapes if its total clears --floor while
                  EVERY member sits under --candidate-floor, which takes more
                  than ten of them. Spinning processes only get that small when
                  there are 500+ on a 10-core box; duty-cycled work gets there
                  at any count, and is the stuck-process class by another name.

Both blind spots fail CLOSED. Unattributable is not orphan.

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
				PolecatsRoot:   root,
				Window:         window,
				Floor:          floor,
				CandidateFloor: candidateFloor,
				LiveOwners:     liveOwnersFromRegistry,
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
	cmd.Flags().Float64Var(&floor, "floor", orphanwatch.DefaultFloor,
		"Rate in cores at or above which a dead owner's processes, SUMMED, are reported")
	cmd.Flags().Float64Var(&candidateFloor, "candidate-floor", orphanwatch.DefaultCandidateFloor,
		"Per-process rate in cores below which a process is not attributed at all (must be under --floor)")
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
	fmt.Fprintf(&b, "  window %s, floor %.2f cores per OWNER, candidate floor %.2f per process\n",
		rep.Window, rep.Floor, rep.CandidateFloor)
	fmt.Fprintf(&b, "  %d processes sampled, %d above the candidate floor\n", rep.Sampled, rep.Busy)
	fmt.Fprintf(&b, "  %d spared (owner still running), %d spared (owner under the floor), "+
		"%d unattributable, %d cwd unreadable\n",
		rep.LiveOwner, rep.BelowOwnerFloor, rep.Unattributable, rep.CwdUnreadable)

	if len(rep.Orphans) == 0 {
		b.WriteString("\nNo orphaned compute.\n")
		if rep.Busy == 0 {
			b.WriteString("Note: nothing on this host was above the candidate floor, so nothing was\n" +
				"attributed. That is a clean host OR a candidate floor set too high — the counts\n" +
				"above tell you which.\n")
		}
		if rep.BelowOwnerFloor > 0 {
			fmt.Fprintf(&b, "Note: %d process(es) belong to polecats that are GONE and were spared only\n"+
				"because their owner's total is under %.2f cores. Nothing here is worth acting on,\n"+
				"but this is the count that would grow first if the floor were set too high.\n",
				rep.BelowOwnerFloor, rep.Floor)
		}
		if all {
			b.WriteString("\nThe spared count is the positive control: those processes carry the same\n" +
				"ppid=1, high-CPU signature as an orphan and were left alone because their\n" +
				"owner is alive.\n")
		}
		return b.String()
	}

	// The owner summary comes first and is never elided. The finding is "this
	// polecat is gone and still holding N cores"; reading that off 52 nearly
	// identical process lines is how 87% of a host went unnoticed for 41
	// minutes (mg-c675).
	fmt.Fprintf(&b, "\n%d DEAD POLECAT(S) STILL HOLDING COMPUTE, %.2f cores total:\n\n",
		len(rep.Owners), rep.TotalCores())
	for _, load := range rep.Owners {
		fmt.Fprintf(&b, "  %-24s %5.2f cores across %d process(es), %s of CPU burnt\n",
			load.Owner+" (not running)", load.Cores, load.Procs, load.CPU.Round(time.Second))
	}

	fmt.Fprintf(&b, "\n%d ORPHANED PROCESS(ES):\n\n", len(rep.Orphans))
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
// The os.Exit calls live HERE and the temp directory lives in the callee, so
// the callee's defer is reached on every verdict. Deferred functions do not run
// on os.Exit, and until mg-60eb this function exited past its own
// `defer os.RemoveAll` on all four of its failure arms — leaking the probe's
// store exactly when the probe failed, which is the only time anyone runs it
// twice. That is this ticket's defect in production code rather than in a test
// helper: a cleanup that runs only when nothing went wrong.
func runOrphanProbe(jsonOutput bool) {
	if code := orphanProbeVerdict(jsonOutput); code != 0 {
		os.Exit(code)
	}
}

// orphanProbeVerdict conducts the probe and returns the exit code its verdict
// calls for, 0 meaning pass. It never calls os.Exit, which is the whole point.
func orphanProbeVerdict(jsonOutput bool) int {
	dir, err := os.MkdirTemp("", "orphanprobe")
	if err != nil {
		fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — probe could not be built: %v\n", err)
		return exitInstrumentFailure
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
			// The rate the host GRANTED each burner, whatever bucket it landed
			// in. orphan_cores above rides on a finding, so it is 0 on exactly
			// the runs where a reader most needs the magnitude — a probe that
			// did not report reads as 0.00 cores for a process that was
			// provably spinning (mg-5aac).
			"orphan_rate_cores":  res.OrphanRate,
			"control_rate_cores": res.ControlRate,
			"reported":           res.Reported,
			"spared":             res.Spared,
			"blind":              res.Blind,
			"attempts":           res.Attempts,
			"passed":             res.Passed(),
			"report":             res.Report,
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
		return exitInstrumentFailure
	}
	if res.Blind != "" {
		// The probe was BUILT and its arms were not conducted. That is the same
		// class as a probe that would not build — it proved nothing — and must
		// not be reported as a detector failure, which is how a load-driven
		// lsof refusal got classed as a branch DEFECT on 2026-08-10 (mg-db12).
		fmt.Fprintf(os.Stderr, "INSTRUMENT FAILURE — the probe was built but this host would not let it be\n"+
			"observed, after %d attempt(s): %s\n", res.Attempts, res.Blind)
		return exitInstrumentFailure
	}
	if !res.Passed() {
		return cli.ExitError
	}
	return 0
}
