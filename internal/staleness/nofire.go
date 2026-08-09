package staleness

// The DID-NOT-RUN witness (mg-2416), read from the deploy LOG rather than from
// the deploy's outcome.
//
// THE HALF OF THE OUTAGE NOTHING COULD SEE. Of the eight nights 2026-07-29..
// 08-07, four were not failures at all. On 08-01, 08-02, 08-03 and 08-04 the
// job NEVER STARTED — no `pogo-deploy: start` line, no exit code, no stamp, no
// mail. Every alarm we owned was downstream of the job running: the RED fires
// on a nonzero rc, and a job that does not start has no rc. Four consecutive
// silent nights were therefore indistinguishable from four healthy ones,
// because health is also silence.
//
// WHY THIS READS THE LOG AND NOT launchd. The obvious instinct is to ask
// launchd how many times it spawned the job. It does not work, and it fails in
// the direction that produces a false all-clear. Measured on this box on
// 2026-08-07, after seven fires had demonstrably run and written to the log:
//
//	gui/501/com.pogo.deploy = { runs = 0, last exit code = (never exited) }
//
// `runs` is a counter on the CURRENT bootstrap of the job, and re-installing
// the plist (mg-b201 did exactly that, that morning) resets it to zero. A
// detector keyed on `runs` would have read 0 for a job that had just run, and
// 0 for a job that had never run, with no way to tell them apart. Nothing else
// in launchd's runtime state dates a fire either. The log is the only artifact
// on the box that records WHICH NIGHTS a fire happened on, because the runner
// appends a timestamped start line before it does anything else.
//
// WHAT IS JUDGED. An EXPECTATION against a RECORD, the same shape as the
// missed-run witness in deploy.go, but with a record that holds every night
// instead of one:
//
//	expectation   a fire on every night, by the schedule in service.DeploySchedule
//	record        ~/Library/Logs/pogo/pogo-deploy.log — one `pogo-deploy: start`
//	              line per fire, timestamped UTC by the runner's own `ts()`
//
// A due night with no start line in a log that COVERS that night is a fire that
// did not happen. That is the positive reading of a negative fact, and unlike
// every alarm that failed in this incident it does not require the job to have
// run in order to say anything.
//
// THE HORIZON IS THE WHOLE CORRECTNESS ARGUMENT. A log can only speak for the
// period it covers. If it were rotated or truncated, the nights before its
// first line have no evidence either way — and calling those nights "no-fire"
// would manufacture an outage out of housekeeping, while calling them "fine"
// would be the false all-clear this witness exists to remove. So they are
// neither: a night is judged only when the log's earliest line is at or before
// that night's FIRST scheduled fire, and the nights before that are counted and
// reported as UNKNOWABLE, in their own field, never folded into the missed
// count and never folded into the clear.
//
// DRY RUNS ARE NOT DEPLOYS. `pogo-deploy.sh --dry-run` writes a start line with
// `dry_run=true` and deploys nothing; one such line sits in the real log at
// 2026-07-29T18:32:12Z. Counting it would let a human's manual dry run mark a
// night as covered, so it is parsed, counted separately, and never satisfies a
// night.
//
// IT REPORTS THE ABSENCE, NOT A CAUSE. Powered off, wedged by the
// nondemand-spawn pending state (mg-50e0), LaunchAgent unloaded, plist removed —
// all four are indistinguishable from this record and all four mean the fleet is
// running yesterday's code. For the 08-01..08-04 nights the cause was measured
// afterwards and it was the FIRST one: the host was off from 07-31 21:18 to
// 08-04 11:40 (see docs/investigations/deploy-nofire-nights-were-a-power-off-
// 2026-08-07.md), not the wedge mg-2416's body proposed. Which is the argument
// for not building the judgement around any of them.
//
// WHAT SURVIVES. Nothing here consults the running binary, the network, the
// stamp, or launchd. It reads a text file and a schedule constant, which a
// daemon 85 commits behind main has exactly as right as a current one.
//
// ---------------------------------------------------------------------------
// A RUN THAT STARTS AND NEVER FINISHES (mg-56ac)
// ---------------------------------------------------------------------------
// Everything above partitions the world into `ran` and `did-not-run`, and on
// 2026-08-08 that partition put the worst night of the window on the GOOD side.
// The measurement, from the log this witness reads:
//
//	[2026-08-08T02:00:05Z] pogo-deploy: start (...)
//	[2026-08-08T02:00:05Z] GH_TOKEN: sourced from /Users/daniel/.zshenv
//	  ... 31 hours 39 minutes of nothing ...
//	[2026-08-09T09:39:43Z] sync: /Users/daniel/.pogo/deploy-src at main 738e322
//	[2026-08-09T09:43:23Z] pogo-deploy: done — pogod redeployed to 738e322
//
// ONE run. It started on time, blocked inside the sync for 31h39m, and then
// completed. The crew had been stopped at 00:44Z and stayed stopped for 33
// hours, because the run that would have brought it back was still in that gap.
//
// `deploy_nofire` fired the next morning and reported five missed nights —
// 08-09, 08-04, 08-03, 08-02, 08-01 — and 2026-08-08 WAS NOT AMONG THEM. It had
// a start line, so the check called it a night that ran, which is the one thing
// it was designed never to get wrong about a night that produced no deploy. The
// same run also stamped its attempt with the date it FINISHED, so 08-09 was
// reported missed on a morning when a deploy had in fact just landed. The
// instrument was wrong about both nights, in both directions.
//
// So the question this file asks is no longer "did a run start?" but "did a run
// START AND FINISH, inside a length a night can afford?". A run is HUNG when the
// distance from its start line to its terminal line — or to now, if it has not
// written one — exceeds HungAfter. That covers both shapes: the run that comes
// back far too late (08-08) and the run that never comes back at all.
//
// TWO ARMS, BECAUSE THEY REST ON DIFFERENT EVIDENCE, AND ONLY ONE NEEDS A NEW
// RUNNER:
//
//	TERMINATED-LATE   the run DID write a terminal line, more than HungAfter
//	                  after its start. Positive evidence, present in logs this
//	                  box already holds, so this arm judges every run in the
//	                  record and needed no deploy to become true. It is the arm
//	                  that fires on 2026-08-08.
//	NEVER-TERMINATED  the run wrote no terminal line at all. The absence of a
//	                  line is only evidence about the RUN once the runner is
//	                  known to write one — before that it is evidence about the
//	                  runner's version. Judging it unconditionally would have
//	                  reported 2026-07-31 (which exited 9 at 02:30 under a runner
//	                  that had no terminal line yet) as a five-day hang. So this
//	                  arm is armed by the log itself: it judges only runs at or
//	                  after the first `pogo-deploy: end` line, the marker the
//	                  runner's EXIT trap now writes on every path, and reports
//	                  the runs it did NOT judge rather than folding them into
//	                  either verdict.
//
// The horizon argument is the same one this file already makes about rotation,
// applied to a marker instead of a date: a witness must not manufacture an
// outage out of its own newness, and must not report an all-clear it did not
// earn either.
//
// WHY NOT JUST BOUND THE RUN AND BE DONE. The runner grew a wall-clock deadline
// in the same ticket, and it is the repair — this is the witness for it. They
// are not redundant: the deadline lives inside the run and can be defeated by
// the same wedge that stopped the run (a SIGKILLed shell writes nothing, an
// unarmed watchdog watches nothing), while this reads a file afterwards from
// another process. A bound whose only evidence that it worked is the bounded
// thing reporting so is the shape this whole lineage keeps failing on.

