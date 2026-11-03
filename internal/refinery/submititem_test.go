package refinery

import (
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

// An enth repo has no merge queue (mg-5b904): every enth submit is refused
// with the pointer to the worker's own land, before enth is asked anything.
func TestSubmitEnthItemIsRefused(t *testing.T) {
	r, _ := newCallbackRefinery(t)
	repo := t.TempDir() // an enth project's path: no .git, so any git call fails

	for name, req := range map[string]MergeRequest{
		"an item":              {Item: "mg-1"},
		"an item and a branch": {Item: "mg-1", Branch: "polecat-p1"},
		"a branch":             {Branch: "b"},
	} {
		t.Run(name, func(t *testing.T) {
			bin, argv := fakeEnth(t, change("c1", "T", "mg-1", "p1"))
			withEnthRepo(t, repo, bin)
			req.RepoPath = repo
			id, err := r.Submit(req)
			if err == nil || !strings.Contains(err.Error(), "pogo land --item=") || !strings.Contains(err.Error(), "no merge queue") {
				t.Fatalf("Submit = %q, %v; want the self-land refusal", id, err)
			}
			if data, _ := os.ReadFile(argv); len(strings.TrimSpace(string(data))) != 0 {
				t.Errorf("a refused submit still called enth: %q", data)
			}
		})
	}
	if len(r.Queue()) != 0 {
		t.Error("a refused enth submit was queued")
	}
	// Submit's own first check still comes first.
	bin, _ := fakeEnth(t, "")
	withEnthRepo(t, repo, bin)
	if _, err := r.Submit(MergeRequest{RepoPath: repo}); err == nil || !strings.Contains(err.Error(), "branch is required") {
		t.Errorf("no item, no branch = %v", err)
	}
}

// An enth MR that reaches the queue — whatever its repo's record now says —
// is failed before any git step, never handed to the git pipeline.
func TestProcessMergeRefusesEnthWork(t *testing.T) {
	r, _ := newCallbackRefinery(t)
	mr := &MergeRequest{ID: "mr-x", RepoPath: t.TempDir(), Item: "mg-1", TargetRef: "main", EnthSubmitted: []string{"c1"}}
	_, err := r.processMerge(mr)
	if err == nil || !strings.Contains(err.Error(), "no longer lands enth work") || !strings.Contains(err.Error(), "pogo land --item=mg-1") {
		t.Fatalf("processMerge = %v, want the self-land refusal", err)
	}
	if mr.FailureClass != ClassDefect || !strings.Contains(mr.NotRetriedReason, "not retryable") || mr.AttemptCount != 1 || mr.Attempts[0].Stage != "enth-config" {
		t.Errorf("record = class %q reason %q attempts %d", mr.FailureClass, mr.NotRetriedReason, mr.AttemptCount)
	}
}
