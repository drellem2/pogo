package firstturn

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Mail identities and routing defaults.
const (
	mailFrom = "first-turn"
	// DefaultNotifyTo is the mayor, for the SINGLE-agent case: one crew agent
	// that never came up is the coordinator's to restart, and it is the actor
	// that can do so without a person.
	DefaultNotifyTo = "mayor"
	// DefaultEscalateTo is the mailbox a person reads. pogod overrides it with
	// `[agents] escalation_box`, which is the whole point of that seam: the
	// fleet-wide case must reach a recipient OUTSIDE the fleet, and `mayor` is
	// inside every outage in this system's history (mg-e2a4).
	DefaultEscalateTo = "human"
)

// Event types written to ~/.pogo/events.log.
const (
	// EventDark records a mailed finding.
	EventDark = "first_turn_watch_dark"
	// EventClear records a sample with nothing to report, carrying the coverage
	// counts. It exists because a silent correct outcome and a control that is
	// not running are otherwise the same observation — and this whole package
	// exists because a detector's quiet was read as the fleet's health for 17
	// hours.
	EventClear = "first_turn_watch_clear"
	// EventBlind records a sample that could not be judged. A detector that
	// cannot look must be visibly unable to look.
	EventBlind = "first_turn_watch_blind"
	// EventUnreported records a finding whose every recipient refused the mail.
	// It is the one state worse than the bug this arm fixes: the fleet never
	// came up, pogod noticed, and the notice did not leave the machine.
	EventUnreported = "first_turn_watch_unreported"
)

// EpisodeKind is this detector's value for details.kind on the generic
// incident_episode_cleared event (mg-55b2 contract), so the pogo-reminders
// notifier coalesces a fleet-wide close into ONE notification.
const EpisodeKind = "no_first_turn"

// IncidentEpisodeClearedEvent is the generic episode-close event type, spelled
// out rather than imported to keep this package free of a dependency on
// internal/claude. It is byte-identical to claude.IncidentEpisodeClearedEvent.
const IncidentEpisodeClearedEvent = "incident_episode_cleared"

// DefaultFirstRepage is how long after the opening page the first repeat goes
// out; each subsequent repeat waits twice as long as the last.
//
// A ladder rather than a fixed interval, and rather than synthwatch's
// page-once-per-episode, because BOTH of the obvious shapes failed on the real
// outage. Page once and the notice is a single line 17 hours deep in a mailbox
// holding 1400 unread messages. Page on a fixed interval and you get what
// ackwatch's blackout arm sent: 33 notices with an IDENTICAL subject, of which
// the 33rd carried no more information than the 1st, and none of which ever
// said how long this had been going on.
//
// Doubling produces pages at +1h, +3h, +7h, +15h — five mails across a 17-hour
// outage, each with a strictly larger duration in its subject line. The number
// that grows is the one a person skimming subject lines can actually act on.
const DefaultFirstRepage = time.Hour

// DefaultMaxRepage caps the ladder so a long outage still produces a heartbeat
// rather than doubling into silence.
const DefaultMaxRepage = 6 * time.Hour

// MailFunc sends operator mail. pogod wires client.SendMGMail.
type MailFunc func(to, from, subject, body string) error

// SourceFunc produces one sample.
type SourceFunc func(now time.Time) Snapshot

// Options carries the runner's dependencies.
type Options struct {
	// Enabled arms the runner.
	Enabled bool
	// Source produces the snapshot. Required.
	Source SourceFunc
	// Mail delivers the notice. Required — a detector that cannot report is
	// precisely the thing this package exists to stop existing.
	Mail MailFunc
	// Emit writes first_turn_watch_* events. Defaults to events.Emit.
	Emit func(events.Event)
	// Interval is the sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// Params tunes the predicate. Zero fields fall back to DefaultParams.
	Params Params
	// NotifyTo receives the single-agent case. Empty means DefaultNotifyTo.
	NotifyTo string
	// EscalateTo receives the fleet-wide case, immediately. Empty means
	// DefaultEscalateTo.
	EscalateTo string
	// FirstRepage / MaxRepage tune the escalation ladder. Zero means the
	// defaults.
	FirstRepage time.Duration
	MaxRepage   time.Duration
	// StartedAt is the hosting process's start. Findings are suppressed until
	// one Grace has elapsed since it, because pogod's own restart is when every
	// agent is freshly spawned and none of them has had time to ack — judging
	// them against a clock that started before they existed would make every
	// bounce a fleet alarm.
	StartedAt time.Time
}

