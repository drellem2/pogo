package refinery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The acceptance battery for mg-c3b7: a transport failure that lands AFTER the
// quality gates must not throw the gate run away, and the network retry budget
// must be sized against the outage duration that was actually measured.
//
// The incident, from a live instance on 2026-08-10:
//
//	03:31:09Z  quality-gates start
//	03:40:07Z  ./build.sh FINISHED CLEAN after 8m58s
//	03:40:07Z+ fetch: ssh: connect to host github.com port 22: Undefined error: 0
//	           5 of 5 network-class attempts consumed over 52s of backoff
//	03:41:00Z  status=failed(infrastructure) — retry budget spent
//
// Everything except the budget worked: the classification was right, the mail
// went out, the author resubmitted instead of chasing a phantom defect. The
// 8m58s of gate was still discarded, and re-run from scratch on resubmit.
//
// A controlled sampler with a positive control measured the outage that was
// still running at that moment: onset 03:37:23Z, recovery 03:52:49Z, DURATION
// 15m26s. That is the wave this incident happened in — it is NOT the size of the
// event. The same fault recurs (mg-964e), its duration is a distribution, and
// that distribution has widened twice: "15 min ±41s" was withdrawn, a 17m52s
// maximum superseded it, and wave L then ran ~35m03s (n=12) with a ~28m floor
// corroborated independently of the lease. The shipped 21m45s campaign covers
// 17m52s but NOT the current maximum — that shortfall is mg-682d. See
// TestNetworkBudgetIsNotTrimmedBelowWhatItWasSizedFor below.
//
// The fault induced below is a REAL post-gate transport failure against a real
// git: a pre-receive hook on the origin rejects the first push with the exact
// ssh wording the incident carried, then accepts. That puts the failure where
// the incident put it — after ./build.sh has already run and passed — which is
// the property the whole ticket turns on.

// pushOutageWording is the incident's verbatim ssh error. errno 0: connect()
// failed and the error slot was never populated, which is why the classifier
// reads the step that aborted rather than reasoning about what the errno "must
// mean".
const pushOutageWording = "ssh: connect to host github.com port 22: Undefined error: 0"

// rejectFirstPush installs a pre-receive hook on the origin that fails the
// first push with a network error and lets later ones through.
func (f *retryFixture) rejectFirstPush(t *testing.T) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "pushes")
	hook := "#!/bin/sh\n" +
		// Drain stdin: git writes the ref updates there and a hook that never
		// reads them can take a SIGPIPE instead of the exit status under test.
		"cat > /dev/null\n" +
		"n=$(cat " + counter + " 2>/dev/null || echo 0)\n" +
		"n=$((n + 1))\n" +
		"echo $n > " + counter + "\n" +
		"if [ \"$n\" -le 1 ]; then\n" +
		"  echo '" + pushOutageWording + "' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"exit 0\n"
	path := filepath.Join(f.origin, "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(hook), 0755); err != nil {
		t.Fatal(err)
	}
}

