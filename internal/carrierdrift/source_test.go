package carrierdrift

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/ghteardown"
)

// TestParseBodyIsStructural pins the three restrictions, each of which was
// earned by a real body in this store. The fence rule in particular is not
// pedantry: this package's own ticket body pastes a `gh:` line inside a fence,
// and a loose parse would let a ticket manufacture carriers out of its own prose.
func TestParseBodyIsStructural(t *testing.T) {
	body := `
# triage: something

workflow: gh-issue
stage: triage
gh: drellem2/pogo#159

> **CARRIER NOTE 2026-09-07** — moved to the front of the queue.
> stage: gated
> gh: drellem2/pogo#999

The filing recipe, quoted so the next reader can copy it:

` + "```" + `
workflow: gh-issue
stage: build
gh: <owner>/<repo>#<n>
` + "```" + `

gh-parked: waiting on the reporter
`
	stage, ref, declClosed, declAck, declParked, ok := ParseBody(body)
	if !ok {
		t.Fatal("a gh-issue carrier did not parse as one")
	}
	if stage != "triage" {
		t.Fatalf("stage = %q — a blockquoted note or a fenced recipe won", stage)
	}
	if ref != "drellem2/pogo#159" {
		t.Fatalf("gh ref = %q", ref)
	}
	if declParked != "waiting on the reporter" {
		t.Fatalf("gh-parked = %q", declParked)
	}
	if declClosed != "" || declAck != "" {
		t.Fatalf("undeclared keys came back set: closed=%q ack=%q", declClosed, declAck)
	}
}

// TestParseBodyRejectsNonCarriers: the `workflow: gh-issue` line is the
// predicate. An item that merely mentions an issue is not carrying it, and
// treating it as one would report drift against an issue nobody claimed.
func TestParseBodyRejectsNonCarriers(t *testing.T) {
	for name, body := range map[string]string{
		"no workflow line": "stage: triage\ngh: drellem2/pogo#1\n",
		"other workflow":   "workflow: refinery\nstage: triage\ngh: drellem2/pogo#1\n",
		"prose mention":    "This is about drellem2/pogo#1 and the gh: marker convention.\n",
		"fenced only":      "```\nworkflow: gh-issue\ngh: drellem2/pogo#1\n```\n",
		"blockquoted only": "> workflow: gh-issue\n> gh: drellem2/pogo#1\n",
		"empty":            "",
	} {
		if _, _, _, _, _, ok := ParseBody(body); ok {
			t.Errorf("%s: parsed as a carrier", name)
		}
	}
}

// TestParseRef covers the decorations a ref arrives wrapped in, and the one
// case with a real consequence: a `gh:` line naming several refs. The FIRST is
// the carrier's subject; re-reading a cited one would report drift against an
// issue this carrier never claimed.
func TestParseRef(t *testing.T) {
	for _, tc := range []struct {
		in     string
		repo   string
		number int
		bad    bool
	}{
		{in: "drellem2/pogo#159", repo: "drellem2/pogo", number: 159},
		{in: "  drellem2/pogo#159 ", repo: "drellem2/pogo", number: 159},
		{in: "`drellem2/pogo#159`", repo: "drellem2/pogo", number: 159},
		{in: "https://github.com/drellem2/pogo/issues/159", repo: "drellem2/pogo", number: 159},
		{in: "drellem2/pogo#159, drellem2/pogo#160", repo: "drellem2/pogo", number: 159},
		{in: "drellem2/pogo", bad: true},
		{in: "pogo#159", bad: true},
		{in: "drellem2/pogo#0", bad: true},
		{in: "drellem2/pogo#abc", bad: true},
		{in: "", bad: true},
	} {
		repo, number, err := ParseRef(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("ParseRef(%q) = %s#%d, want an error", tc.in, repo, number)
			}
			continue
		}
		if err != nil || repo != tc.repo || number != tc.number {
			t.Errorf("ParseRef(%q) = %s#%d, %v; want %s#%d", tc.in, repo, number, err, tc.repo, tc.number)
		}
	}
}

