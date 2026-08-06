package main

// check-staleness: the two witnesses for "what is running is not what shipped,
// and the mechanism that should have refreshed it did not run" (mg-dd49).
//
// Newest of the check-* family and the only one whose subject is an ABSENCE.
// Same posture as its siblings: it reports and never acts, and exits 1 when it
// has anything to say.
//
// It exists because five consecutive nights of a missed redeploy produced no
// signal at all. Four nights the job never fired (the box was powered off
// through each 03:00 window and launchd does not replay a calendar interval
// across a power cycle); the fifth it fired and died one second in. pogod went
// on serving 52-commit-old code and every polecat dispatched ran a superseded
// template. Both were found by hand, one by running `ls` on a binary and one by
// a dispatch eating a 409.
//
// WHY NEITHER EXISTING DETECTOR SAW IT — and the reason this is one command
// with two halves rather than an extension of either:
//
//	the deploy's own output   A fire that never happens writes no log line and
//	                          exits nothing. Every detector downstream of the
//	                          runner is blind to the case by construction, which
//	                          is exactly why four nights passed unnoticed under a
//	                          runner that alerts loudly when it fails.
//	`pogo doctor --check`     Its prompt-drift check compares the installed
//	                          corpus against the running binary's EMBEDDED copy.
//	                          A missed redeploy staled both together, so the two
//	                          matched and it passed — truthfully, and about the
//	                          wrong question.
//
// So: half one reads an EXPECTATION (when should the nightly have run?) against
// the attempt record, and half two reads a GIT REF against the installed prompt
// tree. Neither consults the deploy's output and neither consults this binary's
// own build or embed — which is what lets a stale install still raise the alarm
// about itself, the property the ticket asked for when it said to prefer an
// out-of-tree witness.
//
// SCOPE, stated so it is not re-litigated. This is the general witness. It does
// not subsume its two siblings and does not overlap them:
//
//	mg-fc99  the installed plist declares one fire where the code declares
//	         three, so the retry half of mg-8f7e is inert. That is a check of
//	         the plist against deployHours, BEFORE a night needs it. This
//	         witness reads neither plist nor code schedule for that comparison;
//	         it would see the CONSEQUENCE the next morning, never the cause.
//	mg-0d70  a sync failure is reported with the wrong cause attached. That is
//	         about the content of an alert this witness does not emit; here a
//	         non-zero rc is reported as a non-zero rc, with the log named.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/service"
	"github.com/drellem2/pogo/internal/staleness"
)

// defaultStampPath mirrors pogo-deploy.sh's own resolution order, so the
// witness reads the file the runner writes rather than a second guess at where
// it lives.
func defaultStampPath() string {
	if p := os.Getenv("POGO_DEPLOY_STAMP"); p != "" {
		return p
	}
	return filepath.Join(config.PogoHome(), "deploy-attempt.stamp")
}

// defaultReferenceRepo prefers the dedicated deploy checkout over the working
// tree. It is the tree the nightly actually builds from, it is not a place a
// human is mid-edit, and it exists on exactly the hosts that have a nightly.
// Falling back to the working directory keeps the command usable from a
// checkout, which is where its own tests and a developer run it.
func defaultReferenceRepo() string {
	src := os.Getenv("POGO_DEPLOY_SRC")
	if src == "" {
		src = filepath.Join(config.PogoHome(), "deploy-src")
	}
	if fi, err := os.Stat(filepath.Join(src, ".git")); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
		return src
	}
	cwd, err := os.Getwd()
	if err != nil {
		return src
	}
	return cwd
}

