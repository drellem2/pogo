// Package turnwatch is the POGOD-RESIDENT reader of the fleet's
// turn-completion artifact (mg-a270, amendment of 2026-08-11 21:15).
//
// # WHY THE READER IS THE DELIVERABLE, NOT JUST THE ARTIFACT
//
// mg-a270 was filed as a coverage gap: three of five crew agents wrote no
// artifact a completed turn was needed to produce. Giving them one (see
// internal/turnlog) is necessary. For the COORDINATOR it is not sufficient, and
// the amendment is worth restating in full because the reasoning is not
// obvious:
//
//	Every fleet-wide scheduled check on this machine is mayor-owned —
//	mg-schedule-sweep, predeploy-quiesce, predeploy-stop-noncritical,
//	gate-lift. The only fleet-wide check owned by a non-mayor agent is
//	architect's nightly deploy-verify, and on the night of the outage that one
//	read green over an inert fleet. So a detector that routes through mayor
//	cannot report mayor being down. That is a CIRCULARITY, not a mistuned
//	threshold, and it is the whole explanation for 22 hours.
//
// A mayor-owned sweep reading mayor's own turnlog writes an artifact nobody
// reads and goes green for exactly the reason that night went green. So for the
// coordinator the criterion binds the READER: it needs the artifact AND a
// reader that is not itself.
//
// pm-pogo ruled (same amendment) that the reader must be pogod-resident, and
// explicitly refused the cheaper nightly-crew-reader variant on two grounds
// recorded here so the trade can be overridden knowingly rather than eroded
// quietly:
//
//   - The three measured outages ran ~23h30m, ~33h and ~22h. A nightly reader's
//     worst-case latency is ~24h. A detector whose latency is the same order as
//     the outages it detects is not a floor — it would have caught the last one
//     at roughly the moment it ended.
//   - Every crew-agent reader reintroduces the circularity one level out.
//     Architect is one bounce from unreachable and proved it: its predecessor
//     was bounced and a 03:30 fire sat unread for 17 hours.
//
// pogod is the only participant that is not a crew agent and does not route
// through mayor.
//
// WHAT A POGOD-RESIDENT READER COVERS, AND WHAT IT DOES NOT (from mayor, and
// stated here so the next reader does not see "floor" and assume it is under
// everything). It closes FLEET DOWN, POGOD UP — which is all three recorded
// outages. It does NOT close POGOD WEDGED rather than exited: a resident reader
// wedges with its host, and launchd restarts on exit only. That gap is real, it
// is not this ticket's, and scope should not be widened for it here.
//
// # THE ROUTING RULE, WHICH IS THIS PACKAGE'S ONE REAL IDEA
//
// A finding about the coordinator is NEVER delivered to the coordinator. It
// goes to the human box. Findings about anyone else go to the coordinator,
// which is the agent that can act on them. TestCoordinatorFindingNeverReaches
// TheCoordinator fails the build if that inverts — and it would invert
// silently, because mailing the coordinator is the correct default for every
// other detector in this tree.
//
// # WHAT IT IS NOT
//
// REPORT-ONLY. It mails and emits; it has no seam through which it could nudge,
// restart, or stop anything. That is not timidity: a stale turnlog has two
// causes that look identical and take opposite responses — a wedged session
// (restart is right) and an agent failing every turn in ~10ms on an expired
// credential (restart destroys the transcript that diagnoses it and the
// replacement inherits the credential). pogod already distinguishes those
// elsewhere; this detector's job is to make the condition visible at all.
//
// It is deliberately NOT merged with internal/firstturn, which asks the
// spawn-scoped question on ack evidence. The two look like one predicate with a
// different N and are not: the safe N differs by an order of magnitude (see
// turnlog.DefaultMaxAge), and a merged reader would also collapse two independent
// witnesses onto one evidence source and one blind state. An agent that stops
// writing turnlog lines while still acking stays visible, and the converse.
//
// It is also not the fleet's first alarm, and saying so plainly keeps the next
// reader from inheriting a wrong premise. Through the 22-hour outage ackwatch's
// FLEET BLACKOUT arm fired 33 consecutive times, each naming all five agents,
// each escalated to the human box, ~35 surfaced as macOS notifications.
// Detection, routing and out-of-process delivery all worked, and the outage
// still ran 22 hours. What was missing was a check pointed at AGENT-SIDE
// COMPLETION and a reader of it that mayor does not own. This is that, and it
// is a second witness on an existing floor rather than a first one.
//
// Stated as plainly as it can be, because the overstated version is the one
// that gets quoted: WHAT THIS ADDS IS LATENCY AND ROUTING, not detection where
// there was none. Latency, because it reads agent-written completion evidence
// on a 15-minute tick instead of inferring a ratio over a trailing 3h window,
// so it can speak about an agent that has been up for less than one window.
// Routing, because a finding about the coordinator goes somewhere the
// coordinator does not own. Anyone reading this file to decide whether the
// fleet is now covered should read the paragraph above about pogod wedged
// rather than exited before answering yes.
package turnwatch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/turnlog"
)

