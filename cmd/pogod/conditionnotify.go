package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
)

// The shared annunciator for pogod conditions that have an actor and no channel
// to reach them — rows A2..A15 of
// docs/investigations/pogod-log-conditions-with-no-reader-2026-07-30.md
// (mg-c3f0), carried forward by mg-342d.
//
// WHY A SHARED MECHANISM AND NOT FOURTEEN COPIES OF promptsyncnotify.go.
// mg-c3f0 §6 argued against "a generic pogod-condition notifier" and it was
// right at the size it was writing for: one condition does not justify an
// abstraction, and the six watcher packages (ackwatch, deafwatch, driftwatch,
// ghteardown, credexpiry, synthwatch) already are that abstraction for anything
// POLLABLE. The reason this file exists anyway is that A2..A15 are not
// pollable. `scheduler load failed` is not a state you can sample later — the
// scheduler either loaded on this boot or it did not, and by the time a watcher
// interval elapses the only surviving trace is the log line nobody reads. These
// are BOOT-TIME AND DECISION-POINT FACTS, so they need A1's shape (know it where
// it happens, remember across restarts because the restart is the tick) rather
// than a watcher's shape (sample a live subsystem on an interval).
//
// So this is A1's mechanism with the condition identity lifted out: one
// transition store, one mailer seam, one event contract, one set of tests, and
// three lines at each decision point. Fourteen bespoke notifiers would be
// fourteen chances to get the suppression wrong, and the suppression is the part
// that decides whether the alarm survives being real.
//
// WHAT IS DELIBERATELY NOT HERE. This does not tail a log, does not sample any
// subsystem, and does not run on the scheduler. The first is mg-c3f0's
// constraint; the third matters on its own and is argued at conditionWake below.

const (
	// conditionNoticesFile holds the cross-boot transition state, beside the
	// other small daemon state files at the POGO_HOME root.
	conditionNoticesFile = "pogod-conditions.json"

	// conditionRenotifyAfter is how long a still-live condition stays quiet
	// between reminders. Shorter than promptSyncRenotifyAfter's 72h because
	// these are not judgement calls about which local edits are load-bearing —
	// a fleet with no mail-check loops, a crew agent that failed to start, a
	// crashed agent that failed to respawn: each is an outage with a mechanical
	// remedy, and 24h of silence on an outage is too much. Still slow enough
	// that a condition persisting for a week produces 7 reminders rather than
	// one per 30-second heartbeat tick.
	conditionRenotifyAfter = 24 * time.Hour

	// conditionMinInterval is a floor on mail per condition id, enforced
	// REGARDLESS of whether the fingerprint changed.
	//
	// This is the one place this mechanism is stricter than A1's, and the reason
	// is that its fingerprints are less trustworthy. A1 fingerprints the content
	// hash of a .dist sidecar, which is stable by construction: the same shipped
	// prompt hashes the same on every boot. Here the fingerprint is usually an
	// ERROR STRING, and error strings carry pids, ports, temp paths, byte
	// offsets and timestamps. A fingerprint that varies per occurrence would
	// read as "changed" every time and mail on every boot or, for the
	// heartbeat-driven rows (A11), every 30 seconds — which is precisely the
	// firehose that gets a real alarm filtered out. The floor bounds that at
	// one mail per condition per hour no matter how noisy the error text is.
	conditionMinInterval = time.Hour

	// conditionEvent is the structured record of one annunciation decision. It
	// is emitted on EVERY raise, including suppressed ones, which is what makes
	// "the condition is still live and we are staying quiet on purpose"
	// distinguishable from "the notifier has quietly stopped working".
	conditionEvent = "pogod_condition"

	// conditionClearedEvent records a condition going away, so the spine shows
	// resolution rather than just an unexplained end to the raises.
	conditionClearedEvent = "pogod_condition_cleared"

	// conditionSummaryEvent is the per-boot roll-up. See report().
	conditionSummaryEvent = "pogod_condition_summary"

	// conditionMailFrom is the sender every condition notice carries, so a
	// recipient can filter on it and an operator can tell these from the
	// watcher packages' mail.
	conditionMailFrom = "pogod-conditions"

	// conditionWakeDeadline bounds how long a queued wake keeps being retried.
	// Past this the mail is still in the addressee's maildir and will be read
	// on its next mail-check; only the ACTIVE wake is abandoned. See
	// retryWakes.
	conditionWakeDeadline = 30 * time.Minute
)

