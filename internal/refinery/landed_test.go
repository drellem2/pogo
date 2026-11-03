package refinery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
)

// enth-vcs slice 8 (§7): the merge record's `enth` field, mr.Landed(), the
// refinery_merged event's `landed`, and the deploy-hook env.

func TestLandedAccessor(t *testing.T) {
	cases := []struct {
		name  string
		mr    MergeRequest
		kind  vcs.Kind
		id    string
		label string
	}{
		{"git merged", MergeRequest{MergedSHA: "0123456789abcdef0123456789abcdef01234567"}, vcs.Git,
			"0123456789abcdef0123456789abcdef01234567", "0123456789abcdef0123456789abcdef01234567"},
		{"git not merged", MergeRequest{}, vcs.Git, "", ""},
		{"enth landed", MergeRequest{EnthSubmitted: []string{"c1"}, Enth: &vcs.LandRecord{New: strings.Repeat("ab", 32)}},
			vcs.Enth, strings.Repeat("ab", 32), "enth " + strings.Repeat("ab", 32)},
		{"enth submitted, not landed", MergeRequest{EnthSubmitted: []string{"c1"}}, vcs.Enth, "", ""},
	}
	for _, c := range cases {
		k, id := c.mr.Landed()
		if k != c.kind || id != c.id {
			t.Errorf("%s: Landed() = (%q, %q); want (%q, %q)", c.name, k, id, c.kind, c.id)
		}
		if got := c.mr.LandedLabel(0); got != c.label {
			t.Errorf("%s: LandedLabel(0) = %q; want %q", c.name, got, c.label)
		}
		d, ok := c.mr.EnthLandedDetail()
		if want := c.kind == vcs.Enth && c.id != ""; ok != want {
			t.Errorf("%s: EnthLandedDetail ok = %v; want %v", c.name, ok, want)
		} else if ok && (d["vcs"] != "enth" || d["id"] != c.id) {
			t.Errorf("%s: EnthLandedDetail = %v", c.name, d)
		}
	}
	// Shortened labels: git truncates to the caller's width, enth to 12 with
	// its kind — never a bare prefix that could pass for a git sha.
	g := MergeRequest{MergedSHA: "0123456789abcdef0123456789abcdef01234567"}
	if got := g.LandedLabel(7); got != "0123456" {
		t.Errorf("git LandedLabel(7) = %q", got)
	}
	e := MergeRequest{Enth: &vcs.LandRecord{New: strings.Repeat("ab", 32)}}
	if got := e.LandedLabel(7); got != "enth abababababab" {
		t.Errorf("enth LandedLabel(7) = %q", got)
	}
}