// Defaults. Deliberately coarse: this detector separates "the fleet is doing
// work" from "the fleet is present and doing nothing", a distinction that took
// 22 hours to make by hand. It is not a wedge detector and tightening it toward
// one would trade its whole value for false positives.
const (
	DefaultInterval = 15 * time.Minute
	// There is deliberately no DefaultMaxAge here. The staleness threshold
	// belongs to turnlog.DefaultMaxAge, which is where its measurement is
	// recorded, and a mirrored copy in this package would be a second number to
	// keep in step with a first — the drift shape this tree has been bitten by
	// before. This watcher does not choose the threshold; the Scan it is given
	// carries it.
	DefaultHoldDown      = 30 * time.Minute
	DefaultRenotifyAfter = 6 * time.Hour
	// DefaultGrace is how long after an agent starts it may go without a
	// completed turn before it is judged.
	//
	// The number is measured rather than picked. Across 87 crew spawns since
	// ack-tracking began (2026-07-23), spawn -> first completion took at most
	// 33.7 minutes for the 67 healthy spawns, and at least 150.8 minutes for
	// the 20 spawns inside the three outages. Nothing at all falls between.
	// 45 minutes sits inside that gap (measurement from mg-3cbb).
	DefaultGrace = 45 * time.Minute
	// DefaultHumanBox is where a coordinator finding goes, because the
	// coordinator cannot be told about itself.
	DefaultHumanBox = "human"

	EventFinding = "turn_watch_finding"
	EventClear   = "turn_watch_clear"
	EventError   = "turn_watch_error"
	EventSkipped = "turn_watch_skipped"
)

// ScanFunc produces the joined reading. Required.
type ScanFunc func(now time.Time) (turnlog.Report, error)

// MailFunc delivers a notice.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	Enabled bool
	// Scan produces the reading. Required.
	Scan ScanFunc
	// Mail delivers notices. Required — a detector that cannot report is the
	// thing this lineage exists to stop existing.
	Mail MailFunc
	// Emit writes turn_watch_* events. Defaults to events.Emit.
	Emit Emitter

	Interval time.Duration
	// HoldDown is how long a red reading must persist, unbroken, before it is
	// announced. Zero means DefaultHoldDown; NEGATIVE disables it, which only
	// tests should do — without it every pogod restart announces the whole
	// fleet in the gap between spawn and the first completed turn.
	HoldDown      time.Duration
	RenotifyAfter time.Duration
	// Grace is the post-start window in which an agent with no completed turn
	// is not judged. Zero means DefaultGrace; negative disables it.
	Grace time.Duration

	// Coordinator is the agent whose findings must NOT be mailed to it.
	Coordinator string
	// HumanBox receives coordinator findings. Empty means DefaultHumanBox.
	HumanBox string
	// StartedAt suppresses reporting for one hold-down after pogod starts: a
	// restart bounces the fleet, and every agent legitimately has no completed
	// turn for a while afterwards.
	StartedAt time.Time
}

