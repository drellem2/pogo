package credexpiry

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// The dated prediction this whole package exists to beat (mg-ed45). Fixtures
// are anchored to it rather than to time.Now so the suite does not change
// meaning as the real date passes it.
const nextOutage = "2026-08-21T21:31:50.920Z"

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad fixture time %q: %v", s, err)
	}
	return ts.UTC()
}

// blobJSON builds a credential fixture in the REAL nested shape observed on the
// live keychain: the fields sit under `claudeAiOauth`, not at the top level.
// The token fields are present and carry obvious dummy values, because the
// decode target must be proven to DROP them rather than merely not print them.
func blobJSON(expiresMS, refreshExpiresMS int64) []byte {
	return []byte(`{"claudeAiOauth":{
		"accessToken":"DUMMY-ACCESS-TOKEN-MUST-NOT-SURVIVE-DECODE",
		"refreshToken":"DUMMY-REFRESH-TOKEN-MUST-NOT-SURVIVE-DECODE",
		"expiresAt":` + strconv.FormatInt(expiresMS, 10) + `,
		"refreshTokenExpiresAt":` + strconv.FormatInt(refreshExpiresMS, 10) + `,
		"scopes":["user:file_upload","user:inference","user:mcp_servers","user:profile","user:sessions:claude_code"],
		"subscriptionType":"max",
		"rateLimitTier":"default_claude_max_20x"}}`)
}

// recorder captures mail instead of sending it.
type recorder struct {
	mails []mail
	err   error
}

type mail struct{ to, from, subject, body string }

func (r *recorder) send(to, from, subject, body string) error {
	r.mails = append(r.mails, mail{to, from, subject, body})
	return r.err
}

func (r *recorder) subjects() []string {
	out := make([]string, len(r.mails))
	for i, m := range r.mails {
		out[i] = m.subject
	}
	return out
}

// fixedReader returns a Status regardless of context.
func fixedReader(st Status) Reader {
	return func(context.Context) Status { return st }
}

func presentAt(expiry time.Time) Status {
	return Status{
		State:            StatePresent,
		RefreshExpiry:    expiry,
		AccessExpiry:     expiry.Add(-29 * 24 * time.Hour),
		SubscriptionType: "max",
		RateLimitTier:    "default_claude_max_20x",
		ScopeCount:       5,
	}
}

func newTestWatcher(r Reader, rec *recorder) *Watcher {
	return New(Options{
		Enabled:  true,
		Read:     r,
		Mail:     rec.send,
		Emit:     func(events.Event) {},
		Interval: time.Minute,
	})
}

// --- The self-check the ticket demands -------------------------------------
//
// A warning that has never been observed to fire is indistinguishable from one
// that cannot. These three tests are that observation: it FIRES on an imminent
// expiry, it FIRES on one already in the past, and it stays SILENT on a healthy
// credential.

// TestFiresOnImminentExpiry is the load-bearing one. The warner must speak
// while there is still time to act.
func TestFiresOnImminentExpiry(t *testing.T) {
	expiry := mustTime(t, nextOutage)

	cases := []struct {
		name      string
		now       time.Time
		wantTier  Tier
		wantInSub string
	}{
		{"one week out", expiry.Add(-7 * 24 * time.Hour), TierWeek, "7d 0h"},
		{"three days out", expiry.Add(-71 * time.Hour), TierThreeDay, "2d 23h"},
		{"one day out", expiry.Add(-23 * time.Hour), TierDay, "23h 0m"},
		{"two hours out", expiry.Add(-90 * time.Minute), TierFinal, "1h 30m"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			w := newTestWatcher(fixedReader(presentAt(expiry)), rec)

			w.Check(context.Background(), tc.now)

			if len(rec.mails) != 1 {
				t.Fatalf("expected exactly one warning, got %d: %v", len(rec.mails), rec.subjects())
			}
			got := rec.mails[0]
			if got.to != mailTo {
				t.Errorf("warning went to %q, want %q — only a human can run /login", got.to, mailTo)
			}
			if TierFor(expiry.Sub(tc.now)) != tc.wantTier {
				t.Errorf("tier = %v, want %v", TierFor(expiry.Sub(tc.now)), tc.wantTier)
			}
			if !strings.Contains(got.subject, tc.wantInSub) {
				t.Errorf("subject %q does not carry remaining time %q", got.subject, tc.wantInSub)
			}
			// The remedy must be in the body. A warning that does not say what
			// to do is a warning that gets deferred.
			if !strings.Contains(got.body, "/login") {
				t.Errorf("warning body never mentions /login:\n%s", got.body)
			}
			if !strings.Contains(got.body, expiry.Format(time.RFC3339)) {
				t.Errorf("warning body omits the expiry date:\n%s", got.body)
			}
		})
	}
}

