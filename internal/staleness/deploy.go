package staleness

// The MISSED-RUN half of the witness (mg-dd49).
//
// THE PROBLEM THIS SOLVES IS AN ABSENCE. Between 2026-07-31 and 2026-08-04 the
// nightly redeploy did not fire on four consecutive nights (the box was powered
// off through each 03:00 window, and launchd replays a missed
// StartCalendarInterval on WAKE but not across a power cycle). On the fifth it
// fired and died one second in on a transient ssh failure. Nothing alarmed on
// any of the five, and pogod went on serving code that was eventually 52 commits
// and 6 days old. The gap was found by hand, by running `ls` on a binary.
//
// The reason nothing alarmed is structural, and it is the whole design
// constraint here: A DEPLOY THAT NEVER RUNS PRODUCES NO OUTPUT. There is no log
// line for a fire that did not happen, no non-zero exit, no mail. Every detector
// that reads the deploy's own output is blind to exactly this case — which is
// why four nights passed unnoticed under a runner that alerts loudly on failure.
//
// So the witness does not read the deploy. It reads an EXPECTATION and asks
// whether the record satisfies it:
//
//	expectation   a successful deploy on every night, settling by the last
//	              scheduled fire plus a grace (service.DeploySchedule)
//	record        ~/.pogo/deploy-attempt.stamp, one line, written by the EXIT
//	              trap of pogo-deploy.sh: "<date> <attempts> <last_rc>"
//
// A night after the stamp's date has no record at all, which is precisely the
// signature of a fire that never happened. That is a positive reading of a
// negative fact, and it is available even though the missing run wrote nothing.
//
// TWO WAYS A NIGHT FAILS TO REDEPLOY, and both are reported, because from the
// outside they have the same consequence — the fleet runs yesterday's code:
//
//	no-fire   no record exists for that night (powered off, LaunchAgent
//	          unloaded, plist removed, launchd wedged — all indistinguishable
//	          from here, and all equally worth waking up for)
//	failed    a record exists and its rc is non-zero (2026-08-05: rc=1 on a
//	          one-second ssh failure)
//
// The stamp holds ONE line, so only the most recent night's rc is knowable;
// earlier nights are judged solely on the absence of a record. That is a real
// limit and the report states it rather than implying a full history.
//
// WHY THIS SURVIVES A STALE BINARY. The witness that watches for staleness must
// keep working while the thing it watches is stale — otherwise the first failure
// disables the alarm. Nothing here consults the running binary's build, its
// embedded assets, or a network. It reads a text file and a schedule constant,
// both of which a 52-commit-behind binary has exactly as right as a current one.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// stampDateLayout is the date format pogo-deploy.sh writes into the stamp
// (`deploy_date` is `date +%Y-%m-%d` in the local zone at fire time).
const stampDateLayout = "2006-01-02"

// maxEnumeratedNights caps the per-night listing. A stamp from months ago would
// otherwise render a wall of dates that buries the number, which is the part a
// reader acts on. The COUNT is always exact — only the enumeration is clipped.
const maxEnumeratedNights = 14

// Attempt is a parsed ~/.pogo/deploy-attempt.stamp line.
type Attempt struct {
	Date     string `json:"date"`
	Attempts int    `json:"attempts"`
	RC       int    `json:"rc"`
}

// ParseAttempt parses the stamp's single line, "<date> <attempts> <last_rc>".
//
// It is deliberately strict, which is the opposite of how pogo-deploy.sh reads
// the same file: there, an unparseable stamp degrades to "first attempt of the
// night" so a corrupt stamp costs one extra deploy rather than silently
// disabling the nightly. Here the failing-safe direction is reversed — a stamp
// this code cannot read is a stamp it cannot vouch for, and reporting "cannot
// read the deploy record" is the honest answer. Guessing would let a corrupt
// stamp read as a healthy night, which is the silence this whole witness exists
// to end.
func ParseAttempt(line string) (Attempt, error) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 3 {
		return Attempt{}, fmt.Errorf("expected 3 fields (<date> <attempts> <rc>), got %d in %q", len(fields), strings.TrimSpace(line))
	}
	if _, err := time.Parse(stampDateLayout, fields[0]); err != nil {
		return Attempt{}, fmt.Errorf("field 1 is not a %s date: %q", stampDateLayout, fields[0])
	}
	attempts, err := strconv.Atoi(fields[1])
	if err != nil {
		return Attempt{}, fmt.Errorf("field 2 (attempts) is not a number: %q", fields[1])
	}
	rc, err := strconv.Atoi(fields[2])
	if err != nil {
		return Attempt{}, fmt.Errorf("field 3 (rc) is not a number: %q", fields[2])
	}
	return Attempt{Date: fields[0], Attempts: attempts, RC: rc}, nil
}

// DeploySchedule is the expectation the record is judged against.
type DeploySchedule struct {
	// Hours are the scheduled fire hours, local, in order. Only the last one
	// matters to this file: it is when a night stops being allowed to still be
	// in progress.
	Hours []int
	// Minute is the minute within each fire hour.
	Minute int
	// Grace is how long after the LAST fire a night may still legitimately be
	// running before its silence counts as a miss. It exists because the deploy
	// drains the fleet first and pogo-deploy.sh caps one attempt at 2h
	// (POGO_DEPLOY_MAX_DRAIN), so a 05:00 fire can still be working at 06:30 and
	// has not missed anything.
	Grace time.Duration
}

