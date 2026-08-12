package wedgewatch

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
//
// Note what is absent and cannot be configured: a MailFunc. mg-fc8d's item (3)
// — escalate a fleet-level wedge OUTSIDE the wedged party — is an alerting
// policy reserved to Daniel and unruled at the time of writing, so this runner
// holds no seam through which it could pick a recipient. It emits and it
// remembers; consumption is somebody else's decision. See the package doc.
type Options struct {
	// Source produces the snapshot. Required.
	Source SourceFunc
	// Emit writes wedge_watch_* events. Defaults to events.Emit.
	Emit Emitter
	// Interval is the sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// RenotifyAfter is how long an UNCHANGED roster stays quiet before the
	// finding is emitted again. Zero means DefaultRenotifyAfter.
	RenotifyAfter time.Duration
	// Thresholds tunes the two checks. Zero fields take package defaults.
	Thresholds Thresholds
	// Enabled arms the runner.
	Enabled bool
}

// Watcher is the standing detector for an agent that is animating but not
// working: it rides pogod's heartbeat, samples every agent's PTY and uptime,
// and records a finding when either the enumerated-marker check or the
// counter/uptime cross-check confirms past its hold-down.
//
// It rides the heartbeat rather than a launchd timer for the same reason
// internal/ackwatch, internal/deafwatch and internal/driftwatch do: the
// nondemand-spawn wedge on this box (mg-50e0) leaves launchd timers silently
// never firing, which for a wedge detector would be a particularly complete
// joke.
//
// REPORT-ONLY, and more strictly so than its siblings. It does not nudge,
// dismiss, stop, restart or re-dispatch — and it does not mail either, because
// choosing who hears about a fleet-wide wedge is mg-fc8d item (3) and that
// decision is Daniel's.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	renotifyAfter time.Duration
	th            Thresholds
	source        SourceFunc
	emit          Emitter

	mu      sync.Mutex
	lastRun time.Time
	ran     bool

	// counters remembers each agent's declared-work counter and when it last
	// CHANGED. The freeze clock is the whole cross-check: see Discrepancy.
	counters map[string]counterMemory
	// pending records agents already announced as in-hold-down, so the event
	// log distinguishes "we saw it and waited" from "we never saw it" without
	// re-emitting every interval.
	pending map[string]bool
	// fired records agents currently reported, so a recovery can be recorded
	// once rather than a silence that leaves the reader holding an open
	// incident forever.
	fired map[string]Finding
	// lastConnFailure is the fleet's memory of the most recent connectivity
	// failure seen on ANY agent. It is what merges an outage with a 401 that
	// surfaces afterwards — the single most important piece of state here, and
	// the reason the two 2026-08-04 observations were mistaken for two events
	// when they were split across two observers.
	lastConnFailure time.Time

	lastPrint  string
	lastEmit   time.Time
	latest     []Finding
	latestSeen time.Time
}

type counterMemory struct {
	// declared is the last successfully parsed value.
	declared time.Duration
	// since is when that value was FIRST observed in the current unbroken run.
	// A change resets it, so a flap restarts the hold-down rather than
	// accumulating toward it.
	since time.Time
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
	renotify := opts.RenotifyAfter
	if renotify <= 0 {
		renotify = DefaultRenotifyAfter
	}
	return &Watcher{
		enabled:       opts.Enabled,
		interval:      interval,
		renotifyAfter: renotify,
		th:            opts.Thresholds.withDefaults(),
		source:        opts.Source,
		emit:          emit,
		counters:      map[string]counterMemory{},
		pending:       map[string]bool{},
		fired:         map[string]Finding{},
	}
}

