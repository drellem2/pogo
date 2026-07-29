package ackwatch

import (
	"sort"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/scheduler"
)

// This file is the ONLY place ackwatch touches live pogo state. The detector in
// ackwatch.go takes a Snapshot and nothing else, so every test in this package
// builds fixtures by hand — mg-6092, mg-e8e7 and mg-5336 are three separate
// tickets for tests that read the developer's live ~/.pogo, and this package
// does not add a fourth.

// SampleEntries converts scheduler entries into detector samples. cadence is
// computed relative to ref because a cron's interval is only well-defined
// between two concrete firings (see scheduler.Entry.CronInterval).
func SampleEntries(entries []scheduler.Entry, ref time.Time) []Sample {
	out := make([]Sample, 0, len(entries))
	for _, e := range entries {
		kind := string(e.Kind)
		if kind == "" {
			kind = string(scheduler.KindOther)
		}
		out = append(out, Sample{
			Agent:          e.Agent,
			ID:             e.ID,
			Kind:           kind,
			Cadence:        e.CronInterval(ref),
			CreatedAt:      e.CreatedAt,
			FiresDelivered: e.FiresDelivered,
			FiresCompleted: e.FiresCompleted,
			UnackedStreak:  e.UnackedStreak,
			LastCompletion: e.LastCompletion,
		})
	}
	return out
}

// ReadFireTimeline reads the delivery/completion timeline out of logPath,
// restricted to [since, until). A zero until means "up to the end of the log".
//
// The persisted counters cannot answer the population question, so this is the
// one reader that must exist: see the header of populations.go for why (the
// short version is that a re-registration zeroes the counters, and the nightly
// redeploy guarantees one, so a storm's deficit is erased by the restart that
// follows it and only the events log retains it).
//
// Two reads rather than one unfiltered read: events.Filter carries a single
// Type, and this log is tens of megabytes on a live box, most of it neither
// event.
func ReadFireTimeline(logPath string, since, until time.Time) ([]FireEvent, error) {
	var out []FireEvent
	for kind, evType := range map[FireEventKind]string{
		FireDelivered: "scheduler_fire_delivered",
		FireCompleted: "scheduler_fire_completed",
	} {
		evs, err := events.ReadFiltered(logPath, events.Filter{SinceMin: since, Type: evType})
		if err != nil {
			return nil, err
		}
		for _, ev := range evs {
			at, perr := time.Parse(time.RFC3339Nano, ev.Timestamp)
			if perr != nil {
				continue
			}
			if !until.IsZero() && !at.Before(until) {
				continue
			}
			out = append(out, FireEvent{
				At:    at,
				Kind:  kind,
				Agent: detailString(ev.Details, "to"),
				ID:    detailString(ev.Details, "schedule_id"),
				Token: detailString(ev.Details, "fire_token"),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// detailString reads a string detail, tolerating absence and a non-string
// value. A malformed line must not abort a measurement over a whole log.
func detailString(details map[string]any, key string) string {
	if details == nil {
		return ""
	}
	s, _ := details[key].(string)
	return s
}

// DisruptionWindow is how far back LastDisruption looks for a suppressing
// event. It only has to exceed the detector's SettleAfter — an older wake
// cannot suppress anything, so reading further back is wasted I/O.
const DisruptionWindow = 2 * time.Hour

// DisruptionEventType is the event a wake writes. Post-sleep replay makes
// stale acks expected, which is exactly why the mayor's stall-watch rules
// already check for a recent one before nudging or restarting anything.
const DisruptionEventType = "system_wake"

// LastDisruption returns the most recent system_wake in logPath, and a label
// for it, or the zero time when there is none inside DisruptionWindow.
//
// This is one of the two known-benign events that make the completion table
// unrepresentative. The other — a redeploy or restart, after which agents
// re-register their mail-checks and zero their counters (mg-42ac made it
// nightly) — is supplied by the caller as a start time, because no event
// records it and the process that restarted knows perfectly well when it did.
// Both feed the SAME suppression: see Snapshot.LastDisruption and
// Options.StartedAt. The per-sample CreatedAt gate in Detect covers a single
// schedule that re-registered on its own; these two cover the fleet-wide case,
// where the RELATIONSHIPS between schedules are what became untrustworthy.
//
// A missing or unreadable log yields a zero time — no suppression. That fails
// toward alerting rather than toward silence, which is the correct direction
// for a detector whose entire premise is that silence hid a fault for a week.
func LastDisruption(logPath string, now time.Time) (time.Time, string) {
	evs, err := events.ReadFiltered(logPath, events.Filter{
		SinceMin: now.Add(-DisruptionWindow),
		Type:     DisruptionEventType,
	})
	if err != nil {
		return time.Time{}, ""
	}
	var latest time.Time
	for _, ev := range evs {
		ts, perr := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if perr != nil {
			continue
		}
		if ts.After(latest) {
			latest = ts
		}
	}
	if latest.IsZero() {
		return time.Time{}, ""
	}
	return latest, DisruptionEventType
}
