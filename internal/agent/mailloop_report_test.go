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
	"time"
)

// writeCrewPrompt adds a second crew agent to the sandbox sandboxDesiredState
// already created. It writes into the SAME temp home rather than re-sandboxing,
// because sandboxDesiredState mints a fresh HOME on every call and a second
// call would discard the first agent.
func writeCrewPrompt(t *testing.T, name string, autoStart bool) {
	t.Helper()
	flag := "false"
	if autoStart {
		flag = "true"
	}
	path := filepath.Join(CrewPromptDir(), name+".md")
	if err := os.WriteFile(path, []byte("+++\nauto_start = "+flag+"\n+++\n# "+name+"\n"), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
}

// TestMailLoopReport_AnnouncesWithoutBeingAskedForAName is mg-032b's acceptance.
//
// The judgement has been correct since mg-de08 and complete since mg-738f, and
// its only consumer was `pogo agent diagnose <name>` — which cannot be run
// without already knowing the name. This asserts the fleet-wide read: ask
// nothing, and be told WHICH agent cannot be reached.
func TestMailLoopReport_AnnouncesWithoutBeingAskedForAName(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	writeCrewPrompt(t, "architect", true)
	now := time.Now()

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	deaf := mailLoopCrewAgent("pm-pogo", now)
	fine := mailLoopCrewAgent("architect", now)
	reg.agents["pm-pogo"] = deaf
	reg.agents["architect"] = fine

	// architect has a loop; pm-pogo's was reaped.
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{"crew-architect": true}})

	rep, err := reg.MailLoopReport()
	if err != nil {
		t.Fatalf("MailLoopReport: %v", err)
	}
	if len(rep.Missing) != 1 || rep.Missing[0].Name != "pm-pogo" {
		t.Fatalf("Missing = %+v, want exactly [pm-pogo] — the point of the report is that it NAMES the agent", rep.Missing)
	}
	if rep.Missing[0].Identity != "crew-pm-pogo" {
		t.Errorf("Identity = %q, want %q — the event-log form is what the notifier matches on", rep.Missing[0].Identity, "crew-pm-pogo")
	}
	if rep.Scanned != 2 {
		t.Errorf("Scanned = %d, want 2", rep.Scanned)
	}
	if rep.Judged != 2 {
		t.Errorf("Judged = %d, want 2 — both are expected crew agents", rep.Judged)
	}
	if !rep.Actionable() {
		t.Error("Actionable() = false with a deaf agent in the report")
	}

	// The report must AGREE with diagnose, agent for agent. Two answers to the
	// same question is how a detector and its CLI drift apart; this pins them
	// to the one implementation (mailLoopFor).
	if diag := reg.diagnose(deaf); !diag.MailCheckMissing {
		t.Error("diagnose(pm-pogo).MailCheckMissing = false while the fleet report calls it missing")
	}
	if diag := reg.diagnose(fine); diag.MailCheckMissing {
		t.Error("diagnose(architect).MailCheckMissing = true while the fleet report calls it fine")
	}

	// Positive control: restore the loop and the report is empty — the reader
	// must fire on a MISSING loop, not on every agent.
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{"crew-architect": true, "crew-pm-pogo": true}})
	rep, err = reg.MailLoopReport()
	if err != nil {
		t.Fatalf("MailLoopReport: %v", err)
	}
	if len(rep.Missing) != 0 {
		t.Errorf("positive control: Missing = %+v, want empty", rep.Missing)
	}
	if rep.Judged != 2 {
		t.Errorf("positive control: Judged = %d, want 2", rep.Judged)
	}
	if rep.Actionable() {
		t.Error("positive control: Actionable() = true with every loop present")
	}
}

