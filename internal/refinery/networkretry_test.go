package refinery

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The acceptance battery for mg-e5c2, run against a REAL git and a REAL
// resolver failure.
//
// The induced fault is deliberately a RESOLVER failure and not a blackholed
// address, because that is what actually happened: en1 lost its DHCP lease and
// mDNSResponder suppressed every unicast query, so the queries failed
// INSTANTLY. A blackhole produces the opposite timing (a per-attempt timeout)
// and would test a fault the box did not have — indeed the burst's instant
// failures were read as evidence AGAINST a network cause precisely because a
// blackhole was the only network fault being imagined.
//
// Here the unresolvable name is a `.invalid` host (RFC 2606), which every
// resolver answers NXDOMAIN for without leaving the machine, so the test needs
// no network and cannot be flaky on a working one.
//
// Predicted before the run, so the battery cannot be fitted afterwards to the
// answer it produced:
//
//	fetch fails → class infrastructure → retried with backoff → budget spent →
//	terminal failure carrying 5 attempt records, every one naming transport
//	https and quoting "Could not resolve host" verbatim, the last one carrying a
//	not-retried reason → status label failed(infrastructure) → and with the same
//	branch, the same refinery and only the resolver restored, the merge succeeds.
const unresolvableRemote = "https://mg-e5c2-no-such-host.invalid/drellem2/pogo.git"

// fastRetries compresses the shipped backoff schedule so the acceptance tests
// exercise the real loop without sleeping the real 52 seconds. The COUNT and the
// bounding are untouched — those are what is under test — only the delays shrink.
func fastRetries(t *testing.T) {
	t.Helper()
	saved := networkRetryBackoff
	networkRetryBackoff = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { networkRetryBackoff = saved })
}

// captureLog redirects the standard logger into a buffer for the duration of a
// test. The per-attempt log line is a deliverable of this ticket, not a debug
// aid — "failed once" and "gave up after five" have to be different sentences —
// so it gets a positive control like any other output.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	savedOut, savedFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(savedOut); log.SetFlags(savedFlags) })
	return buf
}

// retryFixture builds an origin bare repo, a branch to merge, and a refinery
// pointed at them. gateExit selects the quality gate's exit status.
type retryFixture struct {
	origin  string
	work    string
	wtDir   string
	r       *Refinery
	branch  string
	mrID    string
	failedC chan *MergeRequest
}

func newRetryFixture(t *testing.T, gateExit int) *retryFixture {
	return newRetryFixtureWith(t, gateExit, "", false)
}

