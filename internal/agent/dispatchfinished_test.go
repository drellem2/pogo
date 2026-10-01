package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// Finished workers and the per-repo cap (drellem2/pogo#128). A live worker
// whose work item already reads terminal has nothing left to build: it is
// waiting to be reaped, and it is not running the repo's test suite. Measured in
// gh#128: two `done` workers (bdd6, 9d97) were never reaped and held their slots
// until stopped by hand.

// statusesOf is an ItemStatusReader over a fixed id -> status map. Anything not
// in the map is an error, as an unreadable store is.
func statusesOf(m map[string]string) ItemStatusReader {
	return ItemStatusReaderFunc(func(id string) (string, error) {
		if s, ok := m[id]; ok {
			return s, nil
		}
		return "", errors.New("store unreadable")
	})
}

// TestFinishedWorkerIsNotCounted: three live workers fill the cap; one of them
// is done. It must not count, it must be named, and a dispatch must be admitted.
// The control — the same fixture with every item claimed — is refused, so the
// admission below is the status reader's doing.
func TestFinishedWorkerIsNotCounted(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo) // a-cat, b-cat, c-cat
	reg.SetItemStatusReader(statusesOf(map[string]string{
		"mg-a-cat": "claimed", "mg-b-cat": "claimed", "mg-c-cat": "claimed",
	}))
	if occ := reg.RepoOccupancyFor(goRepo); !occ.WouldRefuse || occ.Count != 3 {
		t.Fatalf("control: three claimed workers against cap %d should refuse, got count %d refuse %v",
			occ.Cap, occ.Count, occ.WouldRefuse)
	}

	for _, terminal := range []string{"done", "archived"} {
		reg.SetItemStatusReader(statusesOf(map[string]string{
			"mg-a-cat": terminal, "mg-b-cat": "claimed", "mg-c-cat": "claimed",
		}))
		occ := reg.RepoOccupancyFor(goRepo)
		if occ.Count != 2 {
			t.Errorf("%s: count = %d (%v), want 2 — a finished worker is using none of the repo's suite",
				terminal, occ.Count, occ.Polecats)
		}
		if len(occ.Finished) != 1 || occ.Finished[0] != "a-cat" {
			t.Errorf("%s: Finished = %v, want [a-cat] — an uncounted worker must be named, not vanish", terminal, occ.Finished)
		}
		for _, p := range occ.Polecats {
			if p == "a-cat" {
				t.Errorf("%s: a-cat is in both Polecats and Finished", terminal)
			}
		}
		if occ.WouldRefuse {
			t.Errorf("%s: still refusing with a finished worker holding the third slot: %+v", terminal, occ)
		}
	}
	if rr := spawnIntoRepo(t, reg, "cat-next", goRepo); rr.Code == http.StatusServiceUnavailable {
		t.Errorf("spawn refused although one of three workers is finished: %s", rr.Body.String())
	}
}

// TestUnreadableStatusIsCounted: the fail-open direction. A worker whose status
// could not be read might be building, so it counts exactly as it did before
// #128 — including the one whose item does not exist in the store at all.
func TestUnreadableStatusIsCounted(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo)
	// mg-a-cat is absent from the map: the reader errors on it.
	reg.SetItemStatusReader(statusesOf(map[string]string{"mg-b-cat": "claimed", "mg-c-cat": "claimed"}))
	occ := reg.RepoOccupancyFor(goRepo)
	if occ.Count != 3 || len(occ.Finished) != 0 {
		t.Fatalf("an unreadable status must count the worker: count %d, finished %v", occ.Count, occ.Finished)
	}
	if !occ.WouldRefuse {
		t.Fatalf("three counted workers against cap %d must refuse", occ.Cap)
	}
}