// gateLandsAnotherMergeOnce rewrites the target's quality gate so that its
// FIRST run lands an unrelated commit on the target ref — another merge
// finishing while this one is still inside its 9-minute gate. That is the real
// mechanism by which the tree a verdict was computed on stops being the tree
// that will land, and it needs no hook: it happens through the refinery's own
// ff-only check, which then fails as contention and retries.
//
// A pre-receive hook cannot stand in for it. git runs hooks inside a quarantine
// environment that forbids ref updates, so a hook that tries to move the target
// silently changes nothing and the test passes for the wrong reason.
func (f *retryFixture) gateLandsAnotherMergeOnce(t *testing.T, marker string) {
	t.Helper()
	once := filepath.Join(t.TempDir(), "landed")
	// Build on origin/main, NOT the fixture's local main — that one has already
	// merged the branch locally and pushing it would land the branch under test.
	run(t, f.work, "git", "checkout", "-B", "stage", "origin/main")
	script := "#!/bin/sh\n" +
		"echo ran >> " + marker + "\n" +
		"if [ ! -f " + once + " ]; then\n" +
		"  : > " + once + "\n" +
		"  git push origin refs/remotes/origin/main-ahead:refs/heads/main >/dev/null 2>&1\n" +
		"fi\n" +
		"exit 0\n"
	os.WriteFile(filepath.Join(f.work, "build.sh"), []byte(script), 0755)
	run(t, f.work, "git", "add", ".")
	run(t, f.work, "git", "commit", "-m", "test: the gate lands another merge on its first run")
	run(t, f.work, "git", "push", "origin", "stage:main")

	// The unrelated merge itself, staged on top of the new target so the push
	// above it is a fast-forward.
	os.WriteFile(filepath.Join(f.work, "other.go"), []byte("package main\n// another merge landed during the outage\n"), 0644)
	run(t, f.work, "git", "add", ".")
	run(t, f.work, "git", "commit", "-m", "feat: an unrelated merge that landed during the outage")
	run(t, f.work, "git", "push", "origin", "stage:main-ahead")
	run(t, f.work, "git", "checkout", "main")
}

// gateRuns counts how many times the quality gate actually executed.
func gateRuns(t *testing.T, marker string) int {
	t.Helper()
	b, err := os.ReadFile(marker)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return len(strings.Fields(string(b)))
}

// TestPostGateTransportFailureDoesNotDiscardTheGateRun is the ticket's headline
// acceptance. The gate passes, the push then fails on the network, the retry
// succeeds — and the gate must have run ONCE, not twice.
//
// The count is the whole assertion. "The merge succeeded" was already true
// before this change; what was not true is that it succeeded without paying for
// the gate a second time.
func TestPostGateTransportFailureDoesNotDiscardTheGateRun(t *testing.T) {
	fastRetries(t)
	marker := filepath.Join(t.TempDir(), "gate-runs")
	f := newRetryFixtureWith(t, 0, marker, false)
	f.rejectFirstPush(t)
	logs := captureLog(t)

	f.submit(t)
	f.r.processNext()

	mr := f.r.Get(f.mrID)
	if mr == nil {
		t.Fatal("merge request vanished")
	}
	if mr.Status != StatusMerged {
		t.Fatalf("status = %s (%s), want merged — the fixture's second push is accepted, so a failure here means the retry never happened: %s",
			mr.Status, mr.FailureClass, mr.Error)
	}
	// The failure has to have been the one we induced, or the count below is
	// measuring something else entirely.
	if len(mr.Attempts) != 1 {
		t.Fatalf("failed attempts = %d, want exactly 1 (the rejected push)", len(mr.Attempts))
	}
	if got := mr.Attempts[0].Class; got != ClassInfrastructure {
		t.Fatalf("induced failure classified %q, want infrastructure — the fixture is not producing the fault under test", got)
	}
	if !strings.Contains(mr.Attempts[0].RawError, "connect to host") {
		t.Fatalf("induced failure did not carry the incident's wording: %q", mr.Attempts[0].RawError)
	}

	// THE DELIVERABLE.
	if n := gateRuns(t, marker); n != 1 {
		t.Errorf("the quality gate ran %d times across a post-gate transport failure, want 1 — a completed gate verdict is being discarded and recomputed, which is the 8m58s mg-c3b7 was filed about", n)
	}

	// And it is legible: a reader must be able to tell a held verdict from a
	// gate that silently did not run.
	if !strings.Contains(logs.String(), "HELD from attempt 1") {
		t.Errorf("the log does not say the verdict was held; a skipped gate that says nothing is indistinguishable from a gate that was never configured:\n%s", logs.String())
	}
	if !strings.Contains(mr.GateOutput, "quality gates NOT re-run") {
		t.Errorf("the gate output does not record that it is a replay:\n%s", mr.GateOutput)
	}
}

