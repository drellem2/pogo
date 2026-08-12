package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/stallwatch"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// Tests for dispatch-time work-item claiming (mg-7254). See spawnclaim.go for
// the defect these are about: claiming was the polecat's own first step, so it
// required a model-API turn, so a polecat wedged on 529 Overloaded ran for half
// an hour with its item still in available/.
//
// mgNewAvailable, below, is the fixture that matters. Every test in this file
// leaves the work item exactly where a real dispatch finds it — in available/ —
// rather than pre-claiming it, because "the item was already claimed" is the one
// state in which this whole class of bug is invisible.

// mgNewAvailable creates a work item in the sandbox store and leaves it in
// available/, which is where dispatch finds one. Compare mgNewClaimed in
// claimrelease_test.go, which claims it: that fixture models a polecat
// mid-flight, this one models the moment before a polecat exists.
func mgNewAvailable(t *testing.T, root, title string) string {
	t.Helper()
	// --no-repo for the reason spelled out at mgNewClaimed in
	// claimrelease_test.go: the fixture is about nothing, and letting mg resolve
	// a repo from the cwd records a path pogo deletes at reap (mg-1eb6).
	out, err := exec.Command("mg", "--root", root, "new", "--no-repo", title).CombinedOutput()
	if err != nil {
		t.Fatalf("mg new: %v: %s", err, out)
	}
	m := mgCreatedID.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("could not parse work item id out of %q", out)
	}
	id := m[1]
	if got := mgStatus(t, root, id); got != "available" {
		t.Fatalf("fixture %s is in %q, want available — the defect under test is only "+
			"observable on an item dispatch would actually pick up", id, got)
	}
	return id
}

// noopClaimer is pogod BEFORE this change: it takes no claim at dispatch,
// leaving ownership to the polecat's own `mg claim` step. It is not a stub for
// convenience — it is the pre-fix daemon, and the positive control below installs
// it deliberately to prove the assertions in these tests can fail.
type noopClaimer struct{}

func (noopClaimer) ClaimForSpawn(string) ClaimVerdict {
	return ClaimVerdict{Outcome: ClaimUnknown, Detail: "claiming is the polecat's job (pre-mg-7254)"}
}

// newClaimTestRegistry builds a registry wired to one macguffin sandbox on BOTH
// the claim and the release side. Setting only one is the footgun documented on
// Registry.releaseSpawnClaim — a rollback would then release nothing, silently —
// and TestSpawnClaimAndReleaseResolveTheSameStore pins that they agree in
// production.
func newClaimTestRegistry(t *testing.T, root string) *Registry {
	t.Helper()
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	reg.SetCommandConfig(catCommandConfig{})
	reg.SetWorkItemClaimer(MGWorkItemClaimer{Root: root})
	reg.SetClaimReleaser(MGClaimReleaser{Root: root})
	return reg
}

// spawnPolecatRaw posts a spawn request and returns the recorder without
// asserting on the status, so refusal tests can read it.
func spawnPolecatRaw(t *testing.T, reg *Registry, spawnReq SpawnPolecatAPIRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(spawnReq)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/agents/spawn-polecat", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr, req)
	return rr
}

// claimTestTemplate writes a worker template and returns its name. The body is
// irrelevant to these tests — what matters is that the spawn reaches the claim
// rather than being refused earlier by the template router (mg-9a04).
func claimTestTemplate(t *testing.T) string {
	t.Helper()
	writeTemplate(t, "claimcat", "body for {{.Id}}\n")
	return "claimcat"
}