// Check runs one sample subject to the throttle. It is the integration point
// for pogod's heartbeat OnTick and a no-op on all but the first tick of each
// interval.
func (w *Watcher) Check(now time.Time) {
	if w == nil || !w.enabled || w.source == nil {
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

// Latest returns the findings from the most recent completed sample and when it
// was taken. It is the read path for whoever ends up consuming this — pogod
// logs it today; a router would read it once item (3) is ruled.
func (w *Watcher) Latest() ([]Finding, time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Finding(nil), w.latest...), w.latestSeen
}

// LastConnectivityFailure exposes the fleet's outage memory, so an operator can
// see WHY a 401 was or was not merged into one signature.
func (w *Watcher) LastConnectivityFailure() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastConnFailure
}

func (w *Watcher) sample(now time.Time) {
	snap, err := w.source(now)
	if err != nil {
		// A fleet that cannot be read is a real failure, not a clean scan. This
		// detector exists because instruments read healthy when they could not
		// see; making its own blindness silent would be the same bug one level
		// up.
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Details:   map[string]any{"error": err.Error(), "phase": "source"},
		})
		return
	}

	// Fold connectivity observations into the fleet memory FIRST, so a 401 seen
	// on one agent in the same sample as ENOTFOUND on another is merged rather
	// than classified in isolation.
	for _, o := range snap.Agents {
		if hasSig(Signatures(ScanMarkers(o.Output, nil)), SigConnectivity) {
			w.noteConnFailure(now)
			break
		}
	}

	var confirmed []Finding
	var pendingNow []pendingNote
	var blind []blindNote

	for _, o := range snap.Agents {
		f, state := w.inspect(o, snap.Cred, snap.Host, now)
		switch state.kind {
		case stateConfirmed:
			confirmed = append(confirmed, f)
		case statePending:
			pendingNow = append(pendingNow, pendingNote{obs: o, why: state.why})
		case stateBlind:
			blind = append(blind, blindNote{obs: o, why: state.why})
		}
	}

	sort.Slice(confirmed, func(i, j int) bool { return confirmed[i].Name < confirmed[j].Name })

	w.emitBlind(blind, now)
	w.emitPending(pendingNow, confirmed, now)
	w.recordCleared(confirmed, now)
	w.record(snap, confirmed, now)
}

type stateKind int

const (
	stateHealthy stateKind = iota
	statePending
	stateConfirmed
	// stateBlind is "I could not judge this agent" — no counter and no event-log
	// fallback. It is never folded into healthy.
	stateBlind
)

type inspectState struct {
	kind stateKind
	why  string
}

type pendingNote struct {
	obs Observation
	why string
}

type blindNote struct {
	obs Observation
	why string
}

