package refinery

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// incidentSetupMisclassified is the error string the refinery actually recorded
// for mr-da2ls4qtjv1vk5gh57n0 on 2026-08-19, copied out of
// ~/.pogo/refinery-state.json. It is the whole ticket in one line: the sentence
// says the setup failed and NOT the branch, and the class recorded beside it
// said `defect`, "establishes a fact about the branch. A fix is warranted."
const incidentSetupMisclassified = `quality gate: ./build.sh failed [test setup failed, not the branch: ` +
	`PASS: a sandbox HOME that is a symlink to the developer's home: prints the SETUP FAILURE banner]: exit status 1`

// incidentPassingControl is the gate output that produced it — a suite whose
// PASSING assertion NAMES the banner it asserts gets printed, plus the run's
// real failure further down.
const incidentPassingControl = `--- 2. a sandbox HOME that is a symlink to the developer's home ---
PASS: a sandbox HOME that is a symlink to the developer's home: prints the SETUP FAILURE banner
--- 7. namespace hygiene ---
  PASS: sourcing the library leaves the caller's NC and probe_tcp alone

=== net-control.sh: 14 passed, 4 failed ===
`

// genuineBanner is a real setup failure: internal/testsandbox's banner, with the
// assertion wreckage mg-3412 measured underneath it.
const genuineBanner = `=== RUN   TestSomething
SETUP FAILURE (internal/testsandbox): POGO_HOME resolves onto the live tree /Users/daniel/.pogo
--- FAIL: TestSomething (0.00s)
--- FAIL: TestSomethingElse (0.00s)
FAIL	github.com/drellem2/pogo/internal/agent	0.104s
`

func fastSetupRetries(t *testing.T) {
	t.Helper()
	saved := gateSetupRetryBackoff
	gateSetupRetryBackoff = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { gateSetupRetryBackoff = saved })
}

// TestAGenuineSetupFailureIsNotADefect is mg-15bb's headline, stated against the
// disposition it replaces. Before this, a gate that bannered its own setup
// failure reached verdictStages["build"] and came back
//
//	class=defect  signal="stage=build"
//	NOT RETRIED — the build gate ran on this tree and returned a verdict —
//	              re-running establishes the same fact
//
// while the summariser, one function earlier in the same process, had already
// written "test setup failed, not the branch" into the error text beside it.
func TestAGenuineSetupFailureIsNotADefect(t *testing.T) {
	gse := newGateSetupError("./build.sh", genuineBanner, errors.New("exit status 1"))
	if gse == nil {
		t.Fatal("a real testsandbox banner was not recognised as a setup failure")
	}
	// "build" is the stage that made this a defect: verdictStages is consulted
	// on the stage alone, and the stage is right.
	d := classifyFailure("build", "", gse)
	if d.Class != ClassSetup {
		t.Fatalf("class = %s, want %s — a gate that never stood its environment up returned no verdict on the tree", d.Class, ClassSetup)
	}
	if !d.Retryable {
		t.Error("retryable = false — requirement 2 of the ticket; one re-run is what separates 'broken once' from 'broken standing'")
	}
	if !d.GateRerun {
		t.Error("GateRerun = false — a retry here costs a whole gate run and must not spend the fetch-stage budget")
	}
	if !strings.Contains(d.Signal, "gate-setup") || !strings.Contains(d.Signal, "testsandbox") {
		t.Errorf("the signal must name the evidence that decided the class, got %q", d.Signal)
	}
	if d.RetryReason == "" {
		t.Error("a retried class with no stated purpose is a loop, not a policy — requirement 2")
	}
	if countsAgainstAuthor(ClassSetup) {
		t.Error("a broken gate environment must not accumulate into the author's consecutive-failure streak")
	}
	// The assertion names under a broken envelope are not findings, and the
	// error must not lead with them.
	if strings.Contains(strings.SplitN(gse.Error(), "\n", 2)[0], "TestSomething") {
		t.Errorf("the headline offers an assertion name under a broken sandbox: %q", gse.Error())
	}
}