// TestFiresOnAlreadyLapsedExpiry covers the fixture with an expiry in the past.
// The grant is gone; the fleet is running out its last access token right now.
func TestFiresOnAlreadyLapsedExpiry(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	now := expiry.Add(30 * time.Minute) // half an hour past the lapse

	rec := &recorder{}
	w := newTestWatcher(fixedReader(presentAt(expiry)), rec)

	w.Check(context.Background(), now)

	if len(rec.mails) != 1 {
		t.Fatalf("a lapsed grant produced %d mails, want 1: %v", len(rec.mails), rec.subjects())
	}
	got := rec.mails[0]
	if !strings.Contains(got.subject, "LAPSED") {
		t.Errorf("lapsed subject should be unmistakable, got %q", got.subject)
	}
	if !strings.Contains(got.body, "no longer a prediction") {
		t.Errorf("lapsed body should say the prediction has become fact:\n%s", got.body)
	}
	// The death window: lapse + the final 8h access token.
	wantDeath := expiry.Add(AccessTokenLife).Format(time.RFC3339)
	if !strings.Contains(got.body, wantDeath) {
		t.Errorf("lapsed body omits the death deadline %s:\n%s", wantDeath, got.body)
	}
}

// TestSilentOnHealthyCredential is the other half of the demonstration. A
// warner that fires constantly gets muted, and a muted warner is no warner.
func TestSilentOnHealthyCredential(t *testing.T) {
	expiry := mustTime(t, nextOutage)

	// A month out, a fortnight out, and a hair over the first lead time. All
	// healthy: nothing should be said.
	for _, now := range []time.Time{
		expiry.Add(-30 * 24 * time.Hour),
		expiry.Add(-14 * 24 * time.Hour),
		expiry.Add(-LeadWeek - time.Minute),
	} {
		rec := &recorder{}
		w := newTestWatcher(fixedReader(presentAt(expiry)), rec)
		w.Check(context.Background(), now)
		if len(rec.mails) != 0 {
			t.Errorf("healthy credential at %s produced mail: %v", now.Format(time.RFC3339), rec.subjects())
		}
	}
}

// --- Escalation shape -------------------------------------------------------

// TestEscalatesOncePerTier proves the ratchet: sampling every 15 minutes across
// a 30-day grant must produce five mails, not three thousand.
func TestEscalatesOncePerTier(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	rec := &recorder{}
	w := New(Options{
		Enabled:  true,
		Read:     fixedReader(presentAt(expiry)),
		Mail:     rec.send,
		Emit:     func(events.Event) {},
		Interval: 15 * time.Minute,
	})

	// Walk from 10 days out to 1 hour past the lapse, every 15 minutes: the
	// real sampling cadence over the window that matters.
	for now := expiry.Add(-10 * 24 * time.Hour); now.Before(expiry.Add(time.Hour)); now = now.Add(15 * time.Minute) {
		w.Check(context.Background(), now)
	}

	if len(rec.mails) != 5 {
		t.Fatalf("expected 5 mails (7d, 72h, 24h, 2h, lapsed), got %d:\n%s",
			len(rec.mails), strings.Join(rec.subjects(), "\n"))
	}
	// And they must arrive in escalating order, each deeper than the last.
	// The 15-minute walk lands exactly on each lead time, so each subject
	// reports its tier boundary precisely.
	wantOrder := []string{"7d 0h", "3d 0h", "1d 0h", "2h 0m", "LAPSED"}
	for i, want := range wantOrder {
		if !strings.Contains(rec.mails[i].subject, want) {
			t.Errorf("mail %d subject %q does not match expected tier marker %q", i, rec.mails[i].subject, want)
		}
	}
}

