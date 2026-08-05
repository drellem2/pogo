package ghteardown

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// sleepRecorder captures the backoff a retry would have spent, so the policy is
// asserted rather than waited out.
type sleepRecorder struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *sleepRecorder) sleep(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waits = append(s.waits, d)
}

func (s *sleepRecorder) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.waits)
}

// The exact stderr `gh` produced on 2026-08-04, quoted from the mail that filed
// mg-dd22. Tests match against the real string, not a paraphrase — the whole
// mechanism is text classification, and a paraphrase would test the paraphrase.
const observedNetworkErr = "gh issue view drellem2/pogo#89 failed: error connecting to api.github.com\n" +
	"check your internet connection or https://githubstatus.com"

// The mg-03ea text, the auth-class sibling. The ticket asks explicitly that the
// next occurrence not have to be hand-separated from mg-03ea by reading prose.
const observedAuthErr = "gh issue view drellem2/pogo#89 failed: gh: To use GitHub CLI in a workflow, " +
	"set the GH_TOKEN environment variable, or run gh auth login"

// flakyLookup fails network-class for the first failures attempts on each ref,
// then answers from states. It models a blip: the carrier's real state is there
// the whole time, and only our ability to ask is intermittent.
func flakyLookup(failures int, states map[string]IssueState) (LookupFunc, func(string) int) {
	var mu sync.Mutex
	tries := map[string]int{}
	lookup := func(repo string, number int) (IssueState, error) {
		key := refKey(repo, number)
		mu.Lock()
		tries[key]++
		n := tries[key]
		mu.Unlock()
		if n <= failures {
			return StateUnknown, errors.New(observedNetworkErr)
		}
		st, ok := states[key]
		if !ok {
			return StateUnknown, errors.New("GraphQL: Could not resolve to an Issue")
		}
		return st, nil
	}
	attempts := func(key string) int {
		mu.Lock()
		defer mu.Unlock()
		return tries[key]
	}
	return lookup, attempts
}

// THE POSITIVE CONTROL FOR mg-dd22, HALF ONE: construct a transient failure and
// show the finding SURVIVES it.
//
// mg-07ba is done and drellem2/pogo#89 is open — a real teardown miss. The
// first lookup hits the 2026-08-04 network error; the second succeeds. The
// detector must report the miss, exactly as it would have with no blip at all.
//
// The failing arm is permanent and deliberate. Without it a regression that
// removed the retry entirely would still leave the first assertion green only
// if the blip vanished too — and the arm below proves the blip is genuinely
// fatal to an un-retried lookup, so the first arm's pass is attributable to the
// retry and to nothing else.
func TestATransientBlipDoesNotMaskARealFinding(t *testing.T) {
	states := map[string]IssueState{"drellem2/pogo#89": StateOpen}

	// The bug: one attempt, one blip, and the real finding is gone.
	raw, _ := flakyLookup(1, states)
	before := Detect([]Carrier{carrier07ba()}, raw)
	if len(before.Misses) != 0 || len(before.Blocked) != 1 {
		t.Fatalf("PREMISE FAILED: without retry the blip must swallow the finding, got %d miss / %d blocked",
			len(before.Misses), len(before.Blocked))
	}

	// The fix: the same blip, retried.
	flaky, attempts := flakyLookup(1, states)
	sleeps := &sleepRecorder{}
	after := Detect([]Carrier{carrier07ba()}, Retrying(flaky, 3, 2*time.Second, sleeps.sleep))

	if len(after.Misses) != 1 {
		t.Fatalf("THE FINDING WAS MASKED BY A BLIP: want 1 miss, got %d (blocked=%d indeterminate=%d)",
			len(after.Misses), len(after.Blocked), len(after.Indeterminate))
	}
	if after.Misses[0].Carrier.ID != "mg-07ba" {
		t.Errorf("recovered the wrong carrier: %+v", after.Misses[0].Carrier)
	}
	if len(after.Blocked) != 0 || after.Actionable() != true {
		t.Errorf("a recovered blip must leave no residue: %+v", after)
	}
	if n := attempts("drellem2/pogo#89"); n != 2 {
		t.Errorf("want 2 attempts (one blip, one answer), got %d", n)
	}
	if sleeps.count() != 1 {
		t.Errorf("want exactly one backoff between the two attempts, got %d", sleeps.count())
	}
}

