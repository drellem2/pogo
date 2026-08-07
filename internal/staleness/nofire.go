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

// Clean reports whether every night the log can speak for got a fire. A missing
// log is not clean: a witness whose input is absent must not answer "fine".
func (r NoFireReport) Clean() bool {
	return r.LogFound && r.MissedTotal == 0
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
	rep := NoFireReport{LogPath: logPath, LogFound: found}
	due := sched.LastDueNight(now)
	rep.LastDueNight = due.Format(stampDateLayout)

	if !found {
		return rep
	}

	loc := now.Location()
	fired := map[string]bool{}
	var horizon time.Time

	for _, line := range strings.Split(logText, "\n") {
		if stamp, ok := parseLogStamp(line); ok {
			// The horizon comes from ANY timestamped line, not just start lines:
			// the log's coverage begins with whatever it wrote first, and a log
			// whose first line is mid-run still proves it was open then.
			if horizon.IsZero() || stamp.Before(horizon) {
				horizon = stamp
			}
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

// Summary is the one-line form used in subjects and CLI headers — the part that
// travels. The count leads because it is the number a reader acts on.
func (r NoFireReport) Summary() string {
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
