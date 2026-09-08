// Package midsessionwedge detects — and releases — the agent that wedged AFTER
// a healthy start, with its next action sitting unsubmitted in the composer.
//
// # THE FAILURE, AND WHY EVERY EXISTING INSTRUMENT READS GREEN OVER IT
//
// A polecat starts cleanly, works, and then a delivery lands in its composer
// that the harness never submits. The agent has produced output, has a fresh
// transcript, and is idle in a way that is indistinguishable from thinking.
// Nothing recovers it.
//
// This is NOT the unstarted case. internal/agent/startverify covers that one —
// per-spawn, one-shot, gated on a started-signal (work item unclaimed, claim pid
// not restamped, no ready composer), every one of which is satisfied long before
// this failure begins. The rest of the tree misses it for reasons documented in
// docs/design/midsession-wedge-notes.md and summarised here so this file stands
// alone: internal/wedgewatch is report-only and string-matching over dead-end
// markers, and a wedge at a healthy composer prints no marker; internal/turnwatch
// works at 3h staleness with a 30m hold-down, so a 17-minute wedge sits entirely
// inside its blind spot; internal/progresswatch is fleet-level with a floor of
// three workers, and one wedged polecat is below its population by construction;
// and diagnoseAgentAt's StallThresholdPolecat is measured from
// RingBuffer.LastWriteTime, which a repainting TUI keeps fresh.
//
// # THE SPINNER TRAP, WHICH IS THE FIRST THING TO GET RIGHT
//
// The obvious instrument is a hash of the PTY ring: hash it every tick, and an
// agent whose hash holds is doing nothing. The trap is that A SPINNER IS PTY
// OUTPUT. Claude Code redraws an animated status line — with an elapsed-second
// counter in it — while it works, so the ring hash of a busy agent changes
// constantly, and mg-fc8d's thirteen-hour login-prompt wedge went undetected for
// exactly the mirror-image reason: the agents were ANIMATING at a dead prompt.
//
// The two readings were taken separately on this box before anything here was
// written (2026-09-08, 2s sampling over the live fleet, full 64KB rings):
//
//   - PARKED agent (turn over, composer rendered, no spinner): the ring is
//     BYTE-IDENTICAL sample to sample — not slow-changing, identical — for as
//     long as the park lasts: 5.5 to 10.6 minutes for the crew agents between
//     cron fires, and 19.0 minutes for a polecat holding after `mg done`.
//     `last-activity` ages monotonically across the whole run, i.e. nothing is
//     writing to that PTY at all.
//   - SPINNING agent (mid-turn): the ring changed on 100.0% of the 2.19s
//     samples and 99.9% of the 0.44s ones, and `last-activity` reads "just now"
//     throughout.
//
// A correction to mg-daf4's notes falls out of the first reading, and it is
// recorded because the notes' argument for preferring a ring HASH over
// RingBuffer.LastWriteTime rests on it. The notes reason that an identical
// repaint frame leaves the 64KB window unchanged while `lastWrite` advances, so
// repaint-without-progress is visible to the hash and invisible to the timer.
// That case was NOT reproduced here: every parked agent measured was silent, so
// the hash and the timer agreed on all of them. The hash is kept because it
// DOMINATES — it is never worse, and it covers the reported-but-unreproduced
// case — not because a measurement showed it winning.
//
// So the hash separates parked from working, and the asymmetry is the whole
// point: CHANGED across any interval is conclusive liveness; STATIC only means
// "nothing is happening", which is what a healthy finished agent looks like too.
// The spinner therefore only ever EXONERATES here. That direction is safe. The
// residue it leaves is declared at the bottom of this comment.
//
// # WHY QUIESCENCE ALONE CANNOT BE THE CRITERION
//
// A polecat that has finished its work and is holding for the coordinator —
// every polecat's normal end state — is parked at an empty composer with a
// byte-identical ring, indefinitely, by design. A quiescence timer alone fires
// on all of them. No threshold fixes that, because there is no duration a
// healthy hold does not reach.
//
// What separates the wedge from the hold is that in the wedge A SUBMIT IS OWED.
// pogod knows when it owes one, and already records it: deliverConfirmed
// (internal/agent/nudge.go) writes a nudge to a mid-turn agent, gets no
// submission receipt — Claude Code emits no UserPromptSubmit for a prompt typed
// into the middle of a turn — and returns ErrNudgeQueued, correctly declining to
// resend something that very probably landed. internal/agent/queuednudge.go
// makes that moment durable: the timestamp, and the receipt count the obligation
// is measured against.
//
// The criterion is the CONJUNCTION:
//
//	a submit is owed (a mid-turn delivery went unconfirmed)
//	AND the receipt count has not moved since
//	AND the PTY ring has been byte-identical for Quiescence
//	AND the agent's worktree has not moved either.
//
// Each clause is an independent witness, and every one of them can only clear
// the alarm, never raise it on its own.
//
// # CHEAPEST FIRST, AND THE ONE POSITIVE-ONLY SIGNAL
//
// The checks run in increasing cost, so the expensive one is rare by
// construction:
//
//  1. Receipt count (one file read; already implemented as agent.CountSubmits).
//     Moved => the composer drained. Conclusive, and a POSITIVE observation of
//     the harness submitting rather than an inference from silence.
//  2. Ring hash (in-process, no syscall). Changed => alive. Conclusive.
//  3. Worktree movement (a few stats). The mayor's instruction was that this
//     "runs first"; it runs THIRD, and the difference is cost accounting rather
//     than disagreement. The intent — exonerate cheaply before concluding
//     anything — is what the ordering implements, and the two clauses above it
//     are strictly cheaper: one file read and an in-process hash, against six
//     stats and a gitdir resolution. Putting the stats first would spend them on
//     every agent on every tick, including the ones a free clause was about to
//     clear anyway. This is the mayor's 2026-09-08 signal,
//     which settled a live case: a commit timestamped one minute earlier proved
//     an agent that read health=idle/idle_seconds=0 was alive and about to
//     submit. It is used ONLY to exonerate, and the limit is the reason: a fresh
//     commit proves recent life, and THE ABSENCE OF ONE PROVES NOTHING AT ALL —
//     an agent thinking hard between commits is byte-identical to an agent
//     wedged between commits, and commits are rare events. Boundary-only
//     progress logging is the classic version of that trap. So it runs last,
//     only over an agent the first two have already failed to clear, and it
//     never appears in the incriminating direction.
//
// # WHAT IT DOES ON THE CONJUNCTION
//
// It delivers a BARE SUBMIT TERMINATOR — Agent.Nudge(""), the same payload
// deliverConfirmed's step 2 sends and the same thing that released pae5a.
// A bare return carries no content, so it submits whatever is loaded and CANNOT
// duplicate anything; that property is why it is the only action taken here.
// Attempts are bounded (DefaultMaxAttempts), every one emits an event, and the
// recovery is confirmed by the receipt count moving — never by the nudge
// returning nil, which only says the bytes left pogod.
//
// It DECLINES LOUDLY rather than firing blind. An agent with no
// submission-receipt hook cannot own a submit, so it is skipped with an event
// saying so, not judged. That distinction is internal/blindwatch's founding
// bug one level out: an instrument that cannot judge must not read as clean.
//
// # DECLARED RESIDUE
//
// It does NOT catch a wedge whose PTY keeps receiving bytes from something
// unrelated to the agent's own progress — a background shell, or another nudge
// landing in the composer. That is mg-daf4's "an instrument that cannot fail
// because something unrelated keeps satisfying it", one level down, and it is
// the first thing to attack when this is revisited.
//
// It also only sees deliveries pogod made THROUGH deliverConfirmed. A prompt a
// human typed into an attached terminal and never submitted owes nothing that
// pogod recorded, and is invisible here.
package midsessionwedge

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Defaults.
const (
	// DefaultInterval is the gap between samples. It must be much finer than
	// Quiescence, because the quiet run is measured from the last tick at which
	// the hash was seen to CHANGE, so the interval is the resolution of the
	// threshold as well as its sampling rate.
	DefaultInterval = 30 * time.Second

	// DefaultQuiescence is how long an agent's PTY ring must be byte-identical,
	// with a submit owed, before the composer is judged stuck.
	//
	// THE NUMBER IS MEASURED, and that is the whole reason mg-daf4 shipped no
	// detector: "any threshold that was measured rather than picked" was the
	// named gap, and its own intended pick of 8 minutes rested on argument (c)
	// below and on nothing else.
	//
	// Measured on this box 2026-09-08 over the live fleet: eight agents' FULL
	// 64KB rings sampled every 2.19s for 20.4 minutes (561 readings each), plus
	// one continuously-working agent's ring and receipt count sampled TOGETHER
	// every 0.44s for 10.3 minutes (1390 readings). Three readings came out of
	// it, and they have to be stated separately because the ticket's trap is
	// that a spinner IS output:
	//
	//	(a) A WORKING agent's ring never holds still. It changed on 100.0% of the
	//	    2.19s samples and 99.9% of the 0.44s ones; its longest byte-identical
	//	    run across 571 completed runs was 2.35s at the coarse cadence and
	//	    0.42s at the fine one — one sampling period, i.e. the resolution of
	//	    the instrument rather than a property of the agent.
	//	(b) A PARKED agent's ring is byte-identical for tens of minutes: 5.5,
	//	    9.7, 9.8, 9.8, 10.6 minutes for the crew agents, and 19.0 minutes for
	//	    a polecat holding after `mg done`. Fleet-wide, 11 of 788 completed
	//	    quiet runs (1.40%) reached five minutes, and EVERY ONE of them
	//	    belonged to an idle agent with nothing owed — the population the
	//	    Owed clause excludes by construction, which is why that clause and
	//	    not this number is what keeps a finished polecat safe.
	//	(c) At every submit observed from a working agent, the ring had been
	//	    quiet 0.00s: the composer drains at the instant the turn ends.
	//
	// So the ring going quiet at all is a reliable turn-end signal for the one
	// population that can owe a submit, and five minutes is ~700x the measured
	// working-agent ceiling of 0.42s. It is also strictly INSIDE the fleet's
	// */10 mail-check cadence, so a wedged agent reaches the threshold within
	// one cron gap instead of having its quiet run reset by every fire — which
	// is mg-daf4's argument, kept, but now as the second reason rather than the
	// only one.
	//
	// WHAT IS NOT MEASURED, because it decides how far this number should be
	// trusted: the JOINT distribution — quiet run AND submit owed — over the
	// queued population. The fleet produced zero queued deliveries during the
	// observation window; the current rate is 0.2-0.8/hour. Historically 3611
	// queued deliveries are recorded, of which 1558 join to a later receipt with
	// p50 19.3 min and 16.6% still unsubmitted an hour later, but that latency
	// spans the whole spinning period too and cannot be split into "still
	// working" and "parked with the prompt loaded" without ring history nobody
	// kept. Reading (a) is what stands in for that split, and it is an inference
	// from the working-agent ceiling rather than a direct observation of the
	// rare case.
	DefaultQuiescence = 5 * time.Minute

	// DefaultMaxAttempts bounds the bare returns sent to one agent for one owed
	// submit. Bounded for startverify's reason: a dead agent, or one whose work
	// was cancelled out from under it, must not draw an unbounded stream of
	// stray keystrokes.
	DefaultMaxAttempts = 3

	// DefaultRenotifyAfter floors how often ONE agent's exhausted-recovery
	// notice is mailed.
	//
	// It is not decoration. The attempt budget is per OWED SUBMIT, and a new
	// owed submit arrives whenever pogod nudges the agent again — which for an
	// agent on the fleet's */10 mail-check cadence is every ten minutes,
	// forever. Without a floor a single wedged agent mails the coordinator six
	// times an hour, and this tree has watched a correct detector fire every
	// five minutes for thirteen hours into an inbox nobody was reading. The
	// bare returns themselves stay unfloored: they are harmless keystrokes and
	// each new delivery is a genuinely new thing to try to submit.
	DefaultRenotifyAfter = 6 * time.Hour

	// DefaultSettle is how long recovery waits for the receipt count to move
	// before recording the attempt as unconfirmed. It matches the confirm path's
	// minConfirmStep floor: a submit that is going to register does so at once,
	// and waiting longer converts no failure into a success.
	DefaultSettle = 2 * time.Second

	EventFired       = "midsession_wedge_fired"
	EventRecovered   = "midsession_wedge_recovered"
	EventUnrecovered = "midsession_wedge_unrecovered"
	EventExonerated  = "midsession_wedge_exonerated"
	EventSkipped     = "midsession_wedge_skipped"
	EventError       = "midsession_wedge_error"
)

