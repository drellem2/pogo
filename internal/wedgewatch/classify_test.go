package wedgewatch

import (
	"strings"
	"testing"
	"time"
)

var classifyNow = time.Date(2026, 8, 4, 21, 0, 0, 0, time.UTC)

// validCred is the credential as the doctor ACTUALLY found it on 2026-08-05
// while every agent on the box was showing "401 ... has been revoked": readable,
// refresh grant good for another 16.5 days, nothing revoked.
var validCred = CredentialView{
	Readable:      true,
	RefreshValid:  true,
	RefreshExpiry: classifyNow.Add(395 * time.Hour),
}

// lapsedCred is the case that has never actually been observed on this box: a
// credential that says of itself that it is unusable.
var lapsedCred = CredentialView{
	Readable:      true,
	RefreshValid:  false,
	RefreshExpiry: classifyNow.Add(-2 * time.Hour),
}

var blindCred = CredentialView{
	Readable: false,
	Reason:   "the keychain read timed out (likely an authorization prompt); expiry is UNKNOWN, not fine",
}

// TestA401AfterAConnectivityFailureIsOneSignature is mg-fc8d's first
// non-negotiable rule, and the single most consequential assertion in this
// package.
//
// The ticket was FILED blaming a revoked OAuth token. It was wrong: nothing was
// revoked, nobody logged in, and every agent resumed on the same credential.
// The 401 was downstream of a network outage that swallowed a token refresh.
// Concluding "credential revoked, page the human" from the 401 alone pages
// Daniel for a re-login that fixes nothing — and since the access token turns
// over roughly every 8h, any outage overlapping a refresh window reproduces
// this, about three chances a day.
func TestA401AfterAConnectivityFailureIsOneSignature(t *testing.T) {
	t.Run("both symptoms on the same agent", func(t *testing.T) {
		v := Classify(Evidence{
			Signatures: []Signature{SigAPI401, SigLoginPrompt, SigConnectivity},
			Cred:       validCred,
			Now:        classifyNow,
		}, Thresholds{})
		assertMergedOutage(t, v)
	})

	t.Run("connectivity remembered from another agent, minutes earlier", func(t *testing.T) {
		// This is the shape of the real incident: mayor read the 401 in a PTY,
		// the doctor read ENOTFOUND in the logs, and because the two
		// observations were split across two observers they were recorded as
		// two events.
		v := Classify(Evidence{
			Signatures:      []Signature{SigAPI401, SigLoginPrompt},
			LastConnFailure: classifyNow.Add(-22 * time.Minute),
			Cred:            validCred,
			Now:             classifyNow,
		}, Thresholds{})
		assertMergedOutage(t, v)
	})
}

func assertMergedOutage(t *testing.T, v Verdict) {
	t.Helper()
	if v.Cause != CauseRefreshFailedDuringOutage {
		t.Fatalf("cause = %s, want %s — a 401 beside a connectivity failure is ONE signature",
			v.Cause, CauseRefreshFailedDuringOutage)
	}
	if v.Cause == CausePoisonedCredential {
		t.Fatal("named a poisoned credential; this is the verdict that pages a human for a re-login that fixes nothing")
	}
	if v.Response != ResponseAwaitNetworkRecovery {
		t.Errorf("response = %s, want %s", v.Response, ResponseAwaitNetworkRecovery)
	}
	if !strings.Contains(v.Why, "NOTHING IS REVOKED") {
		t.Errorf("the reasoning must say plainly that nothing is revoked, or the next reader "+
			"repeats the mistake the ticket was filed with; got %q", v.Why)
	}
}

// TestPoisonedCredentialIsOnlyNamedWhenTheCredentialSaysSo pins the other side
// of the rule. The detector may name a bad credential — but only on the
// credential's own evidence, never inferred from a 401.
func TestPoisonedCredentialIsOnlyNamedWhenTheCredentialSaysSo(t *testing.T) {
	v := Classify(Evidence{
		Signatures: []Signature{SigAPI401, SigLoginPrompt},
		Cred:       lapsedCred,
		Now:        classifyNow,
	}, Thresholds{})
	if v.Cause != CausePoisonedCredential {
		t.Fatalf("cause = %s, want %s when the refresh grant has demonstrably lapsed", v.Cause, CausePoisonedCredential)
	}
	if v.Response != ResponseStopAndRedispatch {
		t.Errorf("response = %s, want %s", v.Response, ResponseStopAndRedispatch)
	}
}

