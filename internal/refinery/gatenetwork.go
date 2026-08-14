package refinery

import (
	"fmt"
	"strings"
	"time"
)

// A quality gate that fails because the GATE could not reach the network is not
// a verdict on the branch, and until mg-67c9 the refinery had no way to say so.
//
// # The same root cause, two dispositions, one night
//
// All three of these are from 2026-08-14, inside the intermittent DNS /
// reachability outage tracked in mg-c058. All three were read back out of
// ~/.pogo/refinery-state.json here rather than taken on report:
//
//	mr-d9v89k2tjv1vk5gh573g   stage=fetch          class=infrastructure
//	  repo: /Users/daniel/research/one_third_width_three
//	  ssh: connect to host github.com port 22: Undefined error: 0
//	  -> retried; 10 consecutive fetch failures spanning 13m46s
//	     (04:00:34.8 -> 04:14:20.5); MERGED ON ATTEMPT 11 at 04:16:28.1
//
//	mr-d9v8pgatjv1vk5gh576g   stage=build          class=defect
//	  repo: /Users/daniel/dev/pogo
//	  internal/agent/terminal.go:9:2: nhooyr.io/websocket@v1.8.17:
//	    Get "https://proxy.golang.org/nhooyr.io/websocket/@v/v1.8.17.zip":
//	    dial tcp: lookup proxy.golang.org: no such host
//	  -> signal="stage=build", 1 attempt, NOT retried, merge dead, human round trip
//
// A name that could not be resolved for a few minutes, twice. One healed itself
// on the eleventh attempt; the other stopped and waited for a person.
//
// The two are in DIFFERENT REPOS, and that is stated because mg-ff3a exists
// about exactly this — one queue serves seven repos and a view that cannot say
// which cost three agents an evening. It does not weaken the comparison: the
// classifier is one piece of code serving every lane, and the disposition turned
// on the stage, not on the repo.
//
// # The mechanism, verified rather than inferred
//
// The classifier keys on PIPELINE POSITION for a stage that ran: classifyFailure
// consults verdictStages before any text matching, so `build` and `test` return
// ClassDefect with signal `stage=build` and the reason "the gate ran on this
// tree and returned a verdict — re-running establishes the same fact". Both
// failed attempts above carry exactly that signal and that reason in the
// persisted record, so this is the mechanism and not a reconstruction of one.
//
// Two things sharpen it beyond "the classifier ignores the text":
//
//   - For a gate failure the error text is not merely unconsulted, it is NOT
//     PRESENT. classifyFailure is handed `rawOf(err)`, and for a gate the wrapped
//     error is the one-line summary — `quality gate: ./build.sh failed
//     [internal/agent]: exit status 1`. The module-proxy line lives in the gate's
//     output, which that call site never sees. So a fix that only adds patterns
//     to a table would have matched nothing: the output has to be judged where it
//     exists, which is runQualityGates, and travel to the classifier as a typed
//     error. That is the shape newHostResourceError already uses (mg-b41f) and
//     the shape used here.
//   - `DEFECT` is not just a label. It commits to "re-running establishes the
//     same fact", and that commitment is what suppresses the retry. For a DNS
//     failure the commitment is false, so the label removes the one action that
//     resolves it — the same retry logic that produced the attempt-11 merge above.
//
// # The third instance in the ticket is a DIFFERENT defect, and it is not this one
//
// mg-67c9 also cites mr-d9vah0atjv1vk5gh57c0, whose error reads
//
//	quality gate: ./build.sh failed [test setup failed, not the branch:
//	  PASS: a sandbox HOME that is a symlink to the developer's home:
//	  prints the SETUP FAILURE banner]
//
// and offers it as the sharpest evidence — the gate stated its conclusion in
// plain English and the classifier still said defect. Read back out of the
// refinery state, that sentence is a FALSE POSITIVE OF THE SUMMARISER, not a
// conclusion the gate reached. summarizeGateFailure looked for the substring
// `SETUP FAILURE` anywhere in the output and found it inside a PASSING line of a
// positive control that asserts the banner is printed (scripts/pogo-sandbox_test.sh
// prints `PASS: $1`). No setup failed. The run's actual failure was
// `=== net-control.sh: 14 passed, 4 failed ===`, further down the same output.
//
// Two consequences, and both are load-bearing:
//
//   - A demotion built on the gate's own "not the branch" wording would have
//     fired on that run for a reason that was not true. The specimen offered as
//     proof that gate text can be trusted is a specimen of gate text misleading a
//     reader. The false positive is fixed in gatefailsummary.go; it is NOT wired
//     into the classifier.
//   - A genuine SETUP FAILURE is still not grounds for demotion: a branch can
//     break its own test setup, so "the envelope did not stand up" does not
//     establish that the envelope's collapse was environmental. Only a signal
//     that the GATE'S OWN NETWORK I/O failed does that, which is the whole of
//     what this file recognises.
//
// # Why this may read the gate's output when the network table may not
//
// failureclass.go draws a deliberate boundary: gate output is arbitrary text and
// must not be matched against the network table, because a genuine assertion
// failure that happened to print "connection refused" would then be retried
// forever. That boundary is about RETRY, and unlike ClassHost this DOES cross
// it — a merge classified here is retried. So the boundary is not waved away, it
// is replaced with a narrower one:
//
//	the network wording and a Go MODULE-FETCH MARKER must appear on the SAME LINE.
//
// A test asserting on `connection refused` does not print `proxy.golang.org` on
// that line; the toolchain's own fetch failure always does, because the marker
// is the URL it was fetching. The conjunction — not the size of either table —
// is what carries the safety, which is why the network vocabulary below can
// afford to be broader than the strictly-measured discipline hostResourceSignals
// follows. Counted over the 89 merge requests retained in
// ~/.pogo/refinery-state.json on 2026-08-14:
//
//	proxy.golang.org        3 hits / 1 MR    <- mr-d9v8pgatjv1vk5gh576g, the case above
//	dial tcp                2 hits / 1 MR    <- same MR
//	no such host            2 hits / 1 MR    <- same MR
//	sum.golang.org          0 hits
//	go: downloading         0 hits
//	i/o timeout             0 hits
//	tls handshake timeout   0 hits
//	connection refused      0 hits
//	connection reset by peer 0 hits
//	context deadline exceeded 0 hits
//
// So there is not one specimen in this fleet's corpus of a marker line that was
// anything other than a real fetch failure — and, separately, not one specimen
// of a network wording appearing in gate output for any other reason either.
// Those counts are a LOWER BOUND: the gate_output persisted on a merge request
// is capped to 8 KiB with its middle elided (gateoutputcap.go), so anything that
// fell in an elided middle is uncounted. That is also why the classification
// happens in runQualityGates against the FULL output, before the cap.
//
// # Can this mask a real defect as flaky?
//
// pm-pogo's condition on the fix was that text may DEMOTE and never PROMOTE, so
// that the change "can only ever add retries to things that would otherwise have
// stopped dead, and can never mask a real defect as flaky". A gate output can
// carry BOTH a genuine test failure and a module-fetch failure — the 2026-08-14
// specimen does, its shell suites having failed downstream of the same outage —
// and this fires on the network line first, which looks like exactly the masking
// that was ruled out.
//
// It is a DELAY and not an erasure, and the reason is the retry itself: a retry
// re-runs the whole gate, and a genuine defect fails it again. Once the network
// is back the second run produces the failure without a network line in it, the
// stage table classifies it DEFECT, and the author is told. The masking would be
// real if this class skipped the gate — it does not.
//
// What it costs when the outage outlives the budget is one round trip: the merge
// is reported INFRASTRUCTURE while a real defect was also present, and the author
// finds it on resubmission. That is the same cost the fetch-stage campaign
// already pays for a branch whose gates never ran, and it is paid in slower bad
// news rather than a wrong accusation.
//
// # What this does not cover
//
// The marker table is Go's module-fetch vocabulary. A gate that pulls a
// container, fetches a remote schema, or curls an API is NOT recognised, and
// will still come out DEFECT. That is a real limit and it is left open rather
// than papered over with a marker like `https://`, which would match any test
// printing a URL and hand the conjunction back for nothing. When such a gate
// appears on this fleet, the specimen it produces is what should extend the
// table — the same rule gatehostresource.go applies to its own.

