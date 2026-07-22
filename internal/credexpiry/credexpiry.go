// Package credexpiry PREDICTS the next fleet-wide auth outage and warns a
// human early enough to prevent it, rather than detecting it after the fleet is
// already dead.
//
// # Why prediction is possible here and almost nowhere else
//
// mg-ed45 (docs/investigations/credential-expiry-mechanism-2026-07-23.md)
// established the mechanism: the OAuth refresh grant has a hard 30-day life
// from the moment it is minted and is NOT extended by use. When it lapses the
// harness can no longer mint access tokens; the fleet coasts on its final
// 8-hour access token and dies when that expires. The expiry is a plain integer
// on local disk. No network call, no inference, no new infrastructure.
//
// mg-ed45's load-bearing distinction, which this package implements and must
// not be read as weakening: a PERIODIC fault can be predicted; a CHRONIC one
// can only be detected. Auth is periodic — exactly two outages ever, 30 days
// apart, each producing exactly one error string (498 INVALID_CRED in June, 914
// LOGIN_EXPIRED in July, a perfectly clean split). Rate-limit (2818),
// weekly-limit (885) and spend-limit (280) are genuinely chronic and spread
// across history.
//
// So this package COMPLEMENTS mg-8cdb's reactive detector and does not replace
// it. Prediction covers exactly one member of the class, and even for auth it
// covers only the scheduled lapse — an early REVOCATION is invisible here and
// reactive-only. Ship both.
//
// # What it reads, and what it must never read
//
// The credential is a macOS Keychain generic-password item, `Claude
// Code-credentials`, holding JSON. This package extracts ONLY two integers and
// a few non-secret descriptors. It never reads, echoes, logs, mails or commits
// a token value — not even a prefix (standing rule, ~/.claude/CLAUDE.md;
// mg-ed45 complied and this follows its method). Concretely:
//
//   - the decode target struct has no field for either token, so the values are
//     dropped by encoding/json rather than being held and trusted-not-to-leak;
//   - the raw blob is zeroed immediately after decode;
//   - no error returned from this package carries command output or decoder
//     text, both of which can quote input. Reasons come from a fixed vocabulary.
//
// # refreshTokenExpiresAt is the predictive field; expiresAt is NOT
//
// Only the refresh-grant expiry drives a warning. The 8-hour access-token
// `expiresAt` is frequently in the past on a perfectly healthy machine — the
// harness re-mints on demand and does not always rewrite the stored blob (this
// was observed live on 2026-07-22, `expiresAt` 7h stale while the fleet was
// fine). Warning on it would produce constant false alarms and get the whole
// mechanism muted before the run that matters. It is carried for context only.
//
// # The absence-as-evidence trap
//
// The Keychain item name and JSON schema are harness-internal, not a pogo
// contract. This package probes, uses when present, and degrades when absent —
// but a credential it cannot read must NEVER read as "expiry is fine". That is
// the exact error this whole family of bugs is made of. So the failure modes
// are split three ways and reported differently:
//
//   - ABSENT (no item, not macOS, no `security` binary): the warner is not
//     armed. Silent in mail, but LOUD in the log at startup, so the silence is
//     declared rather than assumed. A sandbox or a Linux box must not mail.
//   - UNREADABLE (item present, extraction failed, timed out, or the schema
//     moved): this is the dangerous one — something that used to work stopped,
//     and the warning is BLIND. It mails, throttled, saying so.
//   - PRESENT: the ordinary path.
//
// The schema-drift case is real and not hypothetical. The fields live nested
// under `claudeAiOauth`, not at the top level; mg-ed45's report lists them flat.
// A flat parse finds nothing — and if "field missing" were treated as "fine",
// the warner would sit silent through the very outage it exists to prevent.
// That is why a present item with a missing/zero refreshTokenExpiresAt is
// UNREADABLE, never ABSENT and never healthy.
//
// # It warns; it never acts
//
// Only a human can run `/login`. This package never attempts to refresh or
// re-mint a credential, and holds no seam through which it could.
package credexpiry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// KeychainService is the generic-password service name the harness stores the
// credential under. HARNESS-INTERNAL: this is an observed value, not a contract
// pogo is owed. If it changes, the read fails ABSENT and the warner disarms
// loudly rather than reporting health it cannot see.
const KeychainService = "Claude Code-credentials"

