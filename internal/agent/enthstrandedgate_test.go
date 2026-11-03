package agent

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
)

// enth-vcs, mg-e2ab1: the spawn-time stranded-work gate on an enth repo, on a
// REAL sandboxed enthd. Before this, GitStrandedWorkGate's Fetch and
// ResolveTarget failed on the enth read checkout (no .git), pogod logged
// "dispatching WITHOUT the stranded-work check", and an enth item with
// unlanded Changes was dispatched from the trunk with nothing refusing it.

// enthGateTemplate is a worktree template that branches on .VCS, so the enth
// template gate (§12) lets the spawn through to a real enth checkout.
func enthGateTemplate(t *testing.T) string {
	t.Helper()
	writeTemplate(t, "enthgatewt", "+++\nworktree = true\n+++\n# polecat for {{.Id}} on {{.VCS}}\n")
	return "enthgatewt"
}

func enthGateSpawn(t *testing.T, reg *Registry, src, name, item string, mod func(*SpawnPolecatAPIRequest)) (int, string) {
	t.Helper()
	req := SpawnPolecatAPIRequest{Name: name, Id: item, Repo: src, Template: enthGateTemplate(t)}
	if mod != nil {
		mod(&req)
	}
	rr := spawnPolecat(t, reg, req)
	return rr.Code, rr.Body.String()
}

func polecatDir(t *testing.T, name string) string {
	t.Helper()
	d, _ := gitgc.DefaultPolecatsDir()
	return filepath.Join(d, name)
}

func TestEnthSpawnWithUnlandedWorkIsRefused(t *testing.T) {
	useTempEventLog(t)
	captureStrandedMail(t)
	s, src := enthStrandedFixture(t, "available")
	id, _ := s.Work(t, "mg-e2a1", "pe2a1", "feat: built", map[string]string{"x.txt": "x\n"})

	reg := adoptRegistry(t)
	code, body := enthGateSpawn(t, reg, src, "qe2a1", "mg-e2a1", nil)
	if code != http.StatusConflict {
		t.Fatalf("spawn onto an enth item with an unlanded Change = %d, want 409: %s", code, body)
	}
	for _, want := range []string{
		"UNLANDED enth Change", id, "--stranded-adopt", "--stranded-override",
		"enth --remote " + s.URL + " --repo " + s.Repo + " land " + id, "pogo land --item=mg-e2a1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal does not name %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "pogo refinery submit") {
		t.Errorf("an enth refusal names a refinery submit (the refinery refuses enth MRs):\n%s", body)
	}
	if reg.Get("qe2a1") != nil {
		t.Error("a refused spawn left an agent behind")
	}
	if _, err := os.Stat(polecatDir(t, "qe2a1")); !os.IsNotExist(err) {
		t.Errorf("a refused spawn left a checkout behind (%v)", err)
	}
}

func TestEnthSpawnWithUnlandedWorkAndAdoptSucceeds(t *testing.T) {
	logPath := useTempEventLog(t)
	captureStrandedMail(t)
	s, src := enthStrandedFixture(t, "available")
	id, _ := s.Work(t, "mg-e2a2", "pe2a2", "feat: built", map[string]string{"x.txt": "x\n"})

	reg := adoptRegistry(t)
	code, body := enthGateSpawn(t, reg, src, "qe2a2", "mg-e2a2", func(r *SpawnPolecatAPIRequest) {
		r.StrandedAdopt = "the Change is good; land it"
	})
	if code != http.StatusCreated {
		t.Fatalf("adopt spawn = %d, want 201: %s", code, body)
	}
	st, err := enthvcs.ReadState(polecatDir(t, "qe2a2"))
	if err != nil || st.Base != id {
		t.Errorf("adopted checkout base = %q (%v), want the unlanded Change %s", st.Base, err, id)
	}
	var adopted map[string]any
	for _, ev := range readEventLines(t, logPath) {
		if ev["event_type"] == "dispatch_stranded_work_adopted" {
			adopted, _ = ev["details"].(map[string]any)
		}
	}
	if adopted == nil || adopted["adopted_ref"] != id || !strings.Contains(adopted["refusal"].(string), "UNLANDED enth Change") {
		t.Errorf("adopted event = %v, want adopted_ref %s and the gate's refusal", adopted, id)
	}
}