// gateFetchMarkers are the wordings that identify a line as the Go toolchain
// reporting on a MODULE FETCH. One of these must be on the same line as a
// network wording before anything here fires.
var gateFetchMarkers = []string{
	"proxy.golang.org",
	"sum.golang.org",
	"index.golang.org",
	"go: downloading",
	"go: module ",
	"verifying module",
}

// `goproxy` is deliberately NOT a marker, and the reason is a specimen in this
// repo. scripts/pogo-sandbox_test.sh prints `GOPROXY=off` on several of its own
// output lines — it runs the toolchain pin proof with the proxy disabled — so
// the marker would be live in gate output on every clean run, waiting only for a
// network wording to land beside it. It also names the wrong condition: `module
// lookup disabled by GOPROXY=off` is a configuration, not an outage.

// goNetworkSignals are the Go runtime's and net/http's wordings for "this did
// not reach the far end". They are separate from networkSignals because that
// table is git's and curl's vocabulary — `dial tcp` and `no such host` never
// appear in it, which is exactly why the 2026-08-14 module failure would not
// have matched it even if it had been consulted.
var goNetworkSignals = []string{
	"no such host",
	"dial tcp",
	"i/o timeout",
	"tls handshake timeout",
	"server misbehaving",
	"context deadline exceeded",
	"unexpected eof",
	"network is unreachable",
	"connection reset",
	"connection refused",
}