// THE POSITIVE CONTROL FOR mg-dd22, HALF TWO: construct a GENUINE indeterminate
// and show it is still reported as one.
//
// The risk of the retry is that it turns every non-answer into "we'll try
// again" and eventually into noise. A deleted issue is not a blip: GitHub
// answered about this exact ref, the answer is final, and re-running only
// reproduces it. It must be reported as a determination — and it must cost
// exactly one attempt.
func TestAGenuineIndeterminateIsStillReportedAndNeverRetried(t *testing.T) {
	var attempts int
	lookup := func(string, int) (IssueState, error) {
		attempts++
		return StateUnknown, errors.New("gh issue view drellem2/pogo#89 failed: " +
			"GraphQL: Could not resolve to an Issue with the number of 89. (repository.issue)")
	}
	sleeps := &sleepRecorder{}
	rep := Detect([]Carrier{carrier07ba()}, Retrying(lookup, 3, 2*time.Second, sleeps.sleep))

	if len(rep.Indeterminate) != 1 {
		t.Fatalf("a genuine indeterminate must still be reported as indeterminate, got %+v", rep)
	}
	if len(rep.Blocked) != 0 {
		t.Errorf("a working instrument must not be reported as a broken one: %+v", rep.Blocked)
	}
	if got := rep.Indeterminate[0].Class; got != FailureSubject {
		t.Errorf("class = %q, want %q", got, FailureSubject)
	}
	if attempts != 1 {
		t.Errorf("A DETERMINISTIC FAILURE WAS RETRIED %d times — re-running it only reproduces it "+
			"and spends the window", attempts)
	}
	if sleeps.count() != 0 {
		t.Errorf("no backoff should be spent on a repeatable failure, spent %v", sleeps.waits)
	}
	if body := rep.Render(); !strings.Contains(body, "NOT clean") {
		t.Errorf("a genuine indeterminate must still be reported as not-clean:\n%s", body)
	}
}

// An auth failure is an instrument failure and a repeatable one. It must be
// loud (Blocked, not Indeterminate) and it must not be retried: a credential
// does not appear between two attempts two seconds apart.
func TestAnAuthFailureIsLoudButNotRetried(t *testing.T) {
	var attempts int
	lookup := func(string, int) (IssueState, error) {
		attempts++
		return StateUnknown, errors.New(observedAuthErr)
	}
	rep := Detect([]Carrier{carrier07ba()}, Retrying(lookup, 3, time.Second, func(time.Duration) {}))

	if len(rep.Blocked) != 1 || rep.Blocked[0].Class != FailureAuth {
		t.Fatalf("an auth failure must be reported as instrument-blocked: %+v", rep)
	}
	if attempts != 1 {
		t.Errorf("auth failure retried %d times; it is repeatable by construction", attempts)
	}
}

// When a network blip does NOT clear, the finding must survive the retries as a
// loud instrument failure — and say that it survived them. "unresolved after 3
// attempts" is what stops a reader mistaking an outage for one unlucky sample,
// which is precisely how 2026-08-04's batch read as twelve broken carriers.
func TestExhaustedRetriesAreReportedAsBlockedAndSayHowManyAttempts(t *testing.T) {
	var attempts int
	lookup := func(string, int) (IssueState, error) {
		attempts++
		return StateUnknown, errors.New(observedNetworkErr)
	}
	sleeps := &sleepRecorder{}
	rep := Detect([]Carrier{carrier07ba()}, Retrying(lookup, 3, 2*time.Second, sleeps.sleep))

	if attempts != 3 {
		t.Errorf("want 3 attempts, got %d", attempts)
	}
	if want := []time.Duration{2 * time.Second, 4 * time.Second}; !equalDurations(sleeps.waits, want) {
		t.Errorf("backoff must double: got %v, want %v", sleeps.waits, want)
	}
	if len(rep.Blocked) != 1 {
		t.Fatalf("an unresolved network failure must be blocked, not indeterminate: %+v", rep)
	}
	f := rep.Blocked[0]
	if f.Class != FailureNetwork {
		t.Errorf("class = %q, want %q", f.Class, FailureNetwork)
	}
	if !strings.Contains(f.Detail, "after 3 attempts") {
		t.Errorf("detail must record that the failure survived the retries, got %q", f.Detail)
	}
	if !strings.Contains(f.Detail, "api.github.com") {
		t.Errorf("detail must keep the original cause, got %q", f.Detail)
	}
}

