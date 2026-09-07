package heartwatch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Runner defaults.
const (
	// DefaultInterval is how often the runner samples. The heartbeat it reads
	// is refreshed every ten minutes, so a five-minute tick never misses a
	// transition by more than half a cadence.
	DefaultInterval = 5 * time.Minute
	// DefaultHoldDown is how long a red reading must persist, unbroken, before
	// it is announced. The reading already carries a 90-minute threshold; this
	// is on top of it, and exists only to absorb a single missed mail-check at
	// exactly the wrong moment.
	DefaultHoldDown = 10 * time.Minute
	// DefaultRenotifyAfter is how long an UNCHANGED roster stays quiet.
	DefaultRenotifyAfter = 6 * time.Hour
	// DefaultHumanBox receives a finding the coordinator cannot be told about.
	DefaultHumanBox = "human"

	EventFinding = "heart_watch_finding"
	EventClear   = "heart_watch_clear"
	EventError   = "heart_watch_error"
	EventSkipped = "heart_watch_skipped"

	mailFrom = "heart-watch"
)

// ScanFunc produces the joined reading. Required.
type ScanFunc func(now time.Time) (Report, error)

// MailFunc delivers a notice.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	Enabled bool
	// Scan produces the reading. Required.
	Scan ScanFunc
	// Mail delivers notices. Required — a detector that cannot report is
	// precisely the thing this package exists to stop existing.
	Mail MailFunc
	// Emit writes heart_watch_* events. Defaults to events.Emit.
	Emit Emitter

	Interval time.Duration
	// HoldDown is how long a red reading must persist before it is announced.
	// Zero means DefaultHoldDown; NEGATIVE disables it. Zero and negative must
	// differ, or a config that omits the key would silently turn it off.
	HoldDown      time.Duration
	RenotifyAfter time.Duration

	// Coordinator is the agent whose findings must NOT be mailed to it.
	Coordinator string
	// HumanBox receives coordinator findings. Empty means DefaultHumanBox.
	HumanBox string
	// StartedAt suppresses reporting for one hold-down after pogod starts: a
	// restart bounces the fleet and every agent legitimately has a stale
	// heartbeat for a while afterwards.
	StartedAt time.Time
}

// Watcher rides pogod's heartbeat and reads the crew's sweep.log mtimes.
//
// It rides the heartbeat rather than a launchd timer for the same reason
// ackwatch, deafwatch, absentwatch and turnwatch do: the nondemand-spawn wedge
// on this box (mg-50e0) leaves launchd timers silently never firing — which for
// a detector of things that silently stopped happening would be especially apt.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	holdDown      time.Duration
	renotifyAfter time.Duration
	coordinator   string
	humanBox      string
	startedAt     time.Time

	scan ScanFunc
	mail MailFunc
	emit Emitter

	mu      sync.Mutex
	lastRun time.Time
	ran     bool
	// sinceRed is when each agent was first observed red in the current
	// unbroken run. An agent that recovers is deleted, so a flap restarts the
	// hold-down instead of accumulating toward it.
	sinceRed   map[string]time.Time
	lastPrint  string
	lastMailed time.Time
	// toldHuman records whether the human box was ever included in this
	// episode, so the all-clear reaches everyone who was alarmed. A clear that
	// goes to fewer mailboxes than the alarm leaves someone holding an open
	// incident forever.
	toldHuman bool
}

// New builds a Watcher, applying defaults for zero-valued options.
func New(opts Options) *Watcher {
	emit := opts.Emit
	if emit == nil {
		emit = func(e events.Event) { events.Emit(context.Background(), e) }
	}
	pick := func(v, def time.Duration) time.Duration {
		if v <= 0 {
			return def
		}
		return v
	}
	window := func(v, def time.Duration) time.Duration {
		switch {
		case v == 0:
			return def
		case v < 0:
			return 0
		default:
			return v
		}
	}
	humanBox := opts.HumanBox
	if humanBox == "" {
		humanBox = DefaultHumanBox
	}
	return &Watcher{
		enabled:       opts.Enabled,
		interval:      pick(opts.Interval, DefaultInterval),
		holdDown:      window(opts.HoldDown, DefaultHoldDown),
		renotifyAfter: pick(opts.RenotifyAfter, DefaultRenotifyAfter),
		coordinator:   opts.Coordinator,
		humanBox:      humanBox,
		startedAt:     opts.StartedAt,
		scan:          opts.Scan,
		mail:          opts.Mail,
		emit:          emit,
		sinceRed:      map[string]time.Time{},
	}
}

