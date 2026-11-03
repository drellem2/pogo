package refinery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// `pogo refinery submit --item` (enth-vcs slice 6, mg-30da0).

func withItemResolver(t *testing.T, f ItemResolver) {
	t.Helper()
	SetItemResolver(f)
	t.Cleanup(func() { SetItemResolver(nil) })
}

func TestSubmitGitItemResolvesToTheWorkersBranch(t *testing.T) {
	fx := newEmptyBranchFixture(t)
	r, _ := newCallbackRefinery(t)
	fx.branchWithWork(t, "polecat-p1", "a.txt")
	// fx.work is now on polecat-p1: it is p1's worktree.
	withItemResolver(t, func(item string) (ItemWorker, bool) {
		if item == "mg-1" {
			return ItemWorker{Name: "p1", Dir: fx.work}, true
		}
		return ItemWorker{}, false
	})

	// --item alone: the branch comes from the spawn record.
	id, err := r.Submit(MergeRequest{RepoPath: fx.work, Item: "mg-1", TargetRef: "main"})
	if err != nil {
		t.Fatalf("Submit --item: %v", err)
	}
	if mr := r.byID[id]; mr.Branch != "polecat-p1" || mr.Item != "mg-1" || len(mr.EnthSubmitted) != 0 {
		t.Errorf("MR = branch %q item %q enth %v; want polecat-p1, mg-1, no enth ids", mr.Branch, mr.Item, mr.EnthSubmitted)
	}
	// --item with the same branch: accepted.
	if _, err := r.Submit(MergeRequest{RepoPath: fx.work, Branch: "polecat-p1", Item: "mg-1", TargetRef: "main"}); err != nil {
		t.Errorf("Submit branch + matching --item: %v", err)
	}
	// --item with another worker's branch: refused.
	fx.branchWithWork(t, "polecat-p2", "b.txt")
	run(t, fx.work, "git", "checkout", "-q", "polecat-p1")
	if _, err := r.Submit(MergeRequest{RepoPath: fx.work, Branch: "polecat-p2", Item: "mg-1", TargetRef: "main"}); err == nil ||
		!strings.Contains(err.Error(), "on branch polecat-p1, not polecat-p2") {
		t.Errorf("mismatched branch: %v", err)
	}
	// --item nobody is working, no branch: refused, naming the remedy.
	if _, err := r.Submit(MergeRequest{RepoPath: fx.work, Item: "mg-9", TargetRef: "main"}); err == nil ||
		!strings.Contains(err.Error(), "pass the branch") {
		t.Errorf("unresolvable --item: %v", err)
	}
	// An unresolvable --item beside a branch is just recorded.
	if id, err := r.Submit(MergeRequest{RepoPath: fx.work, Branch: "polecat-p2", Item: "mg-9", TargetRef: "main"}); err != nil || r.byID[id].Item != "mg-9" {
		t.Errorf("branch + unknown --item: %v", err)
	}

	// THE GIT CONTROL: no --item is today's submit, unchanged.
	id, err = r.Submit(MergeRequest{RepoPath: fx.work, Branch: "polecat-p2", TargetRef: "main"})
	if err != nil || r.byID[id].Item != "" || r.byID[id].Branch != "polecat-p2" {
		t.Errorf("plain git submit: %v %+v", err, r.byID[id])
	}
	if _, err := r.Submit(MergeRequest{RepoPath: fx.work, TargetRef: "main"}); err == nil || err.Error() != "branch is required" {
		t.Errorf("no branch, no item = %v, want today's \"branch is required\"", err)
	}
	if _, err := r.Submit(MergeRequest{RepoPath: fx.work, Branch: "no-such-branch", TargetRef: "main"}); err == nil {
		t.Error("an unpushed branch was accepted: validateSubmitBranch must still run on git")
	}
}

// fakeEnth writes an enth stand-in that answers `query show REF` with a tip
// and `changes ...` with the contents of changesFile, and records its argv.
func fakeEnth(t *testing.T, changes string) (bin, argv string) {
	t.Helper()
	dir := t.TempDir()
	cf := filepath.Join(dir, "changes.jsonl")
	if err := os.WriteFile(cf, []byte(changes), 0o644); err != nil {
		t.Fatal(err)
	}
	argv = filepath.Join(dir, "argv")
	script := `#!/bin/sh
echo "$*" >> ` + argv + `
case "$*" in
  *"query show refs/heads/main"*) printf '%s\n' '{"type":"change","change":"T","parents":[],"subject":"t","message":"t"}' ;;
  *"query show"*) echo '{"type":"error","code":"not_found","message":"no such ref"}' >&2; exit 3 ;;
  *changes*) cat ` + cf + ` ;;
esac
`
	bin = filepath.Join(dir, "enth")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argv
}

