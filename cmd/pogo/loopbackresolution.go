package main

// The "loopback resolution" line in `pogo doctor --check` (mg-e314,
// drellem2/pogo#110).
//
// WHAT IT DETECTS. A process that is not pogod answering on the daemon port
// under the name `localhost`, while pogod itself is healthy on 127.0.0.1. On a
// stock macOS /etc/hosts `localhost` resolves ::1 first; pogod binds
// 127.0.0.1 only (config.DefaultBind), so ::1:<port> is free for anything else
// to claim, and whatever claims it becomes pogod for every consumer that dials
// the name. On 2026-07-31 a `kubectl port-forward` landed there after its IPv4
// bind was refused and took the pogo control plane down for ~20 minutes. pogod
// was up the entire time.
//
// WHY GO'S DUAL-STACK FALLBACK DOES NOT COVER THIS. Happy Eyeballs retries the
// other family on connection REFUSED. The interloper is not refusing — it is
// accepting and answering wrongly, which is indistinguishable from success at
// every layer below this check.
//
// WHY IT IS NOT REDUNDANT WITH THE `ServerURL` FIX. The companion change in
// this commit points the CLI at 127.0.0.1, so the CLI stops being fooled. It
// does not empty the port: everything else that dials the NAME — editor
// integrations (mg-b36f), a stale `ssh -L`, a forward whose far end is not a
// daemon — still is. This check is what makes that condition visible instead
// of silent, and it is why A ships with B rather than after it.
//
// EXACTLY WHAT IT DETECTS, AND WHAT IT DOES NOT. It detects a responder on the
// NAME that is not POGOD-SHAPED. It cannot tell THIS pogod from ANOTHER one.
// A second pogod bound to a v6 loopback emits health.LivenessBody like any
// other, so both probes report pogod and the row passes — likewise an `ssh -L`
// or port-forward whose far end is a real pogod elsewhere. That case is NOT
// covered, and saying otherwise would be the failure this file argues against
// one case over: a reader told the row covers a second daemon reads the green
// line and stops looking. The approved recommendation (mg-e314) promised the
// broader class in the same words; it over-promised, and this is the correction
// rather than a quiet narrowing.
//
// The row does say what it saw. When both probes answer as pogod but the name
// landed somewhere other than the bind address, the pass line names that
// address and states plainly that a dual-stack bind and a second daemon are
// both consistent with it and that this check does not separate them —
// evidence the probe already holds, offered without a verdict it cannot
// support. Separating them needs /version (start_time differs between two
// daemons on one revision) and carries a dual-stack false positive; that is a
// feature, not this row, and it is deliberately not built here.
//
// WHY THE PROBE MATCHES ON A BODY. "The TCP dial succeeded" and even "HTTP
// 200" are properties of any listener, not of pogod. The incident is precisely
// a wrong process passing those tests, so a probe built on them would
// reproduce the defect it is meant to detect, one layer up. The probe requires
// health.LivenessBody — the string pogod's /health emits. Note the limit that
// follows from this and is spelled out above: it identifies pogod-SHAPED
// responders, so every pogod emits it and it cannot single out this one.
//
// WHY THE "NOTHING ANSWERS" CASE RENDERS NOTHING. Check 1 ("pogod running")
// already says the daemon is down, and a checklist that reports every stopped
// daemon twice is a checklist people stop reading. This row speaks only when
// the two addresses DISAGREE, which is the whole of what it knows that check 1
// does not.
//
// WHY FAIL AND NOT WARN. Unlike the launchd and $POGO_HOME rows, the remedy is
// not somebody else's ops decision — an impersonator on the daemon port makes
// every answer the fleet reads untrustworthy, and it is fixed by killing one
// process. That is worth doctor's exit code.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/health"
)

// loopbackCheckName is the doctor checklist row this renders on.
const loopbackCheckName = "loopback resolution"

// loopbackProbeTimeout bounds each of the two probes. Both targets are
// loopback, so a live pogod answers in microseconds and a free port refuses
// immediately; the budget exists for the third case — an interloper that
// accepts the connection and then never writes a response, which is a hang,
// not an error, and would otherwise wedge `doctor --check` indefinitely.
const loopbackProbeTimeout = 2 * time.Second

// loopbackBodyLimit caps how much of a stranger's response is read. The
// identity signal is a short fixed string; anything past it is an unknown
// process's output and there is no reason to hold it in memory.
const loopbackBodyLimit = 4096

