package blindwatch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Defaults.
const (
	// DefaultInterval is how often the consumer samples the detector's state.
	DefaultInterval = 15 * time.Minute
	// DefaultHoldDown is how long a blindness must persist, unbroken, before it
	// is announced. Blindness is expected to flicker — an agent between PTY
	// redraws is momentarily unreadable — and the condition worth reporting is
	// the standing one. Measured against mg-d616's case this is not a close
	// call: that blindness ran 18 days.
	DefaultHoldDown = 2 * time.Hour
	// DefaultStaleAfter is how long the detector may go without completing a
	// sample before its SILENCE becomes the finding. wedgewatch's own interval
	// is minutes; an hour is many missed samples and is not reachable by
	// scheduling jitter.
	DefaultStaleAfter = time.Hour
	// DefaultRenotifyAfter is how long an unchanged finding stays quiet. A
	// standing blindness is a slow condition and 2609 notices is the failure
	// mode on the other side.
	DefaultRenotifyAfter = 24 * time.Hour
	// DefaultHumanBox receives a finding the coordinator cannot act on.
	DefaultHumanBox = "human"

	EventFinding = "blind_watch_finding"
	EventClear   = "blind_watch_clear"
	EventError   = "blind_watch_error"

	mailFrom = "blind-watch"
)

// SourceFunc produces one reading of the watched detector's state. Required.
type SourceFunc func(now time.Time) (Snapshot, error)

// MailFunc delivers a notice.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	Enabled bool
	// Source reads the watched detector. Required.
	//
	// A NOTE ON ITS ERROR RETURN, because this package's own subject matter is
	// branches that never run. The only production source today
	// (WedgeSource) never returns a non-nil error — it reads an in-process
	// accessor, and a detector that is not armed is reported as a STOPPED
	// finding rather than as an error, deliberately. So sample()'s error arm is
	// exercised by tests alone, and that is stated here rather than left to
	// look like coverage: mg-20eb is four days of a documented fallback whose
	// keying field nothing in production ever assigned. If a source is ever
	// added that CAN fail, the error arm becomes live and its blind_watch_error
	// needs a reader of its own — which is this ticket, one level further out.
	Source SourceFunc
	// Mail delivers notices. Required.
	Mail MailFunc
	// Emit writes blind_watch_* events. Defaults to events.Emit.
	Emit Emitter

	Interval time.Duration
	// HoldDown is how long a condition must persist before it is announced.
	// Zero means DefaultHoldDown; NEGATIVE disables it. Zero and negative must
	// differ, or a config that omits the key would silently turn it off.
	HoldDown time.Duration
	// StaleAfter is how long the watched detector may go without completing a
	// sample before its silence is the finding. Zero means DefaultStaleAfter;
	// negative disables that arm — which only a test should do, since it is the
	// arm that keeps "no blindness" from meaning "no detector".
	StaleAfter    time.Duration
	RenotifyAfter time.Duration

	// Coordinator is the agent whose findings must NOT be mailed to it.
	Coordinator string
	// HumanBox receives escalated notices. Empty means DefaultHumanBox.
	HumanBox string
	// StartedAt suppresses reporting for one StaleAfter after pogod starts: a
	// freshly armed detector has legitimately completed no sample yet.
	StartedAt time.Time
}

// Watcher rides pogod's heartbeat and reads a detector's own judgement state.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	holdDown      time.Duration
	staleAfter    time.Duration
	renotifyAfter time.Duration
	coordinator   string
	humanBox      string
	startedAt     time.Time

	source SourceFunc
	mail   MailFunc
	emit   Emitter

	mu      sync.Mutex
	lastRun time.Time
	ran     bool
	// sinceBlind is when each target was first seen blind in the current
	// unbroken run, held here rather than trusted from the snapshot so a
	// detector that restarts and re-dates its own clocks cannot reset this
	// consumer's patience.
	sinceBlind map[string]time.Time
	// sinceQuiet is when the detector was first observed past StaleAfter.
	sinceQuiet time.Time
	// sinceEmpty is when it was first observed examining nobody.
	sinceEmpty time.Time
	lastPrint  string
	lastMailed time.Time
	toldHuman  bool
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
		staleAfter:    window(opts.StaleAfter, DefaultStaleAfter),
		renotifyAfter: pick(opts.RenotifyAfter, DefaultRenotifyAfter),
		coordinator:   opts.Coordinator,
		humanBox:      humanBox,
		startedAt:     opts.StartedAt,
		source:        opts.Source,
		mail:          opts.Mail,
		emit:          emit,
		sinceBlind:    map[string]time.Time{},
	}
}

