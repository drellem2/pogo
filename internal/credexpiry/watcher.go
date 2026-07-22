package credexpiry

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// mailFrom is the sender stamped on every expiry notice. Like drift-watch's,
// it is not an agent — the notice is a system-level alert — so it uses a fixed
// pogod-side identity.
const mailFrom = "cred-expiry"

// mailTo is `human`, not the mayor. Running `/login` is something ONLY a person
// can do; routing it to a coordination inbox would put a human-gated action in
// a queue no human reads promptly. `human` is what the apple-side notifier
// surfaces.
const mailTo = "human"

// DefaultInterval is how often the watcher samples. Deliberately coarse: the
// event being predicted is a month away and moves only when a human logs in, so
// sampling faster buys nothing. It is fine to be up to one interval late at the
// 2h tier — the tiers are lead times, not deadlines.
const DefaultInterval = 15 * time.Minute

// DefaultBlindRenotify throttles the "cannot read the credential" mail. Once a
// day: often enough that a blind warner is not forgotten, rare enough that a
// permanently-moved schema does not bury the inbox.
const DefaultBlindRenotify = 24 * time.Hour

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. It is the only side-effect channel this package has — there is no
// seam for refreshing or re-minting a credential, by design.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log. Defaults to events.Emit.
type Emitter func(events.Event)

// Options carries the watcher's dependencies so it is testable without a
// keychain, a clock, or a live mailer.
type Options struct {
	// Read obtains the credential Status. Defaults to SystemReader.
	Read Reader
	// Mail delivers notices. Required — a warner that cannot report is pointless.
	Mail MailFunc
	// Emit writes cred_expiry_* events. Defaults to events.Emit.
	Emit Emitter
	// Interval is the coarse sampling gap. Zero means DefaultInterval.
	Interval time.Duration
	// BlindRenotify throttles the unreadable-credential mail. Zero means
	// DefaultBlindRenotify.
	BlindRenotify time.Duration
	// Enabled is the off switch. Defaults to on at the pogod call site.
	Enabled bool
}

// Watcher samples the credential on a coarse interval and mails `human` as the
// expiry approaches.
//
// It rides pogod's heartbeat rather than a launchd timer, for the same reason
// drift-watch does: the nondemand-spawn wedge on this box (mg-50e0) leaves a
// launchd timer silently never firing, which is the exact inert-but-correct-
// looking failure a warning system must not have.
//
// pogod is also the right HOST specifically because it survives the condition
// it predicts. pogod is a Go daemon with no Claude credential of its own; when
// the grant lapses and every agent starts failing, pogod keeps ticking. That
// constraint is weaker here than for a reactive pager — this warner does its
// work while everything is still healthy — but the heartbeat is free, so there
// is no reason to take the weaker option.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	blindRenotify time.Duration
	read          Reader
	mail          MailFunc
	emit          Emitter

	mu      sync.Mutex
	lastRun time.Time
	ran     bool
	// mailedTier is the deepest tier already mailed. Tiers only deepen, so this
	// makes each tier mail exactly once instead of once per sample. It RESETS
	// when the observed expiry date moves — that is a `/login`, and the next
	// cycle deserves its own full escalation.
	mailedTier Tier
	// mailedFor is the expiry the ratchet refers to, so a new grant clears it.
	mailedFor time.Time
	// lastBlind is when the unreadable-credential mail last went out.
	lastBlind  time.Time
	blindEver  bool
	disarmOnce sync.Once
}

