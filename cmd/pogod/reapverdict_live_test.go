package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/refinery"
)

// mg-dfea's premise, established against the REAL mg binary rather than read
// off the ticket.
//
// The ticket says pogod "overwrites" the polecat's `mg done --result`. It does
// not, and the difference decides the fix. `mg done` on an already-done item is
// REFUSED — so whichever writer gets there first owns the sidecar and the second
// changes nothing. pogod destroys the worker's verdict by PREEMPTION, not by
// clobbering: it closes the item the instant the merge lands and stops the
// polecat ~0.5s later, so the polecat's own call is the one that gets refused.
//
// That is why "have pogod merge rather than overwrite" cannot be implemented as
// a merge at THIS writer — there is nothing here to merge with, because pogod is
// never second. The merge has to happen with something the author handed over
// while it was still running, which is what MergeRequest.Verdict carries.
//
// Both arms are driven through the same store so the asymmetry is the
// mechanism's, not the fixture's.
func TestMGDoneRefusesASecondResultRatherThanOverwritingIt(t *testing.T) {
	root := mgSandboxStore(t)

	readSidecar := func(id string) map[string]any {
		t.Helper()
		var found string
		for _, dir := range []string{"done", "claimed", "available", "archive"} {
			matches, _ := filepath.Glob(filepath.Join(root, "work", dir, id+".result.json*"))
			if len(matches) > 0 {
				found = matches[0]
				break
			}
		}
		if found == "" {
			t.Fatalf("no result sidecar anywhere in the store for %s", id)
		}
		b, err := os.ReadFile(found)
		if err != nil {
			t.Fatalf("read sidecar %s: %v", found, err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("sidecar %s is not JSON: %v (%s)", found, err, b)
		}
		return m
	}

	done := func(id, result string) error {
		return exec.Command("mg", "--root", root, "done", id, "--result="+result).Run()
	}

	// ARM 1 — the worker gets there first. Its verdict stands and pogod's later
	// call changes nothing. This is the outcome the reap path's log line now
	// names explicitly instead of reporting as a possible failure.
	workerFirst := mgClaimedItem(t, root, "worker closes its own item first")
	if err := done(workerFirst, `{"verdict":"pass","summary":"WORKER"}`); err != nil {
		t.Fatalf("worker's mg done: %v", err)
	}
	if err := done(workerFirst, `{"branch":"polecat-x","completed_by":"refinery"}`); err == nil {
		t.Error("mg accepted a second done — this test's premise, and the reap path's comment, are wrong")
	}
	if got := readSidecar(workerFirst); got["summary"] != "WORKER" {
		t.Errorf("the worker's result did not stand: %v", got)
	}

	// ARM 2 — pogod gets there first, which is what actually happens: the reap
	// fires at merge+0.5s and the polecat is still polling. The worker's verdict
	// is refused. Nothing was overwritten and nothing complained.
	pogodFirst := mgClaimedItem(t, root, "pogod closes the item at merge")
	if err := done(pogodFirst, `{"branch":"polecat-y","completed_by":"refinery"}`); err != nil {
		t.Fatalf("pogod's mg done: %v", err)
	}
	if err := done(pogodFirst, `{"verdict":"pass","summary":"WORKER"}`); err == nil {
		t.Fatal("mg accepted the worker's second done")
	}
	got := readSidecar(pogodFirst)
	if _, ok := got["verdict"]; ok {
		t.Fatalf("premise broken: the worker's verdict reached the sidecar after all: %v", got)
	}
	if got["completed_by"] != "refinery" {
		t.Errorf("expected the refinery's sidecar to stand, got %v", got)
	}
}

// The end-to-end control for mg-dfea, and the one that produces bytes an
// external instrument can measure: the REAL reap path, closing REAL work items
// through the REAL mg binary, for the two authors that matter.
//
// The predicate asserted here is the detector's own, from mg-bf3f's d2_cause.py
// D2.5 — "does this landed item's result sidecar carry ANY field beyond
// branch/mr/target". Over the live store on 2026-08-06 it answered yes for 10
// of 149. It is asserted in BOTH directions on purpose: an author that recorded
// a verdict must come out answered, and an author that recorded none must still
// come out as a drop. A fix that made every sidecar look answered would have
// removed the instrument instead of the defect.
func TestReapMergedPolecatMeasuredByTheDetectorsOwnPredicate(t *testing.T) {
	root := mgSandboxStore(t)
	// client.CompleteMGWorkItem shells out to `mg done` with no --root, so the
	// sandbox is pinned through the environment or this test closes items in
	// the developer's live store.
	t.Setenv("MG_ROOT", root)

	sidecarOf := func(id string) (map[string]json.RawMessage, string) {
		t.Helper()
		matches, _ := filepath.Glob(filepath.Join(root, "work", "done", id+".result.json*"))
		if len(matches) == 0 {
			t.Fatalf("no result sidecar for %s — the item did not land", id)
		}
		b, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatalf("read sidecar: %v", err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("sidecar is not JSON: %v (%s)", err, b)
		}
		return m, string(b)
	}

	// d2_cause.py D2.5, transcribed. The key set is the detector's, not ours.
	answered := func(side map[string]json.RawMessage) bool {
		for k := range side {
			switch k {
			case "branch", "completed_by", "mr", "target":
			default:
				return true
			}
		}
		return false
	}

	run := func(item string, verdict json.RawMessage) (map[string]json.RawMessage, string) {
		t.Helper()
		reg := &fakeReaper{agents: map[string]*agent.Agent{
			item: {Name: item, WorkItemID: item, Type: agent.TypePolecat},
		}}
		mr := &refinery.MergeRequest{
			ID: "mr-" + item, Branch: "polecat-" + item, Author: item,
			TargetRef: "main", Verdict: verdict,
		}
		reapMergedPolecat(reg, mr, client.CompleteMGWorkItem, postMergeVerdict{}, nil)
		return sidecarOf(item)
	}

	// BEFORE — an author with no verdict. This is byte-for-byte what every
	// pre-mg-dfea merge produced: the field is new, so no pogod before this
	// change could set it, and the merge below is gated on its presence.
	silent := mgClaimedItem(t, root, "an author that records no verdict")
	side, raw := run(silent, nil)
	t.Logf("no-verdict sidecar (the pre-fix shape): %s", raw)
	if answered(side) {
		t.Errorf("a verdict-free close must still read as a DROP to the detector: %s", raw)
	}

	// AFTER — the same path, same writer, with a verdict handed over at submit.
	speaking := mgClaimedItem(t, root, "an author that records a verdict")
	side, raw = run(speaking, json.RawMessage(`{"verdict":"pass","summary":"the parser round-trips","unverified":[]}`))
	t.Logf("verdict-bearing sidecar (the post-fix shape): %s", raw)
	if !answered(side) {
		t.Errorf("the author's verdict did not survive to the sidecar: %s", raw)
	}
	var v map[string]any
	if err := json.Unmarshal(side["verdict"], &v); err != nil {
		t.Fatalf("verdict did not survive as an object: %v", err)
	}
	if v["verdict"] != "pass" || v["summary"] != "the parser round-trips" {
		t.Errorf("verdict altered in transit: %v", v)
	}
}
