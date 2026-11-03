package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// Regression tests for drellem2/pogo#169: spawn-polecat emitted nothing for
// its whole duration — its first events fired at the very end and every log
// line before the terminal one was conditional — so a slow spawn, a wedged
// spawn and a spawn never requested were indistinguishable. The fix is an
// unconditional request record (event + log line) at handler entry and four
// phase-boundary log lines.

// eventIndex returns the position of the first event matching type and agent,
// or -1.
func eventIndex(evs []map[string]any, eventType, agent string) int {
	for i, e := range evs {
		if e["event_type"] == eventType && e["agent"] == agent {
			return i
		}
	}
	return -1
}

// TestSpawnPolecat_RequestIsOnTheRecordBeforeAnyGate is the load-bearing half:
// a spawn refused at the very first gate must still leave a request record,
// and that record must precede the failure that closes it. The drain gate is
// the earliest refusal there is, so if the request survives that, it survives
// every gate after it.
func TestSpawnPolecat_RequestIsOnTheRecordBeforeAnyGate(t *testing.T) {
	logPath := useTempEventLog(t)
	logs := captureLog(t)
	reg := newDrainTestRegistry(t)
	reg.SetDraining(true)

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name:     "req-169",
		Id:       "mg-169",
		Repo:     "/some/repo",
		Template: "polecat-build-pr",
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("spawn while draining = %d, want 503", rr.Code)
	}

	evs := readEventLines(t, logPath)
	ri := eventIndex(evs, "agent_spawn_requested", "cat-req-169")
	if ri < 0 {
		t.Fatal("no agent_spawn_requested event: a refused spawn is again indistinguishable " +
			"from one never requested")
	}
	fi := eventIndex(evs, "agent_spawn_failed", "cat-req-169")
	if fi < 0 {
		t.Fatal("positive control failed: the drain refusal emitted no agent_spawn_failed, " +
			"so this test's fixture is not reaching the handler")
	}
	if ri > fi {
		t.Errorf("agent_spawn_requested (index %d) came after agent_spawn_failed (index %d): "+
			"the request must open the window the failure closes", ri, fi)
	}

	ev := evs[ri]
	if ev["work_item_id"] != "mg-169" {
		t.Errorf("work_item_id = %v, want mg-169", ev["work_item_id"])
	}
	if ev["repo"] != "/some/repo" {
		t.Errorf("repo = %v, want /some/repo", ev["repo"])
	}
	details, _ := ev["details"].(map[string]any)
	if details["agent_type"] != "polecat" {
		t.Errorf("details.agent_type = %v, want polecat", details["agent_type"])
	}
	if details["agent_name"] != "req-169" {
		t.Errorf("details.agent_name = %v, want req-169", details["agent_name"])
	}
	if details["template"] != "polecat-build-pr" {
		t.Errorf("details.template = %v, want polecat-build-pr", details["template"])
	}

	// The log line is unconditional too: no override, no model, no tag.
	if !strings.Contains(logs(), `polecat req-169: spawn requested (work item "mg-169", repo "/some/repo", template "polecat-build-pr")`) {
		t.Errorf("no unconditional request log line; log was:\n%s", logs())
	}
	// And a refusal at the first gate is not past the gates.
	if strings.Contains(logs(), "gates passed") {
		t.Errorf("a drain-refused spawn logged \"gates passed\"; log was:\n%s", logs())
	}
}

// TestSpawnPolecat_UndecodableRequestEmitsNoRequestEvent keeps the request
// record meaningful: it names what is being dispatched, so a body that names
// nothing produces none. Its agent_spawn_failed is the record instead.
func TestSpawnPolecat_UndecodableRequestEmitsNoRequestEvent(t *testing.T) {
	logPath := useTempEventLog(t)
	reg := newDrainTestRegistry(t)

	r := httptest.NewRequest("POST", "/agents/spawn-polecat", strings.NewReader("{not json"))
	rr := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr, r)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("undecodable request = %d, want 400", rr.Code)
	}
	evs := readEventLines(t, logPath)
	if eventIndex(evs, "agent_spawn_failed", "pogod") < 0 {
		t.Fatal("positive control failed: undecodable request emitted no agent_spawn_failed")
	}
	for _, e := range evs {
		if e["event_type"] == "agent_spawn_requested" {
			t.Errorf("undecodable request emitted agent_spawn_requested: %+v", e)
		}
	}
}

