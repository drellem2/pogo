package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
)

// newHostLoadCmd builds `pogo host load`.
//
// It exists because nothing on this fleet read the host's resource state
// before deciding to start work. The shipped concurrency rule counts workers,
// and mg-1b8c measured why that is not the same question: one worker that
// self-parallelised into three compute processes held ~5.7 of 10 cores, which
// any count of workers reads as an idle host.
//
// `uptime` is not the answer either, and the command says so in its own
// output. Measured the same night: a load average of 214 against roughly 7.5
// of 10 cores in use, because Darwin counts uninterruptible-sleep tasks in
// that figure. The load average is printed here, last and labelled, because it
// is what makes a human look — and it is not what anything decides on.
func newHostLoadCmd(jsonOutput *bool) *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "load",
		Short: "Report how much of this host's CPU the fleet is holding",
		Long: `Report how much of this host's CPU the pogo fleet is actually consuming.

The number to read is the fleet's cores. It is measured by differencing
per-process CPU time over a short window and attributing it by process subtree
from pogod, so:

  - a single agent running many compute processes counts as all of them, and
  - CPU that is not the fleet's is reported separately and never held against
    dispatch, because pausing fleet work would not give those cores back.

The 1-minute load average is printed as context only. On Darwin it counts
uninterruptible-sleep tasks as well as runnable ones, so on an I/O-bound host
it can read many times the core count while cores sit idle. Do not gate on it.

'would_refuse_dispatch' is answered by the same gate 'pogo agent spawn-polecat'
consults, so this command and the daemon cannot disagree.

--repo additionally reports that REPOSITORY's worker occupancy against the
per-repo dispatch cap. That is a separate refusal from the host one and neither
implies the other: a repo can be full on an idle host, because what saturates is
one repo's test suite run concurrently rather than the fleet's worker count. Ask
for it before planning several dispatches into the same repository.

'worker_budget' is the share of this host the NEXT worker spawned here will be
told it may use. It is a policy division of the core count, not a measurement,
and nothing enforces it — it exists because both dispatch gates count workers,
so one worker whose toolchain self-parallelises can hold the box while a cap of
3 reads one-of-three.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.GetHostLoad(repo)
			if err != nil {
				return err
			}
			if *jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			if !resp.Measured {
				fmt.Println("Host load: NOT MEASURED — the sample could not be taken.")
				fmt.Println("This is missing information, not an idle host.")
				fmt.Printf("\n%s\n", resp.Advice)
				// The budget is a policy division of the core count, not a
				// sample, so it answers when the host could not be measured.
				printWorkerBudget(os.Stdout, resp.WorkerBudget)
				// The repo cap is a count, not a sample: it still answers when
				// the host could not be measured, and suppressing it here would
				// hide the refusal the caller is most likely about to hit.
				//
				// hostWouldRefuse is FALSE here because nothing was measured,
				// not because the host has room — so the "reroute elsewhere"
				// note stays, which is the right call: it is advice about this
				// cap, and an unmeasured host is not evidence against it.
				printRepoOccupancy(os.Stdout, resp.RepoOccupancy, false)
				return nil
			}
			s := resp.Sample
			fmt.Printf("Cores:      %d\n", s.Cores)
			if s.Attributed {
				fmt.Printf("Fleet:      %.2f cores (%.0f%%) across %d processes\n",
					s.FleetCores, s.FleetSaturation()*100, s.FleetProcs)
			} else {
				fmt.Printf("Fleet:      unattributed — no fleet root process found.\n")
				fmt.Printf("            This is missing information, not an idle fleet.\n")
			}
			fmt.Printf("Non-fleet:  %.2f cores (not counted against dispatch)\n", s.ExternalCores)
			fmt.Printf("Free:       %.2f cores\n", s.FreeCores())
			fmt.Printf("Window:     %s\n", s.Window.Round(1e6))
			fmt.Printf("Load avg:   %.2f (1-min) — CONTEXT ONLY, not a decision input\n", s.LoadAvg1)
			fmt.Printf("\n%s\n", resp.Advice)
			if resp.WouldRefuseDispatch {
				fmt.Printf("\n`pogo agent spawn-polecat` would currently be refused with 503 (retryable),\n" +
					"for ANY repo — this gate does not read the --repo argument.\n")
			}
			printWorkerBudget(os.Stdout, resp.WorkerBudget)
			printRepoOccupancy(os.Stdout, resp.RepoOccupancy, resp.WouldRefuseDispatch)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "",
		"also report this repository's worker occupancy against the per-repo dispatch cap")
	return cmd
}

// printWorkerBudget renders the share the next worker will be told it may take.
//
// It is printed on the measured and unmeasured paths both, because it is not a
// measurement: it is a division of the core count by the enforced worker cap,
// and it answers whether one worker can legitimately plan to hold most of the
// box. That question had no answer at all before mg-eb47, which is how one Lean
// build came to hold 9.0 of 10 cores while the per-repo cap read one-of-three.
func printWorkerBudget(w io.Writer, b agent.WorkerBudget) {
	if !b.Known() {
		fmt.Fprintf(w, "\nWorker budget: NOT DERIVED — %s\n", b.Basis)
		return
	}
	fmt.Fprintf(w, "\nWorker budget: %d of %d cores per worker (%s).\n", b.Cores, b.HostCores, b.Basis)
	fmt.Fprintf(w, "               Advisory: it reaches a worker as $%s and prompt prose.\n"+
		"               Nothing enforces it — a toolchain that ignores it takes the box, and\n"+
		"               the host gate above is still what notices.\n", agent.WorkerCoresEnv)
}

// printRepoOccupancy renders the per-repo cap's view, or nothing when --repo
// was not given.
//
// It prints the three states the count can be in rather than only the refusal:
// a disarmed cap, an under-cap repo, and a full one. A command that says nothing
// when it found nothing is indistinguishable from one that was not asked — and
// the caller here is deciding whether to dispatch, so silence would read as
// permission.
//
// hostWouldRefuse is the host gate's current answer, and it is a parameter
// rather than something re-derived here because the two must agree. It gates
// the "dispatch into a different repo" note: that advice was measured wrong on
// 2026-08-13 (mg-eb47), when a per-repo slot freed by a merge was unusable
// because the host gate was refusing every spawn regardless of repo.
func printRepoOccupancy(w io.Writer, occ *agent.RepoOccupancy, hostWouldRefuse bool) {
	if occ == nil {
		return
	}
	fmt.Fprintf(w, "\nRepo:       %s\n", occ.Repo)
	if occ.ConfiguredCap <= 0 {
		fmt.Fprintf(w, "Cap:        DISARMED (max_polecats_per_repo = 0) — this repo refuses nothing.\n")
	} else if occ.RefineryReserved > 0 {
		fmt.Fprintf(w, "Cap:        %d of %d (%d reserved for the refinery, which has work in this repo)\n",
			occ.Cap, occ.ConfiguredCap, occ.RefineryReserved)
	} else {
		fmt.Fprintf(w, "Cap:        %d\n", occ.Cap)
	}
	fmt.Fprintf(w, "Workers:    %d", occ.Count)
	if occ.Count > 0 {
		fmt.Fprintf(w, " — %s", strings.Join(occ.Polecats, ", "))
	}
	fmt.Fprintln(w)
	if !occ.RefineryKnown {
		fmt.Fprintf(w, "Refinery:   NOT ASKED — no slot is being reserved. This is missing information,\n"+
			"            not an idle refinery.\n")
	}
	if occ.WitnessErr != "" {
		fmt.Fprintf(w, "Witness:    UNREADABLE (%s) — polecats that outlived an earlier pogod are\n"+
			"            missing from this count, which may therefore be low.\n", occ.WitnessErr)
	}
	if len(occ.Unattributed) > 0 {
		fmt.Fprintf(w, "Unattributed: %d live worker(s) with no repo recorded, NOT counted here: %s\n",
			len(occ.Unattributed), strings.Join(occ.Unattributed, ", "))
	}
	if occ.WouldRefuse {
		fmt.Fprintf(w, "\n`pogo agent spawn-polecat --repo=%s` would currently be refused with 503\n"+
			"(retryable — a dispatch into a DIFFERENT repo is not refused by THIS cap).\n", occ.Repo)
		if hostWouldRefuse {
			fmt.Fprintf(w, "But rerouting will NOT help right now: the host gate above is ALSO refusing,\n"+
				"and it refuses every spawn regardless of repo. A slot freed here is not capacity —\n"+
				"it is the absence of one particular refusal. Retry on the fleet's core share, not on\n"+
				"a worker exiting.\n")
		}
	}
}

// newHostCmd is the parent for host-level measurements.
func newHostCmd(jsonOutput *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Inspect the host the fleet is running on",
	}
	cmd.AddCommand(newHostLoadCmd(jsonOutput))
	return cmd
}
