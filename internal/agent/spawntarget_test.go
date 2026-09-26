package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The drellem2/pogo#176 tests: a spawn's target branch defaults from the work
// item's `branch:` field, a --branch that contradicts it is refused, and the
// base the worktree actually got is reported.

// branchStore writes a macguffin store whose items carry the given `branch:`
// values ("" writes no branch line) and returns its root.
func branchStore(t *testing.T, items map[string]string) string {
	t.Helper()
	root := t.TempDir()
	avail := filepath.Join(root, "work", "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, branch := range items {
		body := "---\nid: " + id + "\ntype: task\n"
		if branch != "" {
			body += "branch: " + branch + "\n"
		}
		body += "---\n# " + id + "\n"
		if err := os.WriteFile(filepath.Join(avail, id+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// targetRegistry is adoptRegistry wired to read `branch:` from storeRoot.
func targetRegistry(t *testing.T, storeRoot string) *Registry {
	t.Helper()
	reg := adoptRegistry(t)
	reg.SetWorkItemBrancher(MGWorkItemBrancher{Root: storeRoot})
	return reg
}

// spawnBase decodes the base report off a 201.
func spawnBase(t *testing.T, rr *httptest.ResponseRecorder) *SpawnBase {
	t.Helper()
	var resp SpawnPolecatAPIResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode spawn response: %v\n%s", err, rr.Body.String())
	}
	if resp.Name == "" {
		t.Errorf("the embedded agent record did not decode at top level: %s", rr.Body.String())
	}
	if resp.Base == nil {
		t.Fatalf("spawn response carries no base report: %s", rr.Body.String())
	}
	return resp.Base
}

// TestSpawnDefaultsTheTargetFromTheWorkItem is the fix itself: no --branch, an
// item that names an integration branch, and the worktree must start ON that
// branch — carrying its commits — rather than on the default branch.
func TestSpawnDefaultsTheTargetFromTheWorkItem(t *testing.T) {
	repo := strandedRepo(t)
	integSha := pushBranch(t, repo, "integ", "integ.md", "feat: integration-branch work")

	reg := targetRegistry(t, branchStore(t, map[string]string{"mg-7601": "integ"}))
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "t7601", Id: "mg-7601", Repo: repo, Template: adoptTemplate(t),
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if !carriesCommit(t, repo, "polecat-t7601", integSha) {
		t.Fatalf("the worktree does not carry the integration branch's commit %s: it was based on "+
			"the default branch, which is drellem2/pogo#176 reproduced", integSha[:12])
	}
	b := spawnBase(t, rr)
	if b.Target != "integ" || b.TargetFrom != TargetFromWorkItem || b.BaseRef != "origin/integ" {
		t.Errorf("base report = %+v, want target integ from work_item on origin/integ", *b)
	}
	if b.Warning != "" {
		t.Errorf("a base that IS the target carries a warning: %q", b.Warning)
	}
}

// TestSpawnTargetReachesTheTemplate: the defaulted branch is what {{.Branch}}
// renders, so the submit target and the PR base agree with the worktree base.
func TestSpawnTargetReachesTheTemplate(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "integ", "integ.md", "feat: integration-branch work")

	reg := targetRegistry(t, branchStore(t, map[string]string{"mg-7602": "integ"}))
	writeTemplate(t, "targetwt", "+++\nworktree = true\n+++\nsubmit --target={{.Branch}}\n")
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "t7602", Id: "mg-7602", Repo: repo, Template: "targetwt",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	prompt, err := os.ReadFile(reg.Get("t7602").PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "submit --target=integ") {
		t.Errorf("the prompt's submit target is not the item's branch:\n%s", prompt)
	}
}

// TestSpawnRefusesABranchThatContradictsTheItem is the mismatch direction: an
// explicit --branch that disagrees with the item's `branch:` is a 409 naming
// both, and nothing is created.
func TestSpawnRefusesABranchThatContradictsTheItem(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "integ", "integ.md", "feat: integration-branch work")

	reg := targetRegistry(t, branchStore(t, map[string]string{"mg-7603": "integ"}))
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "t7603", Id: "mg-7603", Repo: repo, Branch: "main", Template: adoptTemplate(t),
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for --branch main against an item on integ: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{`"main"`, `"integ"`, "mg-7603"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not name %s: %s", want, body)
		}
	}
	if reg.Get("t7603") != nil {
		t.Error("a refused spawn registered an agent")
	}
	if polecatBranchExists(repo, "polecat-t7603") {
		t.Error("a refused spawn created its branch — the refusal must precede every side effect")
	}
}

