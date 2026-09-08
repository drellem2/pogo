package ghtoken

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// # Why Ensure is not enough, and what Verify adds (mg-4d59)
//
// Ensure and Result.OK() answer ONE question: does a credential EXIST for this
// process. That is the whole question pathenv's sibling needed to answer, and it
// is answered correctly. It is not the question a reader of a failed scan is
// asking.
//
// The gap was measured on this host on 2026-09-08. pogod (pid 6610, up 174h)
// had been exec'd by a shell that held GH_TOKEN, so Ensure short-circuited on
// SourceAmbient and did nothing — correctly, by its own contract. That token was
// subsequently rotated. `~/.zshenv` gained the new value, every shell and every
// crew agent picked it up, and pogod kept the 174-hour-old copy, because Go's
// os/exec hands children a copy of the PARENT's environment and nothing
// re-reads a shell after startup.
//
// The consequence is the reason this file exists. `gh issue list` returned
// HTTP 401 on BOTH watched repos on every intake sample for 173 hours, while
// the same command from a crew agent's shell succeeded — and the report
// rendered the failures under CredentialPresent, whose text says a credential
// "WAS configured, so this is not a missing one" and ranks "EXPIRED or REVOKED"
// FOURTH behind network, rate limiting and repo renames. The detector failed
// loudly and correctly for 173 hours while pointing every reader away from the
// one true cause.
//
// Two facts about that are worth separating, because only one of them is a bug:
//
//   - "A credential exists" was TRUE. Ensure did not lie.
//   - "A credential exists" was being READ as "the credential authenticates".
//     Existence and validity are different predicates, and the report was
//     spending the first one's evidence on the second one's claim.
//
// So Verify is a second, DIFFERENT predicate rather than a repair to the first.
// Nothing here changes what Ensure does or when it runs.
//
// # The contract is the HTTP status, not any prose
//
// This package's standing rule is that gh's English is not an interface: a
// matcher against "Bad credentials" stops working silently the first time the
// wording changes, and a check that keeps passing while it stops checking is
// worse than no check. Verify obeys that rule and gets a STRONGER contract for
// it than the exit-status one Ensure uses, because it speaks to the API
// directly: HTTP 401 is defined by GitHub's API, versioned as such, and means
// exactly one thing.
//
// That also removes the need for a separate reachability control. A transport
// error and a 401 are different return paths from one round trip, so REJECTED
// and UNREACHABLE are distinguished by construction rather than by a second
// probe whose own failure would need interpreting. (The obvious control — an
// anonymous `gh api` call — does not exist: gh refuses to run `api` with no
// credential at all, exiting 0 with a "please run gh auth login" banner and no
// request made. Measured, not assumed.)
//
// # It verifies the credential THIS PROCESS HOLDS
//
// The token is read from this process's own environment, with gh's precedence
// (GH_TOKEN, then GITHUB_TOKEN). That is deliberate and it is the entire point:
// the question is not "is there a valid token on this machine" — there was, in
// every shell, all 173 hours — but "does the credential this process's `gh`
// children will resolve actually authenticate". Anything that consulted a shell
// or a config file would answer the question that was never in doubt.
//
// # Secret discipline is unchanged
//
// The value goes into one Authorization header and nowhere else. No branch of
// this file puts it into a State, a Detail, an error, or a log line, and
// TestVerifyObservablesNeverCarryTheValue pins that.

// VerifyState is the outcome of one credential verification. It is deliberately
// four-valued: the two failure states have different remedies, and the fourth
// exists so that "the probe could not run" is never silently rendered as either.
type VerifyState string

const (
	// VerifyUnknown: no verification happened. Either there was no credential in
	// this process's environment to verify, or the round trip produced a status
	// this package declines to interpret. The honest zero value — a caller that
	// gets this learns nothing and must not claim anything.
	VerifyUnknown VerifyState = "unknown"
	// VerifyOK: GitHub accepted the credential (HTTP 200) at the moment of the
	// probe. Not a claim about any later moment.
	VerifyOK VerifyState = "ok"
	// VerifyRejected: GitHub REFUSED the credential (HTTP 401). The credential
	// exists, is well-formed, and does not authenticate — expired, revoked, or
	// rotated out from under a long-lived process. This is the state that was
	// unreachable before mg-4d59, and the state this host spent 173 hours in.
	VerifyRejected VerifyState = "rejected"
	// VerifyUnreachable: the request did not complete — DNS, connect, TLS or
	// timeout. Says nothing whatsoever about the credential, and must not be
	// rendered as though it did. This is the state the 2026-08-14 network outage
	// belonged in, which the old single-predicate report could not express.
	VerifyUnreachable VerifyState = "unreachable"
)