// A lookup that succeeds first time must cost exactly one call and no backoff —
// the retry is free on the happy path, which is what makes it affordable to run
// over every done carrier on every sample.
func TestASuccessfulLookupIsNotRetried(t *testing.T) {
	var attempts int
	lookup := func(string, int) (IssueState, error) {
		attempts++
		return StateClosed, nil
	}
	sleeps := &sleepRecorder{}
	rep := Detect([]Carrier{carrier07ba()}, Retrying(lookup, 3, time.Second, sleeps.sleep))
	if attempts != 1 || sleeps.count() != 0 {
		t.Errorf("clean lookup cost %d attempts and %d sleeps, want 1 and 0", attempts, sleeps.count())
	}
	if rep.Actionable() {
		t.Errorf("a confirmed-closed carrier must be clean: %+v", rep)
	}
}

// The classifier is the load-bearing piece, and the ticket names the exact
// distinction it must make: today's NETWORK text and mg-03ea's AUTH text differ
// only in prose, and had to be hand-separated after the fact.
func TestClassifyTheObservedFailures(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want FailureClass
	}{
		{"the 2026-08-04 outage", observedNetworkErr, FailureNetwork},
		{"the mg-03ea auth gap", observedAuthErr, FailureAuth},
		{"dns", "dial tcp: lookup api.github.com: no such host", FailureNetwork},
		{"timeout", "Get \"https://api.github.com\": context deadline exceeded", FailureNetwork},
		{"tls", "net/http: TLS handshake timeout", FailureNetwork},
		{"refused", "dial tcp 140.82.121.6:443: connect: connection refused", FailureNetwork},
		{"bad credentials", "HTTP 401: Bad credentials (https://api.github.com/graphql)", FailureAuth},
		{"primary rate limit", "API rate limit exceeded for user ID 1234", FailureRateLimit},
		{"secondary rate limit", "You have exceeded a secondary rate limit", FailureRateLimit},
		{"missing issue", "GraphQL: Could not resolve to an Issue with the number of 999", FailureSubject},
		{"missing repo", "GraphQL: Could not resolve to a Repository with the name 'x/y'", FailureSubject},
		{"unmodelled state", "gh issue view x/y#1: unrecognised state \"LOCKED\"", FailureSubject},
		{"malformed ref", "unresolvable gh ref \"\"#0 — carrier cannot be checked", FailureSubject},
		{"novel", "gh: something nobody has seen before", FailureUnclassified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyLookupError(errors.New(tc.msg)); got != tc.want {
				t.Errorf("classified %q as %q, want %q", tc.msg, got, tc.want)
			}
		})
	}

	// The property the ticket asks for, stated as a property rather than left
	// implicit in the table: these two must not be the same answer.
	if ClassifyLookupError(errors.New(observedNetworkErr)) == ClassifyLookupError(errors.New(observedAuthErr)) {
		t.Error("the mg-dd22 network failure and the mg-03ea auth failure still classify identically — " +
			"the next occurrence would again have to be hand-separated by reading prose")
	}
}

