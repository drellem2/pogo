package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// The claim-pid re-stamp started-signal (mg-7d6d). See claimrestamp.go.
//
// THE SHAPE THAT TRAPS YOU HERE, quoted from the acceptance criteria because it
// is the entire reason this file is not three lines long: "construct the wedge
// with promptReadySeen already latched and the work item already claimed by
// pogod. A test that leaves either of those unset passes against today's code
// with the gap present."
//
// Both unset states are the easy mistake, and each hides the defect in a
// different direction. Leave promptReadySeen false and the ready-composer
// fallback fires all by itself, so the test passes with no claim-pid signal
// anywhere. Leave ClaimedAtSpawn false and the ORIGINAL claim signal is chosen,
// which was never broken. The wedge under test is the narrow intersection: pogod
// holds the claim, the composer rendered, and no turn ran.
//
// So the pair that matters is wedgedPolecat + the two tests immediately below it,
// which run the same wedge with the mechanism off and on. The off case is the
// positive control — mg-7254's TestUnclaimedWorkingPolecatIsTheDefect pattern:
// assert the bad state ARISES.

// restampVerifier returns a ClaimRestampVerifier reporting `restamped` per the
// given schedule, and a call counter. Mirrors countingVerifier so the two signals
// can be driven independently in one test — which is how "the claim-pid arm was
// chosen" is distinguished from "some arm answered".
func restampVerifier(results []verifyCall) (ClaimRestampVerifier, func() int) {
	var mu sync.Mutex
	calls := 0
	fn := func(string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		i := calls
		calls++
		if i >= len(results) {
			i = len(results) - 1
		}
		return results[i].started, results[i].err
	}
	return fn, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

// wedgedPolecat builds the mg-ce61 paste-buffer wedge exactly: pogod holds the
// claim (ClaimedAtSpawn), the harness DID render its composer so promptReadySeen
// is latched, and no turn has run. This is the state a wedged polecat is actually
// in — not an approximation of it — and it is the state in which both the claim
// signal and the ready-composer signal report "started" for an agent that has
// done nothing.
func wedgedPolecat(t *testing.T, workItemID string) (*Agent, func() string) {
	t.Helper()
	a, readAll, _ := newRenudgeTestAgent(t, workItemID)
	a.ClaimedAtSpawn = true // pogod claimed it at spawn (mg-7254)
	a.markPromptReady()     // WaitForReady saw the composer before the nudge was absorbed
	return a, readAll
}

// TestWedgedPolecatIsUndetectedWithoutTheRestampSignal IS THE POSITIVE CONTROL.
//
// It runs the wedge against start-verification with the claim-pid mechanism OFF —
// which is pogod between mg-7254 and mg-7d6d, and is still pogod today on any host
// whose mg cannot re-stamp a claim. It asserts the bad state ARISES: not one
// recovery CR is delivered to a polecat that will never act.
//
// Without this test the guard below proves nothing. A watcher that renudged for
// some unrelated reason, or a fixture that quietly failed to latch
// promptReadySeen, would satisfy it just as well.
func TestWedgedPolecatIsUndetectedWithoutTheRestampSignal(t *testing.T) {
	a, readAll := wedgedPolecat(t, "mg-wedged")
	// The claim verifier reports started — as it now always does for a dispatch
	// pogod claimed (mg-7254) — and no restamp verifier is installed, so the
	// watcher falls back to the composer, which rendered.
	verifier, _ := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Fatalf("the positive control did not reproduce the mg-7d6d gap: a wedged polecat drew "+
			"%q with the claim-pid signal OFF. Something other than the claim-pid re-stamp is "+
			"recovering this wedge, so the guard test below proves nothing about the mechanism "+
			"it names", got)
	}
}