// TestSpawnClaimsWorkItemBeforeTheFirstTurn is the guard mg-7254 asks for: a
// dispatched polecat's item must not sit in available/ while that polecat is
// healthy and working.
//
// The agent command here is `cat` (catCommandConfig) — a process that starts,
// stays alive, and executes NO turn. That is not a testing shortcut, it is the
// production failure faithfully: polecat cat-7254 was spawned at ~20:43Z,
// recorded failing_turns=2 against `API Error: 529 Overloaded`, and ran for 27
// minutes without completing a single turn. A polecat that cannot reach its own
// `mg claim` step and a polecat running `cat` are indistinguishable from the
// store's point of view, which is exactly the point: ownership must not depend on
// the worker doing anything.
func TestSpawnClaimsWorkItemBeforeTheFirstTurn(t *testing.T) {
	testsandbox.Isolate(t)
	root := mgSandbox(t)
	id := mgNewAvailable(t, root, "work the polecat will never get to claim itself")

	reg := newClaimTestRegistry(t, root)
	rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-claim", Id: id, Template: claimTestTemplate(t),
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("spawn status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	// The polecat is registered and alive, and has run nothing.
	a := reg.Get("cat-claim")
	if a == nil {
		t.Fatal("polecat not registered after a 201")
	}

	if got := mgStatus(t, root, id); got != "claimed" {
		t.Errorf("work item %s is in %q while a live polecat works it, want claimed — "+
			"this is the mg-7254 state: invisible to every ownership check the fleet has", id, got)
	}
	if mgDispatchable(t, root, id) {
		t.Errorf("work item %s is still dispatchable while %s works it — a second dispatch "+
			"onto it would not be refused by anything", id, a.Name)
	}
}

// TestUnclaimedWorkingPolecatIsTheDefect IS THE POSITIVE CONTROL, and the
// acceptance criterion that demanded it is worth quoting: "prove the detection
// can fail — construct the unclaimed-but-working state deliberately and confirm
// whatever guard you add actually fires on it. A guard tested only against a
// normally-claimed item passes today, with this defect present."
//
// So this runs the test above against pogod as it was — noopClaimer, ownership
// left to the polecat's own step 1 — and asserts the bad state ARISES: the item
// stays in available/, stays dispatchable, and stall-watch reports it as
// neglected while the polecat is alive. Both halves are asserted, because the
// false wake is not a side effect of the defect, it is the cost that promoted
// this ticket: seven of them in one evening.
//
// Without this test, TestSpawnClaimsWorkItemBeforeTheFirstTurn would still pass
// if the claim were taken by something incidental, or if the fixture drifted into
// being pre-claimed, and nothing would say so.
func TestUnclaimedWorkingPolecatIsTheDefect(t *testing.T) {
	testsandbox.Isolate(t)
	root := mgSandbox(t)
	id := mgNewAvailable(t, root, "work nobody claims")

	reg := newClaimTestRegistry(t, root)
	reg.SetWorkItemClaimer(noopClaimer{}) // pogod before mg-7254

	rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-unclaimed", Id: id, Template: claimTestTemplate(t),
	})
	// Fails OPEN, so the dispatch still happens — that is what made the defect
	// silent, and it is also why the fail-open direction had to be chosen
	// deliberately rather than inherited.
	if rr.Code != http.StatusCreated {
		t.Fatalf("spawn status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if a := reg.Get("cat-unclaimed"); a == nil {
		t.Fatal("polecat not registered; the control needs a LIVE polecat on an unclaimed item")
	}

	// Half one: the item is stranded in available/ under a working polecat.
	if got := mgStatus(t, root, id); got != "available" {
		t.Fatalf("control did not reproduce the defect: %s is in %q, want available. "+
			"Something other than the claim-at-spawn mechanism is moving the item, so the "+
			"guard test above proves nothing", id, got)
	}
	if !mgDispatchable(t, root, id) {
		t.Error("control did not reproduce the defect: the item is not dispatchable, so a " +
			"second dispatch would be refused and there is nothing to fix")
	}

	// Half two: stall-watch reports it as neglected. Same store, real Watcher —
	// the noise this ticket was promoted for, reproduced rather than described.
	var fired []events.Event
	w := stallwatch.New(config.StallWatchConfig{
		Enabled:                   true,
		Agent:                     "mayor",
		UnclaimedItemAgeThreshold: 10 * time.Minute,
	}, stallwatch.Options{
		WorkRoot: filepath.Join(root, "work"),
		MailRoot: filepath.Join(root, "mail"),
		Nudge: func(string, stallwatch.Notice) (stallwatch.Delivery, error) {
			return stallwatch.Delivery{}, nil
		},
		Emit: func(e events.Event) { fired = append(fired, e) },
	})
	// An hour on, past the 10-minute threshold — the same distance out mg-86e7
	// was when it drew its second false wake.
	w.Check(time.Now().Add(time.Hour))

	if !firedOn(fired, id) {
		t.Fatalf("stall-watch did NOT report the unclaimed-but-worked item %s as neglected. "+
			"Either the control is not reproducing the state or stall-watch stopped reading it; "+
			"either way the guard's value is unproven. events=%+v", id, fired)
	}

	// And the close of the loop: with the mechanism ON, the same watcher over the
	// same store is silent. This is the pair that makes the guard's value a
	// measurement rather than a claim.
	reg2 := newClaimTestRegistry(t, root)
	if rr := spawnPolecatRaw(t, reg2, SpawnPolecatAPIRequest{
		Name: "cat-claimed", Id: id, Template: claimTestTemplate(t),
	}); rr.Code != http.StatusCreated {
		t.Fatalf("re-dispatch with the claimer on: status = %d, body=%s", rr.Code, rr.Body.String())
	}
	fired = nil
	w.Check(time.Now().Add(2 * time.Hour))
	if firedOn(fired, id) {
		t.Errorf("stall-watch still reports %s as neglected after the claim-at-spawn "+
			"mechanism took it out of available/: %+v", id, fired)
	}
}

