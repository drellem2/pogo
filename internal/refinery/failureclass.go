package refinery

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Failure classification and the retry policy built on it (mg-e5c2).
//
// On 2026-08-05 thirty-one merge requests failed across three windows, every
// one of them at the fetch step, and the refinery retried NONE of them. The
// cause was established afterwards with a control: en1 lost its DHCP lease and
// mDNSResponder suppressed every unicast query —
//
//	DetermineUnicastQuerySuppression: Query suppressed for <github.com> (no DNS service)
//
// — with suppressed-query counts of 54/44/73 inside the three failure windows
// and ZERO in every window where merges succeeded. A suppressed query fails
// instantly, which is why a single-slot queue could drain twelve requests to
// `failed` inside one second.
//
// Two policies come out of that incident and are encoded here.
//
// # 1. What may be retried — pm-pogo's ruling (mg-0d70), applied one layer up
//
//	Retry a failure that establishes NOTHING about the tree; do not retry one
//	that establishes a FACT. Concretely: would re-running plausibly give a
//	different answer, for a reason UNRELATED TO THE CODE?
//
// A fetch that cannot resolve a hostname never reached the branch, the base or
// the gate, so it has no opinion about any of them — retry. A quality gate that
// ran and returned RED has an opinion, and it is exactly as true in thirty
// seconds — do not retry. Credentials the remote refused are refused again on
// the next attempt, so that is not retried either, even though it establishes
// nothing about the tree: the two halves of the ruling are separate axes here,
// which is why FailureClass and retryability are separate fields.
//
// # 2. Record the transport and the raw error, per attempt, never a summary
//
// Of the 31 failures, 20 were ssh reporting `Undefined error: 0` and 11 were
// HTTPS reporting `Could not resolve host: github.com`, interleaved ~200ms
// apart in the same bursts. The HTTPS half named the cause outright. Several
// readers — including the mayor — sampled only the ssh subset, reasoned from
// errno-0 semantics, and produced two confident WRONG mechanisms over several
// hours (a "not really a network failure" reading, and a refuted fd-leak).
//
// A single-transport view is how that happened, so every failing attempt here
// records the transport it was made over, the git command as invoked, and git's
// combined output VERBATIM. Nothing downstream reduces those to one normalised
// summary line, because the normalised line is what both wrong mechanisms were
// built out of.
//
// # Relation to internal/ghteardown/failure.go
//
// That file applies the same ruling to the same host's network, landed the same
// day (mg-dd22), and is deliberately NOT shared code. It classifies the `gh`
// CLI's vocabulary ("error connecting to api.github.com") against a lookup that
// either reaches GitHub or does not; this file classifies git's ssh and HTTPS
// vocabularies against a seven-step pipeline where the STAGE decides as much as
// the wording. Folding them into one table would mean one set of patterns
// answering two questions, and the failure mode of that is the one this ticket
// is about — a classification made from evidence that does not belong to the
// case. If a third component needs this, that is the point to extract a shared
// core; two is not yet a pattern.
type FailureClass string

const (
	// ClassInfrastructure marks a failure that establishes nothing about the
	// submitted branch: the network, the remote's credentials check, or the
	// refinery's own checkout. It is the discriminator a coordinator triaging a
	// queue needs — thirty-one of these invited dispatching thirty-one fixes for
	// defects that do not exist. Do not dispatch a code fix for one.
	ClassInfrastructure FailureClass = "infrastructure"
	// ClassContention marks a lost race with another merge — the target moved
	// between our rebase and our push. Establishes nothing about the branch and
	// is retried; this is the refinery's original (pre-mg-e5c2) retry class.
	ClassContention FailureClass = "contention"
	// ClassDefect marks a failure that establishes a FACT about the submitted
	// branch: a gate verdict, a rebase conflict, a refused commit message. This
	// is the only class that invites dispatching a fix.
	ClassDefect FailureClass = "defect"
	// ClassUnclassified marks a failure the refinery could not place. It is
	// retried on a small budget — an unrecognised failure might plausibly differ
	// on a retry — and it is reported as unclassified rather than silently
	// folded into either of the two classes that carry a triage instruction.
	ClassUnclassified FailureClass = "unclassified"
)

// TriageNote returns the one-line instruction a coordinator needs on seeing
// this class in a status. The status is what a queue reader sees first; before
// mg-e5c2 the only thing separating "your code is wrong" from "the network
// hiccuped" was a line of error text deep inside `pogo refinery show`.
func (c FailureClass) TriageNote() string {
	switch c {
	case ClassInfrastructure:
		return "INFRASTRUCTURE — establishes nothing about the branch. Resubmit; do NOT dispatch a fix."
	case ClassContention:
		return "CONTENTION — lost a race with another merge. Resubmit; do NOT dispatch a fix."
	case ClassDefect:
		return "DEFECT — establishes a fact about the branch. A fix is warranted."
	case ClassUnclassified:
		return "UNCLASSIFIED — the refinery could not establish which class this is. Read the raw error below before reacting."
	}
	return ""
}

