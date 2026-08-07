package revcheck

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/selfdrift"
)

// The property this whole package exists for: an answer that could not be
// measured must never arrive at a caller as agreement. Every absence sentinel
// gets its own row on both sides, because the defect being removed is exactly
// the one where two different absences collapse into one passing comparison.
func TestAnUnreadableSideIsUnknownAndNeverAgreement(t *testing.T) {
	const rev = "73757a8ffffffffffffffffffffffffffffffff0"

	cases := []struct {
		name            string
		running         string
		expected        string
		want            Verdict
		reasonMentions  string
		explicitlyNotOK bool
	}{
		{name: "both read and equal", running: rev, expected: rev, want: Agrees},
		{name: "both read and different", running: rev, expected: "d31297f" + strings.Repeat("0", 33), want: Differs, explicitlyNotOK: true},

		{name: "daemon not answering", running: RevUnreachable, expected: rev, want: Unknown, reasonMentions: "/version", explicitlyNotOK: true},
		{name: "daemon carries no stamp", running: RevUnstamped, expected: rev, want: Unknown, reasonMentions: "vcs.revision", explicitlyNotOK: true},
		{name: "daemon revision empty", running: "", expected: rev, want: Unknown, explicitlyNotOK: true},

		{name: "expected binary missing", running: rev, expected: RevMissing, want: Unknown, reasonMentions: "not on disk", explicitlyNotOK: true},
		{name: "expected binary unstamped", running: rev, expected: RevUnstamped, want: Unknown, reasonMentions: "vcs.revision", explicitlyNotOK: true},
		{name: "expected unset", running: rev, expected: "", want: Unknown, explicitlyNotOK: true},

		// Both sides absent is the case most likely to be read as "nothing to
		// report". It is the least informative state there is, and it must land
		// in UNKNOWN rather than in the "" == "" trap.
		{name: "both sides absent", running: "", expected: "", want: Unknown, explicitlyNotOK: true},
		{name: "both sides unstamped", running: RevUnstamped, expected: RevUnstamped, want: Unknown, explicitlyNotOK: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compare(tc.running, tc.expected)
			if got.Verdict != tc.want {
				t.Fatalf("Compare(%q, %q) verdict = %s, want %s", tc.running, tc.expected, got.Verdict, tc.want)
			}
			if tc.explicitlyNotOK && got.OK() {
				t.Fatalf("Compare(%q, %q) reported OK; a non-Agrees verdict must never be reportable as success", tc.running, tc.expected)
			}
			if tc.want == Unknown && got.Reason == "" {
				t.Fatalf("Compare(%q, %q) is UNKNOWN with no reason — an unexplained UNKNOWN is what a caller silently drops", tc.running, tc.expected)
			}
			if tc.reasonMentions != "" && !strings.Contains(got.Reason, tc.reasonMentions) {
				t.Fatalf("reason %q does not mention %q", got.Reason, tc.reasonMentions)
			}
		})
	}
}

// A verdict that gets logged must lead with the verdict. The eight-day incident
// this package answers was survivable partly because the readings that were
// taken read like success at a glance.
func TestStringLeadsWithTheVerdictAndShowsBothSides(t *testing.T) {
	r := Compare("d31297f"+strings.Repeat("a", 33), "73757a8"+strings.Repeat("b", 33))
	s := r.String()
	if !strings.HasPrefix(s, "revision check DIFFERS") {
		t.Fatalf("String() = %q; the verdict must come first", s)
	}
	if !strings.Contains(s, "d31297fa") || !strings.Contains(s, "73757a8b") {
		t.Fatalf("String() = %q; both revisions must be visible", s)
	}

	u := Compare(RevUnreachable, "73757a8"+strings.Repeat("b", 33))
	us := u.String()
	if !strings.HasPrefix(us, "revision check UNKNOWN") {
		t.Fatalf("String() = %q; UNKNOWN must be stated, not implied", us)
	}
	// Truncating a sentinel to 8 chars would render "<unreach" — which reads
	// like a short hash. The sentinel must survive shortening intact.
	if !strings.Contains(us, RevUnreachable) {
		t.Fatalf("String() = %q; the sentinel must not be truncated into something that looks like a hash", us)
	}
}

