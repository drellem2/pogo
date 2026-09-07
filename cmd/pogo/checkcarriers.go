package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/carrierdrift"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/ghtoken"
)

// newCheckCarriersCmd builds `pogo check-carriers` (mg-5d9d): the on-demand half
// of pogod's carrier RE-READ. Same detector, same inputs, same rendered text as
// the mail the standing runner sends — so a coordinator who receives a notice can
// reproduce it exactly rather than take its word.
//
// A sibling of check-acks, check-commit-body, check-intake, check-mailloops,
// check-prompts, check-review-decl, check-staleness, check-strandedmail,
// check-teardown and check-verdicts. The family's membership criterion is a
// READ-ONLY DETECTOR THAT REPORTS A CONDITION AND TAKES NO ACTION, and this one
// is at the strict end of it: internal/carrierdrift has no seam through which an
// issue could be commented on or a work item edited.
func newCheckCarriersCmd(jsonOutput *bool) *cobra.Command {
	var (
		ackWindow   time.Duration
		stageWindow time.Duration
		closedGrace time.Duration
		stages      []string
		shelved     bool
	)
	cmd := &cobra.Command{
		Use:   "check-carriers",
		Short: "Re-read every LIVE gh-issue carrier's issue and report the ones whose record has gone stale",
		Long: `Re-read the CURRENT state of the GitHub issue behind every LIVE gh-issue carrier,
and report the carriers whose record has drifted away from it.

WHY THIS EXISTS. A carrier records that an issue was noticed ONCE, and nothing
re-reads the issue afterwards — so the carrier's existence is evidence about the
PAST that reads as evidence about the PRESENT. Three instances surfaced on
2026-09-07, by three unrelated accidents and no instrument:

  not ACKNOWLEDGED  drellem2/pogo#159 and #160 were carried, and days later
                    nothing had appeared on either thread. From the reporter's
                    side, carried-but-silent is INDISTINGUISHABLE from uncarried.
  not TRIAGED       drellem2/pogo#156 was carried and still at ` + "`stage: triage`" + ` 17 days later,
                    while dominating a newer issue nobody had connected to it.
  not still OPEN    drellem2/pogo#127's carrier was live against an issue closed the
                    SAME DAY, and sat as dispatchable work for a MONTH. Dispatching
                    it would have sent a triage worker at a solved problem and
                    posted an acknowledgement comment on a closed thread.

` + "`pogo check-intake`" + ` read "44 carried, 0 uncarried" throughout, and it was right:
it measures whether an issue has a CARRIER, which is our bookkeeping, and once one
exists the issue leaves its population forever. The gap is the AXIS, not the accuracy
— nothing re-read the issue.

FINDINGS:

  issue closed    a LIVE carrier whose issue is closed. Dispatchable work against
                  a solved problem.
  not acknowledged  an open issue past --ack-window since the REPORTER filed, with
                  no acknowledgement on the thread. Measured from the issue, not
                  the carrier: the reporter cannot see our carrier.
  stage stuck     a carrier still at the stage it was FILED at, past --stage-window.
                  The covered stages are exactly the filing stages, because mg
                  records no stage-change timestamp and for those stages ONLY the
                  carrier's own age is the stage's age. Later stages are a stated
                  coverage gap, not a silent one.
  not re-read     the INSTRUMENT failed and this carrier carries no verdict at all.
                  Reported in a different shape from a determination: a failure to
                  measure must never be read as a measurement (mg-dd22).
  unresolvable    GitHub answered about this ref and the answer is unusable — a
                  malformed ` + "`gh:`" + ` line, a deleted issue, a renamed repo.
  declared        a finding a human has explicitly declared deliberate. Listed and
                  never alarmed on; suppressed-forever-and-forgotten is the same
                  absence this check exists to catch.

WHAT COUNTS AS AN ACKNOWLEDGEMENT is the TEXT, not the author, and that was
measured rather than chosen. Of the 40 issues live carriers pointed at on
2026-09-07, 40 were filed by the repo owner and 39 had zero comments from any
other login — the fleet's acknowledgements are posted by agents under the owner's
credential, so author identity does not discriminate at all and would have
reported 38 of 40 carriers as unacknowledged. The shipped triage prompt hands the
worker the exact line to post, so the text is decidable.

DECLARING A FINDING DELIBERATE. Add the matching line to the carrier body:

  gh-closed: <why this carrier is live against a closed issue>
  gh-ack:    <where the reporter was answered, if not on the thread>
  gh-parked: <why this carrier is held at this stage>

REPORTS ONLY. It never comments on an issue, never closes one, and never edits a
work item. In particular the cheapest imaginable fix for the acknowledgement case
— post the ack automatically when a carrier is filed — is deliberately NOT here:
whether this fleet posts automated comments on other people's issues is a
decision for a human, not a detail of a detector.

Exit status is 0 when nothing is actionable, 1 when anything is found, and ` + fmt.Sprint(exitInstrumentFailure) + `
when NO carrier could be re-read at all — a broken instrument, not a result.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Every re-read is a `gh` call, so an unauthenticated gh turns the
			// whole report into "not re-read". In an authed shell this is a no-op;
			// run from launchd, cron, or any other minimal environment it is the
			// difference between an answer and a blind one (mg-03ea). Warned, not
			// fatal — a person who typed the command is owed the report, including
			// the part that says why it is empty.
			if res := ghtoken.Ensure(); !res.OK() {
				fmt.Fprintf(os.Stderr, "warning: %s\n", res)
			}

			src := carrierdrift.MGSource{IncludeShelved: shelved}
			carriers, items, err := src.Carriers()
			if err != nil {
				// A store we could not read is not "no carriers" — that would
				// render as a clean re-read of nothing. Fail loudly instead.
				fmt.Fprintf(os.Stderr, "cannot read work-item store: %v\n", err)
				os.Exit(cli.ExitError)
			}

			windows := carrierdrift.Windows{
				Ack: ackWindow, Stage: stageWindow, Closed: closedGrace,
			}
			if cmd.Flags().Changed("stage") {
				windows.Stages = stages
			}

			// RetryingSnapshot, not the bare GHSnapshot: this box's network is
			// ~50% intermittent (mg-0ffc), and an un-retried re-read turns one
			// blip into a full batch of non-answers (mg-dd22). The CLI and pogod's
			// watcher ride out a blip identically, so a hand re-run cannot
			// disagree with the unattended one for want of a retry.
			snap := carrierdrift.Prefetch(carriers,
				carrierdrift.RetryingSnapshot(carrierdrift.GHSnapshot), 0)
			rep := carrierdrift.Detect(carriers, snap, time.Now(), windows)
			rep.StoreItems = items
			rep.Statuses = src.Statuses()

			if *jsonOutput {
				type outFinding struct {
					Carrier      string `json:"carrier"`
					Issue        string `json:"issue"`
					Title        string `json:"title,omitempty"`
					Stage        string `json:"stage,omitempty"`
					Status       string `json:"status,omitempty"`
					State        string `json:"state,omitempty"`
					AgeSec       int    `json:"age_seconds"`
					Comments     int    `json:"comments"`
					Acknowledged bool   `json:"acknowledged"`
					Detail       string `json:"detail,omitempty"`
					// Class names WHY there is no state, so a consumer can
					// separate a network blip from an auth gap from a deleted
					// issue without re-parsing gh's prose (mg-dd22).
					Class string `json:"class,omitempty"`
					// Suppressed is what a declared finding would have been.
					Suppressed string `json:"suppressed,omitempty"`
				}
				conv := func(fs []carrierdrift.Finding) []outFinding {
					out := make([]outFinding, 0, len(fs))
					for _, f := range fs {
						out = append(out, outFinding{
							Carrier: f.Carrier.ID, Issue: f.Carrier.Ref(),
							Title: f.Carrier.Title, Stage: f.Carrier.Stage,
							Status: f.Carrier.Status, State: string(f.Snapshot.State),
							AgeSec: int(f.Age.Seconds()), Comments: f.Snapshot.Comments,
							Acknowledged: f.Snapshot.Acknowledged, Detail: f.Detail,
							Class: string(f.Class), Suppressed: string(f.Suppressed),
						})
					}
					return out
				}
				cli.PrintJSON(map[string]interface{}{
					"scanned":              rep.Scanned,
					"current":              rep.Current,
					"store_items":          rep.StoreItems,
					"statuses":             rep.Statuses,
					"issue_closed":         conv(rep.Closed),
					"unacknowledged":       conv(rep.Unacknowledged),
					"stuck_stage":          conv(rep.StuckStage),
					"not_checked":          conv(rep.Blocked),
					"indeterminate":        conv(rep.Indeterminate),
					"declared":             conv(rep.Declared),
					"ack_window_seconds":   int(rep.Windows.Ack.Seconds()),
					"stage_window_seconds": int(rep.Windows.Stage.Seconds()),
					"closed_grace_seconds": int(rep.Windows.Closed.Seconds()),
					"stuck_stages":         rep.Windows.Stages,
					"actionable":           rep.Actionable(),
					// A machine consumer must be able to tell a result from a run
					// that measured nothing WITHOUT counting findings.
					"instrument_failure": rep.InstrumentFailure(),
					"failure_classes":    rep.FailureClasses(),
				})
			} else {
				fmt.Print(rep.Render())
				if len(carriers) == 0 {
					// No live carriers and a store that could not be searched both
					// render as zero findings. Say which this was, or a check that
					// examined nothing reads as a check that found nothing.
					fmt.Printf("NO live gh-issue carriers found in %d work item(s) [%s] — nothing was re-read.\n",
						items, joinStatuses(src.Statuses()))
				}
			}

			// A blind run gets its own exit code. Collapsing it into the ordinary
			// "found something" exit would put the two facts a schedule most needs
			// to separate — "the detector has findings" and "the detector cannot
			// see" — behind the same integer.
			if rep.InstrumentFailure() {
				os.Exit(exitInstrumentFailure)
			}
			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().DurationVar(&ackWindow, "ack-window", 0,
		"How long a reporter may wait for an acknowledgement, measured from when THEY filed (0: default; negative: off)")
	cmd.Flags().DurationVar(&stageWindow, "stage-window", 0,
		"How long a carrier may sit at its filing stage (0: default; negative: off)")
	cmd.Flags().DurationVar(&closedGrace, "closed-grace", 0,
		"How long a carrier may stay live after its issue closes (0: default; negative: report immediately)")
	cmd.Flags().StringSliceVar(&stages, "stage", nil,
		"Stages the stuck-stage check covers; default is the filing stages, where carrier age IS stage age")
	cmd.Flags().BoolVar(&shelved, "shelved", false,
		"Also re-read shelved carriers (a shelved carrier cannot mis-dispatch, but can still leave a reporter waiting)")
	return cmd
}

// joinStatuses renders a status list for the empty-population line.
func joinStatuses(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
