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

// Event type names. Named rather than inlined because two of them are a matched
// pair — one records grant HISTORY and the other records warning TIERS — and a
// query that reaches for the wrong one gets a clean, complete-looking series
// that cannot answer the question being asked (mg-2127).
const (
	// EventWarned is emitted when a lead-time tier is first reached. Its domain
	// is the warning tiers and nothing else.
	EventWarned = "cred_expiry_warned"
	// EventGrantObserved is emitted when the observed refresh-grant expiry
	// CHANGES, and once when it is first seen after startup. This is the grant
	// ledger.
	EventGrantObserved = "cred_expiry_grant_observed"
	// EventBlind is emitted when the credential exists but cannot be read.
	EventBlind = "cred_expiry_blind"
	// EventDisarmed is emitted once when there is no credential to inspect.
	EventDisarmed = "cred_expiry_disarmed"
)

// Domain notes. Every event this package writes carries one, in-band, saying
// what its silence does and does not mean.
//
// This exists because of a measured failure (mg-2127). `cred_expiry_warned`
// fires ONLY inside a lead-time tier, so grant issuance is outside its domain
// BY CONSTRUCTION — not by sampling luck. Across the whole of ~/.pogo/events.log
// on 2026-09-07 its 25 rows carried ONE grant expiry rounded two ways
// (2026-09-07T10:39:14Z x20, ...:15Z x5) and no transition at all, while the
// live grant was 2026-10-06T10:52:13Z. The series looked complete. An analyst
// reading it for grant history got silence that read exactly like a negative
// finding, and a 30-day grant lifetime this log has never measured was inferred
// from it and believed. No retention period and no busier week would have
// changed that; only a different instrument does.
const (
	// DomainWarned is stamped on every cred_expiry_warned event.
	DomainWarned = "warning tiers only — never fires at issuance; grant history is " + EventGrantObserved

	// DomainGrantObserved is stamped on every cred_expiry_grant_observed event.
	// It is the same medicine applied to the remedy: this ledger records what
	// pogod SAW, so a grant minted and replaced while pogod was stopped, blind
	// or disarmed leaves no row here either.
	DomainGrantObserved = "pogod observations only — a grant minted and replaced while pogod was down, blind or disarmed leaves no row; issuance is bracketed, never timestamped"

	// DomainBlind is stamped on every cred_expiry_blind event.
	DomainBlind = "the credential could not be read — this is not a health report and not a grant record"

	// DomainDisarmed is stamped on every cred_expiry_disarmed event.
	DomainDisarmed = "no credential on this host — no warning and no grant history will be recorded here at all"
)

// unknownBound and unboundedSpan are the in-band values for a bound that does
// not exist, written into the field rather than left out of it. An omitted
// field is the same absence-reads-as-evidence trap one level down: a reader
// scanning rows for `lifetime_at_most` would find nothing on a
// first-observation row and have no way to tell that from a field that was
// never written.
const (
	unknownBound  = "unknown"
	unboundedSpan = "unbounded"
)

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

	// --- the grant-observation ledger (mg-2127) ---------------------------
	//
	// Deliberately SEPARATE from mailedTier/mailedFor above, which are a mail
	// ratchet and are reset by reportBlind so a recovered credential escalates
	// honestly. Going blind does not un-observe what was already seen, so
	// reusing that pair here would erase the previous grant from the ledger and
	// make the next reading look like a first observation — which is precisely
	// the misreading this ledger exists to prevent.
	//
	// grantSeen is the newest refresh expiry observed; grantSeenAt is the time
	// of the most recent sample that observed it, which is the LOWER bound on
	// when any later grant was minted. grantEver separates "first observation"
	// from "transition", because on a fresh process grantSeen is the zero time
	// and every value differs from it.
	grantSeen   time.Time
	grantSeenAt time.Time
	grantEver   bool
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
				EventType: EventDisarmed,
				Agent:     "pogod",
				Details:   map[string]any{"reason": st.Reason, "domain": DomainDisarmed},
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
	obs := w.recordGrantLocked(st.RefreshExpiry, now)
	// Also clear the blind state: the credential is readable again.
	w.blindEver = false
	shouldMail := tier > w.mailedTier
	if shouldMail {
		w.mailedTier = tier
	}
	w.mu.Unlock()

	// The grant ledger is emitted BEFORE the tier check and independently of it.
	// Coupling the two would rebuild the defect: warnings are the thing that
	// only happens near expiry, and a login is the thing that only happens away
	// from it, so a grant record gated on a warning is a grant record that can
	// never fire at issuance (mg-2127).
	if obs.emit {
		w.emitGrantObserved(obs, st.RefreshExpiry, now)
	}

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
		// The domain note is not decoration. This event's `expires_at` is the
		// only grant value the log carried for its first year, and reading the
		// series as grant history is what mg-2127 was filed about.
		"domain": DomainWarned,
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
	w.emit(events.Event{EventType: EventWarned, Agent: "pogod", Details: details})
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
	details := map[string]any{"reason": st.Reason, "domain": DomainBlind}
	if err != nil {
		details["mail_error"] = err.Error()
	}
	log.Printf("pogod: credential expiry is UNREADABLE (%s) — advance warning is blind", st.Reason)
	w.emit(events.Event{EventType: EventBlind, Agent: "pogod", Details: details})
}

