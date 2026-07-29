package deafwatch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	// Source produces the snapshot. Required.
	Source SourceFunc
	// Mail delivers the announcement. Required — a detector that cannot report
	// is precisely the thing this package exists to stop existing.
	Mail MailFunc
	// Emit writes deaf_watch_* events. Defaults to events.Emit.
	Emit Emitter
	// Interval is the sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// HoldDown is how long a missing mail loop must persist, unbroken, before
	// it is announced. Zero means DefaultHoldDown. NEGATIVE disables the
	// hold-down, which only tests should do: without it, every pogod restart
	// announces the whole fleet in the gap between spawn and registration.
	HoldDown time.Duration
	// RenotifyAfter is how long an unchanged roster stays quiet. Zero means
	// DefaultRenotifyAfter.
	RenotifyAfter time.Duration
	// NotifyTo is the mailbox announcements go to. Empty means DefaultNotifyTo.
	NotifyTo string
	// EscalateAfter is how long a finding may persist before the notice ALSO
	// goes to EscalateTo. Zero means DefaultEscalateAfter; NEGATIVE disables
	// the AGE-based escalation only — a finding that names NotifyTo itself
	// still escalates immediately, because that one is not a matter of
	// patience (see escalateNow). Zero and negative must differ, or a config
	// that omits the key would silently turn escalation off.
	EscalateAfter time.Duration
	// EscalateTo receives escalated notices. Empty means DefaultEscalateTo.
	EscalateTo string
	// Enabled arms the runner.
	Enabled bool
}

// Watcher is the standing announcement for an agent with no mail-delivery path:
// it rides pogod's heartbeat, applies diagnose's own mail-loop judgement to the
// whole registry, and mails when an agent has been unreachable for longer than
// the hold-down.
//
// It rides the heartbeat rather than a launchd timer for the same reason
// internal/ackwatch, internal/driftwatch and internal/ghteardown do: the
// nondemand-spawn wedge on this box (mg-50e0) leaves launchd timers silently
// never firing — which for THIS detector would be especially apt, since it
// exists to catch a scheduled thing that silently stopped happening.
//
// # Episode semantics
//
// While at least one agent is confirmed deaf an episode is open. A changed
// roster mails immediately; an unchanged one stays quiet until RenotifyAfter.
// When the last finding clears, the episode closes: a clear mail goes to
// everyone who was told, and a generic
// incident_episode_cleared{kind:"deaf_agent"} event carries the roster and
// window (the mg-55b2 contract) so the notifier coalesces the close into one
// notification instead of a swarm.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	holdDown      time.Duration
	renotifyAfter time.Duration
	escalateAfter time.Duration
	notifyTo      string
	escalateTo    string
	source        SourceFunc
	mail          MailFunc
	emit          Emitter

	mu      sync.Mutex
	lastRun time.Time
	ran     bool
	// sinceMissing is when each agent was FIRST observed missing in the current
	// unbroken run of observations. An agent that regains its loop is deleted,
	// so a flap restarts the hold-down rather than accumulating toward it.
	sinceMissing map[string]time.Time
	// announced is the set already reported in this episode, so an agent that
	// joins a live episode is news (roster change) but does not reset the
	// clocks of the ones already in it.
	roster     map[string]Finding
	confirmed  map[string]time.Time
	episodeID  string
	openedAt   time.Time
	lastPrint  string
	lastMailed time.Time
	// toldEscalate records whether EscalateTo was ever included in this
	// episode's mail, so the clear mail reaches everyone who was alarmed. An
	// all-clear that goes to fewer mailboxes than the alarm leaves someone
	// holding an open incident forever.
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
	hold := opts.HoldDown
	if hold == 0 {
		hold = DefaultHoldDown
	}
	if hold < 0 {
		hold = 0
	}
	renotify := opts.RenotifyAfter
	if renotify <= 0 {
		renotify = DefaultRenotifyAfter
	}
	escalate := opts.EscalateAfter
	if escalate == 0 {
		escalate = DefaultEscalateAfter
	}
	notifyTo := opts.NotifyTo
	if notifyTo == "" {
		notifyTo = DefaultNotifyTo
	}
	escalateTo := opts.EscalateTo
	if escalateTo == "" {
		escalateTo = DefaultEscalateTo
	}
	return &Watcher{
		enabled: opts.Enabled, interval: interval, holdDown: hold,
		renotifyAfter: renotify, escalateAfter: escalate,
		notifyTo: notifyTo, escalateTo: escalateTo,
		source: opts.Source, mail: opts.Mail, emit: emit,
		sinceMissing: map[string]time.Time{},
		roster:       map[string]Finding{},
		confirmed:    map[string]time.Time{},
	}
}