// DefaultGrace matches POGO_DEPLOY_MAX_DRAIN in pogo-deploy.sh: the longest one
// attempt is allowed to take. A grace shorter than that would report a deploy
// that is at that moment succeeding.
const DefaultGrace = 2 * time.Hour

// lastFireHour is the hour of the final scheduled fire; 0 for an empty
// schedule, which then makes a night due at midnight + Grace.
func (s DeploySchedule) lastFireHour() int {
	if len(s.Hours) == 0 {
		return 0
	}
	return s.Hours[len(s.Hours)-1]
}

// settleTime is the instant by which the deploy for night `day` should have
// finished, one way or the other.
func (s DeploySchedule) settleTime(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), s.lastFireHour(), s.Minute, 0, 0, day.Location()).Add(s.Grace)
}

// LastDueNight is the most recent night whose deploy should already have
// settled at `now`. Nights are named by the local calendar date of their fire,
// matching what pogo-deploy.sh writes into the stamp.
//
// Walking back day by day rather than assuming "today or yesterday" keeps this
// correct for any Grace, including one longer than a day.
func (s DeploySchedule) LastDueNight(now time.Time) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for i := 0; i < 400; i++ {
		if !s.settleTime(day).After(now) {
			return day
		}
		day = day.AddDate(0, 0, -1)
	}
	return day
}

// MissedNight is one night on which the fleet did not get a successful deploy.
type MissedNight struct {
	Date string `json:"date"`
	// Reason is "no-fire" (no record for that night at all) or "failed" (a
	// record exists and its rc is non-zero). They are kept apart because they
	// send a reader to different places: no-fire means look at launchd and the
	// host's uptime, failed means read pogo-deploy.log.
	Reason string `json:"reason"`
	RC     int    `json:"rc,omitempty"`
}

// DeployReport is the missed-run witness's answer.
type DeployReport struct {
	// StampPath is the record that was read, named so a reader can check it.
	StampPath string `json:"stamp_path"`
	// StampFound is false when the file does not exist. On a host with the
	// deploy agent installed that is itself a finding: nothing has ever
	// recorded an attempt.
	StampFound bool `json:"stamp_found"`
	// ParseErr is set when the stamp exists and could not be read. Also a
	// finding — see ParseAttempt on why this does not degrade to "assume fine".
	ParseErr string `json:"parse_error,omitempty"`
	// Last is the most recent recorded attempt, when the stamp parsed.
	Last *Attempt `json:"last_attempt,omitempty"`
	// LastDueNight is the expectation's answer to "which night should already
	// have deployed?" — printed so the reader can check the judgement rather
	// than take it.
	LastDueNight string `json:"last_due_night"`
	// Missed is the enumeration, newest first, clipped to maxEnumeratedNights.
	Missed []MissedNight `json:"missed"`
	// MissedTotal is exact even when Missed is clipped.
	MissedTotal int `json:"missed_total"`
	// Truncated records that Missed is shorter than MissedTotal, so a reader
	// never mistakes the clip for the whole set.
	Truncated bool `json:"truncated,omitempty"`
}

// Clean reports whether the nightly deploy is meeting its expectation.
func (r DeployReport) Clean() bool {
	return r.StampFound && r.ParseErr == "" && r.MissedTotal == 0
}

// CheckDeploy judges a stamp against a schedule. Pure: the caller supplies the
// stamp's bytes, whether it existed, and the clock, so both halves of the
// positive control — a genuinely missed run and a healthy night — are
// constructible without waiting a night or moving the system clock.
//
// found=false and a parse failure are BOTH reported, and neither is folded into
// "0 missed nights". An alarm whose broken-input case looks identical to its
// all-clear case is an alarm that goes quiet exactly when its input rots.
func CheckDeploy(stampPath, stampLine string, found bool, now time.Time, sched DeploySchedule) DeployReport {
	rep := DeployReport{StampPath: stampPath, StampFound: found}
	due := sched.LastDueNight(now)
	rep.LastDueNight = due.Format(stampDateLayout)

	if !found {
		return rep
	}
	att, err := ParseAttempt(stampLine)
	if err != nil {
		rep.ParseErr = err.Error()
		return rep
	}
	rep.Last = &att

	last, _ := time.ParseInLocation(stampDateLayout, att.Date, now.Location())

	// Every night strictly after the record's date, up to and including the
	// last due one, has no record — the signature of a fire that never
	// happened. Counted first, enumerated second, so the count survives the
	// clip.
	for d := due; d.After(last); d = d.AddDate(0, 0, -1) {
		rep.MissedTotal++
		if len(rep.Missed) < maxEnumeratedNights {
			rep.Missed = append(rep.Missed, MissedNight{Date: d.Format(stampDateLayout), Reason: "no-fire"})
		} else {
			rep.Truncated = true
		}
	}

	// The recorded night itself, which is the only one whose outcome is
	// knowable: the stamp holds a single line. A record dated after the last
	// due night is tonight's run, still inside its grace, and is not judged.
	if att.RC != 0 && !last.After(due) {
		rep.MissedTotal++
		if len(rep.Missed) < maxEnumeratedNights {
			rep.Missed = append(rep.Missed, MissedNight{Date: att.Date, Reason: "failed", RC: att.RC})
		} else {
			rep.Truncated = true
		}
	}
	return rep
}