// AttemptFailure is the record of one failing attempt. One is kept per attempt,
// including the attempts that were retried and succeeded afterwards, so
// "failed once" and "failed after 3 attempts" are different records and not
// only different prose.
type AttemptFailure struct {
	Attempt int       `json:"attempt"`
	Stage   string    `json:"stage"`
	Time    time.Time `json:"time"`
	// Transport is how this attempt talked to the remote — ssh, https, file, or
	// unknown — measured from the clone's origin URL, and falling back to the
	// wording of the error itself when the URL cannot be read. It is recorded
	// per attempt even though a clone has one origin, because the view that cost
	// several hours on 2026-08-05 was a view of one transport that did not say
	// so.
	Transport string `json:"transport,omitempty"`
	// Remote is the origin URL as configured at the moment of the failure.
	Remote string `json:"remote,omitempty"`
	// Command is the git invocation that failed, as invoked.
	Command string `json:"command,omitempty"`
	// RawError is git's combined output plus the wrapped error, VERBATIM. It is
	// never summarised, truncated to one line, or normalised — see the package
	// comment above for what that cost.
	RawError string `json:"raw_error"`
	// Class is the classification, and Signal names the evidence that decided
	// it (the matched wording, or the stage) so the classification can be
	// audited rather than taken on trust.
	Class  FailureClass `json:"class"`
	Signal string       `json:"signal,omitempty"`
	// Retried records whether a further attempt followed this one. When it did
	// not, NotRetriedReason says why — requirement 3 of mg-e5c2: the absence of
	// a retry must be legible rather than looking like a policy that does not
	// exist.
	Retried          bool    `json:"retried"`
	NotRetriedReason string  `json:"not_retried_reason,omitempty"`
	BackoffSeconds   float64 `json:"backoff_seconds,omitempty"`
}

// Line renders one attempt as a single log/mail line. The transport and the
// class come before the raw error so a reader scanning a column of these can
// see that both transports are present without reading any of them.
func (a AttemptFailure) Line() string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "attempt %d  stage=%s  class=%s  transport=%s",
		a.Attempt, a.Stage, a.Class, transportOrUnknown(a.Transport))
	if a.Signal != "" {
		fmt.Fprintf(b, "  signal=%q", a.Signal)
	}
	if a.Retried {
		if a.BackoffSeconds > 0 {
			fmt.Fprintf(b, "  retried after %s", a.Backoff().Round(time.Millisecond))
		} else {
			b.WriteString("  retried immediately")
		}
	} else {
		fmt.Fprintf(b, "  NOT RETRIED — %s", a.NotRetriedReason)
	}
	return b.String()
}

// Backoff is the delay that was slept before the next attempt.
func (a AttemptFailure) Backoff() time.Duration {
	return time.Duration(a.BackoffSeconds * float64(time.Second))
}

func transportOrUnknown(t string) string {
	if t == "" {
		return "unknown"
	}
	return t
}

// disposition is the classifier's answer for a single failure.
type disposition struct {
	Class FailureClass
	// Retryable answers the ruling's question: would re-running plausibly give a
	// different answer, for a reason unrelated to the code?
	Retryable bool
	// Signal names the evidence that decided the class.
	Signal string
	// Reason is filled when Retryable is false and states, in one sentence, why
	// re-running would give the same answer. It becomes "not retryable: ..." in
	// the record.
	Reason string
}

// networkSignal is one measured wording that means the attempt never reached
// the tree. Transport names which client's vocabulary the wording belongs to —
// recorded so a reader can tell at a glance that both halves of a mixed-
// transport incident are represented.
type networkSignal struct {
	pattern   string
	transport string
}