// Wait must poll THROUGH the transient states of a restart. For a few seconds
// after a kickstart the old process is still answering with the old revision,
// and after that nothing answers at all; a check that concluded on the first
// sample would report DIFFERS on every healthy restart.
func TestWaitPollsThroughTheTransientStatesOfARestart(t *testing.T) {
	const old = "d31297f" + "0000000000000000000000000000000"
	const want = "73757a8" + "1111111111111111111111111111111"

	samples := []string{old, old, RevUnreachable, RevUnreachable, want}
	i := 0
	clock := time.Unix(0, 0)

	res := Wait(Options{
		Expected: want,
		Timeout:  60 * time.Second,
		Interval: 3 * time.Second,
		running: func() string {
			s := samples[i]
			if i < len(samples)-1 {
				i++
			}
			return s
		},
		sleep: func(d time.Duration) { clock = clock.Add(d) },
		now:   func() time.Time { return clock },
	})

	if res.Verdict != Agrees {
		t.Fatalf("Wait settled on %s (%s); a restart that converges within the window must be reported as AGREES", res.Verdict, res)
	}
	if i != len(samples)-1 {
		t.Fatalf("Wait consumed %d samples, expected to reach the last (%d)", i, len(samples)-1)
	}
	if res.Waited == 0 {
		t.Fatalf("Wait reported zero elapsed time after %d probes", i)
	}
}

// The failing shape: the daemon comes back, is healthy, answers /version — and
// is running the SAME binary it was before. Every path except the deploy
// script's called that a successful restart.
func TestWaitReportsAStaleDaemonThatCameBackHealthy(t *testing.T) {
	const stale = "d31297f" + "0000000000000000000000000000000"
	const want = "73757a8" + "1111111111111111111111111111111"

	clock := time.Unix(0, 0)
	probes := 0
	res := Wait(Options{
		Expected: want,
		Timeout:  60 * time.Second,
		Interval: 3 * time.Second,
		running:  func() string { probes++; return stale },
		sleep:    func(d time.Duration) { clock = clock.Add(d) },
		now:      func() time.Time { return clock },
	})

	if res.Verdict != Differs {
		t.Fatalf("Wait = %s, want DIFFERS — a daemon that never adopts the new revision must not pass", res)
	}
	if res.OK() {
		t.Fatal("Wait reported OK for a daemon still on the old revision")
	}
	if res.Running != stale || res.Expected != want {
		t.Fatalf("Wait = %+v; both revisions must survive into the result so the caller can print them", res)
	}
	// Bounded: it stops at the deadline instead of spinning.
	if probes < 2 || probes > 25 {
		t.Fatalf("Wait made %d probes over a 60s/3s budget; expected it to poll and then stop", probes)
	}
	if clock.Sub(time.Unix(0, 0)) > 60*time.Second {
		t.Fatalf("Wait slept past its own deadline (%s)", clock.Sub(time.Unix(0, 0)))
	}
}

// A daemon that never answers is UNKNOWN, not DIFFERS: we did not measure a
// disagreement, we failed to measure at all, and the remedies differ.
func TestWaitOnADaemonThatNeverAnswersIsUnknown(t *testing.T) {
	clock := time.Unix(0, 0)
	res := Wait(Options{
		Expected: "73757a8" + "1111111111111111111111111111111",
		Timeout:  30 * time.Second,
		Interval: 3 * time.Second,
		running:  func() string { return RevUnreachable },
		sleep:    func(d time.Duration) { clock = clock.Add(d) },
		now:      func() time.Time { return clock },
	})
	if res.Verdict != Unknown {
		t.Fatalf("Wait = %s, want UNKNOWN", res)
	}
	if res.OK() {
		t.Fatal("an unreachable daemon was reported as OK")
	}
}

// An unknown EXPECTED cannot be resolved by probing, so Wait must not spend the
// timeout on it — that would be a pure 60s delay inside a restart path.
func TestWaitDoesNotPollWhenTheExpectedRevisionIsUnknowable(t *testing.T) {
	for _, expected := range []string{"", RevMissing, RevUnstamped} {
		probes := 0
		clock := time.Unix(0, 0)
		res := Wait(Options{
			Expected: expected,
			Timeout:  60 * time.Second,
			Interval: 3 * time.Second,
			running:  func() string { probes++; return "73757a8" },
			sleep:    func(d time.Duration) { clock = clock.Add(d) },
			now:      func() time.Time { return clock },
		})
		if res.Verdict != Unknown {
			t.Fatalf("expected=%q: verdict %s, want UNKNOWN", expected, res.Verdict)
		}
		if probes != 0 {
			t.Fatalf("expected=%q: probed the daemon %d times for an answer that cannot change", expected, probes)
		}
		if clock != time.Unix(0, 0) {
			t.Fatalf("expected=%q: slept %s waiting for an unknowable expectation", expected, clock.Sub(time.Unix(0, 0)))
		}
	}
}

