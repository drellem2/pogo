package promptedit

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Default cadences for the standing runner.
const (
	// DefaultInterval is how often the runner samples. Coarse: the scan is a
	// non-recursive read of a handful of directories with no network and no
	// subprocess, so cost is not what sets this. What sets it is that a
	// hand-edit is a STEADY STATE, not an event — the file stays edited until
	// somebody reconciles it — so sampling faster buys nothing. Six hours puts
	// four samples in a day, which is enough that an edit made in the morning is
	// reported the same working day.
	DefaultInterval = 6 * time.Hour

	// DefaultRenotifyAfter is how long an UNCHANGED finding stays quiet before
	// being raised again.
	//
	// Deliberately long, and it matches promptSyncRenotifyAfter for the same
	// reason: reconciling a prompt is a judgement about which local edits are
	// still load-bearing, not a command to run, and a nag that arrives faster
	// than the work can reasonably be scheduled trains the recipient to filter
	// it — which is the failure mode of the incident this detector exists for,
	// one level up. An edit that is deliberate and permanent will produce this
	// notice roughly twice a week forever, and that is the intended cost of
	// keeping it visible.
	DefaultRenotifyAfter = 72 * time.Hour

	// NoticesFile holds the cross-restart suppression state, beside the other
	// small daemon state files at the POGO_HOME root.
	NoticesFile = "prompt-edit-notices.json"

	// mailFrom is the sender every notice carries, so a recipient can filter on
	// it and tell these from pogod-promptsync's declined-sync mail — a
	// different condition with a different remedy.
	mailFrom = "pogod-promptedit"

	// ranEvent is emitted on EVERY sample including the clean ones. "The
	// detector ran and found nothing" and "the detector has not run since the
	// last restart" are the two states this whole lineage keeps confusing, and
	// an absence cannot tell them apart. This is the positive record.
	ranEvent = "prompt_edit_watch_ran"
	// firedEvent records one notice decision, sent or suppressed or failed.
	firedEvent = "prompt_edit_watch_fired"
	// errorEvent records a sweep that could not run at all — distinct from a
	// sweep that ran and found nothing.
	errorEvent = "prompt_edit_watch_error"
)

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. It is the ONLY side-effect channel this package has: there is no
// repair seam, by construction, and see the package doc for why adding one
// would be worse than the defect.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log. Defaults to events.Emit.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	// Enabled arms the runner.
	Enabled bool
	// Root is the installed prompt tree (~/.pogo/agents). Required.
	Root string
	// ShippedFS is the reference corpus that defines the domain. Production
	// passes agent.DefaultPromptsFS(); see LoadShipped for why the embed and
	// not a git ref.
	ShippedFS fs.FS
	// Coordinator is the configured coordinator name, used to address prompts
	// no running agent owns. Empty is refused at New rather than defaulted: a
	// guessed name is a phantom mailbox, and mail into one is lost silently.
	Coordinator string
	// Mail delivers the notice. Required — a runner that cannot report is
	// pointless.
	Mail MailFunc
	// Emit writes the prompt_edit_watch_* events. Defaults to events.Emit.
	Emit Emitter
	// Interval is the coarse sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// RenotifyAfter is how long an unchanged finding stays quiet. Zero means
	// DefaultRenotifyAfter.
	RenotifyAfter time.Duration
	// StatePath is where the suppression store lives. Empty disables
	// persistence, which is a TEST-ONLY posture — see Watcher.
	StatePath string
}

