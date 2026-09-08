package promptstale

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/staleness"
)

// Default cadences for the standing runner.
const (
	// DefaultInterval is how often the runner samples.
	//
	// Coarse, and cost is only half the reason: the sweep shells out to git
	// several times per shipped file against a local object store, plus one
	// bounded network call for the remote head. What actually sets it is that
	// staleness is a STEADY STATE with exactly one clearing event — a redeploy,
	// nightly at 03:00 — so sampling faster cannot report anything sooner than
	// the condition can change. Six hours puts four samples in a day, which is
	// enough that a corpus stranded by a failed nightly is announced the same
	// morning.
	DefaultInterval = 6 * time.Hour

	// DefaultRenotifyAfter is how long an UNCHANGED finding stays quiet.
	//
	// Shorter than promptedit's 72h, and the difference is deliberate rather
	// than an oversight. A hand-edit is somebody's deliberate local state and
	// nagging about it faster than it can be scheduled trains the recipient to
	// filter; a stale prompt is an agent running superseded instructions RIGHT
	// NOW, it is nobody's decision, and it clears on the next successful
	// nightly with no work at all. A notice that outlives more than one nightly
	// is reporting a nightly that is not fixing it, which is news.
	DefaultRenotifyAfter = 24 * time.Hour

	// NoticesFile holds the cross-restart suppression state, beside the other
	// small daemon state files at the POGO_HOME root.
	NoticesFile = "prompt-stale-notices.json"

	// mailFrom is the sender every notice carries, so a recipient can filter on
	// it and tell these apart from pogod-promptedit (a LOCAL edit) and
	// pogod-promptsync (a shipped update DECLINED because of one). Three
	// senders, three conditions, three different remedies.
	mailFrom = "pogod-promptstale"

	// ranEvent is emitted on EVERY sample including the clean ones. "The
	// detector ran and found nothing" and "the detector has not run since the
	// last restart" are the two states this whole lineage keeps confusing, and
	// an absence cannot tell them apart. This is the positive record.
	ranEvent = "prompt_stale_watch_ran"
	// firedEvent records one notice decision, sent or suppressed or failed.
	firedEvent = "prompt_stale_watch_fired"
	// errorEvent records a sweep that could not run at all — an unresolvable
	// ref, an unreadable tree — as distinct from one that ran and found the
	// fleet current.
	errorEvent = "prompt_stale_watch_error"
)

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. It is the ONLY side-effect channel this package has.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log. Defaults to events.Emit.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	// Enabled arms the runner.
	Enabled bool
	// Repo is the reference checkout and Ref the ref within it. Production
	// passes staleness.DeployReferenceRepo's answer and "origin/main"; both are
	// required, and an absent repo must be caught by the CALLER so the host can
	// be told the runner is disarmed rather than have it read as clean.
	Repo string
	Ref  string
	// Root is the installed prompt tree (~/.pogo/agents). Required.
	Root string
	// Coordinator is the configured coordinator name, used to address prompts
	// no running agent owns. Empty disarms the runner rather than defaulting: a
	// guessed name is a phantom mailbox, and mail into one is lost silently.
	Coordinator string
	// Mail delivers the notice. Required — a runner that cannot report is
	// pointless.
	Mail MailFunc
	// Emit writes the prompt_stale_watch_* events. Defaults to events.Emit.
	Emit Emitter
	// Interval is the coarse sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// RenotifyAfter is how long an unchanged finding stays quiet. Zero means
	// DefaultRenotifyAfter.
	RenotifyAfter time.Duration
	// RemoteTimeout bounds the live-remote query. Zero means
	// staleness.DefaultRemoteTimeout.
	RemoteTimeout time.Duration
	// SkipRemote disarms that query entirely. The corpus verdict is unaffected;
	// what is lost is the qualifier on the reference, which the notice then
	// says out loud. Off by default — the call is read-only and bounded.
	SkipRemote bool
	// StatePath is where the suppression store lives. Empty disables
	// persistence, which is a TEST-ONLY posture.
	StatePath string
}