// TestThrottleSuppressesSubIntervalSamples proves the coarse throttle: pogod's
// ~30s heartbeat must not turn into a 30s credential read.
func TestThrottleSuppressesSubIntervalSamples(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	reads := 0
	rec := &recorder{}
	w := New(Options{
		Enabled:  true,
		Read:     func(context.Context) Status { reads++; return presentAt(expiry) },
		Mail:     rec.send,
		Emit:     func(events.Event) {},
		Interval: 15 * time.Minute,
	})

	base := expiry.Add(-20 * 24 * time.Hour)
	// 60 heartbeat ticks at 30s = 30 minutes = 2 intervals.
	for i := 0; i < 60; i++ {
		w.Check(context.Background(), base.Add(time.Duration(i)*30*time.Second))
	}
	if reads != 2 {
		t.Errorf("sampled the keychain %d times over 30 min at a 15 min interval, want 2", reads)
	}
}

// TestLoginResetsEscalation proves a `/login` starts a fresh cycle. Without
// this the ratchet would stay latched at `lapsed` forever and the NEXT outage
// would get no warning at all — the warner would work exactly once.
func TestLoginResetsEscalation(t *testing.T) {
	oldExpiry := mustTime(t, nextOutage)
	rec := &recorder{}
	st := presentAt(oldExpiry)
	w := New(Options{
		Enabled:  true,
		Read:     func(context.Context) Status { return st },
		Mail:     rec.send,
		Emit:     func(events.Event) {},
		Interval: time.Minute,
	})

	// Escalate all the way to lapsed on the old grant.
	w.Check(context.Background(), oldExpiry.Add(-6*24*time.Hour))
	w.Check(context.Background(), oldExpiry.Add(time.Hour))
	if len(rec.mails) == 0 {
		t.Fatal("no warning on the first cycle")
	}
	before := len(rec.mails)

	// A human runs /login: a new 30-day grant.
	newExpiry := oldExpiry.Add(30 * 24 * time.Hour)
	st = presentAt(newExpiry)

	// Healthy again — silence.
	w.Check(context.Background(), newExpiry.Add(-20*24*time.Hour))
	if len(rec.mails) != before {
		t.Errorf("a freshly re-minted grant produced mail: %v", rec.subjects())
	}

	// ...and the new cycle escalates on its own schedule.
	w.Check(context.Background(), newExpiry.Add(-6*24*time.Hour))
	if len(rec.mails) != before+1 {
		t.Errorf("the second cycle did not warn — the warner would work exactly once. got %v", rec.subjects())
	}
}

// --- The absence-as-evidence trap ------------------------------------------

// TestAbsentCredentialNeverMailsAndNeverClaimsHealth covers a sandbox, a Linux
// box, or a machine with no such keychain item. It must not mail, and it must
// not be recorded as healthy.
func TestAbsentCredentialNeverMailsAndNeverClaimsHealth(t *testing.T) {
	rec := &recorder{}
	var evs []events.Event
	w := New(Options{
		Enabled:  true,
		Read:     fixedReader(Status{State: StateAbsent, Reason: ReasonItemNotFound}),
		Mail:     rec.send,
		Emit:     func(e events.Event) { evs = append(evs, e) },
		Interval: time.Minute,
	})

	base := mustTime(t, nextOutage).Add(-20 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		w.Check(context.Background(), base.Add(time.Duration(i)*time.Hour))
	}

	if len(rec.mails) != 0 {
		t.Errorf("an absent credential mailed %v — sandboxes and non-macOS hosts must stay quiet", rec.subjects())
	}
	// But the silence must be DECLARED exactly once, not merely assumed.
	var disarmed int
	for _, e := range evs {
		if e.EventType == "cred_expiry_disarmed" {
			disarmed++
		}
	}
	if disarmed != 1 {
		t.Errorf("emitted %d cred_expiry_disarmed events over 5 samples, want exactly 1", disarmed)
	}
	// Critically: no event anywhere may assert the credential is fine.
	for _, e := range evs {
		if e.EventType == "cred_expiry_warned" {
			t.Errorf("an absent credential produced a warning event: %+v", e)
		}
	}
}