// TestOppositeResponsesStayOpposite is mg-fc8d's second non-negotiable rule
// stated as an invariant rather than as two separate assertions: an outage and
// a poisoned credential must never resolve to the same handling. Leaving a
// wedged agent alone through an outage preserves hours of context; leaving one
// alone on a dead credential holds a slot and a worktree forever.
func TestOppositeResponsesStayOpposite(t *testing.T) {
	outage := Classify(Evidence{
		Signatures: []Signature{SigAPI401, SigConnectivity},
		Cred:       validCred,
		Now:        classifyNow,
	}, Thresholds{})
	poisoned := Classify(Evidence{
		Signatures: []Signature{SigAPI401},
		Cred:       lapsedCred,
		Now:        classifyNow,
	}, Thresholds{})
	if outage.Response == poisoned.Response {
		t.Fatalf("both resolved to %s — these two need OPPOSITE responses and the detector "+
			"has collapsed them", outage.Response)
	}
}

// TestUnknownRatherThanGuess covers every case where the evidence does not
// separate the two candidates. UNKNOWN is a first-class answer here: since the
// responses are opposites, a guess is worse than a shrug.
func TestUnknownRatherThanGuess(t *testing.T) {
	cases := []struct {
		name string
		ev   Evidence
	}{
		{
			// The credential actively refutes revocation, and no connectivity
			// evidence survives in the window. Something is wrong and the
			// detector does not know what.
			name: "401 with a credential that is readable and in date",
			ev: Evidence{
				Signatures: []Signature{SigAPI401, SigLoginPrompt},
				Cred:       validCred,
				Now:        classifyNow,
			},
		},
		{
			name: "401 with a credential that could not be read",
			ev: Evidence{
				Signatures: []Signature{SigAPI401},
				Cred:       blindCred,
				Now:        classifyNow,
			},
		},
		{
			// The un-enumerated case: a prompt nobody has met, caught only by
			// the counter/uptime cross-check.
			name: "a frozen counter and no enumerated marker at all",
			ev: Evidence{
				Signatures: []Signature{SigDeclaredTimeBelowUptime},
				Cred:       validCred,
				Now:        classifyNow,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Classify(tc.ev, Thresholds{})
			if v.Cause != CauseUnknown {
				t.Fatalf("cause = %s, want %s", v.Cause, CauseUnknown)
			}
			if v.Response != ResponseInvestigate {
				t.Errorf("response = %s, want %s", v.Response, ResponseInvestigate)
			}
		})
	}
}

// TestUnknownNeverRecommendsARelogin guards the specific wrong action. UNKNOWN
// must not quietly mean "page Daniel to re-login": on both occasions this fleet
// has seen the symptom, nothing was revoked and no login was performed.
func TestUnknownNeverRecommendsARelogin(t *testing.T) {
	v := Classify(Evidence{
		Signatures: []Signature{SigAPI401, SigLoginPrompt},
		Cred:       validCred,
		Now:        classifyNow,
	}, Thresholds{})
	if !strings.Contains(v.Why, "Do NOT page for a re-login") {
		t.Errorf("the UNKNOWN verdict must say explicitly not to page for a re-login; got %q", v.Why)
	}
}

