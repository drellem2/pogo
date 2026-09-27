// Package wakewatch wakes an agent only when there is something for it to do
// (mg-5496, phase 1 of 4; built as mg-e00c).
//
// # What it does
//
// It tails macguffin's own event log (~/.macguffin/events.jsonl) and acts on
// four facts that log already records:
//
//   - ARRIVAL. A `mail.sent` whose `to` resolves to a running agent (its agent
//     name, or the work-item box of the item it holds), or an assignment — a
//     `work.edited` with `fields=assignee` whose `assignee_after` is a running
//     agent or `blocked:<agent>`, or a `work.created` whose item carries an
//     assignee — gets ONE pointer nudge: at most MaxPointerLen bytes, built here,
//     with the command to run LAST. Pointers to one recipient are coalesced to at
//     most one per Params.Coalesce, carrying a count.
//   - RECOVERY. Every Params.RecoveryInterval, per running agent, the oldest
//     mail with no `mail.read`/`mail.archived` and the oldest assignment the
//     agent has not claimed or touched are checked. Older than
//     Params.RenudgeAfter, the pointer is re-sent — at most once per
//     Params.RenudgeEvery, up to Params.MaxRenudges times — and after that the
//     work is reported unconsumed: a `wake_unconsumed` event and one mail to the
//     coordinator.
//   - BOUNCE. Mail to a name that denotes an agent that is NOT running (a reaped
//     polecat, its work-item box, a parked or stopped crew agent) is reported to
//     the sender and the coordinator: "recipient <x> is not running; your mail is
//     unread in <box>". Mail to a box that is not an agent at all (`human`, a
//     probe's selftest box) is neither pointed at nor bounced.
//
// # SHADOW (phase 1)
//
// Nothing here replaces the mail-check timers; they stay on. Phase 2's gate is
// `pogo check-wakewatch` (see Check), which joins every mail a timer-driven
// mail-check turn read against the pointers sent before it and lists the misses.
// Every decision this package takes — including the ones where it deliberately
// sends nothing — is an event, so that join can explain a miss rather than
// merely count it.
//
// # "No data" is never "no mail"
//
// events.jsonl can vanish (it was deleted on 2026-09-27) or be replaced by a
// rebuilt copy. An absent file makes the watcher BLIND: it says so once
// (`wake_watch_blind`) and suspends recovery, because re-nudging from a model
// that can no longer see `mail.read` would nag agents about mail they have
// already read — and concluding "nothing unread" would be worse. A replaced file
// (new inode, or shorter than the offset) is read from its start, but only
// events younger than Params.Fresh are treated as arrivals, and a message id the
// model already holds is never pointed at twice.
//
// # Restart
//
// The byte offset reached and the file's identity are persisted (State). A
// restarted pogod rebuilds its model from the last Params.Lookback of the log but
// points only at events past the persisted offset — and with no state at all
// (first run), only at events written after it started. Re-nudge counts and
// escalations are persisted too, so a restart does not reset the three-try
// budget and re-nag.
package wakewatch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/drellem2/pogo/internal/mailbox"
)

// Event types wakewatch emits into pogod's events.log.
const (
	EventPointerSent    = "wake_pointer_sent"
	EventPointerSkipped = "wake_pointer_skipped"
	EventRenudge        = "wake_renudge"
	EventUnconsumed     = "wake_unconsumed"
	EventBounce         = "wake_bounce"
	EventArmed          = "wake_watch_armed"
	EventBlind          = "wake_watch_blind"
	EventSighted        = "wake_watch_sighted"
)

// Nudge outcomes recorded on wake_pointer_sent / wake_renudge.
const (
	OutcomeDelivered = "delivered"
	// OutcomeQueued: written to a harness mid-turn, which emits no receipt for
	// it (agent.ErrNudgeQueued). Read at the end of the turn; counts as sent.
	OutcomeQueued = "queued"
	OutcomeFailed = "failed"
)

// Sender is the From: on every mail wakewatch sends.
const Sender = "pogod"