// Watcher is the standing prompt-corpus staleness detector: it rides pogod's
// heartbeat, compares the installed corpus against a git ref on a coarse
// interval, and mails the agent that is reading each superseded file.
//
// # Why the heartbeat
//
// The same two reasons its sibling internal/promptedit gives, and they apply
// with more force here. `pogo doctor --check` has no scheduled runner on this
// host (mg-10e3), so siting a sweep behind it is a scheduled detector feeding an
// unscheduled reader. A launchd timer is worse: the nondemand-spawn wedge
// (mg-50e0) leaves timers silently never firing, and "inert while appearing
// correct" is the exact failure this detector exists to catch.
//
// There is a third reason particular to this condition. The artifact that goes
// stale is installed by pogod's own boot, so the interesting window is precisely
// the one in which pogod has NOT restarted. A check that ran at startup would be
// blind for the whole of it.
//
// # Why it mails the affected agent
//
// Because the agent reading a superseded prompt is the party being harmed, and
// it is the only one that can weigh "my instructions may be out of date" against
// what it is doing right now. Routing to `human` instead would put a fleet
// condition in a maildir with hundreds of unread messages — the defect wearing
// the fix's clothes (mg-c3f0). Findings on templates and pm-template, which no
// running agent owns, go to the coordinator, because it is the agent that
// dispatches from them.
//
// # Notification policy
//
// Per PATH, not per sweep. A finding mails on the transition into the condition,
// again whenever either side's hash moves (the recipient's situation is now a
// different one), again if it is re-addressed, and then at most once per
// RenotifyAfter while it stays unresolved. A path that stops reading as stale is
// FORGOTTEN, so a recurrence is news rather than inheriting a suppression window
// from a resolved incident.
//
// The state is on disk because a pogod restart must not reset the alarm clock —
// and here that is not a nicety. A redeploy restarts pogod AND installs prompts,
// so in the healthy case the restart clears the condition; in the case that
// matters, the daemon restarted for some other reason and in-memory state would
// re-announce every finding at every restart.
//
// A store that cannot be read is treated as "nothing remembered", biasing toward
// a duplicate mail. The opposite bias would let a corrupt file silently disable
// the alarm, which is the class of defect this lineage exists to remove. Never
// fail toward silence.
type Watcher struct {
	enabled       bool
	repo          string
	ref           string
	root          string
	coordinator   string
	mail          MailFunc
	emit          Emitter
	interval      time.Duration
	renotifyAfter time.Duration
	remoteTimeout time.Duration
	skipRemote    bool
	statePath     string

	mu      sync.Mutex
	lastRun time.Time
	ran     bool
	// memory backs the suppression store when statePath is empty (tests).
	memory notices
}

// notice is one remembered notification.
type notice struct {
	// Fingerprint is the finding's two-sided hash, so a further move on either
	// side re-notifies rather than being suppressed as "already told them".
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
	Stale   map[string]notice `json:"stale"`
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
	timeout := opts.RemoteTimeout
	if timeout <= 0 {
		timeout = staleness.DefaultRemoteTimeout
	}
	return &Watcher{
		enabled:       opts.Enabled,
		repo:          opts.Repo,
		ref:           opts.Ref,
		root:          opts.Root,
		coordinator:   opts.Coordinator,
		mail:          opts.Mail,
		emit:          emit,
		interval:      interval,
		renotifyAfter: renotify,
		remoteTimeout: timeout,
		skipRemote:    opts.SkipRemote,
		statePath:     opts.StatePath,
		memory:        notices{Version: 1, Stale: map[string]notice{}},
	}
}