// Watcher is the standing hand-edit detector: it rides pogod's heartbeat,
// samples the installed prompt corpus on a coarse interval, and mails the agent
// that can act on each edited file.
//
// # Why the heartbeat, and specifically NOT `pogo doctor --check`
//
// mg-10e3 is the siting hazard this ticket was told to read first, and it is a
// detector that is armed, correct, and reports into a surface whose read cadence
// is "when somebody thinks to type it". `pogo doctor --check` has no scheduled
// runner on this host: 23 scheduler entries and none runs it, no launchd plist
// references it, and doctor's own auto_start is false. Putting this sweep behind
// doctor would reproduce that defect one level down — a scheduled detector
// feeding an unscheduled reader.
//
// It is not a launchd timer either. The nondemand-spawn wedge on this box
// (mg-50e0) leaves launchd timers silently never firing, which is exactly the
// "inert while appearing correct" failure this detector exists to catch.
//
// pogod's heartbeat already ticks ~30s and already drives the reaper, the
// scheduler and half a dozen sibling watchers. This rides it and throttles
// itself to Interval.
//
// # Why it mails the affected agent
//
// Because that is what demonstrably worked. On 2026-08-20 the ONLY reason a
// hand-edited mayor.md was noticed at all is that pogod mailed the affected
// agent directly when it declined a colliding update (the prompt-sync notice,
// mg-c3f0). And the affected agent is the only party who can judge whether a
// given edit is still load-bearing — the judgement this detector deliberately
// refuses to make. Routing to `human` instead would put a fleet condition in a
// maildir with ~800 unread messages, which is the defect wearing the fix's
// clothes.
//
// # Notification policy
//
// Per PATH, not per sweep. A finding mails on the transition into the condition,
// again if the body changes (the edit grew, so the recipient's merge job is now
// different), and then at most once per RenotifyAfter while it stays unresolved.
// A path that stops reading as edited is FORGOTTEN, so a recurrence is news
// again rather than inheriting a suppression window from a resolved incident.
//
// The state is on disk because a pogod restart must not reset the alarm clock.
// Unlike a declined sync — where the restart IS the tick — the tick here is the
// heartbeat, so in-memory state would work while the daemon lives and would
// re-mail every finding at every restart. On this host that is roughly daily.
//
// A store that cannot be read is treated as "nothing remembered", and the bias
// is deliberately toward NOISE: at worst a duplicate mail. The opposite bias
// would let a corrupt file silently disable the alarm, which is the class of
// defect this whole lineage exists to remove. Never fail toward silence.
type Watcher struct {
	enabled       bool
	root          string
	shippedFS     fs.FS
	coordinator   string
	mail          MailFunc
	emit          Emitter
	interval      time.Duration
	renotifyAfter time.Duration
	statePath     string

	mu      sync.Mutex
	lastRun time.Time
	ran     bool
	// memory backs the suppression store when statePath is empty (tests).
	memory notices
}

// notice is one remembered notification.
type notice struct {
	// Fingerprint is the body hash that was announced, so a FURTHER edit to the
	// same file re-notifies rather than being suppressed as "already told them".
	Fingerprint string `json:"fingerprint"`
	// NotifiedAt is stamped only on a SUCCESSFUL send. A mail that failed must
	// not be remembered as delivered, or the retry never happens and the alarm
	// dies silently.
	NotifiedAt time.Time `json:"notified_at"`
	// To records where it went, so a misroute is auditable after the fact and
	// so a renamed coordinator re-addresses rather than staying quiet.
	To string `json:"to"`
}

type notices struct {
	Version int               `json:"version"`
	Edits   map[string]notice `json:"edits"`
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
		root:          opts.Root,
		shippedFS:     opts.ShippedFS,
		coordinator:   opts.Coordinator,
		mail:          opts.Mail,
		emit:          emit,
		interval:      interval,
		renotifyAfter: renotify,
		statePath:     opts.StatePath,
		memory:        notices{Version: 1, Edits: map[string]notice{}},
	}
}

// Check runs one sample subject to the coarse throttle. It is the integration
// point for the heartbeat OnTick callback, and a no-op on all but the first tick
// of each interval.
//
// It refuses to run without a coordinator name rather than substituting one:
// every finding on a file no running agent owns is addressed to the
// coordinator, and a guessed name would deliver the whole report into a phantom
// mailbox that exists and is read by nobody.
func (w *Watcher) Check(now time.Time) {
	if w == nil || !w.enabled || w.mail == nil || w.root == "" || w.shippedFS == nil || w.coordinator == "" {
		return
	}
	if !w.due(now) {
		return
	}
	w.sample(now)
}