// Params are wakewatch's thresholds. The defaults are the design's starting
// values (mg-5496); phase 1's data is what sets them.
type Params struct {
	// Coalesce: at most one pointer per recipient per this window.
	Coalesce time.Duration
	// RecoveryInterval: how often recovery runs.
	RecoveryInterval time.Duration
	// RenudgeAfter: age past which unconsumed work is re-pointed.
	RenudgeAfter time.Duration
	// RenudgeEvery: minimum gap between two re-nudges of one piece of work.
	RenudgeEvery time.Duration
	// MaxRenudges: re-nudges before the work is reported unconsumed.
	MaxRenudges int
	// Lookback bounds the model: events older than this are ignored.
	Lookback time.Duration
	// Fresh: an event older than this is never an ARRIVAL, whatever its offset.
	// It is what stops a rebuilt events.jsonl from re-pointing its history.
	Fresh time.Duration
	// PollInterval: how often the log is tailed.
	PollInterval time.Duration
}

// Defaults.
const (
	DefaultCoalesce         = 60 * time.Second
	DefaultRecoveryInterval = 5 * time.Minute
	DefaultRenudgeAfter     = 15 * time.Minute
	DefaultRenudgeEvery     = 15 * time.Minute
	DefaultMaxRenudges      = 3
	DefaultLookback         = 24 * time.Hour
	DefaultFresh            = 10 * time.Minute
	DefaultPollInterval     = 3 * time.Second
)

// DefaultParams returns the design's starting values.
func DefaultParams() Params {
	return Params{
		Coalesce:         DefaultCoalesce,
		RecoveryInterval: DefaultRecoveryInterval,
		RenudgeAfter:     DefaultRenudgeAfter,
		RenudgeEvery:     DefaultRenudgeEvery,
		MaxRenudges:      DefaultMaxRenudges,
		Lookback:         DefaultLookback,
		Fresh:            DefaultFresh,
		PollInterval:     DefaultPollInterval,
	}
}

func (p Params) withDefaults() Params {
	d := DefaultParams()
	if p.Coalesce <= 0 {
		p.Coalesce = d.Coalesce
	}
	if p.RecoveryInterval <= 0 {
		p.RecoveryInterval = d.RecoveryInterval
	}
	if p.RenudgeAfter <= 0 {
		p.RenudgeAfter = d.RenudgeAfter
	}
	if p.RenudgeEvery <= 0 {
		p.RenudgeEvery = d.RenudgeEvery
	}
	if p.MaxRenudges <= 0 {
		p.MaxRenudges = d.MaxRenudges
	}
	if p.Lookback <= 0 {
		p.Lookback = d.Lookback
	}
	if p.Fresh <= 0 {
		p.Fresh = d.Fresh
	}
	if p.PollInterval <= 0 {
		p.PollInterval = d.PollInterval
	}
	return p
}

// AgentRef is one agent the daemon knows, running or not.
type AgentRef struct {
	Name       string
	WorkItemID string
	Running    bool
}

// Item is what wakewatch needs to know about a work item.
type Item struct {
	Title    string
	Assignee string
}

// Deps are wakewatch's seams. Every one is required except Crew.
type Deps struct {
	// EventsPath is macguffin's events.jsonl.
	EventsPath string
	// StatePath is where the offset and re-nudge bookkeeping persist.
	StatePath string
	// Agents lists the registry: running agents, and parked/exited ones.
	Agents func() []AgentRef
	// Crew lists configured crew names; a configured agent that is not in the
	// registry is still an agent, so mail to it bounces. Optional.
	Crew func() []string
	// Item looks up a work item's title and assignee.
	Item func(id string) (Item, bool)
	// Subject reads a message's Subject: header.
	Subject func(box, msgID string) string
	// Nudge types a pointer into an agent's terminal.
	Nudge func(agent, text string) (outcome string, err error)
	// Mail sends macguffin mail.
	Mail func(to, from, subject, body string) error
	// Emit records one wakewatch event.
	Emit func(eventType, workItemID string, details map[string]any)
	// Coordinator receives bounces and unconsumed-work reports.
	Coordinator string
	// Now is the clock.
	Now func() time.Time
	// Async sends nudges on their own goroutine (production). Tests leave it
	// false so a nudge has happened when the call that caused it returns.
	Async bool
}

// State is what persists across a pogod restart.
type State struct {
	// Dev/Ino identify the file Offset belongs to.
	Dev    uint64 `json:"dev"`
	Ino    uint64 `json:"ino"`
	Offset int64  `json:"offset"`
	// Renudges is the recovery budget spent per work key (mail:<id>, item:<id>).
	Renudges map[string]*Renudge `json:"renudges,omitempty"`
	// Seen maps a canonical agent name or work-item box to the agent that held
	// it, for every agent this watcher has observed. It is how a reaped
	// polecat's box is still recognised as an agent's — so mail to it bounces —
	// after the registry has forgotten it.
	Seen map[string]SeenAgent `json:"seen,omitempty"`
}