// New builds a Watcher, applying defaults.
func New(opts Options) *Watcher {
	read := opts.Read
	if read == nil {
		read = SystemReader
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	blind := opts.BlindRenotify
	if blind <= 0 {
		blind = DefaultBlindRenotify
	}
	emit := opts.Emit
	if emit == nil {
		emit = func(e events.Event) { events.Emit(context.Background(), e) }
	}
	return &Watcher{
		enabled:       opts.Enabled,
		interval:      interval,
		blindRenotify: blind,
		read:          read,
		mail:          opts.Mail,
		emit:          emit,
	}
}

// Check runs one sample subject to the coarse throttle. It is the integration
// point for pogod's heartbeat OnTick, which ticks every ~30s; Check is a no-op
// on all but the first tick of each interval.
func (w *Watcher) Check(ctx context.Context, now time.Time) {
	if w == nil || !w.enabled || w.mail == nil {
		return
	}
	if !w.due(now) {
		return
	}
	w.sample(ctx, now)
}

// due reports whether the interval has elapsed, recording now as the new sample
// time before the sample runs so a slow or failing sample still consumes its
// slot.
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

// sample reads the credential and mails if a deeper tier has been reached.
func (w *Watcher) sample(ctx context.Context, now time.Time) {
	st := w.read(ctx)

	switch st.State {
	case StateAbsent:
		// Nothing to warn about and — this is the important half — nothing
		// CLAIMED either. Say so once in the log so the silence is declared
		// rather than mistaken for a clean bill of health, then stay quiet.
		// Mailing here would spam every sandbox and non-macOS host.
		w.disarmOnce.Do(func() {
			log.Printf("pogod: credential-expiry warner is NOT armed — %s. "+
				"No advance warning of an auth expiry will be sent on this host.", st.Reason)
			w.emit(events.Event{
				EventType: "cred_expiry_disarmed",
				Agent:     "pogod",
				Details:   map[string]any{"reason": st.Reason},
			})
		})
		return

	case StateUnreadable:
		w.reportBlind(st, now)
		return
	}

	remaining := st.RefreshExpiry.Sub(now)
	tier := TierFor(remaining)

	w.mu.Lock()
	// A changed expiry means the grant was re-minted (a `/login`). Reset the
	// ratchet so the new 30-day cycle gets its own escalation from the top.
	if !st.RefreshExpiry.Equal(w.mailedFor) {
		w.mailedFor = st.RefreshExpiry
		w.mailedTier = TierNone
	}
	// Also clear the blind state: the credential is readable again.
	w.blindEver = false
	shouldMail := tier > w.mailedTier
	if shouldMail {
		w.mailedTier = tier
	}
	w.mu.Unlock()

	if !shouldMail {
		return
	}

	subject, body := WarningMail(tier, st, now)
	err := w.mail(mailTo, mailFrom, subject, body)

	details := map[string]any{
		"tier":            tier.String(),
		"expires_at":      st.RefreshExpiry.Format(time.RFC3339),
		"remaining":       FormatRemaining(remaining),
		"remaining_hours": int(remaining.Hours()),
	}
	if err != nil {
		// The warning was computed but could not be delivered. Record it: a
		// warning that reaches nobody is the failure this watcher exists to
		// prevent, and it must not vanish silently.
		details["mail_error"] = err.Error()
		log.Printf("pogod: credential-expiry warning (%s) could not be mailed: %v", tier, err)
	} else {
		log.Printf("pogod: credential-expiry warning mailed to %s (tier=%s, expires %s, %s left)",
			mailTo, tier, st.RefreshExpiry.Format(time.RFC3339), FormatRemaining(remaining))
	}
	w.emit(events.Event{EventType: "cred_expiry_warned", Agent: "pogod", Details: details})
}

// reportBlind mails the unreadable-credential notice, throttled.
func (w *Watcher) reportBlind(st Status, now time.Time) {
	w.mu.Lock()
	due := !w.blindEver || now.Sub(w.lastBlind) >= w.blindRenotify
	if due {
		w.lastBlind = now
		w.blindEver = true
		// The tier ratchet is meaningless while blind; clear it so that when the
		// credential becomes readable again the escalation restarts honestly
		// rather than assuming the tiers it could not see were delivered.
		w.mailedTier = TierNone
		w.mailedFor = time.Time{}
	}
	w.mu.Unlock()

	if !due {
		return
	}

	subject, body := BlindMail(st, now)
	err := w.mail(mailTo, mailFrom, subject, body)
	details := map[string]any{"reason": st.Reason}
	if err != nil {
		details["mail_error"] = err.Error()
	}
	log.Printf("pogod: credential expiry is UNREADABLE (%s) — advance warning is blind", st.Reason)
	w.emit(events.Event{EventType: "cred_expiry_blind", Agent: "pogod", Details: details})
}
