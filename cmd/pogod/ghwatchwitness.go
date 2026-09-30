package main

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/ghwatch"
)

// The gh-watch witness (mg-257a8): pogod's half of moving the gh-issue
// detectors out of the daemon.
//
// Until mg-257a8 pogod RAN the intake, teardown and carrier re-read watchers,
// and raised A13's not-armed conditions itself at startup. They now run in
// `pogo gh-watch`, a job launchd starts through a login shell, so pogod no
// longer needs a GitHub credential for them. What pogod keeps is the part only
// a process that is already running can do: read the job's record and say so
// when a detector did not arm, or when the job has stopped writing records at
// all. A job that does not run emits nothing, and "the detector found nothing"
// and "the detector never ran" otherwise read the same — the verdictwatch
// lesson (a correct, audited detector that NOTHING RAN) is why this exists
// rather than trusting the plist to be loaded.
//
// WHY "AWAKE TIME". The job fires on launchd's StartInterval, which does not
// fire while the host sleeps, and pogod's heartbeat keeps ticking across a
// wake before launchd has had its chance. Judging staleness against wall-clock
// time alone would raise this alarm on nearly every wake. So a record is stale
// only when it is older than the window AND pogod has itself been ticking,
// without a sleep-sized gap, for at least that long: the job has had a full
// window of awake time to run and did not.

const (
	// ghWatchStaleAfter is how old the job's last record may be before the job
	// counts as not reporting. The job fires every 15 minutes; an hour is four
	// consecutive missing records.
	ghWatchStaleAfter = time.Hour
	// ghWatchCheckEvery throttles the witness to one file read per interval.
	ghWatchCheckEvery = 5 * time.Minute
	// ghWatchSleepGap is the gap between two heartbeat ticks that counts as the
	// host having slept (the heartbeat ticks every ~30s).
	ghWatchSleepGap = 3 * time.Minute
)

// conditionSink is the part of *conditionAnnunciator the witness drives.
type conditionSink interface {
	Raise(c pogodCondition, now time.Time)
	Clear(id string, now time.Time)
}

// ghWatchWitness reads the gh-watch job's record on pogod's heartbeat.
type ghWatchWitness struct {
	home string
	// Which detectors pogod's config has enabled. The job reads the same
	// config, but pogod decides whether their silence is a fault.
	teardownEnabled, intakeEnabled, driftEnabled bool
	// Mailboxes: each not-armed condition goes to its detector's own reader
	// (A13's routing), the not-reporting one to the coordinator.
	teardownTo, intakeTo, coordinator string

	read func(home string) (ghwatch.File, error)

	mu          sync.Mutex
	lastTick    time.Time
	awakeSince  time.Time
	lastChecked time.Time
}

func (w *ghWatchWitness) anyEnabled() bool {
	return w.teardownEnabled || w.intakeEnabled || w.driftEnabled
}

// Check is called on every heartbeat tick.
func (w *ghWatchWitness) Check(conds conditionSink, now time.Time) {
	if w == nil || conds == nil {
		return
	}
	w.mu.Lock()
	if w.lastTick.IsZero() || now.Sub(w.lastTick) > ghWatchSleepGap {
		w.awakeSince = now
	}
	w.lastTick = now
	if !w.lastChecked.IsZero() && now.Sub(w.lastChecked) < ghWatchCheckEvery {
		w.mu.Unlock()
		return
	}
	w.lastChecked = now
	awakeSince := w.awakeSince
	w.mu.Unlock()

	if !w.anyEnabled() {
		conds.Clear(rowA13GHWatchNotReporting, now)
		conds.Clear(rowA13TeardownNotArmed, now)
		conds.Clear(rowA13IntakeNotArmed, now)
		conds.Clear(rowA13IntakeNoCred, now)
		return
	}

	read := w.read
	if read == nil {
		read = ghwatch.Read
	}
	f, err := read(w.home)

	// Not reporting: no record, an unreadable one, or a stale one — judged only
	// once pogod has itself been awake for the whole window.
	var stale string
	switch {
	case errors.Is(err, os.ErrNotExist):
		stale = fmt.Sprintf("no record at %s — the job has never completed a run on this host",
			ghwatch.StatePath(w.home))
	case err != nil:
		stale = fmt.Sprintf("record unreadable: %v", err)
	case now.Sub(f.FinishedAt) >= ghWatchStaleAfter:
		stale = fmt.Sprintf("last record finished %s (%s ago); the job fires every 15m",
			f.FinishedAt.UTC().Format(time.RFC3339), now.Sub(f.FinishedAt).Round(time.Minute))
	}
	switch {
	case stale == "":
		conds.Clear(rowA13GHWatchNotReporting, now)
	case now.Sub(awakeSince) >= ghWatchStaleAfter:
		conds.Raise(conditionGHWatchNotReporting(w.coordinator, stale), now)
	}
	if err != nil {
		// No record to judge arming from. Leave those conditions as they are:
		// asserting either way would be a claim nothing checked.
		return
	}

	// Arming, from the job's latest record.
	if w.teardownEnabled && f.Teardown.Arming == ghwatch.NoGHBinary {
		conds.Raise(conditionTeardownNotArmed(w.teardownTo, f.Teardown.ArmingDetail), now)
	} else {
		conds.Clear(rowA13TeardownNotArmed, now)
	}
	// Exactly one intake condition is live at once: without `gh` the credential
	// predicate is not measurable, so a missing binary clears the credential row
	// rather than leaving a stale one to suppress it later (mg-fb29).
	switch {
	case w.intakeEnabled && f.Intake.Arming == ghwatch.NoGHBinary:
		conds.Clear(rowA13IntakeNoCred, now)
		conds.Raise(conditionIntakeNotArmed(w.intakeTo, f.Intake.ArmingDetail), now)
	case w.intakeEnabled && f.Intake.Arming == ghwatch.NoCredential:
		conds.Clear(rowA13IntakeNotArmed, now)
		conds.Raise(conditionIntakeNoCredential(w.intakeTo, f.Intake.ArmingDetail), now)
	default:
		conds.Clear(rowA13IntakeNotArmed, now)
		conds.Clear(rowA13IntakeNoCred, now)
	}
}