// TestTheRecordedSpecimenIsNotASetupFailureAtAll is the ticket's CORRECTION,
// pinned. The line the refinery quoted as the cause begins `PASS:` — it is an
// assertion in scripts/pogo-sandbox_test.sh that went GREEN, and the words SETUP
// FAILURE are the NAME of the banner it asserts gets printed. Nothing had failed
// to set up; the run's real failure was a network positive control.
//
// So this class must NOT claim that specimen. A remedy that took a red gate,
// called it a setup failure, retried it three times and cleared its author would
// be strictly worse than the misclassification it replaces.
func TestTheRecordedSpecimenIsNotASetupFailureAtAll(t *testing.T) {
	// POSITIVE CONTROL: the fixture really does contain the substring a naive
	// reading matches, so a green result here cannot come from a fixture that
	// never exercised the defect.
	if !strings.Contains(incidentPassingControl, "SETUP FAILURE") {
		t.Fatal("the fixture does not contain the substring the discarded reading matched — it is not a specimen")
	}
	if gse := newGateSetupError("./build.sh", incidentPassingControl, errors.New("exit status 1")); gse != nil {
		t.Fatalf("a PASSING control line was promoted to a setup failure: %q", gse.Banner)
	}
	d := classifyFailure("build", "", errors.New("exit status 1"))
	if d.Class != ClassDefect {
		t.Fatalf("class = %s, want %s — a red suite that merely names the banner is a finding about the branch", d.Class, ClassDefect)
	}
	// And the count must apply the same guard, or the field nobody audits
	// reintroduces the reading the classification rejected.
	if n := countSetupFailureLines(incidentPassingControl); n != 0 {
		t.Errorf("countSetupFailureLines = %d over passing controls, want 0", n)
	}
}

// TestTheSymmetricHalfIsNotExcused is the worse direction, and the same guard
// covers it: pogo-sandbox_test.sh's FAILING branch prints
// `printed no 'SETUP FAILURE' banner`, so a control that caught a REAL
// regression must not be waved through as a setup failure and retried.
func TestTheSymmetricHalfIsNotExcused(t *testing.T) {
	const output = `--- 2. a broken sandbox ---
FAIL: a sandbox HOME that is a symlink: printed no 'SETUP FAILURE' banner; output was: ok|ok|
=== pogo-sandbox.sh: 3 passed, 1 failed ===
`
	if gse := newGateSetupError("./build.sh", output, errors.New("exit status 1")); gse != nil {
		t.Fatalf("a control's own failure was excused as a setup failure: %q", gse.Banner)
	}
}

// TestEveryRealBannerStillReachesTheClass is the control on the guard: none of
// the three genuine banner forms in this repo opens with a harness verdict
// prefix, so the guard that discards the specimen above costs nothing real.
func TestEveryRealBannerStillReachesTheClass(t *testing.T) {
	for _, b := range []string{
		"SETUP FAILURE (internal/testsandbox): POGO_HOME resolves onto the live tree",
		"SETUP FAILURE: /repo has no git HEAD; this suite has nothing to compare against",
		"=== SETUP FAILURE — the sandbox could not be established (exit 3) ===",
	} {
		out := "Testing Go packages\n" + b + "\nFAIL\n"
		gse := newGateSetupError("./build.sh", out, errors.New("exit status 1"))
		if gse == nil {
			t.Errorf("banner %q no longer reaches the setup class", b)
			continue
		}
		if d := classifyFailure("build", "", gse); d.Class != ClassSetup {
			t.Errorf("banner %q classified %s", b, d.Class)
		}
	}
}