// Reading is one agent's mid-session state at one instant. Everything the
// judgement needs is here, so a test drives the whole decision with no registry,
// no PTY and no filesystem.
type Reading struct {
	// Name is the agent name (not the pogo-cat-<name> display label).
	Name string
	// Kind is "polecat" or "crew". Reported, never judged on: the mechanism is
	// type-independent, and a crew agent was observed in this exact state while
	// the detector was being written.
	Kind string
	// Digest is a hash of the agent's FULL PTY ring. Empty means the ring could
	// not be read, which is a decline, not a quiescence.
	Digest string
	// Submits is the harness-written submission-receipt count.
	Submits int
	// HasReceipt is whether pogod installed a receipt hook it could resolve.
	// False means this agent cannot own a submit and must not be judged.
	HasReceipt bool
	// Owed is the outstanding unconfirmable delivery, or nil when nothing is
	// owed. OwedSubmits is the receipt count at that delivery.
	Owed        bool
	OwedAt      time.Time
	OwedSubmits int
}

// SourceFunc produces one reading per live agent.
type SourceFunc func(now time.Time) ([]Reading, error)

// RecoverFunc delivers a bare submit terminator to the named agent.
type RecoverFunc func(name string) error

// SubmitsFunc re-reads one agent's receipt count, so a recovery is confirmed by
// the harness rather than by the nudge returning nil.
type SubmitsFunc func(name string) (int, error)

