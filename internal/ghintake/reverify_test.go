package ghintake

import (
	"strings"
	"testing"
	"time"
)

// verifierSaying builds a Verifier and records whether it was consulted, so the
// tests can assert the NEGATIVE half of the design — that a healthy scan spends
// no request at all — as well as the positive.
func verifierSaying(state CredentialState, detail string, calls *int) Verifier {
	return func() (CredentialState, string) {
		if calls != nil {
			*calls++
		}
		return state, detail
	}
}

// The mg-4d59 shape end to end: a scan armed with a credential that EXISTS,
// every watched repo failing, and GitHub answering 401 when asked directly.
func TestReverifyDowngradesPresentToRejectedOnA401(t *testing.T) {
	inv := Inventory{
		Credential: CredentialPresent, CredentialSource: "ambient",
		RepoErrors: []RepoError{
			{Repo: "drellem2/pogo", Detail: "HTTP 401: Bad credentials"},
			{Repo: "drellem2/macguffin", Detail: "HTTP 401: Bad credentials"},
		},
	}
	calls := 0
	got := Reverify(inv, verifierSaying(CredentialRejected, "GitHub REFUSED the credential (HTTP 401)", &calls))
	if calls != 1 {
		t.Fatalf("the verifier must be consulted exactly once, got %d", calls)
	}
	if got.Credential != CredentialRejected {
		t.Fatalf("want rejected, got %q", got.Credential)
	}
	if got.CredentialSource != "ambient" {
		t.Errorf("the source must survive the downgrade — it names WHICH credential failed, got %q", got.CredentialSource)
	}
	if !strings.Contains(got.CredentialDetail, "401") {
		t.Errorf("the measured detail must be carried, got %q", got.CredentialDetail)
	}
}

// The cost objection the arm-time design raised is answered, not overridden: a
// scan with nothing to explain must not make a request.
func TestReverifyDoesNothingWhenNoRepoFailed(t *testing.T) {
	inv := Inventory{Credential: CredentialPresent, CredentialSource: "shell"}
	calls := 0
	got := Reverify(inv, verifierSaying(CredentialRejected, "would be wrong to ask", &calls))
	if calls != 0 {
		t.Fatalf("a clean scan must not consult the verifier, got %d call(s)", calls)
	}
	if got.Credential != CredentialPresent || got.CredentialDetail != "" {
		t.Fatalf("a clean scan's predicate must be untouched, got %+v", got)
	}
}

// The mirror defect, and the one that would be easiest to ship by accident: a
// network failure reported as a credential verdict. UNREACHABLE must leave the
// classification alone — the report's cause 1 is network, and downgrading here
// would send a reader at `gh auth login` for an outage, which is mg-c058 rebuilt.
func TestReverifyNeverDowngradesOnANonRejection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state CredentialState
	}{
		{"unreachable or uninterpretable", CredentialUnknown},
		{"accepted", CredentialPresent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := Inventory{
				Credential: CredentialPresent, CredentialSource: "ambient",
				RepoErrors: []RepoError{{Repo: "drellem2/pogo", Detail: "ENOTFOUND api.github.com"}},
			}
			got := Reverify(inv, verifierSaying(tc.state, "the request did not complete", nil))
			if got.Credential != CredentialPresent {
				t.Fatalf("only a rejection may change the classification, got %q", got.Credential)
			}
			if got.CredentialDetail == "" {
				t.Error("the detail must be recorded even when nothing changed — " +
					"'could not be re-checked' is the corroboration cause 1 needs")
			}
		})
	}
}

// CredentialMissing is already decided, against the `gh auth login` store, and
// its remedy is the right one. Re-asking would spend a request to learn nothing
// and could only muddy a correct message.
func TestReverifyLeavesAMissingCredentialAlone(t *testing.T) {
	inv := Inventory{Credential: CredentialMissing,
		RepoErrors: []RepoError{{Repo: "drellem2/pogo", Detail: "gh: not logged in"}}}
	calls := 0
	got := Reverify(inv, verifierSaying(CredentialRejected, "x", &calls))
	if calls != 0 {
		t.Fatalf("a missing credential must not be re-asked, got %d call(s)", calls)
	}
	if got.Credential != CredentialMissing {
		t.Fatalf("want missing, got %q", got.Credential)
	}
}

