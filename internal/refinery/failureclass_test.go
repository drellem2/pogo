package refinery

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The strings below are the ACTUAL failure text from the 2026-08-05 incident,
// copied verbatim from mg-e5c2 rather than paraphrased. Both transports are
// present on purpose: 20 of the 31 failures were the ssh line and 11 were the
// HTTPS line, and every reader who worked from one of them alone got the
// mechanism wrong. A test that carried only the ssh half would be the same
// mistake in a place that outlives the incident.
const (
	incidentSSH   = "ssh: connect to host github.com port 22: Undefined error: 0\nfatal: Could not read from remote repository."
	incidentHTTPS = "fatal: unable to access 'https://github.com/drellem2/pogo/': Could not resolve host: github.com"
)

// TestBothTransportsOfTheIncidentClassifyTheSame is the regression that the
// ticket's own history demands. The ssh half carries errno 0 — a wording that
// names no cause at all — and the HTTPS half names DNS outright. They were the
// same event; they must land in the same class, and both must be retryable.
func TestBothTransportsOfTheIncidentClassifyTheSame(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"ssh half (20 of 31 failures)", incidentSSH},
		{"https half (11 of 31 failures)", incidentHTTPS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := classifyFailure("fetch", tc.raw, errors.New("fetch: "+tc.raw))
			if d.Class != ClassInfrastructure {
				t.Errorf("class = %q, want %q — this failure never reached the tree and establishes nothing about it", d.Class, ClassInfrastructure)
			}
			if !d.Retryable {
				t.Errorf("retryable = false, want true: re-running plausibly gives a different answer for a reason unrelated to the code")
			}
			if d.Signal == "" {
				t.Error("no signal recorded — the classification must name the evidence that decided it")
			}
		})
	}
}

// TestErrnoZeroIsNotClassifiedByItsErrno guards the reasoning error that cost
// several hours: "Undefined error: 0" was read as evidence that the failure was
// NOT a network failure, because a blackholed host gives "Operation timed out".
// The class here comes from the STEP that failed — a transport step that never
// reached the tree — not from what errno 0 is imagined to mean.
func TestErrnoZeroIsNotClassifiedByItsErrno(t *testing.T) {
	d := classifyFailure("fetch", incidentSSH, errors.New(incidentSSH))
	if d.Signal == "undefined error: 0" {
		t.Fatalf("classification keyed on the errno text itself (%q) — that is the reasoning that produced two wrong mechanisms", d.Signal)
	}
	if d.Signal != "connect to host" {
		t.Errorf("signal = %q, want the connect-step wording %q", d.Signal, "connect to host")
	}
}

func TestRetryableNetworkClasses(t *testing.T) {
	retryable := []struct{ name, raw string }{
		{"dns suppressed, https", "fatal: unable to access 'https://github.com/x/y': Could not resolve host: github.com"},
		{"dns, ssh wording", "ssh: Could not resolve hostname github.com: nodename nor servname provided"},
		{"linux resolver", "ssh: Could not resolve hostname github.com: Name or service not known"},
		{"blackhole timeout", "ssh: connect to host 192.0.2.1 port 22: Operation timed out"},
		{"refused", "ssh: connect to host github.com port 22: Connection refused"},
		{"reset mid-transfer", "fatal: the remote end hung up unexpectedly\nerror: RPC failed; curl 56 Recv failure: Connection reset by peer"},
		{"github 5xx", "fatal: unable to access 'https://github.com/x/y': The requested URL returned error: 503"},
		{"github rate limit", "fatal: unable to access 'https://github.com/x/y': The requested URL returned error: 429"},
		{"kex", "kex_exchange_identification: Connection closed by remote host"},
		{"tls", "fatal: unable to access 'https://github.com/x/y': gnutls_handshake() failed"},
	}
	for _, tc := range retryable {
		t.Run(tc.name, func(t *testing.T) {
			d := classifyFailure("fetch", tc.raw, errors.New(tc.raw))
			if d.Class != ClassInfrastructure || !d.Retryable {
				t.Errorf("class=%q retryable=%v, want infrastructure/true", d.Class, d.Retryable)
			}
		})
	}
}

