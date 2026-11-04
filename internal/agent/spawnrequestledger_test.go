package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests for drellem2/pogo#265's retry half: a second spawn request for the same
// work item or name, arriving while the first is still inside the handler, must
// be refused with a non-overridable 409 — not raced into the same tree.
//
// Every request here uses an unresolvable template, so a request the ledger
// lets through fails early with no git side effects. That keeps the tests safe
// to fail: a broken ledger produces a wrong STATUS, never a real spawn.

const ledgerBogusTemplate = "no-such-template-265"

// holdFirstSpawn installs a hook that parks the first request to reach it until
// release is closed. entered is closed once that request is held. Later requests
// pass through the hook without blocking.
func holdFirstSpawn(reg *Registry) (entered chan struct{}, release chan struct{}) {
	entered = make(chan struct{})
	release = make(chan struct{})
	var once sync.Once
	reg.spawnRequestHeldHook = func(route, name, workItemID string) {
		held := false
		once.Do(func() { held = true })
		if held {
			close(entered)
			<-release
		}
	}
	return entered, release
}

func waitClosed(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestSpawnRequestLedger_RefusesARetryWhileTheFirstIsHeld is the acceptance
// test and its positive control: hold spawn #1 inside the handler, fire #2 at
// the same item and at the same name, and assert a 409 that cites the
// agent_spawn_requested record and offers no override. Then release #1 and
// assert the ledger is empty and a retry is answered by the ordinary gates — the
// live-owner refusal once an owner exists — rather than refused forever.
func TestSpawnRequestLedger_RefusesARetryWhileTheFirstIsHeld(t *testing.T) {
	sandboxWitness(t)
	logPath := useTempEventLog(t)
	reg := newDrainTestRegistry(t)
	entered, release := holdFirstSpawn(reg)

	first := SpawnPolecatAPIRequest{Name: "first265", Id: "mg-ledger-265", Repo: "/some/repo", Template: ledgerBogusTemplate}
	firstDone := make(chan struct{})
	var firstRR *httptest.ResponseRecorder
	go func() {
		defer close(firstDone)
		firstRR = spawnPolecat(t, reg, first)
	}()
	waitClosed(t, entered, "spawn #1 to be held inside the handler")

	for _, tc := range []struct {
		half string
		req  SpawnPolecatAPIRequest
	}{
		{"item", SpawnPolecatAPIRequest{Name: "second265", Id: "mg-ledger-265", Repo: "/some/repo", Template: ledgerBogusTemplate}},
		{"name", SpawnPolecatAPIRequest{Name: "first265", Id: "mg-other-265", Repo: "/some/repo", Template: ledgerBogusTemplate}},
	} {
		rr := spawnPolecat(t, reg, tc.req)
		if rr.Code != http.StatusConflict {
			t.Fatalf("%s half: status = %d while spawn #1 is in flight, want 409; body: %s",
				tc.half, rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		for _, want := range []string{"ALREADY IN PROGRESS", "agent_spawn_requested", "first265", "mg-ledger-265",
			"pogo events list --agent cat-first265"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s half: refusal must contain %q, got: %s", tc.half, want, body)
			}
		}
		assertNotOverridable(t, body)
	}

	// Negative control: an unrelated request is NOT refused by the ledger, so
	// the 409s above are about the collision and not a ledger that refuses all.
	if rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: "third265", Id: "mg-unrelated-265",
		Repo: "/some/repo", Template: ledgerBogusTemplate}); strings.Contains(rr.Body.String(), "ALREADY IN PROGRESS") {
		t.Errorf("an unrelated request was refused as in-flight: %s", rr.Body.String())
	}

	// The refusal is on the record, like every other refusal on this route.
	if ev := findEvent(readEventLines(t, logPath), "agent_spawn_failed", "cat-second265"); ev == nil {
		t.Error("the in-flight refusal emitted no agent_spawn_failed event")
	}

	close(release)
	waitClosed(t, firstDone, "spawn #1 to finish")
	if strings.Contains(firstRR.Body.String(), "ALREADY IN PROGRESS") {
		t.Fatalf("spawn #1 was refused by its own entry: %s", firstRR.Body.String())
	}
	if n := reg.spawnRequestsInFlightCount(); n != 0 {
		t.Fatalf("ledger holds %d entries after spawn #1 returned: an exit path leaked one", n)
	}

	// After #1: no ledger refusal for the same keys...
	if rr := spawnPolecat(t, reg, first); strings.Contains(rr.Body.String(), "ALREADY IN PROGRESS") {
		t.Errorf("a retry after spawn #1 ended was still refused as in-flight: %s", rr.Body.String())
	}
	// ...and once an owner exists, the ordinary live-owner refusal answers.
	reg.agents["first265"] = livePolecat("first265", "mg-ledger-265")
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: "fourth265", Id: "mg-ledger-265",
		Repo: "/some/repo", Template: ledgerBogusTemplate})
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "ALREADY BEING WORKED") ||
		strings.Contains(rr.Body.String(), "ALREADY IN PROGRESS") {
		t.Errorf("with a live owner, want the live-owner 409; got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestSpawnRequestLedger_PanicDoesNotLeakAnEntry: a leaked entry refuses its
// item and name until pogod restarts, so the release must run on a panic's
// unwind, not only on returns.
func TestSpawnRequestLedger_PanicDoesNotLeakAnEntry(t *testing.T) {
	sandboxWitness(t)
	useTempEventLog(t)
	reg := newDrainTestRegistry(t)
	reg.spawnRequestHeldHook = func(route, name, workItemID string) { panic("boom mid-spawn") }

	req := SpawnPolecatAPIRequest{Name: "panic265", Id: "mg-panic-265", Repo: "/some/repo", Template: ledgerBogusTemplate}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the hook's panic did not propagate: this test proves nothing")
			}
		}()
		spawnPolecat(t, reg, req)
	}()
	if n := reg.spawnRequestsInFlightCount(); n != 0 {
		t.Fatalf("ledger holds %d entries after a panicking spawn: the release is not deferred", n)
	}
	reg.spawnRequestHeldHook = nil
	if rr := spawnPolecat(t, reg, req); strings.Contains(rr.Body.String(), "ALREADY IN PROGRESS") {
		t.Errorf("a panicked spawn's item stays refused: %s", rr.Body.String())
	}
}