// inspect judges one agent against both checks and folds the freeze clock.
func (w *Watcher) inspect(o Observation, cred CredentialView, host HostView, now time.Time) (Finding, inspectState) {
	hits := ScanMarkers(o.Output, nil)
	sigs := Signatures(hits)

	declared, declaredRead := ParseDeclaredWork(o.Output)
	frozenFor := w.foldCounter(o.Name, declared, declaredRead, now)

	stalledFor, stallSource, established := stallOf(o, declaredRead, frozenFor, now)

	f := Finding{
		Name:         o.Name,
		Identity:     o.identity(),
		Type:         o.Type,
		Uptime:       o.Uptime,
		Declared:     declared,
		DeclaredRead: declaredRead,
		StalledFor:   stalledFor,
		StallSource:  stallSource,
		Animating:    !o.LastOutputAt.IsZero() && now.Sub(o.LastOutputAt) <= w.th.AnimatingWithin,

		HostReadable:  host.Readable,
		HostSaturated: host.Saturated,
		HostUsedCores: host.UsedCores,
		HostCores:     host.Cores,
	}

	discrepancy, discrepancyWhy := Discrepancy(DiscrepancyInput{
		Uptime:       o.Uptime,
		Declared:     declared,
		DeclaredRead: declaredRead,
		FrozenFor:    frozenFor,
	}, w.th)

	markerFinding := len(sigs) > 0 && established && stalledFor >= w.th.MarkerHoldDown

	if !discrepancy && !markerFinding {
		switch {
		case !established:
			return f, inspectState{kind: stateBlind, why: stallSource}
		case len(sigs) > 0 || (declaredRead && frozenFor > 0):
			// Something is off but not yet old enough to report.
			why := discrepancyWhy
			if len(sigs) > 0 {
				why = "an enumerated dead-end marker is on screen; waiting out the " +
					w.th.MarkerHoldDown.String() + " hold-down, because an agent merely WRITING about " +
					"one of these strings puts it in its own PTY"
			}
			return f, inspectState{kind: statePending, why: why}
		case !declaredRead:
			// The event fallback established a CLOCK, not a verdict. With no
			// counter and no marker there is nothing for that clock to time,
			// and this agent cannot be judged. See blindOnFallbackAlone.
			return f, inspectState{kind: stateBlind, why: blindOnFallbackAlone(stalledFor)}
		default:
			return f, inspectState{kind: stateHealthy}
		}
	}

	if discrepancy {
		sigs = append(sigs, SigDeclaredTimeBelowUptime)
	}
	f.Signatures = sortSigs(sigs)

	v := Classify(Evidence{
		Signatures:      f.Signatures,
		LastConnFailure: w.connMemory(),
		Cred:            cred,
		Host:            host,
		Now:             now,
	}, w.th)
	f.Cause = v.Cause
	f.Response = v.Response
	f.Why = v.Why
	if discrepancy {
		f.Why += " Cross-check: " + discrepancyWhy + "."
	}
	return f, inspectState{kind: stateConfirmed}
}

// blindOnFallbackAlone is the message for an agent whose ONLY signal is the
// event-log fallback: no parseable counter, no dead-end marker on screen.
//
// It is a separate sentence from stallOf's two because it reports a different
// fact. Those two say the detector had no clock at all. This one says it HAS a
// clock and still cannot reach a verdict — which is the harder thing to explain
// and the reason drellem2/pogo#138 sat unnoticed: `established` reads true, so
// the agent walked past every guard into the healthy default.
//
// The age is included because it is genuinely established and because it is
// what an operator needs in order to decide whether to go and look. What the
// sentence must NOT do is present the age as a verdict: event-log silence
// distinguishes a wedged agent from a busy one only when something else says
// the agent OUGHT to be producing events, and with no counter and no marker
// nothing here says that. An idle agent between turns and an agent wedged
// behind a modal produce the same reading.
func blindOnFallbackAlone(stalledFor time.Duration) string {
	return "no declared-work counter could be parsed and no dead-end marker is on screen, so the " +
		"only signal left is the event log's — whose newest entry for this identity is " +
		stalledFor.Round(time.Second).String() + " old. Event-log age ALONE cannot separate a " +
		"wedged agent from an idle one: the fallback can time a marker's hold-down, it cannot " +
		"produce a verdict by itself. The agent could NOT be judged, which is not the same as healthy"
}