// Reverify can promote an unrecorded arm-time predicate straight to rejected, so
// the report must have something to print for the source. "source=" with nothing
// after it is a rendering bug in the one message whose job is being believed.
func TestAnUnrecordedCredentialSourceStillRendersAName(t *testing.T) {
	for _, st := range []CredentialState{CredentialRejected, CredentialPresent} {
		rep := Detect(Inventory{
			Credential: st, CredentialSource: "", CredentialDetail: "HTTP 401", ItemsScanned: 10,
			RepoErrors: []RepoError{{Repo: "drellem2/pogo", Detail: "HTTP 401: Bad credentials"}},
		}, time.Now(), time.Hour)
		for _, out := range []string{rep.Render(), rep.MailSubject()} {
			if strings.Contains(out, "source=)") || strings.Contains(out, "source=\n") ||
				strings.Contains(out, "source= ") || strings.HasSuffix(out, "source=") {
				t.Errorf("%s: an empty source rendered as a dangling \"source=\":\n%s", st, out)
			}
			if !strings.Contains(out, "unrecorded") {
				t.Errorf("%s: an empty source must be NAMED as unrecorded:\n%s", st, out)
			}
		}
	}
}

// A 401 does not rule out everything, and saying it does would be this ticket's
// own defect with the confidence pointed the other way.
func TestTheRejectedReportDoesNotOverclaimWhatA401Rules0ut(t *testing.T) {
	body := Detect(Inventory{
		Credential: CredentialRejected, CredentialSource: "ambient",
		CredentialDetail: "GitHub REFUSED the credential (HTTP 401)", ItemsScanned: 10,
		RepoErrors: []RepoError{{Repo: "drellem2/pogo", Detail: "HTTP 401: Bad credentials"}},
	}, time.Now(), time.Hour).Render()

	// Network and rate limiting a 401 genuinely does settle, and the body must
	// say WHY rather than assert it.
	if !strings.Contains(body, "the API answered") || !strings.Contains(body, "403") {
		t.Errorf("the body rules causes out without giving its reason:\n%s", body)
	}
	// A rename it does NOT settle, and claiming otherwise is the over-claim.
	if strings.Contains(body, "renamed repo are all RULED OUT") {
		t.Errorf("the body claims a 401 rules out a rename, which it does not:\n%s", body)
	}
	if !strings.Contains(body, "would hide one if it existed") {
		t.Errorf("the body must state the residual it actually has:\n%s", body)
	}
}

// A caller with nothing to bind is honest, not broken.
func TestReverifyWithANilVerifierIsANoOp(t *testing.T) {
	inv := Inventory{Credential: CredentialPresent,
		RepoErrors: []RepoError{{Repo: "drellem2/pogo", Detail: "boom"}}}
	if got := Reverify(inv, nil); got.Credential != CredentialPresent || got.CredentialDetail != "" {
		t.Fatalf("nil verifier must change nothing, got %+v", got)
	}
}

// VerifierFor must not launder a non-rejection into a positive claim. It knows
// only one bit, and CredentialPresent is not something it measured.
func TestVerifierForOnlyEverAssertsARejection(t *testing.T) {
	v := VerifierFor(func() (bool, string) { return true, "HTTP 401" })
	if st, d := v(); st != CredentialRejected || d != "HTTP 401" {
		t.Fatalf("want rejected/HTTP 401, got %q/%q", st, d)
	}
	v = VerifierFor(func() (bool, string) { return false, "HTTP 200" })
	if st, d := v(); st != CredentialUnknown || d != "HTTP 200" {
		t.Fatalf("a non-rejection must be unknown (detail kept), got %q/%q", st, d)
	}
	if VerifierFor(nil) != nil {
		t.Error("VerifierFor(nil) must be nil so Reverify's no-op path is reachable")
	}
}