// Movement is what the worktree probe saw.
type Movement struct {
	// Moved is whether the newest mtime is at or after the quiet run's start.
	Moved bool
	// At is that newest mtime.
	At time.Time
	// Path names WHICH file carried it, relative to the worktree. It is
	// reported because this probe can only ever exonerate: a path that moves for
	// a reason unrelated to the agent's progress would suppress every finding
	// for every polecat, silently and forever. In the log it is the same
	// filename every time, which is a failure somebody can see.
	Path string
}

// WorktreeFunc reports whether the named agent's worktree moved at or after
// `since`. ok=false means there is no worktree to read (a crew agent, or a
// polecat spawned with --no-worktree) and carries NO judgement either way.
type WorktreeFunc func(name string, since time.Time) (Movement, bool)

// MailFunc delivers a notice.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	Enabled bool
	// Source produces the reading. Required.
	Source SourceFunc
	// Recover delivers the bare return. Nil makes the watcher REPORT-ONLY: it
	// still detects, emits and mails, and it says so in the event.
	Recover RecoverFunc
	// Submits re-reads the receipt count to confirm a recovery. Required
	// whenever Recover is set — without it a recovery could only be assumed.
	Submits SubmitsFunc
	// Worktree is the positive-only exonerating probe. Nil disables it, which
	// costs sensitivity in the safe direction only.
	Worktree WorktreeFunc
	// Mail delivers the notice raised when the bounded attempts are exhausted.
	// Nil means no mail; detection still emits.
	Mail MailFunc
	// NotifyTo is the mailbox for an exhausted recovery. Empty means no mail.
	NotifyTo string
	// From is the sender name on that mail.
	From string
	// Emit writes midsession_wedge_* events. Defaults to events.Emit.
	Emit Emitter

	Interval    time.Duration
	Quiescence  time.Duration
	MaxAttempts int
	Settle      time.Duration
	// RenotifyAfter floors how often one agent's exhausted-recovery notice is
	// mailed. Zero means DefaultRenotifyAfter; NEGATIVE removes the floor,
	// which only tests should do.
	RenotifyAfter time.Duration
	// StartedAt suppresses judgement for one Quiescence after pogod starts. A
	// restart leaves the watcher with no hash history, so the first tick would
	// otherwise measure every agent's quiet run from zero and judge nobody, or
	// — worse, if the clock were seeded from process start — judge everybody.
	StartedAt time.Time
}