// TestSetupIsDecidedBeforeTheOutputIsCapped is the same requirement mg-b41f and
// mg-67c9 pinned for their classes, and the reason this cannot live in
// classifyFailure's text tables alone: the persisted copy of the gate output is
// capped with its middle elided, and classifyFailure is never handed the gate
// output at all — for a gate failure the wrapped error is the one-line summary.
func TestSetupIsDecidedBeforeTheOutputIsCapped(t *testing.T) {
	filler := strings.Repeat("ok  \tgithub.com/drellem2/pogo/internal/filler\t0.01s\n", 200)
	full := filler + "SETUP FAILURE (internal/testsandbox): POGO_HOME resolves onto the live tree\n" + filler

	if _, ok := reportedSetupFailureLine(full); !ok {
		t.Fatal("the full output does not report the condition — fixture is wrong")
	}
	capped := capGateOutputTo(full, 2048)
	if !gateOutputWasCapped(capped) {
		t.Fatal("fixture did not exceed the cap")
	}
	if _, ok := reportedSetupFailureLine(capped); ok {
		t.Skip("this fixture's evidence survived the cap; the ordering requirement is unchanged")
	}
	if gse := newGateSetupError("./build.sh", full, errors.New("exit status 1")); gse == nil {
		t.Fatal("classification from the FULL output failed — the pre-cap reading is the only one that works")
	}

	// The other half. This is the exact string the refinery recorded for
	// mr-da2ls4qtjv1vk5gh57n0, and it is all classifyFailure would have got: it
	// SAYS "test setup failed, not the branch" and a text-only fix would have
	// matched on that — and been wrong, because the specimen was a red gate.
	d := classifyFailure("build", incidentSetupMisclassified, errors.New(incidentSetupMisclassified))
	if d.Class != ClassDefect {
		t.Fatalf("class = %s — matching the recorded summary's prose is exactly the reading this ticket rejects", d.Class)
	}
}

// TestAKilledGateOutranksASetupBanner and its two siblings pin the ordering
// inside classifyFailure. Setup is the LAST of the four carve-outs, and each of
// the three above it either names something more specific or does not retry.
func TestAKilledGateOutranksASetupBanner(t *testing.T) {
	to := &gateTimeoutError{Gate: "./build.sh", Timeout: time.Hour, Elapsed: time.Hour}
	gse := newGateSetupError("./build.sh", genuineBanner, to)
	if gse == nil {
		t.Fatal("fixture is wrong")
	}
	if d := classifyFailure("build", "", gse); d.Class != ClassIndeterminate {
		t.Fatalf("class = %s, want %s — a kill says nothing about whether the branch caused the hang", d.Class, ClassIndeterminate)
	}
}

func TestAFullDiskOutranksASetupBanner(t *testing.T) {
	// A disk that cannot be written to is a plausible REASON a sandbox failed to
	// stand up, and "the host ran out of disk space" names something that
	// "setup failed" does not.
	out := genuineBanner + "link: mapping output file failed: no space left on device\n"
	hre := newHostResourceError("./build.sh", out, t.TempDir(), errors.New("exit status 1"))
	if hre == nil {
		t.Fatal("fixture is wrong")
	}
	gse := newGateSetupError("./build.sh", out, hre)
	if gse == nil {
		t.Fatal("fixture is wrong")
	}
	if d := classifyFailure("build", "", gse); d.Class != ClassHost {
		t.Fatalf("class = %s, want %s — free the resource first is the instruction that works", d.Class, ClassHost)
	}
}

func TestAGateNetworkFailureOutranksASetupBanner(t *testing.T) {
	out := genuineBanner + "internal/agent/terminal.go:9:2: nhooyr.io/websocket@v1.8.17: Get \"https://proxy.golang.org/nhooyr.io/websocket/@v/v1.8.17.zip\": dial tcp: lookup proxy.golang.org: no such host\n"
	gne := newGateNetworkError("./build.sh", out, errors.New("exit status 1"))
	if gne == nil {
		t.Fatal("fixture is wrong")
	}
	gse := newGateSetupError("./build.sh", out, gne)
	if gse == nil {
		t.Fatal("fixture is wrong")
	}
	if d := classifyFailure("build", "", gse); d.Class != ClassInfrastructure {
		t.Fatalf("class = %s, want %s — a same-line module-fetch failure is the narrower statement", d.Class, ClassInfrastructure)
	}
}