// TestSpawnRequestLedger_StaysOpenPastA202 is the case the ledger is mostly
// for: the caller got 202 spawn-still-running and is tempted to retry, while the
// handler is still running behind WithSpawnResponseDeadline. The entry must
// still be open then, and close when the handler really ends.
func TestSpawnRequestLedger_StaysOpenPastA202(t *testing.T) {
	sandboxWitness(t)
	useTempEventLog(t)
	withSpawnDeadline(t, 50*time.Millisecond)
	reg := newDrainTestRegistry(t)
	entered, release := holdFirstSpawn(reg)
	h := WithSpawnResponseDeadline(spawnPolecatRoute, reg.handleSpawnPolecat)

	post := func(req SpawnPolecatAPIRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(req)
		rr := httptest.NewRecorder()
		h(rr, httptest.NewRequest("POST", spawnPolecatRoute, bytes.NewReader(body)))
		return rr
	}

	req := SpawnPolecatAPIRequest{Name: "slow265", Id: "mg-slow-265", Repo: "/some/repo", Template: ledgerBogusTemplate}
	firstAnswered := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstAnswered <- post(req) }()
	waitClosed(t, entered, "spawn #1 to be held")
	if rr := <-firstAnswered; rr.Code != http.StatusAccepted {
		t.Fatalf("spawn #1 status = %d, want 202 spawn-still-running; body: %s", rr.Code, rr.Body.String())
	}

	if rr := post(req); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "ALREADY IN PROGRESS") {
		t.Fatalf("a retry after the 202, while #1 still runs: status %d, want the in-flight 409; body: %s",
			rr.Code, rr.Body.String())
	}

	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for reg.spawnRequestsInFlightCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("ledger entry still open 10s after the late handler was released")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSpawnRequestLedger_StartSharesTheNameSpace: /agents/start is keyed on the
// name in the same ledger, because a crew agent and a polecat register under
// one agent name space.
func TestSpawnRequestLedger_StartSharesTheNameSpace(t *testing.T) {
	sandboxWitness(t)
	useTempEventLog(t)
	t.Setenv("POGO_HOME", t.TempDir())
	reg := newDrainTestRegistry(t)
	entered, release := holdFirstSpawn(reg)

	done := make(chan struct{})
	go func() {
		defer close(done)
		spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: "shared265", Id: "mg-shared-265",
			Repo: "/some/repo", Template: ledgerBogusTemplate})
	}()
	waitClosed(t, entered, "spawn #1 to be held")

	rr := httptest.NewRecorder()
	reg.handleStart(rr, httptest.NewRequest("POST", startRoute, strings.NewReader(`{"name":"shared265"}`)))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "ALREADY IN PROGRESS") {
		t.Errorf("start under a name with a spawn in flight: status %d, want the in-flight 409; body: %s",
			rr.Code, rr.Body.String())
	}
	close(release)
	waitClosed(t, done, "spawn #1 to finish")
	if n := reg.spawnRequestsInFlightCount(); n != 0 {
		t.Fatalf("ledger holds %d entries after both requests returned", n)
	}
}

// TestOpenSpawnRequest_CheckAndSet pins the primitive: it records nothing on a
// hit, records nothing for empty keys, and a double release never deletes a
// successor's entry.
func TestOpenSpawnRequest_CheckAndSet(t *testing.T) {
	reg := &Registry{}

	rel, refusal := reg.openSpawnRequest(spawnPolecatRoute, "", "")
	if refusal != "" || reg.spawnRequestsInFlightCount() != 0 {
		t.Fatalf("empty keys: refusal %q, count %d; want neither", refusal, reg.spawnRequestsInFlightCount())
	}
	rel()

	rel1, refusal := reg.openSpawnRequest(spawnPolecatRoute, "a", "mg-1")
	if refusal != "" || reg.spawnRequestsInFlightCount() != 2 {
		t.Fatalf("first open: refusal %q, count %d; want none and 2", refusal, reg.spawnRequestsInFlightCount())
	}
	if rel, refusal := reg.openSpawnRequest(spawnPolecatRoute, "b", "mg-1"); refusal == "" || rel != nil {
		t.Fatal("an item collision was not refused")
	}
	if reg.spawnRequestsInFlightCount() != 2 {
		t.Fatal("a refused open recorded its other key")
	}
	rel1()
	rel2, refusal := reg.openSpawnRequest(spawnPolecatRoute, "a", "mg-1")
	if refusal != "" {
		t.Fatalf("reopen after release refused: %s", refusal)
	}
	rel1() // a stale double release must not delete the successor's entries
	if reg.spawnRequestsInFlightCount() != 2 {
		t.Fatal("a double release deleted a successor's entries")
	}
	rel2()
	if reg.spawnRequestsInFlightCount() != 0 {
		t.Fatal("release left entries behind")
	}

	// Concurrency: of N simultaneous opens on one item, exactly one wins.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if _, refusal := reg.openSpawnRequest(spawnPolecatRoute, "n"+string(rune('a'+i)), "mg-race"); refusal == "" {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d concurrent opens on one item succeeded, want exactly 1", wins)
	}
}