// TestGateVerdictIsNotHeldWhenTheRebasedTreeChanges is the constructive control
// for the test above, and the reason the hold is keyed on the TREE rather than
// on the attempt number.
//
// Another merge lands on the target while this one is in its gate. The retry
// therefore rebases onto a different base and produces a different tree, so the
// held verdict is about content that will no longer be what lands — and the
// gates MUST run again.
//
// Without this test the first one is satisfied by "never re-run the gates",
// which would land ungated code — a strictly worse outcome than the wasted nine
// minutes the hold exists to save.
func TestGateVerdictIsNotHeldWhenTheRebasedTreeChanges(t *testing.T) {
	fastRetries(t)
	marker := filepath.Join(t.TempDir(), "gate-runs")
	f := newRetryFixtureWith(t, 0, marker, false)
	f.gateLandsAnotherMergeOnce(t, marker)

	f.submit(t)
	f.r.processNext()

	mr := f.r.Get(f.mrID)
	if mr == nil {
		t.Fatal("merge request vanished")
	}
	if mr.Status != StatusMerged {
		t.Fatalf("status = %s (%s), want merged: %s", mr.Status, mr.FailureClass, mr.Error)
	}
	if n := gateRuns(t, marker); n != 2 {
		t.Errorf("the quality gate ran %d times, want 2 — the target moved under the merge, so the tree that would land is not the tree that was gated and the verdict must NOT be held", n)
	}
	if strings.Contains(mr.GateOutput, "quality gates NOT re-run") {
		t.Errorf("a stale verdict was replayed after the target moved:\n%s", mr.GateOutput)
	}
}

// TestGateHoldRevalidatesAndFailsClosed pins the hold's decision rule directly,
// including the case where the tree cannot be read at all.
//
// The failure being guarded against is the mirror image of the defect: a hold
// that is too eager lands an ungated tree, which is strictly worse than the
// wasted 9 minutes it is meant to save. So an unknown tree takes no hold.
func TestGateHoldRevalidatesAndFailsClosed(t *testing.T) {
	h := &gateHold{}
	if h.held("abc") {
		t.Error("an empty hold matched a tree")
	}

	h.record("abc", "gate output", 1)
	if !h.held("abc") {
		t.Error("the hold does not match the tree it was recorded on")
	}
	if h.held("def") {
		t.Error("the hold matched a DIFFERENT tree — this is how an ungated tree lands")
	}
	if h.held("") {
		t.Error("the hold matched an unreadable tree; an unknown tree must never satisfy it")
	}

	// A tree that could not be read takes no hold rather than a blank one that
	// would then match every other unreadable tree.
	empty := &gateHold{}
	empty.record("", "gate output", 1)
	if empty.tree != "" || empty.held("") {
		t.Error("an unreadable tree was recorded as a hold")
	}

	// A nil hold is inert, not a panic: it is the shape any future caller that
	// does not want holding will pass.
	var nilHold *gateHold
	if nilHold.held("abc") {
		t.Error("a nil hold matched")
	}
	nilHold.record("abc", "out", 1)
}

// TestGatedTreeTracksContentNotCommits is the positive control for the key the
// hold is validated on. If rev-parse HEAD^{tree} did not move with the content,
// the control test above would pass for the wrong reason — it would be
// comparing a constant.
func TestGatedTreeTracksContentNotCommits(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0644)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "one")
	first := gatedTreeOf(dir)
	if first == "" {
		t.Fatal("tree unreadable in a repo that has a commit")
	}

	// A new COMMIT over identical content keeps the tree — which is exactly the
	// property the hold needs, since a rebase rewrites committer dates and so
	// changes every commit SHA while changing no content.
	run(t, dir, "git", "commit", "--allow-empty", "-m", "two")
	if same := gatedTreeOf(dir); same != first {
		t.Errorf("tree changed across an empty commit (%s -> %s) — the hold would never match after a rebase", first, same)
	}

	// Changed content must change it, or the hold can never be invalidated.
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n// changed\n"), 0644)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "three")
	if changed := gatedTreeOf(dir); changed == first {
		t.Error("the tree did not move when the content did — the hold could never be invalidated")
	}

	if gatedTreeOf(filepath.Join(dir, "no-such-dir")) != "" {
		t.Error("an unreadable tree did not come back empty")
	}
}