// TestWedgedPolecatDrawsRenudgeOnTheRestampSignal is the acceptance criterion: a
// polecat wedged by the mg-ce61 paste-buffer failure — composer rendered, kickoff
// absorbed, no turn executed — draws its auto_renudge recovery again.
//
// Identical fixture to the control above. The only difference is the mechanism.
func TestWedgedPolecatDrawsRenudgeOnTheRestampSignal(t *testing.T) {
	a, readAll := wedgedPolecat(t, "mg-wedged")
	verifier, claimCalls := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3)
	// The claim is still stamped with pogod's pid: no turn ran, so nothing
	// re-stamped it.
	restamp, restampCalls := restampVerifier([]verifyCall{{started: false}})
	reg.SetClaimRestampVerifier(restamp)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "\r\r\r" {
		t.Errorf("a polecat wedged by the mg-ce61 paste buffer drew %q, want 3 recovery CRs — "+
			"the wedge mg-feb3 exists for is going unrecovered", got)
	}
	if restampCalls() != 3 {
		t.Errorf("claim-restamp verifier consulted %d time(s), want 3 (one per attempt)", restampCalls())
	}
	if claimCalls() != 0 {
		t.Errorf("the claim-existence verifier was consulted %d time(s); want 0 — pogod's own "+
			"claim says nothing about this agent, which is the whole point of the pid arm",
			claimCalls())
	}
}

// TestRestampedPolecatIsNotRenudged is the negative control. A polecat that DID
// re-stamp has executed a turn, so it must draw nothing; without this a watcher
// that renudged unconditionally would pass the test above.
func TestRestampedPolecatIsNotRenudged(t *testing.T) {
	a, readAll := wedgedPolecat(t, "mg-working")
	verifier, _ := countingVerifier([]verifyCall{{started: false}})
	reg := fastRenudgeRegistry(verifier, 3)
	restamp, _ := restampVerifier([]verifyCall{{started: true}})
	reg.SetClaimRestampVerifier(restamp)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("renudged a polecat that had re-stamped its claim (so it demonstrably ran a "+
			"turn), got %q", got)
	}
}

// TestRestampSignalStopsRenudgingOnceTheClaimMoves pins that the signal is read
// per attempt rather than once: a polecat that starts late — the slow-but-healthy
// case DefaultStartVerifyDelay is sized for — takes its first CR and then no more.
func TestRestampSignalStopsRenudgingOnceTheClaimMoves(t *testing.T) {
	a, readAll := wedgedPolecat(t, "mg-slow")
	verifier, _ := countingVerifier([]verifyCall{{started: false}})
	reg := fastRenudgeRegistry(verifier, 3)
	restamp, calls := restampVerifier([]verifyCall{{started: false}, {started: true}})
	reg.SetClaimRestampVerifier(restamp)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "\r" {
		t.Errorf("got %q, want one CR then a stop — the second check saw the claim re-stamped, "+
			"so the agent is up and further keystrokes are stray input", got)
	}
	if calls() != 2 {
		t.Errorf("restamp verifier consulted %d time(s), want 2", calls())
	}
}

// TestRestampSignalErrorDoesNotRenudge: an unreadable store is inconclusive, and
// the watcher's standing discipline is that it delivers only while the agent is
// PROVABLY unstarted. A bare CR into a working agent is worse than deferring to
// the mail-check schedule.
func TestRestampSignalErrorDoesNotRenudge(t *testing.T) {
	a, readAll := wedgedPolecat(t, "mg-unreadable")
	verifier, _ := countingVerifier([]verifyCall{{started: false}})
	reg := fastRenudgeRegistry(verifier, 3)
	restamp, calls := restampVerifier([]verifyCall{{err: errors.New("claimed/ unreadable")}})
	reg.SetClaimRestampVerifier(restamp)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("renudged on an inconclusive claim-pid read, got %q", got)
	}
	if calls() != 1 {
		t.Errorf("restamp verifier consulted %d time(s), want 1 — an error must stop the loop, "+
			"not be retried into a renudge", calls())
	}
}