// pogodCondition is one annunciable condition, filled in at its decision point.
type pogodCondition struct {
	// ID is the stable suppression key. It must identify the CONDITION, not the
	// occurrence: "scheduler_load_failed", or "autostart_failed:doctor" when the
	// condition is per-subject. Two different subjects must not share an id or
	// one will suppress the other's alarm.
	ID string

	// Row is the enumeration row this carries ("A2".."A15"). Recorded on the
	// event so the investigation and the running daemon can be reconciled
	// without grepping code — an enumeration that cannot be checked against the
	// implementation is how the remainder gets lost a second time.
	Row string

	// To is the mailbox that can act. NEVER `human`: measured at 988 unread
	// against 0-10 for every standing crew mailbox (mg-c3f0 §3), so routing a
	// fleet condition there reproduces the defect while looking like a fix.
	To string

	// Detail is the human-readable cause, usually an error string. It goes in
	// the mail body and on the event verbatim, and is hashed for Fingerprint
	// when the caller does not supply one.
	Detail string

	// Fingerprint distinguishes WHICH instance of the condition was announced,
	// so a materially different failure re-announces instead of hiding behind
	// the first one's quiet window. Empty means "hash Detail".
	Fingerprint string

	// Subject and Body are the mail. Body should say what broke, what it costs,
	// and what the recipient can do — see conditionBody for the frame every
	// caller shares.
	Subject string
	Body    string

	// Wake asks for an active PTY nudge in addition to the mail.
	//
	// SET THIS ONLY WHEN MAIL ALONE CANNOT BE READ, and A2 is the case it exists
	// for. Every agent's mail-check loop is a `pogo schedule` entry, so if the
	// scheduler failed to load then the mail lands in a maildir nothing will
	// ever wake the recipient to read. mg-c3f0 §6 stopped exactly here: "mailing
	// the coordinator works, but the coordinator will not be WOKEN to read it,
	// only mailed." The nudge closes that, and it is not dead by the same fault
	// — see conditionWake.
	Wake bool
}

// conditionNoticeState is one remembered annunciation.
type conditionNoticeState struct {
	Fingerprint string    `json:"fingerprint"`
	Row         string    `json:"row,omitempty"`
	To          string    `json:"to"`
	FirstSeen   time.Time `json:"first_seen"`
	// NotifiedAt is stamped only on a SUCCESSFUL send. A mail that failed must
	// not be remembered as delivered or the retry never happens and the alarm
	// dies silently — the defect one level up.
	NotifiedAt time.Time `json:"notified_at"`
}

// conditionNotices is the on-disk transition store.
//
// It has to be on disk for the same reason A1's does: most of these conditions
// are re-derived identically at every boot from unchanged inputs (a corrupt
// schedules.json is still corrupt next boot; an unwritable prompt dir is still
// unwritable), so the process restart IS the tick and in-process memory cannot
// suppress across it.
type conditionNotices struct {
	Version    int                             `json:"version"`
	Conditions map[string]conditionNoticeState `json:"conditions"`
}

func conditionNoticesPath() string {
	return filepath.Join(config.PogoHome(), conditionNoticesFile)
}

// conditionMailer is the send seam — client.SendMGMail in production, a recorder
// in tests. Without it a test run shells out to the real `mg` and manufactures a
// fleet alarm.
type conditionMailer func(to, from, subject, body string) error

// conditionWaker is the wake seam. In production it is a PTY nudge through the
// agent registry.
//
// WHY A NUDGE IS A LEGITIMATE CHANNEL FOR A2, against the reading that any
// in-pogod detector for A2 is dead by its own fault. The nudge does not go
// through the scheduler. pogod's heartbeat (internal/heartbeat) is what drives
// the scheduler, not the other way round — main.go's OnTick reads
// `if sched != nil { sched.Tick(...) }`, so a nil scheduler leaves the heartbeat
// ticking normally. Mail is likewise independent: client.SendMGMail shells out
// to `mg` (mg-c3f0 §4 verified this). So on the A2 boot, mail delivery works and
// the heartbeat works; the ONLY thing that stops working is the recipient's
// mail-check loop, and a nudge is exactly the substitute for that.
//
// This does not close the whole failure class, and the difference matters enough
// to state here rather than only in the ticket: a scheduler that LOADED and then
// silently stopped firing is a different fault from `scheduler.New` returning an
// error, it has no decision point inside pogod at all, and nothing here detects
// it. That one needs a positive out-of-process instrument ("at least one
// mail-check has acked in the last N minutes") and is argued in §4 of
// docs/investigations/pogod-condition-annunciation-2026-07-30.md, which also
// records the evidence for the paragraph above. What this file claims is
// narrower and true: for the
// condition the enumeration actually names, pogod both knows it and can reach a
// reader.
type conditionWaker func(agentName, message string) error