// Check runs one sample subject to the throttle. It is the integration point
// for pogod's heartbeat OnTick and a no-op on all but the first tick of each
// interval.
func (w *Watcher) Check(now time.Time) {
	if w == nil || !w.enabled || w.scan == nil || w.mail == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	if !w.due(now) {
		return
	}
	w.sample(now)
}

// due reports whether the interval has elapsed, recording now BEFORE the sample
// runs so a slow or failing sample still consumes its slot.
func (w *Watcher) due(now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ran && now.Sub(w.lastRun) < w.interval {
		return false
	}
	w.lastRun = now
	w.ran = true
	return true
}

func (w *Watcher) sample(now time.Time) {
	if !w.startedAt.IsZero() && w.holdDown > 0 && now.Sub(w.startedAt) < w.holdDown {
		return
	}

	rep, err := w.scan(now)
	if err != nil {
		// A fleet that could not be read is a real failure, not a clean scan,
		// and it goes on the event spine so a blind detector stays
		// distinguishable from a quiet one.
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Details:   map[string]any{"error": err.Error()},
		})
		return
	}

	if rep.Examined == 0 {
		// Zero examined produces zero findings, which is the exact shape of
		// green that hid the outage this package was filed for. It is an
		// instrument failure, never a clear — and in particular it must not
		// close an open episode.
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Details: map[string]any{
				"error": "0 agents examined — an empty population is not a clean fleet; " +
					"the registry returned nobody to look up a heartbeat for",
			},
		})
		return
	}

	if rep.WakeSuppressed {
		// mayor.md §3a's suppression, in code: after a host sleep the
		// schedules are still replaying and a stale heartbeat is expected.
		// The READING was still taken and is still in the report; only the
		// announcement is held.
		w.emit(events.Event{
			EventType: EventSkipped,
			Agent:     "pogod",
			Details: map[string]any{
				"why":      "a system_wake landed inside the wake grace; post-sleep schedule replay makes a stale heartbeat expected",
				"woke_at":  rep.WokeAt.UTC().Format(time.RFC3339),
				"findings": rep.Findings,
				"examined": rep.Examined,
			},
		})
		return
	}

	confirmed := w.observe(rep, now)
	if len(confirmed) == 0 {
		// The all-clear is gated on the READING having no findings at all, not
		// on this sample having none PAST THE HOLD-DOWN. Those differ, and the
		// difference is a false all-clear: one agent recovers while another goes
		// late inside the same interval, `confirmed` is empty because the new
		// one is still in its hold-down, and a clear saying "every heartbeat
		// fresh again" goes out over a fleet with a stale heartbeat in it. That
		// is this ticket's own defect committed by its own remedy — an
		// instrument whose unknown resolves to green.
		if rep.Findings == 0 {
			w.clear(rep, now)
		}
		return
	}
	w.announce(rep, confirmed, now)
}

// observe folds one reading into the hold-down state and returns the findings
// old enough to announce.
func (w *Watcher) observe(rep Report, now time.Time) []State {
	w.mu.Lock()
	defer w.mu.Unlock()

	seen := map[string]bool{}
	var confirmed []State
	for _, s := range rep.Agents {
		if !s.Verdict.Finding() {
			continue
		}
		seen[s.Agent] = true
		at, known := w.sinceRed[s.Agent]
		if !known {
			at = now
			w.sinceRed[s.Agent] = at
		}
		if now.Sub(at) >= w.holdDown {
			confirmed = append(confirmed, s)
		}
	}
	for name := range w.sinceRed {
		if !seen[name] {
			delete(w.sinceRed, name)
		}
	}
	// The coordinator sorts first when present. It is the hub: the only agent
	// whose failure hides itself from every other detector on this machine, and
	// the one a reader skimming a notice must see without scrolling.
	sort.Slice(confirmed, func(i, j int) bool {
		ci, cj := confirmed[i].Agent == w.coordinator, confirmed[j].Agent == w.coordinator
		if ci != cj {
			return ci
		}
		return confirmed[i].Agent < confirmed[j].Agent
	})
	return confirmed
}

