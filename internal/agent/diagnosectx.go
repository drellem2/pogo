package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// EventContext is one fact diagnose read from events.log to put BESIDE a
// verdict — never under it. Both its uses (drellem2/pogo#232, #236) are
// context only, and that is a rule, not a phase:
//
//   - LastMailCheckRemoval sits under an undeclared no_mail_loop. It must not
//     SUPPRESS the RED: reason=explicit_rm is also what `pogo agent park` and
//     any `schedule rm` emit, so "someone removed it" is not "someone meant
//     this agent to have none". Only a declaration says that.
//   - LastWakeUnconsumed sits under a pointer-wake declaration. Making it a
//     Health input is wakewatch phase 3's job (mg-a2ff).
//
// Absence is bounded by coverage. events.log rotates (100MB x 5) and this
// reader scans only the live file, so "not seen" means "not seen since
// ScannedSince" — the render says so, and a reader must not take it for
// "never happened".
type EventContext struct {
	// Seen is true when a matching record was found.
	Seen bool `json:"seen"`
	// At is the matching record's time (the latest one).
	At time.Time `json:"at,omitzero"`
	// Reason is the record's reason (schedule_removed) or trigger
	// (wake_unconsumed).
	Reason string `json:"reason,omitempty"`
	// Detail is a short qualifier: the message or work item that went
	// unconsumed, for wake_unconsumed.
	Detail string `json:"detail,omitempty"`
	// ScannedSince is the oldest record the scan read: the floor of what
	// Seen=false can claim.
	ScannedSince time.Time `json:"scanned_since,omitzero"`
	// Error is set when the log could not be read; Seen is then meaningless.
	Error string `json:"error,omitempty"`
}

// Describe renders the context for a terminal, coverage included.
func (c *EventContext) Describe() string {
	if c == nil {
		return "not reported"
	}
	if c.Error != "" {
		return "could not read events.log: " + c.Error
	}
	if !c.Seen {
		if c.ScannedSince.IsZero() {
			return "none seen (events.log is empty)"
		}
		return "none seen in events.log since " + c.ScannedSince.UTC().Format(time.RFC3339)
	}
	var b strings.Builder
	b.WriteString(c.At.UTC().Format(time.RFC3339))
	var parts []string
	if c.Reason != "" {
		parts = append(parts, c.Reason)
	}
	if c.Detail != "" {
		parts = append(parts, c.Detail)
	}
	if len(parts) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
	}
	return b.String()
}

// DiagnoseEventContext supplies diagnose's events.log context. pogod backs it
// with EventLogContext; a nil provider omits the context, which is the default
// and what unit tests use.
type DiagnoseEventContext interface {
	// LastScheduleRemoval returns the latest schedule_removed for scheduleID.
	LastScheduleRemoval(scheduleID string) EventContext
	// LastWakeUnconsumed returns the latest wake_unconsumed whose recipient is
	// agentName.
	LastWakeUnconsumed(agentName string) EventContext
}

// EventLogContext reads diagnose's context from an events.log. It scans the
// LIVE file only: a full parse of every retained chunk costs seconds, and this
// runs on an interactive command. EventContext.ScannedSince carries the
// resulting floor so the reading stays honest about it.
type EventLogContext struct {
	Path string
}

// LastScheduleRemoval implements DiagnoseEventContext.
func (l EventLogContext) LastScheduleRemoval(scheduleID string) EventContext {
	return l.scan(func(ev events.Event, c *EventContext) {
		if ev.EventType != "schedule_removed" || detailStr(ev.Details, "schedule_id") != scheduleID {
			return
		}
		c.Seen = true
		c.At = eventAt(ev, "removed_at")
		c.Reason = "reason=" + detailStr(ev.Details, "reason")
	})
}

// LastWakeUnconsumed implements DiagnoseEventContext.
func (l EventLogContext) LastWakeUnconsumed(agentName string) EventContext {
	return l.scan(func(ev events.Event, c *EventContext) {
		if ev.EventType != "wake_unconsumed" || detailStr(ev.Details, "recipient") != agentName {
			return
		}
		c.Seen = true
		c.At = eventAt(ev, "")
		c.Reason = detailStr(ev.Details, "trigger")
		c.Detail = ""
		if id := detailStr(ev.Details, "msg_id"); id != "" {
			c.Detail = "mail " + id
		} else if ev.WorkItemID != "" {
			c.Detail = "item " + ev.WorkItemID
		}
	})
}

// scan walks the live log once; visit overwrites c for each match, so the
// last (latest, the log being append-ordered) wins.
func (l EventLogContext) scan(visit func(events.Event, *EventContext)) EventContext {
	var c EventContext
	if strings.TrimSpace(l.Path) == "" {
		c.Error = "no events log path"
		return c
	}
	err := events.ScanFile(l.Path, func(ev events.Event) {
		if c.ScannedSince.IsZero() {
			if t, perr := time.Parse(time.RFC3339Nano, ev.Timestamp); perr == nil {
				c.ScannedSince = t
			}
		}
		visit(ev, &c)
	})
	if err != nil {
		c.Error = err.Error()
	}
	return c
}

// eventAt prefers a timestamp the emitter put in details (the scheduler's own
// clock), falling back to the record's write stamp.
func eventAt(ev events.Event, key string) time.Time {
	if key != "" {
		if t, err := time.Parse(time.RFC3339, detailStr(ev.Details, key)); err == nil {
			return t
		}
	}
	t, _ := time.Parse(time.RFC3339Nano, ev.Timestamp)
	return t
}

func detailStr(d map[string]any, key string) string {
	if v, ok := d[key].(string); ok {
		return v
	}
	return ""
}