// track is the watcher's per-agent memory between ticks.
type track struct {
	digest string
	// since is when the current digest was FIRST seen. The quiet run is
	// now-since, so it is bounded below by the sampling interval and never
	// inferred from a timestamp the agent controls.
	since time.Time
	// owedAt identifies which owed submit the attempts belong to, so a NEW
	// queued nudge starts a fresh budget instead of inheriting a spent one.
	owedAt   time.Time
	attempts int
	fired    bool
	// skipNoted latches the no-receipt-signal decline, so the fact is stated
	// once per agent per pogod run rather than on every tick. Un-latched agents
	// are the ones nobody has been told about yet.
	skipNoted bool
}

// Watcher rides pogod's heartbeat.
//
// It rides the heartbeat rather than a launchd timer for the reason every
// detector in this tree does: the nondemand-spawn wedge on this box leaves
// launchd timers silently never firing, which for a detector of things that
// silently stopped happening would be especially apt.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	quiescence    time.Duration
	maxAttempts   int
	settle        time.Duration
	renotifyAfter time.Duration
	notifyTo      string
	from          string
	startedAt     time.Time

	source   SourceFunc
	recover  RecoverFunc
	submits  SubmitsFunc
	worktree WorktreeFunc
	mail     MailFunc
	emit     Emitter

	mu      sync.Mutex
	lastRun time.Time
	ran     bool
	// sampling is set for the whole of one sample, INCLUDING the recovery pass,
	// which waits out a settle window per agent and so can outlast the
	// interval. Without it two samples overlap and both judge the same agent
	// against the same attempt count, delivering more bare returns than the
	// budget allows. Bare returns are harmless, but a bound that is not a bound
	// is worse than no bound: it is one somebody will cite.
	sampling bool
	tracks   map[string]*track
	// Self-report of the last completed sample. See State for why an
	// armed-and-silent detector must be readable rather than inferred.
	lastSample time.Time
	judged     int
	skipped    int
	owed       int
	// lastMailed floors the exhausted-recovery notice per agent. Kept beside
	// tracks rather than on it: a new owed submit resets the attempt budget on
	// purpose, and the mail floor must NOT be reset with it.
	lastMailed map[string]time.Time
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
	// Zero and negative must differ for the one window a test needs to turn
	// OFF. A config that simply omits the key would otherwise silently disable
	// the quiescence requirement, and a detector with no quiescence requirement
	// types a bare return into every agent that owes a submit the instant it
	// owes one — which is the state EVERY healthy mid-turn agent is in.
	quiesce := opts.Quiescence
	switch {
	case quiesce == 0:
		quiesce = DefaultQuiescence
	case quiesce < 0:
		quiesce = 0
	}
	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}
	renotify := opts.RenotifyAfter
	switch {
	case renotify == 0:
		renotify = DefaultRenotifyAfter
	case renotify < 0:
		renotify = 0
	}
	return &Watcher{
		enabled:       opts.Enabled,
		interval:      pick(opts.Interval, DefaultInterval),
		quiescence:    quiesce,
		maxAttempts:   attempts,
		settle:        pick(opts.Settle, DefaultSettle),
		renotifyAfter: renotify,
		notifyTo:      opts.NotifyTo,
		from:          opts.From,
		startedAt:     opts.StartedAt,
		source:        opts.Source,
		recover:       opts.Recover,
		submits:       opts.Submits,
		worktree:      opts.Worktree,
		mail:          opts.Mail,
		emit:          emit,
		tracks:        map[string]*track{},
		lastMailed:    map[string]time.Time{},
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

func (w *Watcher) due(now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sampling {
		return false
	}
	if w.ran && now.Sub(w.lastRun) < w.interval {
		return false
	}
	w.lastRun = now
	w.ran = true
	w.sampling = true
	return true
}

func (w *Watcher) sampleDone() {
	w.mu.Lock()
	w.sampling = false
	w.mu.Unlock()
}

func (w *Watcher) sample(now time.Time) {
	defer w.sampleDone()

	readings, err := w.source(now)
	if err != nil {
		// A fleet that could not be read is a real failure, not a clean scan.
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Details: map[string]any{
				"error": err.Error(),
				"note": "the mid-session wedge detector could not read the fleet. " +
					"No agent was judged, which is not the same as every agent being healthy.",
			},
		})
		return
	}

	// A restart leaves no hash history. Track quiet runs from the first tick,
	// but judge nobody until a full quiescence has elapsed since start — the
	// first observation of a digest establishes only that it is current, not
	// how long it has been.
	warming := !w.startedAt.IsZero() && now.Sub(w.startedAt) < w.quiescence

	sort.Slice(readings, func(i, j int) bool { return readings[i].Name < readings[j].Name })

	w.mu.Lock()
	seen := make(map[string]bool, len(readings))
	judged, skipped, owedNow := 0, 0, 0
	var act []Reading
	for _, r := range readings {
		seen[r.Name] = true
		if r.Digest == "" || !r.HasReceipt {
			skipped++
		} else {
			judged++
			if r.Owed && r.Submits <= r.OwedSubmits {
				owedNow++
			}
		}
		tr := w.tracks[r.Name]
		if tr == nil {
			tr = &track{}
			w.tracks[r.Name] = tr
		}
		// A ring that could not be read is a decline, not a quiescence. Hold the
		// quiet run where it is rather than letting an unreadable ring look like
		// a byte-identical one.
		if r.Digest == "" {
			continue
		}
		if tr.digest != r.Digest {
			tr.digest = r.Digest
			tr.since = now
		} else if tr.since.IsZero() {
			tr.since = now
		}
		if !w.judge(now, r, tr, warming) {
			continue
		}
		act = append(act, r)
	}
	for name := range w.tracks {
		if !seen[name] {
			delete(w.tracks, name)
			delete(w.lastMailed, name)
		}
	}
	w.lastSample, w.judged, w.skipped, w.owed = now, judged, skipped, owedNow
	w.mu.Unlock()

	for _, r := range act {
		w.attempt(now, r)
	}
}