// An unrecognised failure must fail toward LOUD. Calling it a determination
// about the carrier is exactly the collapse this ticket was filed about, so the
// safe direction is to treat it as an instrument failure — and not to retry it,
// since nothing establishes that a re-run would differ.
func TestAnUnclassifiedFailureIsTreatedAsAnInstrumentFailure(t *testing.T) {
	if !FailureUnclassified.Instrument() {
		t.Error("an unrecognised failure must not be reported as a determination about a carrier")
	}
	if FailureUnclassified.Retryable() {
		t.Error("an unrecognised failure must not be retried; nothing says a re-run would differ")
	}
	var attempts int
	rep := Detect([]Carrier{carrier07ba()}, Retrying(func(string, int) (IssueState, error) {
		attempts++
		return StateUnknown, errors.New("gh: something nobody has seen before")
	}, 3, time.Second, func(time.Duration) {}))
	if len(rep.Blocked) != 1 || attempts != 1 {
		t.Errorf("want 1 blocked finding after 1 attempt, got %d blocked after %d attempts",
			len(rep.Blocked), attempts)
	}
}

// Only the network class is retryable, and nothing else may drift into being
// so. Asserted directly because the cost of a wrong answer here is a scan that
// spends 3x the window reproducing the same deterministic error on every
// carrier.
func TestOnlyNetworkFailuresAreRetryable(t *testing.T) {
	for _, c := range []FailureClass{FailureAuth, FailureRateLimit, FailureSubject, FailureUnclassified, FailureNone} {
		if c.Retryable() {
			t.Errorf("%q must not be retryable — it is repeatable, and re-running it burns the window", c)
		}
	}
	if !FailureNetwork.Retryable() {
		t.Error("a network failure must be retried; that is the entire point of mg-dd22")
	}
	if FailureSubject.Instrument() {
		t.Error("a subject failure is a determination, not a broken instrument")
	}
}

// THE 2026-08-04 BATCH, REPLAYED.
//
// Twelve done carriers: six whose issue is closed (clean teardown) and six
// whose issue is still OPEN (real misses — the finding this package exists to
// produce). Every lookup hits one network blip before answering, which is what
// actually happened. The recorded outcome was "12 indeterminate" and six real
// findings were lost inside it.
//
// This is the end-to-end assertion that the whole repair holds: the six misses
// come back, the six clean carriers stay clean, and the run is not an
// instrument failure.
func TestTheMaskedBatchOf20260804IsRecovered(t *testing.T) {
	states := map[string]IssueState{}
	var carriers []Carrier
	closedRefs := []int{89, 93, 97, 193, 197, 199}
	openRefs := []int{100, 91, 88, 191, 188, 187}
	for i, n := range closedRefs {
		c := Carrier{ID: fmt.Sprintf("mg-clean%d", i), Status: "done", Repo: "drellem2/pogo", Number: n}
		carriers = append(carriers, c)
		states[c.String()] = StateClosed
	}
	for i, n := range openRefs {
		c := Carrier{ID: fmt.Sprintf("mg-miss%d", i), Status: "done", Repo: "drellem2/pogo", Number: n}
		carriers = append(carriers, c)
		states[c.String()] = StateOpen
	}

	// What happened: one attempt each, one blip each, twelve non-answers.
	raw, _ := flakyLookup(1, states)
	before := Detect(carriers, raw)
	if len(before.Blocked) != 12 || len(before.Misses) != 0 {
		t.Fatalf("PREMISE FAILED: un-retried, the batch must lose every finding; got %d blocked, %d misses",
			len(before.Blocked), len(before.Misses))
	}
	if !before.InstrumentFailure() {
		t.Error("an all-no-verdict batch must be reported as a suspected instrument failure")
	}

	// What should have happened.
	flaky, _ := flakyLookup(1, states)
	after := Detect(carriers, Retrying(flaky, 3, time.Millisecond, func(time.Duration) {}))
	if len(after.Misses) != 6 {
		t.Fatalf("want the 6 real teardown misses back, got %d (blocked=%d)", len(after.Misses), len(after.Blocked))
	}
	if len(after.Blocked) != 0 || len(after.Indeterminate) != 0 {
		t.Errorf("the recovered batch must carry no residue: blocked=%d indeterminate=%d",
			len(after.Blocked), len(after.Indeterminate))
	}
	if after.InstrumentFailure() {
		t.Error("a batch with six real verdicts is a result, not a broken instrument")
	}
	if after.Scanned != 12 {
		t.Errorf("scanned = %d, want 12", after.Scanned)
	}
}

