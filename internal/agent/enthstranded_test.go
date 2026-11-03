package agent

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/ghpr"
	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
)

// enth-vcs slice 9b (mg-35ae4): pogod's release reporter and restart sweep
// judge an enth repo from its unlanded trailer listing, on a REAL sandboxed
// enthd. Before this, both called strandedwork.Inspect on an enth read
// checkout, which has no .git: the release logged "could NOT check" and sent
// nothing, and the sweep counted the item Unjudged, log only.

// enthStrandedFixture serves a sandboxed enthd and records src, a real enth
// read checkout (no .git), as the enth project. status is what the item-status
// probe answers.
func enthStrandedFixture(t *testing.T, status string) (*enthdtest.Server, string) {
	t.Helper()
	s := enthdtest.Start(t)
	src := filepath.Join(t.TempDir(), "src")
	s.Checkout(t, vcs.DefaultEnthTrunk, src)
	useEnthRecord(t, src, s.EnthRepo())
	SetWorkItemStatusProbe(func(string) string { return status })
	t.Cleanup(func() { SetWorkItemStatusProbe(nil) })
	return s, src
}

func useEnthRecord(t *testing.T, src string, e vcs.EnthRepo) {
	t.Helper()
	SetRepoVCS(func(p string) (vcs.Kind, *vcs.EnthRepo) {
		if p == src {
			e := e
			return vcs.Enth, &e
		}
		return vcs.Git, nil
	})
	t.Cleanup(func() { SetRepoVCS(nil) })
}

// supersededWork makes item's Change A, then a Change B on the trunk carrying
// Pogo-Supersedes: A, and lands B — the self-land conflict shape (mg-5b904).
// A stays unlanded forever.
func supersededWork(t *testing.T, s *enthdtest.Server, item string) (a, b string) {
	t.Helper()
	a, _ = s.Work(t, item, "w1", "feat: first try", map[string]string{"x.txt": "1\n"})
	dir := filepath.Join(t.TempDir(), "w2")
	s.Checkout(t, vcs.DefaultEnthTrunk, dir)
	writeFile(t, filepath.Join(dir, "x.txt"), "2\n")
	b = s.Change(t, dir, "feat: re-made\n\n"+vcs.TrailerItem+": "+item+"\n"+vcs.TrailerWorker+": w2\n"+
		vcs.TrailerSupersedes+": "+a+"\n")
	s.Land(t, b)
	return a, b
}

func releaseEnth(t *testing.T, reg *Registry, src, name, item string) {
	t.Helper()
	a := livePolecat(name, item)
	a.SourceRepo = src
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}
}

func enthReleaseRegistry(t *testing.T) *Registry {
	t.Helper()
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})
	return reg
}

func strandedEvents(t *testing.T, logPath string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range readEventLines(t, logPath) {
		if et, _ := ev["event_type"].(string); et == "work_item_stranded_push" {
			out = append(out, ev)
		}
	}
	return out
}