// Check runs one sample subject to the throttle. It is the integration point
// for the heartbeat OnTick callback, and a no-op on all but the first tick of
// each interval.
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
	snap, err := w.source(now)
	if err != nil {
		// A fleet that cannot be judged is a real failure, not a clean scan.
		// Emit it so a blind detector is visible in the event log rather than
		// indistinguishable from a quiet one — the founding bug of this whole
		// lineage, one level up.
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Details:   map[string]any{"error": err.Error()},
		})
		return
	}

	confirmed, pending := w.observe(snap, now)

	for _, f := range pending {
		w.emit(events.Event{
			EventType: EventPending,
			Agent:     "pogod",
			Details: map[string]any{
				"target":    f.Name,
				"identity":  f.identity(),
				"hold_down": w.holdDown.String(),
				"why":       "no mail-check schedule; waiting out the hold-down before announcing, because spawn and registration are not simultaneous",
			},
		})
	}

	if len(confirmed) == 0 {
		w.closeEpisode(snap, now)
		return
	}

	w.announce(snap, confirmed, now)
}

// observe folds one snapshot into the hold-down state and returns the findings
// old enough to announce plus the ones that just entered the window.
func (w *Watcher) observe(snap Snapshot, now time.Time) (confirmed, pending []Finding) {
	w.mu.Lock()
	defer w.mu.Unlock()

	seen := make(map[string]bool, len(snap.Missing))
	for _, f := range snap.Missing {
		if f.Name == "" {
			continue
		}
		seen[f.Name] = true
		at, known := w.sinceMissing[f.Name]
		if !known {
			at = now
			w.sinceMissing[f.Name] = at
			pending = append(pending, f)
		}
		if now.Sub(at) >= w.holdDown {
			confirmed = append(confirmed, f)
		}
	}
	// An agent whose loop came back leaves the hold-down state entirely, so a
	// later recurrence starts its clock from scratch rather than inheriting
	// credit for an interruption that was in fact repaired.
	for name := range w.sinceMissing {
		if !seen[name] {
			delete(w.sinceMissing, name)
		}
	}
	sort.Slice(confirmed, func(i, j int) bool { return confirmed[i].Name < confirmed[j].Name })
	sort.Slice(pending, func(i, j int) bool { return pending[i].Name < pending[j].Name })
	return confirmed, pending
}