// An all-no-verdict run must announce ITSELF as the finding — in the subject
// line, which is the only part a reader who has learned to skim still sees.
func TestAnAllNoVerdictBatchIsReportedAsAnInstrumentFailure(t *testing.T) {
	var carriers []Carrier
	for i := 0; i < 13; i++ {
		carriers = append(carriers, Carrier{
			ID: fmt.Sprintf("mg-%04d", i), Status: "done", Repo: "drellem2/pogo", Number: i + 1,
		})
	}
	rep := Detect(carriers, func(string, int) (IssueState, error) {
		return StateUnknown, errors.New(observedNetworkErr)
	})

	if !rep.InstrumentFailure() {
		t.Fatalf("13 carriers, 13 non-answers: this is a broken instrument, not 13 broken carriers: %+v", rep)
	}
	subject := rep.MailSubject()
	if !strings.Contains(subject, "INSTRUMENT FAILURE") {
		t.Errorf("subject must say the run measured nothing, got %q", subject)
	}
	if !strings.Contains(subject, "network") {
		t.Errorf("subject must name the cause so it need not be hand-separated, got %q", subject)
	}
	body := rep.Render()
	if !strings.HasPrefix(body, "SUSPECTED INSTRUMENT FAILURE") {
		t.Errorf("the banner must lead the report:\n%s", body)
	}
	if !strings.Contains(body, "NOT a result") {
		t.Errorf("the report must deny being a result:\n%s", body)
	}
}

// A batch with even one real verdict is a result. The instrument demonstrably
// worked, so a non-answer inside it is about that carrier — and calling it an
// instrument failure would make the loud signal cheap.
func TestAPartiallyBlindBatchIsStillAResult(t *testing.T) {
	carriers := []Carrier{
		{ID: "mg-0001", Status: "done", Repo: "drellem2/pogo", Number: 1},
		{ID: "mg-0002", Status: "done", Repo: "drellem2/pogo", Number: 2},
		{ID: "mg-0003", Status: "done", Repo: "drellem2/pogo", Number: 3},
	}
	rep := Detect(carriers, func(_ string, number int) (IssueState, error) {
		if number == 1 {
			return StateOpen, nil
		}
		return StateUnknown, errors.New(observedNetworkErr)
	})
	if rep.InstrumentFailure() {
		t.Error("a batch containing a real verdict is not an instrument failure")
	}
	if len(rep.Misses) != 1 || len(rep.Blocked) != 2 {
		t.Fatalf("want 1 miss and 2 blocked, got %+v", rep)
	}
	// The miss must still be visible, and it must not be presented as though
	// the whole batch had been audited.
	body := rep.Render()
	if !strings.Contains(body, "TEARDOWN MISS") || !strings.Contains(body, "NOT CHECKED") {
		t.Errorf("both the finding and the coverage gap must appear:\n%s", body)
	}
	if !strings.Contains(rep.MailSubject(), "NOT CHECKED") {
		t.Errorf("the subject must admit the two unchecked carriers, got %q", rep.MailSubject())
	}
}

// One carrier is not a sample. With a single scanned carrier, "the instrument
// is broken" and "this carrier cannot be resolved" are the same observation,
// and claiming the former would be inventing the distinction rather than
// detecting it.
func TestOneNoVerdictCarrierIsNotClaimedAsAnInstrumentFailure(t *testing.T) {
	rep := Detect([]Carrier{carrier07ba()}, func(string, int) (IssueState, error) {
		return StateUnknown, errors.New(observedNetworkErr)
	})
	if rep.InstrumentFailure() {
		t.Error("a sample of one cannot distinguish a broken instrument from a broken carrier")
	}
	// It is still loud, just not as a claim about the whole run.
	if len(rep.Blocked) != 1 || !rep.Actionable() {
		t.Errorf("a blocked carrier must still be reported: %+v", rep)
	}
}

func equalDurations(got, want []time.Duration) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