// judge applies the conjunction to one reading and returns whether an attempt is
// owed. It runs under w.mu and does no I/O beyond the injected worktree probe.
func (w *Watcher) judge(now time.Time, r Reading, tr *track, warming bool) bool {
	// (0) Decline loudly, and BEFORE anything else. An agent with no
	// submission-receipt hook can never own a submit, so every clause below is
	// unreadable for it and its quiet composer would simply never be judged.
	// Saying nothing there is the shape internal/blindwatch exists to stop: an
	// instrument that CANNOT judge an agent must not be indistinguishable from
	// one that judged it healthy. Latched, so the fact is stated once per agent
	// rather than on every tick.
	if !r.HasReceipt {
		if !tr.skipNoted {
			tr.skipNoted = true
			w.emit(events.Event{
				EventType: EventSkipped,
				Agent:     r.Name,
				Details: map[string]any{
					"kind":   r.Kind,
					"reason": "no_receipt_signal",
					"note": "pogod installed no submission-receipt hook for this agent, so " +
						"whether it owes a submit CANNOT be read. It is not judged at all, " +
						"which is not the same as healthy.",
				},
			})
		}
		return false
	}
	tr.skipNoted = false

	// (1) Cheapest and conclusive: the harness submitted something since the
	// unconfirmable delivery. The composer drained; nothing is owed.
	if !r.Owed || r.Submits > r.OwedSubmits {
		if tr.fired {
			w.emit(events.Event{
				EventType: EventRecovered,
				Agent:     r.Name,
				Details: map[string]any{
					"kind":     r.Kind,
					"submits":  r.Submits,
					"attempts": tr.attempts,
					"note":     "the composer drained; the owed submit was recorded by the harness",
				},
			})
		}
		tr.fired = false
		tr.attempts = 0
		tr.owedAt = time.Time{}
		return false
	}

	// A NEW owed submit gets a fresh attempt budget: the previous one belongs to
	// a delivery that is no longer the thing sitting in the composer.
	if !r.OwedAt.Equal(tr.owedAt) {
		tr.owedAt = r.OwedAt
		tr.attempts = 0
		tr.fired = false
	}

	if warming {
		return false
	}

	// (2) Free and conclusive: the ring is still moving. A spinner counts, and
	// counting it is correct — it can only exonerate.
	quiet := now.Sub(tr.since)
	if quiet < w.quiescence {
		return false
	}

	if tr.attempts >= w.maxAttempts {
		return false
	}

	// (3) The positive-only probe, run last and only over an agent the free
	// clauses failed to clear. Movement exonerates; absence of movement is NOT
	// evidence of a wedge and contributes nothing to the judgement.
	if w.worktree != nil {
		if m, ok := w.worktree(r.Name, tr.since); ok && m.Moved {
			w.emit(events.Event{
				EventType: EventExonerated,
				Agent:     r.Name,
				Details: map[string]any{
					"kind":          r.Kind,
					"quiet_for":     quiet.String(),
					"moved_at":      m.At.Format(time.RFC3339),
					"moved_path":    m.Path,
					"note":          "worktree moved inside the quiet run; the agent is alive between commits",
					"positive_only": true,
				},
			})
			return false
		}
	}
	return true
}

