package refinery

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// incidentGateDNS is the tail of the gate output the refinery actually recorded
// for mr-d9v8pgatjv1vk5gh576g on 2026-08-14, copied verbatim out of
// ~/.pogo/refinery-state.json. It is a fixture rather than a hand-written
// approximation for the reason mg-b41f's fixture is: the whole complaint is
// about what a real report said, and a synthesised one can be made to say
// whatever the test wants.
//
// Note what is in it besides the module-fetch line. The same outage broke a
// shell suite further down, so this specimen also carries a genuine-looking
// `FAIL: SETUP: ...` — which is why "can this mask a real defect" is answered
// with a retry rather than with a promise that the two never co-occur.
const incidentGateDNS = `Testing Go packages (per-package budget: 20m; override with POGO_GO_TEST_TIMEOUT)
ok  	github.com/drellem2/pogo/cmd/pogo	43.571s
ok  	github.com/drellem2/pogo/internal/agent	358.827s
: dial tcp: lookup proxy.golang.org: no such host
# github.com/drellem2/pogo/internal/agent
internal/agent/terminal.go:9:2: nhooyr.io/websocket@v1.8.17: Get "https://proxy.golang.org/nhooyr.io/websocket/@v/v1.8.17.zip": dial tcp: lookup proxy.golang.org: no such host
FAIL	github.com/drellem2/pogo/internal/agent [setup failed]
FAIL

Test 5: repeated runs do not grow the testtmp root
  FAIL: SETUP: the fixture-creating suite did not pass on the third run
`

// TestGateDNSFailureIsInfrastructureNotADefect is mg-67c9's headline, stated
// against the recorded disposition it replaces:
//
//	class=defect  signal="stage=build"
//	NOT RETRIED — the build gate ran on this tree and returned a verdict —
//	              re-running establishes the same fact
//
// The same night, the same DNS fault at the FETCH stage was classed
// infrastructure, retried, and merged on attempt 11. Same cause, opposite
// disposition, and pipeline position was the whole of the difference.
func TestGateDNSFailureIsInfrastructureNotADefect(t *testing.T) {
	gne := newGateNetworkError("./build.sh", incidentGateDNS, errors.New("exit status 1"))
	if gne == nil {
		t.Fatal("the incident's own gate output was not recognised as a gate-network failure")
	}
	// "build" is the stage that made this a defect: verdictStages is consulted
	// on the stage alone, and the stage is right.
	d := classifyFailure("build", "", gne)
	if d.Class != ClassInfrastructure {
		t.Fatalf("class = %s, want %s — a name that would not resolve is not a fact about the branch", d.Class, ClassInfrastructure)
	}
	if !d.Retryable {
		t.Error("retryable = false — the retry that would have resolved this is the whole ticket; attempt 11 merged that night")
	}
	if !d.GateRerun {
		t.Error("GateRerun = false — a retry here costs a whole gate run and must not spend the fetch-stage budget")
	}
	if !strings.Contains(d.Signal, "gate-network") || !strings.Contains(d.Signal, "proxy.golang.org") {
		t.Errorf("the signal must name the evidence that decided the class, got %q", d.Signal)
	}
	if countsAgainstAuthor(ClassInfrastructure) {
		t.Error("a DNS outage must not accumulate into the author's consecutive-failure streak")
	}
}

// TestGateDNSFailureIsClassifiedFromTheSameLine pins the guard the crossing
// rests on. Neither half is sufficient on its own: a bare network wording
// anywhere in gate output is the thing failureclass.go's boundary forbids
// matching, and a bare module-proxy mention is just a URL.
func TestGateDNSFailureIsClassifiedFromTheSameLine(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"the incident, both on one line", incidentGateDNS, true},
		{
			// The case the boundary was written to refuse: a real assertion
			// failure whose message happens to carry network wording.
			name:   "a red test that prints network wording",
			output: "--- FAIL: TestDialsTheThing (0.01s)\n    dial_test.go:9: got \"connection refused\", want nil\nFAIL\tgithub.com/drellem2/pogo/internal/agent\t0.030s\n",
			want:   false,
		},
		{
			name:   "network wording and a marker on DIFFERENT lines",
			output: "go: downloading nhooyr.io/websocket v1.8.17\n--- FAIL: TestThing (0.01s)\n    thing_test.go:9: no such host\n",
			want:   false,
		},
		{
			name:   "a marker with no network wording at all",
			output: "go: downloading nhooyr.io/websocket v1.8.17\n--- FAIL: TestThing (0.01s)\n    thing_test.go:9: got 1, want 2\n",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, ok := outputReportsGateNetworkFailure(tc.output)
			if ok != tc.want {
				t.Fatalf("outputReportsGateNetworkFailure = %v, want %v", ok, tc.want)
			}
			// And the classification that follows from it, since that is what
			// actually reaches a reader.
			var err error = errors.New("exit status 1")
			if gne := newGateNetworkError("./build.sh", tc.output, err); gne != nil {
				err = gne
			}
			d := classifyFailure("build", "", err)
			wantClass := ClassDefect
			if tc.want {
				wantClass = ClassInfrastructure
			}
			if d.Class != wantClass {
				t.Fatalf("class = %s, want %s", d.Class, wantClass)
			}
		})
	}
}