// VerifyResult carries the state and a one-line, existence-only reason.
type VerifyResult struct {
	// State is the verdict. See VerifyState.
	State VerifyState
	// Detail says what was actually measured, in terms a reader can re-derive:
	// an HTTP status, or the transport failure. It NEVER contains the token, and
	// never contains a response body — GitHub's error bodies are prose, and a
	// message that ends up in mail should not carry a remote server's text.
	Detail string
}

// OK reports whether the credential authenticated. False for every state that
// is not VerifyOK, INCLUDING the two that mean "we do not know" — a caller that
// wants to distinguish "bad" from "unknown" must read State, and the naming is
// blunt on purpose so that a caller which does not cannot accidentally treat an
// unreachable network as a good credential.
func (r VerifyResult) OK() bool { return r.State == VerifyOK }

// String renders the result for a log line, existence-only by construction.
func (r VerifyResult) String() string {
	if r.Detail == "" {
		return fmt.Sprintf("credential verification: %s", r.State)
	}
	return fmt.Sprintf("credential verification: %s — %s", r.State, r.Detail)
}

// VerifyURL is the endpoint the probe calls. `/user` is the cheapest endpoint
// that requires authentication and exists for every token shape, so a 200 from
// it means the credential is live rather than merely well-formed.
//
// A var, not a const, so a test can point it at an httptest server and reach
// every branch — including the transport-failure branch — with no network and
// no real secret.
var VerifyURL = "https://api.github.com/user"

// verifyTimeout bounds the probe. Short: Verify runs on the failure path of a
// scan that has already spent its time budget failing, and a hung probe would
// turn a diagnosable failure into a stalled one.
const verifyTimeout = 10 * time.Second

// Verify asks GitHub whether the credential in THIS PROCESS's environment still
// authenticates.
//
// It is a separate predicate from Ensure/OK() and does not replace it — see the
// commentary at the top of this file for the 173-hour reason both are needed.
//
// Call it on the FAILURE path, not on every scan. It is a network round trip,
// and on a healthy scan the answer is both already implied by the successful
// calls and not worth a request. What it is for is the moment a scan's GitHub
// calls have failed and a reader is about to be told which of four causes to
// chase.
func Verify() VerifyResult { return verify(os.Getenv, http.DefaultClient) }

// doer is the seam. http.Client satisfies it; a test substitutes a stub to
// reach the transport-failure branch deterministically.
type doer interface {
	Do(*http.Request) (*http.Response, error)
}

func verify(getenv func(string) string, client doer) VerifyResult {
	// gh's own precedence. Matching it is what makes this a statement about the
	// credential this process's `gh` children will actually resolve.
	var tok string
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			tok = v
			break
		}
	}
	if tok == "" {
		return VerifyResult{State: VerifyUnknown,
			Detail: "no credential in this process's environment to verify"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, VerifyURL, nil)
	if err != nil {
		return VerifyResult{State: VerifyUnknown, Detail: "could not build the probe request"}
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		// Deliberately not err.Error(): a URL error's text can echo the request,
		// and this string travels into logs and mail. The states are what a
		// reader acts on; the reason is named in kind, not quoted.
		return VerifyResult{State: VerifyUnreachable,
			Detail: fmt.Sprintf("the request to %s did not complete (DNS, connect, TLS or timeout) — "+
				"this says nothing about the credential", VerifyURL)}
	}
	defer resp.Body.Close()
	// Drained and discarded. GitHub's error bodies are prose; nothing here reads
	// them, for the same reason nothing in this package reads gh's stderr.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch resp.StatusCode {
	case http.StatusOK:
		return VerifyResult{State: VerifyOK,
			Detail: "GitHub accepted the credential (HTTP 200) at the moment of this probe"}
	case http.StatusUnauthorized:
		return VerifyResult{State: VerifyRejected,
			Detail: "GitHub REFUSED the credential (HTTP 401) — it exists but does not authenticate"}
	default:
		// 403 is the one that must NOT be folded into either verdict: it is
		// rate limiting as often as it is a scope problem, and calling a
		// rate-limited credential "rejected" would rebuild this ticket's own
		// defect pointing at a different wrong remedy.
		return VerifyResult{State: VerifyUnknown,
			Detail: fmt.Sprintf("GitHub answered HTTP %d, which this probe does not interpret "+
				"as either acceptance or rejection", resp.StatusCode)}
	}
}

// RejectionProbe is Verify reduced to the one bit a report's classification
// turns on: did GitHub REFUSE this credential. Everything else — accepted,
// unreachable, uninterpretable — collapses to false, because none of those is
// evidence that the credential is bad and a caller must not act as if it were.
//
// It exists in this shape so that internal/ghintake can bind a real probe
// without importing this package's types, the same arrangement CredentialFor
// already uses for Ensure. The detail survives the collapse, so the caller can
// still print what was measured when the answer is "not a rejection".
func RejectionProbe() (rejected bool, detail string) {
	r := Verify()
	return r.State == VerifyRejected, r.Detail
}