// Check runs one sample subject to the coarse throttle. It is the integration
// point for the heartbeat OnTick callback, and a no-op on all but the first tick
// of each interval.
//
// Every precondition is checked here rather than assumed, and each missing one
// disarms the whole sweep rather than being substituted: a comparison with no
// reference repo, or findings addressed to a guessed coordinator, would produce
// confident output about nothing.
func (w *Watcher) Check(ctx context.Context, now time.Time) {
	if w == nil || !w.enabled || w.mail == nil || w.repo == "" || w.ref == "" || w.root == "" || w.coordinator == "" {
		return
	}
	if !w.due(now) {
		return
	}
	w.sample(ctx, now)
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
func (w *Watcher) Sample(ctx context.Context, now time.Time) Report {
	return w.sample(ctx, now)
}

func (w *Watcher) sample(ctx context.Context, now time.Time) Report {
	raw := staleness.CheckPrompts(ctx, staleness.PromptOptions{
		Repo:          w.repo,
		Ref:           w.ref,
		InstalledRoot: w.root,
		SkipRemote:    w.skipRemote,
		// Fetch stays false. See the package doc: a detector that mutates the
		// tree it judges has made itself a participant, and the fetch would
		// destroy the timestamp the reference's own age is read from.
		Fetch:         false,
		RemoteTimeout: w.remoteTimeout,
		Now:           now,
		// THE ONE CEILING THIS SWEEP CAN TAKE FOR FREE (mg-1e8e), and the one
		// that matters most here: pogod IS the automatic installer, so its own
		// embed is the ceiling on the automatic path — read in-process, with no
		// git call, no HTTP call and no reference lookup.
		//
		// Without it the notice prescribed `pogo agent prompt install` over a
		// state where every install is a no-op. Measured 2026-09-08: the
		// running pogod (7edd223, built 08-20) carried byte-for-byte what was
		// already in ~/.pogo/agents, and its boot installer had said so seven
		// times in the prompt_refresh stream — `changed=0 ... ok=true` — while
		// mayor.md sat 129 lines behind. An installer that carries what is
		// already on disk cannot close a gap, and the recipient has to be told
		// which of those two worlds they are in.
		Ceilings: []staleness.CeilingSource{
			staleness.EmbedCeilingSource(SelfCeilingName,
				"agent.InstallPrompts at every pogod boot — the automatic path, from THIS daemon's own embed",
				agent.DefaultPromptsFS()),
		},
	})
	rep := FromStaleness(raw, w.coordinator)
	// Set explicitly rather than threaded through FromStaleness: the witness
	// reports a skipped query as an unarmed one, and only the caller that asked
	// for the skip can tell the two apart.
	rep.RemoteSkipped = w.skipRemote

	if rep.Err != "" {
		// A comparison that could not be made has NOT found the fleet current.
		// Emit it so a blind detector is visible in the event log rather than
		// indistinguishable from a quiet one, and return without touching the
		// suppression store — a failed sweep must not forget what a working one
		// announced.
		w.emit(events.Event{EventType: errorEvent, Agent: "pogod", Details: map[string]any{
			"error": rep.Err, "repo": w.repo, "ref": w.ref, "root": w.root,
		}})
		log.Printf("promptstale: ⚠ sweep could not run (%s) — the corpus is UNJUDGED, not current", rep.Err)
		return rep
	}

	// The positive record, on EVERY run. The denominators travel with it so an
	// operator asking "was this detector seeing anything?" gets an answer from
	// the event log — and so a domain that has quietly collapsed to zero shipped
	// paths is visible as a number rather than as a run of clean reports. The
	// reference's own age and remote position travel too, because a clean sweep
	// against a frozen mirror is a weaker statement than a clean sweep and must
	// not be recorded as the same thing.
	details := map[string]any{
		"root":                w.root,
		"repo":                w.repo,
		"ref":                 w.ref,
		"reference_commit":    rep.Reference.Commit,
		"shipped_paths":       rep.Shipped,
		"findings":            len(rep.Findings),
		"unjudged":            len(rep.Unjudged),
		"reference_qualified": rep.ReferenceQualified(),
		"remote_behind":       rep.Remote.Behind,
	}
	if rep.Reference.Fetch.Known() {
		details["reference_fetch_age_seconds"] = rep.Reference.Fetch.AgeSeconds
	}
	w.emit(events.Event{EventType: ranEvent, Agent: "pogod", Details: details})

	w.notify(rep, now)
	return rep
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
			prev, seen := store.Stale[f.Path]
			switch {
			case !seen:
				reasons[f.Path], due = "new", true
			case prev.Fingerprint != f.Fingerprint():
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

		if err := w.mail(rc.Agent, mailFrom, rc.Subject(), rc.Body(rep)); err != nil {
			// The finding was detected and could not be reported. Say so loudly
			// AND do not remember these paths as announced: dropping them means
			// the next sweep treats them as new and tries again. A notifier that
			// silently stops is this detector's own failure mode, one level up.
			log.Printf("promptstale: ⚠ staleness notice to %s FAILED for %d file(s) (%v) — "+
				"that agent is still reading a superseded prompt and does not know; retrying next sweep",
				rc.Agent, len(rc.Findings), err)
			for _, f := range rc.Findings {
				delete(next, f.Path)
				w.emitFired(f, rc.Agent, false, reasons[f.Path], err.Error())
			}
			continue
		}
		for _, f := range rc.Findings {
			next[f.Path] = notice{Fingerprint: f.Fingerprint(), NotifiedAt: now, To: rc.Agent}
			w.emitFired(f, rc.Agent, true, reasons[f.Path], "")
		}
		log.Printf("promptstale: staleness notice mailed to %s for %s (reference %s = %s)",
			rc.Agent, strings.Join(pathsOf(rc.Findings), ", "), rep.Reference.Ref, shortSHA(rep.Reference.Commit))
	}

	// Only paths still reading as stale survive, so a redeployed prompt is
	// forgotten and a recurrence mails immediately rather than inheriting a
	// suppression window from the resolved incident.
	store.Stale = next
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
		"path":           f.Path,
		"kind":           f.Kind,
		"addressee":      to,
		"owner":          f.Owned,
		"shipped_hash":   f.ShippedHash,
		"installed_hash": f.InstalledHash,
		"notified":       notified,
		"reason":         reason,
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
		out := notices{Version: 1, Stale: map[string]notice{}}
		for k, v := range w.memory.Stale {
			out.Stale[k] = v
		}
		return out
	}
	empty := notices{Version: 1, Stale: map[string]notice{}}
	data, err := os.ReadFile(w.statePath)
	if err != nil {
		return empty
	}
	var n notices
	if err := json.Unmarshal(data, &n); err != nil {
		log.Printf("promptstale: notice store %s is unreadable (%v) — re-announcing any stale prompts", w.statePath, err)
		return empty
	}
	if n.Stale == nil {
		n.Stale = map[string]notice{}
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
		log.Printf("promptstale: could not persist notice store %s: %v — "+
			"stale prompts may be re-announced", w.statePath, err)
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
	remote := "remote-qualifier=on"
	if w.skipRemote {
		remote = "remote-qualifier=off"
	}
	return fmt.Sprintf("interval=%s renotify=%s root=%s reference=%s@%s coordinator=%s %s (report-only, never fetches)",
		w.interval, w.renotifyAfter, w.root, w.repo, w.ref, w.coordinator, remote)
}