// TestFailuresThatEstablishAFactAreNotRetried is the other half of pm-pogo's
// ruling. Each of these reached the tree and returned an answer about it, so a
// retry asks a question that has already been answered — and each must record
// WHY it was not retried, so the absence of a retry is legible.
func TestFailuresThatEstablishAFactAreNotRetried(t *testing.T) {
	cases := []struct {
		name  string
		stage string
		raw   string
		want  FailureClass
	}{
		{"test gate RED", "test", "quality gate: ./test.sh: exit status 1\nFAIL github.com/x/y", ClassDefect},
		{"build failure", "build", "quality gate: ./build.sh: exit status 2", ClassDefect},
		{"do_prove RED via gate", "test", "do_prove: RED", ClassDefect},
		{"closing-ref refusal", "closing-ref-check", "commit message would close drellem2/pogo#12", ClassDefect},
		{"rebase conflict", "rebase", "error: could not apply 0ab12cd... feat: x\nCONFLICT (content): Merge conflict in main.go", ClassDefect},
		{"credentials refused", "fetch", "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.", ClassInfrastructure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := classifyFailure(tc.stage, tc.raw, errors.New(tc.raw))
			if d.Class != tc.want {
				t.Errorf("class = %q, want %q", d.Class, tc.want)
			}
			if d.Retryable {
				t.Error("retryable = true, want false — this failure establishes a fact that a retry cannot change")
			}
			if strings.TrimSpace(d.Reason) == "" {
				t.Error("no reason recorded — mg-e5c2 requirement 3: the absence of a retry must say why, or it looks like a policy that does not exist")
			}
		})
	}
}

// TestGateOutputIsNotMinedForNetworkWording is the deliberate boundary stated in
// failureclass.go. A red test that happens to PRINT network wording must not
// become retryable, or a genuine assertion failure would be retried forever.
func TestGateOutputIsNotMinedForNetworkWording(t *testing.T) {
	raw := "quality gate: ./test.sh: exit status 1\n--- FAIL: TestDial\n    dial_test.go:12: got \"connection refused\", want nil"
	d := classifyFailure("test", raw, errors.New(raw))
	if d.Class != ClassDefect || d.Retryable {
		t.Fatalf("class=%q retryable=%v — a gate that RAN returned a verdict on this tree; its output is not a transport diagnosis", d.Class, d.Retryable)
	}
}

// TestCredentialFailureIsInfrastructureButNotRetried holds the two axes apart.
// A refused key establishes nothing about the branch — so no one should be sent
// to read the code — but it is refused identically on the next attempt.
func TestCredentialFailureIsInfrastructureButNotRetried(t *testing.T) {
	d := classifyFailure("push", "git@github.com: Permission denied (publickey).", errors.New("push failed"))
	if d.Class != ClassInfrastructure {
		t.Errorf("class = %q, want infrastructure — nobody should be dispatched to fix the branch over a refused key", d.Class)
	}
	if d.Retryable {
		t.Error("retryable = true, want false — the same question gets the same answer")
	}
	if !strings.Contains(d.Reason, "credentials") {
		t.Errorf("reason %q does not say what to fix", d.Reason)
	}
}

func TestContentionKeepsItsPreExistingClass(t *testing.T) {
	err := &retryableError{errors.New("merge (ff-only): Not possible to fast-forward")}
	d := classifyFailure("rebase", "", err)
	if d.Class != ClassContention || !d.Retryable {
		t.Errorf("class=%q retryable=%v, want contention/true", d.Class, d.Retryable)
	}
}

func TestUnknownFailureIsUnclassifiedNotMisfiled(t *testing.T) {
	d := classifyFailure("merge", "something nobody has seen before", errors.New("x"))
	if d.Class != ClassUnclassified {
		t.Errorf("class = %q, want unclassified — folding an unknown into either triage class asserts more than was established", d.Class)
	}
	if !d.Retryable {
		t.Error("an unclassified failure gets the benefit of the doubt once")
	}
}

