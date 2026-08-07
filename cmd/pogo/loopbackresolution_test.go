package main

// Tests for the "loopback resolution" doctor row (mg-e314, drellem2/pogo#110).
//
// The load-bearing tests here are the POSITIVE CONTROLS: a check for silent
// impersonation that has only ever been observed returning pass is itself a
// silent impersonation of a check. Two of them run against real sockets —
// TestLoopbackResolution_InterloperOnV6Fires binds a stranger to [::1]:<port>
// and requires the check to FIRE, and TestLoopbackResolution_PassesOnceTheV6
// PortIsFree removes it and requires the same code to go quiet — because
// "fires when it should" and "does not fire when it should not" are two
// claims, and a detector needs both.

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/health"
)

const testProbeTimeout = 2 * time.Second

// ---------------------------------------------------------------------------
// The rendering, as a pure function over the two probes.
// ---------------------------------------------------------------------------

// TestLoopbackResolutionLine_ShadowedNameFails is the incident, in table form:
// pogod healthy on the bind address, a stranger answering under the name.
func TestLoopbackResolutionLine_ShadowedNameFails(t *testing.T) {
	bind := loopbackProbe{URL: "http://127.0.0.1:10000", Remote: "127.0.0.1:10000", IsPogod: true}
	name := loopbackProbe{
		URL:     "http://localhost:10000",
		Remote:  "[::1]:10000",
		Detail:  "answered HTTP 200 but the body is not pogod's (got \"Handling connection for 10000\")",
		IsPogod: false,
	}
	status, detail := loopbackResolutionLine(bind, name, 10000)
	if status != "fail" {
		t.Fatalf("status = %q, want fail — a shadowed name is the condition this row exists for", status)
	}
	// Each of these is a fact the reader cannot reconstruct from the others.
	for _, want := range []string{
		"[::1]:10000",                        // which address is lying
		"HEALTHY",                            // so nobody restarts a working daemon
		"do not restart it",                  //
		"lsof -nP -i6TCP:10000 -sTCP:LISTEN", // the command that names the culprit
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail is missing %q; got: %s", want, detail)
		}
	}
}

// TestLoopbackResolutionLine_UnreachableNameFailsWithoutInventingACulprit:
// the name may fail to connect at all rather than reach an impostor. Still a
// failure — the name does not lead to pogod — but the detail must not describe
// a process it never saw.
func TestLoopbackResolutionLine_UnreachableNameFailsWithoutInventingACulprit(t *testing.T) {
	bind := loopbackProbe{URL: "http://127.0.0.1:10000", Remote: "127.0.0.1:10000", IsPogod: true}
	name := loopbackProbe{URL: "http://localhost:10000", Detail: "no answer: lookup localhost: no such host"}
	status, detail := loopbackResolutionLine(bind, name, 10000)
	if status != "fail" {
		t.Fatalf("status = %q, want fail", status)
	}
	if !strings.Contains(detail, "could not be reached at all") {
		t.Errorf("detail should say the name never connected; got: %s", detail)
	}
	if strings.Contains(detail, "is NOT pogod") {
		t.Errorf("detail describes a process that was never observed; got: %s", detail)
	}
	// Nothing answered, so there is no culprit. A remedy that sends an operator
	// hunting a process on a host whose name simply does not resolve wastes
	// exactly the time this row exists to save (review round 1, advisory).
	if !strings.Contains(detail, "no process to hunt") {
		t.Errorf("detail must say there is nothing to find; got: %s", detail)
	}
	if !strings.Contains(detail, "not an impersonator") {
		t.Errorf("detail must rule the impersonator reading out explicitly; got: %s", detail)
	}
	// The remedy must not be the culprit-hunting one. "impersonator" is fine in
	// the negation above, so this checks the ACTIONS a reader would take.
	for _, forbidden := range []string{"lsof", "talking to that process", "keeps the port until it is stopped"} {
		if strings.Contains(detail, forbidden) {
			t.Errorf("detail sends the reader hunting a process that was never observed (%q); got: %s", forbidden, detail)
		}
	}
}

