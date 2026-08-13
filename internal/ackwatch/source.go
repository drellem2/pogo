package ackwatch

import (
	"os"
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
			EverAcked:      e.EverAcked,
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
				Due:   detailTime(ev.Details, "original_due"),
				Fired: detailTime(ev.Details, "fired_at"),
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

// detailTime reads an RFC3339 timestamp detail, returning the zero time for
// absence or garbage. The scheduler writes original_due/fired_at with an offset
// rather than a Z (time.RFC3339 on a local clock), so this must not assume UTC.
//
// Zero is the correct failure value here and not merely a convenient one: every
// consumer of Due/Fired treats a missing pair as "not measured" rather than as
// "on time", so an unparseable stamp cannot manufacture either a late fire or
// an acquittal.
func detailTime(details map[string]any, key string) time.Time {
	s := detailString(details, key)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ReadFailureEpisodes reconstructs synthwatch's synthetic-failure episodes from
// logPath over [since, until), for SplitWithEpisodes to join against.
//
// An episode is a detected..cleared pair per agent. Repeated detections while
// one is already open do not open a second — synthwatch re-emits on every scan
// that still sees the agent failing, and treating each as a fresh episode would
// shatter one 4-hour outage into fifty overlapping slivers.
//
// An episode with no clear in the window comes back with a zero Until, which
// FailureEpisode.Covers reads as open-ended. That is deliberate: the alternative is to
// close it at the last event read, which would quietly acquit every fire after
// that point on the strength of having stopped looking.
func ReadFailureEpisodes(logPath string, since, until time.Time) ([]FailureEpisode, error) {
	// One flat, time-ordered stream of transitions. The two reads below are
	// separate scans (events.Filter carries a single Type), so they arrive
	// interleaved-by-type and must be re-sorted before they can be paired.
	type transition struct {
		at      time.Time
		agent   string
		cleared bool
	}
	var stream []transition

	for _, evType := range []string{"synthetic_failure_detected", "synthetic_failure_cleared"} {
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
			agent := detailString(ev.Details, "target")
			if agent == "" {
				continue
			}
			stream = append(stream, transition{
				at: at, agent: agent, cleared: evType == "synthetic_failure_cleared",
			})
		}
	}
	sort.SliceStable(stream, func(i, j int) bool { return stream[i].at.Before(stream[j].at) })

	open := map[string]time.Time{}
	var episodes []FailureEpisode
	for _, tr := range stream {
		from, isOpen := open[tr.agent]
		switch {
		case tr.cleared && isOpen:
			episodes = append(episodes, FailureEpisode{Agent: tr.agent, From: from, Until: tr.at})
			delete(open, tr.agent)
		case tr.cleared:
			// A clear with nothing open: the detection fell before `since`.
			// Dropped rather than back-dated — inventing a start would extend
			// an episode over fires we have no evidence were dark.
		case !isOpen:
			open[tr.agent] = tr.at
		}
	}
	for agent, from := range open {
		episodes = append(episodes, FailureEpisode{Agent: agent, From: from})
	}
	sort.SliceStable(episodes, func(i, j int) bool {
		if !episodes[i].From.Equal(episodes[j].From) {
			return episodes[i].From.Before(episodes[j].From)
		}
		return episodes[i].Agent < episodes[j].Agent
	})
	return episodes, nil
}

// RecentFires measures the fleet's fire traffic over the trailing window ending
// at now, for the ABSOLUTE (blackout) arm. It never returns an error: a window
// it could not read comes back as a Recent carrying Err, because a failed
// measurement that looked like a measurement of zero would be a false blackout,
// and one that looked like a clean scan would be the silence this package
// exists to end. Detect renders either as Report.BlackoutBlind.
//
// Why events rather than the persisted counters, given the counters are already
// in hand: they are lifetime totals and they are zeroed by re-registration,
// which the nightly redeploy guarantees. See the package header, "Why the
// blackout arm reads EVENTS rather than the counters".
func RecentFires(logPath string, now time.Time, window time.Duration) Recent {
	if window <= 0 {
		window = DefaultBlackoutWindow
	}
	out := Recent{Window: window}
	// A log that is not there has to be named as blindness HERE, because the
	// events layer deliberately treats a nonexistent path as "no events yet"
	// rather than as an error (see events.ScanFile). Left to the deliveries
	// floor, an absent log would report itself as "only 0 fires delivered",
	// which reads as a quiet fleet rather than as an arm that cannot look.
	if _, statErr := os.Stat(logPath); statErr != nil {
		out.Err = "scheduler event log unreadable: " + statErr.Error()
		return out
	}
	evs, err := ReadFireTimeline(logPath, now.Add(-window), now)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	schedules := map[string]bool{}
	agents := map[string]bool{}
	perAgentSchedules := map[string]map[string]bool{}
	byAgent := map[string]AgentFires{}
	bySchedule := map[string]ScheduleFires{}
	for _, ev := range evs {
		f := byAgent[ev.Agent]
		key := scheduleKey(ev.Agent, ev.ID)
		s := bySchedule[key]
		switch ev.Kind {
		case FireDelivered:
			out.Delivered++
			schedules[key] = true
			if ev.Agent != "" {
				agents[ev.Agent] = true
			}
			f.Delivered++
			s.Delivered++
			if perAgentSchedules[ev.Agent] == nil {
				perAgentSchedules[ev.Agent] = map[string]bool{}
			}
			perAgentSchedules[ev.Agent][ev.ID] = true
		case FireCompleted:
			out.Completed++
			f.Completed++
			s.Completed++
			if ev.At.After(s.LastCompletedAt) {
				s.LastCompletedAt = ev.At
			}
			if ev.At.After(out.LastCompletedAt) {
				out.LastCompletedAt = ev.At
			}
		}
		byAgent[ev.Agent] = f
		bySchedule[key] = s
	}
	out.BySchedule = bySchedule
	for a, ids := range perAgentSchedules {
		f := byAgent[a]
		f.Schedules = len(ids)
		byAgent[a] = f
	}
	out.ByAgent = byAgent
	out.Schedules = len(schedules)
	out.Agents = make([]string, 0, len(agents))
	for a := range agents {
		out.Agents = append(out.Agents, a)
	}
	sort.Strings(out.Agents)
	return out
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