// TestSpawnAcceptsABranchThatAgreesWithTheItem is the other direction: a
// matching --branch, and an item with no `branch:` at all, both spawn — the
// refusal must not fire on agreement or on silence.
func TestSpawnAcceptsABranchThatAgreesWithTheItem(t *testing.T) {
	repo := strandedRepo(t)
	integSha := pushBranch(t, repo, "integ", "integ.md", "feat: integration-branch work")

	reg := targetRegistry(t, branchStore(t, map[string]string{"mg-7604": "integ", "mg-7605": ""}))
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "t7604", Id: "mg-7604", Repo: repo, Branch: "integ", Template: adoptTemplate(t),
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("matching --branch: status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if b := spawnBase(t, rr); b.TargetFrom != TargetFromRequest || b.BaseRef != "origin/integ" {
		t.Errorf("matching --branch: base report = %+v", *b)
	}
	if !carriesCommit(t, repo, "polecat-t7604", integSha) {
		t.Error("matching --branch: the worktree is not on the target")
	}

	// An item that names no branch keeps today's behaviour: --branch stands.
	rr = spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "t7605", Id: "mg-7605", Repo: repo, Branch: "integ", Template: adoptTemplate(t),
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("item without branch:, explicit --branch: status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if !carriesCommit(t, repo, "polecat-t7605", integSha) {
		t.Error("item without branch:: --branch no longer decides the base")
	}
}

// TestSpawnWarnsWhenTheTargetIsNotOnOrigin: the default-branch fallback is kept
// (the refinery creates the target at submit), but it is no longer silent.
func TestSpawnWarnsWhenTheTargetIsNotOnOrigin(t *testing.T) {
	repo := strandedRepo(t)
	gitRun(t, repo, "remote", "set-head", "origin", "main")

	reg := targetRegistry(t, branchStore(t, map[string]string{"mg-7606": "not-yet-on-origin"}))
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "t7606", Id: "mg-7606", Repo: repo, Template: adoptTemplate(t),
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 — a target not on origin is warned about, never refused: %s",
			rr.Code, rr.Body.String())
	}
	b := spawnBase(t, rr)
	if b.BaseRef != "origin/main" {
		t.Errorf("base = %q, want the origin/main fallback", b.BaseRef)
	}
	want := "target not-yet-on-origin not on origin — based on origin/main; the refinery will create not-yet-on-origin at submit"
	if b.Warning != want {
		t.Errorf("warning = %q, want %q", b.Warning, want)
	}
}

// TestSpawnAdoptionBaseStillWinsOverTheItemTarget pins that the stranded-adopt
// base is untouched by the new defaulting sitting beside it: an item that names
// a branch, adopted, is based on the STRANDED ref and not on origin/<branch>.
func TestSpawnAdoptionBaseStillWinsOverTheItemTarget(t *testing.T) {
	repo := strandedRepo(t)
	integSha := pushBranch(t, repo, "integ", "integ.md", "feat: integration-branch work")
	strandedSha := pushBranch(t, repo, "polecat-7607", "audit.md", "feat(audit): finished (mg-7607)")

	reg := targetRegistry(t, branchStore(t, map[string]string{"mg-7607": "integ"}))
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "t7607", Id: "mg-7607", Repo: repo, Template: adoptTemplate(t),
		StrandedAdopt: "finished; needs a rebase onto integ",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if !carriesCommit(t, repo, "polecat-t7607", strandedSha) {
		t.Fatal("the adopting worker does not carry the stranded commit: the item's target overrode the adoption base")
	}
	if carriesCommit(t, repo, "polecat-t7607", integSha) {
		t.Error("the adopting worker was based on origin/integ, not on the stranded branch")
	}
	b := spawnBase(t, rr)
	if b.Target != "integ" || b.Warning != "" {
		t.Errorf("base report = %+v, want target integ and no warning (an adopted base is not a fallback)", *b)
	}
}

// fakeBrancher is a canned WorkItemBrancher for the unit test below.
type fakeBrancher map[string]string

func (f fakeBrancher) WorkItemBranch(id string) (string, bool) {
	b, ok := f[id]
	return b, ok
}

func TestResolveSpawnTarget(t *testing.T) {
	reg := &Registry{}
	reg.SetWorkItemBrancher(fakeBrancher{"mg-a": "integ", "mg-b": ""})
	for _, tc := range []struct {
		id, branch, target, from string
		refused                  bool
	}{
		{"mg-a", "", "integ", TargetFromWorkItem, false},
		{"mg-a", "integ", "integ", TargetFromRequest, false},
		{"mg-a", " integ ", "integ", TargetFromRequest, false},
		{"mg-a", "main", "", "", true},
		{"mg-b", "", "", "", false},
		{"mg-b", "main", "main", TargetFromRequest, false},
		{"mg-missing", "main", "main", TargetFromRequest, false},
		{"", "", "", "", false},
	} {
		target, from, refusal := reg.resolveSpawnTarget(SpawnPolecatAPIRequest{Id: tc.id, Branch: tc.branch})
		if target != tc.target || from != tc.from || (refusal != "") != tc.refused {
			t.Errorf("resolveSpawnTarget(id=%q, branch=%q) = (%q, %q, refused=%v), want (%q, %q, refused=%v)",
				tc.id, tc.branch, target, from, refusal != "", tc.target, tc.from, tc.refused)
		}
	}
}
