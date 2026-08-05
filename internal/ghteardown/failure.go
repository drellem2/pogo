package ghteardown

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// # Why a failed lookup needs a CLASS (mg-dd22)
//
// The package doc explains why a failed lookup must never read as "closed".
// That discipline held. What it did not do is distinguish the two very
// different reasons a lookup can fail, and on 2026-08-04 that cost the detector
// its entire signal:
//
//	gh issue view drellem2/pogo#NN failed: error connecting to api.github.com
//	check your internet connection or https://githubstatus.com
//
// One network blip, one sample, and all 12 carriers came back indeterminate.
// Re-running them by hand minutes later resolved every one — and the batch was
// NOT uniform: 6 were clean, and 6 were exactly the finding this package exists
// to produce (done carrier, issue still OPEN). The blip converted six real
// findings into twelve units of noise. It recurred 15 hours later, 13 for 13.
//
// Two separate defects, and the second is the worse one:
//
//  1. The failure was never RETRIED. On a host whose network is ~50%
//     intermittent (mg-0ffc), a detector that treats one transient sample as a
//     terminal verdict produces a full-batch no-verdict on a regular basis.
//
//  2. The failure was reported in the SAME SHAPE as a determination. "I asked
//     GitHub and the answer does not tell me whether this issue is closed" and
//     "I never reached GitHub" both rendered as `indeterminate`, so a masked
//     finding was indistinguishable from a real one. A reader who sees two
//     identical "N indeterminate" mails in a row learns to skip them, which is
//     precisely when the real finding arrives.
//
// A FailureClass answers both. It says whether re-running is likely to produce
// a different answer (so a blip is retried and a deterministic failure is not —
// re-running a repeatable failure only reproduces it and burns the window), and
// it says whether the failure was in the INSTRUMENT or in the SUBJECT (so the
// two never share a bucket again).

// FailureClass names why a lookup produced no usable issue state.
type FailureClass string

const (
	// FailureNone is the zero value: no failure.
	FailureNone FailureClass = ""

	// FailureNetwork: the lookup never reached GitHub — DNS, TCP, TLS, timeout.
	// GitHub has said NOTHING about this issue, so nothing has been learned
	// about the carrier. The only class that is retried.
	FailureNetwork FailureClass = "network"

	// FailureAuth: gh has no usable credential (mg-03ea's class — the launchd
	// GH_TOKEN gap). An instrument failure, but a perfectly repeatable one: the
	// credential will not appear between two attempts seconds apart, so
	// retrying only multiplies the same error.
	FailureAuth FailureClass = "auth"

	// FailureRateLimit: GitHub answered and refused to serve. Instrument, and
	// NOT retried inside a run — a primary rate limit resets on the hour and a
	// secondary one wants minutes, both far longer than any backoff worth
	// holding a sample open for. The next scheduled sample is the retry.
	FailureRateLimit FailureClass = "rate_limit"

	// FailureSubject: GitHub answered ABOUT THIS REF and the answer yields no
	// usable state — no such issue, no such repo, a state we do not model, a
	// malformed `gh:` line. This is the genuine indeterminate: a determination
	// about the carrier, reached by a working instrument, that re-running
	// reproduces exactly.
	FailureSubject FailureClass = "subject"

	// FailureUnclassified: the error text matched nothing known.
	//
	// It is treated as an INSTRUMENT failure, not a subject one. That is the
	// deliberate direction to fail: calling an unrecognised failure a
	// determination about the carrier is precisely the collapse mg-dd22 was
	// filed about, and the cost of being wrong the other way is one carrier
	// listed under the loud heading instead of the quiet one — with its error
	// text printed either way. It is not retried, because nothing establishes
	// that a re-run would differ.
	FailureUnclassified FailureClass = "unclassified"
)