// AccessTokenLife is the measured life of one access token (mg-ed45: exactly
// 8h). It is used ONLY to describe the death WINDOW in the warning prose —
// after the refresh grant lapses at T, the fleet coasts on its last access
// token and dies by T+8h. It is never used to compute an expiry: mg-ed45's
// point 5 is explicit that the warner must read the field, never derive it.
const AccessTokenLife = 8 * time.Hour

// readTimeout bounds the `security` call. A keychain read can block
// indefinitely on an authorization prompt, and a warner wedged forever on a
// modal is indistinguishable from one that decided everything is fine. A
// timeout therefore resolves to UNREADABLE (blind), never to healthy.
const readTimeout = 10 * time.Second

// State is what could be established about the credential. The three values
// exist precisely so that "could not read" is never collapsed into "fine".
type State int

const (
	// StateAbsent means there is no credential to inspect here: not macOS, no
	// `security` binary, or no such keychain item. The warner disarms. This is
	// NOT a health claim — it is a declaration that no claim can be made.
	StateAbsent State = iota
	// StateUnreadable means the item EXISTS but the expiry could not be
	// extracted: a decode failure, a timeout, or a schema that moved. The
	// warning is blind, which is itself news worth mailing.
	StateUnreadable
	// StatePresent means refreshTokenExpiresAt was read successfully.
	StatePresent
)

func (s State) String() string {
	switch s {
	case StateAbsent:
		return "absent"
	case StateUnreadable:
		return "unreadable"
	case StatePresent:
		return "present"
	}
	return "unknown"
}

// Status is everything this package will say about the credential. It carries
// no token material and no field capable of holding any.
type Status struct {
	State State
	// RefreshExpiry is when the OAuth refresh grant lapses — the predictive
	// field, and the only one that drives a warning. Zero unless StatePresent.
	RefreshExpiry time.Time
	// AccessExpiry is the stored 8-hour access-token expiry. INFORMATIONAL
	// ONLY: it is routinely in the past on a healthy machine (see package doc).
	// Never compared against a threshold.
	AccessExpiry time.Time
	// SubscriptionType, RateLimitTier and ScopeCount are non-secret descriptors,
	// carried so the warning can identify WHICH credential it means without
	// naming any part of a token.
	SubscriptionType string
	RateLimitTier    string
	ScopeCount       int
	// Reason explains a non-present State in fixed, sanitized language. It is
	// built only from constants in this package — never from command output or
	// decoder text, both of which can quote the input they failed on.
	Reason string
}

// credentialBlob is the decode target. It deliberately declares NO field for
// accessToken or refreshToken: the token values are discarded by encoding/json
// during decode rather than being held in a struct and trusted not to leak.
// This is the enforcement of the never-read-a-token rule, not a comment about
// it.
type credentialBlob struct {
	OAuth struct {
		ExpiresAtMS             int64    `json:"expiresAt"`
		RefreshTokenExpiresAtMS int64    `json:"refreshTokenExpiresAt"`
		SubscriptionType        string   `json:"subscriptionType"`
		RateLimitTier           string   `json:"rateLimitTier"`
		Scopes                  []string `json:"scopes"`
	} `json:"claudeAiOauth"`
}

// Fixed reason vocabulary. Every non-present Status draws its Reason from
// exactly one of these. Nothing derived from the credential bytes, the
// decoder, or the `security` process's output is ever interpolated.
const (
	ReasonNotDarwin      = "not macOS — the credential lives in the macOS Keychain"
	ReasonNoSecurityBin  = "the `security` binary is not on PATH"
	ReasonItemNotFound   = "no `" + KeychainService + "` keychain item on this machine"
	ReasonReadTimedOut   = "the keychain read timed out (likely an authorization prompt); expiry is UNKNOWN, not fine"
	ReasonReadFailed     = "the keychain item exists but could not be read"
	ReasonDecodeFailed   = "the keychain item was read but is not the JSON shape this check understands"
	ReasonFieldMissing   = "the credential decoded but carries no `claudeAiOauth.refreshTokenExpiresAt` — the harness schema has moved"
	ReasonNotImplemented = "no credential reader is wired for this platform"
)