// TestSetupTriageNoteAssertsNoMoreThanTheClassSupports is requirement 3. The
// sentence that misdirected a worker was "establishes a fact about the branch",
// printed for a failure the gate attributed elsewhere; no note on this class may
// carry it, or anything that reads as it.
func TestSetupTriageNoteAssertsNoMoreThanTheClassSupports(t *testing.T) {
	note := ClassSetup.TriageNote()
	for _, forbidden := range []string{
		"establishes a fact about the branch",
		"A fix is warranted",
		"re-running establishes the SAME fact",
	} {
		if strings.Contains(note, forbidden) {
			t.Errorf("the setup note asserts %q, which the class does not support: %q", forbidden, note)
		}
	}
	if !strings.Contains(note, "do NOT dispatch a fix on this alone") {
		t.Errorf("the note must tell a coordinator not to dispatch a fix ON THIS ALONE — the unqualified form would assert more than the class supports: %q", note)
	}
	// It must also not borrow INFRASTRUCTURE's "resubmit" — by the time a human
	// reads this the refinery has already resubmitted it up to the budget, and
	// a reader who re-runs it by hand learns nothing.
	if !strings.Contains(note, "retried automatically") {
		t.Errorf("the note must say the retries already happened: %q", note)
	}
	// A clearance is as wrong as a condemnation, and this class asserts neither.
	if !strings.Contains(note, "did not clear it") {
		t.Errorf("the note must refuse the clearance as well as the accusation: %q", note)
	}
}

// TestSetupClassNeverBlamesTheEnvironment is mg-67c9's standing ruling, pinned
// as a test rather than honoured in a comment. That ticket looked at exactly
// this and refused it: "a branch can break its own test setup, so 'the envelope
// did not stand up' does not establish that its collapse was environmental."
//
// The ruling is not overturned here — it is what bounds what this class may say.
// A note reading "fix the environment the gate runs in" would be requirement 3's
// own defect committed in the opposite direction: a caption asserting more than
// the class supports, aimed at the box instead of the branch.
func TestSetupClassNeverBlamesTheEnvironment(t *testing.T) {
	gse := newGateSetupError("./build.sh", genuineBanner, errors.New("exit status 1"))
	if gse == nil {
		t.Fatal("fixture is wrong")
	}
	surfaces := map[string]string{
		"triage note":  ClassSetup.TriageNote(),
		"error text":   gse.Error(),
		"retry reason": classifyFailure("build", "", gse).RetryReason,
	}
	for name, text := range surfaces {
		for _, forbidden := range []string{
			"fix the environment",
			"the environment is at fault",
			"the box",
		} {
			if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) &&
				!strings.Contains(strings.ToLower(text), "and so can the box") {
				t.Errorf("the %s blames the environment (%q), which the class does not establish: %q", name, forbidden, text)
			}
		}
	}
	// And the two surfaces a reader acts on must say the ambiguity out loud
	// rather than merely avoid resolving it — silence reads as the accusation
	// the reader already had in mind.
	for _, name := range []string{"triage note", "error text"} {
		if !strings.Contains(surfaces[name], "a branch can break its own test setup") &&
			!strings.Contains(surfaces[name], "A branch can break its own test setup") &&
			!strings.Contains(surfaces[name], "a branch can break its own test setup, and so can the box") {
			t.Errorf("the %s does not say the banner leaves the culprit open: %q", name, surfaces[name])
		}
	}
}

// TestSetupHeadlineAssertsOnlyWhatTheClassEstablishes is requirement 3 applied
// to the line that actually travels.
//
// The record mg-15bb was filed about contradicted itself in two ADJACENT
// FIELDS: "test setup failed, not the branch" beside "establishes a fact about
// the branch". The first draft of this remedy reproduced it — a headline reading
// "FAILED IN ITS OWN SETUP, NOT ON THIS BRANCH" three sentences above a
// paragraph saying the banner does not say whose setup failed. A caveat in
// paragraph three does not reach the reader who forwards the headline, so the
// headline is where this has to hold.
func TestSetupHeadlineAssertsOnlyWhatTheClassEstablishes(t *testing.T) {
	gse := newGateSetupError("./build.sh", genuineBanner, errors.New("exit status 1"))
	if gse == nil {
		t.Fatal("fixture is wrong")
	}
	headline := strings.SplitN(gse.Error(), "\n", 2)[0]
	if !strings.Contains(headline, "RETURNED NO VERDICT") {
		t.Errorf("the headline does not state what the class establishes: %q", headline)
	}
	// "not on this branch" claims the branch is innocent; the class does not
	// establish that. "not a finding against" is the weaker, true form and is
	// allowed.
	if strings.Contains(strings.ToLower(headline), "not on this branch") {
		t.Errorf("the headline clears the branch, which the class does not establish: %q", headline)
	}
	if !strings.Contains(headline, "NOT A CLEARANCE") {
		t.Errorf("the headline condemns without refusing the opposite reading: %q", headline)
	}
}