// Check runs one sample subject to the throttle. It is the integration point
// for pogod's heartbeat OnTick.
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
	// A freshly armed detector has legitimately completed no sample yet.
	if !w.startedAt.IsZero() && w.staleAfter > 0 && now.Sub(w.startedAt) < w.staleAfter {
		return
	}

	snap, err := w.source(now)
	if err != nil {
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Details:   map[string]any{"error": err.Error()},
		})
		return
	}

	confirmed := w.observe(snap, now)
	if len(confirmed) == 0 {
		// Same gate as internal/heartwatch, and here the false all-clear is
		// worse: `confirmed` is empty whenever a condition is inside its
		// hold-down, INCLUDING a detector that has just stopped sampling. A
		// clear at that moment would mail "wedge-watch can judge the fleet
		// again" about a detector that had gone dark two minutes earlier. So the
		// clear requires the reading itself to be clean — sampled recently, a
		// population above zero, and nobody blind at all.
		if healthy(snap, now, w.staleAfter) {
			w.clear(snap, now)
		}
		return
	}
	w.announce(snap, confirmed, now)
}

// observe folds one reading into the hold-down state and returns the conditions
// old enough to announce.
func (w *Watcher) observe(snap Snapshot, now time.Time) []Finding {
	w.mu.Lock()
	defer w.mu.Unlock()

	var out []Finding

	// STOPPED first. It subsumes the other two: a detector that is not sampling
	// has an empty blind set and a zero population for the same reason, and
	// reporting all three would be one fault wearing three labels.
	quiet := w.staleAfter > 0 &&
		(snap.SampledAt.IsZero() || now.Sub(snap.SampledAt) >= w.staleAfter)
	if quiet {
		if w.sinceQuiet.IsZero() {
			w.sinceQuiet = now
		}
		if now.Sub(w.sinceQuiet) >= w.holdDown {
			last := "never"
			if !snap.SampledAt.IsZero() {
				last = fmt.Sprintf("%s ago (%s)", now.Sub(snap.SampledAt).Round(time.Minute),
					snap.SampledAt.UTC().Format(time.RFC3339))
			}
			out = append(out, Finding{
				Kind: KindStopped,
				Detail: fmt.Sprintf("%s has completed no sample for longer than %s — last verdict: %s. "+
					"It emits nothing on a clean pass, so its silence and a healthy fleet are the same "+
					"reading in the event log; that is why this arm exists",
					snap.Detector, w.staleAfter, last),
			})
		}
		// The blind clocks are NOT advanced while the detector is stopped: an
		// agent it stopped looking at has not been unjudgeable for longer, it
		// has been unlooked-at, and merging the two would date the wrong fault.
		w.forgetBlind(nil)
		w.sinceEmpty = time.Time{}
		return out
	}
	w.sinceQuiet = time.Time{}

	if snap.Examined == 0 {
		if w.sinceEmpty.IsZero() {
			w.sinceEmpty = now
		}
		if now.Sub(w.sinceEmpty) >= w.holdDown {
			out = append(out, Finding{
				Kind: KindEmpty,
				Detail: fmt.Sprintf("%s is sampling a population of ZERO. Zero examined yields zero "+
					"blind agents and zero findings, which is the shape of green, not a fleet it can see",
					snap.Detector),
			})
		}
	} else {
		w.sinceEmpty = time.Time{}
	}

	live := make(map[string]bool, len(snap.Blind))
	var targets []Target
	for _, b := range snap.Blind {
		if b.Name == "" {
			continue
		}
		live[b.Name] = true
		at, known := w.sinceBlind[b.Name]
		if !known {
			at = now
			w.sinceBlind[b.Name] = at
		}
		if now.Sub(at) >= w.holdDown {
			targets = append(targets, Target{Name: b.Name, Why: b.Why, Since: at})
		}
	}
	w.forgetBlind(live)
	if len(targets) > 0 {
		sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
		oldest := targets[0].Since
		for _, t := range targets {
			if t.Since.Before(oldest) {
				oldest = t.Since
			}
		}
		out = append(out, Finding{
			Kind: KindBlind,
			Detail: fmt.Sprintf("%s has been unable to judge %d agent(s) for up to %s. "+
				"An agent that could NOT be judged is not the same as a healthy one",
				snap.Detector, len(targets), now.Sub(oldest).Round(time.Minute)),
			Targets: targets,
		})
	}
	return out
}