// Renudge is one work key's recovery bookkeeping.
type Renudge struct {
	Count     int       `json:"count"`
	Last      time.Time `json:"last"`
	Escalated bool      `json:"escalated,omitempty"`
}

// SeenAgent records an agent name a box belonged to.
type SeenAgent struct {
	Agent string    `json:"agent"`
	At    time.Time `json:"at"`
}

// seenRetention bounds State.Seen.
const seenRetention = 30 * 24 * time.Hour

type mailRec struct {
	msgID, box, from string
	ts               time.Time
	consumed         bool
}

type assignRec struct {
	itemID, agent, actor, event string
	ts                          time.Time
	consumed                    bool
}

type pending struct {
	batch []Trigger
}

// Watcher is one wakewatch. Build it with New; drive it with Run, or with
// Start + Poll + Recover in tests.
type Watcher struct {
	p Params
	d Deps

	mu          sync.Mutex
	st          State
	mails       map[string]*mailRec
	assigns     map[string]*assignRec
	pend        map[string]*pending
	lastSent    map[string]time.Time
	lastRecover time.Time
	blind       string
	started     bool
	// arrivalFrom is the byte offset at or past which an event is an arrival.
	arrivalFrom int64
	wg          sync.WaitGroup
}

// New builds a watcher.
func New(p Params, d Deps) *Watcher {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Emit == nil {
		d.Emit = func(string, string, map[string]any) {}
	}
	return &Watcher{
		p:        p.withDefaults(),
		d:        d,
		mails:    map[string]*mailRec{},
		assigns:  map[string]*assignRec{},
		pend:     map[string]*pending{},
		lastSent: map[string]time.Time{},
	}
}

// Params returns the effective thresholds.
func (w *Watcher) Params() Params { return w.p }

// Run starts the watcher and drives it until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	w.Start()
	t := time.NewTicker(w.p.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.wg.Wait()
			return
		case <-t.C:
			w.Poll()
			w.Recover()
		}
	}
}

// Wait blocks until every in-flight asynchronous nudge has finished.
func (w *Watcher) Wait() { w.wg.Wait() }

// Start loads persisted state and builds the model from the log's retained
// history WITHOUT pointing at any of it.
func (w *Watcher) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loadState()
	now := w.d.Now()
	w.lastRecover = now

	fi, err := os.Stat(w.d.EventsPath)
	if err != nil {
		w.setBlind(fmt.Sprintf("%s is absent: %v", w.d.EventsPath, err))
		w.emitArmed(0, "absent")
		w.st.Offset = 0
		w.st.Dev, w.st.Ino = 0, 0
		w.arrivalFrom = 0
		w.started = true
		w.saveState()
		return
	}
	dev, ino := fileID(fi)
	resume := "first-run"
	arrivalFrom := fi.Size()
	if w.st.Ino != 0 || w.st.Dev != 0 {
		if w.st.Dev == dev && w.st.Ino == ino && w.st.Offset <= fi.Size() {
			// Same file: everything past the saved offset arrived while pogod
			// was down, and is an arrival (still subject to Fresh).
			arrivalFrom = w.st.Offset
			resume = "resumed"
		} else {
			// A different file under the same name — rotated, deleted and
			// recreated, rebuilt. Its offsets say nothing about ours, and it may
			// hold a rebuilt history: point at none of it. Recovery covers
			// anything genuinely new within RenudgeAfter.
			resume = "replaced"
		}
	}
	w.arrivalFrom = arrivalFrom
	w.st.Dev, w.st.Ino, w.st.Offset = dev, ino, 0
	w.started = true
	// Armed BEFORE the replay: mail that arrived while pogod was down is pointed
	// during it, and the shadow join classes anything sent before the first
	// armed event as PRE-ARM — so the event must precede those pointers.
	w.emitArmed(arrivalFrom, resume)
	w.readLocked()
	w.saveState()
}