// due reports whether the interval has elapsed, recording now BEFORE the sample
// runs so a slow or failing sample still consumes its slot — one sample per
// interval, never one per tick.
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

// Sample runs one sweep unconditionally, ignoring the throttle. Exported for
// tests and for a caller that wants a reading now; Check is the heartbeat path.
func (w *Watcher) Sample(now time.Time) (Report, error) { return w.sample(now) }

func (w *Watcher) sample(now time.Time) (Report, error) {
	shipped, err := LoadShipped(w.shippedFS)
	if err != nil {
		w.emit(events.Event{EventType: errorEvent, Agent: "pogod",
			Details: map[string]any{"error": err.Error(), "stage": "shipped"}})
		return Report{}, err
	}
	rep, err := Scan(w.root, shipped, w.coordinator)
	if err != nil {
		// A tree that cannot be walked is the instrument failing, not a clean
		// corpus. Emit it so a blind detector is visible in the event log
		// rather than indistinguishable from a quiet one.
		w.emit(events.Event{EventType: errorEvent, Agent: "pogod",
			Details: map[string]any{"error": err.Error(), "stage": "scan", "root": w.root}})
		return rep, err
	}

	// The positive record, on EVERY run. The denominators travel with it so an
	// operator asking "was this detector seeing anything?" gets an answer from
	// the event log without re-reading mail bodies — and so a domain that has
	// quietly collapsed to zero judged files is visible as a number rather than
	// as a run of clean reports.
	w.emit(events.Event{EventType: ranEvent, Agent: "pogod", Details: map[string]any{
		"root":               w.root,
		"enumerated":         rep.Total(),
		"shipped_paths":      rep.ShippedPaths,
		"judged":             len(rep.Clean) + len(rep.Findings),
		"findings":           len(rep.Findings),
		"clean":              len(rep.Clean),
		"unreadable":         len(rep.Unreadable),
		"out_of_domain":      len(rep.OutOfDomain),
		"stamp_missing":      len(rep.OutOfDomainBy(ReasonStampMissing)),
		"upstream_withdrawn": len(rep.OutOfDomainBy(ReasonUpstreamWithdrawn)),
		"no_upstream":        len(rep.OutOfDomainBy(ReasonNoUpstream)),
	}})

	w.notify(rep, now)
	return rep, nil
}

// notify sends at most one mail per agent and updates the suppression store.
func (w *Watcher) notify(rep Report, now time.Time) {
	store := w.loadNotices()
	next := map[string]notice{}

	for _, rc := range rep.Recipients() {
		// Decide per path, then send once per agent if ANY of its paths is due.
		due := false
		reasons := map[string]string{}
		for _, f := range rc.Findings {
			prev, seen := store.Edits[f.Path]
			switch {
			case !seen:
				reasons[f.Path], due = "new", true
			case prev.Fingerprint != f.ActualHash:
				reasons[f.Path], due = "changed", true
			case prev.To != rc.Agent:
				reasons[f.Path], due = "readdressed", true
			case now.Sub(prev.NotifiedAt) >= w.renotifyAfter:
				reasons[f.Path], due = "unresolved", true
			default:
				reasons[f.Path] = "suppressed"
				// Carry the previous stamp forward so the renotify clock keeps
				// running from the last DELIVERY, not from this sweep.
				next[f.Path] = prev
			}
		}
		if !due {
			for _, f := range rc.Findings {
				w.emitFired(f, rc.Agent, false, reasons[f.Path], "")
			}
			continue
		}

		body := rc.Body(rep.Root, rep)
		if err := w.mail(rc.Agent, mailFrom, rc.Subject(), body); err != nil {
			// The finding was detected and could not be reported. Say so loudly
			// AND do not remember these paths as announced: dropping them means
			// the next sweep treats them as new and tries again. A notifier
			// that silently stops is this detector's own failure mode, one
			// level up.
			log.Printf("promptedit: ⚠ hand-edit notice to %s FAILED for %d file(s) (%v) — "+
				"the edit is unannounced; retrying next sweep", rc.Agent, len(rc.Findings), err)
			for _, f := range rc.Findings {
				delete(next, f.Path)
				w.emitFired(f, rc.Agent, false, reasons[f.Path], err.Error())
			}
			continue
		}
		for _, f := range rc.Findings {
			next[f.Path] = notice{Fingerprint: f.ActualHash, NotifiedAt: now, To: rc.Agent}
			w.emitFired(f, rc.Agent, true, reasons[f.Path], "")
		}
		log.Printf("promptedit: hand-edit notice mailed to %s for %s",
			rc.Agent, strings.Join(pathsOf(rc.Findings), ", "))
	}

	// Only paths still reading as edited survive, so a reconciled prompt is
	// forgotten and a recurrence mails immediately rather than inheriting a
	// suppression window from the resolved incident.
	store.Edits = next
	w.saveNotices(store)
}