// attempt delivers one bare return and confirms it from the receipt count.
func (w *Watcher) attempt(now time.Time, r Reading) {
	w.mu.Lock()
	tr := w.tracks[r.Name]
	if tr == nil {
		w.mu.Unlock()
		return
	}
	tr.attempts++
	tr.fired = true
	attempt := tr.attempts
	quiet := now.Sub(tr.since)
	w.mu.Unlock()

	details := map[string]any{
		"kind":         r.Kind,
		"quiet_for":    quiet.String(),
		"owed_since":   r.OwedAt.Format(time.RFC3339),
		"owed_for":     now.Sub(r.OwedAt).String(),
		"submits":      r.Submits,
		"owed_submits": r.OwedSubmits,
		"attempt":      attempt,
		"max_attempts": w.maxAttempts,
		"note": "PTY ring byte-identical for the quiescence window with a submit owed " +
			"and the worktree still: the composer is holding an unsubmitted instruction",
	}
	if w.recover == nil {
		details["action"] = "none_report_only"
		w.emit(events.Event{EventType: EventFired, Agent: r.Name, Details: details})
		return
	}
	details["action"] = "bare_return"
	w.emit(events.Event{EventType: EventFired, Agent: r.Name, Details: details})

	if err := w.recover(r.Name); err != nil {
		w.emit(events.Event{
			EventType: EventUnrecovered,
			Agent:     r.Name,
			Details: map[string]any{
				"kind": r.Kind, "attempt": attempt, "error": err.Error(),
			},
		})
		return
	}

	// Confirm from the harness, never from the nudge returning nil: writing to a
	// PTY master succeeds whether or not anything is listening.
	if w.submits == nil {
		w.emit(events.Event{
			EventType: EventUnrecovered,
			Agent:     r.Name,
			Details: map[string]any{
				"kind": r.Kind, "attempt": attempt,
				"error": "no submits probe: the bare return was delivered and CANNOT be confirmed",
			},
		})
		return
	}
	deadline := time.Now().Add(w.settle)
	for {
		n, err := w.submits(r.Name)
		if err == nil && n > r.OwedSubmits {
			w.emit(events.Event{
				EventType: EventRecovered,
				Agent:     r.Name,
				Details: map[string]any{
					"kind": r.Kind, "attempt": attempt, "submits": n,
					"note": "a bare return submitted the loaded message",
				},
			})
			return
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	w.emit(events.Event{
		EventType: EventUnrecovered,
		Agent:     r.Name,
		Details: map[string]any{
			"kind": r.Kind, "attempt": attempt,
			"error": fmt.Sprintf("no submission receipt within %s of the bare return", w.settle),
		},
	})
	if attempt >= w.maxAttempts {
		w.notifyExhausted(now, r, attempt, quiet)
	}
}

func (w *Watcher) notifyExhausted(now time.Time, r Reading, attempt int, quiet time.Duration) {
	if w.mail == nil || w.notifyTo == "" {
		return
	}
	w.mu.Lock()
	if last, ok := w.lastMailed[r.Name]; ok && w.renotifyAfter > 0 && now.Sub(last) < w.renotifyAfter {
		w.mu.Unlock()
		return
	}
	w.lastMailed[r.Name] = now
	w.mu.Unlock()
	subject := fmt.Sprintf("mid-session wedge: %s did not respond to %d bare returns", r.Name, attempt)
	body := fmt.Sprintf(`%s (%s) is parked at a composer holding an unsubmitted instruction.

  PTY ring byte-identical for   %s
  submit owed since             %s (%s ago)
  submit receipts               %d, unchanged since the delivery
  bare returns delivered        %d of %d, none confirmed by a receipt

The bounded recovery is spent; pogod will not type into this agent again for
this owed submit. What is NOT known from here is why the harness will not
submit — that needs a look at the session. A restart destroys the transcript
that would answer it, so read before bouncing.
`, r.Name, r.Kind, quiet, r.OwedAt.Format(time.RFC3339), now.Sub(r.OwedAt), r.Submits, attempt, w.maxAttempts)
	if err := w.mail(w.notifyTo, w.from, subject, body); err != nil {
		w.emit(events.Event{
			EventType: EventError,
			Agent:     r.Name,
			Details:   map[string]any{"error": err.Error(), "note": "exhausted-recovery notice could not be delivered"},
		})
	}
}

// Quiet reports how long the named agent's ring has been byte-identical as of
// the last completed sample, and whether the watcher has a reading for it at
// all. Exported so a diagnostic — or a consumer asking whether this detector is
// judging anyone — reads the same number the judgement does.
func (w *Watcher) Quiet(name string, now time.Time) (time.Duration, bool) {
	if w == nil {
		return 0, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	tr := w.tracks[name]
	if tr == nil || tr.since.IsZero() {
		return 0, false
	}
	return now.Sub(tr.since), true
}

// State is what this detector can say about ITSELF.
//
// It exists because of the failure shape one level out from the one this
// package detects, and the enumeration is worth stating plainly: this detector
// can only ever fire on an agent that OWES a submit, and a submit becomes owed
// only when deliverConfirmed writes into a mid-turn agent and gets no receipt.
// On a box where that never happens, this watcher runs forever, judges nobody,
// emits nothing, and is INDISTINGUISHABLE FROM A BOX WITH NO WEDGES. That is
// exactly the "instrument that cannot fail because its precondition never
// occurs" that internal/blindwatch was written for, and `wedge_watch_error`
// went 2609 findings without a reader before anyone noticed the analogous gap.
//
// So the armed-and-silent case is made READABLE rather than inferred from
// absence: Owed is how many agents currently owe a submit, Judged how many were
// readable at all, and Skipped how many could not be judged. A consumer seeing
// Judged>0 with Owed==0 for a long time is looking at a detector that is
// working and has nothing to do; one seeing Judged==0 is looking at a detector
// that is not covering anybody.
//
// Nothing consumes this yet, and saying so is the point: it is the seam a
// blindwatch-shaped reader needs, not a claim that one exists.
type State struct {
	// Armed is whether the watcher is enabled and has a source.
	Armed bool
	// LastSample is when the last sample COMPLETED. Zero means it has never run.
	LastSample time.Time
	// Judged is how many agents the last sample could read.
	Judged int
	// Skipped is how many of those could not be judged (no receipt signal, or an
	// unreadable ring).
	Skipped int
	// Owed is how many currently owe an undischarged submit — the population
	// this detector is capable of firing on at all.
	Owed int
	// Quiescence is the threshold in force.
	Quiescence time.Duration
}

// Snapshot returns the watcher's own state.
func (w *Watcher) Snapshot() State {
	if w == nil {
		return State{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return State{
		Armed:      w.enabled && w.source != nil,
		LastSample: w.lastSample,
		Judged:     w.judged,
		Skipped:    w.skipped,
		Owed:       w.owed,
		Quiescence: w.quiescence,
	}
}