// conditionAnnunciator decides, per condition, whether this occurrence is a
// transition into the condition and therefore worth a mail.
//
// Every method is nil-receiver-safe: the daemon builds one before the first
// decision point, but the decision points must not grow a nil check each, and a
// test that does not care about annunciation should be able to pass nil.
type conditionAnnunciator struct {
	statePath string
	mail      conditionMailer
	wake      conditionWaker
	renotify  time.Duration
	minGap    time.Duration

	mu    sync.Mutex
	disk  conditionNotices
	dirty bool

	// mem shadows the store for THIS process. It is consulted on every decision
	// regardless of disk health, and it is the reason a broken store cannot
	// become a mail storm: loadConditionNotices deliberately fails toward
	// "nothing remembered" (never toward silence), which on its own would
	// re-announce on every raise — and A9 and A11 raise on a timer, not at boot.
	// With the shadow, an unreadable store costs at most one mail per condition
	// per conditionMinInterval per process lifetime.
	mem map[string]conditionNoticeState

	// pendingWakes are queued PTY nudges. A wake is queued rather than sent
	// inline because the conditions that need one are detected during startup,
	// BEFORE crew auto-start — at the moment A2 is known there is no coordinator
	// process to nudge yet. See retryWakes.
	pendingWakes map[string]*pendingWake

	raised     int
	suppressed int
	failed     int
}

type pendingWake struct {
	to      string
	message string
	id      string
	row     string
	queued  time.Time
	tries   int
}

func newConditionAnnunciator(statePath string, mail conditionMailer, wake conditionWaker) *conditionAnnunciator {
	return &conditionAnnunciator{
		statePath:    statePath,
		mail:         mail,
		wake:         wake,
		renotify:     conditionRenotifyAfter,
		minGap:       conditionMinInterval,
		disk:         loadConditionNotices(statePath),
		mem:          map[string]conditionNoticeState{},
		pendingWakes: map[string]*pendingWake{},
	}
}

