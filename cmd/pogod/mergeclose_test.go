package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/filernotify"
	"github.com/drellem2/pogo/internal/refinery"
)

// THE TEST THE TICKET ASKED FOR (mg-2b71): no COMPLETED notification for an
// item that is not done after the merge handler runs.
//
// The defect it pins: reapMergedPolecat captured `mg done`'s failure, logged it
// verbatim with its exit status, and then handed the filer a Completion anyway.
// Nothing tied the notification to the outcome it reported, so the reproduced
// case — mg-479c, `status=available, assignee=parked` — produced a mail reading
// "COMPLETED: mg-479c / Closed: its branch merged". Both assertions false, 45
// seconds after pogod's own log recorded why they could not be true.
func TestAFailedCloseIsNotReportedAsACompletion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		closeErr   error
		wantReason string
	}{
		{
			// The exact live failure: `mg done` on an unclaimed item, exit 4.
			name:       "not claimed",
			closeErr:   errors.New("mg done failed: Error: mg-479c: not claimed, so it cannot be completed. (exit status 4)"),
			wantReason: "not claimed",
		},
		{
			// The close pogod DECLINED: a gated item nobody holds. Not a
			// failure, and still not a completion.
			name:       "gated",
			closeErr:   fmt.Errorf("%w: mg-479c is unclaimed and assigned to \"parked\"", client.ErrMGWorkItemGated),
			wantReason: "declined",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &fakeReaper{}
			filer := &capturingFiler{}
			mr := &refinery.MergeRequest{ID: "mr-1", Branch: "polecat-c479c", Author: "mg-479c", MergedSHA: "1a0240a"}

			reapMergedPolecat(reg, mr, func(string, string) error { return tc.closeErr }, postMergeVerdict{}, nil, filer)

			got := filer.all()
			if len(got) != 1 {
				t.Fatalf("the filer is still owed a report — a merged branch against an open item is exactly "+
					"the state nobody else watches; got %d notifications", len(got))
			}
			if got[0].Closed {
				t.Fatalf("the item never closed; reporting it as a completion is the defect (mg-2b71): %+v", got[0])
			}
			if !strings.Contains(got[0].NotClosedReason, tc.wantReason) {
				t.Errorf("the notice must say WHY it is still open, not merely that it is; reason = %q, want it to mention %q",
					got[0].NotClosedReason, tc.wantReason)
			}
			if got[0].Result != "" {
				t.Errorf("no sidecar was written, so none may be offered: %q", got[0].Result)
			}
		})
	}
}

// The other half of the same rule: a close that APPLIED is reported as one.
// Without this the fix could satisfy the test above by never reporting anything.
func TestACloseThatAppliedIsReportedAsACompletion(t *testing.T) {
	reg := &fakeReaper{}
	filer := &capturingFiler{}
	mr := &refinery.MergeRequest{ID: "mr-1", Branch: "polecat-c479c", Author: "mg-479c", MergedSHA: "1a0240a"}

	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, postMergeVerdict{}, nil, filer)

	got := filer.all()
	if len(got) != 1 {
		t.Fatalf("expected one notification, got %d", len(got))
	}
	if !got[0].Closed {
		t.Fatalf("the close applied; the notice must say so: %+v", got[0])
	}
}

// itemIsClosed is the single reading every conditional report turns on, so its
// direction is pinned directly: only positive evidence of closure counts.
func TestItemIsClosedRequiresPositiveEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"the close applied", nil, true},
		{"already done — somebody else closed it", fmt.Errorf("%w: status=done", client.ErrMGWorkItemAlreadyDone), true},
		{"refused", errors.New("mg done failed: not claimed (exit status 4)"), false},
		{"declined as gated", fmt.Errorf("%w: assignee parked", client.ErrMGWorkItemGated), false},
		{"store unreadable", errors.New("mg show failed: no such file"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := itemIsClosed(tc.err); got != tc.want {
				t.Errorf("itemIsClosed(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

// A gated item's report reads as a DECISION, not a fault. The distinction is
// the whole of fix direction 4: pogod could have closed mg-479c and declined,
// and a reader who cannot tell that from a broken close will go and "fix" it.
func TestTheGatedReasonReadsAsADecision(t *testing.T) {
	reason := notClosedReason(fmt.Errorf("%w: mg-479c is unclaimed and assigned to \"parked\"", client.ErrMGWorkItemGated), false)
	if !strings.Contains(reason, "declined") {
		t.Errorf("a deliberate refusal must not read as a failed attempt: %q", reason)
	}
	failed := notClosedReason(errors.New("mg done failed: not claimed"), false)
	if strings.Contains(failed, "declined") {
		t.Errorf("a failed close must not read as a decision: %q", failed)
	}
	if notClosedReason(nil, true) != "" {
		t.Error("a closed item carries no reason")
	}
}

// The durable record carries the same distinction as the mail (mg-2b71). The
// event stream is where this instance was verified from — pogod logs to
// inherited stderr, so a log line is not reliably something anybody can go back
// and read — and an event named `work_item_completion_notice` that cannot say
// whether the item closed rebuilds the defect one layer down.
func TestTheCompletionNoticeEventSaysWhetherTheItemClosed(t *testing.T) {
	spine := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(spine)
	t.Cleanup(func() { events.SetLogPathForTesting("") })

	recordFilerNotice(
		filernotify.Completion{ItemID: "mg-479c", Route: filernotify.RouteMerge, Branch: "polecat-c479c",
			NotClosedReason: "pogod declined to close it: the item is gated (parked)"},
		filernotify.Outcome{To: "pm-onethird", Creator: "pm-onethird"})
	recordFilerNotice(
		filernotify.Completion{ItemID: "mg-145f", Route: filernotify.RouteMerge, Closed: true},
		filernotify.Outcome{To: "pm-onethird", Creator: "pm-onethird"})

	found, err := events.ReadFiltered(spine, events.Filter{Type: "work_item_completion_notice"})
	if err != nil {
		t.Fatalf("reading the spine: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("expected both notices on the spine, got %d", len(found))
	}
	for _, ev := range found {
		closed, ok := ev.Details["closed"].(bool)
		if !ok {
			t.Fatalf("%s: the record must state whether the item closed: %+v", ev.WorkItemID, ev.Details)
		}
		switch ev.WorkItemID {
		case "mg-479c":
			if closed {
				t.Errorf("mg-479c never closed: %+v", ev.Details)
			}
			if r, _ := ev.Details["not_closed_reason"].(string); !strings.Contains(r, "gated") {
				t.Errorf("an open item's record must carry the reason: %+v", ev.Details)
			}
		case "mg-145f":
			if !closed {
				t.Errorf("mg-145f closed: %+v", ev.Details)
			}
			if _, present := ev.Details["not_closed_reason"]; present {
				t.Errorf("a closed item's record must not carry a not-closed reason: %+v", ev.Details)
			}
		}
	}
}