func (w *Watcher) emitArmed(arrivalFrom int64, resume string) {
	w.d.Emit(EventArmed, "", map[string]any{
		"events_path":  w.d.EventsPath,
		"resume":       resume,
		"arrival_from": arrivalFrom,
		"coalesce_s":   int(w.p.Coalesce.Seconds()),
		"renudge_s":    int(w.p.RenudgeAfter.Seconds()),
		"max_renudges": w.p.MaxRenudges,
		"shadow":       true,
	})
}

// Poll reads whatever was appended to the log since the last call, points at
// arrivals, and flushes coalesced pointers that are due.
func (w *Watcher) Poll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return
	}
	fi, err := os.Stat(w.d.EventsPath)
	if err != nil {
		w.setBlind(fmt.Sprintf("%s is absent: %v", w.d.EventsPath, err))
		w.flushLocked()
		return
	}
	dev, ino := fileID(fi)
	if dev != w.st.Dev || ino != w.st.Ino || fi.Size() < w.st.Offset {
		// Replaced under us. Read the new file from its start; Fresh and the
		// msg-id dedupe keep a rebuilt history from being re-pointed.
		w.st.Dev, w.st.Ino, w.st.Offset = dev, ino, 0
		w.arrivalFrom = 0
	}
	w.setSighted()
	w.readLocked()
	w.flushLocked()
	w.saveState()
}

func (w *Watcher) setBlind(reason string) {
	if w.blind == reason {
		return
	}
	w.blind = reason
	w.d.Emit(EventBlind, "", map[string]any{"reason": reason, "recovery": "suspended"})
}

func (w *Watcher) setSighted() {
	if w.blind == "" {
		return
	}
	w.d.Emit(EventSighted, "", map[string]any{"was": w.blind})
	w.blind = ""
}

// Blind reports why the watcher cannot see, or "".
func (w *Watcher) Blind() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.blind
}

// storeEvent is one line of events.jsonl — only the fields wakewatch reads.
type storeEvent struct {
	Type          string `json:"type"`
	TS            string `json:"ts"`
	From          string `json:"from"`
	To            string `json:"to"`
	MsgID         string `json:"msg_id"`
	Mailbox       string `json:"mailbox"`
	ItemID        string `json:"item_id"`
	Actor         string `json:"actor"`
	Fields        string `json:"fields"`
	AssigneeAfter string `json:"assignee_after"`
	// Assignee is read from work.created when macguffin records it. Today it
	// does not, so work.created falls back to the item's own frontmatter.
	Assignee string `json:"assignee"`
}

// readLocked consumes complete lines from the saved offset to EOF.
func (w *Watcher) readLocked() {
	f, err := os.Open(w.d.EventsPath)
	if err != nil {
		w.setBlind(fmt.Sprintf("%s is unreadable: %v", w.d.EventsPath, err))
		return
	}
	defer f.Close()
	if _, err := f.Seek(w.st.Offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 64*1024)
	off := w.st.Offset
	now := w.d.Now()
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// A partial last line is left for the next poll.
			break
		}
		start := off
		off += int64(len(line))
		var ev storeEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		ts, perr := time.Parse(time.RFC3339, ev.TS)
		if perr != nil {
			continue
		}
		if now.Sub(ts) > w.p.Lookback {
			continue
		}
		arrival := start >= w.arrivalFrom && now.Sub(ts) <= w.p.Fresh
		w.apply(ev, ts, arrival)
	}
	w.st.Offset = off
}

// apply folds one event into the model, and handles it as an arrival if it is
// one.
func (w *Watcher) apply(ev storeEvent, ts time.Time, arrival bool) {
	switch ev.Type {
	case "mail.sent":
		if ev.MsgID == "" || ev.To == "" {
			return
		}
		if _, dup := w.mails[ev.MsgID]; dup {
			return // a rebuilt log repeating a message already modelled
		}
		w.mails[ev.MsgID] = &mailRec{msgID: ev.MsgID, box: ev.To, from: ev.From, ts: ts}
		if arrival {
			w.arriveMail(ev)
		}
	case "mail.read", "mail.archived":
		if m := w.mails[ev.MsgID]; m != nil {
			m.consumed = true
		}
	case "work.edited":
		if a := w.assigns[ev.ItemID]; a != nil && ev.Actor != "" && ev.Actor == a.agent {
			a.consumed = true
		}
		if !hasField(ev.Fields, "assignee") {
			return
		}
		w.assign(ev.ItemID, ev.AssigneeAfter, ev.Actor, ev.Type, ts, arrival)
	case "work.created":
		assignee := ev.Assignee
		if assignee == "" && arrival && w.d.Item != nil {
			// Only an arrival needs the lookup: history is not pointed at, and
			// a disk read per historical creation would be the cost of a
			// restart.
			if it, ok := w.d.Item(ev.ItemID); ok {
				assignee = it.Assignee
			}
		}
		if assignee != "" {
			w.assign(ev.ItemID, assignee, ev.Actor, ev.Type, ts, arrival)
		}
	case "work.claim", "work.done", "work.archive":
		if a := w.assigns[ev.ItemID]; a != nil {
			a.consumed = true
		}
	default:
		if a := w.assigns[ev.ItemID]; a != nil && ev.Actor != "" && ev.Actor == a.agent {
			a.consumed = true
		}
	}
}