// newRetryFixtureWith optionally records every gate run to markerPath and
// optionally sets [gates] skip_on_retry.
func newRetryFixtureWith(t *testing.T, gateExit int, markerPath string, skipOnRetry bool) *retryFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	f := &retryFixture{branch: "polecat-ze5c2", failedC: make(chan *MergeRequest, 4)}
	f.origin = t.TempDir()
	run(t, f.origin, "git", "init", "--bare", "-b", "main")

	f.work = t.TempDir()
	run(t, f.work, "git", "clone", f.origin, ".")
	run(t, f.work, "git", "config", "user.email", "test@test.com")
	run(t, f.work, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(f.work, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	// The gate config and the gate itself go on MAIN, not only on the branch.
	// loadConfig reads .pogo/refinery.toml from the refinery's clone as it stands
	// BEFORE the branch is checked out — i.e. from the target ref — so a config
	// that exists only on the branch is never read, and a fixture that puts it
	// there tests a refinery with no gates configured at all.
	os.MkdirAll(filepath.Join(f.work, ".pogo"), 0755)
	toml := "[gates]\ncommands = [\"./build.sh\"]\n"
	if skipOnRetry {
		toml += "skip_on_retry = true\n"
	}
	os.WriteFile(filepath.Join(f.work, ".pogo", "refinery.toml"), []byte(toml), 0644)
	os.WriteFile(filepath.Join(f.work, "build.sh"), []byte(execScript(gateExit, markerPath)), 0755)
	run(t, f.work, "git", "add", ".")
	run(t, f.work, "git", "commit", "-m", "initial")
	run(t, f.work, "git", "push", "origin", "main")

	run(t, f.work, "git", "checkout", "-b", f.branch)
	os.WriteFile(filepath.Join(f.work, "feature.go"), []byte("package main\n// feature\n"), 0644)
	run(t, f.work, "git", "add", ".")
	run(t, f.work, "git", "commit", "-m", "feat: add feature (mg-e5c2)")
	run(t, f.work, "git", "push", "origin", f.branch)
	run(t, f.work, "git", "checkout", "main")
	run(t, f.work, "git", "merge", "--ff-only", f.branch, "--")

	f.wtDir = t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  f.wtDir,
		StatePath:    filepath.Join(t.TempDir(), "state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.SetOnFailed(func(mr *MergeRequest) { f.failedC <- mr })
	f.r = r
	return f
}

func execScript(exit int, markerPath string) string {
	s := "#!/bin/sh\n"
	if markerPath != "" {
		s += "echo ran >> " + markerPath + "\n"
	}
	if exit == 0 {
		return s + "exit 0\n"
	}
	return s + "echo 'FAIL github.com/drellem2/pogo/internal/x'\nexit 1\n"
}

// breakResolver points the repo's origin at an unresolvable host, which the
// refinery propagates into its private clone on every merge.
func (f *retryFixture) breakResolver(t *testing.T) {
	t.Helper()
	run(t, f.origin, "git", "remote", "add", "origin", unresolvableRemote)
}

func (f *retryFixture) fixResolver(t *testing.T) {
	t.Helper()
	run(t, f.origin, "git", "remote", "remove", "origin")
	// The clone made while the resolver was broken still carries the bad URL;
	// fixRemoteURL leaves a bare source repo's clone alone, so point it back at
	// origin explicitly — this is the operator restoring DNS, nothing more.
	run(t, filepath.Join(f.wtDir, filepath.Base(f.origin)), "git", "remote", "set-url", "origin", f.origin)
}

func (f *retryFixture) submit(t *testing.T) {
	t.Helper()
	id, err := f.r.Submit(MergeRequest{
		RepoPath:  f.origin,
		Branch:    f.branch,
		TargetRef: "main",
		Author:    "mg-e5c2",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.mrID = id
}

// TestResolverFailureIsRetriedBoundedAndLoggedPerAttempt is the ticket's
// headline acceptance: induce the fault that actually happened, and show the
// retry fires, is bounded, and is logged per attempt.
func TestResolverFailureIsRetriedBoundedAndLoggedPerAttempt(t *testing.T) {
	fastRetries(t)
	f := newRetryFixture(t, 0)
	f.breakResolver(t)
	logs := captureLog(t)

	f.submit(t)
	f.r.processNext()

	mr := f.r.Get(f.mrID)
	if mr == nil {
		t.Fatal("merge request vanished")
	}

	// RETRY FIRES. Before this change the answer here was 1.
	if len(mr.Attempts) < 2 {
		t.Fatalf("attempts recorded = %d — a network-class failure was not retried at all, which is the defect mg-e5c2 was filed for", len(mr.Attempts))
	}
	// AND IS BOUNDED.
	if len(mr.Attempts) != networkMaxAttempts {
		t.Errorf("attempts = %d, want exactly the network budget %d — an unbounded retry is not a fix, it is a different failure", len(mr.Attempts), networkMaxAttempts)
	}
	if mr.Status != StatusFailed {
		t.Fatalf("status = %s, want failed once the budget is spent", mr.Status)
	}

	// CLASSIFIED, IN THE STATUS.
	if mr.FailureClass != ClassInfrastructure {
		t.Errorf("failure class = %q, want infrastructure", mr.FailureClass)
	}
	if got := mr.StatusLabel(); got != "failed(infrastructure)" {
		t.Errorf("status label = %q, want failed(infrastructure) — a coordinator reads this before the error text", got)
	}

	// EVERY ATTEMPT CARRIES ITS TRANSPORT AND THE RAW ERROR, VERBATIM.
	for _, a := range mr.Attempts {
		if a.Transport != "https" {
			t.Errorf("attempt %d transport = %q, want https — a failure record that omits the transport is the record that made 2026-08-05 unreadable", a.Attempt, a.Transport)
		}
		if !strings.Contains(strings.ToLower(a.RawError), "could not resolve host") {
			t.Errorf("attempt %d raw error does not quote the resolver failure verbatim: %q", a.Attempt, a.RawError)
		}
		if a.Command == "" {
			t.Errorf("attempt %d does not record the git command as invoked", a.Attempt)
		}
		if a.Stage != "fetch" {
			t.Errorf("attempt %d stage = %q, want fetch", a.Attempt, a.Stage)
		}
	}

	// THE RETRIES ARE MARKED AS RETRIES, AND THE LAST ONE SAYS WHY IT STOPPED.
	for i, a := range mr.Attempts[:len(mr.Attempts)-1] {
		if !a.Retried {
			t.Errorf("attempt %d is not marked retried, but attempt %d followed it", i+1, i+2)
		}
	}
	last := mr.Attempts[len(mr.Attempts)-1]
	if last.Retried {
		t.Error("the terminal attempt is marked as retried")
	}
	if !strings.Contains(last.NotRetriedReason, "not retryable:") {
		t.Errorf("terminal attempt reason = %q, want a 'not retryable: <reason>' record — mg-e5c2 requirement 3", last.NotRetriedReason)
	}
	if !strings.Contains(last.NotRetriedReason, "INFRASTRUCTURE") {
		t.Errorf("the exhausted-budget reason must still say the class is infrastructure, or a spent budget reads as a verdict on the branch: %q", last.NotRetriedReason)
	}
	if mr.NotRetriedReason != last.NotRetriedReason {
		t.Error("the merge request does not carry the terminal not-retried reason")
	}

	// LOGGED PER ATTEMPT: 'failed once' and 'failed after N' are different logs.
	out := logs.String()
	for _, want := range []string{
		"attempt 1  stage=fetch  class=infrastructure  transport=https",
		"attempt 5  stage=fetch  class=infrastructure  transport=https",
		"NOT RETRIED",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\n--- log ---\n%s", want, out)
		}
	}
	if n := strings.Count(out, "raw error (verbatim, transport=https)"); n != networkMaxAttempts {
		t.Errorf("raw error logged %d times, want once per attempt (%d)", n, networkMaxAttempts)
	}

	// THE AUTHOR IS NOT BLAMED. The streak feeds an escalation whose advice is
	// about the polecat, and a DNS outage is not evidence about a polecat.
	if mr.FailureCount != 0 {
		t.Errorf("author failure streak = %d after an infrastructure failure, want 0", mr.FailureCount)
	}

	select {
	case got := <-f.failedC:
		if got.ID != mr.ID {
			t.Errorf("onFailed fired for %s, want %s", got.ID, mr.ID)
		}
	default:
		t.Error("onFailed did not fire — an infrastructure failure is still a failure and must still be reported")
	}
}

// TestTheSameBranchMergesOnceTheResolverIsRestored is the control the ticket's
// doubt section asked for: what differs between the failing attempt and the
// succeeding one, given both run from the same daemon against the same host?
// Here the ONLY thing that changes is name resolution. The branch, the refinery
// and the gate are identical — so the failure established nothing about any of
// them, which is exactly why retrying it is correct.
func TestTheSameBranchMergesOnceTheResolverIsRestored(t *testing.T) {
	fastRetries(t)
	f := newRetryFixture(t, 0)
	f.breakResolver(t)

	f.submit(t)
	f.r.processNext()
	if mr := f.r.Get(f.mrID); mr.Status != StatusFailed || mr.FailureClass != ClassInfrastructure {
		t.Fatalf("setup: expected an infrastructure failure, got %s/%s", mr.Status, mr.FailureClass)
	}

	f.fixResolver(t)
	f.submit(t)
	f.r.processNext()

	mr := f.r.Get(f.mrID)
	if mr.Status != StatusMerged {
		t.Fatalf("status = %s (%s), want merged: nothing about the branch changed, only the resolver", mr.Status, mr.Error)
	}
}

// TestRetriedSuccessNamesTheAttemptThatWon covers the third condition of
// pm-pogo's ruling. A silent retry converts a flaky night into an invisible one,
// and invisible is how this box's network became the dominant failure mode
// without anybody holding the evidence.
func TestRetriedSuccessNamesTheAttemptThatWon(t *testing.T) {
	fastRetries(t)
	f := newRetryFixture(t, 0)
	f.breakResolver(t)
	logs := captureLog(t)

	// Repair the resolver from underneath the retry loop, after the first
	// attempt has already failed.
	f.submit(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if mr := f.r.Get(f.mrID); mr != nil && len(mr.Attempts) >= 1 {
				f.fixResolver(t)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	f.r.processNext()
	<-done

	mr := f.r.Get(f.mrID)
	if mr.Status != StatusMerged {
		t.Fatalf("status = %s (%s), want merged — the retry should have carried it", mr.Status, mr.Error)
	}
	if mr.RecoveredOnAttempt < 2 {
		t.Fatalf("recovered_on_attempt = %d, want the attempt that won named", mr.RecoveredOnAttempt)
	}
	if len(mr.Attempts) == 0 {
		t.Error("the failed attempts before the win were discarded — a retried success that hides the retry hides the flaky host")
	}
	if !strings.Contains(logs.String(), "RECOVERED on attempt") {
		t.Errorf("the log does not name the attempt that won:\n%s", logs.String())
	}
}

// TestDeterministicFailureIsNotRetried is the other half of the ruling, and the
// half a retry patch can most easily break: a gate that RAN and returned a
// verdict must be attempted exactly ONCE, and must say why there was no retry.
func TestDeterministicFailureIsNotRetried(t *testing.T) {
	fastRetries(t)
	f := newRetryFixture(t, 1) // build.sh exits 1
	logs := captureLog(t)

	f.submit(t)
	f.r.processNext()

	mr := f.r.Get(f.mrID)
	if mr.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", mr.Status)
	}
	if len(mr.Attempts) != 1 {
		t.Fatalf("attempts = %d, want exactly 1 — re-running a gate that already returned a verdict burns a serial slot for no new information", len(mr.Attempts))
	}
	a := mr.Attempts[0]
	if a.Class != ClassDefect {
		t.Errorf("class = %q, want defect — this failure establishes a fact about the branch", a.Class)
	}
	if a.Retried {
		t.Error("a gate verdict was retried")
	}
	if !strings.Contains(a.NotRetriedReason, "not retryable:") && !strings.Contains(a.NotRetriedReason, "verdict") {
		t.Errorf("no legible reason for the absence of a retry: %q", a.NotRetriedReason)
	}
	// And the status stays the plain token, so a real defect still reads as one.
	if got := mr.StatusLabel(); got != "failed" {
		t.Errorf("status label = %q, want plain failed — mistaking a real defect for a network casualty happened on 2026-08-05 too, in the other direction", got)
	}
	if mr.FailureCount != 1 {
		t.Errorf("author failure streak = %d, want 1 — a defect DOES count against the author", mr.FailureCount)
	}
	if !strings.Contains(logs.String(), "NOT RETRIED") {
		t.Errorf("the log does not record that no retry was attempted:\n%s", logs.String())
	}
}

// TestRetryBudgetIsNotSharedBetweenClasses guards the split budget: a network
// blip must not consume the attempts that exist to absorb a lost race, or a
// merge that hits both fails for the wrong reason.
func TestRetryBudgetIsNotSharedBetweenClasses(t *testing.T) {
	if networkMaxAttempts+defaultMaxAttempts+defaultUnclassifiedAttempts <= defaultMaxAttempts {
		t.Fatal("the hard cap does not exceed the contention budget")
	}
	if networkMaxAttempts < 2 {
		t.Fatal("the network budget allows no retry at all")
	}
}

// TestBackoffIsInterruptedByCancel: backoff must not let a cancel wait out the
// whole schedule. The retry loop exists to absorb the network, not to defeat an
// operator.
func TestBackoffIsInterruptedByCancel(t *testing.T) {
	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	mr := &MergeRequest{ID: "mr-backoff", RepoPath: "/repos/alpha", Status: StatusProcessing}
	markInFlight(r, mr)
	if !r.sleepUnlessCancelled(mr, 5*time.Millisecond) {
		t.Fatal("an uncancelled sleep reported cancellation")
	}

	// A merge backing off in ANOTHER repo's lane must be unaffected. Backoff is
	// where a merge spends the longest stretch not holding any lock, so it is
	// the likeliest place for a per-lane cancel to be mistaken for a global one
	// — and a backoff cut short by someone else's cancel would abandon a retry
	// the network-class budget exists to spend (mg-e5c2, mg-37ad).
	other := &MergeRequest{ID: "mr-other-lane", RepoPath: "/repos/beta", Status: StatusProcessing}
	markInFlight(r, other)

	r.mu.Lock()
	r.requestInFlightCancelLocked(r.laneHoldingLocked(mr.ID))
	r.mu.Unlock()

	start := time.Now()
	if r.sleepUnlessCancelled(mr, 10*time.Second) {
		t.Error("a cancelled sleep reported success")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancel took %s to interrupt the backoff", elapsed)
	}
	if !r.sleepUnlessCancelled(other, 5*time.Millisecond) {
		t.Error("cancelling one repo's merge also interrupted another repo's backoff")
	}
}

// TestANetworkRetryDoesNotLetSkipOnRetryBypassGatesThatNeverRan is a control on
// THIS change rather than on the original defect — the way a retry patch can
// most plausibly cause harm.
//
// `[gates] skip_on_retry` rests on a premise: "gates already passed on
// near-identical code; only the version-bump commit from main differs". Before
// mg-e5c2 a fetch failure was terminal, so every retry did follow an attempt
// that had reached the gates and the premise held. Retrying a fetch falsifies
// it: attempt 1 dies before the gates exist, and a retry keyed on `attempt > 1`
// alone would merge a branch NO gate ever ran against. The fix makes the
// condition say what the premise claims — gates were reached at least once —
// and this is the test that would fail if it regressed.
func TestANetworkRetryDoesNotLetSkipOnRetryBypassGatesThatNeverRan(t *testing.T) {
	fastRetries(t)
	marker := filepath.Join(t.TempDir(), "gate-runs")
	f := newRetryFixtureWith(t, 0, marker, true)
	f.breakResolver(t)

	f.submit(t)
	// Repair the resolver once the first fetch has failed, so the merge lands on
	// a RETRY — the exact situation where skip_on_retry used to apply.
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if mr := f.r.Get(f.mrID); mr != nil && len(mr.Attempts) >= 1 {
				f.fixResolver(t)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	f.r.processNext()
	<-done

	mr := f.r.Get(f.mrID)
	if mr.Status != StatusMerged {
		t.Fatalf("status = %s (%s), want merged on a retry", mr.Status, mr.Error)
	}
	if mr.RecoveredOnAttempt < 2 {
		t.Fatalf("the merge did not land on a retry (recovered_on_attempt=%d), so this test proves nothing", mr.RecoveredOnAttempt)
	}
	runs, err := os.ReadFile(marker)
	if err != nil || len(strings.Fields(string(runs))) == 0 {
		t.Fatalf("the quality gate NEVER RAN and the branch merged anyway (marker=%q, err=%v) — a network retry must not inherit skip_on_retry's premise that gates already passed", string(runs), err)
	}
}