// networkSignals are matched case-insensitively against git's combined output.
//
// The ssh entries include bare `connect to host` on purpose: on 2026-08-05
// macOS ssh reported the DNS failure as
//
//	ssh: connect to host github.com port 22: Undefined error: 0
//
// i.e. errno 0 — connect() failed and the error slot was never populated. That
// wording carries no cause at all, and reasoning about what errno 0 "must mean"
// is precisely what produced two wrong mechanisms. It is classified by the step
// it aborted (a transport step that never reached the tree), not by the errno.
var networkSignals = []networkSignal{
	// DNS — the 2026-08-05 cause, in both transports' vocabularies.
	{"could not resolve host", "https"},
	{"could not resolve hostname", "ssh"},
	{"name or service not known", "any"},
	{"nodename nor servname provided", "any"},
	{"temporary failure in name resolution", "any"},
	{"no address associated with hostname", "any"},
	{"no dns service", "any"},

	// Transport establishment.
	{"connect to host", "ssh"},
	{"failed to connect to", "https"},
	{"connection refused", "any"},
	{"connection reset by peer", "any"},
	{"connection timed out", "any"},
	{"operation timed out", "any"},
	{"network is unreachable", "any"},
	{"network is down", "any"},
	{"no route to host", "any"},
	{"ssh_exchange_identification", "ssh"},
	{"kex_exchange_identification", "ssh"},
	{"broken pipe", "any"},

	// Mid-transfer loss.
	{"the remote end hung up unexpectedly", "any"},
	{"early eof", "any"},
	{"rpc failed", "https"},
	{"index-pack failed", "any"},
	{"unexpected disconnect", "any"},

	// TLS.
	{"gnutls_handshake", "https"},
	{"ssl_read", "https"},
	{"ssl connect error", "https"},
	{"openssl", "https"},

	// The far end answered but could not serve us. A 5xx or a rate limit says
	// nothing about the branch and is the textbook retry.
	{"the requested url returned error: 5", "https"},
	{"the requested url returned error: 429", "https"},
	{"internal server error", "https"},
	{"bad gateway", "https"},
	{"service unavailable", "https"},
	{"gateway time-out", "https"},
	{"gateway timeout", "https"},
}

// credentialSignals mean the remote ANSWERED and refused the identity this box
// offered. That establishes nothing about the tree — so it is infrastructure,
// not a defect, and must not send anyone to read the branch — but re-running it
// asks the same question and gets the same answer, so it is not retried.
var credentialSignals = []string{
	"permission denied (publickey",
	"could not read username",
	"could not read password",
	"authentication failed",
	"invalid username or password",
	"terminal prompts disabled",
	"support for password authentication was removed",
	"repository not found",
	"the requested url returned error: 403",
	"the requested url returned error: 401",
	"access denied",
}

// conflictSignals mean the rebase reached the tree and the tree disagreed —
// exactly as true on the next attempt.
var conflictSignals = []string{
	"could not apply",
	"conflict (content)",
	"conflict (add/add)",
	"conflict (modify/delete)",
	"conflict (rename",
	"patch failed",
	"needs merge",
}

// verdictStages are the stages at which something RAN against the tree and
// returned an answer about it. A failure here is a fact, per the ruling.
//
// Deliberate boundary: a quality gate that failed because IT could not reach
// the network (a module download, say) is classified a defect here, because the
// gate ran and reported. Retrying on gate output would mean pattern-matching
// arbitrary test output for network wording, which is how a genuine assertion
// failure that happens to print "connection refused" would be retried forever.
// The gate's own output is preserved verbatim for the reader who needs to make
// that call.
var verdictStages = map[string]string{
	"build":             "the build gate ran on this tree and returned a verdict",
	"test":              "the test gate ran on this tree and returned a verdict",
	"closing-ref-check": "a commit message on this branch would close a GitHub issue",
}

// classifyFailure places a failed attempt and answers whether re-running it
// could plausibly differ for a reason unrelated to the code.
//
// stage is the pipeline step that failed; raw is git's combined output verbatim
// (empty when the failure did not come from git); err is the wrapped error.
func classifyFailure(stage string, raw string, err error) disposition {
	// A stage that RAN against the tree is judged by that, before any text
	// matching. Gate output is arbitrary and may contain any wording at all;
	// letting it reach the network table would make a red test retryable
	// whenever it happened to print "connection refused".
	if reason, ok := verdictStages[stage]; ok {
		return disposition{
			Class:     ClassDefect,
			Retryable: false,
			Signal:    "stage=" + stage,
			Reason:    reason + " — re-running establishes the same fact",
		}
	}

	// The refinery's own checkout was left modified by a gate. Not the branch's
	// doing, and a retry replays the same writes.
	var dirtErr *gateDirtError
	if errors.As(err, &dirtErr) {
		return disposition{
			Class:     ClassInfrastructure,
			Retryable: false,
			Signal:    "gate-dirt",
			Reason:    "a gate wrote to tracked files in the refinery's own checkout; a retry replays the same writes, and the submitted branch did not cause it",
		}
	}

	hay := strings.ToLower(raw)
	if hay == "" && err != nil {
		hay = strings.ToLower(err.Error())
	}

	// Credentials before network: "Permission denied (publickey)" contains no
	// network wording, but a 403 body can, and the refusal is the stronger fact.
	for _, pat := range credentialSignals {
		if strings.Contains(hay, pat) {
			return disposition{
				Class:     ClassInfrastructure,
				Retryable: false,
				Signal:    pat,
				Reason:    "the remote answered and refused the credentials this box offered — the same question gets the same answer on a retry. Fix the credentials; do NOT dispatch a code fix",
			}
		}
	}

	for _, sig := range networkSignals {
		if strings.Contains(hay, sig.pattern) {
			return disposition{
				Class:     ClassInfrastructure,
				Retryable: true,
				Signal:    sig.pattern,
			}
		}
	}

	for _, pat := range conflictSignals {
		if strings.Contains(hay, pat) {
			return disposition{
				Class:     ClassDefect,
				Retryable: false,
				Signal:    pat,
				Reason:    "the rebase reached the tree and the tree disagreed — exactly as true on the next attempt",
			}
		}
	}

	// The pre-existing retry class: the target moved under us between rebase and
	// push. Establishes nothing about the branch.
	if isRetryable(err) {
		return disposition{
			Class:     ClassContention,
			Retryable: true,
			Signal:    "target moved during merge",
		}
	}

	return disposition{
		Class:     ClassUnclassified,
		Retryable: true,
		Signal:    "no known signal matched",
	}
}