func TestEnthReleaseReportsUnlandedWorkAsStranded(t *testing.T) {
	logPath := useTempEventLog(t)
	mail := captureStrandedMail(t)
	s, src := enthStrandedFixture(t, "available")
	id, _ := s.Work(t, "mg-e5a1", "pe5a1", "feat: built", map[string]string{"x.txt": "x\n"})
	reg := enthReleaseRegistry(t)

	releaseEnth(t, reg, src, "pe5a1", "mg-e5a1")

	sent := mail()
	if len(sent) != 1 {
		t.Fatalf("an enth item with an unlanded Change and no live polecat mailed %d alert(s), want 1", len(sent))
	}
	a := sent[0]
	if a.Enth == nil || a.Enth.Work == nil || len(a.Enth.Work.Changes) != 1 || a.Enth.Work.Changes[0] != id {
		t.Fatalf("alert's enth work = %+v, want the one unlanded Change %s", a.Enth, id)
	}
	if a.Route != RouteRelease {
		t.Errorf("route = %q", a.Route)
	}
	subject, body := a.Message()
	if !strings.HasPrefix(subject, "[stranded-push]") || !strings.Contains(subject, "do NOT dispatch at mg-e5a1") {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{"pogo land --item=mg-e5a1", "enth --remote " + s.URL, "land " + id, "DO NOT DISPATCH A WORKER AT mg-e5a1"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	// The remedy must not be a refinery submit: the refinery refuses enth MRs.
	if strings.Contains(body, "pogo refinery submit ") || strings.Contains(body, "polecat-pe5a1") {
		t.Errorf("enth alert names a refinery submit or a git branch:\n%s", body)
	}
	evs := strandedEvents(t, logPath)
	if len(evs) != 1 {
		t.Fatalf("stranded_push events = %d, want 1", len(evs))
	}
	d, _ := evs[0]["details"].(map[string]any)
	if d["vcs"] != "enth" || d["disposition"] != "resubmit" || d["unmerged"] != float64(1) || d["route"] != RouteRelease {
		t.Errorf("event details = %v", d)
	}
}

func TestEnthReleaseDoneItemIsStillReported(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	s, src := enthStrandedFixture(t, "done")
	s.Work(t, "mg-e5a2", "pe5a2", "feat: built", map[string]string{"x.txt": "x\n"})
	releaseEnth(t, enthReleaseRegistry(t), src, "pe5a2", "mg-e5a2")
	sent := mail()
	if len(sent) != 1 {
		t.Fatalf("done item with unlanded work: %d mail(s), want 1", len(sent))
	}
	subject, body := sent[0].Message()
	if !strings.Contains(subject, "is done but its enth work never landed") || !strings.Contains(body, "THE ITEM IS ALREADY DONE") {
		t.Errorf("closed-item wording missing:\n%s\n%s", subject, body)
	}
}

func TestEnthReleaseLandedChangeIsNotReported(t *testing.T) {
	logPath := useTempEventLog(t)
	mail := captureStrandedMail(t)
	s, src := enthStrandedFixture(t, "available")
	id, _ := s.Work(t, "mg-e5a3", "pe5a3", "feat: built", map[string]string{"x.txt": "x\n"})
	s.Land(t, id)
	releaseEnth(t, enthReleaseRegistry(t), src, "pe5a3", "mg-e5a3")
	if n := len(mail()); n != 0 {
		t.Errorf("a landed Change mailed %d alert(s)", n)
	}
	if n := len(strandedEvents(t, logPath)); n != 0 {
		t.Errorf("a landed Change emitted %d stranded event(s)", n)
	}
}

func TestEnthReleaseSupersededChangeIsNotReported(t *testing.T) {
	logPath := useTempEventLog(t)
	mail := captureStrandedMail(t)
	s, src := enthStrandedFixture(t, "available")
	supersededWork(t, s, "mg-e5a4")
	releaseEnth(t, enthReleaseRegistry(t), src, "pe5a4", "mg-e5a4")
	if n := len(mail()); n != 0 {
		t.Errorf("a superseded Change mailed %d alert(s)", n)
	}
	if n := len(strandedEvents(t, logPath)); n != 0 {
		t.Errorf("a superseded Change emitted %d stranded event(s)", n)
	}
}

func TestEnthReleaseUnreadableListingAlerts(t *testing.T) {
	logPath := useTempEventLog(t)
	mail := captureStrandedMail(t)
	s, src := enthStrandedFixture(t, "available")
	s.Work(t, "mg-e5a5", "pe5a5", "feat: built", map[string]string{"x.txt": "x\n"})
	// The record names a repo enthd does not serve: the listing cannot be read.
	bad := s.EnthRepo()
	bad.Repo = "alice/no-such-repo"
	useEnthRecord(t, src, bad)
	releaseEnth(t, enthReleaseRegistry(t), src, "pe5a5", "mg-e5a5")

	sent := mail()
	if len(sent) != 1 {
		t.Fatalf("an unreadable listing mailed %d alert(s), want 1 — it must alert, never allow", len(sent))
	}
	if sent[0].Enth == nil || sent[0].Enth.ListingError == "" || sent[0].Enth.Work != nil {
		t.Fatalf("alert enth = %+v, want a listing error and no work", sent[0].Enth)
	}
	subject, body := sent[0].Message()
	if !strings.Contains(subject, "could not list mg-e5a5's enth work") || !strings.Contains(body, "NOT a report that there is none") ||
		!strings.Contains(body, "UNTIL THE LISTING ANSWERS") || strings.Contains(body, "already exists") {
		t.Errorf("unreadable wording missing:\n%s\n%s", subject, body)
	}
	evs := strandedEvents(t, logPath)
	if len(evs) != 1 {
		t.Fatalf("stranded_push events = %d, want 1", len(evs))
	}
	if d, _ := evs[0]["details"].(map[string]any); d["disposition"] != "unjudged" || d["listing_error"] == "" {
		t.Errorf("event details = %v", d)
	}
}

func TestEnthReleaseWithAnotherLivePolecatIsInFlight(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	s, src := enthStrandedFixture(t, "claimed")
	s.Work(t, "mg-e5a6", "pe5a6", "feat: built", map[string]string{"x.txt": "x\n"})
	reg := enthReleaseRegistry(t)
	other := livePolecat("qe5a6", "mg-e5a6")
	other.SourceRepo = src
	reg.agents[other.Name] = other
	releaseEnth(t, reg, src, "pe5a6", "mg-e5a6")
	if n := len(mail()); n != 0 {
		t.Errorf("unlanded work with another live polecat on the item mailed %d alert(s)", n)
	}
}

// liveWitness records a polecat whose process is still running.
func liveWitness(t *testing.T, name, workItem, repo string) {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Process.Wait() })
	if err := RecordPolecatWitness(name, cmd.Process.Pid, workItem, repo); err != nil {
		t.Fatal(err)
	}
}