func (w *Watcher) clear(rep Report, now time.Time) {
	w.mu.Lock()
	had := w.lastPrint != ""
	told := w.toldHuman
	w.lastPrint = ""
	w.lastMailed = time.Time{}
	w.toldHuman = false
	w.mu.Unlock()
	if !had {
		return
	}
	w.emit(events.Event{
		EventType: EventClear,
		Agent:     "pogod",
		Details: map[string]any{
			"examined": rep.Examined,
			"fresh":    rep.Fresh,
		},
	})
	subject := fmt.Sprintf("heart-watch: every heartbeat fresh again (%d/%d)", rep.Fresh, rep.Examined)
	body := fmt.Sprintf(
		"Every crew heartbeat this detector was reporting on is fresh again.\n\n"+
			"Examined %d agent(s) from pogod's registry: %d fresh, 0 stale, 0 past T_restart,\n"+
			"0 missing, 0 unreadable.\n\nRead it yourself: pogo check-heartbeats\n",
		rep.Examined, rep.Fresh)
	for _, to := range w.clearRecipients(told) {
		if err := w.mail(to, mailFrom, subject, body); err != nil {
			w.emit(events.Event{
				EventType: EventError,
				Agent:     "pogod",
				Details:   map[string]any{"error": err.Error(), "to": to, "phase": "clear"},
			})
		}
	}
}

// announce mails when the roster changed or the renotify interval elapsed.
func (w *Watcher) announce(rep Report, confirmed []State, now time.Time) {
	print := fingerprint(confirmed)

	w.mu.Lock()
	shouldMail := print != w.lastPrint || now.Sub(w.lastMailed) >= w.renotifyAfter
	if shouldMail {
		w.lastPrint = print
		w.lastMailed = now
	}
	w.mu.Unlock()
	if !shouldMail {
		return
	}

	coordinatorHit := false
	for _, s := range confirmed {
		if s.Agent == w.coordinator {
			coordinatorHit = true
		}
	}

	to := w.recipients(coordinatorHit)
	for _, box := range to {
		body := renderNotice(rep, confirmed, w.coordinator, box, w.humanBox, coordinatorHit, now)
		if err := w.mail(box, mailFrom, subject(confirmed, w.coordinator), body); err != nil {
			// The fault was detected and could not be reported. Record it: a
			// notice that reaches nobody is this ticket's bug, one level up.
			w.emit(events.Event{
				EventType: EventError,
				Agent:     "pogod",
				Details:   map[string]any{"error": err.Error(), "to": box},
			})
		}
	}
	if coordinatorHit {
		w.mu.Lock()
		w.toldHuman = true
		w.mu.Unlock()
	}

	names := make([]string, 0, len(confirmed))
	for _, s := range confirmed {
		names = append(names, s.Agent+":"+string(s.Verdict))
	}
	w.emit(events.Event{
		EventType: EventFinding,
		Agent:     "pogod",
		Details: map[string]any{
			"findings":         names,
			"coordinator_hit":  coordinatorHit,
			"examined":         rep.Examined,
			"notified":         to,
			"reader_is_pogod":  true,
			"routed_via_mayor": false,
		},
	})
}

// recipients applies the routing rule. It is a function rather than an inline
// branch so a test can assert it directly, and it is the only place in this
// package that decides who hears about what.
//
// When the coordinator is among the findings the notice goes to the human box
// and NOT to the coordinator. Delivering "your heartbeat has been stale for 14
// days" to the agent whose heartbeat is stale is a message that arrives only if
// the claim is false — the circularity this package exists to break, and it
// would read as working code.
func (w *Watcher) recipients(coordinatorHit bool) []string {
	if coordinatorHit || w.coordinator == "" {
		return []string{w.humanBox}
	}
	return []string{w.coordinator}
}

// clearRecipients mirrors recipients for the all-clear. An episode that ever
// reached the human box clears there too, and it also clears at the coordinator
// so the agent that may have been asked to act is told it is over.
func (w *Watcher) clearRecipients(toldHuman bool) []string {
	if w.coordinator == "" {
		return []string{w.humanBox}
	}
	if toldHuman && w.humanBox != w.coordinator {
		return []string{w.coordinator, w.humanBox}
	}
	return []string{w.coordinator}
}

func subject(confirmed []State, coordinator string) string {
	for _, s := range confirmed {
		if s.Agent == coordinator {
			if len(confirmed) == 1 {
				return fmt.Sprintf("%s's heartbeat is %s — nothing else on this machine can tell it so",
					coordinator, s.Verdict)
			}
			return fmt.Sprintf("%s's heartbeat is %s — and %d other(s)",
				coordinator, s.Verdict, len(confirmed)-1)
		}
	}
	if len(confirmed) == 1 {
		return fmt.Sprintf("%s heartbeat %s (%s)", confirmed[0].Agent, confirmed[0].Verdict,
			confirmed[0].Age().Round(time.Minute))
	}
	return fmt.Sprintf("%d crew heartbeats are not fresh", len(confirmed))
}