// lineReportsNetworkWording returns the network wording found on one
// already-lowercased line, or "". It consults the Go vocabulary first and then
// git's, so a marker line carrying either is recognised and the two tables
// cannot drift into disagreeing about what a network failure looks like.
func lineReportsNetworkWording(line string) string {
	for _, p := range goNetworkSignals {
		if strings.Contains(line, p) {
			return p
		}
	}
	for _, s := range networkSignals {
		if strings.Contains(line, s.pattern) {
			return s.pattern
		}
	}
	return ""
}

// lineReportsFetchMarker returns the module-fetch marker found on one
// already-lowercased line, or "".
func lineReportsFetchMarker(line string) string {
	for _, m := range gateFetchMarkers {
		if strings.Contains(line, m) {
			return m
		}
	}
	return ""
}

// outputReportsGateNetworkFailure is the single reading of the conjunction,
// shared by newGateNetworkError and by summarizeGateFailure so those two cannot
// end up contradicting each other inside one report — the same reason
// outputReportsConflict and outputReportsHostResourceExhaustion exist.
//
// It must be given the FULL gate output, for the reason stated in the package
// comment above: the persisted copy is capped with its middle elided.
//
// A REMEDY IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT IT REMEDIES. The
// summariser bug this ticket uncovered — a substring scan landing inside a
// PASSING control line that merely QUOTED the wording it was looking for — is
// available to this function in exactly the same form, and more plausibly here:
// this repo's own net-control.sh suite exists to test network-probe behaviour,
// so a line like `PASS: retries on dial tcp: lookup proxy.golang.org: no such
// host` is a thing it could print tomorrow. The same guard is therefore applied
// here (isHarnessVerdictLine), and it costs nothing on the real specimen: the Go
// toolchain's fetch errors open with a source position or a module path, never
// with `PASS:`/`FAIL:`.
func outputReportsGateNetworkFailure(output string) (signal, marker, sample string, count int, ok bool) {
	for _, raw := range strings.Split(output, "\n") {
		if isHarnessVerdictLine(strings.TrimSpace(raw)) {
			continue
		}
		line := strings.ToLower(raw)
		m := lineReportsFetchMarker(line)
		if m == "" {
			continue
		}
		sig := lineReportsNetworkWording(line)
		if sig == "" {
			continue
		}
		count++
		if !ok {
			signal, marker, sample, ok = sig, m, strings.TrimSpace(raw), true
		}
	}
	return signal, marker, sample, count, ok
}