// errItemNotFound distinguishes `security`'s not-found exit (44) from every
// other failure, because the two must produce different States: not-found
// disarms the warner, anything else makes it blind and mails.
var errItemNotFound = errors.New("keychain item not found")

// Reader obtains a Status. Production uses SystemReader; tests inject fixtures.
// It is the ONLY seam through which this package touches the credential, and it
// is read-only by construction — there is no writer counterpart, because only a
// human can run `/login`.
type Reader func(ctx context.Context) Status

// SystemReader reads the live macOS Keychain item. It never returns token
// material and never lets the raw blob outlive the decode.
func SystemReader(ctx context.Context) Status {
	if runtime.GOOS != "darwin" {
		return Status{State: StateAbsent, Reason: ReasonNotDarwin}
	}
	if _, err := exec.LookPath("security"); err != nil {
		return Status{State: StateAbsent, Reason: ReasonNoSecurityBin}
	}

	raw, err := readKeychainBlob(ctx)
	if err != nil {
		switch {
		case errors.Is(err, errItemNotFound):
			// No item: nothing to warn about, and crucially nothing to CLAIM.
			return Status{State: StateAbsent, Reason: ReasonItemNotFound}
		case errors.Is(err, context.DeadlineExceeded):
			return Status{State: StateUnreadable, Reason: ReasonReadTimedOut}
		default:
			// Note what is NOT here: err.Error(). The `security` process's
			// stderr can echo the material it was asked for.
			return Status{State: StateUnreadable, Reason: ReasonReadFailed}
		}
	}

	st := decodeBlob(raw)
	// Zero the blob the moment the two integers are out of it. The struct never
	// held a token; this makes sure the byte slice does not either.
	zero(raw)
	return st
}

// readKeychainBlob shells out to `security` under a hard timeout. The returned
// bytes are the caller's to zero.
func readKeychainBlob(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	// -w prints the secret blob to stdout. This is the one call in pogo that
	// handles credential bytes, which is why the decode target above cannot
	// represent a token and why the slice is zeroed immediately after.
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", KeychainService, "-w")
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// 44 is SecKeychain's errSecItemNotFound as surfaced by security(1).
			if ee.ExitCode() == 44 {
				return nil, errItemNotFound
			}
			// Discard ee.Stderr deliberately — never propagate process output.
			return nil, errors.New("security exited non-zero")
		}
		return nil, errors.New("security could not be run")
	}
	return out, nil
}

// decodeBlob extracts the two integers and the non-secret descriptors. It is
// separated from the process call so tests can drive every schema-drift case
// without a keychain.
func decodeBlob(raw []byte) Status {
	var blob credentialBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		// The decoder error is dropped, not wrapped: json errors can quote the
		// offending input, and the offending input here is a credential.
		return Status{State: StateUnreadable, Reason: ReasonDecodeFailed}
	}

	// A present item whose predictive field is missing or zero is UNREADABLE,
	// never absent and never healthy. This is the schema-drift case: the fields
	// are nested under `claudeAiOauth`, and a parse aimed at the wrong shape
	// lands here rather than silently reporting health.
	if blob.OAuth.RefreshTokenExpiresAtMS <= 0 {
		return Status{State: StateUnreadable, Reason: ReasonFieldMissing}
	}

	return Status{
		State:            StatePresent,
		RefreshExpiry:    msToTime(blob.OAuth.RefreshTokenExpiresAtMS),
		AccessExpiry:     msToTime(blob.OAuth.ExpiresAtMS),
		SubscriptionType: blob.OAuth.SubscriptionType,
		RateLimitTier:    blob.OAuth.RateLimitTier,
		ScopeCount:       len(blob.OAuth.Scopes),
	}
}

func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Tier is how urgent the warning is. Tiers only ever deepen, and each one mails
// at most once, so a 15-minute sampling interval produces five mails over a
// 30-day grant rather than three thousand.
type Tier int

const (
	// TierNone is more than LeadWeek out: nothing to say.
	TierNone Tier = iota
	TierWeek
	TierThreeDay
	TierDay
	TierFinal
	// TierLapsed is at or past the expiry: the grant is gone and the fleet is
	// running out its last access token right now.
	TierLapsed
)

