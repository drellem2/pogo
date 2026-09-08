package ghtoken

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubDoer reaches the transport-failure branch without a network, which is the
// branch that must NOT be confused with a rejection — the whole point of the
// four-valued state.
type stubDoer struct{ err error }

func (s stubDoer) Do(*http.Request) (*http.Response, error) { return nil, s.err }

func envGet(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// serveStatus stands up a server that answers every request with one status,
// and points VerifyURL at it for the duration of the test.
func serveStatus(t *testing.T, code int, seen *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Header.Clone()
		}
		w.WriteHeader(code)
		// A prose body, deliberately: GitHub sends one, and nothing in this
		// package may read it.
		_, _ = w.Write([]byte(`{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest"}`))
	}))
	t.Cleanup(srv.Close)
	old := VerifyURL
	VerifyURL = srv.URL
	t.Cleanup(func() { VerifyURL = old })
	return srv
}

// The state this package could not express before mg-4d59, and the one this
// host spent 173 hours in: a credential that EXISTS and does not authenticate.
func TestVerifyRejectsA401(t *testing.T) {
	serveStatus(t, http.StatusUnauthorized, nil)
	res := verify(envGet(map[string]string{"GH_TOKEN": fakeToken}), http.DefaultClient)
	if res.State != VerifyRejected {
		t.Fatalf("want rejected, got %+v", res)
	}
	if res.OK() {
		t.Fatal("a rejected credential must not satisfy OK()")
	}
	if !strings.Contains(res.Detail, "401") {
		t.Errorf("the detail must name what was measured: %q", res.Detail)
	}
}

func TestVerifyAcceptsA200(t *testing.T) {
	serveStatus(t, http.StatusOK, nil)
	res := verify(envGet(map[string]string{"GH_TOKEN": fakeToken}), http.DefaultClient)
	if res.State != VerifyOK || !res.OK() {
		t.Fatalf("want ok, got %+v", res)
	}
}

// A network failure must never be reported as a credential verdict in EITHER
// direction. Reporting it as rejected would rebuild mg-4d59's defect pointing
// at the opposite wrong remedy; reporting it as ok would be the 2026-08-14
// outage all over again.
func TestVerifyTransportFailureIsUnreachableNotRejected(t *testing.T) {
	res := verify(envGet(map[string]string{"GH_TOKEN": fakeToken}), stubDoer{err: errors.New("dial tcp: no such host")})
	if res.State != VerifyUnreachable {
		t.Fatalf("want unreachable, got %+v", res)
	}
	if res.OK() {
		t.Fatal("an unreachable API must not satisfy OK()")
	}
	if !strings.Contains(res.Detail, "says nothing about the credential") {
		t.Errorf("the detail must refuse to make a credential claim: %q", res.Detail)
	}
}

// 403 is rate limiting as often as it is a scope problem. Folding it into
// rejected would send a reader at `gh auth login` for a throttle.
func TestVerifyDoesNotInterpretA403(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusNotFound} {
		serveStatus(t, code, nil)
		res := verify(envGet(map[string]string{"GH_TOKEN": fakeToken}), http.DefaultClient)
		if res.State != VerifyUnknown {
			t.Errorf("HTTP %d: want unknown, got %+v", code, res)
		}
	}
}

// With nothing to verify there is nothing to say. Unknown, not ok, and not
// rejected — a probe that cannot run must not answer.
func TestVerifyWithNoCredentialIsUnknown(t *testing.T) {
	res := verify(envGet(nil), stubDoer{err: errors.New("must not be called")})
	if res.State != VerifyUnknown {
		t.Fatalf("want unknown, got %+v", res)
	}
	res = verify(envGet(map[string]string{"GH_TOKEN": "   "}), stubDoer{err: errors.New("must not be called")})
	if res.State != VerifyUnknown {
		t.Fatalf("a blank token is nothing to verify, got %+v", res)
	}
}

// gh's precedence, matched: GH_TOKEN wins, GITHUB_TOKEN is honoured when it does
// not. This is what makes Verify a statement about the credential this process's
// `gh` children will actually resolve, rather than about the machine at large —
// the distinction mg-4d59 turns on.
func TestVerifyUsesGHPrecedenceOfThisProcessEnvironment(t *testing.T) {
	var seen http.Header
	serveStatus(t, http.StatusOK, &seen)

	verify(envGet(map[string]string{"GH_TOKEN": "gh-token-value", "GITHUB_TOKEN": "github-token-value"}), http.DefaultClient)
	if got := seen.Get("Authorization"); got != "Bearer gh-token-value" {
		t.Errorf("GH_TOKEN must win, got %q", got)
	}

	verify(envGet(map[string]string{"GITHUB_TOKEN": "github-token-value"}), http.DefaultClient)
	if got := seen.Get("Authorization"); got != "Bearer github-token-value" {
		t.Errorf("GITHUB_TOKEN must be honoured when GH_TOKEN is unset, got %q", got)
	}
}

// The secret-discipline guard, extended to the new observables. Everything this
// probe produces travels into logs and mail.
func TestVerifyObservablesNeverCarryTheValue(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden} {
		serveStatus(t, code, nil)
		res := verify(envGet(map[string]string{"GH_TOKEN": fakeToken}), http.DefaultClient)
		if strings.Contains(res.Detail, fakeToken) || strings.Contains(res.String(), fakeToken) {
			t.Fatalf("HTTP %d: the verification result leaked the token value", code)
		}
	}
	res := verify(envGet(map[string]string{"GH_TOKEN": fakeToken}), stubDoer{err: errors.New(fakeToken)})
	if strings.Contains(res.Detail, fakeToken) || strings.Contains(res.String(), fakeToken) {
		t.Fatal("the transport-failure detail repeated the error text, which can carry the request")
	}
}

// GitHub's error bodies are prose, and this package's standing rule is that
// prose is not an interface. Nothing may reach a verdict by reading one.
func TestVerifyNeverEchoesTheResponseBody(t *testing.T) {
	serveStatus(t, http.StatusUnauthorized, nil)
	res := verify(envGet(map[string]string{"GH_TOKEN": fakeToken}), http.DefaultClient)
	for _, prose := range []string{"Bad credentials", "documentation_url"} {
		if strings.Contains(res.String(), prose) {
			t.Errorf("the result repeated the response body (%q): %s", prose, res)
		}
	}
}

// The four states must render distinguishably — the same requirement
// TestResultStringNamesTheWinningSource pins for Ensure, and for the same
// reason: a line that cannot tell two states apart cannot answer the question
// it was added to answer.
func TestVerifyStatesRenderDistinguishably(t *testing.T) {
	seen := map[string]VerifyState{}
	for _, st := range []VerifyState{VerifyUnknown, VerifyOK, VerifyRejected, VerifyUnreachable} {
		line := VerifyResult{State: st}.String()
		if !strings.Contains(line, string(st)) {
			t.Errorf("VerifyResult{%s}.String() does not name its state: %s", st, line)
		}
		if other, dup := seen[line]; dup {
			t.Errorf("states %s and %s render identically: %s", other, st, line)
		}
		seen[line] = st
		if (VerifyResult{State: st}).OK() != (st == VerifyOK) {
			t.Errorf("OK() must be true for exactly VerifyOK, %s disagrees", st)
		}
	}
}