func pathsOf(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

func (w *Watcher) emitFired(f Finding, to string, notified bool, reason, mailErr string) {
	details := map[string]any{
		"path":          f.Path,
		"addressee":     to,
		"owner":         f.Owned,
		"recorded_hash": f.RecordedHash,
		"actual_hash":   f.ActualHash,
		"stamp_version": f.StampVersion,
		"notified":      notified,
		"reason":        reason,
	}
	if mailErr != "" {
		details["mail_error"] = mailErr
	}
	// Attributed to the affected agent, not to pogod: the condition is about
	// that agent's prompt, so `pogo events --agent <name>` shows it in that
	// agent's history.
	w.emit(events.Event{EventType: firedEvent, Agent: to, Details: details})
}

// loadNotices reads the suppression store, treating every failure as "nothing
// remembered". See the Watcher doc for why the bias is toward noise.
func (w *Watcher) loadNotices() notices {
	if w.statePath == "" {
		w.mu.Lock()
		defer w.mu.Unlock()
		out := notices{Version: 1, Edits: map[string]notice{}}
		for k, v := range w.memory.Edits {
			out.Edits[k] = v
		}
		return out
	}
	empty := notices{Version: 1, Edits: map[string]notice{}}
	data, err := os.ReadFile(w.statePath)
	if err != nil {
		return empty
	}
	var n notices
	if err := json.Unmarshal(data, &n); err != nil {
		log.Printf("promptedit: notice store %s is unreadable (%v) — re-announcing any hand-edits", w.statePath, err)
		return empty
	}
	if n.Edits == nil {
		n.Edits = map[string]notice{}
	}
	n.Version = 1
	return n
}

func (w *Watcher) saveNotices(n notices) {
	if w.statePath == "" {
		w.mu.Lock()
		w.memory = n
		w.mu.Unlock()
		return
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err == nil {
		if mkErr := os.MkdirAll(filepath.Dir(w.statePath), 0755); mkErr != nil {
			err = mkErr
		} else {
			err = os.WriteFile(w.statePath, append(data, '\n'), 0644)
		}
	}
	if err != nil {
		// Failing to persist means the next sweep re-announces. That is the
		// harmless direction; log it and carry on.
		log.Printf("promptedit: could not persist notice store %s: %v — "+
			"hand-edits may be re-announced", w.statePath, err)
	}
}

// NoticesPath is where the suppression store lives under a given POGO_HOME.
func NoticesPath(pogoHome string) string { return filepath.Join(pogoHome, NoticesFile) }

// Summary is the one-line arming report pogod logs at startup, so an operator
// can see the detector is wired without reading the event log.
func (w *Watcher) Summary() string {
	if w == nil || !w.enabled {
		return "disabled"
	}
	return fmt.Sprintf("interval=%s renotify=%s root=%s coordinator=%s (report-only)",
		w.interval, w.renotifyAfter, w.root, w.coordinator)
}