// firedOn reports whether any recorded stall_watch_fired event names id among
// its item_ids. It reads the event rather than the nudge text because the event
// is the structured record; the message is prose.
func firedOn(recorded []events.Event, id string) bool {
	for _, e := range recorded {
		if e.EventType != "stall_watch_fired" {
			continue
		}
		ids, ok := e.Details["item_ids"].([]string)
		if !ok {
			continue
		}
		for _, got := range ids {
			if got == id {
				return true
			}
		}
	}
	return false
}

// TestSpawnRefusedWhenWorkItemAlreadyClaimed is the duplicate-dispatch guard the
// ticket asks for by name: "Nothing prevents a second dispatch [...] that check
// lives in the dispatcher's memory, which is exactly the class of control that
// fails."
//
// It now lives in the store. macguffin's claim is a rename(2) out of available/,
// so the second dispatch loses atomically rather than by anyone remembering to
// look first.
func TestSpawnRefusedWhenWorkItemAlreadyClaimed(t *testing.T) {
	testsandbox.Isolate(t)
	root := mgSandbox(t)
	tmpl := claimTestTemplate(t)
	id := mgNewAvailable(t, root, "one item, two dispatches")

	reg := newClaimTestRegistry(t, root)
	if rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-first", Id: id, Template: tmpl,
	}); rr.Code != http.StatusCreated {
		t.Fatalf("first dispatch status = %d, body=%s", rr.Code, rr.Body.String())
	}

	rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-second", Id: id, Template: tmpl,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("second dispatch onto %s: status = %d, want 409 — two polecats on one item "+
			"means duplicated branches and a merge race", id, rr.Code)
	}
	// The refusal has to be actionable without reading source: name the item, and
	// name both ways out (check for a live owner, or unclaim).
	body := rr.Body.String()
	for _, want := range []string{id, "pogo agent list", "mg unclaim"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal does not mention %q; a bare 409 sends an agent to read source: %q", want, body)
		}
	}
	// Nothing left behind, and the FIRST polecat's claim is untouched.
	if a := reg.Get("cat-second"); a != nil {
		t.Error("a refused dispatch registered an agent anyway")
	}
	if got := mgStatus(t, root, id); got != "claimed" {
		t.Errorf("work item %s is in %q after the refused second dispatch, want claimed — "+
			"the refusal must not disturb the first polecat's ownership", id, got)
	}
}

// missingBinCommandConfig makes Registry.Spawn fail: the command names a binary
// that does not exist, so the spawn dies after the claim has been taken. This is
// the acceptance criterion "check what happens when the spawn succeeds and the
// claim fails" read from the other side — the spawn fails and the claim
// succeeded, which strands the item unless the claim is given back.
type missingBinCommandConfig struct{}

func (missingBinCommandConfig) AgentCommand(string) string {
	return "/nonexistent/pogo-test-no-such-binary"
}
func (missingBinCommandConfig) AgentProvider(string) string { return "" }