// healthy reports whether the reading shows an instrument that is answering:
// it sampled inside staleAfter, it examined somebody, and it declared nobody
// unjudgeable. It is deliberately stricter than "no confirmed findings" — see
// the call site.
func healthy(snap Snapshot, now time.Time, staleAfter time.Duration) bool {
	if staleAfter > 0 && (snap.SampledAt.IsZero() || now.Sub(snap.SampledAt) >= staleAfter) {
		return false
	}
	return snap.Examined > 0 && len(snap.Blind) == 0
}

// forgetBlind drops clocks for targets no longer in the live set. Callers hold
// w.mu.
func (w *Watcher) forgetBlind(live map[string]bool) {
	for name := range w.sinceBlind {
		if !live[name] {
			delete(w.sinceBlind, name)
		}
	}
}

func (w *Watcher) clear(snap Snapshot, now time.Time) {
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
		Details:   map[string]any{"detector": snap.Detector, "examined": snap.Examined},
	})
	subject := fmt.Sprintf("blind-watch: %s can judge the fleet again", snap.Detector)
	body := fmt.Sprintf(
		"%s is sampling and judging again: last sample %s, %d agent(s) examined, 0 unjudgeable.\n\n"+
			"This clears the condition blind-watch reported. Nothing about the fleet's HEALTH is\n"+
			"asserted here — only that the instrument is answering.\n",
		snap.Detector, snap.SampledAt.UTC().Format(time.RFC3339), snap.Examined)
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

func (w *Watcher) announce(snap Snapshot, confirmed []Finding, now time.Time) {
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

	escalate := aboutTheDetector(confirmed) || namesCoordinator(confirmed, w.coordinator)
	to := w.recipients(escalate)
	body := renderNotice(snap, confirmed, w.coordinator, escalate, now)
	subj := subject(snap, confirmed)
	for _, box := range to {
		if err := w.mail(box, mailFrom, subj, body); err != nil {
			w.emit(events.Event{
				EventType: EventError,
				Agent:     "pogod",
				Details:   map[string]any{"error": err.Error(), "to": box},
			})
		}
	}
	if escalate {
		w.mu.Lock()
		w.toldHuman = true
		w.mu.Unlock()
	}

	kinds := make([]string, 0, len(confirmed))
	var names []string
	for _, f := range confirmed {
		kinds = append(kinds, string(f.Kind))
		for _, t := range f.Targets {
			names = append(names, t.Name)
		}
	}
	w.emit(events.Event{
		EventType: EventFinding,
		Agent:     "pogod",
		Details: map[string]any{
			"detector":        snap.Detector,
			"kinds":           kinds,
			"targets":         names,
			"examined":        snap.Examined,
			"notified":        to,
			"escalated":       escalate,
			"reader_is_pogod": true,
		},
	})
}

// recipients applies the routing rule. Same shape as internal/turnwatch and
// internal/heartwatch, and it is the only place here that decides who hears
// what.
//
// A finding ABOUT THE DETECTOR — stopped, or examining nobody — escalates, and
// that is not the same branch as "the finding names the coordinator". A stopped
// wedge-watcher is a statement about pogod's instrument set; the coordinator is
// not the agent that repairs one, and routing it there would put a report about
// a broken instrument into the queue of an agent whose own liveness that
// instrument is one of the few things that could have reported.
func (w *Watcher) recipients(escalate bool) []string {
	if escalate || w.coordinator == "" {
		return []string{w.humanBox}
	}
	return []string{w.coordinator}
}

func (w *Watcher) clearRecipients(toldHuman bool) []string {
	if w.coordinator == "" {
		return []string{w.humanBox}
	}
	if toldHuman && w.humanBox != w.coordinator {
		return []string{w.coordinator, w.humanBox}
	}
	return []string{w.coordinator}
}