// TestLoopbackResolutionLine_SecondPogodPassesButSaysSo. A second pogod on the
// v6 loopback emits health.LivenessBody like any other, so BOTH probes report
// pogod and this row passes. That case is genuinely not detected — the body
// match separates pogod-shaped from not-pogod-shaped, never THIS pogod from
// ANOTHER — and the green line must therefore not read as an all-clear for it
// (review round 1, blocking). The row states the observation it does hold: the
// name landed somewhere other than the bind address.
func TestLoopbackResolutionLine_SecondPogodPassesButSaysSo(t *testing.T) {
	bind := loopbackProbe{URL: "http://127.0.0.1:10000", Remote: "127.0.0.1:10000", IsPogod: true}
	name := loopbackProbe{URL: "http://localhost:10000", Remote: "[::1]:10000", IsPogod: true}
	status, detail := loopbackResolutionLine(bind, name, 10000)
	if status != "pass" {
		t.Fatalf("status = %q, want pass — this row cannot tell two pogods apart and must not pretend to", status)
	}
	for _, want := range []string{
		"NOT the address pogod was probed on", // the observation it does hold
		"SECOND pogod",                        // the case it does not cover, named
		"does not tell them apart",            // its own limit, stated
		"start_time",                          // where to actually settle it
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("pass line is missing %q, so a reader chasing a second daemon reads green as an all-clear; got: %s", want, detail)
		}
	}
}

// TestLoopbackResolutionLine_PassStaysQuietWhenTheNameLandsOnTheBindAddress.
// The caveat above is bought with words on every healthy run if it fires
// unconditionally. It must appear only when there is something to explain.
func TestLoopbackResolutionLine_PassStaysQuietWhenTheNameLandsOnTheBindAddress(t *testing.T) {
	bind := loopbackProbe{URL: "http://127.0.0.1:10000", Remote: "127.0.0.1:10000", IsPogod: true}
	name := loopbackProbe{URL: "http://localhost:10000", Remote: "127.0.0.1:10000", IsPogod: true}
	_, detail := loopbackResolutionLine(bind, name, 10000)
	if strings.Contains(detail, "SECOND pogod") {
		t.Errorf("the second-daemon caveat fired on an ordinary healthy pass; got: %s", detail)
	}
}

// TestLoopbackResolutionLine_QuietWhenNothingAnswers. Check 1 already reports a
// stopped daemon. A checklist that says it twice is one people skim past, and
// this row would then be skimmed past on the day it has something new to say.
func TestLoopbackResolutionLine_QuietWhenNothingAnswers(t *testing.T) {
	bind := loopbackProbe{URL: "http://127.0.0.1:10000", Detail: "no answer: connection refused"}
	name := loopbackProbe{URL: "http://localhost:10000", Detail: "no answer: connection refused"}
	status, detail := loopbackResolutionLine(bind, name, 10000)
	if status != "" {
		t.Errorf("status = %q (detail %q), want \"\" — a down daemon is check 1's finding, not this row's", status, detail)
	}
}

// TestLoopbackResolutionLine_NameOnlyWarns is the fourth case: a daemon bound
// to a v6 loopback. It warns rather than fails — the daemon IS answering — and
// must say what breaks anyway, because that configuration is already unsafe.
func TestLoopbackResolutionLine_NameOnlyWarns(t *testing.T) {
	bind := loopbackProbe{URL: "http://127.0.0.1:10000", Detail: "no answer: connection refused"}
	name := loopbackProbe{URL: "http://localhost:10000", Remote: "[::1]:10000", IsPogod: true}
	status, detail := loopbackResolutionLine(bind, name, 10000)
	if status != "warn" {
		t.Fatalf("status = %q, want warn — the daemon answers, so this is not a fail", status)
	}
	for _, want := range []string{"rival pogod", "bind"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail is missing %q; got: %s", want, detail)
		}
	}
}

// TestLoopbackResolutionLine_BothAnswerPasses, and names the address the name
// landed on: "v6 was free and Go fell back" and "pogod is where the name
// points" are different worlds and only one of them is stable.
func TestLoopbackResolutionLine_BothAnswerPasses(t *testing.T) {
	bind := loopbackProbe{URL: "http://127.0.0.1:10000", Remote: "127.0.0.1:10000", IsPogod: true}
	name := loopbackProbe{URL: "http://localhost:10000", Remote: "127.0.0.1:10000", IsPogod: true}
	status, detail := loopbackResolutionLine(bind, name, 10000)
	if status != "pass" {
		t.Fatalf("status = %q, want pass", status)
	}
	if !strings.Contains(detail, "the name reached 127.0.0.1:10000") {
		t.Errorf("detail should name the address the name landed on; got: %s", detail)
	}
}

// ---------------------------------------------------------------------------
// The probe, against real HTTP responders.
// ---------------------------------------------------------------------------