func TestEnthSpawnWithUnlandedWorkAndOverrideSucceeds(t *testing.T) {
	logPath := useTempEventLog(t)
	captureStrandedMail(t)
	s, src := enthStrandedFixture(t, "available")
	s.Work(t, "mg-e2a3", "pe2a3", "feat: built", map[string]string{"x.txt": "x\n"})

	reg := adoptRegistry(t)
	// Both exits at once is refused, as on git (before the real dispatch, so
	// the live-owner gate does not answer first).
	code, body := enthGateSpawn(t, reg, src, "qe2a4", "mg-e2a3", func(r *SpawnPolecatAPIRequest) {
		r.StrandedOverride, r.StrandedAdopt = "spent", "good"
	})
	if code != http.StatusConflict || !strings.Contains(body, "both --stranded-adopt and --stranded-override") {
		t.Errorf("both exits = %d %s, want 409", code, body)
	}

	code, body = enthGateSpawn(t, reg, src, "qe2a3", "mg-e2a3", func(r *SpawnPolecatAPIRequest) {
		r.StrandedOverride = "the Change is spent"
	})
	if code != http.StatusCreated {
		t.Fatalf("override spawn = %d, want 201: %s", code, body)
	}
	st, err := enthvcs.ReadState(polecatDir(t, "qe2a3"))
	if err != nil || st.Base != s.Tip(t) {
		t.Errorf("override checkout base = %q (%v), want the trunk tip", st.Base, err)
	}
	found := false
	for _, ev := range readEventLines(t, logPath) {
		found = found || ev["event_type"] == "dispatch_stranded_work_overridden"
	}
	if !found {
		t.Error("no dispatch_stranded_work_overridden event")
	}

}

func TestEnthSpawnLandedOrSupersededWorkIsNotRefused(t *testing.T) {
	for _, tc := range []struct {
		name, polecat string
		setup         func(t *testing.T, s *enthdtest.Server, item string)
	}{
		{"landed", "qe2b1", func(t *testing.T, s *enthdtest.Server, item string) {
			id, _ := s.Work(t, item, "w1", "feat: built", map[string]string{"x.txt": "x\n"})
			s.Land(t, id)
		}},
		{"superseded", "qe2b2", func(t *testing.T, s *enthdtest.Server, item string) { supersededWork(t, s, item) }},
		{"no work at all", "qe2b3", func(*testing.T, *enthdtest.Server, string) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempEventLog(t)
			mail := captureStrandedMail(t)
			s, src := enthStrandedFixture(t, "available")
			tc.setup(t, s, "mg-e2a5")
			code, body := enthGateSpawn(t, adoptRegistry(t), src, tc.polecat, "mg-e2a5", nil)
			if code != http.StatusCreated {
				t.Fatalf("spawn = %d, want 201: %s", code, body)
			}
			var resp SpawnPolecatAPIResponse
			if err := json.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Base != nil && strings.Contains(resp.Base.Warning, "UNCHECKED") {
				t.Errorf("a readable listing produced the unchecked warning: %s", resp.Base.Warning)
			}
			if n := len(mail()); n != 0 {
				t.Errorf("a clean enth dispatch mailed %d alert(s)", n)
			}
		})
	}
}

// A live polecat on the item is not an exemption: the second worker would
// duplicate its Changes. The refusal names it.
func TestEnthSpawnRefusedEvenWithALivePolecatOnTheItem(t *testing.T) {
	useTempEventLog(t)
	captureStrandedMail(t)
	sandboxWitness(t)
	s, src := enthStrandedFixture(t, "claimed")
	s.Work(t, "mg-e2a6", "pe2a6", "feat: wip", map[string]string{"x.txt": "x\n"})
	reg := adoptRegistry(t)
	other := livePolecat("pe2a6", "mg-e2a6")
	other.SourceRepo = src
	reg.mu.Lock()
	reg.agents[other.Name] = other
	reg.mu.Unlock()
	t.Cleanup(func() { reg.mu.Lock(); delete(reg.agents, other.Name); reg.mu.Unlock() })
	refusal, unchecked := reg.enthStrandedCheck(*enthRepoFor(src), "mg-e2a6", "qe2a6", src)
	if unchecked != nil || !strings.Contains(refusal, "Polecat pe2a6 is live on the item") {
		t.Errorf("refusal = %q, unchecked = %v; want a refusal naming the live polecat", refusal, unchecked)
	}
}