// Watcher rides pogod's heartbeat and reads the turn-completion artifacts.
//
// It rides the heartbeat rather than a launchd timer for the same reason
// ackwatch, deafwatch and driftwatch do: the nondemand-spawn wedge on this box
// leaves launchd timers silently never firing, which for a detector of things
// that silently stopped happening would be especially apt.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	holdDown      time.Duration
	renotifyAfter time.Duration
	grace         time.Duration
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
	// Zero and negative must differ for the two windows a test needs to turn
	// OFF: a config that simply omits the key would otherwise silently disable
	// the hold-down, and a detector with no hold-down announces the whole fleet
	// on every pogod restart.
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
	grace := window(opts.Grace, DefaultGrace)
	hold := window(opts.HoldDown, DefaultHoldDown)
	humanBox := opts.HumanBox
	if humanBox == "" {
		humanBox = DefaultHumanBox
	}
	return &Watcher{
		enabled:       opts.Enabled,
		interval:      pick(opts.Interval, DefaultInterval),
		holdDown:      hold,
		renotifyAfter: pick(opts.RenotifyAfter, DefaultRenotifyAfter),
		grace:         grace,
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
	// A restart bounces the fleet, so every agent legitimately has no recent
	// completed turn for a while afterwards. Suppress for one hold-down.
	if !w.startedAt.IsZero() && now.Sub(w.startedAt) < w.holdDown {
		return
	}

	rep, err := w.scan(now)
	if err != nil {
		// A fleet that could not be judged is a real failure, not a clean
		// scan, and it goes on the event spine so a blind detector is
		// distinguishable from a quiet one. That distinction is the founding
		// bug of this whole lineage, one level up.
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Details:   map[string]any{"error": err.Error()},
		})
		return
	}

	confirmed := w.observe(rep, now)
	if len(confirmed) == 0 {
		w.clear(rep, now)
		return
	}
	w.announce(rep, confirmed, now)
}

// observe folds one reading into the hold-down state and returns the findings
// old enough to announce.
func (w *Watcher) observe(rep turnlog.Report, now time.Time) []turnlog.State {
	w.mu.Lock()
	defer w.mu.Unlock()

	seen := map[string]bool{}
	var confirmed []turnlog.State
	for _, s := range rep.Agents {
		if !s.Verdict.Finding() {
			continue
		}
		// An agent still inside its post-start grace has not failed to complete
		// a turn; it has not had time to complete one. Judging it would make
		// every spawn a finding and teach the reader to ignore this detector.
		if w.grace > 0 && !s.Started.IsZero() && now.Sub(s.Started) < w.grace {
			w.emit(events.Event{
				EventType: EventSkipped,
				Agent:     "pogod",
				Details: map[string]any{
					"target": s.Agent, "verdict": string(s.Verdict),
					"why": "inside the post-start grace window (" + w.grace.String() + ")",
				},
			})
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
	// whose failure hides itself from every other detector, and the one a
	// reader skimming a notice must see without scrolling.
	sort.Slice(confirmed, func(i, j int) bool {
		ci, cj := confirmed[i].Agent == w.coordinator, confirmed[j].Agent == w.coordinator
		if ci != cj {
			return ci
		}
		return confirmed[i].Agent < confirmed[j].Agent
	})
	return confirmed
}

func (w *Watcher) clear(rep turnlog.Report, now time.Time) {
	w.mu.Lock()
	had := w.lastPrint != ""
	w.lastPrint = ""
	w.mu.Unlock()
	if !had {
		return
	}
	w.emit(events.Event{
		EventType: EventClear,
		Agent:     "pogod",
		Details:   map[string]any{"population": len(rep.Agents), "live": rep.Live},
	})
}

// announce mails when the roster changed or the renotify interval elapsed.
func (w *Watcher) announce(rep turnlog.Report, confirmed []turnlog.State, now time.Time) {
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

	for _, to := range w.recipients(coordinatorHit) {
		body := renderNotice(rep, confirmed, w.coordinator, to, w.humanBox, now)
		if err := w.mail(to, "turn-watch", subject(confirmed, w.coordinator), body); err != nil {
			w.emit(events.Event{
				EventType: EventError,
				Agent:     "pogod",
				Details:   map[string]any{"error": err.Error(), "to": to},
			})
		}
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
			"population":       len(rep.Agents),
			"notified":         w.recipients(coordinatorHit),
			"reader_is_pogod":  true,
			"routed_via_mayor": false,
		},
	})
}

