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
	// ClassHost marks a gate that RAN and reported the BOX rather than the
	// branch: the host ran out of a resource under it (mg-b41f).
	//
	// It is not ClassDefect, and the distinction is the whole ticket. The
	// no-retry rule for a verdict stage — "the gate ran on this tree and
	// returned a verdict, re-running establishes the same fact" — is true for a
	// deterministic failure and FALSE for an environmental one: on 2026-08-12 a
	// boot volume at 255 MiB free failed a gate, and the identical branch merged
	// clean once 7.3G was reclaimed. Re-running after the resource is restored
	// establishes a DIFFERENT fact.
	//
	// It is not ClassInfrastructure either. Infrastructure's triage note says
	// the failure establishes nothing about the branch and invites a straight
	// resubmit, and a straight resubmit into a still-full disk burns a gate slot
	// to re-derive the same fact. This class carries the extra instruction that
	// makes the difference: free the resource FIRST.
	//
	// See gatehostresource.go for the incident, the signal table and the
	// measured reasons the neighbouring conditions are not in it.
	ClassHost FailureClass = "host"
	// ClassIndeterminate marks a gate that was KILLED before it returned a
	// verdict — it timed out. It sits deliberately between the two classes
	// above and must not be folded into either (mg-e565).
	//
	// It is not ClassDefect: the gate never finished, so it has no opinion about
	// the tree, and the class that means "a fix is warranted" is a claim the
	// evidence does not support. Polecat z37ad's merge failed at exactly 1h0m0s
	// three times on a Go toolchain download holding a lock (root cause
	// mg-cdf1); the message was indistinguishable from a test failure, so it
	// spent hours believing its own change was at fault.
	//
	// It is not ClassInfrastructure either, and that half matters just as much:
	// infrastructure's triage note says the failure establishes NOTHING about
	// the branch and invites a straight resubmit. A gate can hang because the
	// branch made it hang — an infinite loop, a deadlock, a test waiting on
	// something that never arrives — and clearing the branch on a kill would be
	// the same error in the opposite direction.
	//
	// The honest reading is that the run was cut short and the question is still
	// open. What was observed is reported alongside it, as several facts that
	// may disagree, because no single signal settles it: a healthy gate was
	// measured silent for 22 minutes, and an idle subtree is also what a process
	// blocked on I/O looks like.
	ClassIndeterminate FailureClass = "indeterminate"
	// ClassUnclassified marks a failure the refinery could not place. It is
	// retried on a small budget — an unrecognised failure might plausibly differ
	// on a retry — and it is reported as unclassified rather than silently
	// folded into either of the two classes that carry a triage instruction.
	ClassUnclassified FailureClass = "unclassified"
)

// allFailureClasses is every class, for checks that must cover all of them.
// It lives beside the constants so a class added without a triage note fails a
// test rather than reaching a coordinator as a bare status.
var allFailureClasses = []FailureClass{
	ClassInfrastructure, ClassContention, ClassDefect, ClassHost, ClassIndeterminate, ClassUnclassified,
}

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
	case ClassHost:
		return "HOST — the host ran out of a resource while the gate ran, so the gate reported the BOX and not the branch. " +
			"The package and test names in the error are NOT findings. Free the resource, then resubmit UNCHANGED. " +
			"Do NOT dispatch a fix, and do NOT resubmit before the resource is freed — every gate on this host fails " +
			"the same way until it is."
	case ClassIndeterminate:
		return "INDETERMINATE — the gate was KILLED at its timeout before it returned a verdict. " +
			"It did not clear this branch and it did not condemn it. Read the per-layer signals in " +
			"the error below, which may point different ways, before reacting. Do NOT dispatch a fix " +
			"on this alone, and do NOT resubmit unchanged without checking the host first."
	case ClassUnclassified:
		return "UNCLASSIFIED — the refinery could not establish which class this is. Read the raw error below before reacting."
	}
	return ""
}

