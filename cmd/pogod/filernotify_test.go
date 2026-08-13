package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/filernotify"
	"github.com/drellem2/pogo/internal/refinery"
)

// newFakeReaperReg is a fakeReaper holding one live polecat, registered under
// its bare name and carrying the full work-item id — the real shape (gh #48).
func newFakeReaperReg(name, item string) *fakeReaper {
	return &fakeReaper{agents: map[string]*agent.Agent{
		name: {Name: name, WorkItemID: item, Type: agent.TypePolecat},
	}}
}

// capturingFiler records what the reap paths hand the completion notifier.
type capturingFiler struct {
	mu   sync.Mutex
	seen []filernotify.Completion
}

func (c *capturingFiler) Notify(comp filernotify.Completion) filernotify.Outcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, comp)
	return filernotify.Outcome{To: "pm-onethird"}
}

func (c *capturingFiler) all() []filernotify.Completion {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]filernotify.Completion, len(c.seen))
	copy(out, c.seen)
	return out
}

// The merge close is the path the observed instance took: mg-145f was
// dispatched, merged and archived, and its commissioner heard nothing.
func TestTheMergeCloseTellsTheFiler(t *testing.T) {
	reg := newFakeReaperReg("145f", "mg-145f")
	filer := &capturingFiler{}
	mr := &refinery.MergeRequest{ID: "mr-1", Branch: "polecat-p145f", Author: "mg-145f",
		TargetRef: "main", MergedSHA: "abc1234"}

	reapMergedPolecat(reg, mr, func(string, string) error { return nil }, postMergeVerdict{}, nil, filer)

	got := filer.all()
	if len(got) != 1 {
		t.Fatalf("expected exactly one completion handed to the notifier, got %d", len(got))
	}
	if got[0].ItemID != "mg-145f" || got[0].Route != filernotify.RouteMerge {
		t.Errorf("wrong completion: %+v", got[0])
	}
	if got[0].MergedSHA != "abc1234" || got[0].Branch != "polecat-p145f" {
		t.Errorf("completion lost what landed: %+v", got[0])
	}
	if got[0].Worker != "145f" {
		t.Errorf("completion must name the worker so a self-filed item can be skipped: %+v", got[0])
	}
	if !strings.Contains(got[0].Result, `"merged_sha":"abc1234"`) {
		t.Errorf("completion should carry the sidecar this writer produced as a fallback: %q", got[0].Result)
	}
}

// An "already done" means the WORKER won the race and its own result stands.
// The item is closed either way, so the filer is still told — and the sidecar
// offered as a fallback is dropped, because ours is not the one recorded.
func TestAnAlreadyDoneCloseStillTellsTheFilerAndDropsOurSidecar(t *testing.T) {
	reg := newFakeReaperReg("1234", "mg-1234")
	filer := &capturingFiler{}
	mr := &refinery.MergeRequest{ID: "mr-1", Branch: "b", Author: "mg-1234", MergedSHA: "beef"}

	reapMergedPolecat(reg, mr, func(string, string) error {
		return fmt.Errorf("%w: mg show mg-1234 reports status=done", client.ErrMGWorkItemAlreadyDone)
	}, postMergeVerdict{}, nil, filer)

	got := filer.all()
	if len(got) != 1 {
		t.Fatalf("a close that was refused as already-done is still a close; expected one notification, got %d", len(got))
	}
	if !got[0].Closed {
		t.Errorf("an already-done refusal means the item IS closed; the notice must say so: %+v", got[0])
	}
	if got[0].NotClosedReason != "" {
		t.Errorf("a closed item carries no not-closed reason, or the mail reads as a caveat on the completion: %q", got[0].NotClosedReason)
	}
	if got[0].Result != "" {
		t.Errorf("our sidecar was not written, so it must not be offered as the verdict: %q", got[0].Result)
	}
}

// A merge that is deliberately NOT completion (PR flow, --defer-done, a
// declared remainder) must not report the item as finished.
func TestAMergeThatIsNotCompletionTellsNobody(t *testing.T) {
	for _, tc := range []struct {
		name string
		mr   *refinery.MergeRequest
		pmv  postMergeVerdict
	}{
		{"pr flow", &refinery.MergeRequest{ID: "m", Branch: "b", Author: "mg-1234", PRFlow: true}, postMergeVerdict{}},
		{"defer done", &refinery.MergeRequest{ID: "m", Branch: "b", Author: "mg-1234", DeferDone: true}, postMergeVerdict{}},
		{"declared remainder", &refinery.MergeRequest{ID: "m", Branch: "b", Author: "mg-1234"},
			postMergeVerdict{Declared: true, Reason: "declares post-merge work"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newFakeReaperReg("1234", "mg-1234")
			filer := &capturingFiler{}
			reapMergedPolecat(reg, tc.mr, func(string, string) error { return nil }, tc.pmv, nil, filer)
			if n := len(filer.all()); n != 0 {
				t.Fatalf("the item is deliberately still open; expected no completion notice, got %d", n)
			}
		})
	}
}