// recipients applies the routing rule. It is a function rather than an inline
// branch so a test can assert it directly, and it is the only place in this
// package that decides who hears about what.
//
// When the coordinator is among the findings, the notice goes to the human box
// and NOT to the coordinator. Delivering "the coordinator has completed no turn
// in N hours" to the coordinator is a message that arrives only if the claim is
// false — which is the circularity this whole package exists to break, and it
// would read as working code.
func (w *Watcher) recipients(coordinatorHit bool) []string {
	if coordinatorHit {
		return []string{w.humanBox}
	}
	if w.coordinator == "" {
		return []string{w.humanBox}
	}
	return []string{w.coordinator}
}

func subject(confirmed []turnlog.State, coordinator string) string {
	for _, s := range confirmed {
		if s.Agent == coordinator {
			return fmt.Sprintf("%s has completed no recent turn (%s) — and %d other(s)",
				coordinator, s.Verdict, len(confirmed)-1)
		}
	}
	if len(confirmed) == 1 {
		return fmt.Sprintf("%s has completed no recent turn (%s)", confirmed[0].Agent, confirmed[0].Verdict)
	}
	return fmt.Sprintf("%d agents present with no recent completed turn", len(confirmed))
}

func fingerprint(confirmed []turnlog.State) string {
	parts := make([]string, 0, len(confirmed))
	for _, s := range confirmed {
		parts = append(parts, s.Agent+"="+string(s.Verdict))
	}
	return strings.Join(parts, ",")
}

func renderNotice(rep turnlog.Report, confirmed []turnlog.State, coordinator, to, humanBox string, now time.Time) string {
	var b strings.Builder
	b.WriteString("Agents are PRESENT and have completed no recent turn.\n\n")
	b.WriteString("Evidence: " + rep.Dir + "/<agent>.log — one line per completed turn, written by the\n")
	b.WriteString("agent itself. Nothing but a finished turn produces it, which is what makes its\n")
	b.WriteString("absence mean something. Read by pogod, not by any crew agent.\n\n")

	for _, s := range confirmed {
		age := "never"
		if !s.Last.IsZero() {
			age = fmt.Sprintf("%s ago (%s)", time.Duration(s.AgeSecs*float64(time.Second)).Round(time.Minute),
				s.Last.Format(time.RFC3339))
		}
		mark := ""
		if s.Agent == coordinator {
			mark = "  <- the coordinator"
		}
		fmt.Fprintf(&b, "  %-10s %-16s last completed turn: %s%s\n", s.Verdict, s.Agent, age, mark)
		if s.Detail != "" {
			fmt.Fprintf(&b, "             %s\n", s.Detail)
		}
	}

	fmt.Fprintf(&b, "\nPopulation: %d present (from pogod's registry), %d live, %d stale, %d silent, %d unreadable.\n",
		len(rep.Agents), rep.Live, rep.Stale, rep.Silent, rep.Bad)

	for _, s := range confirmed {
		if s.Agent != coordinator {
			continue
		}
		fmt.Fprintf(&b, "\nWHY THIS CAME TO %s AND NOT TO %s: every fleet-wide scheduled check on this\n",
			strings.ToUpper(to), strings.ToUpper(coordinator))
		fmt.Fprintf(&b, "machine is %s-owned, so a detector routed through %s cannot report %s\n",
			coordinator, coordinator, coordinator)
		b.WriteString("being down. That circularity, not a mistuned threshold, is the whole reason a\n")
		b.WriteString("22-hour fleet outage read green on 2026-08-10/11. This reader lives in pogod.\n")
		break
	}

	b.WriteString("\nBEFORE RESTARTING ANYTHING:\n")
	b.WriteString("  pogo agent diagnose <name> --json | jq '{health, restart_suppressed, transcript_check}'\n")
	b.WriteString("A `silent` agent may simply be running a prompt rendered before this artifact\n")
	b.WriteString("existed — check its uptime. An agent failing every turn in ~10ms is not wedged:\n")
	b.WriteString("restarting it destroys the transcript that says so, and the replacement inherits\n")
	b.WriteString("whatever credential or limit is failing.\n")
	b.WriteString("\nRead the whole fleet yourself: pogo check-turns\n")
	b.WriteString("Confirm this check can still go red: pogo check-turns --probe\n")
	b.WriteString("\nREPORT-ONLY. Nothing was nudged, restarted or stopped.\n")
	return b.String()
}