// Retryable reports whether re-attempting the same lookup seconds later is
// likely to produce a different answer. Only the network class qualifies: every
// other failure here is repeatable by construction, and re-running a repeatable
// failure reproduces it while spending the window.
func (c FailureClass) Retryable() bool { return c == FailureNetwork }

// Instrument reports whether the failure was in our ability to ask rather than
// in the answer. Everything that is not positively known to be about the
// subject counts as an instrument failure — see FailureUnclassified.
func (c FailureClass) Instrument() bool {
	return c != FailureNone && c != FailureSubject
}

// Describe renders the class as a short phrase for a human-facing report. It
// does NOT repeat the class name — callers print that alongside — so the two
// together read as a label and its explanation rather than as a stutter.
func (c FailureClass) Describe() string {
	switch c {
	case FailureNetwork:
		return "the lookup never reached GitHub, and survived the retries"
	case FailureAuth:
		return "gh has no usable credential (the mg-03ea class)"
	case FailureRateLimit:
		return "GitHub answered and refused to serve; the next sample is the retry"
	case FailureSubject:
		return "GitHub answered about this ref, and the answer is not a usable state"
	case FailureUnclassified:
		return "the failure text matched no known class, so it is treated as ours"
	default:
		return string(c)
	}
}

// LookupError is an error that carries its own class, so classification is
// structural wherever the failure is raised by code we own and text-matched
// only where the message comes from `gh` itself.
type LookupError struct {
	Class FailureClass
	Msg   string
}

func (e *LookupError) Error() string { return e.Msg }

func lookupErr(class FailureClass, format string, args ...any) error {
	return &LookupError{Class: class, Msg: fmt.Sprintf(format, args...)}
}

// ClassifyLookupError determines the class of a lookup failure: structurally if
// the error carries one, by reading gh's message otherwise.
func ClassifyLookupError(err error) FailureClass {
	if err == nil {
		return FailureNone
	}
	var le *LookupError
	if errors.As(err, &le) && le.Class != FailureNone {
		return le.Class
	}
	return classifyMessage(err.Error())
}

// Marker sets for reading gh's own error prose. Text matching is not something
// to be proud of, but it is the only signal available: `gh issue view` exits 1
// for a nonexistent issue, a nonexistent repo, an expired credential and an
// unplugged cable alike, so the exit code cannot separate them and the message
// is all that differs. mg-dd22 was filed precisely because the two had to be
// hand-separated by reading prose after the fact.
//
// Order matters, and it is checked most-specific first. A message that matches
// nothing is FailureUnclassified rather than being forced into the nearest fit.
var (
	subjectMarkers = []string{
		"could not resolve to a",   // GraphQL: no such issue / no such repository
		"could not resolve to an",  //
		"no issues found",          //
		"http 404",                 // see the ambiguity note below
		"issue not found",          //
		"could not find any issue", //
		"unrecognised state",       // raised by GHLookup itself
		"no state in response",     //
		"unresolvable gh ref",      //
		"unparseable output",       // gh's shape changed; see the note in classifyMessage
	}
	authMarkers = []string{
		"gh auth login",
		"gh_token",
		"github_token",
		"bad credentials",
		"http 401",
		"authentication failed",
		"requires authentication",
		"not logged into",
		"no such credential",
	}
	rateMarkers = []string{
		"rate limit",
		"secondary rate",
		"abuse detection",
		"was submitted too quickly",
	}
	networkMarkers = []string{
		"error connecting to",
		"check your internet connection",
		"dial tcp",
		"no such host",
		"name resolution",
		"i/o timeout",
		"tls handshake",
		"connection refused",
		"connection reset",
		"network is unreachable",
		"host is unreachable",
		"context deadline exceeded",
		"client timeout",
		"timeout awaiting",
		"server misbehaving",
		"unexpected eof",
		"proxyconnect",
		"broken pipe",
	}
)