import (
	"fmt"
	"strings"
	"time"
)

// logStampLayout is the timestamp pogo-deploy.sh's `ts()` writes:
// `date -u +%Y-%m-%dT%H:%M:%SZ`. Every line it logs is prefixed with one in
// brackets.
const logStampLayout = "2006-01-02T15:04:05Z"

// startMarker is the substring that identifies a fire. pogo-deploy.sh logs
// `pogo-deploy: start (src=... window=... dry_run=...)` as the first thing it
// does after the window guard, so its presence dates a fire and its absence
// across a due night dates the lack of one.
const startMarker = "pogo-deploy: start"

// dryRunTrue is the field the runner writes for a --dry-run invocation.
const dryRunTrue = "dry_run=true"

// endMarker is the terminal line the runner's EXIT trap writes on EVERY path
// (mg-56ac): `pogo-deploy: end (rc=N ...)`. It is the one marker whose absence
// is evidence about the RUN rather than about the runner's version, so it — and
// only it — arms the never-terminated arm.
const endMarker = "pogo-deploy: end"

// terminalMarkers are the substrings that date a run's LAST breath. endMarker
// is the one the current runner guarantees; the rest are the terminal lines
// older runners wrote, kept because the terminated-late arm judges the record
// this box already holds and that record spans four runner versions.
//
// Matching is deliberately generous in the direction that costs a MISS rather
// than a false alarm: an extra match makes a run look SHORTER than it was, so a
// marker that fires early can only hide a hang, never invent one. All of them
// are matched on timestamped lines only, which excludes the alert bodies the
// runner echoes verbatim to the log — those carry remedy prose that mentions
// exits, and reading prose as a terminal line is exactly the class of mistake
// (gh#113) this repo has already refused once.
var terminalMarkers = []string{
	endMarker,
	"pogo-deploy: done", // the success path, since the runner's first version
	"attempt recorded:", // the EXIT trap's stamp line (mg-8f7e)
	"Exit 0.",           // the skip gates: window, budget, no-drift, settled
	"exiting 0",         // the lock gate, which skips before the stamp exists
}