func hasField(fields, want string) bool {
	for _, f := range strings.FieldsFunc(fields, func(r rune) bool { return r == ',' || r == ' ' }) {
		if f == want {
			return true
		}
	}
	return false
}

// assigneeAgent strips the dispatch-gate prefix an assignee can carry.
func assigneeAgent(assignee string) string {
	return strings.TrimPrefix(strings.TrimSpace(assignee), "blocked:")
}

func (w *Watcher) assign(itemID, assignee, actor, event string, ts time.Time, arrival bool) {
	if itemID == "" {
		return
	}
	agent := assigneeAgent(assignee)
	delete(w.assigns, itemID)
	if agent == "" || agent == "human" || agent == "parked" {
		return
	}
	w.assigns[itemID] = &assignRec{itemID: itemID, agent: agent, actor: actor, event: event, ts: ts}
	if !arrival {
		return
	}
	tg := w.resolve(agent)
	if !tg.running {
		return // assignments are not bounced: the item itself is the record
	}
	trig := Trigger{Kind: "assign", Event: event, ItemID: itemID}
	if actor == tg.agent {
		w.skip(tg.agent, trig, "self-assigned")
		return
	}
	if w.d.Item != nil {
		if it, ok := w.d.Item(itemID); ok {
			trig.Quoted = it.Title
		}
	}
	w.enqueue(tg.agent, trig)
}

func (w *Watcher) arriveMail(ev storeEvent) {
	tg := w.resolve(ev.To)
	trig := Trigger{Kind: "mail", Event: "mail.sent", MsgID: ev.MsgID, Box: ev.To, From: ev.From}
	switch {
	case tg.running:
		if ev.From == tg.agent {
			w.skip(tg.agent, trig, "self-sent")
			return
		}
		if w.d.Subject != nil {
			trig.Quoted = w.d.Subject(ev.To, ev.MsgID)
		}
		w.enqueue(tg.agent, trig)
	case tg.known:
		w.bounce(ev, tg)
	default:
		// Not an agent's box (human, a probe's selftest box): nobody to wake
		// and nobody to bounce for. Recorded so the shadow join can explain it.
		w.skip(ev.To, trig, "not-an-agent")
	}
}

func (w *Watcher) skip(recipient string, t Trigger, reason string) {
	d := map[string]any{"recipient": recipient, "trigger": t.Event, "reason": reason, "text_len": 0}
	if t.MsgID != "" {
		d["msg_id"] = t.MsgID
		d["box"] = t.Box
	}
	if t.ItemID != "" {
		d["item_id"] = t.ItemID
	}
	w.d.Emit(EventPointerSkipped, t.ItemID, d)
}

type target struct {
	agent   string
	running bool
	known   bool
}