// The non-merge close — a triage, audit or investigation item — is the half the
// refinery never hears about, and the done-reaper is the only vantage point
// that sees it.
func TestTheDoneReaperTellsTheFilerOfASelfClosedItem(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "pd764", WorkItemID: "mg-d764", HasOutput: true, IdleFor: 5 * time.Minute},
	}}
	filer := &capturingFiler{}
	r := newDoneReaper(reg, func(string) (bool, error) { return true, nil }, noReviews, time.Minute)
	r.SetFilerNotifier(filer)

	r.Check(time.Now())

	got := filer.all()
	if len(got) != 1 {
		t.Fatalf("expected the self-close to be reported, got %d notifications", len(got))
	}
	if got[0].ItemID != "mg-d764" || got[0].Route != filernotify.RouteSelfClose || got[0].Worker != "pd764" {
		t.Errorf("wrong completion: %+v", got[0])
	}
}

// The notice is about the CLOSE, not the reap. An exempt builder's item is just
// as closed as a reaped one's, and a stop that fails does not un-close it.
func TestTheCloseIsReportedEvenWhenThePolecatIsNotReaped(t *testing.T) {
	reg := &fakeDoneReg{
		live: []agent.PolecatActivity{
			{Name: "pbuild", WorkItemID: "mg-build", HasOutput: true, IdleFor: 5 * time.Minute},
			{Name: "prev", WorkItemID: "mg-rev", HasOutput: true, IdleFor: time.Second},
		},
		stopErr: map[string]error{},
	}
	filer := &capturingFiler{}
	r := newDoneReaper(reg,
		func(id string) (bool, error) { return id == "mg-build", nil },
		func(id string) (string, error) {
			if id == "mg-rev" {
				return "mg-build", nil
			}
			return "", nil
		}, time.Minute)
	r.SetFilerNotifier(filer)

	stopped := r.Check(time.Now())

	if len(stopped) != 0 {
		t.Fatalf("the review exemption should have held the builder, got stopped=%v", stopped)
	}
	got := filer.all()
	if len(got) != 1 || got[0].ItemID != "mg-build" {
		t.Fatalf("an exempt builder's item is still closed; expected it reported, got %+v", got)
	}
}

// An item still claimed is not a completion, no matter how idle its polecat.
func TestNoNoticeForAnItemThatIsStillOpen(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "pwork", WorkItemID: "mg-work", HasOutput: true, IdleFor: time.Hour},
	}}
	filer := &capturingFiler{}
	r := newDoneReaper(reg, func(string) (bool, error) { return false, nil }, noReviews, time.Minute)
	r.SetFilerNotifier(filer)

	r.Check(time.Now())

	if n := len(filer.all()); n != 0 {
		t.Fatalf("expected no completion notice for an open item, got %d", n)
	}
}

// Both reap paths must tolerate an unwired notifier: a missing seam is a wiring
// fault to be found in one place, never a nil dereference on the merge path.
func TestBothReapPathsTolerateAnUnwiredNotifier(t *testing.T) {
	reg := newFakeReaperReg("1234", "mg-1234")
	reapMergedPolecat(reg, &refinery.MergeRequest{ID: "m", Branch: "b", Author: "mg-1234"},
		func(string, string) error { return nil }, postMergeVerdict{}, nil, nil)

	dr := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "p1", WorkItemID: "mg-1", HasOutput: true, IdleFor: time.Hour},
	}}
	newDoneReaper(dr, func(string) (bool, error) { return true, nil }, noReviews, time.Minute).Check(time.Now())
}

// agentKnown is the polecat-that-no-longer-exists rule. A nil registry answers
// false for everything, which routes every notice to the coordinator — noisy
// and recoverable, where a dropped notice is the defect.
func TestAgentKnownWithNoRegistry(t *testing.T) {
	known := agentKnown(nil)
	if known("pm-onethird") || known("") {
		t.Errorf("a nil registry must not claim to know any name")
	}
}

func TestFallbackSidecarIsOnlyOfferedWhenTheWriteApplied(t *testing.T) {
	if got := fallbackSidecar(nil, `{"a":1}`); got != `{"a":1}` {
		t.Errorf("a successful write should offer its sidecar, got %q", got)
	}
	if got := fallbackSidecar(errors.New("already done"), `{"a":1}`); got != "" {
		t.Errorf("a refused write must not offer its sidecar as the recorded verdict, got %q", got)
	}
}