// TestSetupRetryBudgetIsTheSmallestOfTheGateBudgets guards the number that
// actually costs something. Each setup retry is a whole gate run on the single
// serial slot every queued merge waits behind, and unlike the network budgets
// there is NO measured recovery distribution to size against — so it answers one
// question and stops.
func TestSetupRetryBudgetIsTheSmallestOfTheGateBudgets(t *testing.T) {
	if gateSetupMaxAttempts >= networkMaxAttempts {
		t.Fatalf("gateSetupMaxAttempts = %d, networkMaxAttempts = %d — a retry that re-runs the gate must not be granted the budget written for a git command",
			gateSetupMaxAttempts, networkMaxAttempts)
	}
	if gateSetupMaxAttempts > gateNetworkMaxAttempts {
		t.Fatalf("gateSetupMaxAttempts = %d exceeds gateNetworkMaxAttempts = %d, which IS sized against a measured outage",
			gateSetupMaxAttempts, gateNetworkMaxAttempts)
	}
	if gateSetupMaxAttempts < 2 {
		t.Fatalf("gateSetupMaxAttempts = %d — with no retry at all the class cannot answer 'once or standing', which is the only thing it buys", gateSetupMaxAttempts)
	}
	var total time.Duration
	for i := 1; i < gateSetupMaxAttempts; i++ {
		total += gateSetupBackoffFor(i)
	}
	if total > networkRetryBudget {
		t.Fatalf("the setup schedule sleeps %s, past the %s shared backstop", total, networkRetryBudget)
	}
}

// TestSetupRetriesSpendTheirOwnBudget: a broken envelope must not consume the
// attempts that exist to wait out a network outage, and vice versa.
func TestSetupRetriesSpendTheirOwnBudget(t *testing.T) {
	fastRetries(t)
	fastSetupRetries(t)
	f := newRetryFixtureScript(t, setupBannerGate, false)
	f.submit(t)
	f.r.processNext()

	mr := f.r.Get(f.mrID)
	if mr.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", mr.Status)
	}
	if mr.FailureClass != ClassSetup {
		t.Fatalf("class = %s, want %s", mr.FailureClass, ClassSetup)
	}
	if got := mr.StatusLabel(); got != "failed(setup)" {
		t.Errorf("status label = %q — a coordinator reads this before the error text", got)
	}
	if len(mr.Attempts) != gateSetupMaxAttempts {
		t.Fatalf("%d attempts, want %d — the budget is the bound, not the first failure", len(mr.Attempts), gateSetupMaxAttempts)
	}
	// The machine-readable status is unchanged; every polecat's poll loop keys
	// on the literal "failed".
	if string(mr.Status) != "failed" {
		t.Errorf("machine status = %q", mr.Status)
	}
	// THE AUTHOR IS NOT BLAMED.
	if mr.FailureCount != 0 {
		t.Errorf("author failure streak = %d after a setup failure, want 0", mr.FailureCount)
	}
}

