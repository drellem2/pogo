// Package crewreset asks a long-running crew agent to reset its own context
// (mg-5b58d).
//
// A crew session's context only grows. mg-c2d5's audit estimated that
// restarting crew context every four hours would save about 22% of crew
// tokens, and Daniel asked for it as a gentle nudge rather than a kill
// (2026-09-28): "btw you've been up for 4 hours, time to leave yourself a note
// and reset".
//
// So this package ASKS; it never stops anything. When a crew agent's current
// session passes After, pogod mails it ONE notice telling it to write a handoff
// note at its next safe point and then run `pogo agent stop <itself>`. The
// respawn is the existing restart_on_crash path — Registry.Stop leaves a
// restart_on_crash agent in the registry and pogod's OnExit hook respawns it
// into a fresh session that runs its prompt's "On Startup" — so there is no
// second restart mechanism here to drift from the first.
//
// # Bounded, not a nag loop
//
// A session gets at most MaxNotices notices: the first when it passes After,
// and one more if the SAME session is still running RenoticeAfter later. Then
// the watcher is quiet for the rest of that session, however long it lives. The
// agent may have a good reason to keep going; a third ask would be pressure,
// not information.
//
// A session is identified by (name, start time). A respawn — whether it came
// from the agent following the notice or from anything else — has a new start
// time and so starts a fresh count. That is also why nothing here is persisted:
// a pogod restart takes the crew down with it (their PTYs hang up), so every
// session the next pogod sees is a new one.
//
// # Who is asked
//
// The population is decided by the caller (cmd/pogod), and it must be only the
// agents for which `pogo agent stop` really is a reset rather than a shutdown:
// running crew agents with restart_on_crash, auto_start and no park flag.
// Asking an agent whose respawn is not guaranteed to stop itself would turn a
// token saving into an outage. Polecats are out of scope — they are short-lived
// and die with their work item.
package crewreset

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// Sender is the From: on every notice.
const Sender = "pogod"

// MaxNotices is how many notices one session receives at most: the first, and
// one re-notice.
const MaxNotices = 2

// Defaults, mirrored by internal/config.
const (
	DefaultAfter         = 4 * time.Hour
	DefaultRenoticeAfter = time.Hour
	DefaultInterval      = 5 * time.Minute
)

// Event types on the event spine.
const (
	EventNotice = "crew_reset_notice"
	EventError  = "crew_reset_error"
)

// Present is one agent the watcher may ask.
type Present struct {
	Name      string
	StartedAt time.Time
}

// Options configures a Watcher.
type Options struct {
	// After is the session uptime at which the first notice is sent.
	After time.Duration
	// RenoticeAfter is how long after the first notice the one re-notice goes,
	// if the same session is still running.
	RenoticeAfter time.Duration
	// Interval is the gap between samples in Run.
	Interval time.Duration
	// Exclude names agents that are never asked.
	Exclude []string
	// Population returns the agents that may be asked right now. See the
	// package comment for who belongs in it.
	Population func() ([]Present, error)
	// Mail sends the notice. Required.
	Mail func(to, from, subject, body string) error
	// Nudge, when set, types a short pointer at the agent after the mail
	// lands. It is optional because pogod's wake-watch already types a pointer
	// for every mail to a running agent; cmd/pogod sets it only when wake-watch
	// is off, so the agent gets one pointer, not two.
	Nudge func(name, text string) error
	// Emit records an event. Optional.
	Emit func(eventType string, details map[string]any)
	// Logf defaults to log.Printf.
	Logf func(string, ...any)
}

// Notice is one notice the watcher sent (or tried to send) in a Tick.
type Notice struct {
	Agent     string
	StartedAt time.Time
	Uptime    time.Duration
	// Number is 1 for the first notice of a session, 2 for the re-notice.
	Number int
	// MailErr is non-nil when the mail failed; such a notice is not counted,
	// so the next tick tries again.
	MailErr error
	// NudgeErr is the pointer's error, if a pointer was attempted.
	NudgeErr error
}

type session struct {
	startedAt time.Time
	notices   int
	lastAt    time.Time
}

// Watcher sends the reset notices.
type Watcher struct {
	o       Options
	exclude map[string]bool

	mu       sync.Mutex
	sessions map[string]*session
}

// New builds a Watcher, filling defaults for zero durations.
func New(o Options) *Watcher {
	if o.After <= 0 {
		o.After = DefaultAfter
	}
	if o.RenoticeAfter <= 0 {
		o.RenoticeAfter = DefaultRenoticeAfter
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	ex := make(map[string]bool, len(o.Exclude))
	for _, n := range o.Exclude {
		ex[n] = true
	}
	return &Watcher{o: o, exclude: ex, sessions: map[string]*session{}}
}

// Options returns the effective options (defaults filled).
func (w *Watcher) Options() Options { return w.o }

// Run samples every Interval until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.o.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			w.Tick(now)
		}
	}
}