// TestStartedSignal_ClaimPIDBeatsTheReadyComposer pins the ORDER. The claim-pid
// arm is hard where the composer arm is a heuristic, so a claimed-at-spawn polecat
// with the mechanism available must be watched on the pid — never on the composer,
// which by construction already reports "started" for this wedge.
func TestStartedSignal_ClaimPIDBeatsTheReadyComposer(t *testing.T) {
	a, _ := wedgedPolecat(t, "mg-order")
	verifier, _ := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3)
	restamp, _ := restampVerifier([]verifyCall{{started: false}})
	reg.SetClaimRestampVerifier(restamp)

	started, reason, ok := reg.startedSignal(a)
	if !ok {
		t.Fatalf("startedSignal declined a claimed-at-spawn polecat (reason=%q)", reason)
	}
	if reason != reasonClaimPIDNotRestamped {
		t.Fatalf("reason = %q, want %q — the composer signal cannot see this wedge",
			reason, reasonClaimPIDNotRestamped)
	}
	// Invoke it: asserting on the reason alone would pass for a closure that
	// consulted sawPromptReady anyway.
	got, err := started()
	if err != nil {
		t.Fatalf("started(): %v", err)
	}
	if got {
		t.Error("the chosen signal reported STARTED for a wedged polecat, so it is reading " +
			"promptReadySeen (latched) rather than the claim pid")
	}
}

// TestStartedSignal_FallsBackWhenRestampUnavailable is the other side of the
// capability gate, and it must keep working: on an mg that cannot re-stamp, a
// claimed-at-spawn polecat still gets the weaker ready-composer signal rather than
// being dropped from the watch list altogether. Losing it there would be a
// regression on mg-c33e.
func TestStartedSignal_FallsBackWhenRestampUnavailable(t *testing.T) {
	a, _ := wedgedPolecat(t, "mg-nofallback")
	verifier, _ := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3) // no restamp verifier installed

	_, reason, ok := reg.startedSignal(a)
	if !ok {
		t.Fatalf("startedSignal declined to watch a claimed-at-spawn polecat when the re-stamp "+
			"mechanism is absent (reason=%q); it must fall back, not stop watching", reason)
	}
	if reason != reasonNoReadyComposer {
		t.Errorf("reason = %q, want %q", reason, reasonNoReadyComposer)
	}
}

// TestStartedSignal_UnclaimedAtSpawnStillPrefersTheClaimSignal pins that the new
// arm did not swallow the old one. Where pogod did NOT take the claim — a fail-open
// claim, or a caller using Registry.Spawn directly — the polecat is still the only
// thing that can claim, and no re-stamp is expected of it, so gating on a re-stamp
// there would renudge a perfectly healthy agent three times.
func TestStartedSignal_UnclaimedAtSpawnStillPrefersTheClaimSignal(t *testing.T) {
	a, _, _ := newRenudgeTestAgent(t, "mg-unclaimed") // ClaimedAtSpawn stays false
	verifier, claimCalls := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3)
	restamp, restampCalls := restampVerifier([]verifyCall{{started: false}})
	reg.SetClaimRestampVerifier(restamp)

	started, reason, ok := reg.startedSignal(a)
	if !ok {
		t.Fatalf("startedSignal declined (reason=%q)", reason)
	}
	if _, err := started(); err != nil {
		t.Fatalf("started(): %v", err)
	}
	if reason != reasonWorkItemUnclaimed {
		t.Errorf("reason = %q, want %q", reason, reasonWorkItemUnclaimed)
	}
	if claimCalls() != 1 {
		t.Errorf("claim verifier consulted %d time(s), want 1", claimCalls())
	}
	if restampCalls() != 0 {
		t.Errorf("the restamp verifier was consulted %d time(s) for a polecat pogod did not "+
			"claim for; want 0 — nothing tells that polecat to re-stamp, so the signal would "+
			"report it unstarted forever", restampCalls())
	}
}