// The probes are selfdrift's — this package adds the poll, not the reading. So
// the coverage here is that the wiring is real (a live /version answer reaches
// Compare unchanged) and that the sentinels survive the trip; the exhaustive
// per-failure-mode table for the readers themselves lives in
// internal/selfdrift, where duplicating it would mean two copies to keep
// honest, which is the shape mg-ed4a is about.
func TestWaitReadsARealDaemonThroughTheSharedProbe(t *testing.T) {
	const want = "73757a8" + "1111111111111111111111111111111"

	t.Run("a live daemon on the expected revision agrees", func(t *testing.T) {
		var probed string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			probed = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"revision":%q,"time":"2026-08-07T00:00:00Z","modified":false}`, want)
		}))
		defer srv.Close()

		res := Wait(Options{BaseURL: srv.URL, Expected: want, Timeout: 5 * time.Second, Interval: time.Millisecond})
		if res.Verdict != Agrees {
			t.Fatalf("Wait = %s, want AGREES", res)
		}
		if probed != "/version" {
			t.Fatalf("probed %q, want /version — /health is what could not see this", probed)
		}
	})

	t.Run("a live daemon on a different revision differs", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"revision":"d31297f0000000000000000000000000000000ab"}`)
		}))
		defer srv.Close()

		res := Wait(Options{BaseURL: srv.URL, Expected: want, Timeout: 30 * time.Millisecond, Interval: 10 * time.Millisecond})
		if res.Verdict != Differs {
			t.Fatalf("Wait = %s, want DIFFERS", res)
		}
	})

	t.Run("nothing listening is UNKNOWN, not agreement", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()

		res := Wait(Options{BaseURL: url, Expected: want, Timeout: 30 * time.Millisecond, Interval: 10 * time.Millisecond})
		if res.Verdict != Unknown {
			t.Fatalf("Wait = %s, want UNKNOWN", res)
		}
		if res.OK() {
			t.Fatal("a daemon that is not there was reported as OK")
		}
	})
}

// The sentinels are selfdrift's constants, not copies of them. If either
// package ever grows its own spelling, a restart-path log line and a `pogo
// service status` row would start describing the same state with different
// words — and the comparison between them would silently stop working.
func TestSentinelsAreSharedWithSelfdriftNotDuplicated(t *testing.T) {
	if RevUnreachable != selfdrift.RevUnreachable ||
		RevUnstamped != selfdrift.RevUnstamped ||
		RevMissing != selfdrift.RevMissing {
		t.Fatal("revcheck's sentinels have drifted from selfdrift's")
	}
	for _, s := range []string{RevUnreachable, RevUnstamped, RevMissing} {
		if !IsSentinel(s) {
			t.Fatalf("%q is not recognised as a sentinel", s)
		}
		if Short(s) != s {
			t.Fatalf("Short(%q) = %q; a truncated sentinel reads like a hash", s, Short(s))
		}
	}
}

// AGREES against the on-disk binary means "the restart took", NOT "the daemon
// is current" — and the two answers can disagree, which is why the expectation
// is an argument. Replays the reading measured on this box on 2026-08-07, so
// the distinction survives the box being fixed: the same live daemon AGREES
// with the binary launchd execs and DIFFERS from main's HEAD, simultaneously.
//
// If this ever collapses to one verdict, someone has decided for the caller
// what "supposed to be running" means — which is the green-on-an-adjacent-
// property mistake this package exists to remove, reintroduced one level up.
func TestTheSameDaemonCanAgreeWithTheDiskAndDifferFromMain(t *testing.T) {
	const running = "d31297f493cdd757fc46654351e0a2c93e66f49b" // live, 2026-07-30 build
	const onDisk = "d31297f493cdd757fc46654351e0a2c93e66f49b"  // ~/go/bin/pogod, equally stale
	const main = "22e0541f7fd219ca30f09a84a2e1262a89afb65d"    // origin/main that night

	if got := Compare(running, onDisk); got.Verdict != Agrees {
		t.Fatalf("against the binary launchd execs: %s, want AGREES — the restart did put the on-disk binary into the process", got)
	}
	if got := Compare(running, main); got.Verdict != Differs {
		t.Fatalf("against main's HEAD: %s, want DIFFERS — the disk itself was 8 days stale", got)
	}
}

// Delegation check: BinaryRevision must answer for a path with nothing at it,
// and its answer must not be reportable as agreement.
func TestBinaryRevisionOfAMissingBinaryIsNeverAgreement(t *testing.T) {
	got := BinaryRevision(filepath.Join(t.TempDir(), "no-such-pogod"))
	if got != RevMissing {
		t.Fatalf("BinaryRevision(missing) = %q, want %s", got, RevMissing)
	}
	if Compare("73757a8", got).OK() {
		t.Fatal("a missing expected binary was reported as agreement")
	}
}