// TestParseTimeAbstainsOnRubbish: an unparseable stamp must yield the zero time,
// which every age check treats as "abstain". The alternative — some fallback
// like now, or the epoch — would turn a store-read defect into either silence
// or a wall of ancient carriers.
func TestParseTimeAbstainsOnRubbish(t *testing.T) {
	if got := parseTime("2026-09-07T12:00:00Z"); got.IsZero() {
		t.Fatal("a valid RFC3339 stamp parsed to zero")
	}
	if got := parseTime("2026-09-07T18:37:11.091506231+01:00"); got.IsZero() {
		t.Fatal("the nanosecond+offset form mg actually emits parsed to zero")
	}
	for _, bad := range []string{"", "  ", "yesterday", "1725710400"} {
		if got := parseTime(bad); !got.IsZero() {
			t.Errorf("parseTime(%q) = %s, want the zero time", bad, got)
		}
	}
}

// TestRetryingOnlyRetriesNetworkFailures pins the restriction that is the whole
// design: an expired credential, a missing issue and a rate limit are all
// perfectly repeatable, so re-running them reproduces the identical error while
// spending N times the window — and dresses a deterministic failure as a flake.
func TestRetryingOnlyRetriesNetworkFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		class    ghteardown.FailureClass
		attempts int
	}{
		{"network is retried", ghteardown.FailureNetwork, 3},
		{"auth is not", ghteardown.FailureAuth, 1},
		{"rate limit is not", ghteardown.FailureRateLimit, 1},
		{"subject is not", ghteardown.FailureSubject, 1},
		{"unclassified is not", ghteardown.FailureUnclassified, 1},
	} {
		calls := 0
		snap := func(string, int) (Snapshot, error) {
			calls++
			return Snapshot{State: StateUnknown}, &ghteardown.LookupError{Class: tc.class, Msg: "boom"}
		}
		var slept time.Duration
		wrapped := Retrying(snap, 3, 2*time.Second, func(d time.Duration) { slept += d })
		if _, err := wrapped("drellem2/pogo", 1); err == nil {
			t.Fatalf("%s: no error", tc.name)
		}
		if calls != tc.attempts {
			t.Errorf("%s: %d attempts, want %d", tc.name, calls, tc.attempts)
		}
		if tc.attempts == 1 && slept != 0 {
			t.Errorf("%s: slept %s before giving up on a repeatable failure", tc.name, slept)
		}
	}
}

// TestRetryingRecordsThatTheFailureSurvivedTheRetries. An error that says
// "still failing after 3 attempts" cannot be mistaken for one unlucky sample —
// which is exactly the misreading that made 2026-08-04's batch look like twelve
// broken carriers.
func TestRetryingRecordsThatTheFailureSurvivedTheRetries(t *testing.T) {
	snap := func(string, int) (Snapshot, error) {
		return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
			Class: ghteardown.FailureNetwork, Msg: "no such host"}
	}
	_, err := Retrying(snap, 3, time.Second, func(time.Duration) {})("drellem2/pogo", 1)
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "still failing after 3 attempts") {
		t.Fatalf("error does not record that it survived the retries: %v", err)
	}
	if ClassifyLookupError(err) != ghteardown.FailureNetwork {
		t.Fatalf("wrapped error lost its class: %s", ClassifyLookupError(err))
	}
}