// TestSetupRetriesSayTheyAreRetryingAndWhy is requirement 2's second half. A
// retry that does not state its purpose is indistinguishable, in the record,
// from a loop that re-runs things because it always has.
func TestSetupRetriesSayTheyAreRetryingAndWhy(t *testing.T) {
	fastRetries(t)
	fastSetupRetries(t)
	logs := captureLog(t)
	f := newRetryFixtureScript(t, setupBannerGate, false)
	f.submit(t)
	f.r.processNext()

	mr := f.r.Get(f.mrID)
	first := mr.Attempts[0]
	if !first.Retried {
		t.Fatal("the first setup failure was not retried")
	}
	if !strings.Contains(first.RetriedReason, "RETRYING") {
		t.Errorf("attempt 1 does not say it is retrying: %q", first.RetriedReason)
	}
	for _, want := range []string{"setup attempt 2 of", "broken ONCE or is broken STANDING"} {
		if !strings.Contains(first.RetriedReason, want) {
			t.Errorf("attempt 1's retry reason is missing %q: %q", want, first.RetriedReason)
		}
	}
	if !strings.Contains(first.Line(), "RETRYING") {
		t.Errorf("the per-attempt log line drops the retry reason: %q", first.Line())
	}

	last := mr.Attempts[len(mr.Attempts)-1]
	if last.Retried {
		t.Error("the terminal attempt is marked as retried")
	}
	if !strings.Contains(last.NotRetriedReason, "SETUP") {
		t.Errorf("the exhausted-budget reason must still say the class, or a spent budget reads as a verdict on the branch: %q", last.NotRetriedReason)
	}
	// The sentence that suppressed the retry and misdirected a worker, verbatim
	// from verdictStages. It must not survive into this class's record — and the
	// check is on that sentence rather than on the phrase "returned a verdict",
	// which this note uses correctly in the negative ("never returned a verdict").
	if strings.Contains(last.NotRetriedReason, "ran on this tree and returned a verdict") {
		t.Errorf("the terminal reason claims the gate returned a verdict on this tree: %q", last.NotRetriedReason)
	}
	if !strings.Contains(last.NotRetriedReason, "budget is spent") {
		t.Errorf("the terminal reason does not say the retries ran out: %q", last.NotRetriedReason)
	}
	if mr.NotRetriedReason != last.NotRetriedReason {
		t.Error("the merge request does not carry the terminal not-retried reason")
	}

	out := logs.String()
	for _, want := range []string{"class=setup", "RETRYING", "NOT RETRIED"} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\n--- log ---\n%s", want, out)
		}
	}
}

// TestRetriedReasonSurvivesTheHistoryLog. `pogo refinery history` rebuilds
// attempts from the event log, and that is the only view spanning merge
// requests — the one a coordinator reads a RUN of these on. A reason that lived
// only in the in-memory record would be absent exactly there.
func TestRetriedReasonSurvivesTheHistoryLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.log")
	base := time.Now().Add(-6 * time.Hour).Truncate(time.Second)
	const why = "RETRYING (setup attempt 2 of 3, after 30s): the gate printed a setup-failure banner"

	writeLogLines(t, logPath,
		logLine(t, base, "refinery_merge_attempted", "mg-15bb", "/repo", map[string]any{
			"merge_request_id": "mr-setup", "branch": "polecat-p15bb", "target": "main", "attempt": 1, "author": "mg-15bb",
		}),
		logLine(t, base.Add(time.Minute), "refinery_merge_failed", "mg-15bb", "/repo", map[string]any{
			"merge_request_id": "mr-setup", "branch": "polecat-p15bb", "target": "main", "attempt": 1,
			"stage": "build", "class": string(ClassSetup), "reason": "setup", "terminal": false,
			"retried": true, "retried_reason": why, "backoff_seconds": 30.0,
		}),
		logLine(t, base.Add(2*time.Minute), "refinery_merge_failed", "mg-15bb", "/repo", map[string]any{
			"merge_request_id": "mr-setup", "branch": "polecat-p15bb", "target": "main", "attempt": 2,
			"stage": "build", "class": string(ClassSetup), "reason": "setup", "terminal": true,
			"retried": false, "not_retried_reason": "not retryable: the setup retry budget is spent",
		}),
	)

	w, err := HistoryFromLog(logPath, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	mr := findByID(w, "mr-setup")
	if mr == nil {
		t.Fatal("mr-setup missing")
	}
	if mr.FailureClass != ClassSetup {
		t.Errorf("class rebuilt as %q, want %q", mr.FailureClass, ClassSetup)
	}
	if len(mr.Attempts) < 1 || mr.Attempts[0].RetriedReason != why {
		t.Errorf("the retry reason did not survive the log: %+v", mr.Attempts)
	}
}

// setupBannerGate is a gate that fails the way a broken sandbox does: it banners
// its own setup failure and then prints the assertion wreckage underneath.
const setupBannerGate = `#!/bin/sh
echo "=== RUN   TestSomething"
echo "SETUP FAILURE (internal/testsandbox): POGO_HOME resolves onto the live tree"
echo "--- FAIL: TestSomething (0.00s)"
echo "FAIL	github.com/drellem2/pogo/internal/x	0.10s"
exit 1
`