// TestStartedSignal_RestampArmNeedsAWorkItem: the pid arm reads a work item's
// claim, so with no id there is no claim to read and it must not be chosen. In
// production ClaimedAtSpawn implies an id, which is exactly why the coupling is
// pinned rather than assumed.
func TestStartedSignal_RestampArmNeedsAWorkItem(t *testing.T) {
	a, _, _ := newRenudgeTestAgent(t, "")
	a.ClaimedAtSpawn = true
	verifier, _ := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3)
	restamp, calls := restampVerifier([]verifyCall{{started: false}})
	reg.SetClaimRestampVerifier(restamp)

	_, reason, ok := reg.startedSignal(a)
	if !ok {
		t.Fatalf("startedSignal declined (reason=%q)", reason)
	}
	if reason != reasonNoReadyComposer {
		t.Errorf("reason = %q, want %q for an agent with no work item", reason, reasonNoReadyComposer)
	}
	if calls() != 0 {
		t.Errorf("restamp verifier consulted %d time(s) with no work item id, want 0", calls())
	}
}

// TestStartedSignal_NoStartVerifierStillDeclines: the re-stamp arm must not
// smuggle a watch past the daemon-wide decline. A registry with no start verifier
// is a bare registry or a broken daemon, and mg-2437's loud report of that is the
// thing an operator checks first when no auto_renudge appears.
func TestStartedSignal_NoStartVerifierStillDeclines(t *testing.T) {
	a, _ := wedgedPolecat(t, "mg-noverifier")
	reg := fastRenudgeRegistry(nil, 3)
	restamp, calls := restampVerifier([]verifyCall{{started: false}})
	reg.SetClaimRestampVerifier(restamp)

	if _, _, ok := reg.startedSignal(a); ok {
		t.Error("startedSignal watched an agent on a daemon with no start verifier wired; " +
			"the decline reported by reportUnwatched is what makes that state audible")
	}
	if calls() != 0 {
		t.Errorf("restamp verifier consulted %d time(s) on a verifier-less daemon, want 0", calls())
	}
}

// ---------------------------------------------------------------------------
// The production verifier, against a real macguffin store.
// ---------------------------------------------------------------------------

// mgClaimWithPID claims id in the sandbox store stamping an explicit pid, which
// is what MGWorkItemClaimer does at spawn (`mg claim --pid <pogod>`). An arbitrary
// pid rather than the test process's own, so "the claim pid is pogod's" and "the
// claim pid is whoever ran the test" cannot be confused.
func mgClaimWithPID(t *testing.T, root, id string, pid int) {
	t.Helper()
	out, err := exec.Command("mg", "--root", root, "claim", id,
		"--pid", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		t.Fatalf("mg claim %s --pid %d: %v: %s", id, pid, err, out)
	}
}

const fakePogodPID = 424242

// TestMGClaimRestampVerifier_ReadsThePIDOffTheClaim is the mechanism against a
// real store: an item claimed with pogod's pid reads UNSTARTED, and the same item
// after a re-stamp to any other pid reads STARTED.
//
// The re-stamp is performed here by renaming the claim file, which is what the
// requested `mg reclaim` will do (macguffin mg-bb43) — one rename inside claimed/.
// That is the whole reason this pogo half can be built and tested ahead of the
// macguffin command: the observable is a filename, and the filename convention is
// already load-bearing for claimHeld and workitem.FindFrom.
func TestMGClaimRestampVerifier_ReadsThePIDOffTheClaim(t *testing.T) {
	root := mgSandbox(t)
	id := mgNewAvailable(t, root, "an item pogod claims at spawn")
	mgClaimWithPID(t, root, id, fakePogodPID)

	verify := NewMGClaimRestampVerifier(root, fakePogodPID)

	restamped, err := verify(id)
	if err != nil {
		t.Fatalf("verify(%s): %v", id, err)
	}
	if restamped {
		t.Fatalf("a claim still stamped with pogod's own pid %d read as re-stamped — the "+
			"started-signal is true from the first instant again, which is the mg-7254 defect "+
			"this arm exists to undo", fakePogodPID)
	}

	// The polecat's step 1: re-stamp the claim to its own pid, inside claimed/.
	restampClaim(t, root, id, fakePogodPID, fakePogodPID+7)

	restamped, err = verify(id)
	if err != nil {
		t.Fatalf("verify(%s) after re-stamp: %v", id, err)
	}
	if !restamped {
		t.Error("a re-stamped claim read as NOT re-stamped, so a polecat that did its step 1 " +
			"would draw three stray recovery keystrokes")
	}
}