// TestGateNetworkIgnoresAHarnessVerdictLine is this fix checked for the defect
// it repairs.
//
// The summariser bug mg-67c9 uncovered was a substring scan landing inside a
// PASSING control line that merely quoted the wording it was hunting for. This
// repo's net-control.sh suite tests network-probe behaviour, so a pass line
// quoting a resolver failure is not hypothetical — and if one were read as
// evidence, a genuinely red gate would be demoted to infrastructure and retried.
func TestGateNetworkIgnoresAHarnessVerdictLine(t *testing.T) {
	output := "--- 3. resolver failures ---\n" +
		"  PASS: the probe retries when it gets \"dial tcp: lookup proxy.golang.org: no such host\"\n" +
		"  FAIL: the probe did not report \"dial tcp: lookup proxy.golang.org: no such host\" as unreachable\n" +
		"=== net-control.sh: 14 passed, 4 failed ===\n"
	if _, _, _, _, ok := outputReportsGateNetworkFailure(output); ok {
		t.Fatal("a suite's own verdict lines were read as the gate's network failure — the remedy has the defect it remedies")
	}
	if d := classifyFailure("build", "", errors.New("exit status 1")); d.Class != ClassDefect {
		t.Fatalf("class = %s, want %s — a red network-probe suite is a finding about the branch", d.Class, ClassDefect)
	}
}

// TestGateNetworkIsDecidedBeforeTheOutputIsCapped is the same requirement
// mg-b41f pinned for the host class, and it is the reason the classification
// cannot live in classifyFailure's text tables alone.
//
// Two separate facts make the pre-cap reading the only workable one. The copy of
// the gate output persisted on the merge request is capped to 8 KiB with its
// middle elided — mr-d9v8pgatjv1vk5gh576g lost 4962 of its 12996 bytes that way.
// And classifyFailure is never handed the gate output at all: for a gate failure
// the wrapped error is the one-line summary, so the raw string it matches on is
// `quality gate: ./build.sh failed [internal/agent]: exit status 1`.
func TestGateNetworkIsDecidedBeforeTheOutputIsCapped(t *testing.T) {
	filler := strings.Repeat("ok  \tgithub.com/drellem2/pogo/internal/filler\t0.01s\n", 200)
	full := filler + "internal/agent/terminal.go:9:2: nhooyr.io/websocket@v1.8.17: Get \"https://proxy.golang.org/nhooyr.io/websocket/@v/v1.8.17.zip\": dial tcp: lookup proxy.golang.org: no such host\n" + filler

	if _, _, _, _, ok := outputReportsGateNetworkFailure(full); !ok {
		t.Fatal("the full output does not report the condition — fixture is wrong")
	}
	capped := capGateOutputTo(full, 2048)
	if !gateOutputWasCapped(capped) {
		t.Fatal("fixture did not exceed the cap")
	}
	if _, _, _, _, ok := outputReportsGateNetworkFailure(capped); ok {
		t.Skip("this fixture's evidence survived the cap; the ordering requirement is unchanged")
	}
	if gne := newGateNetworkError("./build.sh", full, errors.New("exit status 1")); gne == nil {
		t.Fatal("classification from the FULL output failed — the pre-cap reading is the only one that works")
	}

	// The other half: the summary is all classifyFailure gets, and it carries no
	// network wording whatsoever. This is what a patterns-only fix would have
	// been matching against.
	summary := "quality gate: ./build.sh failed [internal/agent]: exit status 1"
	if d := classifyFailure("build", summary, errors.New(summary)); d.Class != ClassDefect {
		t.Fatalf("class = %s — the recorded summary carries no network wording, so text alone cannot reach the right answer", d.Class)
	}
}

