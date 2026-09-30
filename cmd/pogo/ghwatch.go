package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/ghtoken"
	"github.com/drellem2/pogo/internal/ghwatch"
)

// newGHWatchCmd builds `pogo gh-watch` (mg-257a8): the three gh-issue watchers
// — intake, teardown and the carrier re-read — run as one standalone,
// scheduled command instead of inside pogod. See internal/ghwatch for why.
//
// It is NOT a member of the check-* family. Those are read-only and mail
// nobody; this is the standing runner itself — it mails findings under the
// same renotify/escalation policy pogod applied, and persists that policy's
// state between runs. The check-* commands remain the on-demand reproductions.
func newGHWatchCmd(jsonOutput *bool) *cobra.Command {
	var force, oneline, noMail bool
	cmd := &cobra.Command{
		Use:   "gh-watch",
		Short: "Run the gh-issue intake, teardown and carrier re-read watchers once (scheduled by com.pogo.ghwatch)",
		Long: `Run the three gh-issue watchers once, and record what they saw.

  intake     every OPEN issue on a watched repo has a carrier work item   (check-intake)
  teardown   every DONE carrier's issue was actually closed               (check-teardown)
  re-read    every LIVE carrier's issue has not moved on without it       (check-carriers)

Each watcher keeps its own interval ([gh_intake] / [gh_teardown] /
[carrier_drift] interval); a run in which a watcher's interval has not elapsed
leaves it alone. --force samples every watcher now. It clears only the interval:
an unchanged finding set is still not re-mailed before renotify_after, and
escalation clocks are kept.

WHY THIS IS NOT IN pogod. Every one of these calls ` + "`gh`" + `, which needs a GitHub
credential. launchd starts pogod without a shell, so pogod had to fetch one at
startup and hold it for its whole life (and could not see a rotation: mg-4d59).
This command is started by launchd through a LOGIN shell
(com.pogo.ghwatch, ` + "`pogo service install-gh-watch`" + `), so it uses the credential
your shell has, read afresh on every fire.

Findings are MAILED exactly as pogod mailed them (REPORT-ONLY: nothing is
filed, closed or commented on). The run's record — arming, whether each watcher
sampled, the last sample's event details and every issue state it read — is
written to $POGO_HOME/gh-watch/state.json, which pogod reads to annunciate a
watcher that did not arm and a job that has stopped running.

Exit status: 0 when the run completed and its record was written (findings are
mail, not an exit status); 1 when the record could not be written; 4 when
another run holds the lock.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// A no-op in a login shell, where GH_TOKEN is already in the
			// environment. It is kept for a hand run from somewhere that is not,
			// and its Result is what intake's credential predicate reads.
			cred := ghtoken.Ensure()
			cfg := config.Load()
			home := config.PogoHome()

			unlock, err := ghwatch.Lock(home)
			if err != nil {
				if errors.Is(err, ghwatch.ErrLocked) {
					fmt.Fprintln(os.Stderr, "gh-watch: another run holds the lock; not running")
					os.Exit(4)
				}
				cli.ExitWithError(*jsonOutput, "gh-watch: taking the run lock: "+err.Error(), cli.ExitError)
			}
			defer unlock()

			prev, err := ghwatch.Read(home)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				// Unreadable state costs one run's memory (it may re-mail an
				// unchanged set once), never a missed finding. Say so and go on.
				fmt.Fprintf(os.Stderr, "gh-watch: previous state unreadable, starting fresh: %v\n", err)
				prev = ghwatch.File{}
			}
			caches := ghwatch.ReadCaches(home)
			deps, repos, repoSrc := ghwatch.Production(cfg, home, caches, cred)
			if noMail {
				deps.Mail = func(to, from, subject, body string) error {
					fmt.Fprintf(os.Stderr, "gh-watch: --no-mail: would mail %s: %s\n", to, subject)
					return nil
				}
			}

			now := time.Now()
			rec := ghwatch.Run(cfg, prev, deps, now, force)

			writeErr := ghwatch.Write(home, rec)
			if err := ghwatch.WriteCaches(home, caches); err != nil {
				fmt.Fprintf(os.Stderr, "gh-watch: could not persist the mg-scan caches (next run forks more): %v\n", err)
			}

			switch {
			case *jsonOutput:
				cli.PrintJSON(rec)
			case oneline:
				fmt.Println(onelineGHWatch(rec))
			default:
				fmt.Print(renderGHWatch(rec, repos, repoSrc))
			}
			if writeErr != nil {
				fmt.Fprintf(os.Stderr, "gh-watch: could not write %s: %v\n", ghwatch.StatePath(home), writeErr)
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "sample every armed watcher now, ignoring its interval")
	// For a verification run beside a live deployment: every read is real,
	// nothing is sent. The record still counts a withheld notice as mailed, so
	// point POGO_HOME at a scratch directory for such a run.
	cmd.Flags().BoolVar(&noMail, "no-mail", false, "send no mail (print what would be sent); use with a scratch POGO_HOME")
	// The launchd job's log is not rotated, and the job fires every 15 minutes:
	// one line per fire keeps it at ~100 lines a day. The full record is in
	// state.json either way.
	cmd.Flags().BoolVar(&oneline, "oneline", false, "print a one-line summary (what the launchd job logs)")
	return cmd
}

// renderGHWatch is the human summary of one run.
func renderGHWatch(f ghwatch.File, repos []string, repoSrc string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "gh-watch run at %s (pid %d)\n", f.StartedAt.UTC().Format(time.RFC3339), f.PID)
	fmt.Fprintf(&b, "  credential: %s\n", f.Credential)
	fmt.Fprintf(&b, "  intake repos: %s (from %s)\n", strings.Join(repos, ", "), repoSrc)
	for _, d := range f.Detectors() {
		fmt.Fprintf(&b, "\n%s: %s", d.Name, d.Arming)
		if d.ArmingDetail != "" {
			fmt.Fprintf(&b, " — %s", d.ArmingDetail)
		}
		b.WriteString("\n")
		if d.Arming != ghwatch.Armed {
			continue
		}
		if !d.SampledThisRun {
			fmt.Fprintf(&b, "  not due (last sampled %s); --force to sample now\n",
				ageOf(d.LastSampledAt, f.StartedAt))
			continue
		}
		fmt.Fprintf(&b, "  sampled: %s\n", d.LastEvent)
		keys := make([]string, 0, len(d.LastDetails))
		for k := range d.LastDetails {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "    %s=%v\n", k, d.LastDetails[k])
		}
		if len(d.Lookups) > 0 {
			byState := map[string]int{}
			for _, l := range d.Lookups {
				byState[l.State]++
			}
			states := make([]string, 0, len(byState))
			for s, n := range byState {
				states = append(states, fmt.Sprintf("%s=%d", s, n))
			}
			sort.Strings(states)
			fmt.Fprintf(&b, "  issues read: %d (%s)\n", len(d.Lookups), strings.Join(states, " "))
		}
	}
	return b.String()
}

func ageOf(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return now.Sub(t).Round(time.Minute).String() + " ago"
}

// onelineGHWatch is the one-line summary the launchd job logs per fire.
func onelineGHWatch(f ghwatch.File) string {
	parts := []string{f.StartedAt.UTC().Format(time.RFC3339), "gh-watch:"}
	for _, d := range f.Detectors() {
		s := d.Name + "=" + string(d.Arming)
		if d.Arming == ghwatch.Armed {
			if d.SampledThisRun {
				s += "/sampled(" + d.LastEvent
				if n := len(d.Lookups); n > 0 {
					s += fmt.Sprintf(", %d issues read", n)
				}
				s += ")"
			} else {
				s += "/not-due"
			}
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}