// TestRetryingReturnsTheFirstConclusiveAnswer: a blip followed by a success is
// a success, not a finding.
func TestRetryingReturnsTheFirstConclusiveAnswer(t *testing.T) {
	calls := 0
	snap := func(string, int) (Snapshot, error) {
		calls++
		if calls == 1 {
			return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
				Class: ghteardown.FailureNetwork, Msg: "connection reset"}
		}
		return Snapshot{State: StateOpen, Acknowledged: true}, nil
	}
	s, err := Retrying(snap, 3, time.Second, func(time.Duration) {})("drellem2/pogo", 1)
	if err != nil || s.State != StateOpen || !s.Acknowledged {
		t.Fatalf("got %+v, %v", s, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// TestPrefetchFetchesEachRefOnce is a correctness property before it is a
// performance one: two carriers on the same ref must be judged against the SAME
// reading, or a chained triage/build pair can be reported as disagreeing about
// an issue that changed between two lookups.
func TestPrefetchFetchesEachRefOnce(t *testing.T) {
	carriers := []Carrier{
		carrier("mg-a", "drellem2/pogo#1", "triage", time.Hour),
		carrier("mg-b", "drellem2/pogo#1", "build", time.Hour),
		carrier("mg-c", "drellem2/pogo#2", "build", time.Hour),
	}
	var mu sync.Mutex
	seen := map[int]int{}
	snap := func(repo string, number int) (Snapshot, error) {
		mu.Lock()
		seen[number]++
		mu.Unlock()
		return Snapshot{State: StateOpen, Acknowledged: true, Created: ago(time.Hour)}, nil
	}

	pf := Prefetch(carriers, snap, 2)
	for _, c := range carriers {
		if _, err := pf(c.Repo, c.Number); err != nil {
			t.Fatalf("prefetched lookup failed: %v", err)
		}
	}
	if seen[1] != 1 || seen[2] != 1 {
		t.Fatalf("fetch counts = %v, want one per distinct ref", seen)
	}
}

// TestPrefetchIsAPerformanceDecisionNotACoverageOne: a ref that was not in the
// prefetch set must be fetched on demand rather than answered with a miss. A
// cache that silently answers "unknown" for anything it does not hold would turn
// an optimisation into blindness — this package's own subject matter.
func TestPrefetchFallsBackForUnstagedRefs(t *testing.T) {
	fetched := 0
	snap := func(repo string, number int) (Snapshot, error) {
		fetched++
		return Snapshot{State: StateOpen, Acknowledged: true}, nil
	}
	pf := Prefetch(nil, snap, 1)
	s, err := pf("drellem2/pogo", 42)
	if err != nil || s.State != StateOpen {
		t.Fatalf("got %+v, %v", s, err)
	}
	if fetched != 1 {
		t.Fatalf("fetched = %d, want the on-demand fallback to have run", fetched)
	}
}

// TestPrefetchPreservesErrors: a prefetch that swallowed the error and served a
// zero Snapshot would move every network failure from the loud "not re-read"
// bucket into a silent unknown.
func TestPrefetchPreservesErrors(t *testing.T) {
	want := errors.New("dial tcp: no such host")
	pf := Prefetch([]Carrier{carrier("mg-a", "drellem2/pogo#1", "build", time.Hour)},
		func(string, int) (Snapshot, error) { return Snapshot{State: StateUnknown}, want }, 1)
	if _, err := pf("drellem2/pogo", 1); !errors.Is(err, want) {
		t.Fatalf("prefetch lost the error: %v", err)
	}
}

// TestStatusesNameTheCoverage. A report that implied it saw everything would be
// making the claim this package exists to catch — so the covered statuses are
// reported, and the shelved boundary is visible in them.
func TestStatusesNameTheCoverage(t *testing.T) {
	if got := strings.Join((MGSource{}).Statuses(), " "); got != "available claimed pending" {
		t.Fatalf("default statuses = %q", got)
	}
	if got := strings.Join((MGSource{IncludeShelved: true}).Statuses(), " "); got != "available claimed pending shelved" {
		t.Fatalf("shelved statuses = %q", got)
	}
	// The two must not alias: Statuses returns a copy, or a caller appending to
	// one source's list would silently widen another's.
	a := (MGSource{}).Statuses()
	a[0] = "mutated"
	if (MGSource{}).Statuses()[0] != "available" {
		t.Fatal("Statuses returned a shared backing array")
	}
}