// classifyMessage reads gh's prose.
//
// Two honest limits, stated rather than hidden:
//
//   - HTTP 404 is read as SUBJECT. GitHub also returns 404 for a private repo
//     the caller cannot see, so an under-privileged token can produce a subject
//     verdict for what is really an instrument problem. It is not silent: a
//     whole batch of 404s trips Report.InstrumentFailure, which is the case
//     that matters.
//   - An unparseable `gh` response is read as SUBJECT rather than instrument.
//     It means gh answered and we could not read it — a repeatable failure that
//     no retry helps, and one whose message names itself unambiguously.
func classifyMessage(msg string) FailureClass {
	m := strings.ToLower(msg)
	for _, group := range []struct {
		markers []string
		class   FailureClass
	}{
		{subjectMarkers, FailureSubject},
		{authMarkers, FailureAuth},
		{rateMarkers, FailureRateLimit},
		{networkMarkers, FailureNetwork},
	} {
		for _, marker := range group.markers {
			if strings.Contains(m, marker) {
				return group.class
			}
		}
	}
	return FailureUnclassified
}

// Retry defaults for the production lookup.
const (
	// DefaultLookupAttempts is how many times one issue is looked up before a
	// network-class failure is given up on. Three, not two: the observed
	// outages on this box are seconds-to-minutes brownouts rather than clean
	// on/off transitions, and two attempts 2s apart sample almost the same
	// instant. Three spans ~6s of wall clock per carrier.
	DefaultLookupAttempts = 3
	// DefaultLookupBackoff is the first sleep between attempts; it doubles.
	// Deliberately short. This runs inside a heartbeat sample over every done
	// carrier, and a scan that stalls for minutes on a genuinely dead network
	// is its own failure — the point is to ride out a blip, not to wait out an
	// outage. An outage that outlives the backoff is reported as one.
	DefaultLookupBackoff = 2 * time.Second
)

// RetryingLookup wraps a lookup with the production retry policy. This is what
// the Watcher binds by default and what `pogo check-teardown` calls, so both
// paths ride out a blip identically.
func RetryingLookup(lookup LookupFunc) LookupFunc {
	return Retrying(lookup, DefaultLookupAttempts, DefaultLookupBackoff, time.Sleep)
}

// Retrying re-attempts a lookup whose failure is network-class, with doubling
// backoff, and returns the first conclusive answer.
//
// Only network-class failures are retried. That restriction is the whole design
// and not an optimisation: an expired credential, a missing issue and a rate
// limit are all perfectly repeatable, so re-running them produces the identical
// error N times while spending N times the window — and, worse, dresses a
// deterministic failure in the language of a flake.
//
// When retries are exhausted the LAST error is returned, wrapped so the report
// records that the failure survived the retries. That wrapping matters: an
// error that says "unresolved after 3 attempts" cannot be mistaken for a single
// unlucky sample, which is exactly the misreading that let 2026-08-04's batch
// look like twelve broken carriers.
//
// sleep is a seam so tests exercise the backoff without spending it.
func Retrying(lookup LookupFunc, attempts int, backoff time.Duration, sleep func(time.Duration)) LookupFunc {
	if attempts < 1 {
		attempts = 1
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	return func(repo string, number int) (IssueState, error) {
		var (
			state IssueState
			err   error
			wait  = backoff
			slept time.Duration
		)
		for attempt := 1; ; attempt++ {
			state, err = lookup(repo, number)
			if err == nil {
				return state, nil
			}
			class := ClassifyLookupError(err)
			if !class.Retryable() {
				// A repeatable failure. Returning immediately is the point:
				// the answer will not change and the window is not free.
				return state, err
			}
			if attempt >= attempts {
				break
			}
			sleep(wait)
			slept += wait
			if wait > 0 {
				wait *= 2
			}
		}
		return StateUnknown, &LookupError{
			Class: FailureNetwork,
			Msg: fmt.Sprintf("%v (network-class failure, still failing after %d attempts spanning %s of backoff)",
				err, attempts, slept),
		}
	}
}