// TestRefusalNamesFinishedWorkers: when the repo is still full after the
// finished workers are left out, the refusal says who was left out, so the
// reader does not have to reconcile `pogo agent list` against the count.
func TestRefusalNamesFinishedWorkers(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo+1) // a..d
	reg.SetItemStatusReader(statusesOf(map[string]string{
		"mg-a-cat": "claimed", "mg-b-cat": "claimed", "mg-c-cat": "claimed", "mg-d-cat": "done",
	}))
	rr := spawnIntoRepo(t, reg, "cat-fifth", goRepo)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 with three claimed workers: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"already has 3 worker(s)", "d-cat", "already done or archived", "drellem2/pogo#128"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal missing %q: %s", want, body)
		}
	}
}

// TestFinishedWorkerDoesNotSpendTheMergeQueueCredit: mg-976f's credit is
// bounded, and a worker that is finished AND still in the queue already does not
// count. Letting it spend a credit would take one away from a waiting worker who
// does need it.
func TestFinishedWorkerDoesNotSpendTheMergeQueueCredit(t *testing.T) {
	reg := capRegistry(t, 4) // a..d
	queueHolding(reg,
		QueuedMerge{Author: "mg-a-cat"},
		QueuedMerge{Author: "mg-b-cat"},
		QueuedMerge{Author: "mg-c-cat"},
	)
	reg.SetItemStatusReader(statusesOf(map[string]string{
		"mg-a-cat": "done", "mg-b-cat": "claimed", "mg-c-cat": "claimed", "mg-d-cat": "claimed",
	}))
	occ := reg.RepoOccupancyFor(goRepo)
	if len(occ.Finished) != 1 || occ.Finished[0] != "a-cat" {
		t.Fatalf("Finished = %v, want [a-cat]", occ.Finished)
	}
	if strings.Join(occ.MergeQueued, ",") != "b-cat,c-cat" {
		t.Errorf("MergeQueued = %v, want [b-cat c-cat] — the finished a-cat spent a credit", occ.MergeQueued)
	}
	if occ.Count != 1 || occ.Polecats[0] != "d-cat" {
		t.Errorf("count = %d (%v), want 1 (d-cat)", occ.Count, occ.Polecats)
	}
	if occ.Count+len(occ.MergeQueued)+len(occ.Finished) != 4 {
		t.Errorf("live workers lost: count %d + queued %v + finished %v != 4", occ.Count, occ.MergeQueued, occ.Finished)
	}
}

// TestFinishedWorkerKeepsItsReviewSlotHoldAccounting: mg-bf42's holds are
// computed over every LIVE worker, and #128 does not change that — a reviewer
// covering a builder stays a cover whether or not its own item is closed.
func TestFinishedWorkerKeepsItsReviewSlotHoldAccounting(t *testing.T) {
	reg := capRegistry(t, 2) // a-cat builder, b-cat reviewer
	reg.SetFlowReader(FlowReaderFunc(func(id string) (FlowCarrier, bool) {
		switch id {
		case "mg-a-cat":
			return FlowCarrier{Workflow: GHIssueWorkflow, Stage: "review"}, true
		case "mg-b-cat":
			return FlowCarrier{Workflow: GHIssueWorkflow, Stage: "review", Reviews: "mg-a-cat"}, true
		}
		return FlowCarrier{}, false
	}))
	reg.SetItemStatusReader(statusesOf(map[string]string{"mg-a-cat": "claimed", "mg-b-cat": "done"}))
	occ := reg.RepoOccupancyFor(goRepo)
	if len(occ.ReviewSlotHolds) != 0 {
		t.Errorf("a finished reviewer still covers its builder; holds = %v", occ.ReviewSlotHolds)
	}
	if occ.Count != 1 || len(occ.Finished) != 1 {
		t.Errorf("count %d finished %v, want 1 and [b-cat]", occ.Count, occ.Finished)
	}
}