// TestUnreadableCredentialMailsRatherThanPassingSilently is the single most
// important negative case. An item that exists but cannot be read is the case a
// naive implementation reports as healthy — the absence-as-evidence error this
// family of bugs is made of.
func TestUnreadableCredentialMailsRatherThanPassingSilently(t *testing.T) {
	for _, reason := range []string{ReasonFieldMissing, ReasonDecodeFailed, ReasonReadTimedOut, ReasonReadFailed} {
		rec := &recorder{}
		w := newTestWatcher(fixedReader(Status{State: StateUnreadable, Reason: reason}), rec)

		w.Check(context.Background(), mustTime(t, nextOutage).Add(-20*24*time.Hour))

		if len(rec.mails) != 1 {
			t.Fatalf("unreadable credential (%s) produced %d mails, want 1 — silence here IS the bug", reason, len(rec.mails))
		}
		body := rec.mails[0].body
		if !strings.Contains(body, "NOT A REPORT THAT THE CREDENTIAL IS FINE") {
			t.Errorf("blind mail must refuse to imply health:\n%s", body)
		}
		if !strings.Contains(body, reason) {
			t.Errorf("blind mail omits the reason %q:\n%s", reason, body)
		}
	}
}

// TestBlindMailIsThrottled keeps a permanently-moved schema from burying the
// inbox while still not letting it be forgotten.
func TestBlindMailIsThrottled(t *testing.T) {
	rec := &recorder{}
	w := New(Options{
		Enabled:       true,
		Read:          fixedReader(Status{State: StateUnreadable, Reason: ReasonFieldMissing}),
		Mail:          rec.send,
		Emit:          func(events.Event) {},
		Interval:      15 * time.Minute,
		BlindRenotify: 24 * time.Hour,
	})

	base := mustTime(t, nextOutage).Add(-20 * 24 * time.Hour)
	// Three days of 15-minute samples.
	for i := 0; i < 3*24*4; i++ {
		w.Check(context.Background(), base.Add(time.Duration(i)*15*time.Minute))
	}
	if len(rec.mails) != 3 {
		t.Errorf("blind over 3 days at a 24h renotify produced %d mails, want 3", len(rec.mails))
	}
}

// TestRecoveryFromBlindRestartsEscalation: if the warner was blind through the
// 7d and 72h tiers, it must not assume those mails were delivered once the
// credential becomes readable again.
func TestRecoveryFromBlindRestartsEscalation(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	rec := &recorder{}
	st := Status{State: StateUnreadable, Reason: ReasonFieldMissing}
	w := New(Options{
		Enabled:  true,
		Read:     func(context.Context) Status { return st },
		Mail:     rec.send,
		Emit:     func(events.Event) {},
		Interval: time.Minute,
	})

	// Blind through the early tiers.
	w.Check(context.Background(), expiry.Add(-8*24*time.Hour))
	w.Check(context.Background(), expiry.Add(-4*24*time.Hour))
	blindMails := len(rec.mails)

	// Credential becomes readable with only 12 hours left.
	st = presentAt(expiry)
	w.Check(context.Background(), expiry.Add(-12*time.Hour))

	if len(rec.mails) != blindMails+1 {
		t.Fatalf("recovering from blind did not warn; got %v", rec.subjects())
	}
	last := rec.mails[len(rec.mails)-1]
	if !strings.Contains(last.subject, "12h") {
		t.Errorf("expected a 24h-tier warning after recovery, got %q", last.subject)
	}
}

// TestDisabledWatcherIsInert.
func TestDisabledWatcherIsInert(t *testing.T) {
	rec := &recorder{}
	w := New(Options{
		Enabled: false,
		Read:    fixedReader(presentAt(mustTime(t, nextOutage))),
		Mail:    rec.send,
		Emit:    func(events.Event) {},
	})
	w.Check(context.Background(), mustTime(t, nextOutage).Add(-time.Hour))
	if len(rec.mails) != 0 {
		t.Errorf("a disabled watcher mailed: %v", rec.subjects())
	}
}

// --- Decoding, and the never-read-a-token rule ------------------------------

// TestDecodeReadsTheNestedSchema pins the shape actually observed on the live
// keychain: fields under `claudeAiOauth`, expiries in epoch MILLIseconds.
func TestDecodeReadsTheNestedSchema(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	access := mustTime(t, "2026-07-23T06:31:32.920Z")

	st := decodeBlob(blobJSON(access.UnixMilli(), expiry.UnixMilli()))

	if st.State != StatePresent {
		t.Fatalf("state = %v (%s), want present", st.State, st.Reason)
	}
	if !st.RefreshExpiry.Equal(expiry) {
		t.Errorf("refreshTokenExpiresAt = %s, want %s", st.RefreshExpiry, expiry)
	}
	if !st.AccessExpiry.Equal(access) {
		t.Errorf("expiresAt = %s, want %s", st.AccessExpiry, access)
	}
	if st.SubscriptionType != "max" || st.RateLimitTier != "default_claude_max_20x" || st.ScopeCount != 5 {
		t.Errorf("descriptors wrong: %+v", st)
	}
}

