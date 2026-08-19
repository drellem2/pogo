package scheduler

// The READER for a one-shot's outcome (mg-8011), and the reason vocabulary it
// shares with the writer.
//
// mg-64e6 made a one-shot's completion recordable: a fired one-shot is retained
// until it is acked or its ack window closes, and the single misleading
// `one_shot_complete` became four honest labels. It stopped there, deliberately
// and correctly — the ticket asked for the record, not for a consumer. So the
// distinction existed and nothing read it, which from a human's seat is the
// original failure unchanged: a one-shot that fires into a dead, wedged or
// zero-token agent writes `one_shot_unacked` into events.log and produces no
// alarm, no row, no digest line.
//
// That population is exactly the fires that happen ONCE and are never retried —
// post-redeploy verification, pre-deploy steps, `revision-check-post-0300`. They
// have no next cycle to catch a silent no-op. `verify-absentwatch-live-mayor` is
// the specimen: it carried mg-7d20's owed post-redeploy verification, fired at
// 02:21:00.012Z into a mayor that happened to be alive, and had it not been, the
// record would have been indistinguishable from success.
//
// # Why the reader lives in this package
//
// Because the writer does. A consumer in another package would hard-code the
// four reason strings a second time, and a rename on the emitting side would
// silently stop matching — a reader that quietly matches nothing is the same
// shape of defect as a record nobody reads, one level along. The reason
// constants below are the single vocabulary, and
// TestReadOneShotOutcomes_ReadsWhatTheSchedulerWrote drives the real scheduler
// through all four outcomes and reads them back through these constants — so a
// rename that desynced the two ends would fail rather than quietly match
// nothing.
//
// # What it deliberately does NOT do
//
// It is not a detector and does not join ackwatch's cohort. ackwatch gates on
// `Cadence <= 0`, which excludes every one-shot (ackwatch.go), and that gate is
// load-bearing there: its whole model is a delivered:completed RATIO over
// repeated fires, which a schedule that fires once cannot have. Bringing
// one-shots into that cohort is a decision someone should take on its own
// evidence, not a wiring detail smuggled in by a consumer.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// The reasons a one-shot can leave the live set. These are the strings written
// into `schedule_removed`.details.reason and they are the vocabulary this file
// reads back; see the package header for why both ends share them.
const (
	// ReasonOneShotAcked — the agent redeemed the token. A live model turn ran
	// a tool to say the work was done. This is the only one of the four that
	// carries evidence of the obligation being met.
	ReasonOneShotAcked = "one_shot_acked"
	// ReasonOneShotUnacked — reaped at AckStaleWindow with the token never
	// redeemed. The fire went out; nobody answered for a full day.
	ReasonOneShotUnacked = "one_shot_unacked"
	// ReasonOneShotUndelivered — delivery itself failed, so no turn ran.
	ReasonOneShotUndelivered = "one_shot_undelivered"
	// ReasonOneShotSkipped — the fire was elided by the replay policy (a stale
	// catch-up under `--replay skip`). Reported separately because for that
	// policy the elision is the configured intent, not a miss.
	ReasonOneShotSkipped = "one_shot_skipped"
	// ReasonOneShotComplete is RETIRED (mg-64e6) and must never be emitted
	// again — TestOneShotCompleteLabelIsGone fails if it returns. It is
	// named here for one reason: a reader that finds it in the window is
	// looking at records written by a binary that PREDATES the split, and must
	// say so rather than report a clean zero. See OneShotReport.Legacy.
	ReasonOneShotComplete = "one_shot_complete"
)

// OneShotOutcome is one one-shot's departure from the live set, with the
// identity of the obligation it carried.
//
// Identity is the entire value of this class, which is why Message is here.
// "1 unacked one-shot" is not actionable; "verify-absentwatch-live-mayor, fired
// 02:21, nobody answered" is. And an id is not always enough on its own —
// `pogo schedule --once` without an explicit `--id` generates `sch-<hex>`, which
// names nothing, so for those the message is the only thing that says what was
// missed.
type OneShotOutcome struct {
	Reason   string    `json:"reason"`
	ID       string    `json:"schedule_id"`
	Agent    string    `json:"agent"`
	Kind     string    `json:"kind,omitempty"`
	Message  string    `json:"message,omitempty"`
	Delivery string    `json:"delivery,omitempty"`
	Fired    time.Time `json:"fired_at,omitempty"`
	Removed  time.Time `json:"removed_at"`
	Error    string    `json:"error,omitempty"`
}

// Unanswered reports whether this outcome is an obligation that nobody met: the
// fire went out and no turn redeemed it, or it never went out at all.
func (o OneShotOutcome) Unanswered() bool {
	return o.Reason == ReasonOneShotUnacked || o.Reason == ReasonOneShotUndelivered
}

// Waited is how long the fire sat unanswered, when both ends are known.
// Zero means the delivery record was not found in the scanned window, which is
// reported as "not measured" rather than as zero elapsed.
func (o OneShotOutcome) Waited() time.Duration {
	if o.Fired.IsZero() || o.Removed.IsZero() {
		return 0
	}
	return o.Removed.Sub(o.Fired)
}