// TestSpawnFailureReleasesTheClaimItTook — a claim taken for a spawn that then
// failed must go back. The stranded state is the mirror image of the one this
// ticket is about, and worse in one way: an item in claimed/ with no worker on it
// is skipped by dispatch AND unseen by stall-watch, so nothing at all reports it.
func TestSpawnFailureReleasesTheClaimItTook(t *testing.T) {
	testsandbox.Isolate(t)
	root := mgSandbox(t)
	id := mgNewAvailable(t, root, "work whose spawn will fail")

	reg := newClaimTestRegistry(t, root)
	reg.SetCommandConfig(missingBinCommandConfig{})

	rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-doomed", Id: id, Template: claimTestTemplate(t),
	})
	if rr.Code == http.StatusCreated {
		t.Fatalf("spawn unexpectedly SUCCEEDED with a nonexistent binary; this test needs "+
			"a failing spawn to be meaningful. body=%s", rr.Body.String())
	}

	if got := mgStatus(t, root, id); got != "available" {
		t.Errorf("work item %s is in %q after a failed spawn, want available — the claim "+
			"taken for a polecat that never ran was not released, and nothing watches claimed/", id, got)
	}
	if !mgDispatchable(t, root, id) {
		t.Errorf("work item %s is not dispatchable after a failed spawn; it can never be "+
			"retried", id)
	}
}

// TestSpawnClaimFailsOpen pins the documented fail-open direction for the cases
// where the claim establishes NOTHING. These are not aspirations: `--id` is
// optional by design (mg-2437), callers legitimately dispatch against ids that
// are not macguffin items, and failing closed on an unreadable store would let
// one bad path in macguffin halt the whole fleet.
func TestSpawnClaimFailsOpen(t *testing.T) {
	testsandbox.Isolate(t)
	root := mgSandbox(t)
	tmpl := claimTestTemplate(t)

	tests := []struct {
		name    string
		claimer MGWorkItemClaimer
		id      string
	}{
		{"no --id supplied", MGWorkItemClaimer{Root: root}, ""},
		{"id not in the store", MGWorkItemClaimer{Root: root}, "mg-ghost"},
		{"store does not exist", MGWorkItemClaimer{Root: filepath.Join(t.TempDir(), "absent")}, "mg-ghost"},
		{"mg binary missing", MGWorkItemClaimer{Root: root, Bin: "/nonexistent/mg"}, "mg-ghost"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := newClaimTestRegistry(t, root)
			reg.SetWorkItemClaimer(tt.claimer)
			rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
				Name: "cat-open" + string(rune('a'+i)), Id: tt.id, Template: tmpl,
			})
			if rr.Code == http.StatusConflict {
				t.Errorf("claim refused the dispatch (%s) when it must fail open: %s",
					tt.name, rr.Body.String())
			}
		})
	}
}

// TestMGWorkItemClaimerVerdicts exercises the discriminator directly: mg's frozen
// exit-code contract, not its error prose. A guard keyed on message text breaks
// the first time a message is reworded and nothing reports that it has.
func TestMGWorkItemClaimerVerdicts(t *testing.T) {
	root := mgSandbox(t)
	claimer := MGWorkItemClaimer{Root: root}

	available := mgNewAvailable(t, root, "claimable")
	if v := claimer.ClaimForSpawn(available); v.Outcome != ClaimTaken {
		t.Errorf("claiming an available item: outcome = %q (%s), want %q",
			v.Outcome, v.Detail, ClaimTaken)
	}
	// Now held — the second attempt is the conflict that must fail CLOSED.
	v := claimer.ClaimForSpawn(available)
	if v.Outcome != ClaimConflict {
		t.Errorf("re-claiming a held item: outcome = %q (%s), want %q — exit 4 is macguffin's "+
			"frozen conflict code", v.Outcome, v.Detail, ClaimConflict)
	}
	if v.Detail == "" {
		t.Error("a conflict verdict carries no detail; mg's own message is what makes the refusal actionable")
	}
	// Unknown id is exit 3 (not_found) and must fail OPEN, not be mistaken for a
	// conflict — this is the pair the exit-code split exists to keep apart.
	if v := claimer.ClaimForSpawn("mg-nosuchitem"); v.Outcome != ClaimUnknown {
		t.Errorf("claiming an unknown id: outcome = %q (%s), want %q", v.Outcome, v.Detail, ClaimUnknown)
	}
	if v := claimer.ClaimForSpawn(""); v.Outcome != ClaimSkipped {
		t.Errorf("empty id: outcome = %q, want %q", v.Outcome, ClaimSkipped)
	}
}