func TestRemoteTransport(t *testing.T) {
	cases := []struct{ url, transport, host string }{
		{"git@github.com:drellem2/pogo.git", "ssh", "github.com"},
		{"ssh://git@github.com/drellem2/pogo.git", "ssh", "github.com"},
		{"https://github.com/drellem2/pogo.git", "https", "github.com"},
		{"https://user@github.com/drellem2/pogo.git", "https", "github.com"},
		{"/Users/daniel/dev/pogo", "file", ""},
		{"file:///tmp/x", "file", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		gotT, gotH := remoteTransport(tc.url)
		if gotT != tc.transport || gotH != tc.host {
			t.Errorf("remoteTransport(%q) = (%q,%q), want (%q,%q)", tc.url, gotT, gotH, tc.transport, tc.host)
		}
	}
}

// TestTransportFallsBackToTheErrorWording matters when the origin URL cannot be
// read: the record must still say which transport spoke, because a record that
// omits the transport is the record that made 2026-08-05 unreadable.
func TestTransportFallsBackToTheErrorWording(t *testing.T) {
	if got := transportFromError(incidentSSH); got != "ssh" {
		t.Errorf("ssh half read as %q", got)
	}
	if got := transportFromError(incidentHTTPS); got != "https" {
		t.Errorf("https half read as %q", got)
	}
}

// TestNetworkBackoffIsBoundedAndAscending guards the "bounded" half of the
// requirement: the schedule must not be able to grow without limit, and the
// total must stay inside the retry budget.
func TestNetworkBackoffIsBoundedAndAscending(t *testing.T) {
	var total float64
	prev := networkBackoffFor(1)
	for n := 1; n <= networkMaxAttempts-1; n++ {
		d := networkBackoffFor(n)
		if d < prev {
			t.Errorf("backoff %d (%s) is shorter than %d (%s)", n, d, n-1, prev)
		}
		prev = d
		total += d.Seconds()
	}
	if total > networkRetryBudget.Seconds() {
		t.Errorf("the shipped schedule sleeps %.0fs across its retries, over the %s budget — the refinery is one serial slot and every queued MR waits behind it", total, networkRetryBudget)
	}
	// Past the end of the schedule the delay clamps rather than growing.
	if networkBackoffFor(99) != networkRetryBackoff[len(networkRetryBackoff)-1] {
		t.Error("backoff past the schedule does not clamp")
	}
}

// TestTheAttemptCountIsWhatBindsTheNetworkCampaign guards the silent no-op that
// makes a widening LOOK done: networkRetryBudget is a clock backstop sitting
// above the schedule, so raising it alone changes nothing a merge experiences.
// mg-682d had to move both, and a later widening will have to move both too.
//
// The failure this produces is deliberately readable in both directions. If the
// backstop is left behind, the campaign silently truncates at the clock and the
// attempt count stops meaning what it says — and the ratchet in gatehold_test
// then reports "the campaign is too short", which points at the wrong constant
// unless something says which bound stopped the walk. This is that something.
func TestTheAttemptCountIsWhatBindsTheNetworkCampaign(t *testing.T) {
	var total time.Duration
	stoppedByClock := false
	for n := 1; n <= networkMaxAttempts-1; n++ {
		next := networkBackoffFor(n)
		if total+next > networkRetryBudget {
			stoppedByClock = true
			break
		}
		total += next
	}
	if stoppedByClock {
		t.Errorf("the schedule hit the %s clock backstop after %s, before spending its %d attempts — the two bounds have drifted apart, so networkMaxAttempts no longer describes the campaign. Raise networkRetryBudget in the same commit that raises the attempt count",
			networkRetryBudget, total.Round(time.Second), networkMaxAttempts)
	}

	// And the backstop must not sit far above the schedule either. The property
	// is that the two bounds TRACK EACH OTHER, and drift can come from either
	// side — so the message names both, because assuming which constant moved is
	// how a reader ends up editing the wrong one. That is not hypothetical: the
	// only way to trip this on the current schedule is to trim the attempt count,
	// which reads at first glance as a complaint about the backstop.
	if slack := networkRetryBudget - total; slack > 2*networkBackoffFor(networkMaxAttempts) {
		t.Errorf("the clock backstop (%s) and the schedule (%s over %d attempts) have drifted %s apart, more than two probe intervals. Either the attempt count was TRIMMED without lowering the backstop — check the ratchet in gatehold_test, which is the assertion that says whether the campaign is still long enough — or the backstop was RAISED without lengthening the schedule, which leaves room for a later schedule edit to stretch the campaign with no bound noticing. Move them together",
			networkRetryBudget, total.Round(time.Second), networkMaxAttempts, slack.Round(time.Second))
	}
}

func TestStatusLabelDistinguishesInfrastructureFromDefect(t *testing.T) {
	infra := &MergeRequest{Status: StatusFailed, FailureClass: ClassInfrastructure}
	defect := &MergeRequest{Status: StatusFailed, FailureClass: ClassDefect}
	merged := &MergeRequest{Status: StatusMerged}

	if got := infra.StatusLabel(); got != "failed(infrastructure)" {
		t.Errorf("infrastructure status label = %q, want failed(infrastructure) — a coordinator reads the status before the error text", got)
	}
	if got := defect.StatusLabel(); got != "failed" {
		t.Errorf("defect status label = %q, want plain failed", got)
	}
	if infra.StatusLabel() == defect.StatusLabel() {
		t.Error("the two are the same token — this is the defect mg-e5c2 requirement 2 names")
	}
	if got := merged.StatusLabel(); got != "merged" {
		t.Errorf("merged label = %q", got)
	}
}

// TestMachineStatusIsUnchanged is the control on the change above. Every polecat
// in the fleet breaks out of its poll loop on the literal strings emitted by
// `pogo refinery show --json | jq -r .status`. A new token THERE would leave a
// polecat spinning through the failure it was meant to report — a worse loss
// than the triage confusion. The class travels beside it, not inside it.
func TestMachineStatusIsUnchanged(t *testing.T) {
	for _, c := range allFailureClasses {
		mr := &MergeRequest{Status: StatusFailed, FailureClass: c}
		if string(mr.Status) != "failed" {
			t.Fatalf("machine-readable status for class %s is %q — poll loops key on \"failed\"", c, mr.Status)
		}
	}
}

func TestAttemptLineSaysWhetherARetryFollowed(t *testing.T) {
	retried := AttemptFailure{Attempt: 1, Stage: "fetch", Class: ClassInfrastructure, Transport: "ssh", Retried: true, BackoffSeconds: 2}
	if got := retried.Line(); !strings.Contains(got, "retried after 2s") || !strings.Contains(got, "transport=ssh") {
		t.Errorf("retried line = %q", got)
	}
	not := AttemptFailure{Attempt: 1, Stage: "test", Class: ClassDefect, NotRetriedReason: "not retryable: the gate ran"}
	got := not.Line()
	if !strings.Contains(got, "NOT RETRIED") || !strings.Contains(got, "the gate ran") {
		t.Errorf("un-retried line = %q — the absence of a retry must carry its reason", got)
	}
}

func TestSummarizeAttemptClasses(t *testing.T) {
	got := summarizeAttemptClasses([]AttemptFailure{
		{Class: ClassInfrastructure}, {Class: ClassInfrastructure}, {Class: ClassContention},
	})
	want := "2x infrastructure, 1x contention"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := summarizeAttemptClasses(nil); got != "no failed attempts" {
		t.Errorf("empty summary = %q", got)
	}
}

// TestEveryClassCarriesATriageNote: a class with no instruction is a token a
// coordinator has to look up, which is one step short of reading the error line.
func TestEveryClassCarriesATriageNote(t *testing.T) {
	for _, c := range allFailureClasses {
		if note := c.TriageNote(); note == "" {
			t.Errorf("class %s has no triage note", c)
		}
	}
	if strings.Contains(ClassInfrastructure.TriageNote(), "dispatch a fix.") &&
		!strings.Contains(ClassInfrastructure.TriageNote(), "do NOT dispatch") {
		t.Error("the infrastructure note must tell a coordinator NOT to dispatch")
	}
	if !strings.Contains(ClassDefect.TriageNote(), "fix is warranted") {
		t.Errorf("the defect note must say a fix IS warranted: %q", ClassDefect.TriageNote())
	}
}

// TestOnlyDefectCommitsToRepeating guards the predicate mg-441f's check-stranded
// remedy is suppressed on. A class added here without deciding this answers NO by
// falling through the switch, which is the safe direction — a remedy printed
// where it should not be costs a wasted gate run, while a remedy withheld where
// it was correct leaves finished work stranded — but it must be a DECISION, so
// the coverage assertion is on the whole table.
func TestOnlyDefectCommitsToRepeating(t *testing.T) {
	if !ClassDefect.ResubmitUnchangedRepeats() {
		t.Error("ClassDefect does not commit to repeating, yet its own triage note says " +
			"re-running establishes the SAME fact")
	}
	for _, c := range allFailureClasses {
		if c == ClassDefect {
			continue
		}
		if c.ResubmitUnchangedRepeats() {
			t.Errorf("class %s claims an unchanged resubmit repeats; only a class that "+
				"establishes a fact about the BRANCH may, and a caller that suppresses the "+
				"resubmit remedy on this would withhold the correct one", c)
		}
	}
	if FailureClass("").ResubmitUnchangedRepeats() {
		t.Error("an unclassified-by-absence failure claims to repeat; the strong claim needs evidence")
	}
	// The distinction the doc comment turns on: HOST also tells a reader not to
	// resubmit yet, and it is deliberately not folded in because its prerequisite
	// is on the box and its eventual remedy IS an unchanged resubmit.
	if !strings.Contains(ClassHost.TriageNote(), "resubmit UNCHANGED") {
		t.Errorf("the host note no longer says the eventual remedy is an unchanged resubmit, "+
			"which is the reason it is excluded above: %q", ClassHost.TriageNote())
	}
}

func TestGitStepErrorKeepsRawOutputAndCommand(t *testing.T) {
	inner := fmt.Errorf("exit status 128")
	err := gitStepFail("fetch", "fetch: "+incidentSSH+": exit status 128", []string{"fetch", "origin"}, incidentSSH, inner)
	if rawOf(err) != incidentSSH {
		t.Errorf("raw output lost: %q", rawOf(err))
	}
	var step *gitStepError
	if !errors.As(err, &step) {
		t.Fatal("gitStepError not reachable through errors.As")
	}
	if step.cmd != "git fetch origin" {
		t.Errorf("command = %q, want the invocation as invoked", step.cmd)
	}
	if !errors.Is(err, inner) {
		t.Error("the wrapped error is no longer reachable")
	}
	// Wrapped in retryableError, both must still be reachable.
	wrapped := &retryableError{err}
	if rawOf(wrapped) != incidentSSH {
		t.Error("raw output not reachable through retryableError")
	}
	if !isRetryable(wrapped) {
		t.Error("retryable marker lost")
	}
}

// TestTheEventLogMakesAMixedTransportIncidentReadable is the regression for the
// reading failure that cost several hours on 2026-08-05. The event log is the
// only view spanning merge requests, so it is where an incident's SHAPE lives —
// and the shape was 20 ssh failures whose wording named no cause interleaved
// with 11 HTTPS failures that named DNS outright. Reconstructing one transport
// is how the wrong mechanisms were built; both must come back.
func TestTheEventLogMakesAMixedTransportIncidentReadable(t *testing.T) {
	path := useTempEventLog(t)

	sshMR := &MergeRequest{ID: "mr-ssh", Branch: "polecat-a", TargetRef: "main", Author: "mg-a", RepoPath: "/repo"}
	emitMergeFailed(sshMR, 1, "fetch", errors.New("fetch: "+incidentSSH), true, "", AttemptFailure{
		Attempt: 1, Stage: "fetch", Transport: "ssh", Class: ClassInfrastructure,
		Command: "git fetch origin", RawError: incidentSSH, Signal: "connect to host",
		NotRetriedReason: "not retryable: the network retry budget is spent",
	})
	httpsMR := &MergeRequest{ID: "mr-https", Branch: "polecat-b", TargetRef: "main", Author: "mg-b", RepoPath: "/repo"}
	emitMergeFailed(httpsMR, 1, "fetch", errors.New("fetch: "+incidentHTTPS), true, "", AttemptFailure{
		Attempt: 1, Stage: "fetch", Transport: "https", Class: ClassInfrastructure,
		Command: "git fetch origin", RawError: incidentHTTPS, Signal: "could not resolve host",
		NotRetriedReason: "not retryable: the network retry budget is spent",
	})

	w, err := HistoryFromLog(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	transports := map[string]string{}
	for _, mr := range w.Requests {
		if len(mr.Attempts) != 1 {
			t.Fatalf("%s reconstructed %d attempts, want 1", mr.ID, len(mr.Attempts))
		}
		a := mr.Attempts[0]
		transports[a.Transport] = a.RawError
		if mr.FailureClass != ClassInfrastructure {
			t.Errorf("%s class = %q, want infrastructure", mr.ID, mr.FailureClass)
		}
		if a.Command != "git fetch origin" {
			t.Errorf("%s lost the git command as invoked", mr.ID)
		}
		if mr.NotRetriedReason == "" {
			t.Errorf("%s lost the reason no retry followed", mr.ID)
		}
	}
	if len(transports) != 2 {
		t.Fatalf("the reconstruction shows %d transport(s): %v — a single-transport view of this incident produced two confident wrong mechanisms", len(transports), transports)
	}
	if !strings.Contains(transports["ssh"], "Undefined error: 0") {
		t.Errorf("ssh raw error was normalised away: %q", transports["ssh"])
	}
	if !strings.Contains(transports["https"], "Could not resolve host") {
		t.Errorf("https raw error was normalised away: %q — this is the half that named the cause", transports["https"])
	}
}
