package main

// `pogo check-oneshots` and the "one-shot acks" row in `pogo doctor --check`
// (mg-8011): the CONSUMER for the record mg-64e6 made recordable.
//
// mg-64e6 split the misleading `one_shot_complete` into `one_shot_acked` /
// `one_shot_unacked` / `one_shot_undelivered` / `one_shot_skipped`, and stopped
// there — correctly, because the ticket asked for the record and not for a
// reader. The consequence it named in its own closing note is what this closes:
// the distinction existed and nothing consumed it, so a one-shot firing into a
// dead, wedged or zero-token agent still produced no alarm, no row, no digest
// line. The class matters because one-shots carry the obligations that happen
// ONCE and are never retried — post-redeploy verification, pre-deploy steps —
// and so have no next cycle in which a silent no-op would show up.
//
// WHY IT REPORTS ON THE DOCTOR ROW, and not as mail. Same reasoning as the
// audit-successors row one file over: `pogo doctor --check` is a checklist
// somebody reads on purpose and is the first thing the doctor agent runs, while
// a mail notice lands in a pile already hundreds deep. The standalone command is
// the detail view behind the row — the row says how many and names the newest,
// the command prints every one with what it was carrying.
//
// WHY IT WARNS AND NEVER FAILS. `fail` sets doctor's exit status, which is a
// claim that this HOST is broken. An unanswered one-shot is a missed obligation,
// usually someone else's and usually hours old; putting it in the path of
// anything scripted against doctor's exit code is the "detector grows into a
// gate" failure. The standalone command exits 1 on a finding, because a caller
// who ran THAT asked this exact question.
//
// THE ONE THING IT MUST NOT DO is report a confident zero from a log whose
// writer could not have expressed the failure. The labels this reads ship in
// d71e1e2 and are inert until the daemon is rebuilt onto it, so on any box still
// running an older pogod every one-shot leaves as the retired
// `one_shot_complete` and a naive reader prints "no unanswered one-shots" with
// total confidence. That is the mg-afd0 / mg-3141 confusion class, and the
// legacy-label branch below exists so this instrument answers for its own
// vintage instead of joining it.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/scheduler"
)

// oneShotCheckName is the doctor checklist row this renders on.
const oneShotCheckName = "one-shot acks"

// defaultOneShotWindow is how far back the row and the command look when
// --since is omitted.
//
// A week, for a reason particular to this class: a one-shot is reaped
// AckStaleWindow (24h) after its fire, so a window of a day or less can contain
// a fire whose verdict has not been written yet and none of the verdicts for the
// fires before it. A week spans several nightly redeploys as well, so a run
// straddling one still shows the regime on both sides.
const defaultOneShotWindow = 7 * 24 * time.Hour

// oneShotLimits is the sentence every verdict this renders carries, in one
// constant so no branch can render a verdict without it.
//
// It is a constant for the reason the audit-successors row one file over gives:
// limits written into the WARN branch alone are read by someone who already has
// a finding, while the PASS branch — the one printed on nearly every run — is
// what a person reads when deciding whether to keep looking. "Nothing was
// recorded unanswered" and "every one-shot was answered" are different claims,
// and only the first is measured here.
const oneShotLimits = "Reports what the log RECORDS: a one-shot acked by an agent that then did nothing counts as answered, and a fire still inside its 24h ack window is neither."