// DefaultHungAfter is how long a run may take before it is a hang rather than a
// deploy.
//
// Six hours. The production window is four ([2,6) local) and the runner's own
// deadline bounds a run at the window's end plus slack, so a legitimate run
// cannot approach this; the 2026-08-08 run exceeded it by a factor of five. The
// number is chosen to sit ABOVE anything the runner can legitimately do and
// BELOW a working day, so the morning's sample names the night that hung while
// the reader can still act on it.
const DefaultHungAfter = 6 * time.Hour

// Fire is one parsed `pogo-deploy: start` line.
type Fire struct {
	// Night is the LOCAL calendar date of the fire, which is how nights are
	// named everywhere else in this package and in the stamp.
	Night string `json:"night"`
	// At is the line's timestamp, as written (UTC).
	At time.Time `json:"at"`
	// DryRun is true for `dry_run=true` lines, which are not deploys.
	DryRun bool `json:"dry_run,omitempty"`
}

// NoFireReport is the did-not-run witness's answer.
type NoFireReport struct {
	// LogPath is the record that was read, named so a reader can check it.
	LogPath string `json:"log_path"`
	// LogFound is false when the file does not exist. On a host with the deploy
	// agent installed that is itself the finding, and the loudest one available:
	// the job has never written a line.
	LogFound bool `json:"log_found"`
	// Fires is how many real (non-dry-run) start lines the log holds.
	Fires int `json:"fires"`
	// DryRuns is how many `dry_run=true` start lines it holds. Reported so a
	// reader can see that they were excluded rather than wonder.
	DryRuns int `json:"dry_runs,omitempty"`
	// Unparsed is how many start lines carried a timestamp this witness could
	// not read. They cannot date a night, so they are counted and named rather
	// than silently dropped — a line we cannot read must not read as a night we
	// can vouch for.
	Unparsed int `json:"unparsed,omitempty"`
	// Horizon is the earliest timestamp anywhere in the log, RFC3339, or "" when
	// the log holds no readable timestamp at all. It bounds what the log can
	// speak for.
	Horizon string `json:"horizon,omitempty"`
	// LastDueNight is the expectation's answer to "which night should already
	// have fired?", printed so a reader can check the judgement rather than take
	// it.
	LastDueNight string `json:"last_due_night"`
	// Missed is the enumeration of due nights, newest first, that the log covers
	// and that hold no real start line. Clipped to maxEnumeratedNights.
	Missed []string `json:"missed"`
	// MissedTotal is exact even when Missed is clipped.
	MissedTotal int `json:"missed_total"`
	// Truncated records that Missed is shorter than MissedTotal.
	Truncated bool `json:"truncated,omitempty"`
	// EarliestJudged is the oldest due night this log could speak for — the
	// oldest night whose first scheduled fire the log was already open for. ""
	// means the log dated nothing at all.
	EarliestJudged string `json:"earliest_judged,omitempty"`
	// Hung is the enumeration of runs that STARTED and did not finish inside
	// HungAfter, newest first. A hung run is not a missed night — it has a start
	// line — and it is not a healthy one either, which is the distinction the
	// 2026-08-08 outage was scored on the wrong side of.
	Hung []HungRun `json:"hung,omitempty"`
	// HungTotal is exact even when Hung is clipped.
	HungTotal int `json:"hung_total"`
	// HungTruncated records that Hung is shorter than HungTotal.
	HungTruncated bool `json:"hung_truncated,omitempty"`
	// HungAfterSeconds is the threshold that was applied, printed so a reader
	// judges the judgement rather than taking it.
	HungAfterSeconds int `json:"hung_after_seconds"`
	// HangArmed reports whether the NEVER-TERMINATED arm could judge anything:
	// true once the log holds a `pogo-deploy: end` line, which is what proves
	// the runner writes one. The terminated-late arm needs no arming and runs
	// regardless.
	HangArmed bool `json:"hang_armed"`
	// HangHorizon is the timestamp of that first end line, RFC3339, or "".
	HangHorizon string `json:"hang_horizon,omitempty"`
	// HangUnjudged counts runs whose termination could NOT be judged because
	// they predate HangHorizon and left no terminal line of any vintage. They
	// are reported rather than counted as either healthy or hung: an unjudgeable
	// run is a hole in the record, and this witness exists because holes were
	// being read as all-clears.
	HangUnjudged int `json:"hang_unjudged,omitempty"`
	// HorizonLimited records that at least one due night fell before the
	// horizon and was therefore NOT judged.
	//
	// A count of such nights is deliberately not reported. There is no
	// meaningful upper bound on "nights before this log existed" — the honest
	// statement is which nights WERE judged, and that the ones before them were
	// not. What matters is that they are excluded from MissedTotal, so a
	// rotation can never manufacture an outage, and excluded from Clean, so it
	// can never manufacture an all-clear either.
	HorizonLimited bool `json:"horizon_limited,omitempty"`
}