// loopbackProbe is one answer to "is pogod behind this URL?".
type loopbackProbe struct {
	// URL is what was dialed, verbatim, so the finding can be reproduced by
	// hand.
	URL string
	// Remote is the ip:port the dial actually LANDED on, which for a name is
	// not derivable from the URL and is the single most useful fact in the
	// failure: it names the address family that is shadowing the daemon.
	// Empty when no connection was established.
	Remote string
	// IsPogod is true only when the responder emitted health.LivenessBody.
	IsPogod bool
	// Detail says why not, when IsPogod is false.
	Detail string
}

// probePogod asks baseURL whether pogod is behind it, and records which
// address the request actually reached.
func probePogod(baseURL string, timeout time.Duration) loopbackProbe {
	p := loopbackProbe{URL: baseURL}

	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/health", nil)
	if err != nil {
		p.Detail = "malformed probe URL: " + err.Error()
		return p
	}
	// The connection hook, not the URL, is how the landed-on address is
	// learned. For "localhost" the URL says nothing about which family the
	// resolver picked, and that is the fact the whole check turns on.
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Conn != nil && info.Conn.RemoteAddr() != nil {
				p.Remote = info.Conn.RemoteAddr().String()
			}
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	// A dedicated client, never http.DefaultClient: the default has no
	// timeout, so a listener that accepts and stays silent hangs doctor.
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		p.Detail = "no answer: " + err.Error()
		return p
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, loopbackBodyLimit))
	if readErr != nil {
		p.Detail = fmt.Sprintf("answered HTTP %d but the body could not be read: %s", resp.StatusCode, readErr)
		return p
	}
	if resp.StatusCode != http.StatusOK {
		p.Detail = fmt.Sprintf("answered HTTP %d, which pogod's /health never does", resp.StatusCode)
		return p
	}
	if !strings.Contains(string(body), health.LivenessBody) {
		p.Detail = fmt.Sprintf("answered HTTP 200 but the body is not pogod's (%s)", loopbackQuoteBody(body))
		return p
	}
	p.IsPogod = true
	return p
}

// loopbackQuoteBody renders a stranger's response for the finding: enough to
// recognise what is on the port, never enough to flood the checklist.
func loopbackQuoteBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "empty body"
	}
	s = strings.Join(strings.Fields(s), " ")
	const max = 120
	if len(s) > max {
		s = s[:max] + "…"
	}
	return "got " + strconv.Quote(s)
}

// loopbackResolutionLine renders one doctor check row from the two probes.
//
// The returned status is "" when the row must not render at all — see the
// "nothing answers" case in the file comment. Callers must skip an empty
// status rather than treating it as a pass.
func loopbackResolutionLine(bind, name loopbackProbe, port int) (status, detail string) {
	switch {
	// pogod on the bind address, something else (or nothing) on the name.
	// This is the incident.
	case bind.IsPogod && !name.IsPogod:
		return "fail", loopbackShadowDetail(bind, name, port)

	// Neither answers as pogod: the daemon is simply down. Check 1 owns that
	// finding; this row stays quiet rather than saying it a second time.
	case !bind.IsPogod && !name.IsPogod:
		return "", ""

	// Reachable over the name but not on 127.0.0.1 — consistent with a `bind`
	// set to a v6 loopback. Warn, not fail: the daemon IS answering, and this
	// configuration is already half-broken today independently of this check
	// (client.daemonBound probes 127.0.0.1, so the spawn-race guard would
	// happily start a rival pogod). The row is the plain-language signal such
	// a user currently gets nowhere.
	case !bind.IsPogod && name.IsPogod:
		return "warn", fmt.Sprintf(
			"pogod answers on %s (reached %s) but NOT on %s: %s. The CLI dials the 127.0.0.1 address, so it will not find this daemon, and the spawn-race guard reads the port as free and may start a rival pogod. Set [server] bind to 127.0.0.1 (a v6 loopback must be written bracketed, \"[::1]\", or pogod refuses to start)",
			name.URL, loopbackOrUnknown(name.Remote), bind.URL, loopbackReason(bind))
	}

	// Both answer as pogod. Say which address the name landed on: it is the
	// difference between "v6 is free and Go fell back" and "pogod is bound
	// where the name points", and only the second is durable.
	detail = fmt.Sprintf("pogod answers on both %s and %s; the name reached %s",
		bind.URL, name.URL, loopbackOrUnknown(name.Remote))

	// The name answered as pogod from an address that is NOT the one pogod was
	// probed on. Two things produce that — a dual-stack bind (0.0.0.0 also
	// binds [::]) and a SECOND pogod — and this row cannot separate them: the
	// body match distinguishes pogod-shaped from not-pogod-shaped, never this
	// pogod from another. Rather than pass in silence or invent a verdict it
	// cannot support, the line states the observation and names its own limit,
	// so a reader chasing a second daemon learns here that they must look
	// elsewhere instead of reading green as an all-clear.
	if name.Remote != "" && bind.Remote != "" && name.Remote != bind.Remote {
		detail += fmt.Sprintf(
			", which is NOT the address pogod was probed on (%s). A dual-stack bind and a SECOND pogod both look like this, and this check does not tell them apart — it distinguishes pogod-shaped responders from other processes, not one pogod from another. If you did not configure a dual-stack bind, compare `/version` start_time on both addresses",
			bind.Remote)
	}
	return "pass", detail
}