// countsAgainstAuthor answers whether a terminal failure of this class should
// accumulate into the author's consecutive-failure streak.
//
// Only a class that establishes a fact about the branch may. The streak feeds
// an escalation whose advice is "consider stopping the polecat or reassigning
// the work item" — advice about a person — so a class that establishes no such
// fact must not reach it. An empty class is a failure recorded before
// classification existed, and stays counted rather than being silently
// forgiven.
//
// ClassIndeterminate is excluded for the reason mg-e565 was filed: three
// timeout kills on one merge would have put z37ad within one of that threshold
// for a Go toolchain download. ClassHost is excluded for the reason mg-b41f was
// filed: a full boot volume fails every gate on the box, so counting it would
// accumulate a verdict about whoever was queued while the disk was full.
func countsAgainstAuthor(c FailureClass) bool {
	return c == "" || c == ClassDefect
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

// outputReportsConflict answers "did this git output say the tree disagreed?"
// It is the single reading of conflictSignals, shared by the classifier and by
// the gate-dirt detector, so those two cannot end up holding different
// definitions of a conflict and contradicting each other inside one report.
func outputReportsConflict(gitOutput string) bool {
	hay := strings.ToLower(gitOutput)
	for _, pat := range conflictSignals {
		if strings.Contains(hay, pat) {
			return true
		}
	}
	return false
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
//
// ClassHost is the one carve-out, and it does not weaken that boundary because
// it does not cross it: the boundary is about RETRY, and a host-resource failure
// is not retried, so the runaway it protects against cannot occur. What it
// changes is who is accused. See gatehostresource.go, and note that its signal
// table contains only wordings measured to have occurred in this fleet's own
// gate output — a speculative pattern there WOULD cross the boundary, by taking
// a real defect away from its author.
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
	// A gate that failed because the HOST ran out of a resource is judged first
	// — ahead of the timeout check below and of the stage table (mg-b41f).
	//
	// Ahead of the STAGE TABLE for the reason the ticket exists: "build" was
	// enough to make a full boot volume come out as a verdict on the branch,
	// with the reason "the build gate ran on this tree and returned a verdict —
	// re-running establishes the same fact". The gate did run; what it reported
	// was the box, and re-running once the disk was freed established a
	// different fact — the identical branch merged clean.
	//
	// Ahead of the TIMEOUT check because both can be true at once — a gate that
	// cannot write can also hang until it is killed — and of the two answers,
	// "the host ran out of disk" names something and "the run was cut short"
	// does not. Neither blames the branch, so preferring the specific one takes
	// nothing away from the author.
	var resErr *hostResourceError
	if errors.As(err, &resErr) {
		return disposition{
			Class:     ClassHost,
			Retryable: false,
			Signal:    "host-resource " + resErr.Signal,
			Reason: "the host ran out of " + resErr.Resource + " while the gate ran, so the gate reported the BOX and not " +
				"the branch — re-running once the resource is restored establishes a DIFFERENT fact, which is exactly what " +
				"the verdict-stage rule assumes it would not. It is NOT retried automatically because a full disk is not " +
				"restored by waiting: another attempt would spend a whole gate run re-deriving the same fact while the host " +
				"is still in the state that fails every gate on it. Free the resource and resubmit unchanged",
		}
	}

	// A gate that was KILLED is judged before the stage table, because it
	// arrives at the SAME stage as a gate that ran (mg-e565). "test" was
	// therefore enough to make a timeout come out as a verdict on the branch,
	// with the reason "the test gate ran on this tree and returned a verdict" —
	// a sentence about an event that did not happen. The distinguishing fact is
	// the error type, not the stage, and it has to be consulted first.
	var toErr *gateTimeoutError
	if errors.As(err, &toErr) {
		return disposition{
			Class:     ClassIndeterminate,
			Retryable: false,
			Signal:    "gate-timeout stage=" + stage,
			Reason: "the gate was killed at its timeout, so it never returned a verdict — this is " +
				"NOT a finding against the branch, and it is not a clearance either. It is not retried " +
				"automatically because another attempt costs a full timeout of the merge slot and " +
				"cannot answer the question; the per-layer signals in the error say what was observed",
		}
	}

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
	//
	// Guarded by the conflict table, and the guard is the point of mg-eac0:
	// this branch used to be taken on the strength of the error TYPE alone,
	// without reading the git output the same error carried. One report
	// therefore said "CONFLICT (content)" and "failed(infrastructure) —
	// establishes nothing about the branch, resubmit" at once. classifyGateDirt
	// no longer builds a gateDirtError for a conflicted step, so this is a
	// second lock on the same door: whatever reaches here, a conflict in the
	// output means the tree answered, and the answer belongs to the branch.
	var dirtErr *gateDirtError
	if errors.As(err, &dirtErr) && !outputReportsConflict(dirtErr.GitOutput) {
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
				Reason: "the rebase reached the tree and the tree disagreed — exactly as true on the next attempt. " +
					"Resubmitting unchanged re-runs the same conflict forever; the branch has to be rebased and the " +
					"conflict resolved by hand before it can land",
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
// THE OUTAGE THIS SIZES AGAINST IS A DISTRIBUTION THAT KEEPS WIDENING (mg-c3b7,
// corrected by mg-7110, widened by mg-682d). It has widened TWICE, so the
// current maximum is a lower bound on the next one and the margin below is
// deliberately large. Read the whole of this before changing any number below.
//
// The previous schedule was 5 attempts over 52s of backoff. It was written
// against a blip and said so honestly — "this schedule does not cover all of
// that, and is not meant to". Then the outage was measured. A controlled
// sampler ran across the window of 2026-08-10 03:37Z at 20s intervals with a
// positive control (ping 1.1.1.1, clean before onset, LOSS for the whole
// window, clean after recovery — so its failures carry information):
//
//	ONSET      03:37:23Z
//	RECOVERY   03:52:49Z
//	DURATION   15m26s
//
// The refinery's own timeline sits inside that window and agrees to the second:
// MR mr-d9sk3matjv1sgaptna70's ./build.sh finished CLEAN after 8m58s at
// 03:40:07Z — 2m44s INTO the outage, which is why the gate passed and the fetch
// that followed did not — then burned all 5 network attempts by 03:41:00Z with
// 11m49s of outage still to run.
//
// 52 seconds against that is not marginally short; it is short by a factor of
// ~17.8, so no rearrangement of backoff INSIDE 52 seconds can succeed. It can
// only fail faster.
//
// BUT THAT ONE WAVE IS NOT THE SIZE OF THE EVENT, AND NO SINGLE WAVE IS. The
// 15m26s above is one cycle of a recurring fault — a 25-minute DHCP lease whose
// T1 renewal fails, so the host loses its address every cycle (mg-964e). Its
// duration is a distribution, and THAT DISTRIBUTION HAS WIDENED TWICE:
//
//	stated as   "15 min ±41s, stable across four waves"   <- WITHDRAWN
//	then        maximum 17m52s   (wave G, n=9)            <- SUPERSEDED
//	now         maximum ~35m03s  (wave L, n=12)           <- current, and open
//
// Each of the first two was believed to be the size of the event when it was
// written. Both were retracted by the agent that measured them — the "±41s"
// figure came from the first three waves, which happened to cluster, and the
// sizing advice built on it ("~15m30s", later "at least 20 minutes") was
// withdrawn by name each time. THERE IS NO ESTABLISHED UPPER BOUND. Treat any
// maximum quoted here, including 35m03s, as the largest seen so far and not as
// the size of the event.
//
// Wave L is ~35m03s: onset predicted, recovery from LeaseStartTime 11:38:19Z.
// Its onset carries the offset uncertainty, so the duration does — but a HARD
// FLOOR of ~28 minutes is established without the lease at all, by an
// independent instrument: three consecutive */10 mail-check fires (11:10Z,
// 11:20Z, 11:30Z) all failed to complete and arrived batched. Three consecutive
// misses require ~28+ minutes of outage wherever onset fell.
//
// The previous campaign — 14 attempts, 21m45s of sleep — was sized against the
// 17m52s maximum of the day and was KNOWN-INSUFFICIENT the moment wave L landed:
// under the ~28m floor, and 13m18s under 35m03s. mg-682d widened it to 49m45s.
//
// HOW THE NEW NUMBER WAS CHOSEN, AND WHAT IT DOES NOT COVER. It is the observed
// maximum plus a large margin, NOT the observed maximum. The distribution's own
// history is the argument for the margin:
//
//	15m26s -> 17m52s   x1.16
//	17m52s -> 35m03s   x1.96
//	35m03s -> 49m45s   x1.42   <- what is shipped below
//
// So state the limit plainly rather than letting the reader infer coverage: a
// third widening the size of the second one (~68m40s) WOULD OUTRUN THIS TOO. The
// margin is sized to absorb a repeat of the first widening comfortably and a
// substantial part of a second, and it is bought against a real cost (below)
// rather than being free. Two departures do not establish a trend, so this is
// not sized against a projected next maximum; it is sized so that being wrong
// again costs another ticket rather than another incident.
//
// DO NOT TRIM TOWARD 35m03s. Every number this comment has ever quoted as "the"
// outage was superseded within days, and the gap between the campaign and the
// newest maximum is the thing most likely to be mistaken for spare room. It is
// not spare room; it is the entire point of the change.
//
// A BIGGER NUMBER IS STILL NOT THE FIX. This is a mitigation and mg-964e (the
// DHCP fault itself — a 25-minute lease whose T1 renewal fails) is the fix; if
// the lease is repaired this budget stops mattering. The shape worth building
// remains the one doctor named: distinguish "the network is down" from "attempts
// exhausted" and requeue, rather than sleeping longer. Sizing is also
// deliberately NOT done against any prediction of when the next window starts.
// (Onset is in fact predictable on this host — LeaseExpirationTime + 54s, n=3
// spanning 2 seconds — but a budget that assumes it fails the first time the
// lease pattern changes. Duration is what the budget has to survive; onset only
// says when.)
//
// What this costs, stated as two different things because they are two different
// things:
//
//   - During an outage, the wait is the OUTAGE plus at most one probe interval
//     (2 minutes), not 49m45s. The plateau exists for exactly this: the campaign
//     wakes every 2 minutes, so it notices recovery promptly and the ceiling is
//     never paid unless the outage actually runs that long. Widening the ceiling
//     therefore costs nothing on the waves already covered.
//   - Against a genuinely dead network — a remote deleted, a credential revoked,
//     anything that will not come back — the merge's lane (one repo; see
//     lanes.go, merges in different lanes run concurrently) is now held ~50
//     minutes before the failure is reported, up from ~22. That is the real
//     price of this change, and it is paid in slower bad news, not in lost work:
//     the class is still INFRASTRUCTURE and the branch is untouched.
//
// The waiting merge is also holding a completed gate verdict it no longer has to
// recompute (see gateHold in merge.go), which is the 8m58s this exists to stop
// throwing away — so a longer wait does not compound into a longer re-run.
//
// Package vars rather than consts so tests can compress the schedule; the
// shipped values are the ones below.
var (
	// networkRetryBackoff is the delay BEFORE each successive network-class
	// retry, clamped at the last entry. It plateaus at 2 minutes rather than
	// doubling forever: past the first minute the question is no longer "was
	// that a blip?" but "has a minutes-long outage ended yet?", and the answer
	// wants a steady probe. Two minutes bounds how long after recovery the
	// merge stays asleep.
	networkRetryBackoff = []time.Duration{15 * time.Second, 30 * time.Second, 60 * time.Second, 2 * time.Minute}
	// networkMaxAttempts bounds network-class attempts, retries included. 28
	// attempts is 27 retries, which under the schedule above sleeps
	// 15s+30s+60s+24x120s = 49m45s.
	//
	// THIS IS THE CONSTANT THAT BINDS. The shipped schedule fits inside the clock
	// backstop below, so raising the backstop alone changes nothing — both have
	// to move together, and moving only one is the silent no-op that makes a
	// widening look done. Against the distribution the balance is:
	//
	//	vs ~35m03s (wave L, current maximum)   +14m42s
	//	vs ~28m    (hard floor, lease-free)    +21m45s
	//	vs  17m52s (wave G)                    +31m53s
	//
	// Read those as margin against THE LARGEST SEEN SO FAR, which is what they
	// are, and not as coverage of the event. The distribution has widened twice
	// and has no established upper bound; a repeat of the larger widening clears
	// this budget too (see the sizing note above the var block).
	//
	// The previous version of this comment advertised "the measured 15m26s with a
	// 6m19s margin" — a figure that compared against a single wave rather than
	// the distribution's maximum and was a 60% overstatement even when written.
	// The three lines above are therefore stated against three different
	// reference points, with the weakest (the lease-independent floor) named, so
	// that no single quoted number can be mistaken for the size of the event.
	//
	// DO NOT TRIM. A reader who sees a 14m42s gap and trims toward 35m03s
	// reintroduces the defect this fixed. 19 attempts sleeps 31m45s, inside the
	// observed range outright, and the ratchet in gatehold_test refuses it. 21
	// attempts sleeps 35m45s and THE RATCHET DOES NOT REFUSE IT — it clears wave
	// L by 42 seconds, which is what "outlasts the observed maximum" is worth as
	// a guarantee. What catches that trim is the backstop-drift check in
	// failureclass_test: the two bounds move together or neither moves. Between
	// them the tests refuse both trims, but only one of them is asserting that
	// the campaign is long ENOUGH, and no test can assert that against a
	// distribution with no upper bound.
	networkMaxAttempts = 28
	// networkRetryBudget bounds the TOTAL time spent sleeping between network
	// retries for one merge request, whatever the schedule says. It is the
	// backstop on the clock for a schedule that is edited later; the shipped
	// schedule fits inside it (49m45s of 50m), so the ATTEMPT COUNT is what
	// actually stops the shipped configuration.
	//
	// The 15s of slack is thin on purpose — it keeps the two bounds saying the
	// same thing, so the reason a campaign ended is always "attempts spent" and
	// never an ambiguous race between the two. Widen this whenever the schedule
	// is widened, in the same commit.
	networkRetryBudget = 50 * time.Minute
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