// oneShotAckLine renders the doctor row from a report.
//
// Five states, and the first two are why this function exists rather than a
// count comparison at the call site:
//
//	NOT MEASURED   the log could not be read. Says so; never renders as clean.
//	OLD WRITER     the window contains retired `one_shot_complete` records, so
//	               the binary that wrote it predates the labels and CANNOT have
//	               emitted them. A zero here is a property of the writer, not of
//	               the fleet, and saying "no unanswered one-shots" would be a
//	               confident falsehood.
//	NOTHING RAN    no one-shot outcome at all in the window. Reported as its own
//	               state with the population, because "none missed" and "none
//	               happened" are the two readings this row must never merge.
//	CLEAN          one-shots ran and every one was answered, with the count.
//	UNANSWERED     the finding: each one named, with what it was carrying.
func oneShotAckLine(rep scheduler.OneShotReport, err error, now time.Time) (string, string) {
	if err != nil {
		return "warn", fmt.Sprintf("NOT MEASURED — the events log could not be read (%v). "+
			"This row is not a clean bill of health; it is a run that looked at nothing. %s", err, oneShotLimits)
	}

	window := describeOneShotWindow(rep, now)

	if rep.WriterPredatesLabels() {
		return "warn", fmt.Sprintf(
			"NOT MEASURABLE on this box yet — %d one-shot removal(s) in %s carry the RETIRED `one_shot_complete` label "+
				"(newest %s), so the pogod that wrote them predates d71e1e2 and could not emit `one_shot_unacked` at all. "+
				"An unanswered one-shot would be invisible here until pogod is rebuilt. Run `curl -s http://127.0.0.1:%d/version` "+
				"to see what is running. %s",
			rep.Legacy, window, rep.LegacyLast.Local().Format("2006-01-02 15:04 MST"), doctorVersionPort(), oneShotLimits)
	}

	if len(rep.Unanswered) == 0 {
		if rep.Total() == 0 && rep.Fires == 0 {
			return "pass", fmt.Sprintf("no one-shot fired or was reaped in %s — nothing to answer for, which is not the same as nothing missed. %s",
				window, oneShotLimits)
		}
		return "pass", fmt.Sprintf("%d one-shot(s) acked in %s, none unanswered%s. %s",
			len(rep.Answered), window, pendingSuffix(rep), oneShotLimits)
	}

	var named []string
	for _, o := range rep.Unanswered {
		named = append(named, oneShotIdentity(o))
	}
	return "warn", fmt.Sprintf("%d one-shot(s) FIRED AND NOBODY ANSWERED in %s: %s. %d acked. "+
		"Run `pogo check-oneshots` for what each was carrying. %s",
		len(rep.Unanswered), window, strings.Join(named, "; "), len(rep.Answered), oneShotLimits)
}

// oneShotIdentity is the short form: which one-shot, whose, and when it left.
// The id alone is not always an identity — an `--id`-less `pogo schedule --once`
// is `sch-<hex>` — so the message digest is appended when the id is generated.
func oneShotIdentity(o scheduler.OneShotOutcome) string {
	s := fmt.Sprintf("%s → %s", o.ID, o.Agent)
	if o.Reason == scheduler.ReasonOneShotUndelivered {
		s += " (never delivered)"
	}
	if strings.HasPrefix(o.ID, "sch-") && o.Message != "" {
		s += fmt.Sprintf(" %q", truncateRunes(o.Message, 60))
	}
	if !o.Removed.IsZero() {
		s += ", " + o.Removed.Local().Format("2006-01-02 15:04 MST")
	}
	return s
}

// pendingSuffix reports fires that are still inside their ack window, so a
// clean row is not read as a verdict on them.
func pendingSuffix(rep scheduler.OneShotReport) string {
	pending := rep.Fires - rep.Total()
	if pending <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d fired and still inside the %s ack window)", pending, scheduler.AckStaleWindow)
}

// describeOneShotWindow states the window actually covered, including the case
// where the log does not reach back as far as asked. A report whose oldest
// record is newer than --since answered a shorter question than it was given.
func describeOneShotWindow(rep scheduler.OneShotReport, now time.Time) string {
	end := now
	if !rep.Until.IsZero() {
		end = rep.Until
	}
	w := fmt.Sprintf("the window %s → %s",
		rep.Since.Local().Format("2006-01-02 15:04 MST"), end.Local().Format("2006-01-02 15:04 MST"))
	if !rep.Oldest.IsZero() && rep.Oldest.After(rep.Since) {
		w += fmt.Sprintf(" (the log only reaches back to %s%s)",
			rep.Oldest.Local().Format("2006-01-02 15:04 MST"), spilledNote(rep))
	}
	return w
}