// TestMGClaimRestampVerifier_RestampNeverLeavesClaimed is mg-7254's ownership
// guarantee, asserted rather than assumed: the re-stamp must not put the item back
// in available/ even for an instant, because that is the window a second dispatch
// fits through. It is also the requirement that rules out `mg unclaim` + `mg claim`
// as an implementation, which is why macguffin mg-bb43 asks for it directly.
func TestMGClaimRestampVerifier_RestampNeverLeavesClaimed(t *testing.T) {
	root := mgSandbox(t)
	id := mgNewAvailable(t, root, "an item that must never return to available")
	mgClaimWithPID(t, root, id, fakePogodPID)

	if got := mgStatus(t, root, id); got != "claimed" {
		t.Fatalf("fixture: %s is %q before the re-stamp, want claimed", id, got)
	}
	restampClaim(t, root, id, fakePogodPID, fakePogodPID+7)
	if got := mgStatus(t, root, id); got != "claimed" {
		t.Fatalf("%s is %q after the re-stamp, want claimed — the re-stamp reopened the "+
			"available/ window mg-7254 closed, and a second polecat could be dispatched "+
			"onto this item", id, got)
	}
	if mgDispatchable(t, root, id) {
		t.Error("the item is dispatchable after a re-stamp; ownership was lost in the handover")
	}
}

// restampClaim renames a claim file from one pid to another, inside claimed/. This
// is the operation macguffin mg-bb43 will expose as `mg reclaim <id> --pid`; doing
// it by rename here keeps this test honest about what is being asserted — the pogo
// side reads a filename and does not care who renamed it.
func restampClaim(t *testing.T, root, id string, from, to int) {
	t.Helper()
	dir := filepath.Join(root, "work", "claimed")
	oldPath := filepath.Join(dir, id+".md."+strconv.Itoa(from))
	newPath := filepath.Join(dir, id+".md."+strconv.Itoa(to))
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("re-stamp %s: %v", id, err)
	}
}

// TestMGClaimRestampVerifier_NoClaimReadsAsStarted: the claim pogod took is gone,
// so the item is done or released. Neither is "sitting under pogod's untouched
// claim", and reporting unstarted would fire a stray CR at a polecat that finished
// inside the 25s window.
func TestMGClaimRestampVerifier_NoClaimReadsAsStarted(t *testing.T) {
	root := mgSandbox(t)
	id := mgNewAvailable(t, root, "an item nobody claimed")
	// Make claimed/ exist but hold nothing for this id — the benign case.
	other := mgNewAvailable(t, root, "some other claimed item")
	mgClaimWithPID(t, root, other, fakePogodPID)

	restamped, err := NewMGClaimRestampVerifier(root, fakePogodPID)(id)
	if err != nil {
		t.Fatalf("verify(%s): %v", id, err)
	}
	if !restamped {
		t.Error("an item with no claim file read as NOT started; the watcher would renudge a " +
			"polecat whose item is already done")
	}
}

// TestMGClaimRestampVerifier_MissingStoreIsAnError is the store-disagreement
// guard, and the direction of the answer is the point.
//
// An absent claimed/ means we are reading a different store than the one
// ClaimForSpawn claimed into — a MacguffinStoreRoot disagreement, e.g. pogod under
// MG_ROOT while the verifier looks in ~/.macguffin. Folded into "no claim held",
// that mistake would report EVERY polecat on the daemon as started and switch the
// wedge detector off in silence. That is the exact failure mg-7d6d exists to end,
// so it must be an error the watcher declines on and logs, not an answer.
func TestMGClaimRestampVerifier_MissingStoreIsAnError(t *testing.T) {
	_, err := NewMGClaimRestampVerifier(filepath.Join(t.TempDir(), "no-such-store"), 1)("mg-x")
	if err == nil {
		t.Fatal("reading a store with no claimed/ directory returned an answer instead of an " +
			"error; a store-root disagreement would silently report every polecat as started")
	}
	if !strings.Contains(err.Error(), "wrong store") {
		t.Errorf("error does not name the cause an operator has to act on: %v", err)
	}
}

