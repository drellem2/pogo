package refinery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
)

// enth-vcs §6 as decided (mg-5b904): the worker lands its own enth work, the
// refinery records it and refuses to land enth work itself. Against a REAL
// sandboxed enthd.

type selfLandFixture struct {
	s    *enthdtest.Server
	repo string // the project path recorded as enth
}

func newSelfLandFixture(t *testing.T) *selfLandFixture {
	t.Helper()
	s := enthdtest.Start(t)
	fx := &selfLandFixture{s: s, repo: t.TempDir()}
	e := s.EnthRepo()
	SetRepoVCS(func(p string) (vcs.Kind, *vcs.EnthRepo) {
		if p == fx.repo {
			return vcs.Enth, &e
		}
		return vcs.Git, nil
	})
	old := enthWork
	enthWork = func(e vcs.EnthRepo) enthvcs.Backend { return enthvcs.Backend{Repo: e, Bin: s.Enth()} }
	t.Cleanup(func() { SetRepoVCS(nil); enthWork = old })
	return fx
}

// land is the worker's own `enth land`, as the template runs it: stdout only.
func (fx *selfLandFixture) land(t *testing.T, requestID string, ids ...string) []byte {
	t.Helper()
	out, err := fx.try(append(append([]string{"land"}, ids...), "--request-id", requestID)...)
	if err != nil {
		t.Fatalf("enth land %v: %v\n%s", ids, err, out)
	}
	return out
}

func (fx *selfLandFixture) try(args ...string) ([]byte, error) {
	c := fx.s.Command("enth", append(fx.s.Remote(), args...)...)
	return c.Output()
}

func (fx *selfLandFixture) request(t *testing.T, item string, landed []byte) SelfLandRequest {
	t.Helper()
	// No target: the trunk, as pogo land sends it by default.
	return SelfLandRequest{RepoPath: fx.repo, Item: item, Landed: landed, RequestID: item + "/1",
		Verdict: json.RawMessage(`{"verdict":"pass"}`)}
}