// TestSpawnClaimRecordsAPidThatOutlivesTheClaim — the claim file names pogod, not
// the `mg claim` subprocess mg would record by default. The pid is informational
// (macguffin never tests it for liveness, `mg unclaim` targets by id), but a
// claim naming a pid that died as it was written is indistinguishable from the
// stranded-under-a-dead-pid state mg-fb13 exists to detect.
func TestSpawnClaimRecordsAPidThatOutlivesTheClaim(t *testing.T) {
	root := mgSandbox(t)
	id := mgNewAvailable(t, root, "whose pid is on this claim")
	if v := (MGWorkItemClaimer{Root: root}).ClaimForSpawn(id); v.Outcome != ClaimTaken {
		t.Fatalf("claim failed: %q %s", v.Outcome, v.Detail)
	}

	entries, err := os.ReadDir(filepath.Join(root, "work", "claimed"))
	if err != nil {
		t.Fatal(err)
	}
	want := id + ".md." + strconv.Itoa(os.Getpid())
	for _, e := range entries {
		if e.Name() == want {
			return
		}
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	t.Errorf("no claim file named %q in claimed/ (got %v) — the recorded pid is not this "+
		"process, so it is a pid that has already exited", want, got)
}

// TestWorkItemClaimerDefaultIsFunctional — an unwired Registry must still claim.
// A mechanism that engages only after someone remembers to call
// SetWorkItemClaimer is absent in every deployment where they didn't, which is
// the same shape as the convention it replaces.
func TestWorkItemClaimerDefaultIsFunctional(t *testing.T) {
	reg := newDrainTestRegistry(t)
	c := reg.getWorkItemClaimer()
	if c == nil {
		t.Fatal("getWorkItemClaimer() returned nil on a fresh registry")
	}
	if _, ok := c.(MGWorkItemClaimer); !ok {
		t.Fatalf("default claimer is %T, want MGWorkItemClaimer", c)
	}
	// Explicit nil restores the default rather than disabling the mechanism.
	reg.SetWorkItemClaimer(nil)
	if reg.getWorkItemClaimer() == nil {
		t.Error("SetWorkItemClaimer(nil) disabled claiming; it must restore the default")
	}
}

// TestSpawnClaimAndReleaseResolveTheSameStore pins the one coupling
// Registry.releaseSpawnClaim documents: the rollback runs through ClaimReleaser,
// so claimer and releaser must resolve the SAME macguffin store or a failed
// spawn silently releases nothing and strands the item. In production both go
// through macguffinStoreRoot; this asserts that rather than leaving it as
// something to remember.
func TestSpawnClaimAndReleaseResolveTheSameStore(t *testing.T) {
	claimRoot := macguffinStoreRoot(MGWorkItemClaimer{}.Root)
	releaseRoot := MGClaimReleaser{}.storeRoot()
	if claimRoot != releaseRoot {
		t.Fatalf("claimer resolves %q but releaser resolves %q — a failed spawn would release "+
			"nothing and strand the item in claimed/", claimRoot, releaseRoot)
	}
	if claimRoot == "" {
		t.Fatal("both resolve to the empty string; neither can reach a store")
	}
}

// TestWorkItemClaimerDefaultRootIsTestSafe is a guard on the tests, not the
// claimer. The default root must never resolve to the live ~/.macguffin from a
// test binary: this claimer MUTATES the store, so a leak would claim Daniel's
// real work items out from under the running fleet. Strictly worse than the
// read-only leak TestDispatchGateDefaultRootIsTestSafe guards against, which is
// why it is asserted separately here.
func TestWorkItemClaimerDefaultRootIsTestSafe(t *testing.T) {
	root := macguffinStoreRoot("")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if live := filepath.Join(home, ".macguffin"); root == live {
		t.Fatalf("macguffinStoreRoot() = %q under a test binary — that is the LIVE store, "+
			"and this claimer writes to it", root)
	}
}