func change(id, base, item, worker string) string {
	tr := `[{"key":"Pogo-Item","value":"` + item + `"}`
	if worker != "" {
		tr += `,{"key":"Pogo-Worker","value":"` + worker + `"}`
	}
	return `{"type":"change","id":"` + id + `","entry":1,"author":{"name":"w","email":"w@x"},"created":1700000000,"base":"` + base + `","trailers":` + tr + `],"landed":null}` + "\n"
}

func withEnthRepo(t *testing.T, repo string, bin string) {
	t.Helper()
	e := vcs.EnthRepo{Remote: "http://127.0.0.1:1", Repo: "o/r", Trunk: vcs.DefaultEnthTrunk}
	SetRepoVCS(func(p string) (vcs.Kind, *vcs.EnthRepo) {
		if p == repo {
			return vcs.Enth, &e
		}
		return vcs.Git, nil
	})
	old := enthWork
	enthWork = func(e vcs.EnthRepo) enthvcs.Backend { return enthvcs.Backend{Repo: e, Bin: bin} }
	t.Cleanup(func() { SetRepoVCS(nil); enthWork = old })
}

func TestSubmitEnthItem(t *testing.T) {
	r, _ := newCallbackRefinery(t)
	repo := t.TempDir() // an enth project's path: no .git, so any git call fails

	cases := []struct {
		name     string
		changes  string
		req      MergeRequest
		resolve  bool
		wantErr  string
		wantIDs  string
		wantWkr  string
		wantArgv string
	}{
		{name: "one stack", changes: change("c1", "T", "mg-1", "p1") + change("c2", "c1", "mg-1", "p1"),
			req: MergeRequest{Item: "mg-1"}, wantIDs: "c1,c2", wantWkr: "p1"},
		{name: "the live polecat filters by worker", changes: change("c1", "T", "mg-1", "p1"),
			req: MergeRequest{Item: "mg-1"}, resolve: true, wantIDs: "c1", wantArgv: "--trailer Pogo-Worker=p1"},
		{name: "none", changes: "", req: MergeRequest{Item: "mg-1"},
			wantErr: "class=defect: mg-1 carries nothing ahead of main"},
		{name: "ambiguous", changes: change("c1", "T", "mg-1", "p1") + change("x1", "T", "mg-1", "p2"),
			req: MergeRequest{Item: "mg-1"}, wantErr: "form 2 stacks, not one"},
		{name: "a branch is refused", req: MergeRequest{Item: "mg-1", Branch: "polecat-p1"},
			wantErr: "named by its work item, not a branch"},
		{name: "no item", req: MergeRequest{}, wantErr: "branch is required"},
		{name: "no item but a branch", req: MergeRequest{Branch: "b"}, wantErr: "not a branch"},
		{name: "a missing target", changes: change("c1", "T", "mg-1", "p1"), req: MergeRequest{Item: "mg-1", TargetRef: "nope"},
			wantErr: `target ref "nope" not found`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, argv := fakeEnth(t, c.changes)
			withEnthRepo(t, repo, bin)
			if c.resolve {
				withItemResolver(t, func(string) (ItemWorker, bool) { return ItemWorker{Name: "p1"}, true })
			}
			req := c.req
			req.RepoPath = repo
			id, err := r.Submit(req)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("Submit = %q, %v; want an error containing %q", id, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			mr := r.byID[id]
			if strings.Join(mr.EnthSubmitted, ",") != c.wantIDs || mr.Item != "mg-1" || mr.Branch != "" || mr.TargetRef != "main" {
				t.Errorf("MR = %+v; want enth ids %s", mr, c.wantIDs)
			}
			if c.wantWkr != "" && mr.EnthWorker != c.wantWkr {
				t.Errorf("worker = %q, want %q", mr.EnthWorker, c.wantWkr)
			}
			if c.wantArgv != "" {
				data, _ := os.ReadFile(argv)
				if !strings.Contains(string(data), c.wantArgv) {
					t.Errorf("enth was called as %q, want %q in it", data, c.wantArgv)
				}
			}
		})
	}
}

func TestProcessMergeRefusesEnthBeforeAnyGitStep(t *testing.T) {
	r, _ := newCallbackRefinery(t)
	mr := &MergeRequest{ID: "mr-x", RepoPath: t.TempDir(), Item: "mg-1", TargetRef: "main", EnthSubmitted: []string{"c1"}}
	_, err := r.processMerge(mr)
	if !errors.Is(err, vcs.ErrUnsupported) || !strings.Contains(err.Error(), "slice 7") {
		t.Fatalf("processMerge = %v, want ErrUnsupported naming slice 7", err)
	}
	if mr.FailureClass != ClassInfrastructure || !strings.Contains(mr.NotRetriedReason, "not retryable") || mr.AttemptCount != 1 {
		t.Errorf("record = class %q reason %q attempts %d", mr.FailureClass, mr.NotRetriedReason, mr.AttemptCount)
	}
	if countsAgainstAuthor(mr.FailureClass) {
		t.Error("an unbuilt land counted against the author")
	}
}