// loadConditionNotices reads the store, treating every failure as "nothing
// remembered".
//
// THE BIAS IS TOWARD NOISE, NEVER SILENCE — same as A1's store and for the same
// reason. Reading a corrupt file as "already told them" would let a bad byte
// silently disable every alarm in this file, which is the exact class mg-c3f0
// exists to remove. The unreadable case is also LOUD (it logs), because a store
// that has been unreadable for a month is itself a condition.
func loadConditionNotices(path string) conditionNotices {
	empty := conditionNotices{Version: 1, Conditions: map[string]conditionNoticeState{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var n conditionNotices
	if err := json.Unmarshal(data, &n); err != nil {
		log.Printf("pogod: condition notice store %s is unreadable (%v) — re-announcing any live conditions", path, err)
		return empty
	}
	if n.Conditions == nil {
		n.Conditions = map[string]conditionNoticeState{}
	}
	n.Version = 1
	return n
}

func saveConditionNotices(path string, n conditionNotices) error {
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func conditionFingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Raise annunciates a condition if this occurrence is a transition into it.
//
// The decision, in order: unseen → mail (transition in); a materially different
// failure → mail (the recipient's problem is not the one they were told about);
// re-addressed → mail (the coordinator was renamed under us and the old mailbox
// is now unread); still live past the renotify window → mail; otherwise stay
// quiet and keep the previous delivery stamp so the renotify clock runs from the
// last DELIVERY rather than restarting on every occurrence.
func (a *conditionAnnunciator) Raise(c pogodCondition, now time.Time) {
	if a == nil {
		return
	}
	if c.To == "" {
		// No addressee resolved. Do not guess a name: mail to a name no agent
		// reads is silently accepted into a phantom mailbox and lost, which
		// would recreate this ticket's defect with extra steps. Say so loudly
		// and put it on the spine — an unroutable condition is worse news than
		// the condition.
		log.Printf("pogod: ⚠ condition %s (%s) has NO ADDRESSEE — not mailed: %s", c.ID, c.Row, c.Detail)
		a.emit(c, "", false, "unroutable", "no addressee resolved")
		a.mu.Lock()
		a.failed++
		a.mu.Unlock()
		return
	}

	fp := c.Fingerprint
	if fp == "" {
		fp = conditionFingerprint(c.Detail)
	}

	a.mu.Lock()
	prev, seen := a.mem[c.ID]
	if !seen {
		prev, seen = a.disk.Conditions[c.ID]
	}
	reason := ""
	switch {
	case !seen:
		reason = "new"
	case prev.To != c.To:
		reason = "readdressed"
	case prev.Fingerprint != fp && now.Sub(prev.NotifiedAt) >= a.minGap:
		reason = "changed"
	case now.Sub(prev.NotifiedAt) >= a.renotify:
		reason = "unresolved"
	default:
		// Suppressed. Carry the stamp forward unchanged.
		st := prev
		st.Row = c.Row
		a.mem[c.ID] = st
		a.disk.Conditions[c.ID] = st
		a.dirty = true
		a.suppressed++
		a.mu.Unlock()
		a.emit(c, c.To, false, "suppressed", "")
		return
	}
	firstSeen := now
	if seen && !prev.FirstSeen.IsZero() {
		firstSeen = prev.FirstSeen
	}
	a.mu.Unlock()

	subject, body := c.Subject, c.Body
	if err := a.mail(c.To, conditionMailFrom, subject, body); err != nil {
		// The notifier failed. Be loud, put the error on the spine, and do NOT
		// remember this as announced — dropping it means the next occurrence
		// treats it as new and tries again.
		log.Printf("pogod: ⚠ condition notice to %s FAILED for %s (%s): %v — "+
			"the condition is UNANNOUNCED; retrying on the next occurrence", c.To, c.ID, c.Row, err)
		a.mu.Lock()
		a.failed++
		delete(a.mem, c.ID)
		delete(a.disk.Conditions, c.ID)
		a.dirty = true
		a.mu.Unlock()
		a.emit(c, c.To, false, reason, err.Error())
		return
	}

	st := conditionNoticeState{Fingerprint: fp, Row: c.Row, To: c.To, FirstSeen: firstSeen, NotifiedAt: now}
	a.mu.Lock()
	a.mem[c.ID] = st
	a.disk.Conditions[c.ID] = st
	a.dirty = true
	a.raised++
	if c.Wake && a.wake != nil {
		a.pendingWakes[c.ID] = &pendingWake{
			to:      c.To,
			message: conditionWakeMessage(c),
			id:      c.ID,
			row:     c.Row,
			queued:  now,
		}
	}
	a.mu.Unlock()

	log.Printf("pogod: condition %s (%s) mailed to %s (%s)", c.ID, c.Row, c.To, reason)
	a.emit(c, c.To, true, reason, "")
}

// Clear forgets a condition, so a recurrence reads as a fresh transition and
// mails immediately instead of inheriting a quiet window from a resolved
// incident.
//
// EVERY WIRED CONDITION MUST HAVE A CLEAR ON ITS HEALTHY PATH. Without it the
// store grows monotonically and — worse — a condition that broke, was fixed, and
// broke again would be suppressed by its own history. This is the half that is
// easy to forget because nothing fails when you do.
func (a *conditionAnnunciator) Clear(id string, now time.Time) {
	if a == nil {
		return
	}
	a.mu.Lock()
	prev, onDisk := a.disk.Conditions[id]
	_, inMem := a.mem[id]
	if !onDisk && !inMem {
		a.mu.Unlock()
		return
	}
	delete(a.disk.Conditions, id)
	delete(a.mem, id)
	delete(a.pendingWakes, id)
	a.dirty = true
	a.mu.Unlock()

	log.Printf("pogod: condition %s CLEARED (was live since %s)", id, prev.FirstSeen.Format(time.RFC3339))
	events.Emit(context.Background(), events.Event{
		EventType: conditionClearedEvent,
		Agent:     prev.To,
		Details: map[string]any{
			"condition":  id,
			"row":        prev.Row,
			"first_seen": prev.FirstSeen.Format(time.RFC3339),
			"cleared_at": now.Format(time.RFC3339),
		},
	})
}

// retryWakes delivers queued PTY nudges. Called from the heartbeat tick, which
// is why it works at all: the wake-needing conditions are detected during
// startup, before crew auto-start, so there is no process to nudge at the
// moment the condition is known. Retrying on the heartbeat means the nudge
// lands on the first tick after the addressee is actually up.
//
// A wake that never lands is not a silent failure: the mail is already in the
// maildir, the raise is already on the spine, and each abandoned wake logs and
// emits. It degrades to "mailed but not woken", which is the state mg-c3f0 §6
// described, and it says so.
func (a *conditionAnnunciator) retryWakes(now time.Time) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if len(a.pendingWakes) == 0 {
		a.mu.Unlock()
		return
	}
	ids := make([]string, 0, len(a.pendingWakes))
	for id := range a.pendingWakes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	batch := make([]*pendingWake, 0, len(ids))
	for _, id := range ids {
		batch = append(batch, a.pendingWakes[id])
	}
	wake := a.wake
	a.mu.Unlock()

	for _, w := range batch {
		w.tries++
		err := error(nil)
		if wake == nil {
			err = fmt.Errorf("no wake channel")
		} else {
			err = wake(w.to, w.message)
		}
		if err == nil {
			log.Printf("pogod: condition %s (%s) — %s WOKEN to read the notice (try %d)", w.id, w.row, w.to, w.tries)
			a.mu.Lock()
			delete(a.pendingWakes, w.id)
			a.mu.Unlock()
			events.Emit(context.Background(), events.Event{
				EventType: conditionEvent,
				Agent:     w.to,
				Details: map[string]any{
					"condition": w.id, "row": w.row, "notified": true,
					"reason": "woken", "tries": w.tries,
				},
			})
			continue
		}
		if now.Sub(w.queued) >= conditionWakeDeadline {
			log.Printf("pogod: ⚠ condition %s (%s) — could not wake %s after %d tries (%v); "+
				"the notice IS in its maildir but nothing will prompt it to read — "+
				"this condition is mailed-but-not-woken", w.id, w.row, w.to, w.tries, err)
			a.mu.Lock()
			delete(a.pendingWakes, w.id)
			a.mu.Unlock()
			events.Emit(context.Background(), events.Event{
				EventType: conditionEvent,
				Agent:     w.to,
				Details: map[string]any{
					"condition": w.id, "row": w.row, "notified": false,
					"reason": "wake_abandoned", "tries": w.tries, "wake_error": err.Error(),
				},
			})
		}
	}
}

// flush persists the store. Failing to persist means the next boot re-announces,
// which is the harmless direction.
func (a *conditionAnnunciator) flush() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.dirty {
		a.mu.Unlock()
		return
	}
	snapshot := conditionNotices{Version: 1, Conditions: map[string]conditionNoticeState{}}
	for k, v := range a.disk.Conditions {
		snapshot.Conditions[k] = v
	}
	a.dirty = false
	path := a.statePath
	a.mu.Unlock()

	if err := saveConditionNotices(path, snapshot); err != nil {
		log.Printf("pogod: could not persist condition notice store %s: %v — "+
			"conditions may be re-announced next boot", path, err)
	}
}

// report is the answer to "how do you know this notifier has not quietly
// stopped". It runs once at the end of startup and puts the boot's counts on the
// spine and in the log.
//
// Three properties make a dead notifier visible:
//
//  1. A raise emits an event even when it SUPPRESSES, so a live condition
//     produces a steady stream of `reason: suppressed` events. Silence on the
//     spine means no conditions — it cannot mean "the notifier stopped", because
//     a stopped notifier stops emitting AND stops suppressing, and a genuinely
//     live condition that is not being suppressed shows up as a run of
//     `reason: new, notified: false`, which is a different shape.
//  2. A failed send is never stamped as delivered, so it retries and each
//     attempt carries `mail_error` on the event.
//  3. This summary fires on EVERY boot including the clean ones, so
//     `pogo events --type pogod_condition_summary` has a heartbeat of its own:
//     a daemon that boots and emits no summary is a daemon where this file is
//     not running at all.
func (a *conditionAnnunciator) report() {
	if a == nil {
		return
	}
	a.flush()
	a.mu.Lock()
	raised, suppressed, failed, live := a.raised, a.suppressed, a.failed, len(a.disk.Conditions)
	rows := make([]string, 0, live)
	for id, st := range a.disk.Conditions {
		rows = append(rows, id+"("+st.Row+")")
	}
	sort.Strings(rows)
	a.mu.Unlock()

	switch {
	case failed > 0:
		log.Printf("pogod: ⚠ condition annunciator: %d mailed, %d suppressed, %d FAILED TO SEND, %d live [%v] — "+
			"a failed notice means an actionable condition reached nobody", raised, suppressed, failed, live, rows)
	case live > 0:
		log.Printf("pogod: condition annunciator: %d mailed, %d suppressed, %d live [%v]",
			raised, suppressed, live, rows)
	default:
		log.Printf("pogod: condition annunciator: no live conditions")
	}

	events.Emit(context.Background(), events.Event{
		EventType: conditionSummaryEvent,
		Agent:     "pogod",
		Details: map[string]any{
			"mailed":     raised,
			"suppressed": suppressed,
			"failed":     failed,
			"live":       live,
			"conditions": rows,
		},
	})
}

func (a *conditionAnnunciator) emit(c pogodCondition, to string, notified bool, reason, mailErr string) {
	details := map[string]any{
		"condition": c.ID,
		"row":       c.Row,
		"addressee": to,
		"notified":  notified,
		"reason":    reason,
	}
	if c.Detail != "" {
		details["detail"] = c.Detail
	}
	if mailErr != "" {
		details["mail_error"] = mailErr
	}
	events.Emit(context.Background(), events.Event{
		EventType: conditionEvent,
		// Attributed to the addressee, not to pogod: the condition is something
		// that agent has to deal with, so `pogo events --agent <name>` should
		// show it in that agent's history. Matches A1.
		Agent:   to,
		Details: details,
	})
}

func conditionWakeMessage(c pogodCondition) string {
	return fmt.Sprintf("pogod condition %s (%s): %s — a notice is in your mailbox "+
		"(`mg mail list %s`). You are being nudged directly because this condition can stop "+
		"your mail-check loop from ever firing, so mail alone would not reach you.",
		c.ID, c.Row, c.Subject, c.To)
}

// conditionBody is the shared frame for every notice: what broke, what it costs
// while unfixed, what to do, and why this arrives as mail at all.
//
// The last part is not filler. A1's whole history is a correct, loud,
// well-worded log line that nobody received for seven days, and a recipient who
// does not know why a daemon is mailing them is a recipient who will filter it.
func conditionBody(row, what, cost, remedy, detail string) string {
	return fmt.Sprintf(
		"%s\n\n"+
			"DETAIL\n  %s\n\n"+
			"WHAT IT COSTS WHILE UNFIXED\n  %s\n\n"+
			"WHAT TO DO\n  %s\n\n"+
			"WHY THIS IS MAIL AND NOT JUST A LOG LINE. pogod has always logged this condition\n"+
			"correctly, by name, in ~/Library/Logs/pogo/pogod.log — a file on no agent's\n"+
			"schedule. Row %s of docs/investigations/pogod-log-conditions-with-no-reader-2026-07-30.md\n"+
			"is one of 15 conditions found to have an actor who could act and no channel to\n"+
			"reach them; the first of them (a declined prompt sync) logged perfectly at every\n"+
			"boot for seven days while the coordinator ran 13-day-stale guidance, and was found\n"+
			"only by accident. So this now reaches the agent that can act on it.\n\n"+
			"You will not get this every boot. It is sent when the condition first appears, when\n"+
			"the failure materially changes, and then at most once every %s while it stays live.\n"+
			"When it clears, a pogod_condition_cleared event is emitted and the next recurrence\n"+
			"mails you immediately rather than inheriting this quiet window.\n"+
			"Audit trail: `pogo events --type pogod_condition` (mg-342d).",
		what, detail, cost, remedy, row, conditionRenotifyAfter)
}