// HungRun is one run that started and did not finish in time.
type HungRun struct {
	// Night is the LOCAL calendar date of the START line. A run that finishes
	// the next day belongs to the night it began on — the 2026-08-08 run
	// stamped its attempt record with 08-09, the date it woke up on, which is
	// how the missed-night list came to name a night that had just deployed.
	Night string `json:"night"`
	// Start is the start line's timestamp, local, RFC3339.
	Start string `json:"start"`
	// Terminated says which arm judged this run: true for a run that wrote a
	// terminal line far too late (evidence), false for one that wrote none at
	// all (absence, judged only past HangHorizon).
	Terminated bool `json:"terminated"`
	// End is the terminal line's timestamp when Terminated; "" otherwise.
	End string `json:"end,omitempty"`
	// ElapsedSeconds is start -> end, or start -> the bound (the next fire, or
	// now) for a run that never terminated. For the second case it is a LOWER
	// bound and Terminated says so.
	ElapsedSeconds int `json:"elapsed_seconds"`
	// SilentSeconds is the longest gap between consecutive lines of this run —
	// the actual stall inside it, as opposed to its total length.
	SilentSeconds int `json:"silent_seconds"`
	// StalledAfter is the last line the run emitted before that gap, verbatim
	// and untrimmed of its timestamp. It is the single most useful field here:
	// on 2026-08-08 it is the GH_TOKEN line, which places the stall inside the
	// sync and nowhere else.
	StalledAfter string `json:"stalled_after,omitempty"`
}

// Clean reports whether every night the log can speak for got a fire that also
// FINISHED. A missing log is not clean: a witness whose input is absent must not
// answer "fine". Neither is a log full of runs that started and hung — that
// reading is the defect mg-56ac exists to remove.
func (r NoFireReport) Clean() bool {
	return r.LogFound && r.MissedTotal == 0 && r.HungTotal == 0
}

// FirstFire is the instant of the FIRST scheduled fire on `day`. It is the
// horizon test: a log that begins after this instant may have missed that
// night's fire without the fire having been missed, so the night is unknowable
// rather than missed.
func (s DeploySchedule) FirstFire(day time.Time) time.Time {
	hour := 0
	if len(s.Hours) > 0 {
		hour = s.Hours[0]
	}
	return time.Date(day.Year(), day.Month(), day.Day(), hour, s.Minute, 0, 0, day.Location())
}