// TestMailLoopReport_NoProviderIsAnErrorNotAnAllClear pins the distinction the
// whole lineage turns on: "nothing is missing" and "I could not look" must not
// render identically. A registry with no mail-check provider has no basis to
// judge, and saying so is the only honest answer.
func TestMailLoopReport_NoProviderIsAnErrorNotAnAllClear(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.agents["pm-pogo"] = mailLoopCrewAgent("pm-pogo", time.Now())

	if _, err := reg.MailLoopReport(); !errors.Is(err, ErrNoMailCheckJudgement) {
		t.Fatalf("MailLoopReport error = %v, want ErrNoMailCheckJudgement — a blind detector must not report a clean fleet", err)
	}
}

// TestMailLoopReport_InheritsDiagnoseExclusions asserts the report does not
// invent a RED that diagnose would not report. Each of these would be a false
// alarm, and mg-738f's reasoning is binding: a health signal that cries wolf
// gets ignored, which is how the fleet ends up back where mg-de08 started.
func TestMailLoopReport_InheritsDiagnoseExclusions(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	now := time.Now()

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// A polecat, RUNNING and with no loop: excluded deliberately (mg-e633 /
	// mg-6fe0 own its registration path).
	cat := mailLoopCrewAgent("cat-032b", now)
	cat.Type = TypePolecat
	cat.PID = liveProcess(t)
	reg.agents["cat-032b"] = cat

	// A configured agent that is NOT running: "not there" is not a fault.
	off := mailLoopCrewAgent("doctor", now)
	off.PID = deadProcess(t)
	reg.agents["doctor"] = off

	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})

	rep, err := reg.MailLoopReport()
	if err != nil {
		t.Fatalf("MailLoopReport: %v", err)
	}
	if len(rep.Missing) != 0 {
		t.Errorf("Missing = %+v, want empty — neither a polecat nor a stopped agent is owed a loop", rep.Missing)
	}
	if rep.Scanned != 2 {
		t.Errorf("Scanned = %d, want 2", rep.Scanned)
	}
	if rep.Judged != 0 {
		t.Errorf("Judged = %d, want 0 — neither agent is judgeable", rep.Judged)
	}

	// And the render must NOT read as an all-clear when nothing was judged.
	out := rep.Render()
	if !strings.Contains(out, "NOTHING JUDGED") {
		t.Errorf("Render() with zero judged agents = %q; it must not print an all-clear for a fleet it never evaluated", out)
	}
}

// TestMailLoopReport_RenderNamesTheAgent pins the one property the per-agent
// subcommand could not offer: the output supplies the name the operator did not
// have.
func TestMailLoopReport_RenderNamesTheAgent(t *testing.T) {
	rep := MailLoopReport{
		Scanned: 3, Judged: 2,
		Missing: []MailLoopFinding{{Name: "doctor", Identity: "crew-doctor", Type: TypeCrew, Alive: true}},
	}
	out := rep.Render()
	for _, want := range []string{"doctor", "pogo agent diagnose doctor", "mail-check-doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() missing %q:\n%s", want, out)
		}
	}
}

// TestHandleMailLoops_ServesTheFleetRead covers the HTTP surface `pogo
// check-mailloops` reads, including the 503: pogod answering "I cannot judge"
// must not be decodable as an empty finding list.
func TestHandleMailLoops_ServesTheFleetRead(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.agents["pm-pogo"] = mailLoopCrewAgent("pm-pogo", time.Now())

	// No provider: 503, not 200-with-empty-body.
	rr := httptest.NewRecorder()
	reg.handleMailLoops(rr, httptest.NewRequest(http.MethodGet, "/agents/mail-loops", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d when there is no basis to judge", rr.Code, http.StatusServiceUnavailable)
	}

	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})
	rr = httptest.NewRecorder()
	reg.handleMailLoops(rr, httptest.NewRequest(http.MethodGet, "/agents/mail-loops", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got MailLoopReport
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Missing) != 1 || got.Missing[0].Name != "pm-pogo" {
		t.Errorf("Missing = %+v, want [pm-pogo]", got.Missing)
	}
}