// resolve maps a mailbox (or assignee) name to the agent reading it.
func (w *Watcher) resolve(box string) target {
	c := mailbox.Canonical(box)
	if c == "" {
		return target{}
	}
	var fleet []AgentRef
	if w.d.Agents != nil {
		fleet = w.d.Agents()
	}
	now := w.d.Now()
	if w.st.Seen == nil {
		w.st.Seen = map[string]SeenAgent{}
	}
	var best *target
	for _, a := range fleet {
		// Every agent observed is remembered, so its boxes still resolve
		// after the registry drops it.
		w.st.Seen[mailbox.Canonical(a.Name)] = SeenAgent{Agent: a.Name, At: now}
		if a.WorkItemID != "" {
			w.st.Seen[mailbox.Canonical(a.WorkItemID)] = SeenAgent{Agent: a.Name, At: now}
		}
		if mailbox.Canonical(a.Name) == c || (a.WorkItemID != "" && mailbox.Canonical(a.WorkItemID) == c) {
			t := target{agent: a.Name, running: a.Running, known: true}
			if best == nil || (t.running && !best.running) {
				best = &t
			}
		}
	}
	if best != nil {
		return *best
	}
	var crew []string
	if w.d.Crew != nil {
		crew = w.d.Crew()
	}
	for _, n := range crew {
		if mailbox.Canonical(n) == c {
			return target{agent: n, known: true}
		}
	}
	if s, ok := w.st.Seen[c]; ok {
		return target{agent: s.Agent, known: true}
	}
	return target{}
}

func (w *Watcher) enqueue(recipient string, t Trigger) {
	p := w.pend[recipient]
	if p == nil {
		p = &pending{}
		w.pend[recipient] = p
	}
	p.batch = append(p.batch, t)
	w.flushOne(recipient)
}

// flushLocked sends every coalesced batch whose window has elapsed.
func (w *Watcher) flushLocked() {
	names := make([]string, 0, len(w.pend))
	for n := range w.pend {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w.flushOne(n)
	}
}

func (w *Watcher) flushOne(recipient string) {
	p := w.pend[recipient]
	if p == nil || len(p.batch) == 0 {
		return
	}
	now := w.d.Now()
	if last, ok := w.lastSent[recipient]; ok && now.Sub(last) < w.p.Coalesce {
		return
	}
	batch := p.batch
	delete(w.pend, recipient)
	w.lastSent[recipient] = now
	text := BuildPointer("", batch)
	latest := batch[len(batch)-1]
	d := map[string]any{
		"recipient": recipient,
		"trigger":   latest.Event,
		"count":     len(batch),
		"text_len":  len(text),
	}
	addIDs(d, batch)
	w.send(EventPointerSent, recipient, latest.ItemID, text, d)
}

// addIDs records the msg/item ids a pointer covers — every one of them, since
// the shadow join asks per message whether a pointer covered it.
func addIDs(d map[string]any, batch []Trigger) {
	var msgs, items, boxes []string
	for _, t := range batch {
		if t.MsgID != "" {
			msgs = append(msgs, t.MsgID)
			boxes = append(boxes, t.Box)
		}
		if t.ItemID != "" {
			items = append(items, t.ItemID)
		}
	}
	if len(msgs) > 0 {
		d["msg_id"] = msgs[len(msgs)-1]
		d["msg_ids"] = msgs
		d["boxes"] = boxes
	}
	if len(items) > 0 {
		d["item_id"] = items[len(items)-1]
		d["item_ids"] = items
	}
}

// send types text into recipient's terminal and records the outcome.
func (w *Watcher) send(eventType, recipient, itemID, text string, d map[string]any) {
	do := func() {
		outcome, err := w.d.Nudge(recipient, text)
		if outcome == "" {
			outcome = OutcomeDelivered
			if err != nil {
				outcome = OutcomeFailed
			}
		}
		d["outcome"] = outcome
		if err != nil {
			d["error"] = err.Error()
		}
		w.d.Emit(eventType, itemID, d)
	}
	if w.d.Async {
		w.wg.Add(1)
		go func() { defer w.wg.Done(); do() }()
		return
	}
	do()
}