// OneShotReport is what a window of events.log says about one-shot obligations.
type OneShotReport struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until,omitempty"`

	// Unanswered is the finding: one-shots that went unacked or undelivered,
	// oldest first.
	Unanswered []OneShotOutcome `json:"unanswered"`
	// Skipped is the replay-policy elisions, kept apart from the finding.
	Skipped []OneShotOutcome `json:"skipped,omitempty"`
	// Answered is the one-shots whose work was reported done. Kept as a list
	// rather than a count for the same reason Unanswered is: it is the
	// denominator that stops an empty Unanswered from reading as "no one-shots
	// ran", and a reader checking whether a specific obligation was met needs
	// to see it named on the answered side too.
	Answered []OneShotOutcome `json:"answered,omitempty"`
	// Fires counts one-shot deliveries seen in the window. A one-shot that
	// fired recently and has not yet been acked or reaped is in Fires and in
	// none of the outcome buckets — it is still in flight, not missing.
	Fires int `json:"fires"`

	// Legacy counts retired `one_shot_complete` records in the window, and
	// LegacyLast is the newest of them. Non-zero means the binary that wrote
	// this window predates mg-64e6, so it could not have emitted the labels
	// this reader looks for and a zero Unanswered proves nothing. Reporting a
	// clean bill of health from a log whose writer cannot express the failure
	// is the confusion class of mg-afd0 / mg-3141, and it is the one thing this
	// reader must not do.
	Legacy     int       `json:"legacy_complete"`
	LegacyLast time.Time `json:"legacy_last,omitempty"`

	// Coverage. Oldest is the oldest record seen across the files scanned, and
	// Spilled says whether rotation may have discarded records before it — the
	// difference between "the log starts here" and "the log was cut off here".
	Oldest  time.Time `json:"oldest_record,omitempty"`
	Spilled bool      `json:"spilled"`
	Files   []string  `json:"files"`
}

// Total is every one-shot outcome recorded in the window.
func (r OneShotReport) Total() int {
	return len(r.Answered) + len(r.Unanswered) + len(r.Skipped) + r.Legacy
}

// WriterPredatesLabels reports whether the window contains evidence that the
// binary writing it could not emit the acked/unacked split. A caller must say so
// out loud instead of rendering an all-clear.
func (r OneShotReport) WriterPredatesLabels() bool { return r.Legacy > 0 }

// ReadOneShotOutcomes reads one-shot outcomes out of the events log at logPath,
// restricted to removals in [since, until). A zero until means "to the end of
// the log".
//
// The delivery join is deliberately NOT windowed. A one-shot is reaped
// AckStaleWindow after its fire, so a removal at the leading edge of any window
// shorter than a day has its `scheduler_fire_delivered` outside that window; a
// windowed join would report the reap with no fire time and understate the wait
// to zero. Every one-shot delivery in the scanned files is kept instead (one
// entry per (agent, id), the last one wins) and only removals are filtered.
func ReadOneShotOutcomes(logPath string, since, until time.Time) (OneShotReport, error) {
	rep := OneShotReport{Since: since, Until: until, Spilled: events.LogSpilled(logPath)}

	// A LOG THAT IS NOT THERE IS AN ERROR, not an empty window — and this is the
	// spot where this reader could most easily commit the defect it exists to
	// close. events.ScanFile treats a missing file as "no events yet" and returns
	// (nil, nil), so an unresolvable POGO_HOME or a renamed log would produce a
	// clean, confident "no one-shot fired or was reaped" from a run that opened
	// nothing at all. Callers must be able to tell that from a quiet week.
	if strings.TrimSpace(logPath) == "" {
		return rep, fmt.Errorf("no events log path: the scheduler root could not be resolved")
	}
	files := scanFilesCovering(logPath, since.Add(-2*AckStaleWindow))
	if len(files) == 0 {
		return rep, fmt.Errorf("no events log at %s", logPath)
	}
	rep.Files = files

	type fireKey struct{ agent, id string }
	fired := map[fireKey]time.Time{}
	var removals []OneShotOutcome

	for _, f := range files {
		err := events.ScanFile(f, func(ev events.Event) {
			written, perr := time.Parse(time.RFC3339Nano, ev.Timestamp)
			if perr != nil {
				return
			}
			// Coverage is a property of the LOG, so it is measured off the
			// record's own write stamp; everything else below is measured off
			// the scheduler's stamp in the details. See eventTime.
			if rep.Oldest.IsZero() || written.Before(rep.Oldest) {
				rep.Oldest = written
			}
			if !detailBool(ev.Details, "one_shot") {
				return
			}
			switch ev.EventType {
			case "scheduler_fire_delivered":
				at := eventTime(ev.Details, "fired_at", written)
				k := fireKey{detailString(ev.Details, "to"), detailString(ev.Details, "schedule_id")}
				fired[k] = at
				if inWindow(at, since, until) {
					rep.Fires++
				}
			case "schedule_removed":
				at := eventTime(ev.Details, "removed_at", written)
				if !inWindow(at, since, until) {
					return
				}
				removals = append(removals, OneShotOutcome{
					Reason:   detailString(ev.Details, "reason"),
					ID:       detailString(ev.Details, "schedule_id"),
					Agent:    detailString(ev.Details, "to"),
					Kind:     detailString(ev.Details, "kind"),
					Message:  detailString(ev.Details, "message"),
					Delivery: detailString(ev.Details, "delivery"),
					Removed:  at,
					Error:    detailString(ev.Details, "error"),
				})
			}
		})
		if err != nil {
			// An events log we cannot read is not an empty measurement. The
			// caller must be able to tell "nothing was missed" from "nothing
			// was looked at".
			return rep, fmt.Errorf("read %s: %w", f, err)
		}
	}

	for _, o := range removals {
		switch o.Reason {
		case ReasonOneShotAcked:
			o.Fired = fired[fireKey{o.Agent, o.ID}]
			rep.Answered = append(rep.Answered, o)
		case ReasonOneShotComplete:
			rep.Legacy++
			if o.Removed.After(rep.LegacyLast) {
				rep.LegacyLast = o.Removed
			}
		case ReasonOneShotSkipped:
			rep.Skipped = append(rep.Skipped, o)
		case ReasonOneShotUnacked, ReasonOneShotUndelivered:
			o.Fired = fired[fireKey{o.Agent, o.ID}]
			rep.Unanswered = append(rep.Unanswered, o)
		default:
			// Some other removal of a one-shot (explicit_rm, agent_gone,
			// cron_unparseable). Not an unanswered obligation: somebody or
			// something took it out of the live set on purpose, and it is not
			// this reader's business to editorialize about that.
		}
	}
	sort.SliceStable(rep.Unanswered, func(i, j int) bool {
		return rep.Unanswered[i].Removed.Before(rep.Unanswered[j].Removed)
	})
	sort.SliceStable(rep.Skipped, func(i, j int) bool {
		return rep.Skipped[i].Removed.Before(rep.Skipped[j].Removed)
	})
	sort.SliceStable(rep.Answered, func(i, j int) bool {
		return rep.Answered[i].Removed.Before(rep.Answered[j].Removed)
	})
	return rep, nil
}