// Watcher is the standing floor: it rides pogod's heartbeat, samples the crew
// population, and mails when an agent has completed nothing since it spawned.
//
// It rides the heartbeat rather than a launchd timer for the same reason
// internal/ackwatch and internal/deafwatch do: the nondemand-spawn wedge on this
// box (mg-50e0) leaves launchd timers silently never firing, which for a
// detector whose entire job is noticing that a scheduled thing stopped
// happening would be especially apt.
type Watcher struct {
	enabled     bool
	interval    time.Duration
	params      Params
	notifyTo    string
	escalateTo  string
	firstRepage time.Duration
	maxRepage   time.Duration
	startedAt   time.Time
	source      SourceFunc
	mail        MailFunc
	emit        func(events.Event)

	mu      sync.Mutex
	lastRun time.Time
	ran     bool
	// Episode state. openedAt/episodeID are stamped by the first sample that
	// finds anything; roster accumulates every agent seen dark in the episode so
	// the clear mail can name them all.
	episodeID  string
	openedAt   time.Time
	roster     map[string]bool
	lastMailed time.Time
	// repageAfter is the current rung of the ladder, doubling on each repeat.
	repageAfter time.Duration
	// toldEscalate records whether EscalateTo was ever on this episode's mail,
	// so the all-clear reaches everyone who was alarmed. An all-clear narrower
	// than the alarm leaves someone holding an open incident forever.
	toldEscalate bool
}