// TestProbePogod_AcceptsOnlyPogodsBody. Every one of these responders would
// pass a "the dial succeeded" probe, and all but the first would pass an "HTTP
// 200" probe. That is the entire point: the interloper in the incident was a
// port-forward returning 200s.
func TestProbePogod_AcceptsOnlyPogodsBody(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    bool
	}{
		{"pogod", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, health.LivenessBody)
		}, true},
		{"port-forward chatter", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "Handling connection for 10000")
		}, false},
		{"empty 200", func(w http.ResponseWriter, r *http.Request) {}, false},
		{"someone else's json", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":"ok"}`)
		}, false},
		{"404 from a different service", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			got := probePogod(srv.URL, testProbeTimeout)
			if got.IsPogod != tc.want {
				t.Errorf("IsPogod = %v, want %v (detail %q)", got.IsPogod, tc.want, got.Detail)
			}
			if !got.IsPogod && got.Detail == "" {
				t.Error("a negative probe recorded no reason")
			}
			if got.Remote == "" {
				t.Error("Remote was not recorded for a connection that succeeded")
			}
		})
	}
}

// TestProbePogod_NothingListening: no connection, no Remote, and a reason.
func TestProbePogod_NothingListening(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // frees the port; the probe now dials a closed one

	got := probePogod(url, testProbeTimeout)
	if got.IsPogod {
		t.Fatal("IsPogod = true against a closed port")
	}
	if got.Remote != "" {
		t.Errorf("Remote = %q for a connection that was never established", got.Remote)
	}
	if !strings.Contains(got.Detail, "no answer") {
		t.Errorf("Detail = %q, want a transport reason", got.Detail)
	}
}

// TestProbePogod_SilentListenerDoesNotHang. A listener that accepts and never
// writes is neither an error nor a success — it is a hang, and it is exactly
// what a half-open forward looks like. Without the client timeout this wedges
// `doctor --check` forever.
func TestProbePogod_SilentListenerDoesNotHang(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold it open, write nothing. Closed by the deferred ln.Close
			// unblocking Accept, plus this handle going out of scope.
			defer conn.Close()
		}
	}()

	start := time.Now()
	got := probePogod("http://"+ln.Addr().String(), 500*time.Millisecond)
	elapsed := time.Since(start)

	if got.IsPogod {
		t.Fatal("IsPogod = true against a listener that wrote nothing")
	}
	if elapsed > 5*time.Second {
		t.Errorf("probe took %s against a silent listener; the client timeout is not being applied", elapsed)
	}
	ln.Close()
	<-done
}

// ---------------------------------------------------------------------------
// Positive controls, against real sockets on both loopback families.
// ---------------------------------------------------------------------------

// loopbackPair binds the SAME port on 127.0.0.1 and on ::1 — two distinct
// sockets, which is the whole hazard — and returns both listeners.
func loopbackPair(t *testing.T) (v4, v6 net.Listener, port int) {
	t.Helper()
	for attempt := 0; attempt < 25; attempt++ {
		l6, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Skipf("no IPv6 loopback on this host, so the shadowing condition cannot be staged: %v", err)
		}
		p := l6.Addr().(*net.TCPAddr).Port
		l4, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			// Something else already holds the v4 side of this port. Retry
			// with a different one rather than reporting a host condition as
			// a code failure.
			l6.Close()
			continue
		}
		return l4, l6, p
	}
	t.Skip("could not find a port free on both 127.0.0.1 and ::1 after 25 attempts")
	return nil, nil, 0
}

// serveOn attaches an HTTP responder to an already-bound listener.
func serveOn(t *testing.T, ln net.Listener, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func pogodHandler(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, health.LivenessBody) }

// interloperHandler answers the way a `kubectl port-forward` does: promptly,
// with a 200, and with something that is not pogod.
func interloperHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Handling connection for "+r.Host)
}