// bounce reports mail addressed to an agent that is not running.
func (w *Watcher) bounce(ev storeEvent, tg target) {
	if ev.From == Sender {
		// pogod's own notifiers route around dead recipients themselves
		// (filernotify redirects to the coordinator), and bouncing pogod's
		// mail to pogod would be a loop with no reader at either end.
		w.skip(ev.To, Trigger{Kind: "mail", Event: "mail.sent", MsgID: ev.MsgID, Box: ev.To}, "pogod-sent")
		return
	}
	subject := ""
	if w.d.Subject != nil {
		subject = sanitize(w.d.Subject(ev.To, ev.MsgID))
	}
	body := fmt.Sprintf("recipient %s is not running; your mail is unread in %s\n\n"+
		"msg_id: %s\nfrom: %s\nsubject: %s\n\n"+
		"Nothing will read it until %s runs again. Re-address it to a running agent "+
		"(pogo agent list) if it is still needed.",
		tg.agent, ev.To, ev.MsgID, ev.From, subject, tg.agent)
	subj := fmt.Sprintf("bounce: %s is not running — mail unread in %s", tg.agent, ev.To)

	d := map[string]any{
		"recipient": tg.agent,
		"box":       ev.To,
		"sender":    ev.From,
		"trigger":   "mail.sent",
		"msg_id":    ev.MsgID,
		"text_len":  len(body),
	}
	var notified []string
	var errs []string
	tos := []string{}
	if ev.From != "" && ev.From != w.d.Coordinator && w.senderReachable(ev.From) {
		tos = append(tos, ev.From)
	}
	if w.d.Coordinator != "" {
		tos = append(tos, w.d.Coordinator)
	}
	for _, to := range tos {
		if w.d.Mail == nil {
			break
		}
		if err := w.d.Mail(to, Sender, subj, body); err != nil {
			errs = append(errs, to+": "+err.Error())
			continue
		}
		notified = append(notified, to)
	}
	d["notified"] = notified
	if len(errs) > 0 {
		d["error"] = strings.Join(errs, "; ")
	}
	w.d.Emit(EventBounce, "", d)
}

// senderReachable: mail the sender only when someone will read it — a running
// agent, or the human. A bounce to a dead sender would itself bounce.
func (w *Watcher) senderReachable(from string) bool {
	if from == "human" {
		return true
	}
	return w.resolve(from).running
}

// Recover runs the recovery pass if RecoveryInterval has elapsed since the
// last one. Suspended while blind.
func (w *Watcher) Recover() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started || w.blind != "" {
		return
	}
	now := w.d.Now()
	if now.Sub(w.lastRecover) < w.p.RecoveryInterval {
		return
	}
	w.lastRecover = now
	w.recoverLocked(now)
	w.pruneLocked(now)
	w.saveState()
}

func (w *Watcher) recoverLocked(now time.Time) {
	if w.d.Agents == nil {
		return
	}
	running := map[string]bool{}
	for _, a := range w.d.Agents() {
		if a.Running {
			running[a.Name] = true
		}
	}
	// Oldest unconsumed mail and assignment per running agent.
	oldestMail := map[string]*mailRec{}
	for _, m := range w.mails {
		if m.consumed || now.Sub(m.ts) < w.p.RenudgeAfter {
			continue
		}
		tg := w.resolve(m.box)
		if !tg.running || !running[tg.agent] || m.from == tg.agent {
			continue
		}
		if o := oldestMail[tg.agent]; o == nil || m.ts.Before(o.ts) {
			oldestMail[tg.agent] = m
		}
	}
	oldestAssign := map[string]*assignRec{}
	for _, a := range w.assigns {
		if a.consumed || now.Sub(a.ts) < w.p.RenudgeAfter {
			continue
		}
		tg := w.resolve(a.agent)
		if !tg.running || !running[tg.agent] || a.actor == tg.agent {
			continue
		}
		if o := oldestAssign[tg.agent]; o == nil || a.ts.Before(o.ts) {
			oldestAssign[tg.agent] = a
		}
	}
	agents := make([]string, 0, len(running))
	for n := range running {
		agents = append(agents, n)
	}
	sort.Strings(agents)
	for _, ag := range agents {
		if m := oldestMail[ag]; m != nil {
			t := Trigger{Kind: "mail", Event: "mail.sent", MsgID: m.msgID, Box: m.box, From: m.from}
			if w.d.Subject != nil {
				t.Quoted = w.d.Subject(m.box, m.msgID)
			}
			w.recoverOne(now, ag, t, m.ts)
		}
		if a := oldestAssign[ag]; a != nil {
			t := Trigger{Kind: "assign", Event: a.event, ItemID: a.itemID}
			if w.d.Item != nil {
				if it, ok := w.d.Item(a.itemID); ok {
					t.Quoted = it.Title
				}
			}
			w.recoverOne(now, ag, t, a.ts)
		}
	}
}

