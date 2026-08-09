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
		logPath    string
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

			// An explicit --deploy-log is the caller ASSERTING which record to
			// judge, so it arms the did-not-run witness on its own. Without it
			// the witness is armed only where the nightly LaunchAgent is
			// installed, matching pogod's own gate (internal/driftwatch): on a
			// host with no nightly there is nothing to be silent, and reporting
			// "no deploy log" there would make every dev box and every sandbox
			// fail a check that has nothing to check.
			logExplicit := logPath != ""
			if logPath == "" {
				logPath = service.DeployLogPath()
			}
			deployInstalled, deployPlist := service.DeployStatus()
			noFireArmed := logExplicit || deployInstalled

			var (
				deployRep  staleness.DeployReport
				noFireRep  staleness.NoFireReport
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

				// The DID-NOT-RUN half (mg-2416), read from the log rather than
				// the one-line stamp. The stamp can only speak for the most
				// recent night; the log holds every night it covers, which is
				// what it takes to name a RUN of silent ones — and the four
				// nights of 2026-08-01..08-04 are exactly that shape.
				if noFireArmed {
					text, err := os.ReadFile(logPath)
					noFireRep = staleness.CheckNoFire(logPath, string(text), err == nil, now, sched)
					if !noFireRep.Clean() {
						findings++
					}
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
					out["nofire_armed"] = noFireArmed
					if noFireArmed {
						out["nofire"] = noFireRep
					}
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
					fmt.Println()
					if noFireArmed {
						printNoFireWitness(noFireRep)
					} else {
						printNoFireDisarmed(deployPlist)
					}
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
	cmd.Flags().StringVar(&logPath, "deploy-log", "", "Deploy log to read the did-not-run witness from (default: ~/Library/Logs/pogo/pogo-deploy.log)")
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

// printNoFireDisarmed says, out loud, that the did-not-run witness did not run.
//
// It is printed rather than skipped for the same reason pogod logs its own
// disarm: this witness's finding is an ABSENCE, so a witness that produced no
// output and a host whose deploy fired every night look identical. Declaring the
// silence is what keeps "nothing reported" from being read as "nothing wrong".
func printNoFireDisarmed(plistPath string) {
	fmt.Println("did-not-run — NOT ARMED on this host, so nothing was checked. This is not an all-clear.")
	if plistPath == "" {
		fmt.Println("  The nightly deploy LaunchAgent is not installed (non-macOS host), so there is no")
		fmt.Println("  nightly whose silence could be a finding.")
	} else {
		fmt.Printf("  No nightly deploy LaunchAgent at %s, so there is no\n", plistPath)
		fmt.Println("  nightly whose silence could be a finding.")
	}
	fmt.Println("  To judge a log anyway — another host's, or one you have copied here — pass --deploy-log.")
}

// printNoFireWitness renders the DID-NOT-RUN half (mg-2416).
//
// It is printed beside the stamp witness rather than folded into it because the
// two answer different questions from different records, and the difference is
// the whole reason four nights went unseen. The stamp says how the most recent
// night ENDED; the log says which nights STARTED. Only the second can name a
// run of nights on which nothing happened at all.
func printNoFireWitness(r staleness.NoFireReport) {
	fmt.Printf("did-not-run — expectation: the job STARTS and FINISHES every night; read from the deploy log, never from an exit code\n")
	fmt.Printf("  record:       %s\n", r.LogPath)
	fmt.Printf("  last due:     %s\n", r.LastDueNight)

	// The hang half FIRST when there is one (mg-56ac). A reader who stops after
	// the first finding must not stop on the weaker one, and a run that started
	// and never came back is the one with a fleet possibly stopped under it.
	if r.HungTotal > 0 {
		fmt.Printf("  HUNG: %d run(s) STARTED and did not finish inside %s. A hung night is not a missed one —\n",
			r.HungTotal, staleness.HumanDuration(r.HungAfterSeconds))
		fmt.Println("        it has a start line, which is why it used to be counted as a night that ran.")
		for _, h := range r.Hung {
			if h.Terminated {
				fmt.Printf("    %s  hung   %s -> %s (%s end to end)\n",
					h.Night, h.Start, h.End, staleness.HumanDuration(h.ElapsedSeconds))
			} else {
				fmt.Printf("    %s  hung   %s -> NO terminal line (%s and counting)\n",
					h.Night, h.Start, staleness.HumanDuration(h.ElapsedSeconds))
			}
			if h.SilentSeconds > 0 {
				fmt.Printf("             silent %s after: %s\n", staleness.HumanDuration(h.SilentSeconds), h.StalledAfter)
			}
		}
		if r.HungTruncated {
			fmt.Printf("    ... %d more (enumeration clipped; the count above is exact)\n", r.HungTotal-len(r.Hung))
		}
		fmt.Println("  A hung run holds the deploy lock for its whole length, and if it hung after quiescing")
		fmt.Println("  the fleet then the fleet is still quiesced. Check what is running, not only the deploy:")
		fmt.Println("    curl -s http://127.0.0.1:10000/version && pogo agent list")
	}
	if r.HangUnjudged > 0 {
		fmt.Printf("  NOT JUDGED FOR HANGING: %d run(s) wrote no terminal line, and this log does not establish\n", r.HangUnjudged)
		fmt.Println("             that the runner of their day ever wrote one. Excluded from the count and from the")
		fmt.Println("             all-clear — before the first `pogo-deploy: end` line the absence is about the runner.")
	}

	switch {
	case !r.LogFound:
		fmt.Printf("  NO LOG: nothing has ever been written to %s.\n", r.LogPath)
		fmt.Println("             On a host with the deploy agent installed the job has never once started.")
		fmt.Println("             Check: launchctl print gui/$UID/com.pogo.deploy")
	case r.MissedTotal == 0:
		fmt.Printf("  ok: %d fire(s) recorded", r.Fires)
		if r.DryRuns > 0 {
			fmt.Printf(" (+%d dry run(s), which do not count)", r.DryRuns)
		}
		fmt.Println("; every night this log covers had a start line.")
		if r.HungTotal == 0 && r.HangArmed {
			fmt.Println("      and every run in it wrote a terminal line, so none of them hung.")
		}
	default:
		fmt.Printf("  DID NOT RUN: %d night(s) due through %s have no `pogo-deploy: start` line at all.\n",
			r.MissedTotal, r.LastDueNight)
		for _, night := range r.Missed {
			fmt.Printf("    %s  no-fire  the job never started — there is no exit code to read\n", night)
		}
		if r.Truncated {
			fmt.Printf("    ... %d more (enumeration clipped; the count above is exact)\n", r.MissedTotal-len(r.Missed))
		}
		fmt.Println("  Do NOT read `runs` from launchctl as a cross-check: it counts spawns on the CURRENT")
		fmt.Println("  bootstrap, so re-installing the plist resets it to 0 — measured 0 on this box after")
		fmt.Println("  seven fires. The log is the only record that dates a night's fire.")
	}

	if r.Horizon != "" {
		fmt.Printf("  log covers:   from %s", r.Horizon)
		if r.EarliestJudged != "" {
			fmt.Printf(" (oldest night judged: %s)", r.EarliestJudged)
		}
		fmt.Println()
	}
	if r.HorizonLimited {
		fmt.Println("  Nights before that are NOT judged either way — this log does not cover them, and a")
		fmt.Println("  rotated-away night must not be reported as a silent one.")
	}
	if r.Unparsed > 0 {
		fmt.Printf("  UNREADABLE: %d start line(s) carried a timestamp this witness could not parse, so they\n", r.Unparsed)
		fmt.Println("             could not be credited to any night.")
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