// ParseFire parses one log line into a Fire, reporting ok=false for any line
// that is not a start line.
//
// The timestamp is required. A start line whose stamp will not parse is
// reported as not-ok and counted by CheckNoFire as Unparsed: it proves a fire
// happened but cannot say which night it belongs to, and guessing would let one
// unreadable line vouch for a night that was in fact silent.
func ParseFire(line string) (Fire, bool) {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, startMarker) {
		return Fire{}, false
	}
	stamp, ok := parseLogStamp(line)
	if !ok {
		return Fire{}, false
	}
	return Fire{At: stamp, DryRun: strings.Contains(line, dryRunTrue)}, true
}

// parseLogStamp reads the `[2026-07-29T02:00:04Z] ` prefix every logged line
// carries.
func parseLogStamp(line string) (time.Time, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[") {
		return time.Time{}, false
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(logStampLayout, line[1:end])
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// CheckNoFire judges a deploy log against a schedule.
//
// Pure: the caller supplies the log's text, whether it existed, and the clock,
// so both halves of the positive control — a genuine run of silent nights and a
// healthy week — are constructible without waiting a night or moving the system
// clock. That matters more here than usual: the condition being detected took
// four days to occur in the wild and cannot be reproduced on demand.
//
// `now` carries the location that names nights. The log's stamps are UTC; they
// are converted into that location before being dated, because the schedule and
// the stamp both name nights by LOCAL calendar date.
func CheckNoFire(logPath, logText string, found bool, now time.Time, sched DeploySchedule) NoFireReport {
	return CheckNoFireWithin(logPath, logText, found, now, sched, DefaultHungAfter)
}

// CheckNoFireWithin is CheckNoFire with the hang threshold supplied.
//
// It exists so the threshold is a parameter of the judgement rather than a
// constant buried in it: a test can drive both sides of it without waiting six
// hours, and a caller on a host with a different window can say so. Every
// production caller goes through CheckNoFire.
func CheckNoFireWithin(logPath, logText string, found bool, now time.Time, sched DeploySchedule, hungAfter time.Duration) NoFireReport {
	rep := NoFireReport{LogPath: logPath, LogFound: found, HungAfterSeconds: int(hungAfter.Seconds())}
	due := sched.LastDueNight(now)
	rep.LastDueNight = due.Format(stampDateLayout)

	if !found {
		return rep
	}

	loc := now.Location()
	fired := map[string]bool{}
	var horizon time.Time
	var stamped []stampedLine

	for _, line := range strings.Split(logText, "\n") {
		stamp, ok := parseLogStamp(line)
		if ok {
			// The horizon comes from ANY timestamped line, not just start lines:
			// the log's coverage begins with whatever it wrote first, and a log
			// whose first line is mid-run still proves it was open then.
			if horizon.IsZero() || stamp.Before(horizon) {
				horizon = stamp
			}
			// Kept for the hang arms below. Only timestamped lines are kept:
			// the runner echoes whole alert bodies and drift-check output to the
			// log unprefixed, and a line that cannot be dated cannot bound
			// anything.
			stamped = append(stamped, stampedLine{at: stamp, text: strings.TrimSpace(line)})
		}
		if !strings.Contains(line, startMarker) {
			continue
		}
		f, ok := ParseFire(line)
		if !ok {
			rep.Unparsed++
			continue
		}
		if f.DryRun {
			rep.DryRuns++
			continue
		}
		rep.Fires++
		fired[f.At.In(loc).Format(stampDateLayout)] = true
	}

	checkHangs(&rep, stamped, now, hungAfter)

	if horizon.IsZero() {
		// A log with no readable timestamp anywhere cannot date anything, so no
		// night was judged. Saying that is the honest answer — the one thing
		// this must never do is report zero missed nights, which is exactly what
		// an empty `fired` set would otherwise produce, and it would read as an
		// all-clear.
		rep.HorizonLimited = true
		return rep
	}
	rep.Horizon = horizon.In(loc).Format(time.RFC3339)

	// Walk back from the last due night. The walk ends at the horizon: the first
	// night whose FIRST scheduled fire predates the log's earliest line cannot
	// be judged, and neither can any night before it.
	//
	// maxWalkNights bounds the walk for the same reason LastDueNight bounds its
	// own: a horizon further back than that is not a rotation, it is a log that
	// has been open for over a year, and the enumeration is clipped long before
	// then anyway.
	for day, i := due, 0; ; day, i = day.AddDate(0, 0, -1), i+1 {
		night := day.Format(stampDateLayout)
		// A night is judged when the log was already open at its FIRST scheduled
		// fire — or when the log holds a fire for it, which proves the night was
		// covered no matter where the horizon falls. The second clause matters
		// for exactly one night, the one logging began on: the real log's first
		// line is the 2026-07-29 start line itself, stamped 4s after that
		// night's 03:00 fire, and the strict test alone would report the night
		// we have direct evidence for as un-judgeable. It can never manufacture
		// a miss, because a night with a fire is never a miss.
		if horizon.After(sched.FirstFire(day)) && !fired[night] {
			rep.HorizonLimited = true
			break
		}
		if i >= maxWalkNights {
			rep.HorizonLimited = true
			break
		}
		rep.EarliestJudged = night
		if !fired[night] {
			rep.MissedTotal++
			if len(rep.Missed) < maxEnumeratedNights {
				rep.Missed = append(rep.Missed, night)
			} else {
				rep.Truncated = true
			}
		}
	}
	return rep
}

// maxWalkNights bounds the night-by-night walk, matching LastDueNight's own
// 400-day bound.
const maxWalkNights = 400

// stampedLine is one log line that could be dated. Undated lines are dropped
// before this: the runner writes whole alert bodies and drift-check output to
// the same stream without a prefix, and those must not be read as the acts of a
// run.
type stampedLine struct {
	at   time.Time
	text string
}

// isTerminal reports whether a line is a run's last breath.
func isTerminal(text string) bool {
	for _, m := range terminalMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// checkHangs fills in the hang half of the report: which runs STARTED and did
// not finish inside hungAfter.
//
// A run is the half-open span from its start line to the NEXT start line, or to
// `now` for the last one. That boundary is what makes the arithmetic honest for
// a run that never terminated: whatever it was doing, it was not doing it past
// the point another fire began, so the next start is a bound on its length
// rather than a guess at it.
func checkHangs(rep *NoFireReport, lines []stampedLine, now time.Time, hungAfter time.Duration) {
	loc := now.Location()

	// The NEVER-TERMINATED arm's horizon: the first `pogo-deploy: end` line
	// anywhere in the log. Before it, a run with no terminal line tells us about
	// the runner that wrote the log, not about the run — see the header.
	var hangHorizon time.Time
	for _, l := range lines {
		if strings.Contains(l.text, endMarker) {
			hangHorizon = l.at
			rep.HangArmed = true
			rep.HangHorizon = l.at.In(loc).Format(time.RFC3339)
			break
		}
	}

	// Start-line indices, dry runs included: a dry run bounds the run before it
	// even though it is never itself judged.
	var starts []int
	for i, l := range lines {
		if strings.Contains(l.text, startMarker) {
			if _, ok := parseLogStamp(l.text); ok {
				starts = append(starts, i)
			}
		}
	}

	for n, si := range starts {
		f, ok := ParseFire(lines[si].text)
		if !ok || f.DryRun {
			continue
		}
		start := lines[si].at
		end := len(lines)
		bound := now
		if n+1 < len(starts) {
			end = starts[n+1]
			bound = lines[end].at
		}

		// The run's own lines, and the first terminal one among them.
		term := -1
		for i := si + 1; i < end; i++ {
			if isTerminal(lines[i].text) {
				term = i
				break
			}
		}

		var elapsed time.Duration
		var terminated bool
		last := end - 1
		switch {
		case term >= 0:
			terminated = true
			elapsed = lines[term].at.Sub(start)
			last = term
		case !rep.HangArmed || start.Before(hangHorizon):
			// Not judgeable by either arm: it wrote no terminal line, and this
			// log does not establish that its runner ever would have. Counted
			// and reported, never folded into the clear.
			rep.HangUnjudged++
			continue
		default:
			elapsed = bound.Sub(start)
		}
		if elapsed < hungAfter {
			continue
		}

		// The stall inside the run, as opposed to its total length: the longest
		// gap between consecutive lines it emitted. For a run that never
		// terminated the trailing silence up to the bound is part of that.
		var gap time.Duration
		stalledAfter := lines[si].text
		prev := si
		for i := si + 1; i <= last && i < end; i++ {
			if d := lines[i].at.Sub(lines[prev].at); d > gap {
				gap, stalledAfter = d, lines[prev].text
			}
			prev = i
		}
		if !terminated {
			if d := bound.Sub(lines[prev].at); d > gap {
				gap, stalledAfter = d, lines[prev].text
			}
		}

		run := HungRun{
			Night:          start.In(loc).Format(stampDateLayout),
			Start:          start.In(loc).Format(time.RFC3339),
			Terminated:     terminated,
			ElapsedSeconds: int(elapsed.Seconds()),
			SilentSeconds:  int(gap.Seconds()),
			StalledAfter:   stalledAfter,
		}
		if terminated {
			run.End = lines[term].at.In(loc).Format(time.RFC3339)
		}
		rep.HungTotal++
		rep.Hung = append(rep.Hung, run)
	}

	// Newest first, matching Missed, so the subject and the enumeration lead
	// with the night a reader can still act on.
	for i, j := 0, len(rep.Hung)-1; i < j; i, j = i+1, j-1 {
		rep.Hung[i], rep.Hung[j] = rep.Hung[j], rep.Hung[i]
	}
	if len(rep.Hung) > maxEnumeratedNights {
		rep.Hung = rep.Hung[:maxEnumeratedNights]
		rep.HungTruncated = true
	}
}

// HumanDuration renders a span the way the notices and the CLI want it: hours
// and minutes, because "31h39m" is the fact a reader acts on and "114 000
// seconds" is one they have to convert first.
func HumanDuration(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, seconds%60)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// HungSummary is the one-line form of the hang half, or "" when nothing hung.
//
// It leads with the DURATION rather than the count, because one run of 31h39m
// and one of 6h01m are the same count and not the same event, and the duration
// is what tells a reader whether the fleet was down while it happened.
func (r NoFireReport) HungSummary() string {
	if r.HungTotal == 0 {
		return ""
	}
	h := r.Hung[0]
	tail := fmt.Sprintf("started %s and took %s to finish", h.Night, HumanDuration(h.ElapsedSeconds))
	if !h.Terminated {
		tail = fmt.Sprintf("started %s and has STILL not finished %s later", h.Night, HumanDuration(h.ElapsedSeconds))
	}
	if r.HungTotal == 1 {
		return fmt.Sprintf("a nightly deploy HUNG — the run that %s", tail)
	}
	return fmt.Sprintf("%d nightly deploys HUNG — the most recent %s", r.HungTotal, tail)
}

// Summary is the one-line form used in subjects and CLI headers — the part that
// travels. The count leads because it is the number a reader acts on.
//
// A hang is reported ahead of a silent night when both are present. Both are
// real, but the hang is the one that has a fleet stopped underneath it right
// now, and the subject is the part a skim-reader gets.
func (r NoFireReport) Summary() string {
	if s := r.HungSummary(); s != "" {
		if r.MissedTotal > 0 {
			return fmt.Sprintf("%s; and it DID NOT RUN at all on %d night(s)", s, r.MissedTotal)
		}
		return s
	}
	switch {
	case !r.LogFound:
		return "the nightly deploy has NEVER written a log line"
	case r.MissedTotal == 1:
		return fmt.Sprintf("the nightly deploy DID NOT RUN on %s", r.Missed[0])
	case r.MissedTotal > 1 && r.Truncated:
		// The oldest enumerated night is not the oldest missed one, so the range
		// would be a lie. The count is exact and leads regardless.
		return fmt.Sprintf("the nightly deploy DID NOT RUN on %d nights (most recent %s)",
			r.MissedTotal, r.Missed[0])
	case r.MissedTotal > 1:
		return fmt.Sprintf("the nightly deploy DID NOT RUN on %d nights (%s .. %s)",
			r.MissedTotal, r.Missed[len(r.Missed)-1], r.Missed[0])
	default:
		return fmt.Sprintf("every night the log covers got a fire (%d fires)", r.Fires)
	}
}