// Lead times. These are deliberately generous, and the asymmetry is the whole
// argument: the failure costs roughly 24 hours of destroyed fleet output (both
// observed outages ran ~23-37h and neither was noticed until the fleet was
// already dead for a day), while the remedy is a human typing `/login` and
// takes seconds. Warning early and repeatedly costs a handful of mails over a
// month. Warning late costs a day of work. At those stakes the trade is not
// close, so the schedule errs hard toward early.
//
// LeadWeek exists on top of mg-ed45's suggested 72h/24h/2h because a human can
// be away for a long weekend, and a warning that first speaks inside a trip is
// a warning that arrives after the decision window closed. Seven days survives
// a holiday.
const (
	LeadWeek     = 7 * 24 * time.Hour
	LeadThreeDay = 72 * time.Hour
	LeadDay      = 24 * time.Hour
	LeadFinal    = 2 * time.Hour
)

// TierFor maps remaining grant life to a tier.
func TierFor(remaining time.Duration) Tier {
	switch {
	case remaining <= 0:
		return TierLapsed
	case remaining <= LeadFinal:
		return TierFinal
	case remaining <= LeadDay:
		return TierDay
	case remaining <= LeadThreeDay:
		return TierThreeDay
	case remaining <= LeadWeek:
		return TierWeek
	default:
		return TierNone
	}
}

func (t Tier) String() string {
	switch t {
	case TierNone:
		return "none"
	case TierWeek:
		return "7d"
	case TierThreeDay:
		return "72h"
	case TierDay:
		return "24h"
	case TierFinal:
		return "2h"
	case TierLapsed:
		return "lapsed"
	}
	return "unknown"
}