// inWindow applies [since, until). since is inclusive here, unlike
// events.Filter's exclusive SinceMin, because a caller asking for "the last 7
// days" means the whole of them.
func inWindow(at, since, until time.Time) bool {
	if !since.IsZero() && at.Before(since) {
		return false
	}
	if !until.IsZero() && !at.Before(until) {
		return false
	}
	return true
}

// scanFilesCovering returns the events-log files that can contain records at or
// after floor, newest-last, so a caller reads them in time order.
//
// The walk itself lives in internal/events (LogFilesCovering) rather than here:
// the first caller to need it was this one, the second was the first-turn floor,
// and a second copy of it was how that floor came to read only the live log
// (mg-9d55). This wrapper stays so the call sites below read as before.
func scanFilesCovering(logPath string, floor time.Time) []string {
	return events.LogFilesCovering(logPath, floor)
}

// oneShotMessageDigestLimit bounds the message digest carried on a one-shot's
// removal record. Long enough for a one-shot's actual payload (a verification
// instruction, a gate-lift note) to be recognizable; short enough that the
// record stays one scannable line.
const oneShotMessageDigestLimit = 200

// digestMessage collapses a schedule message to a single scannable line for the
// removal record. Whitespace runs become single spaces — a scheduler message is
// routinely multi-line, and a raw newline in a detail value turns one JSONL
// record into something a line-oriented reader mis-splits.
//
// Truncation is rune-aware: cutting a UTF-8 sequence mid-way would emit invalid
// bytes into a log every consumer parses as JSON.
func digestMessage(msg string) string {
	d := strings.Join(strings.Fields(msg), " ")
	if len([]rune(d)) <= oneShotMessageDigestLimit {
		return d
	}
	return string([]rune(d)[:oneShotMessageDigestLimit]) + "…"
}

// eventTime prefers the scheduler's own stamp in the details over the record's
// write stamp, falling back to the latter when the detail is absent or garbage.
//
// The two differ by microseconds in production and the fallback is what makes
// an older record — written before a stamp existed — still land in the right
// window rather than being dropped. Preferring the detail matters for the
// elapsed times this report prints: `fired_at` and `removed_at` are what the
// scheduler MEANT, and a fire's wait is the distance between those two, not
// between two file appends.
func eventTime(details map[string]any, key string, fallback time.Time) time.Time {
	if t := detailTime(details, key); !t.IsZero() {
		return t
	}
	return fallback
}

// detailTime reads an RFC3339 stamp from a detail, returning the zero time for
// absence or garbage — never a guess, so a caller can tell "not recorded" from
// a real time.
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

func detailString(details map[string]any, key string) string {
	if details == nil {
		return ""
	}
	s, _ := details[key].(string)
	return s
}

func detailBool(details map[string]any, key string) bool {
	if details == nil {
		return false
	}
	b, _ := details[key].(bool)
	return b
}