// loopbackShadowDetail writes the failure. It has to carry three things a
// reader cannot reconstruct: that pogod is HEALTHY (so nobody restarts it),
// which address is lying, and the one command that names the culprit.
func loopbackShadowDetail(bind, name loopbackProbe, port int) string {
	// Two different failures share this row, and only one of them has a
	// culprit. Sending an operator to hunt a process on a host where the name
	// simply never connected wastes exactly the time this row exists to save,
	// so the remedy is written per branch rather than shared.
	if name.Remote == "" {
		// The name never connected at all while the bind address did. Not the
		// port-forward shape — this is a resolver or /etc/hosts that points
		// the name somewhere pogod is not. There is NO impersonator to find.
		return fmt.Sprintf(
			"%s could not be reached at all (%s), while pogod is HEALTHY on %s — do not restart it. Nothing answered on the name, so there is no process to hunt: this is name resolution, not an impersonator. Check what `localhost` resolves to on this host (`/etc/hosts`, then `dns-sd -q localhost`) — anything that dials the name rather than the address will fail the same way until it resolves to %s",
			name.URL, loopbackReason(name), bind.URL, loopbackOrUnknown(bind.Remote))
	}
	return fmt.Sprintf(
		"%s reached %s, and the process there is NOT pogod (%s), while pogod is HEALTHY on %s — do not restart it. Anything that dials the name (editor integrations, `ssh -L`, scripts) is talking to that process instead of the daemon. Identify it with `%s`, and note that pogod binds 127.0.0.1 only, so the impersonator keeps the port until it is stopped",
		name.URL, name.Remote, loopbackReason(name), bind.URL, loopbackLsofHint(name.Remote, port))
}

// loopbackLsofHint picks the listener query for the family the name actually
// landed on. A -i6TCP query when the shadow is on IPv4 lists nothing, and a
// remedy that prints nothing reads as "the check was wrong".
//
// Its family-agnostic fallback (`-iTCP:<port>`) is currently UNREACHABLE: the
// sole caller is the has-a-culprit branch of loopbackShadowDetail, which runs
// only when name.Remote != "", and a remote address recorded by net always
// splits and parses. It is kept because the alternative is a hint that names
// the WRONG family if a future caller passes an address from somewhere less
// well-formed, and a remedy that lists nothing reads as a wrong check. Stated
// so the empty family is not misread as evidence that this row can emit a
// familyless hint today — the round-1 test that pinned that form was removed
// with the branch it covered.
func loopbackLsofHint(remote string, port int) string {
	family := ""
	if host, _, err := net.SplitHostPort(remote); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			if ip.To4() == nil {
				family = "6"
			} else {
				family = "4"
			}
		}
	}
	return fmt.Sprintf("lsof -nP -i%sTCP:%d -sTCP:LISTEN", family, port)
}

// loopbackReason renders a probe's failure cause, never an empty string: a
// finding whose "why" is blank is a finding a reader has to re-derive.
func loopbackReason(p loopbackProbe) string {
	if p.Detail == "" {
		return "no detail recorded"
	}
	return p.Detail
}

// loopbackOrUnknown names an address that may not have been observed.
func loopbackOrUnknown(remote string) string {
	if remote == "" {
		return "an address that was not recorded"
	}
	return remote
}