// announce records the episode roster and mails when the roster changed or the
// renotify interval elapsed.
func (w *Watcher) announce(snap Snapshot, confirmed []Finding, now time.Time) {
	print := fingerprint(confirmed)

	w.mu.Lock()
	if w.episodeID == "" {
		w.openedAt = now
		w.episodeID = makeEpisodeID(confirmed[0].Name, now)
	}
	for _, f := range confirmed {
		w.roster[f.Name] = f
		if _, ok := w.confirmed[f.Name]; !ok {
			w.confirmed[f.Name] = now
		}
	}
	// Forget clocks for findings that cleared while the episode stayed open, so
	// escalation ages describe outstanding findings only.
	live := make(map[string]bool, len(confirmed))
	for _, f := range confirmed {
		live[f.Name] = true
	}
	var oldest time.Time
	for name, at := range w.confirmed {
		if !live[name] {
			delete(w.confirmed, name)
			continue
		}
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	since := make(map[string]time.Time, len(w.sinceMissing))
	for k, v := range w.sinceMissing {
		since[k] = v
	}
	shouldMail := print != w.lastPrint || now.Sub(w.lastMailed) >= w.renotifyAfter
	if shouldMail {
		w.lastPrint = print
		w.lastMailed = now
	}
	episodeID := w.episodeID
	w.mu.Unlock()

	if !shouldMail {
		return
	}

	stale := w.escalateAfter > 0 && !oldest.IsZero() && now.Sub(oldest) >= w.escalateAfter
	deafCoordinator := escalateNow(confirmed, w.notifyTo)

	body := renderBody(snap, confirmed, since, now)
	recipients := []string{w.notifyTo}
	if (stale || deafCoordinator) && w.escalateTo != w.notifyTo {
		recipients = append(recipients, w.escalateTo)
		body = escalationPreamble(w.notifyTo, oldest, now, stale, deafCoordinator) + body
	}

	subject := "deaf-watch: " + mailSubject(confirmed)
	details := map[string]any{
		"episode_id":  episodeID,
		"count":       len(confirmed),
		"scanned":     snap.Scanned,
		"judged":      snap.Judged,
		"agents":      names(confirmed),
		"identities":  identities(confirmed),
		"notified":    strings.Join(recipients, ","),
		"escalated":   stale || deafCoordinator,
		"coordinator": deafCoordinator,
	}
	for _, to := range recipients {
		if err := w.mail(to, mailFrom, subject, body); err != nil {
			// The fault was detected and could not be reported. Record it: a
			// notice that reaches nobody is this ticket's bug, one level up.
			details["mail_error_"+to] = err.Error()
		}
	}
	if len(recipients) > 1 {
		w.mu.Lock()
		w.toldEscalate = true
		w.mu.Unlock()
	}
	w.emit(events.Event{EventType: EventFired, Agent: "pogod", Details: details})
}

// closeEpisode ends an open episode: it mails the all-clear to everyone who was
// alarmed and emits the generic incident_episode_cleared event. It is a no-op
// when no episode is open, so a quiet fleet stays quiet.
func (w *Watcher) closeEpisode(snap Snapshot, now time.Time) {
	w.mu.Lock()
	if w.episodeID == "" {
		// No episode open. Reset the fingerprint anyway so a deficit that
		// resolves and later recurs is news rather than a suppressed repeat.
		w.lastPrint = ""
		w.mu.Unlock()
		return
	}
	episodeID, openedAt := w.episodeID, w.openedAt
	told := w.toldEscalate
	roster := make([]Finding, 0, len(w.roster))
	for _, f := range w.roster {
		roster = append(roster, f)
	}
	w.roster = map[string]Finding{}
	w.confirmed = map[string]time.Time{}
	w.episodeID = ""
	w.openedAt = time.Time{}
	w.lastPrint = ""
	w.lastMailed = time.Time{}
	w.toldEscalate = false
	w.mu.Unlock()

	sort.Slice(roster, func(i, j int) bool { return roster[i].Name < roster[j].Name })

	w.emit(episodeClearedEvent(episodeID, identities(roster), openedAt, now))

	subject := "deaf-watch: mail loops restored — " + strings.Join(names(roster), ", ")
	body := fmt.Sprintf(
		"Every agent reported by deaf-watch in this episode now has a mail-check schedule again.\n\n"+
			"Restored:\n%s\n"+
			"Episode: %s\n  opened %s\n  cleared %s (%s)\n\n"+
			"Judged %d of %d agents in the registry this sample.\n",
		renderFindings(roster, nil, now), episodeID,
		openedAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339),
		now.Sub(openedAt).Round(time.Minute), snap.Judged, snap.Scanned)

	recipients := []string{w.notifyTo}
	if told && w.escalateTo != w.notifyTo {
		recipients = append(recipients, w.escalateTo)
	}
	for _, to := range recipients {
		if err := w.mail(to, mailFrom, subject, body); err != nil {
			w.emit(events.Event{
				EventType: EventError,
				Agent:     "pogod",
				Details:   map[string]any{"error": err.Error(), "phase": "clear", "to": to},
			})
		}
	}
}

// escalateNow reports whether the roster names the mailbox findings are
// normally sent to.
//
// This is the routing rule unique to this detector. Every sibling watcher
// escalates on AGE: a finding the fleet has had time to fix and has not. Here
// there is a case that must escalate on its FIRST announcement, because
// patience cannot help it — an agent with no mail loop cannot read mail, so a
// notice addressed to it is not a weaker alert, it is no alert. mg-d385 is the
// precedent that this is not hypothetical: the coordinator is itself a crew
// agent and has had the fleet's defects before its peers did.
func escalateNow(findings []Finding, notifyTo string) bool {
	for _, f := range findings {
		if f.Name == notifyTo || f.Identity == notifyTo {
			return true
		}
	}
	return false
}