// TestMGClaimRestampVerifier_PidlessClaimIsAnError: a claim file we cannot read a
// pid off means the store does not look the way this verifier assumes. Guessing
// either answer would make the signal unfalsifiable, which is the one property it
// cannot afford — it is here to be hard.
func TestMGClaimRestampVerifier_PidlessClaimIsAnError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "work", "claimed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mg-odd.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewMGClaimRestampVerifier(root, 1)("mg-odd"); err == nil {
		t.Error("a claim file with no pid suffix returned an answer instead of an error")
	}
}

// TestMGClaimRestampVerifier_EmptyWorkItemIsAnError: an empty id would scan for
// the prefix ".md." and match another item's claim. Refuse rather than answer
// about the wrong item.
func TestMGClaimRestampVerifier_EmptyWorkItemIsAnError(t *testing.T) {
	if _, err := NewMGClaimRestampVerifier(t.TempDir(), 1)(""); err == nil {
		t.Error("verifier answered about an empty work item id")
	}
}

// TestClaimRestampReadsTheStoreTheClaimWentInto pins the coupling
// MacguffinStoreRoot exists for. The verifier reads the claim file ClaimForSpawn
// wrote, so a second resolution of "which store" would be free to disagree — and
// the way it would disagree is by reading ~/.macguffin while pogod claimed under
// MG_ROOT, reporting every polecat started. Same shape as
// TestSpawnClaimAndReleaseResolveTheSameStore.
func TestClaimRestampReadsTheStoreTheClaimWentInto(t *testing.T) {
	want := MGClaimReleaser{}.storeRoot()
	if got := MacguffinStoreRoot(); got != want {
		t.Errorf("MacguffinStoreRoot() = %q but the claim/release path resolves %q — the "+
			"claim-pid started-signal would read a store pogod never claimed into", got, want)
	}
}

// ---------------------------------------------------------------------------
// The capability gate.
// ---------------------------------------------------------------------------

// fakeMGWithReclaim writes an executable named "mg" that exits with `code` for
// `mg reclaim --help`, and returns its path. The probe's whole contract is the
// exit status of that one invocation, so this is the honest fixture for it.
func fakeMGWithReclaim(t *testing.T, code int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mg")
	script := "#!/bin/sh\nif [ \"$1\" = \"" + MGReclaimSubcommand + "\" ] && [ \"$2\" = \"--help\" ]; then\n" +
		"  echo 'Re-stamp the pid on a claim' >&2\n  exit " + strconv.Itoa(code) + "\nfi\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMGSupportsClaimRestamp(t *testing.T) {
	if !MGSupportsClaimRestamp(fakeMGWithReclaim(t, 0)) {
		t.Error("probe reported no support for an mg whose `reclaim --help` exits 0")
	}
	if MGSupportsClaimRestamp(fakeMGWithReclaim(t, 1)) {
		t.Error("probe reported support for an mg whose `reclaim --help` fails — cobra exits " +
			"non-zero on an unknown command, which is the entire discriminator")
	}
	if MGSupportsClaimRestamp(filepath.Join(t.TempDir(), "not-a-binary")) {
		t.Error("probe reported support for a binary that does not exist")
	}
}