// remoteTransport reports which transport an origin URL uses, and the host it
// names. Measured from the URL rather than inferred from git's English — the
// same principle mg-0d70 applied to network-vs-auth in the deploy runner.
func remoteTransport(url string) (transport, host string) {
	u := strings.TrimSpace(url)
	if u == "" {
		return "", ""
	}
	switch {
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		rest := u[strings.Index(u, "//")+2:]
		if i := strings.IndexAny(rest, "/:"); i >= 0 {
			rest = rest[:i]
		}
		if i := strings.Index(rest, "@"); i >= 0 {
			rest = rest[i+1:]
		}
		return "https", rest
	case strings.HasPrefix(u, "ssh://"):
		rest := u[len("ssh://"):]
		if i := strings.IndexAny(rest, "/:"); i >= 0 {
			rest = rest[:i]
		}
		if i := strings.Index(rest, "@"); i >= 0 {
			rest = rest[i+1:]
		}
		return "ssh", rest
	case strings.HasPrefix(u, "git://"):
		rest := u[len("git://"):]
		if i := strings.IndexAny(rest, "/:"); i >= 0 {
			rest = rest[:i]
		}
		return "git", rest
	case strings.HasPrefix(u, "file://"):
		return "file", ""
	case strings.HasPrefix(u, "/"), strings.HasPrefix(u, "."), strings.HasPrefix(u, "~"):
		return "file", ""
	case strings.Contains(u, "@") && strings.Contains(u, ":"):
		// scp-style: git@github.com:owner/repo.git
		rest := u[strings.Index(u, "@")+1:]
		if i := strings.Index(rest, ":"); i >= 0 {
			rest = rest[:i]
		}
		return "ssh", rest
	}
	return "unknown", ""
}

// transportFromError reads the transport out of the error's own wording, used
// when the origin URL could not be read. ssh and git's HTTP client have
// disjoint vocabularies, which is the property that made the mixed-transport
// incident readable once both halves were looked at.
func transportFromError(raw string) string {
	hay := strings.ToLower(raw)
	switch {
	case strings.Contains(hay, "ssh:"), strings.Contains(hay, "connect to host"), strings.Contains(hay, "publickey"):
		return "ssh"
	case strings.Contains(hay, "https://"), strings.Contains(hay, "http://"), strings.Contains(hay, "unable to access"):
		return "https"
	}
	return ""
}

// Retry bounds for infrastructure-class failures. They are separate from the
// ff-only contention budget ([gates] max_attempts) on purpose: a network blip
// must not consume the budget that exists to absorb races, and a long race must
// not consume the budget that exists to wait out a blip.
//
// Package vars rather than consts so tests can compress the schedule; the
// shipped values are the ones below.
var (
	// networkRetryBackoff is the delay BEFORE each successive network-class
	// retry. A suppressed-DNS window on this box lasted from en1 going INIT at
	// 20:30:40 to BOUND at 20:34:02; this schedule does not cover all of that,
	// and is not meant to. It covers a blip, and when it does not, the failure
	// is at least labelled infrastructure so the queue is triaged correctly
	// instead of producing N dispatches for N defects that do not exist.
	networkRetryBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}
	// networkMaxAttempts bounds network-class attempts, retries included.
	networkMaxAttempts = 5
	// networkRetryBudget bounds the TOTAL time spent sleeping between network
	// retries for one merge request, whatever the schedule says. The refinery is
	// a single serial slot: time spent waiting here is time every queued MR
	// waits too, so the bound is on the clock and not only on the count.
	networkRetryBudget = 90 * time.Second
)

// networkBackoffFor returns the delay before the nth network retry (n starting
// at 1), clamped to the last entry in the schedule.
func networkBackoffFor(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > len(networkRetryBackoff) {
		n = len(networkRetryBackoff)
	}
	return networkRetryBackoff[n-1]
}