func spilledNote(rep scheduler.OneShotReport) string {
	if rep.Spilled {
		return ", and rotation has discarded records before that"
	}
	return ""
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// newCheckOneShotsCmd builds `pogo check-oneshots`: the detail view behind the
// doctor row, and the eleventh read-only detector in the check-* family.
func newCheckOneShotsCmd(jsonOutput *bool) *cobra.Command {
	var (
		sinceRaw string
		untilRaw string
		logPath  string
		all      bool
	)
	cmd := &cobra.Command{
		Use:   "check-oneshots",
		Short: "Report one-shot schedules that fired and nobody answered (never re-fires anything)",
		Long: `Report every ONE-SHOT schedule whose fire went unanswered — the obligations that
happen once, are never retried, and whose silent no-op has no next cycle to be
caught by.

WHAT IT READS. ` + "`schedule_removed`" + ` in the scheduler's own events.log, whose reason
says how each one-shot left the live set (all four labels are from mg-64e6):

  one_shot_acked        the agent redeemed the token — a live turn reported the
                        work done. The only outcome carrying evidence.
  one_shot_unacked      reaped 24h after firing with the token never redeemed.
  one_shot_undelivered  delivery failed, so no turn ran at all.
  one_shot_skipped      the replay policy elided a stale fire. Listed apart:
                        under ` + "`--replay skip`" + ` that elision is the configured
                        intent, not a miss.

The first two of those are the finding. Each is printed with WHAT IT WAS
CARRYING, not just a count — the value of this class is entirely in the identity
of the missed obligation, and a one-shot registered without an explicit ` + "`--id`" + ` is
called ` + "`sch-<hex>`" + `, so for those the message is the only thing that says what was
missed. The fire time is joined from ` + "`scheduler_fire_delivered`" + ` so each row can
say how long it sat.

WHAT IT DOES NOT MEASURE, printed on every verdict this command renders: it
reports what the log RECORDS. A one-shot acked by an agent that then did nothing
counts as answered here, and a fire still inside its 24h ack window is neither
answered nor missed.

A LOG WHOSE WRITER PREDATES THE LABELS IS REPORTED AS SUCH, never as clean. The
four labels ship in d71e1e2 and are inert until pogod is rebuilt onto it; before
that every one-shot left as the retired ` + "`one_shot_complete`" + `, which this command
recognises for exactly this purpose. Finding one in the window means an
unanswered one-shot would be INVISIBLE here, and it says so rather than printing
a zero it cannot stand behind.

REPORTS ONLY. It never re-fires a one-shot, never re-registers one, and never
acks on anyone's behalf. Re-firing a missed obligation is a decision with a
blast radius (a pre-deploy step run twice is not a null operation), and it
belongs to whoever reads this.

Exit status: 0 nothing unanswered, 1 at least one unanswered one-shot or a
window this command could not measure.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			now := time.Now()
			since, err := parseOneShotBound(sinceRaw, now.Add(-defaultOneShotWindow))
			if err != nil {
				cli.ExitWithError(*jsonOutput, fmt.Sprintf("--since: %v", err), cli.ExitError)
			}
			until, err := parseOneShotBound(untilRaw, time.Time{})
			if err != nil {
				cli.ExitWithError(*jsonOutput, fmt.Sprintf("--until: %v", err), cli.ExitError)
			}
			if !until.IsZero() && !until.After(since) {
				cli.ExitWithError(*jsonOutput, fmt.Sprintf("--until (%s) must be after --since (%s)",
					until.Format(time.RFC3339), since.Format(time.RFC3339)), cli.ExitError)
			}

			if logPath == "" {
				logPath = defaultSchedulerLogPath()
			}
			rep, rerr := scheduler.ReadOneShotOutcomes(logPath, since, until)
			if rerr != nil {
				// An unreadable log is not an empty measurement. Exiting clean
				// here would report "nothing unanswered" for a run that looked
				// at nothing, which is the failure this whole command exists to
				// stop happening one level up.
				cli.ExitWithError(*jsonOutput,
					fmt.Sprintf("NOT MEASURED — cannot read %s: %v", logPath, rerr), cli.ExitError)
			}

			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(renderOneShotReport(rep, now, all))
			}
			if len(rep.Unanswered) > 0 || rep.WriterPredatesLabels() {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().StringVar(&sinceRaw, "since", "", "Window start: RFC3339, or a duration like 48h (default: 7 days back)")
	cmd.Flags().StringVar(&untilRaw, "until", "", "Window end: RFC3339, or a duration like 6h (default: now)")
	cmd.Flags().StringVar(&logPath, "log", "", "events.log to read (default: the scheduler's own log under POGO_HOME)")
	cmd.Flags().BoolVar(&all, "all", false, "Also list the one-shots that WERE answered, not just the unanswered")
	return cmd
}

// renderOneShotReport is the human view.
func renderOneShotReport(rep scheduler.OneShotReport, now time.Time, all bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "One-shot obligations in %s\n", describeOneShotWindow(rep, now))
	fmt.Fprintf(&b, "  read from: %s\n\n", strings.Join(rep.Files, ", "))

	if rep.WriterPredatesLabels() {
		fmt.Fprintf(&b, "NOT MEASURABLE — %d removal(s) in this window carry the RETIRED `one_shot_complete`\n"+
			"label (newest %s). The pogod that wrote them predates d71e1e2 and\n"+
			"cannot emit `one_shot_unacked`, so an unanswered one-shot would not appear below.\n"+
			"Check what is running:  curl -s http://127.0.0.1:%d/version | jq -r .revision\n\n",
			rep.Legacy, rep.LegacyLast.Local().Format("2006-01-02 15:04 MST"), doctorVersionPort())
	}

	if len(rep.Unanswered) == 0 {
		switch {
		case rep.WriterPredatesLabels():
			// The notice above is the whole answer. Printing "nothing
			// unanswered" underneath it would restate, in the reassuring
			// direction, precisely what the notice just said cannot be known.
			fmt.Fprintf(&b, "%d one-shot(s) left the live set in this window under the retired label.\n"+
				"What happened to each of them is not recorded anywhere.\n", rep.Legacy)
		case rep.Total() == 0 && rep.Fires == 0:
			b.WriteString("No one-shot fired or was reaped in this window.\n" +
				"That is not the same as nothing being missed — it is a window in which\n" +
				"this class of obligation did not occur.\n")
		default:
			fmt.Fprintf(&b, "Nothing unanswered. %d acked%s.\n", len(rep.Answered), pendingSuffix(rep))
		}
	} else {
		fmt.Fprintf(&b, "UNANSWERED (%d) — fired, and no turn ever reported the work done:\n\n", len(rep.Unanswered))
		for _, o := range rep.Unanswered {
			fmt.Fprintf(&b, "  %s\n", oneShotIdentity(o))
			fmt.Fprintf(&b, "    reason:  %s\n", o.Reason)
			if o.Kind != "" {
				fmt.Fprintf(&b, "    kind:    %s\n", o.Kind)
			}
			if !o.Fired.IsZero() {
				fmt.Fprintf(&b, "    fired:   %s", o.Fired.Local().Format("2006-01-02 15:04:05 MST"))
				if w := o.Waited(); w > 0 {
					fmt.Fprintf(&b, " (unanswered for %s)", w.Round(time.Minute))
				}
				b.WriteString("\n")
			} else {
				// Named rather than left blank: the delivery record being
				// outside the scanned files is a gap in this reading, not a
				// fire that did not happen.
				fmt.Fprintf(&b, "    fired:   not found in the scanned log — the delivery record is older than the files read\n")
			}
			if o.Message != "" {
				fmt.Fprintf(&b, "    carried: %s\n", o.Message)
			}
			if o.Error != "" {
				fmt.Fprintf(&b, "    error:   %s\n", o.Error)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d acked in the same window%s.\n", len(rep.Answered), pendingSuffix(rep))
	}

	if len(rep.Skipped) > 0 {
		fmt.Fprintf(&b, "\nSKIPPED (%d) — the replay policy elided a stale fire; under `--replay skip`\n"+
			"that is the configured intent, not a miss:\n", len(rep.Skipped))
		for _, o := range rep.Skipped {
			fmt.Fprintf(&b, "  %s\n", oneShotIdentity(o))
		}
	}

	if all && len(rep.Answered) > 0 {
		fmt.Fprintf(&b, "\nANSWERED (%d) — a live turn redeemed the token:\n", len(rep.Answered))
		for _, o := range rep.Answered {
			fmt.Fprintf(&b, "  %s", oneShotIdentity(o))
			if w := o.Waited(); w > 0 {
				fmt.Fprintf(&b, " (answered in %s)", w.Round(time.Second))
			}
			b.WriteString("\n")
		}
	}

	fmt.Fprintf(&b, "\n%s\n", oneShotLimits)
	return b.String()
}

// parseOneShotBound accepts an RFC3339 stamp or a bare duration ("48h", meaning
// that long ago). Empty yields def.
func parseOneShotBound(raw string, def time.Time) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is neither an RFC3339 timestamp nor a duration like 48h", raw)
	}
	if d < 0 {
		d = -d
	}
	return time.Now().Add(-d), nil
}

// defaultSchedulerLogPath resolves the scheduler's own events.log — the log the
// scheduler WRITES to, which is not necessarily the globally-resolved one (see
// scheduler.EventLogPath and mg-e06d).
func defaultSchedulerLogPath() string {
	p, err := scheduler.DefaultPath()
	if err != nil {
		return ""
	}
	return scheduler.EventLogPath(p)
}

// doctorVersionPort is the port the /version probe in this row's advice would
// hit, read from config rather than hard-coded so the advice is runnable on a
// box that moved it.
func doctorVersionPort() int { return config.Load().Port }