// aboutTheDetector reports whether any finding is about the instrument itself
// rather than about an agent it could not read.
func aboutTheDetector(fs []Finding) bool {
	for _, f := range fs {
		if f.Kind == KindStopped || f.Kind == KindEmpty {
			return true
		}
	}
	return false
}

func namesCoordinator(fs []Finding, coordinator string) bool {
	if coordinator == "" {
		return false
	}
	for _, f := range fs {
		for _, t := range f.Targets {
			if t.Name == coordinator {
				return true
			}
		}
	}
	return false
}

func fingerprint(fs []Finding) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		names := make([]string, 0, len(f.Targets))
		for _, t := range f.Targets {
			names = append(names, t.Name)
		}
		parts = append(parts, string(f.Kind)+"="+strings.Join(names, "+"))
	}
	return strings.Join(parts, ",")
}

func subject(snap Snapshot, confirmed []Finding) string {
	for _, f := range confirmed {
		switch f.Kind {
		case KindStopped:
			return fmt.Sprintf("%s has stopped judging — its silence is not a healthy fleet", snap.Detector)
		case KindEmpty:
			return fmt.Sprintf("%s is examining ZERO agents — that is not a clean fleet", snap.Detector)
		}
	}
	n := 0
	for _, f := range confirmed {
		n += len(f.Targets)
	}
	return fmt.Sprintf("%s cannot judge %d agent(s), and has not been able to for hours", snap.Detector, n)
}

func renderNotice(snap Snapshot, confirmed []Finding, coordinator string, escalate bool, now time.Time) string {
	var b strings.Builder
	b.WriteString("A DETECTOR is reporting that it cannot answer.\n\n")
	b.WriteString("This notice is about the INSTRUMENT, not about the fleet. Nothing here says any\n")
	b.WriteString("agent is wedged. It says the check that would have told you has declined to\n")
	b.WriteString("judge, which is not the same as a clean reading — and until mg-d616 it said so\n")
	b.WriteString("into a channel with no consumer: 2609 wedge_watch_error events over 18 days,\n")
	b.WriteString("each ending \"The agent could NOT be judged, which is not the same as healthy.\"\n\n")

	for _, f := range confirmed {
		fmt.Fprintf(&b, "  %-8s %s\n", strings.ToUpper(string(f.Kind)), f.Detail)
		for _, t := range f.Targets {
			mark := ""
			if t.Name == coordinator {
				mark = "  <- the coordinator"
			}
			fmt.Fprintf(&b, "           %-16s blind for %s%s\n", t.Name, t.Age(now).Round(time.Minute), mark)
			if t.Why != "" {
				fmt.Fprintf(&b, "           %s\n", t.Why)
			}
		}
	}

	last := "never"
	if !snap.SampledAt.IsZero() {
		last = fmt.Sprintf("%s (%s ago)", snap.SampledAt.UTC().Format(time.RFC3339),
			now.Sub(snap.SampledAt).Round(time.Minute))
	}
	fmt.Fprintf(&b, "\nDetector: %s\n  last completed sample: %s\n  population that sample examined: %d\n",
		snap.Detector, last, snap.Examined)

	if escalate {
		b.WriteString("\nWHY THIS WAS ESCALATED: a finding about a detector — stopped, or examining\n")
		b.WriteString("nobody — is a statement about this daemon's instrument set, not a task for the\n")
		b.WriteString("coordinator. And a finding naming the coordinator cannot be delivered to it: that\n")
		b.WriteString("message arrives only when the claim is false.\n")
	}

	b.WriteString("\nWHAT TO DO WITH IT. Read the raw stream and the detector's own view:\n")
	b.WriteString("  pogo events list --since=24h --type=wedge_watch_error --json | jq -r '.[].details.error' | sort | uniq -c\n")
	b.WriteString("A repeated blind reason usually means the agent's harness renamed the status line\n")
	b.WriteString("the detector parses. Fixing that is what restores judgement; raising a threshold\n")
	b.WriteString("does nothing, because nothing was measured.\n")
	b.WriteString("\nREPORT-ONLY. Nothing was nudged, restarted or stopped. This does NOT route\n")
	b.WriteString("wedge-watch's findings — escalating a confirmed fleet-level wedge outside the\n")
	b.WriteString("wedged party is mg-fc8d item (3) and remains unruled.\n")
	return b.String()
}