// TestMGItemStatusReaderReadsTheStoreLayout: the production reader against a
// real directory layout — claimed (with its .<pid> suffix), done, archived, and
// absent. Absent is an error so the cap counts it.
func TestMGItemStatusReaderReadsTheStoreLayout(t *testing.T) {
	root := t.TempDir()
	write := func(rel, id string) {
		p := filepath.Join(root, "work", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nid: " + id + "\ntype: task\n---\n\n# t\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("claimed/mg-c1.md.12345", "mg-c1")
	write("done/mg-d1.md", "mg-d1")
	write("archive/2026-09/mg-z1.md", "mg-z1")
	// A live id that also names an archived item reads as its live status.
	write("claimed/mg-both.md.1", "mg-both")
	write("archive/2026-04/mg-both.md", "mg-both")
	// pending/ and shelved/ are filed items outside FindFrom's default search;
	// they read as their own status, not as absent (mg-36096) — and a parked id
	// that also names an archived item reads as parked, not archived.
	write("pending/mg-p1.md", "mg-p1")
	write("shelved/mg-s1.md", "mg-s1")
	write("pending/mg-pz.md", "mg-pz")
	write("archive/2026-04/mg-pz.md", "mg-pz")

	r := MGItemStatusReader{Root: root}
	for id, want := range map[string]string{
		"mg-c1": "claimed", "mg-d1": "done", "mg-z1": "archived", "mg-both": "claimed",
		"mg-p1": "pending", "mg-s1": "shelved", "mg-pz": "pending",
	} {
		got, err := r.ReadItemStatus(id)
		if err != nil || got != want {
			t.Errorf("ReadItemStatus(%s) = %q, %v; want %q", id, got, err, want)
		}
	}
	for _, id := range []string{"mg-nope", "", "../x", "mg-*", ".."} {
		if got, err := r.ReadItemStatus(id); err == nil {
			t.Errorf("ReadItemStatus(%q) = %q with no error — an absent item must not read as a status", id, got)
		}
	}
}

// TestItemTerminalFunc: the done-reaper's probe built from the store reader
// (mg-36096) answers done/archived as terminal, every other status — pending
// and shelved included — as open, and passes a read error through so the
// reaper leaves the polecat running rather than asserting a completion.
func TestItemTerminalFunc(t *testing.T) {
	probe := ItemTerminalFunc(ItemStatusReaderFunc(func(id string) (string, error) {
		if id == "mg-err" {
			return "", os.ErrNotExist
		}
		return strings.TrimPrefix(id, "mg-"), nil
	}))
	for id, want := range map[string]bool{
		"mg-done": true, "mg-archived": true,
		"mg-available": false, "mg-claimed": false, "mg-pending": false, "mg-shelved": false,
	} {
		got, err := probe(id)
		if err != nil || got != want {
			t.Errorf("probe(%s) = %v, %v; want %v", id, got, err, want)
		}
	}
	if _, err := probe("mg-err"); err == nil {
		t.Error("probe(mg-err) returned no error — an unreadable item must not read as open or done")
	}
}

// TestAgentsEndpointCarriesRepoAndItemStatus: GET /agents carries the repo the
// cap attributes a worker to and its item's status from the same reader the
// cap uses, so `pogo agent list` shows what the cap decided on. An unreadable
// status is omitted, never guessed.
func TestAgentsEndpointCarriesRepoAndItemStatus(t *testing.T) {
	reg := capRegistry(t, 2) // a-cat, b-cat in goRepo
	reg.SetItemStatusReader(statusesOf(map[string]string{"mg-a-cat": "done"}))
	rr := httptest.NewRecorder()
	reg.handleAgents(rr, httptest.NewRequest("GET", "/agents", nil))
	var infos []AgentInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &infos); err != nil {
		t.Fatalf("decode /agents: %v: %s", err, rr.Body.String())
	}
	got := map[string]AgentInfo{}
	for _, i := range infos {
		got[i.Name] = i
	}
	if a := got["a-cat"]; a.SourceRepo != goRepo || a.WorkItemStatus != "done" {
		t.Errorf("a-cat: source_repo=%q status=%q, want %q and done", a.SourceRepo, a.WorkItemStatus, goRepo)
	}
	if b := got["b-cat"]; b.SourceRepo != goRepo || b.WorkItemStatus != "" {
		t.Errorf("b-cat: source_repo=%q status=%q, want %q and an omitted (unreadable) status", b.SourceRepo, b.WorkItemStatus, goRepo)
	}
}