// grantObservation is one entry in the grant ledger: the fact that a new
// refresh-grant expiry was seen, plus the previous observation that brackets
// when it must have been minted.
type grantObservation struct {
	// emit is false when this sample re-confirmed a value already in the
	// ledger, which is every sample but a handful over a 30-day grant.
	emit bool
	// transition separates a real grant CHANGE from the first observation a
	// process makes. This distinction is the whole difficulty: on a fresh
	// pogod, grantSeen is the zero time and any real expiry differs from it, so
	// a naive "the value changed, emit" would stamp a grant transition on every
	// daemon restart. An analyst differencing consecutive rows would then be
	// measuring pogod's uptime and calling it grant lifetime — the same class
	// of error as reading warning tiers as grant history (mg-2127).
	transition bool
	// prevExpiry and prevSeenAt are the previous ledger entry. Meaningful only
	// when transition is true. prevSeenAt is the newest moment the OLD value was
	// still observed, and therefore the lower bound on when the new grant was
	// minted.
	prevExpiry time.Time
	prevSeenAt time.Time
}

// recordGrantLocked folds one present sample into the grant ledger and reports
// whether it is worth an event. Caller holds w.mu.
func (w *Watcher) recordGrantLocked(expiry, now time.Time) grantObservation {
	var obs grantObservation
	if !w.grantEver || !expiry.Equal(w.grantSeen) {
		obs = grantObservation{
			emit:       true,
			transition: w.grantEver,
			prevExpiry: w.grantSeen,
			prevSeenAt: w.grantSeenAt,
		}
		w.grantSeen = expiry
		w.grantEver = true
	}
	// Advance the confirmation time on EVERY present sample, not only on a
	// change. This field is the lower bound on the next grant's issuance, and a
	// bound that is only refreshed when something happens is a bound that widens
	// to uselessness exactly when nothing does.
	w.grantSeenAt = now
	return obs
}

// emitGrantObserved writes the ledger row.
//
// It records a BRACKET, never a timestamp, for when the grant was minted. The
// watcher cannot see a `/login`; it can only see that the old value held at one
// sample and the new value held at the next, which bounds issuance to the gap
// between them. Publishing a point estimate instead — "expiry minus 30 days" —
// is the exact inference mg-3222 made and withdrew, and a field that looked
// like a measurement is what made it believable.
func (w *Watcher) emitGrantObserved(obs grantObservation, expiry, now time.Time) {
	details := map[string]any{
		"expires_at":  expiry.Format(time.RFC3339),
		"observed_at": now.UTC().Format(time.RFC3339),
		"transition":  obs.transition,
		// issuance_before is always known: the grant existed by the time this
		// sample read it.
		"issuance_before": now.UTC().Format(time.RFC3339),
		// lifetime_at_least is what the sample alone establishes — the grant
		// still has this much life left, so it was minted with at least this
		// much.
		"lifetime_at_least": formatSpan(expiry.Sub(now)),
		"domain":            DomainGrantObserved,
	}

	if obs.transition {
		details["previous_expires_at"] = obs.prevExpiry.Format(time.RFC3339)
		details["previous_seen_at"] = obs.prevSeenAt.UTC().Format(time.RFC3339)
		details["issuance_after"] = obs.prevSeenAt.UTC().Format(time.RFC3339)
		details["issuance_window"] = formatSpan(now.Sub(obs.prevSeenAt))
		details["lifetime_at_most"] = formatSpan(expiry.Sub(obs.prevSeenAt))
		details["expiry_advance"] = formatSpan(expiry.Sub(obs.prevExpiry))
	} else {
		// A first observation brackets nothing below. Say so IN the field
		// rather than by leaving it out, so a row read on its own cannot be
		// mistaken for a measured transition.
		details["issuance_after"] = unknownBound
		details["issuance_window"] = unboundedSpan
		details["lifetime_at_most"] = unboundedSpan
	}

	if obs.transition {
		log.Printf("pogod: credential grant CHANGED — expires %s (was %s); minted between %s and %s (%s window)",
			expiry.Format(time.RFC3339), obs.prevExpiry.Format(time.RFC3339),
			obs.prevSeenAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339),
			formatSpan(now.Sub(obs.prevSeenAt)))
	} else {
		log.Printf("pogod: credential grant first observed this process — expires %s (%s left); "+
			"issuance time is NOT known, only that it predates this sample",
			expiry.Format(time.RFC3339), formatSpan(expiry.Sub(now)))
	}

	w.emit(events.Event{EventType: EventGrantObserved, Agent: "pogod", Details: details})
}

// formatSpan renders a duration for the grant ledger. It differs from
// FormatRemaining in the one way that matters here: FormatRemaining collapses
// every non-positive duration to "already lapsed", which is right for a warning
// and wrong for a ledger — an expiry that moved BACKWARD is a real observation
// and must render as a negative span rather than as prose about lapsing.
func formatSpan(d time.Duration) string {
	if d < 0 {
		return "-" + FormatRemaining(-d)
	}
	if d == 0 {
		return "0m"
	}
	return FormatRemaining(d)
}