// TestEnableClaimRestampSignal_BothHalvesOrNeither is the structural guard, and
// it is the most load-bearing test in this file.
//
// Half of this mechanism is a DEFECT, in both directions. The verifier without the
// prompt step gates on a re-stamp no polecat is told to perform: every healthy
// agent reads unstarted, draws three CRs, and emits three auto_renudge rows per
// dispatch — an event stream of pure false positives, which is worse than the gap
// it replaces because it also destroys the signal an operator reads. The prompt
// step without the verifier is a command nothing observes.
//
// So the two are wired from one probe, through one entry point, and this asserts
// they can only move together.
func TestEnableClaimRestampSignal_BothHalvesOrNeither(t *testing.T) {
	restoreClaimRestampCommand(t)

	reg := &Registry{}
	if !reg.EnableClaimRestampSignal(fakeMGWithReclaim(t, 0), 4242) {
		t.Fatal("EnableClaimRestampSignal reported off for a supporting mg")
	}
	if reg.getClaimRestampVerifier() == nil {
		t.Error("the signal engaged but no verifier was installed: polecats are told to " +
			"re-stamp and nothing watches whether they did")
	}
	if ClaimRestampCommand() == "" {
		t.Error("the signal engaged but no prompt step was installed: the watcher gates on a " +
			"re-stamp no polecat is told to perform, so every healthy agent draws three " +
			"spurious recovery CRs")
	}

	off := &Registry{}
	if off.EnableClaimRestampSignal(fakeMGWithReclaim(t, 1), 4242) {
		t.Fatal("EnableClaimRestampSignal reported on for an mg with no reclaim subcommand")
	}
	if off.getClaimRestampVerifier() != nil {
		t.Error("a verifier was installed for an mg that cannot re-stamp; every polecat on " +
			"this daemon would be renudged three times")
	}
	if ClaimRestampCommand() != "" {
		t.Error("a prompt step was installed for an mg that cannot re-stamp; every polecat's " +
			"step 1 would fail on an unknown command")
	}
}

// TestEnableClaimRestampSignal_OffKeepsTheFallback: turning the gate off must
// leave start-verification watching the claimed-at-spawn dispatch on the weaker
// composer signal, not drop it. An mg without `mg reclaim` is a working
// deployment, and mg-c33e's coverage of it still has to hold.
func TestEnableClaimRestampSignal_OffKeepsTheFallback(t *testing.T) {
	restoreClaimRestampCommand(t)

	verifier, _ := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3)
	reg.EnableClaimRestampSignal(fakeMGWithReclaim(t, 1), 4242)

	a, readAll := wedgedPolecat(t, "mg-fallback")
	// promptReadySeen is latched by wedgedPolecat, so the fallback reports started
	// and nothing is delivered — the mg-7d6d gap, in the one state where it is now
	// a deployment fact with a named remedy rather than a design hole.
	reg.verifyStartAndRenudge(a)
	if got := readAll(); got != "" {
		t.Errorf("got %q; with the gate off the composer fallback governs", got)
	}

	// And a polecat whose composer never rendered is still rescued by it.
	b, readB, _ := newRenudgeTestAgent(t, "mg-nocomposer")
	b.ClaimedAtSpawn = true
	reg.verifyStartAndRenudge(b)
	if got := readB(); got != "\r\r\r" {
		t.Errorf("got %q, want 3 CRs — with the claim-pid gate off, mg-c33e's ready-composer "+
			"coverage must still hold", got)
	}
}

// restoreClaimRestampCommand resets the process-wide prompt step after a test
// touches it. It is process-wide for the reason coordinatorName is — it is a
// property of the deployment — which means a test that sets it and does not
// restore it leaks the re-stamp step into every later template expansion.
func restoreClaimRestampCommand(t *testing.T) {
	t.Helper()
	prev := ClaimRestampCommand()
	t.Cleanup(func() { SetClaimRestampCommand(prev) })
	SetClaimRestampCommand("")
}

// ---------------------------------------------------------------------------
// The prompt step, in the shipped templates.
// ---------------------------------------------------------------------------

// shippedPolecatTemplates is every embedded worker template. All six carry step 1,
// so all six must carry the re-stamp — a signal that is hard for the default track
// and absent on the review track would be a detector with a hole shaped like a
// dispatch template.
var shippedPolecatTemplates = []string{
	"prompts/templates/polecat.md",
	"prompts/templates/polecat-qa.md",
	"prompts/templates/polecat-build-pr.md",
	"prompts/templates/polecat-triage.md",
	"prompts/templates/polecat-review.md",
	"prompts/templates/polecat-architect.md",
}

