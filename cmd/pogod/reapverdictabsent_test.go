package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/refinery"
)

// spine redirects events.Emit to a per-test log and returns its path.
func spine(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(p)
	t.Cleanup(func() { events.SetLogPathForTesting(testEventLogPath) })
	return p
}

func absentEvents(t *testing.T, path string) []events.Event {
	t.Helper()
	found, err := events.ReadFiltered(path, events.Filter{Type: EventClosedWithoutVerdict})
	if err != nil {
		t.Fatalf("reading the spine: %v", err)
	}
	return found
}

// mg-c456. The census behind this ticket found 385 ROUTING + 1014 LOST rows in
// the live store, growing at 10-80 rows a working day and going to exactly zero
// across the 118h fleet outage — so it is produced by normal operation, and it
// had no owner because NOTHING OBSERVED THE LOSS AS IT HAPPENED. mg-dfea gave the
// author a channel past this writer; it did not make the failure to use that
// channel visible.
//
// What this fix adds is an observation at the instant of the loss, in the event
// stream, and NOTHING on the item. The sidecar was the obvious place and it is
// the wrong one — see the note above sidecar construction in reap.go, and the
// negative arm below that pins the item's shape as unchanged.
func TestReapMergedPolecat_VerdictFreeCloseIsRecordedAsSuch(t *testing.T) {
	log := spine(t)
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var completedResult string
	complete := func(_, resultJSON string) error {
		completedResult = resultJSON
		return nil
	}

	mr := &refinery.MergeRequest{
		ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "main",
		MergedSHA: "45b4421a",
	}
	reapMergedPolecat(reg, mr, complete, postMergeVerdict{}, nil, nil)

	// THE SIDECAR IS DELIBERATELY UNCHANGED, and this is the arm that pins it.
	// mg-c456's first attempt marked the absence on the item; the d2_cause.py
	// predicate in TestReapMergedPolecatMeasuredByTheDetectorsOwnPredicate reads
	// any new key as "an outcome was written down", so that marker would have made
	// a verdict-free close read as ANSWERED — the remedy exhibiting the defect it
	// remedies. The observation moved to the event stream; the item's shape did
	// not move at all.
	var side map[string]json.RawMessage
	if err := json.Unmarshal([]byte(completedResult), &side); err != nil {
		t.Fatalf("result sidecar is not valid JSON: %v (%q)", err, completedResult)
	}
	if _, ok := side["verdict"]; ok {
		t.Errorf("a verdict-free close must not invent a verdict: %q", completedResult)
	}
	for k := range side {
		switch k {
		case "branch", "mr", "completed_by", "target", "merged_sha", "post_merge_tag":
		default:
			t.Errorf("recording the absence must not add a key the drop-detector reads as an outcome: %q is new (%q)", k, completedResult)
		}
	}

	// The event is the half that survives. pogod logs to inherited stderr, so a
	// log.Printf here would not reliably be a line anybody can go back and read —
	// which is the same failure the marker exists to close, one layer down.
	found := absentEvents(t, log)
	if len(found) != 1 {
		t.Fatalf("expected exactly one %s on the spine, got %d", EventClosedWithoutVerdict, len(found))
	}
	ev := found[0]
	if ev.WorkItemID != "mg-1234" {
		t.Errorf("the record must name the item: %+v", ev)
	}
	live, ok := ev.Details["worker_live_at_close"].(bool)
	if !ok || !live {
		t.Errorf("worker_live_at_close is required and was true here: %+v", ev.Details)
	}
	for _, k := range []string{"route", "branch", "mr", "target"} {
		if v, _ := ev.Details[k].(string); v == "" {
			t.Errorf("details[%q] is required and empty: %+v", k, ev.Details)
		}
	}
	if ev.Details["merged_sha"] != "45b4421a" {
		t.Errorf("the record must name what landed: %+v", ev.Details)
	}
}

// The negative arm. A close that DID carry the author's verdict must produce
// neither the marker nor the event — otherwise the signal is a constant, and a
// constant is exactly as useful as the silence it replaced.
func TestReapMergedPolecat_AVerdictCarriedMeansNoAbsenceRecord(t *testing.T) {
	log := spine(t)
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
	}}
	var completedResult string
	complete := func(_, resultJSON string) error {
		completedResult = resultJSON
		return nil
	}

	mr := &refinery.MergeRequest{
		ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "main",
		Verdict: json.RawMessage(`{"verdict":"pass","summary":"landed it"}`),
	}
	reapMergedPolecat(reg, mr, complete, postMergeVerdict{}, nil, nil)

	if !strings.Contains(completedResult, `"verdict"`) {
		t.Fatalf("the author's verdict did not reach the sidecar: %q", completedResult)
	}
	if n := len(absentEvents(t, log)); n != 0 {
		t.Errorf("an answered close emitted %d absence record(s)", n)
	}
}