func TestEnthStartupSweep(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, s *enthdtest.Server, src string) string // returns the repo the witness names
		alive bool
		want  StrandedSweepReport
		mails int
	}{
		{name: "unlanded, polecat dead: stranded", mails: 1, want: StrandedSweepReport{Candidates: 1, Stranded: 1},
			setup: func(t *testing.T, s *enthdtest.Server, src string) string {
				s.Work(t, "mg-e5b1", "pe5b1", "feat: built", map[string]string{"x.txt": "x\n"})
				return src
			}},
		{name: "landed: clean", want: StrandedSweepReport{Candidates: 1, Clean: 1},
			setup: func(t *testing.T, s *enthdtest.Server, src string) string {
				id, _ := s.Work(t, "mg-e5b1", "pe5b1", "feat: built", map[string]string{"x.txt": "x\n"})
				s.Land(t, id)
				return src
			}},
		{name: "superseded: clean", want: StrandedSweepReport{Candidates: 1, Clean: 1},
			setup: func(t *testing.T, s *enthdtest.Server, src string) string {
				supersededWork(t, s, "mg-e5b1")
				return src
			}},
		{name: "unreadable listing: unjudged AND alerted", mails: 1, want: StrandedSweepReport{Candidates: 1, Unjudged: 1},
			setup: func(t *testing.T, s *enthdtest.Server, src string) string {
				bad := s.EnthRepo()
				bad.Repo = "alice/no-such-repo"
				useEnthRecord(t, src, bad)
				return src
			}},
		{name: "polecat still running: in flight", alive: true, want: StrandedSweepReport{Candidates: 1, InFlight: 1},
			setup: func(t *testing.T, s *enthdtest.Server, src string) string {
				s.Work(t, "mg-e5b1", "pe5b1", "feat: built", map[string]string{"x.txt": "x\n"})
				return src
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxWitness(t)
			useTempEventLog(t)
			mail := captureStrandedMail(t)
			s, src := enthStrandedFixture(t, "available")
			repo := tc.setup(t, s, src)
			if tc.alive {
				liveWitness(t, "pe5b1", "mg-e5b1", repo)
			} else {
				deadWitness(t, "pe5b1", "mg-e5b1", repo)
			}
			rep := newDrainTestRegistry(t).ReportStrandedWorkAcrossRestart()
			if rep != tc.want {
				t.Errorf("sweep = %+v, want %+v", rep, tc.want)
			}
			sent := mail()
			if len(sent) != tc.mails {
				t.Fatalf("sweep mailed %d, want %d", len(sent), tc.mails)
			}
			for _, a := range sent {
				if a.Route != RouteRestartSweep || a.Enth == nil {
					t.Errorf("sweep alert route=%q enth=%v", a.Route, a.Enth)
				}
				if _, body := a.Message(); strings.Contains(body, "pogo refinery submit ") {
					t.Errorf("sweep's enth alert names a refinery submit:\n%s", body)
				}
			}
		})
	}
}

// The git control: the same release on a git repo is the git alert, with no
// enth half and the refinery submit remedy, exactly as before.
func TestEnthStrandedGitControl(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	stubPRProbe(t, func(string, string) (ghpr.Answer, error) { return ghpr.Answer{}, nil })
	SetWorkItemStatusProbe(func(string) string { return "available" })
	t.Cleanup(func() { SetWorkItemStatusProbe(nil) })
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-pe5c1", "f.md", "feat: built (mg-e5c1)")
	reg := enthReleaseRegistry(t)
	releaseEnth(t, reg, repo, "pe5c1", "mg-e5c1")
	sent := mail()
	if len(sent) != 1 || sent[0].Enth != nil {
		t.Fatalf("git release: %d mail(s), enth=%v; want 1 git alert", len(sent), sent)
	}
	if _, body := sent[0].Message(); !strings.Contains(body, "pogo refinery submit polecat-pe5c1") {
		t.Errorf("git remedy changed:\n%s", body)
	}
}