// An unreadable listing fails open, and says so: the spawn goes out, its
// response carries the warning, and an event and a mail say it was dispatched
// unchecked — never "no work".
func TestEnthSpawnUnreadableListingDispatchesAndAlerts(t *testing.T) {
	logPath := useTempEventLog(t)
	mail := captureStrandedMail(t)
	s, src := enthStrandedFixture(t, "available")
	s.Work(t, "mg-e2a7", "pe2a7", "feat: built", map[string]string{"x.txt": "x\n"})
	bad := s.EnthRepo()
	bad.Repo = "alice/no-such-repo"
	useEnthRecord(t, src, bad)
	// The checkout would fail on the bad repo too, so the judgement is taken
	// directly first, then the spawn's alert is driven through the handler with
	// a record whose listing fails but whose checkout works.
	refusal, unchecked := adoptRegistry(t).enthStrandedCheck(bad, "mg-e2a7", "qe2a7", src)
	if refusal != "" || unchecked == nil || unchecked.ListingError == "" {
		t.Fatalf("unreadable listing: refusal=%q unchecked=%+v; want allowed with a listing error", refusal, unchecked)
	}

	// Through the handler: a listing that fails only for the gate (the stub
	// below), with a real enthd behind the checkout.
	useEnthRecord(t, src, s.EnthRepo())
	prev := enthUnlandedForItemFn
	enthUnlandedForItemFn = func(e vcs.EnthRepo, item string) ([]vcs.Change, error) {
		return enthUnlandedForItem(bad, item)
	}
	t.Cleanup(func() { enthUnlandedForItemFn = prev })

	code, body := enthGateSpawn(t, adoptRegistry(t), src, "qe2a7", "mg-e2a7", nil)
	if code != http.StatusCreated {
		t.Fatalf("spawn with an unreadable listing = %d, want 201 (fail open): %s", code, body)
	}
	var resp SpawnPolecatAPIResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Base == nil || !strings.Contains(resp.Base.Warning, "UNCHECKED") || !strings.Contains(resp.Base.Warning, "NOT a report") {
		t.Errorf("response warning = %+v, want the unchecked sentence", resp.Base)
	}
	sent := mail()
	if len(sent) != 1 || sent[0].Route != RouteDispatch || sent[0].Polecat != "qe2a7" {
		t.Fatalf("mail = %+v, want one dispatch-route alert naming qe2a7", sent)
	}
	subject, mbody := sent[0].Message()
	if !strings.Contains(subject, "dispatched WITHOUT the stranded-work check") || !strings.Contains(mbody, "NOT a report that there is no unlanded work") ||
		strings.Contains(mbody, "DO NOT DISPATCH") || !strings.Contains(mbody, "pogo agent stop qe2a7") {
		t.Errorf("unchecked-dispatch wording:\n%s\n%s", subject, mbody)
	}
	var ev map[string]any
	for _, e := range strandedEvents(t, logPath) {
		ev = e
	}
	if d, _ := ev["details"].(map[string]any); d["route"] != RouteDispatch || d["disposition"] != "unjudged" || d["polecat"] != "qe2a7" {
		t.Errorf("event details = %v", ev)
	}
}

// The git control: the same gate on a git repo still refuses with the git
// text and the refinery-submit remedy, and never consults enth.
func TestEnthStrandedGateGitControl(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-pe2a8", "f.md", "feat: built (mg-e2a8)")
	reg := adoptRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: "qe2a8", Id: "mg-e2a8", Repo: repo, Branch: "main", Template: adoptTemplate(t)})
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "UNMERGED work") ||
		strings.Contains(rr.Body.String(), "enth") {
		t.Errorf("git spawn = %d %s; want the git refusal, unchanged", rr.Code, rr.Body.String())
	}
	if n := len(mail()); n != 0 {
		t.Errorf("git refusal mailed %d alert(s)", n)
	}
}