func TestRecordSelfLand(t *testing.T) {
	fx := newSelfLandFixture(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir(), StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	// mg-cef7: the land must be on the record before the callback that closes
	// the item and stops the worker runs.
	var merged []string
	var onDisk bool
	r.SetOnMerged(func(mr *MergeRequest) {
		merged = append(merged, mr.ID)
		data, _ := os.ReadFile(statePath)
		onDisk = strings.Contains(string(data), mr.ID) && strings.Contains(string(data), `"self_landed": true`)
	})

	c1, _ := fx.s.Work(t, "mg-1", "p1", "one", map[string]string{"x.txt": "x\n"})
	out := fx.land(t, "mg-1/1", c1)
	res, err := r.RecordSelfLand(fx.request(t, "mg-1", out))
	if err != nil {
		t.Fatal(err)
	}
	mr := res.MR
	tip := fx.s.Tip(t)
	if res.AlreadyRecorded || mr.Status != StatusMerged || !mr.SelfLanded || mr.Enth == nil || mr.Enth.New != tip ||
		mr.Enth.RequestID != "mg-1/1" || len(mr.EnthSubmitted) != 1 || mr.EnthSubmitted[0] != c1 || mr.EnthWorker != "p1" ||
		mr.Author != "mg-1" || mr.MergedSHA != "" || string(mr.Verdict) != `{"verdict":"pass"}` {
		t.Fatalf("recorded MR = %+v (enth %+v); want merged, self-landed, new %s", mr, mr.Enth, tip)
	}
	if mr.TargetRef != "main" {
		t.Errorf("TargetRef = %q; want main", mr.TargetRef)
	}
	if len(merged) != 1 || merged[0] != mr.ID {
		t.Fatalf("OnMerged = %v; want once for %s", merged, mr.ID)
	}
	if !onDisk {
		t.Error("OnMerged ran before the self-land was written through to the state file (mg-cef7)")
	}
	if got := r.Get(mr.ID); got == nil || got.Status != StatusMerged {
		t.Errorf("refinery show %s = %+v; want the merged MR", mr.ID, got)
	}

	// The same land again — a worker that lost pogo land's answer re-runs it
	// — and the replayed `enth land` output: the first MR, nothing fired.
	again, err := r.RecordSelfLand(fx.request(t, "mg-1", fx.land(t, "mg-1/1", c1)))
	if err != nil || !again.AlreadyRecorded || again.MR.ID != mr.ID {
		t.Fatalf("second record = %+v %v; want the first MR, already recorded", again, err)
	}
	if len(merged) != 1 {
		t.Errorf("OnMerged fired %d times; a land is recorded once", len(merged))
	}

	// A restarted refinery still has it.
	r2, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir(), StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.Get(mr.ID); got == nil || got.Enth == nil || got.Enth.New != tip || !got.SelfLanded {
		t.Errorf("after a restart MR %s = %+v; want the self-land record", mr.ID, got)
	}
}

// Nothing is recorded, and nothing closes an item, on a land enthd does not
// confirm.
func TestRecordSelfLandRefuses(t *testing.T) {
	fx := newSelfLandFixture(t)
	r, cl := newCallbackRefinery(t)

	mine, _ := fx.s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
	theirs, _ := fx.s.Work(t, "mg-2", "p2", "theirs", map[string]string{"y.txt": "y\n"})
	theirsOut := fx.land(t, "mg-2/1", theirs)
	unlanded, _ := fx.s.Work(t, "mg-3", "p3", "never landed", map[string]string{"z.txt": "z\n"})
	mineOut := fx.land(t, "mg-1/1", mine)
	rec, err := enthvcs.ParseLanded(mineOut)
	if err != nil {
		t.Fatal(err)
	}
	line := func(r vcs.LandRecord) []byte {
		b, _ := json.Marshal(map[string]any{"type": "landed", "ref": r.Ref, "entry": r.Entry, "old": r.Old, "new": r.New, "changes": r.Changes})
		return b
	}
	tampered := rec
	tampered.Changes = []vcs.LandedChange{{Submitted: mine, Landed: theirs}}
	tampered.New = theirs
	fabricated := rec
	fabricated.Changes = []vcs.LandedChange{{Submitted: unlanded, Landed: rec.New}}
	dry, _ := fx.try("land", "--dry-run", unlanded)

	cases := map[string]struct {
		req  SelfLandRequest
		want string
	}{
		"another item's land":      {fx.request(t, "mg-1", theirsOut), "not one of mg-1's"},
		"a landed id enthd denies": {fx.request(t, "mg-1", line(tampered)), "enthd says"},
		"an unlanded Change":       {fx.request(t, "mg-3", line(fabricated)), "has not landed"},
		"a dry run":                {fx.request(t, "mg-3", dry), "not a land"},
		"another ref":              {SelfLandRequest{RepoPath: fx.repo, Item: "mg-1", TargetRef: "release", Landed: mineOut}, "not refs/heads/release"},
		"no item":                  {SelfLandRequest{RepoPath: fx.repo, Landed: mineOut}, "item is required"},
		"a git repo":               {SelfLandRequest{RepoPath: t.TempDir(), Item: "mg-1", Landed: mineOut}, "not recorded as an enth repo"},
		"a bad verdict":            {SelfLandRequest{RepoPath: fx.repo, Item: "mg-1", Landed: mineOut, Verdict: json.RawMessage(`[]`)}, "verdict"},
	}
	for name, c := range cases {
		if _, err := r.RecordSelfLand(c.req); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v; want a refusal containing %q", name, err, c.want)
		}
	}
	if len(cl.merged) != 0 || len(r.History()) != 0 {
		t.Errorf("a refused record fired OnMerged %d times, history %d; want nothing", len(cl.merged), len(r.History()))
	}
}

// The refinery refuses enth work with the pointer, at submit and — for an MR
// queued before it stopped taking them — at merge, before any git step.
func TestRefineryRefusesEnthMRs(t *testing.T) {
	fx := newSelfLandFixture(t)
	r, cl := newCallbackRefinery(t)
	fx.s.Work(t, "mg-1", "p1", "one", map[string]string{"x.txt": "x\n"})
	if _, err := r.Submit(MergeRequest{RepoPath: fx.repo, Item: "mg-1", TargetRef: "main", Author: "mg-1"}); err == nil ||
		!strings.Contains(err.Error(), "pogo land --item=mg-1") {
		t.Fatalf("enth submit = %v; want a refusal naming pogo land", err)
	}
	if len(r.Queue()) != 0 {
		t.Fatal("a refused enth submit was queued")
	}

	mr := &MergeRequest{ID: "mr-enth-old", RepoPath: fx.repo, Item: "mg-1", TargetRef: "main", Author: "mg-1",
		EnthSubmitted: []string{strings.Repeat("ab", 32)}, Status: StatusQueued, SubmitTime: time.Now()}
	r.mu.Lock()
	r.queue = append(r.queue, mr)
	r.byID[mr.ID] = mr
	r.mu.Unlock()
	tip := fx.s.Tip(t)
	r.processNext()
	got := r.Get(mr.ID)
	if got == nil || got.Status != StatusFailed || got.FailureClass != ClassDefect || !strings.Contains(got.Error, "pogo land --item=mg-1") {
		t.Fatalf("queued enth MR = %+v; want failed class=defect naming pogo land", got)
	}
	if len(cl.failed) != 1 || len(cl.merged) != 0 {
		t.Errorf("callbacks failed=%v merged=%v; want one failure", cl.failed, cl.merged)
	}
	if fx.s.Tip(t) != tip {
		t.Error("the refused enth MR moved the trunk")
	}
}