func newCheckStalenessCmd(jsonOutput *bool) *cobra.Command {
	var (
		refRepo    string
		ref        string
		promptsDir string
		stampPath  string
		nowFlag    string
		skipDeploy bool
		skipPrompt bool
	)

	cmd := &cobra.Command{
		Use:   "check-staleness",
		Short: "Report a nightly redeploy that did not happen and a prompt corpus behind the repo (never fixes)",
		Long: `Two witnesses for the same failure: the fleet is running something older than
what shipped, and nothing said so.

  redeploy   Was there a SUCCESSFUL nightly deploy on every night that should
             already have settled? Judged against an expectation — the deploy
             schedule and the clock — not against the deploy's output, because a
             fire that never happens writes no output to read. A night with no
             record is reported as ` + "`no-fire`" + `; a night whose record carries a
             non-zero rc is reported as ` + "`failed`" + `.

  prompts    Does the installed prompt corpus under ~/.pogo/agents match the one
             a git ref ships? Compared file by file on a hash of the body, in
             both directions.

WHY IT IS NOT ALREADY COVERED. On 2026-08-04 the nightly had not succeeded since
07-31 — four nights it never fired at all, and nothing alarmed on any of them —
so pogod was 52 commits and 6 days stale and all nine shipped prompts differed
from main. Both were found by hand.

  A missed run is invisible from inside the runner. There is no log line for a
  fire that did not occur, so the only way to see one is to compare against when
  it should have run.

  ` + "`pogo doctor --check`" + ` compares the installed prompts against THIS BINARY's
  embedded copy. A missed redeploy stales the binary and the prompts together,
  so they still matched and it passed. That check answers "did the installer run
  since this binary was built". This one answers "is what the fleet reads what
  the repo ships", and only a comparison against the repo can.

NEITHER HALF READS THIS BINARY's BUILD OR EMBED, deliberately. A staleness alarm
that a stale install disables has failed at the first failure it exists to
catch. The redeploy half reads a text file and a schedule constant; the prompt
half reads a git ref.

LENGTH IS NEVER THE PREDICATE, and the installed tree is not simply an older
main. On 2026-08-04 ` + "`polecat-build-pr.md`" + ` was 231 lines installed against 230 on
main — LONGER — while the other eight were shorter. The decision is a hash of
the file body with the install stamp stripped; line counts are printed for a
human to orient on and are consulted for nothing.

THE REFERENCE CAN ITSELF BE STALE. ` + "`--repo`" + ` defaults to the deploy checkout at
~/.pogo/deploy-src, whose ` + "`origin/main`" + ` is only as fresh as its last successful
fetch — and a failed fetch is one of the things this command is for. The
resolved commit and its date are printed every run so a reader can judge the
reference before judging the verdict. Nothing here fetches: a detector that
mutates the tree it is judging has made itself a participant.

WHAT IT DOES NOT JUDGE, and says so every run: files under the corpus
directories that the ref does not ship. ~/.pogo/agents holds plenty of
legitimately local material — the crew/pm-*.md stubs, pm/anti-drift-protocol.md, the
per-PM .toml configs — and reporting those as findings would train readers to
skip the report, which is the line a real staleness surfaces on.

SIBLINGS IT DOES NOT REPLACE. mg-fc99 checks the installed plist's fire hours
against the schedule the code declares, which catches an inert retry BEFORE a
night needs it; this command would only see the consequence the next morning.
mg-0d70 is about a sync failure's alert naming the wrong cause; this command
emits no such alert and reports a non-zero rc as a non-zero rc.

CONSTRUCTING THE POSITIVE CONTROL. An alarm that has only ever been silent has
not been shown to work, and silence is what the five missed nights produced. Both
halves take their inputs as flags so both states are constructible by hand,
without waiting a night or moving the system clock:

  # RED — a stamp five nights old, and a corpus compared against a newer ref
  printf '2026-07-31 1 0\n' > /tmp/stale.stamp
  pogo check-staleness --stamp /tmp/stale.stamp --now 2026-08-05T09:00:00Z

  # QUIET — the same witness, a healthy night
  printf '2026-08-05 1 0\n' > /tmp/fresh.stamp
  pogo check-staleness --stamp /tmp/fresh.stamp --now 2026-08-05T09:00:00Z --skip-prompts

Exit status is 0 when both halves are clean, 1 when anything is reported —
including when a half could not run. A check that could not run has not found
its subject healthy.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			now := time.Now()
			if nowFlag != "" {
				parsed, err := time.Parse(time.RFC3339, nowFlag)
				if err != nil {
					cli.ExitWithError(*jsonOutput, fmt.Sprintf("--now must be RFC3339: %v", err), cli.ExitError)
				}
				now = parsed.Local()
			}
			if stampPath == "" {
				stampPath = defaultStampPath()
			}
			if refRepo == "" {
				refRepo = defaultReferenceRepo()
			}
			if promptsDir == "" {
				promptsDir = agent.PromptDir()
			}

			hours, minute := service.DeploySchedule()
			sched := staleness.DeploySchedule{Hours: hours, Minute: minute, Grace: staleness.DefaultGrace}

			var (
				deployRep  staleness.DeployReport
				promptRep  staleness.PromptReport
				deployRan  bool
				promptsRan bool
				findings   int
			)

			if !skipDeploy {
				deployRan = true
				line, err := os.ReadFile(stampPath)
				deployRep = staleness.CheckDeploy(stampPath, string(line), err == nil, now, sched)
				if !deployRep.Clean() {
					findings++
				}
			}
			if !skipPrompt {
				promptsRan = true
				promptRep = staleness.CheckPrompts(cmd.Context(), refRepo, ref, promptsDir)
				if !promptRep.Clean() {
					findings++
				}
			}

			if *jsonOutput {
				out := map[string]any{"findings": findings, "now": now.Format(time.RFC3339)}
				if deployRan {
					out["redeploy"] = deployRep
					out["schedule"] = map[string]any{
						"hours": hours, "minute": minute, "grace_seconds": int(staleness.DefaultGrace.Seconds()),
					}
				}
				if promptsRan {
					out["prompts"] = promptRep
				}
				cli.PrintJSON(out)
			} else {
				if deployRan {
					printDeployWitness(deployRep, sched)
				}
				if promptsRan {
					if deployRan {
						fmt.Println()
					}
					printPromptWitness(promptRep)
				}
			}
			if findings > 0 {
				os.Exit(cli.ExitError)
			}
		},
	}

	cmd.Flags().StringVar(&refRepo, "repo", "", "Git repo to read the shipped corpus from (default: ~/.pogo/deploy-src, else the working directory)")
	cmd.Flags().StringVar(&ref, "ref", "origin/main", "Git ref holding the shipped prompt corpus")
	cmd.Flags().StringVar(&promptsDir, "prompts-dir", "", "Installed prompt corpus to judge (default: ~/.pogo/agents)")
	cmd.Flags().StringVar(&stampPath, "stamp", "", "Deploy attempt record to read (default: $POGO_DEPLOY_STAMP, else ~/.pogo/deploy-attempt.stamp)")
	cmd.Flags().StringVar(&nowFlag, "now", "", "RFC3339 instant to judge against instead of the clock — for constructing the positive control")
	cmd.Flags().BoolVar(&skipDeploy, "skip-redeploy", false, "Do not run the missed-redeploy witness")
	cmd.Flags().BoolVar(&skipPrompt, "skip-prompts", false, "Do not run the prompt-corpus witness")
	return cmd
}

func printDeployWitness(r staleness.DeployReport, sched staleness.DeploySchedule) {
	fmt.Printf("redeploy — expectation: a successful deploy every night, settled by %02d:%02d local + %s grace\n",
		sched.Hours[len(sched.Hours)-1], sched.Minute, staleness.DefaultGrace)
	fmt.Printf("  record:       %s\n", r.StampPath)
	fmt.Printf("  last due:     %s\n", r.LastDueNight)

	switch {
	case !r.StampFound:
		fmt.Printf("  NO RECORD: nothing has ever recorded a deploy attempt at %s.\n", r.StampPath)
		fmt.Println("             Either the nightly has never run on this host or the record was removed.")
		fmt.Println("             Check: launchctl print gui/$UID/com.pogo.deploy")
	case r.ParseErr != "":
		fmt.Printf("  UNREADABLE RECORD: %s\n", r.ParseErr)
		fmt.Println("             A record this witness cannot read is a record it cannot vouch for.")
	case r.MissedTotal == 0:
		fmt.Printf("  ok: last attempt %s (attempt %d, rc %d) — no night since is unaccounted for.\n",
			r.Last.Date, r.Last.Attempts, r.Last.RC)
	default:
		fmt.Printf("  MISSED: %d night(s) due through %s produced no successful redeploy (last record: %s, attempt %d, rc %d).\n",
			r.MissedTotal, r.LastDueNight, r.Last.Date, r.Last.Attempts, r.Last.RC)
		for _, m := range r.Missed {
			switch m.Reason {
			case "failed":
				fmt.Printf("    %s  failed   the deploy ran and exited %d — read the log\n", m.Date, m.RC)
			default:
				fmt.Printf("    %s  no-fire  no attempt was recorded for this night at all\n", m.Date)
			}
		}
		if r.Truncated {
			fmt.Printf("    ... %d more (enumeration clipped; the count above is exact)\n", r.MissedTotal-len(r.Missed))
		}
		// Only the most recent night's outcome is knowable: the record holds a
		// single line. Saying so keeps a reader from taking "no-fire" on an
		// older night as evidence that it did not also fail.
		fmt.Println("  The record holds one line, so only the most recent night's outcome is known;")
		fmt.Println("  earlier nights are judged on the absence of a record, which is the signature")
		fmt.Println("  of a fire that never happened.")
	}
}

func printPromptWitness(r staleness.PromptReport) {
	fmt.Printf("prompts — installed corpus vs a git ref (never this binary's own embed)\n")
	fmt.Printf("  installed:    %s\n", r.InstalledRoot)
	if r.Err != "" {
		fmt.Printf("  reference:    %s @ %s\n", r.Reference.Repo, r.Reference.Ref)
		fmt.Printf("  COULD NOT CHECK: %s\n", r.Err)
		fmt.Println("             This is not an all-clear. Nothing was compared.")
		return
	}
	fmt.Printf("  reference:    %s @ %s = %s (committed %s)\n",
		r.Reference.Repo, r.Reference.Ref, shortSHA(r.Reference.Commit), r.Reference.CommitTime)

	if len(r.Unreadable) > 0 {
		fmt.Printf("  UNREADABLE: %d installed file(s) could not be read, so their content is unknown:\n", len(r.Unreadable))
		for _, u := range r.Unreadable {
			fmt.Printf("    %s\n", u)
		}
	}
	if len(r.Deltas) == 0 {
		fmt.Printf("  ok: all %d shipped prompt(s) match the reference.\n", r.Shipped)
	} else {
		fmt.Printf("  STALE: %d of %d shipped prompt(s) differ from the reference.\n", len(r.Deltas), r.Shipped)
		// Say what decided this, in the output and not only in the source. The
		// line counts on the right are the most legible thing on the screen and
		// a reader will reach for them; the measurement that produced mg-8bcb's
		// withdrawn "installed LONGER than main" was exactly a `wc -l` pair, and
		// it was wrong by one stamp line on the one file that was up to date.
		fmt.Println("  Decided on a sha256 of each file BODY (install stamp stripped), both directions.")
		fmt.Println("  The line counts below are for orientation and decided nothing: the ordinary")
		fmt.Println("  shape of a prompt edit swaps one line for another and no length test can see it.")
		for _, d := range r.Deltas {
			fmt.Printf("    %-34s %-14s %s\n", d.Path, d.Kind, d.LineNote())
		}
		fmt.Println("  Every agent reading these is running a superseded prompt.")
		fmt.Println("  Fix: redeploy, or 'pogo agent prompt install' from a build of the reference.")
	}
	// The census, clean or not — the same posture check-prompts takes with the
	// flag values it cannot decide. A report that printed only its findings
	// would read as though it had judged everything it walked past.
	fmt.Printf("  not judged: %d installed file(s) the reference does not ship (locally added)\n", len(r.Unjudged))
	for _, u := range r.Unjudged {
		fmt.Printf("    %s\n", u)
	}
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
