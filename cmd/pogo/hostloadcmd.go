package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

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
consults, so this command and the daemon cannot disagree.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.GetHostLoad()
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
				fmt.Printf("\n`pogo agent spawn-polecat` would currently be refused with 503 (retryable).\n")
			}
			return nil
		},
	}
	return cmd
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