// TestShippedTemplatesCarryTheRestampStep guards the prompt half against a stray
// edit. pogod's hard started-signal observes an action only the prompt asks for, so
// a template that loses this block silently converts every dispatch on that track
// into three spurious recovery CRs — the failure the capability gate exists to
// prevent, reintroduced from the other end.
func TestShippedTemplatesCarryTheRestampStep(t *testing.T) {
	for _, name := range shippedPolecatTemplates {
		data, err := defaultPrompts.ReadFile(name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		body := string(data)
		for _, want := range []string{"{{if .ClaimRestampCmd}}", "{{.ClaimRestampCmd}}", "{{end}}"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: expected %q — the claim-pid started-signal (mg-7d6d) watches for "+
					"an act only this step asks for", name, want)
			}
		}
	}
}

// TestShippedTemplatesGateTheRestampStep is the half that matters more, and it
// asserts behaviour rather than substrings: with the mechanism unavailable the step
// must be ABSENT from the rendered prompt, because the command it names does not
// exist on that host (macguffin mg-bb43) and an unconditional step would fail every
// polecat's step 1.
func TestShippedTemplatesGateTheRestampStep(t *testing.T) {
	testsandbox.Isolate(t)
	restoreClaimRestampCommand(t)

	for _, name := range shippedPolecatTemplates {
		data, err := defaultPrompts.ReadFile(name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		short := filepath.Base(name)
		writeTemplate(t, strings.TrimSuffix(short, ".md"), string(data))
		path, err := ResolveTemplate(strings.TrimSuffix(short, ".md"))
		if err != nil {
			t.Fatalf("ResolveTemplate %s: %v", short, err)
		}

		// Gate OFF: ClaimRestampCommand() is "", so withDefaults leaves the field
		// empty and the block renders away.
		SetClaimRestampCommand("")
		off, err := ExpandTemplate(path, TemplateVars{Id: "mg-0000"})
		if err != nil {
			t.Fatalf("expand %s: %v", short, err)
		}
		if strings.Contains(off, "re-stamp the claim") {
			t.Errorf("%s: the re-stamp step rendered with the mechanism unavailable; every "+
				"polecat's step 1 would run a command its mg does not have", short)
		}

		// Gate ON.
		SetClaimRestampCommand("mg " + MGReclaimSubcommand)
		on, err := ExpandTemplate(path, TemplateVars{Id: "mg-0000"})
		if err != nil {
			t.Fatalf("expand %s: %v", short, err)
		}
		if !strings.Contains(on, "mg "+MGReclaimSubcommand+" mg-0000") {
			t.Errorf("%s: the re-stamp step did not render the command with {{.Id}} expanded; "+
				"the rendered prompt is what the polecat actually runs", short)
		}
		if !strings.Contains(on, "re-stamp the claim") {
			t.Errorf("%s: the re-stamp step is missing from the rendered prompt", short)
		}
	}
}

// TestRestampStepOmittedWithoutAWorkItem pins the prompt half of the agreement
// claimRestampCmdFor documents. A dispatch with no --id has no claim to re-stamp,
// and Registry.startedSignal's pid arm declines it for exactly that reason. If the
// step rendered anyway the polecat would run `mg reclaim` with no argument, fail,
// and be watched on the composer signal regardless — a confusing failure on the
// `--no-worktree` path mg-560d proved load-bearing.
func TestRestampStepOmittedWithoutAWorkItem(t *testing.T) {
	testsandbox.Isolate(t)
	restoreClaimRestampCommand(t)
	SetClaimRestampCommand("mg " + MGReclaimSubcommand)

	data, err := defaultPrompts.ReadFile("prompts/templates/polecat.md")
	if err != nil {
		t.Fatal(err)
	}
	writeTemplate(t, "polecat", string(data))
	path, err := ResolveTemplate("polecat")
	if err != nil {
		t.Fatal(err)
	}

	out, err := ExpandTemplate(path, TemplateVars{}) // no Id
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if strings.Contains(out, "re-stamp the claim") {
		t.Error("the re-stamp step rendered for a dispatch with no work item id; there is no " +
			"claim to re-stamp and the watcher does not watch for one either")
	}
}