// TestAKilledGateOutranksTheNetworkText pins the ordering inside
// classifyFailure. This is the one carve-out that RETRIES, and a retry of a
// timed-out gate costs another full timeout of the merge slot. A gate that hung
// having earlier printed a module-fetch warning must stay INDETERMINATE.
// This is the real path, not a contrived one: runQualityGates reaches the
// gate-network check with whatever runGate returned, so a timed-out gate whose
// output happens to carry a module-fetch line arrives here as a gateNetworkError
// WRAPPING a gateTimeoutError. classifyFailure's ordering is the only thing that
// unpicks it.
func TestAKilledGateOutranksTheNetworkText(t *testing.T) {
	to := &gateTimeoutError{Gate: "./build.sh", Timeout: time.Hour, Elapsed: time.Hour}
	gne := newGateNetworkError("./build.sh", incidentGateDNS, to)
	if gne == nil {
		t.Fatal("fixture is wrong")
	}
	d := classifyFailure("build", "", gne)
	if d.Class != ClassIndeterminate {
		t.Fatalf("class = %s, want %s — a kill says nothing about whether the branch caused the hang", d.Class, ClassIndeterminate)
	}
	if d.Retryable {
		t.Error("retryable = true — three more full timeouts of the merge slot would answer nothing")
	}
}

// TestGateNetworkRetryBudgetIsSizedForAGateRun is the guard on the number that
// actually costs something. The fetch-stage budget is 28 attempts because a
// retried `git fetch` is cheap; a retried GATE is not. The two measured gate
// runs on the night of the incident took 6m18s and 12m46s, so spending 28 here
// would hold one repo's lane for hours.
func TestGateNetworkRetryBudgetIsSizedForAGateRun(t *testing.T) {
	if gateNetworkMaxAttempts >= networkMaxAttempts {
		t.Fatalf("gateNetworkMaxAttempts = %d, networkMaxAttempts = %d — a retry that re-runs the gate must not be granted the budget written for a git command",
			gateNetworkMaxAttempts, networkMaxAttempts)
	}
	var total time.Duration
	for i := 1; i < gateNetworkMaxAttempts; i++ {
		total += gateNetworkBackoffFor(i)
	}
	// The clock backstop is shared with the fetch campaign, and the schedule has
	// to fit inside it or the reason a campaign ended becomes an ambiguous race
	// between two bounds — the drift failureclass.go's own backstop note warns
	// about.
	if total > networkRetryBudget {
		t.Fatalf("the gate-network schedule sleeps %s, past the %s shared backstop", total, networkRetryBudget)
	}
	// And it must not be so short that it cannot outlast a blip. One minute is
	// less than a single gate run on this repo; a schedule under that is
	// decorative.
	if total < 5*time.Minute {
		t.Fatalf("the gate-network schedule sleeps only %s — too short to outlast anything, given each retry also re-runs the gate", total)
	}
}

// TestGateNetworkRetriesSpendTheirOwnBudget is the behavioural half: the two
// budgets are separate, so a gate-network campaign cannot consume the attempts
// that exist to wait out a fetch-stage outage, and vice versa.
func TestGateNetworkRetriesSpendTheirOwnBudget(t *testing.T) {
	gne := newGateNetworkError("./build.sh", incidentGateDNS, errors.New("exit status 1"))
	fetchFail := errors.New("ssh: connect to host github.com port 22: Undefined error: 0")

	gateDisp := classifyFailure("build", "", gne)
	fetchDisp := classifyFailure("fetch", fetchFail.Error(), fetchFail)

	if gateDisp.Class != fetchDisp.Class {
		t.Fatalf("the same root cause got different classes: gate=%s fetch=%s", gateDisp.Class, fetchDisp.Class)
	}
	if !gateDisp.GateRerun {
		t.Error("the gate failure must be marked GateRerun")
	}
	if fetchDisp.GateRerun {
		t.Error("a fetch failure must NOT be marked GateRerun — it would be charged the small budget and stop early")
	}
}

// TestDefectNoteStatesItsOwnCommitment. `DEFECT` does not merely label; it
// commits to "re-running establishes the same fact", and that commitment is what
// suppresses the retry. It was silently false for the DNS failure above. Saying
// it where a reader sees it is the only part of this fix that helps with a case
// nobody has classified yet.
func TestDefectNoteStatesItsOwnCommitment(t *testing.T) {
	note := ClassDefect.TriageNote()
	if !strings.Contains(note, "re-running establishes the SAME fact") {
		t.Errorf("the DEFECT triage note must state the commitment that suppresses the retry, got: %q", note)
	}
	if !strings.Contains(note, "false") {
		t.Errorf("the note must tell a reader what to do when the commitment is untrue, got: %q", note)
	}
	// The control: no other class may borrow that sentence. It is the one thing
	// that separates DEFECT from every class that establishes nothing.
	for _, c := range allFailureClasses {
		if c == ClassDefect {
			continue
		}
		if strings.Contains(c.TriageNote(), "re-running establishes the SAME fact") {
			t.Errorf("%s borrowed DEFECT's commitment", c)
		}
	}
}