// stallOf establishes how long an agent has shown no evidence of progress.
//
// The primary signal is the frozen counter. The fallback — event-log silence —
// exists only for when the counter cannot be parsed at all, so that a harness
// that renames its status line degrades this detector to a coarser one rather
// than to a silent one. If neither is available the agent is BLIND, never
// healthy.
//
// What this function returns is a CLOCK, and `established` means a clock was
// found — not that the agent can be judged. Those came apart in mg-20eb and
// drellem2/pogo#138 is the bill: an agent with an unreadable counter and no
// marker gets `established=true` from the fallback here, which was enough to
// carry it past inspect's blind branch and into the healthy default. The answer
// space went from {healthy, stalled, blind} to {healthy, stalled}, so "I cannot
// judge this" had nowhere to land and collapsed into "healthy" at ANY
// staleness. inspect now re-tests declaredRead for that reason; see
// blindOnFallbackAlone.
//
// The three blind messages here are three DIFFERENT facts and are worded apart
// on purpose. Until mg-20eb there was one, asserting that the event log held no
// entry for the identity — and nothing in production ever wrote EventsLastSeen,
// so that sentence was a constant printed by a detector that had never opened
// the log. Most of the identities it said that about had entries. A message
// that can be checked and found false is how a detector's output stops being
// read, so each branch now says only what was actually established.
func stallOf(o Observation, declaredRead bool, frozenFor time.Duration, now time.Time) (time.Duration, string, bool) {
	if declaredRead {
		return frozenFor, "counter_frozen", true
	}
	if !o.EventsLastSeen.IsZero() {
		return now.Sub(o.EventsLastSeen), "events_stale", true
	}
	if o.EventsRead {
		return 0, "no declared-work counter could be parsed and the event log, which WAS read, has no " +
			"entry for this identity — the agent could NOT be judged, which is not the same as healthy", false
	}
	return 0, "no declared-work counter could be parsed and no event-log fallback is available — " +
		"the agent could NOT be judged, which is not the same as healthy", false
}

// foldCounter updates the freeze clock and returns how long the current value
// has held.
func (w *Watcher) foldCounter(name string, declared time.Duration, ok bool, now time.Time) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !ok {
		// An unreadable counter clears the memory: a later successful parse
		// starts a fresh freeze clock rather than inheriting credit for a gap
		// nobody observed.
		delete(w.counters, name)
		return 0
	}
	mem, known := w.counters[name]
	if !known || mem.declared != declared {
		w.counters[name] = counterMemory{declared: declared, since: now}
		return 0
	}
	return now.Sub(mem.since)
}

func (w *Watcher) noteConnFailure(now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if now.After(w.lastConnFailure) {
		w.lastConnFailure = now
	}
}

func (w *Watcher) connMemory() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastConnFailure
}

func (w *Watcher) emitBlind(blind []blindNote, now time.Time) {
	for _, b := range blind {
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Timestamp: now.UTC().Format(time.RFC3339Nano),
			Details: map[string]any{
				"target":   b.obs.Name,
				"identity": b.obs.identity(),
				"phase":    "judge",
				"error":    b.why,
			},
		})
	}
}

func (w *Watcher) emitPending(pendingNow []pendingNote, confirmed []Finding, now time.Time) {
	confirmedNames := map[string]bool{}
	for _, f := range confirmed {
		confirmedNames[f.Name] = true
	}
	live := map[string]bool{}
	for _, p := range pendingNow {
		live[p.obs.Name] = true
	}

	w.mu.Lock()
	var toEmit []pendingNote
	for _, p := range pendingNow {
		if !w.pending[p.obs.Name] {
			w.pending[p.obs.Name] = true
			toEmit = append(toEmit, p)
		}
	}
	for name := range w.pending {
		if !live[name] {
			delete(w.pending, name)
		}
	}
	w.mu.Unlock()

	for _, p := range toEmit {
		if confirmedNames[p.obs.Name] {
			continue
		}
		w.emit(events.Event{
			EventType: EventPending,
			Agent:     "pogod",
			Timestamp: now.UTC().Format(time.RFC3339Nano),
			Details: map[string]any{
				"target":   p.obs.Name,
				"identity": p.obs.identity(),
				"uptime":   p.obs.Uptime.String(),
				"why":      p.why,
			},
		})
	}
}