// Tick takes one sample at now and sends whatever notices are due. It is the
// whole decision; Run only supplies the clock.
func (w *Watcher) Tick(now time.Time) []Notice {
	if w.o.Population == nil || w.o.Mail == nil {
		return nil
	}
	pop, err := w.o.Population()
	if err != nil {
		w.o.Logf("crew-reset: could not read the agent population: %v", err)
		w.emit(EventError, map[string]any{"error": err.Error(), "phase": "population"})
		return nil
	}
	sort.Slice(pop, func(i, j int) bool { return pop[i].Name < pop[j].Name })

	w.mu.Lock()
	defer w.mu.Unlock()

	// Forget sessions that are gone or replaced, so a respawn starts a fresh
	// count and the map does not grow with every agent ever seen.
	current := make(map[string]time.Time, len(pop))
	for _, p := range pop {
		current[p.Name] = p.StartedAt
	}
	for name, s := range w.sessions {
		if st, ok := current[name]; !ok || !st.Equal(s.startedAt) {
			delete(w.sessions, name)
		}
	}

	var out []Notice
	for _, p := range pop {
		if w.exclude[p.Name] || p.StartedAt.IsZero() {
			continue
		}
		uptime := now.Sub(p.StartedAt)
		if uptime < w.o.After {
			continue
		}
		s := w.sessions[p.Name]
		if s == nil {
			s = &session{startedAt: p.StartedAt}
			w.sessions[p.Name] = s
		}
		if s.notices >= MaxNotices {
			continue
		}
		if s.notices > 0 && now.Sub(s.lastAt) < w.o.RenoticeAfter {
			continue
		}
		n := Notice{Agent: p.Name, StartedAt: p.StartedAt, Uptime: uptime, Number: s.notices + 1}
		subject, body := Message(p.Name, uptime, p.StartedAt, w.o.After, n.Number)
		if err := w.o.Mail(p.Name, Sender, subject, body); err != nil {
			n.MailErr = err
			w.o.Logf("crew-reset: notice %d to %s failed: %v (will retry next tick)", n.Number, p.Name, err)
			w.emit(EventError, map[string]any{"agent": p.Name, "error": err.Error(), "phase": "mail", "notice": n.Number})
			out = append(out, n)
			continue
		}
		s.notices++
		s.lastAt = now
		if w.o.Nudge != nil {
			n.NudgeErr = w.o.Nudge(p.Name, Pointer(n.Number))
			if n.NudgeErr != nil {
				w.o.Logf("crew-reset: pointer to %s failed (the mail landed): %v", p.Name, n.NudgeErr)
			}
		}
		w.o.Logf("crew-reset: asked %s to reset its context (notice %d of %d, session up %s)",
			p.Name, n.Number, MaxNotices, HM(uptime))
		d := map[string]any{
			"agent":          p.Name,
			"notice":         n.Number,
			"max_notices":    MaxNotices,
			"uptime_seconds": int64(uptime.Seconds()),
			"session_start":  p.StartedAt.UTC().Format(time.RFC3339),
		}
		if n.NudgeErr != nil {
			d["nudge_error"] = n.NudgeErr.Error()
		}
		w.emit(EventNotice, d)
		out = append(out, n)
	}
	return out
}

func (w *Watcher) emit(eventType string, d map[string]any) {
	if w.o.Emit != nil {
		w.o.Emit(eventType, d)
	}
}

// Pointer is the short nudge typed after the mail, when wake-watch is not
// already typing one.
func Pointer(number int) string {
	return fmt.Sprintf("pogod: context-reset notice %d/%d in your mail — read it at your next safe point", number, MaxNotices)
}

// Message is the notice's subject and body. The body says where the note goes
// (the agent's existing sweep.log and memory directory — no new store) and
// carries the session start, so a FRESH session that finds this mail still
// unread knows it is not addressed to it.
func Message(name string, uptime time.Duration, startedAt time.Time, after time.Duration, number int) (subject, body string) {
	hours := HM(uptime)
	subject = fmt.Sprintf("crew-reset: you've been up %s — time to leave yourself a note and reset", hours)
	if number > 1 {
		subject = fmt.Sprintf("crew-reset (reminder, last one): you've been up %s — note and reset when you can", hours)
	}
	body = fmt.Sprintf(`You have been running in one session for %s (session started %s). A long context costs tokens on every turn, so pogod asks crew agents to reset after %s.

This is a request, not a kill. At your NEXT SAFE POINT — not in the middle of a task, and not while you hold unread or unhandled mail — do this:

1. Write your handoff note where your prompt already keeps your state:
   - append a dated handoff entry to your own sweep.log (the heartbeat file your prompt names), saying what you were doing, what is open, and what the next step is;
   - save anything durable to your memory directory, as your prompt describes.
   Do not invent a new place for it; your next session reads those on startup.
2. Run:  pogo agent stop %s
   You run with restart_on_crash, so pogod respawns you about 2s later into a fresh session that runs your "On Startup" (re-register your schedules there as usual). Your mail, schedules and work items are kept across the stop.

If you cannot reach a safe point now, carry on; you will get at most one reminder, an hour after the first notice, and then pogod stays quiet for the rest of this session.

If you are reading this in a session that started AFTER %s, it is not for you — you have already reset; mark it read and ignore it.

(notice %d of %d, from pogod's [crew_reset]; an operator can turn it off with [crew_reset] enabled = false or exclude this agent with exclude = ["%s"])`,
		hours, startedAt.UTC().Format(time.RFC3339), HM(after), name,
		startedAt.UTC().Format(time.RFC3339), number, MaxNotices, name)
	return subject, body
}

// HM renders a duration as hours and minutes ("4h01m"), the grain the notice
// is sent at.
func HM(d time.Duration) string {
	m := int(d / time.Minute)
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}