func fingerprint(confirmed []State) string {
	parts := make([]string, 0, len(confirmed))
	for _, s := range confirmed {
		parts = append(parts, s.Agent+"="+string(s.Verdict))
	}
	return strings.Join(parts, ",")
}

func renderNotice(rep Report, confirmed []State, coordinator, to, humanBox string, coordinatorHit bool, now time.Time) string {
	var b strings.Builder
	b.WriteString("Crew heartbeats are not fresh.\n\n")
	b.WriteString("Evidence: <agent dir>/" + HeartbeatFile + " mtime, refreshed by each agent's\n")
	b.WriteString("ten-minute mail-check. Read by POGOD, not by any crew agent — until mg-d616 the\n")
	b.WriteString("only executor of this check was a step in the coordinator's own loop, so it did\n")
	b.WriteString("not degrade when the coordinator stopped: it stopped. Two PMs then sat 14 days\n")
	b.WriteString("at ~168x T_restart with nothing firing.\n\n")

	for _, s := range confirmed {
		age := "never"
		if !s.Last.IsZero() {
			age = fmt.Sprintf("%s ago (%s)", s.Age().Round(time.Minute), s.Last.Format(time.RFC3339))
		}
		mark := ""
		if s.Agent == coordinator {
			mark = "  <- the coordinator"
		}
		fmt.Fprintf(&b, "  %-12s %-16s last heartbeat: %s%s\n", s.Verdict, s.Agent, age, mark)
		if s.Detail != "" {
			fmt.Fprintf(&b, "               %s\n", s.Detail)
		}
		if s.Path != "" {
			fmt.Fprintf(&b, "               %s\n", s.Path)
		}
		for _, p := range s.Searched {
			fmt.Fprintf(&b, "               searched: %s\n", p)
		}
	}

	fmt.Fprintf(&b, "\nExamined %d agent(s) from pogod's registry: %d fresh, %d stale (> %s),\n"+
		"%d past T_restart (> %s), %d missing, %d unreadable.\n",
		rep.Examined, rep.Fresh, rep.Stale, rep.StallAfter, rep.RestartDue, rep.RestartAfter,
		rep.Missing, rep.Bad)

	if coordinatorHit {
		fmt.Fprintf(&b, "\nWHY THIS CAME TO %s AND NOT TO %s: this check used to be a step in %s's\n",
			strings.ToUpper(to), strings.ToUpper(coordinator), coordinator)
		b.WriteString("own coordination loop and had no other executor, so it did not degrade when that\n")
		b.WriteString("agent stopped — it stopped. Mailing a coordinator that its own heartbeat is stale\n")
		b.WriteString("delivers the message only when the claim is false. That is the circularity, not a\n")
		b.WriteString("mistuned threshold, and it is why this reader lives in pogod.\n")
	}

	b.WriteString("\nBEFORE NUDGING OR RESTARTING ANYTHING:\n")
	b.WriteString("  pogo agent diagnose <name> --json | jq '{health, health_detail, restart_suppressed, transcript_check}'\n")
	b.WriteString("A stale heartbeat has two causes that look identical and take OPPOSITE responses.\n")
	b.WriteString("An agent failing every turn in ~10ms on an expired credential or a spend cap is not\n")
	b.WriteString("wedged: restarting it destroys the transcript that says so, and the replacement\n")
	b.WriteString("inherits the credential. On 2026-07-22 that distinction cost 23h30m, and the\n")
	b.WriteString("120-minute rule applied without it would have produced ~66 restarts that recovered\n")
	b.WriteString("nothing.\n")
	b.WriteString("\nA `missing` row is a true reading of a different fact: that agent's tier keeps no\n")
	b.WriteString("heartbeat, or its prompt predates the clause. Check its uptime before acting.\n")
	b.WriteString("\nRead the whole fleet yourself: pogo check-heartbeats\n")
	b.WriteString("Confirm this check can still go red: pogo check-heartbeats --probe\n")
	b.WriteString("\nREPORT-ONLY. Nothing was nudged, restarted or stopped.\n")
	return b.String()
}