// TestLoopbackResolution_InterloperOnV6Fires is THE positive control.
//
// A real pogod-shaped responder on 127.0.0.1:<port>, a real stranger on
// [::1]:<port>, and the same probe pair `pogo doctor --check` runs. It asserts
// two things:
//
//   - Deterministically, on every host: probing the v6 address directly while
//     the bind address holds pogod produces a FAIL. This is the claim that the
//     check is capable of firing at all, and it does not depend on how this
//     host orders localhost's addresses.
//   - On hosts where `localhost` resolves ::1 first — macOS out of the box,
//     and the configuration the incident happened on — the check fires through
//     the NAME, which is the user-visible bug. Where the resolver prefers IPv4
//     the CLI was never shadowed in the first place, so the test asserts the
//     pass and says which world it observed rather than skipping silently.
func TestLoopbackResolution_InterloperOnV6Fires(t *testing.T) {
	l4, l6, port := loopbackPair(t)
	serveOn(t, l4, pogodHandler)
	serveOn(t, l6, interloperHandler)

	bindURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	v6URL := fmt.Sprintf("http://[::1]:%d", port)
	nameURL := fmt.Sprintf("http://localhost:%d", port)

	bind := probePogod(bindURL, testProbeTimeout)
	if !bind.IsPogod {
		t.Fatalf("the staged pogod on %s did not answer as pogod: %s", bindURL, bind.Detail)
	}

	// (1) Host-independent: the check FIRES on the shadowing condition.
	v6 := probePogod(v6URL, testProbeTimeout)
	if v6.IsPogod {
		t.Fatalf("the staged interloper on %s was read AS pogod — the probe cannot tell them apart, which is the defect this check exists to detect", v6URL)
	}
	status, detail := loopbackResolutionLine(bind, v6, port)
	if status != "fail" {
		t.Fatalf("status = %q with pogod on %s and a stranger on %s; want fail. A check that cannot return fail has never been shown to work", status, bindURL, v6URL)
	}
	if !strings.Contains(detail, "[::1]:"+fmt.Sprint(port)) {
		t.Errorf("the failure does not name the shadowing address; got: %s", detail)
	}

	// (2) Through the name, as a user experiences it.
	name := probePogod(nameURL, testProbeTimeout)
	nameStatus, nameDetail := loopbackResolutionLine(bind, name, port)
	switch {
	case name.Remote == "":
		t.Fatalf("probing %s connected to nothing: %s", nameURL, name.Detail)
	case strings.HasPrefix(name.Remote, "[::1]"):
		t.Logf("this host resolves localhost to ::1 first (reached %s) — the incident's configuration", name.Remote)
		if nameStatus != "fail" {
			t.Fatalf("status = %q, want fail: the name reached the interloper at %s and the check did not notice (%s)",
				nameStatus, name.Remote, nameDetail)
		}
	default:
		t.Logf("this host resolves localhost to IPv4 first (reached %s), so the name is not shadowed here", name.Remote)
		if nameStatus != "pass" {
			t.Fatalf("status = %q, want pass: the name reached pogod at %s (%s)", nameStatus, name.Remote, nameDetail)
		}
	}
}

// TestLoopbackResolution_PassesOnceTheV6PortIsFree is the other half of the
// control: the SAME staging with the interloper removed must go quiet. Without
// it, "fires on the bad case" is compatible with "fires on everything", which
// is a check that gets muted and then ignored.
func TestLoopbackResolution_PassesOnceTheV6PortIsFree(t *testing.T) {
	l4, l6, port := loopbackPair(t)
	// Free the v6 side immediately: this is the healthy world, where nothing
	// holds ::1:<port> and Go's dual-stack fallback carries the name to pogod.
	if err := l6.Close(); err != nil {
		t.Fatalf("closing the v6 listener: %v", err)
	}
	serveOn(t, l4, pogodHandler)

	bind := probePogod(fmt.Sprintf("http://127.0.0.1:%d", port), testProbeTimeout)
	name := probePogod(fmt.Sprintf("http://localhost:%d", port), testProbeTimeout)
	status, detail := loopbackResolutionLine(bind, name, port)
	if status != "pass" {
		t.Fatalf("status = %q (%s) with pogod on 127.0.0.1:%d and ::1:%d free; want pass — this check must not fire on a healthy host",
			status, detail, port, port)
	}
}

// TestLoopbackResolution_QuietWhenTheDaemonIsSimplyDown, end to end: neither
// family holds the port, so the row renders nothing at all.
func TestLoopbackResolution_QuietWhenTheDaemonIsSimplyDown(t *testing.T) {
	l4, l6, port := loopbackPair(t)
	l4.Close()
	l6.Close()

	bind := probePogod(fmt.Sprintf("http://127.0.0.1:%d", port), testProbeTimeout)
	name := probePogod(fmt.Sprintf("http://localhost:%d", port), testProbeTimeout)
	if status, detail := loopbackResolutionLine(bind, name, port); status != "" {
		t.Errorf("status = %q (%s) with nothing on either family; want a silent row", status, detail)
	}
}