// The 173-hour failure, restated as an assertion about the SUBJECT LINE — the
// part that travels, gets skimmed, forwarded and filed a ticket from.
func TestRejectedCredentialLeadsTheMailSubjectWithTheCause(t *testing.T) {
	rep := Detect(Inventory{
		Credential: CredentialRejected, CredentialSource: "ambient",
		CredentialDetail: "GitHub REFUSED the credential (HTTP 401)",
		ItemsScanned:     10,
		RepoErrors: []RepoError{
			{Repo: "drellem2/macguffin", Detail: "HTTP 401: Bad credentials"},
			{Repo: "drellem2/pogo", Detail: "HTTP 401: Bad credentials"},
		},
	}, time.Now(), time.Hour)

	if !rep.RejectedCredential() {
		t.Fatal("RejectedCredential() must be true")
	}
	if rep.NoCredential() {
		t.Fatal("a rejected credential is not a missing one — the remedies differ")
	}
	if !rep.Actionable() {
		t.Fatal("two unreadable repos are actionable")
	}

	subj := rep.MailSubject()
	for _, want := range []string{"REJECTED", "401", "restart pogod"} { // subject
		if !strings.Contains(subj, want) {
			t.Errorf("the subject must carry %q — a body hedge does not survive a forward: %s", want, subj)
		}
	}
	// The exact sentence that was wrong for 173 hours.
	if strings.Contains(subj, "not a missing one") {
		t.Errorf("the subject still rules out the cause that IS the cause: %s", subj)
	}

	body := rep.Render()
	for _, want := range []string{
		"GITHUB REJECTED THIS SCAN'S CREDENTIAL",
		"HTTP 401",
		"launchctl kickstart -k gui/$(id -u)/com.pogo.daemon",
		"source=ambient",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the rendered report is missing %q:\n%s", want, body)
		}
	}
	// The remedy must not be the missing-credential one, and must not rank the
	// true cause fourth.
	if strings.Contains(body, "Causes\nstill open, most likely first") {
		t.Error("a measured rejection must not be rendered as an open list of causes")
	}
}

// The other half of the fix: when the re-check ran and did NOT find a rejection,
// the CredentialPresent report must SAY so, and when nothing re-asked it must say
// that instead. Those are different facts and a reader ranking causes needs to
// know which one they hold.
func TestPresentReportStatesWhetherTheReCheckRan(t *testing.T) {
	base := Inventory{
		Credential: CredentialPresent, CredentialSource: "shell", ItemsScanned: 10,
		RepoErrors: []RepoError{{Repo: "drellem2/pogo", Detail: "ENOTFOUND api.github.com"}},
	}

	ran := base
	ran.CredentialDetail = "the request to https://api.github.com/user did not complete"
	body := Detect(ran, time.Now(), time.Hour).Render()
	if !strings.Contains(body, "scan-time credential re-check: the request") {
		t.Errorf("a re-check that ran must be reported:\n%s", body)
	}

	body = Detect(base, time.Now(), time.Hour).Render()
	if !strings.Contains(body, "DID NOT RUN") {
		t.Errorf("a re-check that did NOT run must be reported as such, not left blank:\n%s", body)
	}
}

// Every credential state must render a DISTINGUISHABLE heading and subject.
// Same requirement the source strings carry in ghtoken, same reason: a message
// that reads alike for two states cannot classify either.
func TestEveryCredentialStateRendersDistinguishably(t *testing.T) {
	seenBody := map[string]CredentialState{}
	seenSubj := map[string]CredentialState{}
	for _, st := range []CredentialState{CredentialUnknown, CredentialPresent, CredentialMissing, CredentialRejected} {
		rep := Detect(Inventory{
			Credential: st, CredentialSource: "ambient", ItemsScanned: 10,
			RepoErrors: []RepoError{{Repo: "drellem2/pogo", Detail: "boom"}},
		}, time.Now(), time.Hour)
		body, subj := rep.Render(), rep.MailSubject()
		if other, dup := seenBody[body]; dup {
			t.Errorf("states %s and %s render identical bodies", other, st)
		}
		if other, dup := seenSubj[subj]; dup {
			t.Errorf("states %s and %s render identical subjects", other, st)
		}
		seenBody[body], seenSubj[subj] = st, st
	}
}