// TestSpawnPolecat_PhaseLinesNameHowFarASpawnGot drives a spawn past every gate
// and through the base-ref resolution, then fails it at `git worktree add`.
// The log must show the phases it completed and not the one it never reached,
// which is the whole use of the lines: the newest one says where a spawn is.
func TestSpawnPolecat_PhaseLinesNameHowFarASpawnGot(t *testing.T) {
	useTempEventLog(t)
	logs := captureLog(t)
	workDir, _ := makeRepoWithOrigin(t)
	reg := newDrainTestRegistry(t)

	pogoHome := t.TempDir()
	t.Setenv("POGO_HOME", pogoHome)
	tmplDir := filepath.Join(pogoHome, "agents", "templates")
	if err := os.MkdirAll(tmplDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "polecat.md"), []byte("task {{.Id}}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Block the worktree target so `git worktree add` fails after the base ref
	// is resolved — the same deterministic failure the rollback tests use.
	wtDir := filepath.Join(pogoHome, "polecats", "phase-169")
	if err := os.MkdirAll(wtDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "occupied.txt"), []byte("in the way\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name:     "phase-169",
		Id:       "mg-169",
		Repo:     workDir,
		Template: BuildWorkerTemplate,
	})
	if !strings.Contains(rr.Body.String(), "worktree creation failed") {
		t.Fatalf("expected a worktree-creation failure, got %d: %s", rr.Code, rr.Body.String())
	}

	out := logs()
	for _, want := range []string{
		"polecat phase-169: spawn requested",
		"polecat phase-169: gates passed (template " + BuildWorkerTemplate + ", worktree true, +",
		"polecat phase-169: base ref resolved to ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q; log was:\n%s", want, out)
		}
	}
	for _, notReached := range []string{
		"polecat phase-169: created worktree",
		"polecat phase-169: claim step done",
	} {
		if strings.Contains(out, notReached) {
			t.Errorf("log claims a phase the spawn never reached: %q; log was:\n%s", notReached, out)
		}
	}
	// Ordering: each phase line follows the one before it.
	iReq := strings.Index(out, "spawn requested")
	iGates := strings.Index(out, "gates passed")
	iBase := strings.Index(out, "base ref resolved")
	if !(iReq < iGates && iGates < iBase) {
		t.Errorf("phase lines out of order (requested@%d, gates@%d, base@%d)", iReq, iGates, iBase)
	}
}

// TestSpawnPolecat_SuccessfulSpawnLogsTheClaimStepAndOpensItsWindow covers the
// success path: a spawn that goes all the way out names the claim step's
// verdict on its last phase line, and its request event is followed by the
// agent_spawned that closes the window — with no agent_spawn_failed. The agent
// command is `cat`, the same stand-in spawnclaim_test.go uses.
func TestSpawnPolecat_SuccessfulSpawnLogsTheClaimStepAndOpensItsWindow(t *testing.T) {
	testsandbox.Isolate(t)
	logPath := useTempEventLog(t)
	logs := captureLog(t)
	root := mgSandbox(t)
	id := mgNewAvailable(t, root, "spawn whose phases are logged")

	reg := newClaimTestRegistry(t, root)
	rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
		Name: "ok-169", Id: id, Template: claimTestTemplate(t),
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("spawn status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	want := fmt.Sprintf("polecat ok-169: claim step done for work item %q: %s (+", id, ClaimTaken)
	if !strings.Contains(logs(), want) {
		t.Errorf("log is missing %q; log was:\n%s", want, logs())
	}

	evs := readEventLines(t, logPath)
	ri := eventIndex(evs, "agent_spawn_requested", "cat-ok-169")
	si := eventIndex(evs, "agent_spawned", "cat-ok-169")
	if ri < 0 || si < 0 {
		t.Fatalf("want both agent_spawn_requested and agent_spawned; indexes %d, %d", ri, si)
	}
	if ri > si {
		t.Errorf("agent_spawn_requested (index %d) came after agent_spawned (index %d)", ri, si)
	}
	if fi := eventIndex(evs, "agent_spawn_failed", "cat-ok-169"); fi >= 0 {
		t.Errorf("successful spawn emitted agent_spawn_failed: %+v", evs[fi])
	}
}