// A hand-submitted or stranded branch is a DIFFERENT FINDING and the record says
// which. Nobody's window was shut here: there was no worker to be beaten to the
// item, and the submitter carried no verdict either. Collapsing the two is how a
// scope becomes wrong, which is the failure mode this ticket was written next to.
func TestReapMergedPolecat_AbsenceRecordSeparatesTheWorkerlessClose(t *testing.T) {
	log := spine(t)
	reg := &fakeReaper{}
	complete := func(string, string) error { return nil }

	mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "main"}
	reapMergedPolecat(reg, mr, complete, postMergeVerdict{}, nil, nil)

	found := absentEvents(t, log)
	if len(found) != 1 {
		t.Fatalf("a workerless close still closes the item with no verdict; got %d record(s)", len(found))
	}
	if live, _ := found[0].Details["worker_live_at_close"].(bool); live {
		t.Errorf("no polecat was registered — the record must not claim one: %+v", found[0].Details)
	}
	if _, present := found[0].Details["worker"]; present {
		t.Errorf("there is no worker to name: %+v", found[0].Details)
	}
}

// THE RACE THE POLECAT WON is not a loss and must not be recorded as one. When
// `mg done` is refused as already-done the store holds the WORKER's result, not
// the verdict-free sidecar this writer built — that sidecar is discarded, so
// recording an absence against it would be an assertion about a file that was
// never written.
//
// Same for a close that did not apply at all: the item is still open, nothing
// landed, and the merged-but-open window is already reported at volume.
func TestReapMergedPolecat_NoAbsenceRecordWhenThisCloseDidNotLand(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"polecat won the race", fmt.Errorf("mg done mg-1234: %w", client.ErrMGWorkItemAlreadyDone)},
		{"close was refused", fmt.Errorf("mg done failed: not claimed")},
		{"pogod declined a gated item", fmt.Errorf("%w: assignee parked", client.ErrMGWorkItemGated)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := spine(t)
			reg := &fakeReaper{agents: map[string]*agent.Agent{
				"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
			}}
			complete := func(string, string) error { return tc.err }

			mr := &refinery.MergeRequest{ID: "mr-42", Branch: "polecat-mg-1234", Author: "mg-1234", TargetRef: "main"}
			reapMergedPolecat(reg, mr, complete, postMergeVerdict{}, nil, nil)

			if n := len(absentEvents(t, log)); n != 0 {
				t.Errorf("this close never landed, so there is no sidecar to assert an absence about; got %d record(s)", n)
			}
		})
	}
}

// A DEFERRED merge is not the auto-done lane and must be silent here. The whole
// point of the deferral is that the author still owns `mg done --result`, so its
// window is not shut and there is nothing to record. A marker that fired on every
// deferral would report a loss on the one lane where the author has not yet had
// its turn.
func TestReapMergedPolecat_DeferredMergeRecordsNoAbsence(t *testing.T) {
	for _, tc := range []struct {
		name string
		mr   *refinery.MergeRequest
	}{
		{"defer-done", &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-1234", TargetRef: "main", DeferDone: true}},
		{"pr-flow", &refinery.MergeRequest{ID: "mr-2", Branch: "b", Author: "mg-1234", TargetRef: "feat-x", PRFlow: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := spine(t)
			reg := &fakeReaper{agents: map[string]*agent.Agent{
				"1234": {Name: "1234", WorkItemID: "mg-1234", Type: agent.TypePolecat},
			}}
			called := false
			complete := func(string, string) error { called = true; return nil }
			var escalations []string
			backstop, _ := newTestBackstop(reg, &escalations)

			reapMergedPolecat(reg, tc.mr, complete, postMergeVerdict{}, backstop, nil)

			if called {
				t.Error("a deferred merge must not auto-done")
			}
			if n := len(absentEvents(t, log)); n != 0 {
				t.Errorf("the author still owns its verdict on a deferred lane; got %d absence record(s)", n)
			}
		})
	}
}

// The catalog in docs/event-log.md is how a reader learns this event exists and
// what `worker_live_at_close` means. An event type nobody can look up is half an
// artifact — and this one's whole purpose is to be looked up by whoever takes the
// accrual question, so the field names are pinned too.
func TestClosedWithoutVerdictEventIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "event-log.md"))
	if err != nil {
		t.Skipf("event-log.md not readable from here: %v", err)
	}
	body := string(doc)
	if !strings.Contains(body, EventClosedWithoutVerdict) {
		t.Fatalf("%s is emitted but absent from docs/event-log.md", EventClosedWithoutVerdict)
	}
	for _, field := range []string{"worker_live_at_close", "merged_sha"} {
		if !strings.Contains(body, field) {
			t.Errorf("details field %q is emitted but not documented", field)
		}
	}
}