// TestFlatSchemaIsUnreadableNotHealthy is the regression that matters most for
// schema drift. mg-ed45's report lists the fields flat; a parse aimed at the
// flat shape finds nothing. That MUST surface as unreadable — if it read as
// healthy, the warner would sit silent through the very outage it prevents.
func TestFlatSchemaIsUnreadableNotHealthy(t *testing.T) {
	flat := []byte(`{"expiresAt":1784788292920,"refreshTokenExpiresAt":1787347910920,"subscriptionType":"max"}`)

	st := decodeBlob(flat)

	if st.State == StatePresent {
		t.Fatal("a flat-schema blob decoded as PRESENT — a moved schema would read as healthy")
	}
	if st.State != StateUnreadable {
		t.Errorf("state = %v, want unreadable (NOT absent — the item exists)", st.State)
	}
	if st.Reason != ReasonFieldMissing {
		t.Errorf("reason = %q, want %q", st.Reason, ReasonFieldMissing)
	}
}

func TestMalformedAndEmptyBlobsAreUnreadable(t *testing.T) {
	for name, raw := range map[string][]byte{
		"not json":         []byte(`{{{`),
		"empty":            {},
		"empty object":     []byte(`{}`),
		"null oauth":       []byte(`{"claudeAiOauth":null}`),
		"zero expiry":      []byte(`{"claudeAiOauth":{"refreshTokenExpiresAt":0}}`),
		"negative expiry":  []byte(`{"claudeAiOauth":{"refreshTokenExpiresAt":-1}}`),
		"string not int64": []byte(`{"claudeAiOauth":{"refreshTokenExpiresAt":"soon"}}`),
	} {
		st := decodeBlob(raw)
		if st.State != StateUnreadable {
			t.Errorf("%s: state = %v (%s), want unreadable", name, st.State, st.Reason)
		}
	}
}

// TestStatusCannotCarryTokenMaterial is the enforcement of the standing rule.
// The decode target has no token field, so a blob whose tokens are unmistakable
// strings must yield a Status in which those strings appear nowhere at all —
// not in a value, not in a Reason, not in a mail body.
func TestStatusCannotCarryTokenMaterial(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	raw := blobJSON(expiry.Add(-29*24*time.Hour).UnixMilli(), expiry.UnixMilli())

	st := decodeBlob(raw)

	// Every rendered surface this package can produce.
	warnSubj, warnBody := WarningMail(TierDay, st, expiry.Add(-20*time.Hour))
	blindSubj, blindBody := BlindMail(Status{State: StateUnreadable, Reason: ReasonDecodeFailed}, expiry)
	surfaces := []string{
		st.Reason, st.SubscriptionType, st.RateLimitTier,
		warnSubj, warnBody, blindSubj, blindBody,
	}

	// Both whole token VALUES and any prefix of them: a partial token prefix is
	// enough to identify a leaked credential, so prefixes are treated as leaks
	// too. Field NAMES (`refreshTokenExpiresAt`, `subscriptionType`) are not
	// secrets — they are the non-secret descriptors the warning is allowed to
	// name, and the mail body deliberately cites them so a human can reproduce
	// the read by hand.
	forbidden := []string{
		"DUMMY-ACCESS-TOKEN-MUST-NOT-SURVIVE-DECODE",
		"DUMMY-REFRESH-TOKEN-MUST-NOT-SURVIVE-DECODE",
		"DUMMY-",
	}
	for _, s := range surfaces {
		for _, bad := range forbidden {
			if strings.Contains(s, bad) {
				t.Errorf("token material %q leaked into an output surface:\n%s", bad, s)
			}
		}
	}
}

// TestDecodeDoesNotRetainTheBlob proves the zeroing, which is what keeps
// credential bytes from lingering in a buffer after the two integers are out.
func TestZeroClearsTheBuffer(t *testing.T) {
	raw := blobJSON(1, 2)
	zero(raw)
	for i, b := range raw {
		if b != 0 {
			t.Fatalf("byte %d not zeroed", i)
		}
	}
}