// TestNoVerdictPrescribesANudge is the guard for mayor's 2026-08-05 retraction.
//
// The dispatch brief for this work item said a nudge revived all 15 agents. It
// did not: 968 nudges inside the outage window produced 0 acks, and crew-doctor
// — which received no immediate nudge — woke anyway on a routine scheduled fire
// ten minutes later. A nudge is neither sufficient nor necessary; what changed
// was the network. Naming an intervention that merely correlates with recovery
// would be worse than naming none, because a named remedy gets trusted.
func TestNoVerdictPrescribesANudge(t *testing.T) {
	all := []Evidence{
		{Signatures: []Signature{SigAPI401, SigConnectivity}, Cred: validCred, Now: classifyNow},
		{Signatures: []Signature{SigConnectivity}, Cred: validCred, Now: classifyNow},
		{Signatures: []Signature{SigAPI401}, Cred: lapsedCred, Now: classifyNow},
		{Signatures: []Signature{SigAPI401}, Cred: blindCred, Now: classifyNow},
		{Signatures: []Signature{SigRatingDialog}, Cred: validCred, Now: classifyNow},
		{Signatures: []Signature{SigDeclaredTimeBelowUptime}, Cred: validCred, Now: classifyNow},
	}
	for _, ev := range all {
		v := Classify(ev, Thresholds{})
		if strings.Contains(strings.ToLower(string(v.Response)), "nudge") {
			t.Errorf("response %q prescribes a nudge for %v — 968 nudges produced 0 acks", v.Response, ev.Signatures)
		}
		if strings.Contains(v.Why, "then nudge") || strings.Contains(v.Why, "nudge them") {
			t.Errorf("the reasoning prescribes a nudge for %v: %q", ev.Signatures, v.Why)
		}
	}
}

// TestConnectivityMemoryExpires pins the coincidence window. An outage from
// last week must not explain today's 401 — otherwise a genuine credential
// fault is permanently excused.
func TestConnectivityMemoryExpires(t *testing.T) {
	ev := Evidence{
		Signatures:      []Signature{SigAPI401},
		LastConnFailure: classifyNow.Add(-6 * time.Hour), // past the 2h window
		Cred:            lapsedCred,
		Now:             classifyNow,
	}
	if v := Classify(ev, Thresholds{}); v.Cause != CausePoisonedCredential {
		t.Fatalf("cause = %s, want %s — a stale outage memory must not excuse a demonstrably dead credential",
			v.Cause, CausePoisonedCredential)
	}
}

// TestNetworkDownWithoutAnAuthSymptom covers the outage caught before any 401
// has surfaced — and records that the 401s to follow are the SAME event.
func TestNetworkDownWithoutAnAuthSymptom(t *testing.T) {
	v := Classify(Evidence{
		Signatures: []Signature{SigConnectivity},
		Cred:       validCred,
		Now:        classifyNow,
	}, Thresholds{})
	if v.Cause != CauseNetworkDown {
		t.Fatalf("cause = %s, want %s", v.Cause, CauseNetworkDown)
	}
	if v.Response != ResponseAwaitNetworkRecovery {
		t.Errorf("response = %s, want %s", v.Response, ResponseAwaitNetworkRecovery)
	}
}

// TestModalWedgeDefersToTheModalWatcher records that mg-4421 owns the two
// enumerated modals; a finding here means its dismissal did not win.
func TestModalWedgeDefersToTheModalWatcher(t *testing.T) {
	for _, sig := range []Signature{SigRatingDialog, SigRateLimitModal} {
		v := Classify(Evidence{Signatures: []Signature{sig}, Cred: validCred, Now: classifyNow}, Thresholds{})
		if v.Cause != CauseModalWedge {
			t.Errorf("%s: cause = %s, want %s", sig, v.Cause, CauseModalWedge)
		}
		if v.Response != ResponseModalWatcherOwnsIt {
			t.Errorf("%s: response = %s, want %s", sig, v.Response, ResponseModalWatcherOwnsIt)
		}
	}
}

// TestAuthSymptomOutranksAModal pins precedence. An agent showing both a 401
// and a modal is an auth event with a modal on top of it, not a modal wedge —
// deferring to mg-4421 there would file the fleet-killing case under the one
// that has an owner.
func TestAuthSymptomOutranksAModal(t *testing.T) {
	v := Classify(Evidence{
		Signatures: []Signature{SigRateLimitModal, SigAPI401, SigConnectivity},
		Cred:       validCred,
		Now:        classifyNow,
	}, Thresholds{})
	if v.Cause != CauseRefreshFailedDuringOutage {
		t.Fatalf("cause = %s, want %s", v.Cause, CauseRefreshFailedDuringOutage)
	}
}