func (w *Watcher) recoverOne(now time.Time, agent string, t Trigger, since time.Time) {
	if w.st.Renudges == nil {
		w.st.Renudges = map[string]*Renudge{}
	}
	key := t.Key()
	r := w.st.Renudges[key]
	if r == nil {
		r = &Renudge{}
		w.st.Renudges[key] = r
	}
	if r.Escalated {
		return
	}
	if !r.Last.IsZero() && now.Sub(r.Last) < w.p.RenudgeEvery {
		return
	}
	age := now.Sub(since)
	if r.Count < w.p.MaxRenudges {
		r.Count++
		r.Last = now
		text := BuildPointer("still unread: ", []Trigger{t})
		if t.Kind == "assign" {
			text = BuildPointer("still unclaimed: ", []Trigger{t})
		}
		w.lastSent[agent] = now
		d := map[string]any{
			"recipient": agent,
			"trigger":   t.Event,
			"attempt":   r.Count,
			"age_s":     int(age.Seconds()),
			"text_len":  len(text),
		}
		addIDs(d, []Trigger{t})
		w.send(EventRenudge, agent, t.ItemID, text, d)
		return
	}
	// The budget is spent and the work is still there: it is not being
	// consumed. Report it once.
	r.Escalated = true
	r.Last = now
	what := fmt.Sprintf("mail %s in box %s from %s", t.MsgID, t.Box, t.From)
	if t.Kind == "assign" {
		what = "assignment " + t.ItemID
	}
	body := fmt.Sprintf("%s has unconsumed work: %s, %s old, re-pointed %d times with no read or claim.\n\n"+
		"This is wakewatch's liveness signal (mg-5496): an agent that has work and does not consume it. "+
		"Check it with pogo agent diagnose %s.", agent, what, age.Round(time.Minute), r.Count, agent)
	d := map[string]any{
		"recipient": agent,
		"trigger":   t.Event,
		"renudges":  r.Count,
		"age_s":     int(age.Seconds()),
		"text_len":  len(body),
	}
	addIDs(d, []Trigger{t})
	if w.d.Mail != nil && w.d.Coordinator != "" {
		if err := w.d.Mail(w.d.Coordinator, Sender, "unconsumed work: "+agent, body); err != nil {
			d["error"] = err.Error()
		} else {
			d["notified"] = w.d.Coordinator
		}
	}
	w.d.Emit(EventUnconsumed, t.ItemID, d)
}

func (w *Watcher) pruneLocked(now time.Time) {
	for id, m := range w.mails {
		if now.Sub(m.ts) > w.p.Lookback {
			delete(w.mails, id)
			delete(w.st.Renudges, "mail:"+id)
		}
	}
	for id, a := range w.assigns {
		if now.Sub(a.ts) > w.p.Lookback {
			delete(w.assigns, id)
			delete(w.st.Renudges, "item:"+id)
		}
	}
	for k, r := range w.st.Renudges {
		if now.Sub(r.Last) > w.p.Lookback {
			delete(w.st.Renudges, k)
		}
	}
	for k, s := range w.st.Seen {
		if now.Sub(s.At) > seenRetention {
			delete(w.st.Seen, k)
		}
	}
}

func (w *Watcher) loadState() {
	w.st = State{}
	if w.d.StatePath == "" {
		return
	}
	b, err := os.ReadFile(w.d.StatePath)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &w.st)
}

func (w *Watcher) saveState() {
	if w.d.StatePath == "" {
		return
	}
	b, err := json.Marshal(w.st)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(w.d.StatePath), 0o755); err != nil {
		return
	}
	tmp := w.d.StatePath + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, w.d.StatePath)
	}
}

func fileID(fi os.FileInfo) (dev, ino uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return 0, 0
}

// ReadSubject returns the Subject: header of message msgID in box under a
// macguffin mail root, or "" when it cannot be read.
func ReadSubject(mailRoot, box, msgID string) string {
	if msgID == "" || strings.ContainsAny(msgID, `/\`) {
		return ""
	}
	for _, b := range []string{box, mailbox.Canonical(box)} {
		if b == "" || strings.ContainsAny(b, `/\`) || b == ".." {
			continue
		}
		for _, sub := range []string{"new", "cur", "archive"} {
			f, err := os.Open(filepath.Join(mailRoot, b, sub, msgID))
			if err != nil {
				continue
			}
			s := bufio.NewScanner(f)
			subj := ""
			for s.Scan() {
				line := s.Text()
				if line == "" {
					break
				}
				if strings.HasPrefix(line, "Subject:") {
					subj = strings.TrimSpace(strings.TrimPrefix(line, "Subject:"))
					break
				}
			}
			f.Close()
			return subj
		}
	}
	return ""
}