// FormatRemaining renders a duration the way the warning prose wants it.
func FormatRemaining(d time.Duration) string {
	if d <= 0 {
		return "already lapsed"
	}
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// WarningMail builds the escalating notice for a tier. Subject carries the tier
// and the date so a glance at the inbox is enough; body carries the remedy,
// because a warning that does not say what to do is a warning that gets
// deferred.
func WarningMail(t Tier, st Status, now time.Time) (subject, body string) {
	remaining := st.RefreshExpiry.Sub(now)
	deadline := st.RefreshExpiry.Format(time.RFC3339)
	lastGasp := st.RefreshExpiry.Add(AccessTokenLife).Format(time.RFC3339)

	var b strings.Builder

	if t == TierLapsed {
		subject = fmt.Sprintf("FLEET AUTH GRANT HAS LAPSED (%s) — run /login now", deadline)
		fmt.Fprintf(&b, "The OAuth refresh grant lapsed at %s. This is no longer a prediction.\n\n", deadline)
		fmt.Fprintf(&b, "The harness can no longer mint access tokens. The fleet is coasting on its\n")
		fmt.Fprintf(&b, "final 8-hour access token and will stop working by %s\n", lastGasp)
		fmt.Fprintf(&b, "at the latest — sooner if that token was minted a while ago.\n\n")
	} else {
		subject = fmt.Sprintf("fleet auth expires in %s (%s) — run /login", FormatRemaining(remaining), deadline)
		fmt.Fprintf(&b, "The fleet's OAuth refresh grant expires in %s, at %s.\n\n",
			FormatRemaining(remaining), deadline)
		fmt.Fprintf(&b, "This is a PREDICTION, not a fault report. Nothing is broken right now.\n")
		fmt.Fprintf(&b, "You have time to act at a moment of your choosing, which is the entire\n")
		fmt.Fprintf(&b, "point of this mail.\n\n")
		fmt.Fprintf(&b, "If nothing is done: the harness stops being able to mint access tokens at\n")
		fmt.Fprintf(&b, "%s, the fleet coasts on its last 8-hour access token, and\n", deadline)
		fmt.Fprintf(&b, "everything stops by %s.\n\n", lastGasp)
	}

	fmt.Fprintf(&b, "THE FIX (seconds, and only a human can do it):\n")
	fmt.Fprintf(&b, "  Run  /login  in any Claude Code session.\n\n")
	fmt.Fprintf(&b, "pogod cannot do this for you and does not try — it has no way to re-mint a\n")
	fmt.Fprintf(&b, "credential, and re-minting is not something an agent should be able to do.\n\n")

	fmt.Fprintf(&b, "WHAT IT COSTS TO IGNORE THIS:\n")
	fmt.Fprintf(&b, "  This has happened twice (2026-06-20 and 2026-07-21). Both times it went\n")
	fmt.Fprintf(&b, "  unnoticed until the fleet had already been dead for about a day — 498 and\n")
	fmt.Fprintf(&b, "  then 914 failed agent turns. The grant is a fixed 30-day life that use does\n")
	fmt.Fprintf(&b, "  not extend, so this recurs on a schedule.\n\n")

	fmt.Fprintf(&b, "AFTER YOU LOG IN:\n")
	fmt.Fprintf(&b, "  Already-running sessions do NOT recover instantly — measured at roughly an\n")
	fmt.Fprintf(&b, "  hour (mg-ed45), bounded by the refresh cadence. That lag is expected. Do not\n")
	fmt.Fprintf(&b, "  conclude the login failed and repeat it.\n")
	fmt.Fprintf(&b, "  Confirm the new date with:  pogo credential expiry\n\n")

	fmt.Fprintf(&b, "CREDENTIAL (no token material is read, logged or mailed — two integers only):\n")
	fmt.Fprintf(&b, "  refreshTokenExpiresAt : %s   <- the field this warning reads\n", deadline)
	if !st.AccessExpiry.IsZero() {
		fmt.Fprintf(&b, "  expiresAt             : %s   (8h access token; informational only,\n",
			st.AccessExpiry.Format(time.RFC3339))
		fmt.Fprintf(&b, "                          routinely stale on a healthy machine — not a signal)\n")
	}
	if st.SubscriptionType != "" {
		fmt.Fprintf(&b, "  subscriptionType      : %s\n", st.SubscriptionType)
	}
	if st.RateLimitTier != "" {
		fmt.Fprintf(&b, "  rateLimitTier         : %s\n", st.RateLimitTier)
	}
	fmt.Fprintf(&b, "\nMechanism: docs/investigations/credential-expiry-mechanism-2026-07-23.md (mg-ed45)\n")
	fmt.Fprintf(&b, "This warner predicts the SCHEDULED lapse only. An early revocation produces no\n")
	fmt.Fprintf(&b, "warning here and is caught reactively instead (mg-8cdb).\n")

	return subject, b.String()
}

// BlindMail builds the notice for a credential that exists but cannot be read.
// This mails because silence would be a lie: the machine has a credential, the
// warner cannot see its expiry, and the outage it exists to prevent would
// arrive with no notice at all. Reporting "I am blind" is the only honest
// output, and it is precisely the case that a naive implementation would treat
// as healthy.
func BlindMail(st Status, now time.Time) (subject, body string) {
	subject = "fleet auth expiry is UNREADABLE — the outage warning is blind"

	var b strings.Builder
	fmt.Fprintf(&b, "pogod found the `%s` keychain item but could not\n", KeychainService)
	fmt.Fprintf(&b, "determine when the OAuth refresh grant expires.\n\n")
	fmt.Fprintf(&b, "Reason: %s\n\n", st.Reason)
	fmt.Fprintf(&b, "THIS IS NOT A REPORT THAT THE CREDENTIAL IS FINE. It is a report that the\n")
	fmt.Fprintf(&b, "check which would have told you cannot see. The fleet-wide auth outage this\n")
	fmt.Fprintf(&b, "warner exists to prevent has happened twice, and both times it was noticed\n")
	fmt.Fprintf(&b, "only after roughly a day of destroyed work. Until this is fixed you would get\n")
	fmt.Fprintf(&b, "no advance warning of the third.\n\n")
	fmt.Fprintf(&b, "Most likely cause: the harness moved the credential's storage or JSON schema.\n")
	fmt.Fprintf(&b, "Both are harness-internal and pogo is not owed stability in them, so this is\n")
	fmt.Fprintf(&b, "an expected kind of breakage rather than a bug per se.\n\n")
	fmt.Fprintf(&b, "TO CHECK BY HAND:\n")
	fmt.Fprintf(&b, "  pogo credential expiry\n")
	fmt.Fprintf(&b, "  security find-generic-password -s %q -w | jq '.claudeAiOauth.refreshTokenExpiresAt'\n\n", KeychainService)
	fmt.Fprintf(&b, "Checked at %s.\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Mechanism: docs/investigations/credential-expiry-mechanism-2026-07-23.md (mg-ed45)\n")

	return subject, b.String()
}