func escalationPreamble(notifyTo string, oldest, now time.Time, stale, deafCoordinator bool) string {
	if deafCoordinator {
		return fmt.Sprintf(
			"ESCALATED IMMEDIATELY: %s — the mailbox this alert is normally sent to — is\n"+
				"itself one of the agents below. It has no mail loop, so it will never be woken\n"+
				"to read this. Routing the notice only to the patient is how a fault stays\n"+
				"invisible while looking reported (mg-d385).\n\n", notifyTo)
	}
	if stale && !oldest.IsZero() {
		age := now.Sub(oldest).Round(time.Hour)
		return fmt.Sprintf(
			"ESCALATED: a finding below has been reported to %s for %s without clearing.\n"+
				"An agent that cannot be reached cannot be asked to fix itself, and a fleet\n"+
				"that has not restored the loop in %s is not going to on its own.\n\n",
			notifyTo, age, age)
	}
	return ""
}

// renderBody builds the announcement. It leads with the agent NAMES, because
// not knowing which name to type is the whole reason this fault went
// unannounced for two tickets.
func renderBody(snap Snapshot, confirmed []Finding, since map[string]time.Time, now time.Time) string {
	var b strings.Builder
	b.WriteString("These agents have NO mail-check schedule. They can be mailed, and nothing\n")
	b.WriteString("will ever wake them to read it — every coordination path the fleet has runs\n")
	b.WriteString("through mail, so they are unreachable while looking perfectly healthy.\n\n")
	b.WriteString(renderFindings(confirmed, since, now))
	fmt.Fprintf(&b, "\nJudged %d of %d agents in the registry. Polecats, configured-but-stopped\n"+
		"agents, and agents whose prompt tree could not be read are NOT judged — see\n"+
		"agent.mailLoopJudgeable.\n\n", snap.Judged, snap.Scanned)
	b.WriteString("Confirm one:\n")
	for _, f := range confirmed {
		fmt.Fprintf(&b, "  pogo agent diagnose %s        # expect health=no_mail_loop\n", f.Name)
	}
	b.WriteString("\nRestore one:\n")
	for _, f := range confirmed {
		fmt.Fprintf(&b, "  pogo schedule %s --cron \"*/10 * * * *\" --id mail-check-%s \\\n"+
			"      --replay once --message \"Check your mail with mg mail list %s.\"\n",
			f.Name, f.Name, f.Name)
	}
	b.WriteString("\nList what the scheduler currently holds:\n  pogo schedule list\n")
	b.WriteString("\nThis is REPORT-ONLY — pogod did NOT register a schedule, nudge, or restart\n" +
		"anything. Registering the loop back on the agent's behalf would hide WHY it\n" +
		"vanished, and the reason is the part worth knowing.\n")
	return b.String()
}

// makeEpisodeID builds a stable per-episode id from the first agent in the
// roster and the episode's open time, byte-identical in shape to
// usagelimit.go's and synthwatch's. Deriving it from the injected clock keeps
// it deterministic under test.
func makeEpisodeID(firstAgent string, openedAt time.Time) string {
	return fmt.Sprintf("ep-%d-%s", openedAt.UTC().UnixNano(), firstAgent)
}

// episodeClearedEvent builds the structured incident_episode_cleared event for
// a deaf-episode close. It mirrors synthwatch's and usagelimit.go's emitters
// exactly — same event type, same Agent ("pogod"), same RFC3339Nano timestamps,
// same details field names and nesting — changing only details.kind to
// EpisodeKind. Do not diverge this shape from theirs, or from mg-e0f6's reader,
// without updating all of them.
func episodeClearedEvent(episodeID string, roster []string, openedAt, closedAt time.Time) events.Event {
	ids := append([]string(nil), roster...)
	sort.Strings(ids)
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

func names(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func identities(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.identity())
	}
	sort.Strings(out)
	return out
}