// TestNetworkBudgetIsNotTrimmedBelowWhatItWasSizedFor is a RATCHET, and it is
// deliberately not named for a property the budget no longer has.
//
// This guard used to assert that the budget outlasted "the measured outage",
// against 15m26s — the single wave measured on 2026-08-10 at 03:37Z. Two things
// have since happened to that number, and they pull in opposite directions:
//
//   - It was too WEAK as a guard. 15m26s was one wave; continued sampling of the
//     same DHCP fault (mg-964e) put the maximum at 17m52s. A guard at 15m26s
//     passes at 12 attempts (17m45s, seven seconds short of that) and at 11
//     (15m45s, short by 2m07s) — it would have admitted the exact tune-down
//     mg-7110 was filed about.
//   - The event then outgrew the budget entirely. Wave L ran ~35m03s, with a
//     ~28m floor established independently of the lease. The shipped 21m45s
//     campaign does not cover that and is KNOWN-INSUFFICIENT (mg-682d).
//
// So this test CANNOT assert "the budget outlasts the longest observed outage".
// That statement is false, and a test asserting it would fail on a budget nobody
// is allowed to change from here. What it asserts instead is the ratchet: the
// campaign must not shrink below 17m52s, the largest wave it ever did cover.
// Trimming a known-insufficient budget makes it worse, and the 3m53s that
// separates 21m45s from 17m52s is the thing most likely to be mistaken for
// spare room.
//
// A PASS HERE IS NOT A CERTIFICATE OF ADEQUACY. It means only that nobody has
// trimmed the campaign. Adequacy is mg-682d's question, and the answer today is
// no.
//
// Do not "fix" this by raising the constant to 35m03s. That would fail, and the
// failure would report a defect in a budget this ticket is explicitly forbidden
// to change. The distribution has widened twice with no established upper bound,
// so the remedy is likely a mechanism that distinguishes "the network is down"
// from "attempts exhausted" and requeues — not a larger sleep.
//
// This test is deliberately written against DURATION and not against any
// prediction of when the next window opens: onset is predictable on this host
// (LeaseExpirationTime + 54s), but a budget that leans on that fails the first
// time the lease pattern changes. Duration is what has to be survived.
func TestNetworkBudgetIsNotTrimmedBelowWhatItWasSizedFor(t *testing.T) {
	// Wave G, both ends measured: onset 07:37:48Z, recovery 07:55:40Z. This is
	// the largest outage the shipped campaign covers — NOT the largest observed.
	const largestOutageThisBudgetCovers = 17*time.Minute + 52*time.Second

	// What the shipped schedule will actually sleep, walked exactly as
	// processMerge walks it: one backoff before each retry, netMaxAttempts
	// attempts means netMaxAttempts-1 retries.
	var total time.Duration
	for n := 1; n <= networkMaxAttempts-1; n++ {
		next := networkBackoffFor(n)
		if total+next > networkRetryBudget {
			break // the clock backstop stops the campaign here
		}
		total += next
	}

	if total <= largestOutageThisBudgetCovers {
		t.Errorf("the network retry budget sleeps %s in total, below the %s it was sized for — this budget is ALREADY known-insufficient against the observed maximum (~35m03s, mg-682d), so trimming it further only makes it fail faster (mg-c3b7, mg-7110)",
			total.Round(time.Second), largestOutageThisBudgetCovers)
	}

	// The probe interval bounds how long after recovery the merge stays asleep.
	// A budget that waits 20 minutes but only wakes twice would clear the test
	// above while still missing most of the recovery window.
	if last := networkBackoffFor(networkMaxAttempts); last > 5*time.Minute {
		t.Errorf("the steady-state probe interval is %s — after recovery the merge sleeps that long before noticing", last)
	}
}