// gateNetworkError is a gate that failed because ITS OWN network I/O failed.
type gateNetworkError struct {
	Gate string
	// Signal is the network wording matched, Marker the module-fetch wording
	// that was on the same line. Both are recorded so the classification can be
	// audited from the record rather than taken on trust.
	Signal      string
	Marker      string
	Occurrences int
	Sample      string
	Err         error
}

func (e *gateNetworkError) Error() string {
	return fmt.Sprintf("%s failed: the GATE could not reach the network, not the branch (%q with %q on the same line, x%d): %s: %v",
		e.Gate, e.Signal, e.Marker, e.Occurrences, e.Sample, e.Err)
}

func (e *gateNetworkError) Unwrap() error { return e.Err }

// newGateNetworkError builds the error for a gate whose own network I/O failed,
// or returns nil when the output says nothing of the sort.
func newGateNetworkError(gate, output string, err error) *gateNetworkError {
	sig, marker, sample, n, ok := outputReportsGateNetworkFailure(output)
	if !ok {
		return nil
	}
	return &gateNetworkError{
		Gate:        gate,
		Signal:      sig,
		Marker:      marker,
		Occurrences: n,
		Sample:      truncate(sample, maxSummaryLen),
		Err:         err,
	}
}

// Retry bounds for a gate whose own network I/O failed. They are SEPARATE from
// networkMaxAttempts, and the separation is not decorative: a network-class
// retry at the fetch stage costs a git command, while a retry here costs A
// WHOLE GATE RUN on the single serial slot every queued merge waits behind. The
// two gate-bearing attempts measured on 2026-08-14 took 6m18s
// (mr-d9v8pgatjv1vk5gh576g) and 12m46s (mr-d9vah0atjv1vk5gh57c0), read from
// their START-to-DONE timestamps — so those are UPPER BOUNDS on the gate itself,
// which shares them with the fetch and rebase before it. Spending the 28-attempt
// network budget here would hold one repo's lane for something on the order of
// six hours; the merge it is trying to rescue is worth far less than that.
//
// So the trade is stated rather than maximised:
//
//   - 4 attempts (3 retries) with backoffs of 1m, 5m, 10m. Against a network
//     that never comes back that holds the lane for 16m of sleep plus at most
//     three more gate runs — the same order as the 50m the fetch-stage campaign
//     already costs, and bounded by the same networkRetryBudget clock.
//   - The elapsed coverage is BETTER than 16m, because each retry re-runs the
//     gate and that run is itself minutes of elapsed time during which the
//     outage can end. It is still WORSE than the fetch campaign's, and a 35m
//     outage — the largest measured on this host (see the sizing note in
//     failureclass.go, and note it has no established upper bound) — can still
//     exhaust it.
//
// THE CLASS IS THE LARGER HALF OF THE FIX, NOT THE RETRY. When this budget is
// spent the failure is still reported INFRASTRUCTURE: nobody is sent to read the
// branch, the author's consecutive-failure streak is untouched
// (countsAgainstAuthor), and the triage note says resubmit. That is true whether
// or not any retry happened to land inside the outage.
//
// Package vars rather than consts so tests can compress the schedule.
var (
	gateNetworkRetryBackoff = []time.Duration{1 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	// gateNetworkMaxAttempts bounds gate-network attempts, retries included.
	gateNetworkMaxAttempts = 4
)

// gateNetworkBackoffFor returns the delay before the nth gate-network retry (n
// starting at 1), clamped to the last entry in the schedule.
func gateNetworkBackoffFor(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > len(gateNetworkRetryBackoff) {
		n = len(gateNetworkRetryBackoff)
	}
	return gateNetworkRetryBackoff[n-1]
}