// New builds a Watcher, applying defaults for zero-valued options.
func New(opts Options) *Watcher {
	emit := opts.Emit
	if emit == nil {
		emit = func(e events.Event) { events.Emit(context.Background(), e) }
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	params := opts.Params
	def := DefaultParams()
	if params.Grace <= 0 {
		params.Grace = def.Grace
	}
	if params.MinDeliveries <= 0 {
		params.MinDeliveries = def.MinDeliveries
	}
	if params.MinFleetAgents <= 0 {
		params.MinFleetAgents = def.MinFleetAgents
	}
	notifyTo := opts.NotifyTo
	if notifyTo == "" {
		notifyTo = DefaultNotifyTo
	}
	escalateTo := opts.EscalateTo
	if escalateTo == "" {
		escalateTo = DefaultEscalateTo
	}
	first := opts.FirstRepage
	if first <= 0 {
		first = DefaultFirstRepage
	}
	max := opts.MaxRepage
	if max <= 0 {
		max = DefaultMaxRepage
	}
	return &Watcher{
		enabled: opts.Enabled, interval: interval, params: params,
		notifyTo: notifyTo, escalateTo: escalateTo,
		firstRepage: first, maxRepage: max, startedAt: opts.StartedAt,
		source: opts.Source, mail: opts.Mail, emit: emit,
		roster: map[string]bool{},
	}
}

// Check runs one sample subject to the throttle. It is the heartbeat OnTick
// integration point and a no-op on all but the first tick of each interval.
func (w *Watcher) Check(now time.Time) {
	if w == nil || !w.enabled || w.source == nil || w.mail == nil {
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
	rep := Detect(w.source(now), w.params)

	switch rep.State {
	case StateBlind:
		w.emit(events.Event{
			EventType: EventBlind,
			Agent:     "pogod",
			Details: map[string]any{
				"reason":  rep.BlindReason,
				"scanned": rep.Scanned,
				"why":     "a sample that could not be taken is NOT a clean fleet; this arm degrades to silence loudly rather than quietly",
			},
		})
		return
	case StateCalm:
		w.emit(events.Event{
			EventType: EventClear,
			Agent:     "pogod",
			Details: map[string]any{
				"judged":          rep.Judged,
				"scanned":         rep.Scanned,
				"too_fresh":       rep.TooFresh,
				"beyond_lookback": rep.BeyondLookback,
				"never_addressed": rep.NeverAddressed,
				"grace":           w.params.Grace.String(),
			},
		})
		w.closeEpisode(now)
		return
	}

	// A pogod restart spawns the whole crew at once; none of them can have
	// acked yet. Suppressing for one grace period after our own start keeps a
	// bounce from being reported as an outage, while leaving the case this arm
	// exists for — a bounce whose agents NEVER ack — intact, because that one is
	// still true one grace later.
	if !w.startedAt.IsZero() && now.Sub(w.startedAt) < w.params.Grace {
		w.emit(events.Event{
			EventType: EventClear,
			Agent:     "pogod",
			Details: map[string]any{
				"suppressed": true,
				"reason": fmt.Sprintf("pogod started %s ago; findings are held until one grace period (%s) has elapsed",
					now.Sub(w.startedAt).Round(time.Second), w.params.Grace),
				"would_have_reported": Names(rep.Findings),
			},
		})
		return
	}

	w.announce(rep, now)
}

// announce records the episode and mails on the ladder.
func (w *Watcher) announce(rep Report, now time.Time) {
	names := Names(rep.Findings)

	w.mu.Lock()
	newRoster := false
	for _, n := range names {
		if !w.roster[n] {
			w.roster[n] = true
			newRoster = true
		}
	}
	if w.episodeID == "" {
		w.openedAt = now
		w.episodeID = makeEpisodeID(names[0], now)
		w.repageAfter = w.firstRepage
	}
	// Mail when the roster grew (a new agent joining is news) or the current
	// rung of the ladder has elapsed. Roster growth does NOT advance the rung:
	// a fleet coming apart one agent at a time must not be able to reset its own
	// escalation clock.
	due := w.lastMailed.IsZero() || now.Sub(w.lastMailed) >= w.repageAfter
	shouldMail := newRoster || due
	if shouldMail {
		if due && !w.lastMailed.IsZero() {
			w.repageAfter *= 2
			if w.repageAfter > w.maxRepage {
				w.repageAfter = w.maxRepage
			}
		}
		w.lastMailed = now
	}
	episodeID, openedAt := w.episodeID, w.openedAt
	w.mu.Unlock()

	if !shouldMail {
		return
	}

	// The fleet case escalates on its FIRST sample, structurally, not on an age
	// gate. Patience cannot help a recipient that is inside the failure, and
	// mg-e2a4 paid for that lesson: at 16:12:59 mid-outage the blackout arm
	// mailed exactly one recipient, `mayor`, an agent carrying the same 27
	// unacked fires. A fleet that has never completed a turn cannot restart
	// itself, so the only useful recipient is outside it.
	recipients := []string{w.notifyTo}
	escalated := false
	if rep.Fleet && w.escalateTo != w.notifyTo {
		recipients = append(recipients, w.escalateTo)
		escalated = true
	}

	subject := mailSubject(rep, now, openedAt)
	body := mailBody(rep, now, openedAt, w.params, w.notifyTo, w.escalateTo, escalated)

	details := map[string]any{
		"episode_id":       episodeID,
		"state":            rep.State.String(),
		"fleet":            rep.Fleet,
		"agents":           names,
		"identities":       Identities(rep.Findings),
		"dark_for":         rep.DarkFor.Round(time.Second).String(),
		"episode_age":      now.Sub(openedAt).Round(time.Second).String(),
		"judged":           rep.Judged,
		"scanned":          rep.Scanned,
		"too_fresh":        rep.TooFresh,
		"beyond_lookback":  rep.BeyondLookback,
		"never_addressed":  rep.NeverAddressed,
		"grace":            w.params.Grace.String(),
		"notified":         strings.Join(recipients, ","),
		"escalated":        escalated,
		"notify_to_dark":   inRoster(w.notifyTo, names),
		"escalate_to_dark": inRoster(w.escalateTo, names),
	}
	failures := 0
	for _, to := range recipients {
		if err := w.mail(to, mailFrom, subject, body); err != nil {
			details["mail_error_"+to] = err.Error()
			failures++
		}
	}
	if failures < len(recipients) {
		w.mu.Lock()
		if escalated {
			w.toldEscalate = true
		}
		w.mu.Unlock()
	}
	w.emit(events.Event{EventType: EventDark, Agent: "pogod", Details: details})

	if failures == len(recipients) {
		w.emit(events.Event{
			EventType: EventUnreported,
			Agent:     "pogod",
			Details: map[string]any{
				"recipients": strings.Join(recipients, ","),
				"agents":     names,
				"dark_for":   rep.DarkFor.Round(time.Second).String(),
			},
		})
	}
}

// closeEpisode ends an open episode. It is a no-op when none is open, so a
// healthy fleet stays quiet.
func (w *Watcher) closeEpisode(now time.Time) {
	w.mu.Lock()
	if w.episodeID == "" {
		w.mu.Unlock()
		return
	}
	episodeID, openedAt := w.episodeID, w.openedAt
	told := w.toldEscalate
	roster := make([]string, 0, len(w.roster))
	for n := range w.roster {
		roster = append(roster, n)
	}
	w.episodeID = ""
	w.openedAt = time.Time{}
	w.roster = map[string]bool{}
	w.lastMailed = time.Time{}
	w.repageAfter = 0
	w.toldEscalate = false
	w.mu.Unlock()

	sortStrings(roster)
	w.emit(episodeClearedEvent(episodeID, roster, openedAt, now))

	subject := fmt.Sprintf("first-turn: %d agent(s) completed a turn at last — after %s dark",
		len(roster), now.Sub(openedAt).Round(time.Minute))
	var b strings.Builder
	fmt.Fprintf(&b, "Every agent reported by first-turn in this episode has now completed at\n")
	fmt.Fprintf(&b, "least one scheduled fire since it was spawned.\n\n")
	fmt.Fprintf(&b, "Episode: %s\n  opened  %s\n  cleared %s (%s)\n\n",
		episodeID, openedAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339),
		now.Sub(openedAt).Round(time.Minute))
	for _, n := range roster {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	fmt.Fprintf(&b, "\nThe fires delivered during this episode were CONSUMED, not queued: the work\n")
	fmt.Fprintf(&b, "of that window is gone rather than late (mg-18d0). Confirm what each agent\n")
	fmt.Fprintf(&b, "missed before assuming the gap closed itself.\n")

	recipients := []string{w.notifyTo}
	if told && w.escalateTo != w.notifyTo {
		recipients = append(recipients, w.escalateTo)
	}
	for _, to := range recipients {
		if err := w.mail(to, mailFrom, subject, b.String()); err != nil {
			w.emit(events.Event{
				EventType: EventBlind,
				Agent:     "pogod",
				Details:   map[string]any{"reason": err.Error(), "phase": "clear", "to": to},
			})
		}
	}
}

// mailSubject leads with the DURATION, because that is the field that changes
// between one notice and the next. See DefaultFirstRepage.
func mailSubject(rep Report, now, openedAt time.Time) string {
	dark := rep.DarkFor
	if age := now.Sub(openedAt); age > dark {
		dark = age
	}
	if rep.Fleet {
		return fmt.Sprintf("first-turn: NO CREW AGENT HAS COMPLETED A TURN SINCE SPAWN — %s and counting (%d agents)",
			roundDur(dark), len(rep.Findings))
	}
	return fmt.Sprintf("first-turn: %s has completed nothing since it spawned %s ago",
		strings.Join(Names(rep.Findings), ", "), roundDur(dark))
}

func mailBody(rep Report, now, openedAt time.Time, p Params, notifyTo, escalateTo string, escalated bool) string {
	var b strings.Builder

	if escalated {
		fmt.Fprintf(&b,
			"ESCALATED IMMEDIATELY, NOT ON A TIMER: every crew agent pogod is running has\n"+
				"completed zero turns since it was spawned. %s — the mailbox this notice\n"+
				"normally goes to — is one of them, so it will not be reading this. A fleet\n"+
				"that has never come up cannot be the thing that fixes it, which is why this\n"+
				"is also addressed to %s (mg-e2a4).\n\n", notifyTo, escalateTo)
		if inRoster(escalateTo, Names(rep.Findings)) {
			fmt.Fprintf(&b,
				"WARNING: %s is ALSO one of the agents below, so this notice has no recipient\n"+
					"outside the outage. Set `[agents] escalation_box` to a mailbox no agent owns\n"+
					"— see docs/CONFIGURATION.md.\n\n", escalateTo)
		}
	}

	fmt.Fprintf(&b, "A SPAWN IS NOT A SUCCESS. pogod started these agents and they have never\n")
	fmt.Fprintf(&b, "finished a single scheduled fire since:\n\n")
	for _, f := range rep.Findings {
		fmt.Fprintf(&b, "- %s\n", f.Name)
		fmt.Fprintf(&b, "    spawned   %s (%s ago)\n", f.StartedAt.UTC().Format(time.RFC3339), roundDur(f.DarkFor))
		fmt.Fprintf(&b, "    delivered %d fires since, completed 0\n", f.Delivered)
		fmt.Fprintf(&b, "    diagnose  pogo agent diagnose %s\n", f.Name)
	}

	fmt.Fprintf(&b, "\nEpisode opened %s (%s ago).\n",
		openedAt.UTC().Format(time.RFC3339), roundDur(now.Sub(openedAt)))
	fmt.Fprintf(&b, "Grace is %s — see internal/firstturn for the measurement it comes from.\n\n", p.Grace)

	fmt.Fprintf(&b, "WHAT THIS IS NOT: it is not the fleet going quiet after being alive. That\n")
	fmt.Fprintf(&b, "is ack-watch's FLEET BLACKOUT notice, which measures a completion RATIO\n")
	fmt.Fprintf(&b, "over a trailing 3h window and therefore cannot speak about an agent until\n")
	fmt.Fprintf(&b, "it has been up that long. This arm covers the gap on the other side of a\n")
	fmt.Fprintf(&b, "spawn: agents that were started and never came up at all.\n\n")

	fmt.Fprintf(&b, "The three outages this exists for had three different causes (expired\n")
	fmt.Fprintf(&b, "credential, a deploy that hung 31h39m, a spend limit followed by five inert\n")
	fmt.Fprintf(&b, "spawns) and one thing in common: every one was found by an agent reading\n")
	fmt.Fprintf(&b, "its own logs AFTER it came back. Check the cause before restarting —\n")
	fmt.Fprintf(&b, "no member of the synthetic-failure class is fixable by a restart, and\n")
	fmt.Fprintf(&b, "pogod may already be suppressing respawns for that reason (mg-18d0).\n\n")

	fmt.Fprintf(&b, "  pogo agent list                       # status, pid, uptime\n")
	fmt.Fprintf(&b, "  pogo schedule list                    # unacked streaks per agent\n")
	fmt.Fprintf(&b, "  pogo agent diagnose <name> --json     # health, health_detail, transcript_check\n\n")

	fmt.Fprintf(&b, "Judged %d of %d agents. Not judged: %d too fresh, %d beyond the lookback,\n",
		len(rep.Judged), rep.Scanned, len(rep.TooFresh), len(rep.BeyondLookback))
	fmt.Fprintf(&b, "%d never addressed by any fire (that one is deaf-watch's finding, not this\n", len(rep.NeverAddressed))
	fmt.Fprintf(&b, "one). Polecats are never judged — see internal/firstturn.\n\n")
	fmt.Fprintf(&b, "This is REPORT-ONLY. pogod did not restart, nudge, or respawn anything.\n")
	return b.String()
}

// roundDur renders a duration at a granularity a human reads: minutes under an
// hour, hours and minutes above it.
func roundDur(d time.Duration) string {
	if d < time.Hour {
		return d.Round(time.Minute).String()
	}
	return d.Round(time.Minute).String()
}

func inRoster(name string, roster []string) bool {
	for _, n := range roster {
		if n == name {
			return true
		}
	}
	return false
}

// makeEpisodeID builds a stable per-episode id from the first agent in the
// roster and the episode's open time, byte-identical in shape to deafwatch's,
// synthwatch's and usagelimit.go's. Deriving it from the injected clock keeps it
// deterministic under test.
func makeEpisodeID(firstAgent string, openedAt time.Time) string {
	return fmt.Sprintf("ep-%d-%s", openedAt.UTC().UnixNano(), firstAgent)
}

// episodeClearedEvent mirrors deafwatch's and synthwatch's emitters exactly —
// same event type, same Agent ("pogod"), same RFC3339Nano timestamps, same
// details field names and nesting — changing only details.kind. Do not diverge
// this shape from theirs, or from mg-e0f6's reader, without updating all of
// them.
func episodeClearedEvent(episodeID string, roster []string, openedAt, closedAt time.Time) events.Event {
	ids := append([]string(nil), roster...)
	sortStrings(ids)
	return events.Event{
		EventType: IncidentEpisodeClearedEvent,
		Agent:     "pogod",
		Timestamp: closedAt.UTC().Format(time.RFC3339Nano),
		Details: map[string]any{
			"kind":       EpisodeKind,
			"episode_id": episodeID,
			"roster":     ids,
			"opened_at":  openedAt.UTC().Format(time.RFC3339Nano),
			"closed_at":  closedAt.UTC().Format(time.RFC3339Nano),
		},
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