// A git MR's JSON is byte-for-byte what it was before slice 8: no `enth`, no
// `landed`, merged_sha where it always was.
func TestGitMergeRequestJSONUnchanged(t *testing.T) {
	at := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	mr := MergeRequest{
		ID: "mr-1", RepoPath: "/r", Branch: "polecat-p1", TargetRef: "main", Author: "mg-1",
		Status: StatusMerged, SubmitTime: at, StartTime: at, DoneTime: at,
		MergedSHA: "0123456789abcdef0123456789abcdef01234567", TargetAtStart: "fedcba9876543210fedcba9876543210fedcba98",
	}
	b, err := json.Marshal(&mr)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"mr-1","repo_path":"/r","branch":"polecat-p1","target_ref":"main","author":"mg-1","status":"merged",` +
		`"submit_time":"2026-11-03T08:00:00Z","start_time":"2026-11-03T08:00:00Z","done_time":"2026-11-03T08:00:00Z",` +
		`"merged_sha":"0123456789abcdef0123456789abcdef01234567","target_at_start":"fedcba9876543210fedcba9876543210fedcba98","failure_count":0}`
	if string(b) != want {
		t.Errorf("git MR JSON changed:\n got %s\nwant %s", b, want)
	}
}

// An enth MR, landed through a real (sandboxed) enthd, has an empty
// merged_sha — absent from its JSON, so `jq .merged_sha` is null and the
// release-cut recipe refuses to tag it — and names its landing under `enth`.
// Its refinery_merged event carries `landed: {enth, N}` and no merge commit,
// and its deploy hook sees POGO_LANDED_VCS=enth, POGO_LANDED_ID=N. The git
// control in the same test: merged_sha set, no `enth`, `landed: {git, sha}`,
// and the deploy hook sees the git sha.
func TestEnthMergeRecordAndGitControl(t *testing.T) {
	logPath := useTempEventLog(t)
	// A stale landing inherited from pogod's own environment must not reach
	// the hook.
	t.Setenv("POGO_LANDED_VCS", "stale")
	t.Setenv("POGO_LANDED_ID", "stale")

	// The hook records what it was told landed into ./landed_env, in whatever
	// directory the refinery runs it (the gate checkout for enth, the clone
	// for git).
	hook := filepath.Join(t.TempDir(), "deploy.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf '%s %s' \"$POGO_LANDED_VCS\" \"$POGO_LANDED_ID\" > landed_env\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	deployTOML := "[deploy]\ncommand = \"" + hook + "\"\n"

	fx := newEnthLandFixture(t)
	fx.writeConfig(t, "quality_gate = \""+filepath.Join(fx.ctl, "gate.sh")+"\"\n"+deployTOML)
	r, cl := newCallbackRefinery(t)
	fx.s.Work(t, "mg-1", "p1", "one", map[string]string{"x.txt": "x\n"})
	mr := fx.process(t, r, fx.submit(t, r, "mg-1"))
	if mr.Status != StatusMerged || mr.Enth == nil {
		t.Fatalf("enth MR = %s %q enth %+v; want merged with a land record", mr.Status, mr.Error, mr.Enth)
	}
	tip := fx.s.Tip(t)
	if k, id := mr.Landed(); k != vcs.Enth || id != tip {
		t.Errorf("enth Landed() = (%s, %s); want (enth, %s)", k, id, tip)
	}
	if mr.DeployError != "" {
		t.Fatalf("enth deploy: %s", mr.DeployError)
	}
	gateDir := r.enthGateDir(fx.repo)
	if env, err := os.ReadFile(filepath.Join(gateDir, "landed_env")); err != nil || string(env) != "enth "+tip {
		t.Errorf("enth deploy env = %q (%v); want %q", env, err, "enth "+tip)
	}
	b, err := json.Marshal(mr)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	if v, ok := obj["merged_sha"]; ok {
		t.Errorf("enth MR JSON has merged_sha = %v; want it absent (empty)", v)
	}
	if e, _ := obj["enth"].(map[string]any); e == nil || e["new"] != tip {
		t.Errorf("enth MR JSON enth = %v; want new = %s", obj["enth"], tip)
	}
	if len(cl.merged) != 1 {
		t.Errorf("OnMerged fired %d times; want once", len(cl.merged))
	}

	// The git control.
	originDir, branch := setupRepoWithDeploy(t, deployTOML)
	wtDir := t.TempDir()
	gr, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: wtDir})
	if err != nil {
		t.Fatal(err)
	}
	id, err := gr.Submit(MergeRequest{RepoPath: originDir, Branch: branch, TargetRef: "main", Author: "mg-git"})
	if err != nil {
		t.Fatal(err)
	}
	gr.processNext()
	gm := gr.Get(id)
	if gm == nil || gm.Status != StatusMerged || gm.MergedSHA == "" || gm.Enth != nil {
		t.Fatalf("git MR = %+v; want merged with merged_sha and no enth", gm)
	}
	if k, sha := gm.Landed(); k != vcs.Git || sha != gm.MergedSHA {
		t.Errorf("git Landed() = (%s, %s); want (git, %s)", k, sha, gm.MergedSHA)
	}
	if env, err := os.ReadFile(filepath.Join(wtDir, filepath.Base(originDir), "landed_env")); err != nil || string(env) != "git "+gm.MergedSHA {
		t.Errorf("git deploy env = %q (%v); want %q", env, err, "git "+gm.MergedSHA)
	}
	gb, _ := json.Marshal(gm)
	if strings.Contains(string(gb), `"enth"`) || strings.Contains(string(gb), `"landed"`) {
		t.Errorf("git MR JSON grew an enth/landed key: %s", gb)
	}

	merged := filterEvents(readEvents(t, logPath), "refinery_merged")
	byMR := map[string]map[string]any{}
	for _, ev := range merged {
		byMR[ev.Details["merge_request_id"].(string)] = ev.Details
	}
	ed, gd := byMR[mr.ID], byMR[gm.ID]
	if ed == nil || gd == nil {
		t.Fatalf("refinery_merged events: %v; want one per MR", merged)
	}
	if ed["merge_commit"] != "" || !reflect.DeepEqual(ed["landed"], map[string]any{"vcs": "enth", "id": tip}) {
		t.Errorf("enth refinery_merged merge_commit %v landed %v; want \"\" and {enth %s}", ed["merge_commit"], ed["landed"], tip)
	}
	if gd["merge_commit"] != gm.MergedSHA || !reflect.DeepEqual(gd["landed"], map[string]any{"vcs": "git", "id": gm.MergedSHA}) {
		t.Errorf("git refinery_merged merge_commit %v landed %v; want %s twice", gd["merge_commit"], gd["landed"], gm.MergedSHA)
	}
}

func TestDeployEnv(t *testing.T) {
	base := []string{"PATH=/bin", "POGO_LANDED_VCS=stale", "POGO_LANDED_ID=stale", "POGO_LANDED_IDX=keep"}
	got := deployEnv(base, vcs.Enth, "N")
	want := []string{"PATH=/bin", "POGO_LANDED_IDX=keep", "POGO_REFINERY=1", "POGO_LANDED_VCS=enth", "POGO_LANDED_ID=N"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deployEnv(enth) = %v; want %v", got, want)
	}
	// An unknown landing leaves both unset rather than empty-but-present.
	got = deployEnv(base, vcs.Git, "")
	want = []string{"PATH=/bin", "POGO_LANDED_IDX=keep", "POGO_REFINERY=1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deployEnv(unknown) = %v; want %v", got, want)
	}
}