// recordCleared emits once for each agent that was reported and no longer is.
// An alarm with no all-clear leaves its reader holding an open incident
// forever, which is how a detector's output stops being read.
func (w *Watcher) recordCleared(confirmed []Finding, now time.Time) {
	live := map[string]bool{}
	for _, f := range confirmed {
		live[f.Name] = true
	}
	w.mu.Lock()
	var cleared []Finding
	for name, f := range w.fired {
		if !live[name] {
			cleared = append(cleared, f)
			delete(w.fired, name)
		}
	}
	for _, f := range confirmed {
		w.fired[f.Name] = f
	}
	w.mu.Unlock()

	sort.Slice(cleared, func(i, j int) bool { return cleared[i].Name < cleared[j].Name })
	for _, f := range cleared {
		w.emit(events.Event{
			EventType: EventCleared,
			Agent:     "pogod",
			Timestamp: now.UTC().Format(time.RFC3339Nano),
			Details: map[string]any{
				"target":   f.Name,
				"identity": f.identity(),
				"cause":    string(f.Cause),
				"why":      "the agent's declared work counter advanced again, or the dead-end marker left the screen",
			},
		})
	}
}

// record stores the roster and emits it, throttled by fingerprint so an
// unchanged roster stays quiet until RenotifyAfter. Ages advance every tick, so
// folding them into the fingerprint would re-emit every interval and train
// every reader to filter this detector out — internal/ackwatch's reasoning,
// inherited.
func (w *Watcher) record(snap Snapshot, confirmed []Finding, now time.Time) {
	w.mu.Lock()
	w.latest = append([]Finding(nil), confirmed...)
	w.latestSeen = now
	print := fingerprint(confirmed)
	shouldEmit := len(confirmed) > 0 && (print != w.lastPrint || now.Sub(w.lastEmit) >= w.renotifyAfter)
	if shouldEmit {
		w.lastPrint = print
		w.lastEmit = now
	}
	if len(confirmed) == 0 {
		w.lastPrint = ""
	}
	connAt := w.lastConnFailure
	w.mu.Unlock()

	if !shouldEmit {
		return
	}

	rendered := make([]map[string]any, 0, len(confirmed))
	for _, f := range confirmed {
		rendered = append(rendered, map[string]any{
			"name":           f.Name,
			"identity":       f.identity(),
			"type":           f.Type,
			"uptime":         f.Uptime.Round(time.Second).String(),
			"declared":       f.Declared.Round(time.Second).String(),
			"declared_read":  f.DeclaredRead,
			"stalled_for":    f.StalledFor.Round(time.Second).String(),
			"stall_source":   f.StallSource,
			"animating":      f.Animating,
			"signatures":     sigStrings(f.Signatures),
			"host_readable":  f.HostReadable,
			"host_saturated": f.HostSaturated,
			"cause":          string(f.Cause),
			"response":       string(f.Response),
			"why":            f.Why,
		})
	}
	details := map[string]any{
		"count":    len(confirmed),
		"scanned":  snap.Scanned,
		"judged":   len(snap.Agents),
		"agents":   names(confirmed),
		"findings": rendered,
		// Stated on every emission because it is the input that decides whether
		// a 401 reads as an outage artifact or as a credential fault, and a
		// reader who cannot see it cannot check the verdict.
		"cred_readable":      snap.Cred.Readable,
		"cred_refresh_valid": snap.Cred.RefreshValid,
		// Likewise stated on every emission: host headroom is what separates a
		// wedged agent from a merely starved one, and the two need opposite
		// handling.
		"host_readable":   snap.Host.Readable,
		"host_saturated":  snap.Host.Saturated,
		"host_used_cores": snap.Host.UsedCores,
		"host_cores":      snap.Host.Cores,
		"routed_to": "nobody — mg-fc8d item (3), escalation outside the wedged party, is an " +
			"alerting-policy decision reserved to Daniel and is deliberately NOT built here",
	}
	if !connAt.IsZero() {
		details["last_connectivity_failure"] = connAt.UTC().Format(time.RFC3339Nano)
	}
	w.emit(events.Event{
		EventType: EventFired,
		Agent:     "pogod",
		Timestamp: now.UTC().Format(time.RFC3339Nano),
		Details:   details,
	})
}

func sigStrings(sigs []Signature) []string {
	out := make([]string, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, string(s))
	}
	return out
}

func (o Observation) identity() string {
	if o.Identity != "" {
		return o.Identity
	}
	return o.Name
}