// --- Tier boundaries --------------------------------------------------------

func TestTierBoundaries(t *testing.T) {
	cases := []struct {
		remaining time.Duration
		want      Tier
	}{
		{31 * 24 * time.Hour, TierNone},
		{LeadWeek + time.Second, TierNone},
		{LeadWeek, TierWeek},
		{LeadThreeDay + time.Second, TierWeek},
		{LeadThreeDay, TierThreeDay},
		{LeadDay + time.Second, TierThreeDay},
		{LeadDay, TierDay},
		{LeadFinal + time.Second, TierDay},
		{LeadFinal, TierFinal},
		{time.Second, TierFinal},
		{0, TierLapsed},
		{-time.Hour, TierLapsed},
		{-30 * 24 * time.Hour, TierLapsed},
	}
	for _, tc := range cases {
		if got := TierFor(tc.remaining); got != tc.want {
			t.Errorf("TierFor(%s) = %v, want %v", tc.remaining, got, tc.want)
		}
	}
}

// TestAccessExpiryNeverDrivesAWarning: the 8-hour access-token expiry is
// routinely in the past on a HEALTHY machine (observed live 2026-07-22, seven
// hours stale while the fleet was fine). Warning on it would generate constant
// false alarms and get the whole mechanism muted.
func TestAccessExpiryNeverDrivesAWarning(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	st := presentAt(expiry)
	// Access token expired a week ago; refresh grant is a month out.
	st.AccessExpiry = expiry.Add(-37 * 24 * time.Hour)

	rec := &recorder{}
	w := newTestWatcher(fixedReader(st), rec)
	w.Check(context.Background(), expiry.Add(-30*24*time.Hour))

	if len(rec.mails) != 0 {
		t.Errorf("a stale access token triggered a warning: %v — only refreshTokenExpiresAt is predictive", rec.subjects())
	}
}

func TestFormatRemaining(t *testing.T) {
	cases := map[time.Duration]string{
		7 * 24 * time.Hour:             "7d 0h",
		71*time.Hour + 30*time.Minute:  "2d 23h",
		23*time.Hour + 59*time.Minute:  "23h 59m",
		90 * time.Minute:               "1h 30m",
		45 * time.Minute:               "45m",
		0:                              "already lapsed",
		-time.Hour:                     "already lapsed",
		25*time.Hour + 30*time.Minute:  "1d 1h",
		time.Duration(0) - time.Second: "already lapsed",
	}
	for d, want := range cases {
		if got := FormatRemaining(d); got != want {
			t.Errorf("FormatRemaining(%s) = %q, want %q", d, got, want)
		}
	}
}

// TestMailDeliveryFailureIsNotSilent: a warning that reaches nobody is the
// failure this watcher exists to prevent, so it must still be recorded.
func TestMailDeliveryFailureIsRecorded(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	rec := &recorder{err: errFakeMail{}}
	var evs []events.Event
	w := New(Options{
		Enabled:  true,
		Read:     fixedReader(presentAt(expiry)),
		Mail:     rec.send,
		Emit:     func(e events.Event) { evs = append(evs, e) },
		Interval: time.Minute,
	})

	w.Check(context.Background(), expiry.Add(-3*24*time.Hour))

	var found bool
	for _, e := range evs {
		if e.EventType == "cred_expiry_warned" {
			if _, ok := e.Details["mail_error"]; !ok {
				t.Error("a failed warning mail emitted no mail_error detail")
			}
			found = true
		}
	}
	if !found {
		t.Error("no cred_expiry_warned event emitted for a failed mail")
	}
}

type errFakeMail struct{}

func (errFakeMail) Error() string { return "mail transport down" }

// TestSystemReaderDegradesOffDarwin: on a non-macOS host the reader must report
// ABSENT with a stated reason, never present and never a crash.
func TestSystemReaderNeverPanicsAndNeverClaimsHealthSpuriously(t *testing.T) {
	st := SystemReader(context.Background())
	switch st.State {
	case StatePresent:
		// Only valid outcome is a real, sane expiry.
		if st.RefreshExpiry.IsZero() {
			t.Error("present status with a zero expiry")
		}
	case StateAbsent, StateUnreadable:
		if st.Reason == "" {
			t.Error("a non-present status must always carry a stated reason — silence is the bug")
		}
	}
}
