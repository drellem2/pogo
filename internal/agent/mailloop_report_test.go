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

// TestMailLoopReport_GreenPathNamesWhoItDidNotJudge is drellem2/pogo#127's
// acceptance: the all-clear branch was the ONLY one that omitted the
// who-was-not-judged disclosure the other two carry, so a verdict over 2 of 6
// agents read as a verdict over the fleet.
//
// It also pins the reason taxonomy end to end. All three reasons are staged in
// one registry deliberately: the reasons come from mailLoopExclusionFor, which
// is the same function mailLoopJudgeable is defined in terms of, and a roster
// naming a reason the predicate would not give is this issue one level in.
func TestMailLoopReport_GreenPathNamesWhoItDidNotJudge(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	now := time.Now()

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// Judged and fine — the one agent the all-clear is actually about.
	reg.agents["pm-pogo"] = mailLoopCrewAgent("pm-pogo", now)
	// A running polecat: owns its own registration path (mg-e633).
	cat := mailLoopCrewAgent("cat-032b", now)
	cat.Type = TypePolecat
	cat.PID = liveProcess(t)
	reg.agents["cat-032b"] = cat
	// A configured agent that is not running.
	off := mailLoopCrewAgent("doctor", now)
	off.PID = deadProcess(t)
	reg.agents["doctor"] = off
	// Alive, but this machine has no prompt for it.
	ghost := mailLoopCrewAgent("ghost", now)
	ghost.PID = liveProcess(t)
	reg.agents["ghost"] = ghost

	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{"crew-pm-pogo": true}})

	rep, err := reg.MailLoopReport()
	if err != nil {
		t.Fatalf("MailLoopReport: %v", err)
	}
	if len(rep.Missing) != 0 || rep.Judged != 1 || rep.Scanned != 4 {
		t.Fatalf("want the GREEN branch over 1 of 4; got Missing=%+v Judged=%d Scanned=%d",
			rep.Missing, rep.Judged, rep.Scanned)
	}
	if rep.Unjudged == nil {
		t.Fatal("Unjudged = nil from a registry that computed the set; absent means 'this daemon does not report it'")
	}
	want := []MailLoopExclusion{
		{Name: "cat-032b", Type: TypePolecat, Reason: ExclusionPolecat},
		{Name: "doctor", Type: TypeCrew, Reason: ExclusionNotRunning},
		{Name: "ghost", Type: TypeCrew, Reason: ExclusionNotConfigured},
	}
	if len(*rep.Unjudged) != len(want) {
		t.Fatalf("Unjudged = %+v, want %+v", *rep.Unjudged, want)
	}
	for i, w := range want {
		if got := (*rep.Unjudged)[i]; got != w {
			t.Errorf("Unjudged[%d] = %+v, want %+v", i, got, w)
		}
	}

	// The TEXT is the half the issue was filed about.
	out := rep.Render()
	if !strings.Contains(out, "All 1 judged agent(s)") {
		t.Errorf("the all-clear line is gone:\n%s", out)
	}
	for _, w := range []string{"Not judged: 3 of 4", "cat-032b", "doctor", "ghost",
		"registers its own mail loop at spawn", "not running", "not configured"} {
		if !strings.Contains(out, w) {
			t.Errorf("Render() missing %q — an all-clear over 1 of 4 must name the other 3:\n%s", w, out)
		}
	}

	// And the JSON must say what the text says; a machine reader that gets
	// strictly less than the operator is the same defect wearing a different hat.
	blob, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, w := range []string{`"unjudged"`, `"cat-032b"`, `"not_running"`, `"not_configured"`, `"polecat"`} {
		if !strings.Contains(string(blob), w) {
			t.Errorf("JSON missing %s:\n%s", w, blob)
		}
	}

	// Exit status does NOT move on the unjudged set. Everything in it is
	// excluded on purpose; firing on it would be the cry-wolf mg-738f's
	// boundary exists to prevent (mg-0db1, recorded so it is not reopened).
	if rep.Actionable() {
		t.Error("Actionable() = true with an empty Missing — the unjudged set must not move exit status")
	}
}

// TestMailLoopReport_RedPathNamesWhoItDidNotJudge covers the second branch the
// shared renderer fixed. The RED path printed "Judged N of S" and stopped: a
// count, with no way to turn it into names. The issue named the green branch;
// routing all three through one renderer fixed this one too.
func TestMailLoopReport_RedPathNamesWhoItDidNotJudge(t *testing.T) {
	sandboxDesiredState(t, "pm-pogo", true)
	now := time.Now()

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.agents["pm-pogo"] = mailLoopCrewAgent("pm-pogo", now)
	cat := mailLoopCrewAgent("cat-032b", now)
	cat.Type = TypePolecat
	cat.PID = liveProcess(t)
	reg.agents["cat-032b"] = cat

	// pm-pogo's loop was reaped: the RED branch.
	reg.SetMailCheckProvider(fakeMailChecks{have: map[string]bool{}})

	rep, err := reg.MailLoopReport()
	if err != nil {
		t.Fatalf("MailLoopReport: %v", err)
	}
	if len(rep.Missing) != 1 {
		t.Fatalf("want the RED branch; Missing = %+v", rep.Missing)
	}
	out := rep.Render()
	for _, w := range []string{"NO mail-check schedule", "Not judged: 1 of 2", "cat-032b",
		"registers its own mail loop at spawn"} {
		if !strings.Contains(out, w) {
			t.Errorf("Render() missing %q on the RED branch:\n%s", w, out)
		}
	}
}

// TestMailLoopReport_AbsentUnjudgedSetRendersUnknownNotZero is the version-skew
// guard, and it is the whole reason Unjudged is a pointer.
//
// internal/client plain-decodes this struct off the wire with no version
// negotiation, so a pogod older than the client simply does not send the field.
// If absent flattened to empty, the fix would print a confident "0 not judged"
// over a fleet it judged 2 of 6 of — this issue's exact defect, green, inside
// its own fix. That is not a hypothetical: when this shipped, the running pogod
// was ~93 commits behind main, so the skew case WAS the current state.
func TestMailLoopReport_AbsentUnjudgedSetRendersUnknownNotZero(t *testing.T) {
	// Exactly what an older pogod puts on the wire: no `unjudged` key at all.
	const oldDaemon = `{"now":"2026-08-07T20:00:00Z","scanned":6,"judged":2}`

	var rep MailLoopReport
	if err := json.Unmarshal([]byte(oldDaemon), &rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rep.Unjudged != nil {
		t.Fatalf("Unjudged = %+v after decoding a payload that never mentioned it; absent must stay absent", *rep.Unjudged)
	}

	out := rep.Render()
	if !strings.Contains(out, "UNKNOWN") {
		t.Errorf("Render() over an absent unjudged set must say UNKNOWN:\n%s", out)
	}
	if !strings.Contains(out, "4 of 6") {
		t.Errorf("Render() must still state the COUNT — it is derivable from scanned/judged, which every version sends:\n%s", out)
	}
	for _, forbidden := range []string{"Not judged: 0", "0 of 6"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("Render() claimed %q over a daemon that reported nothing — 'not told' is not 'nobody':\n%s", forbidden, out)
		}
	}

	// The positive control: the SAME counts, with the field present and empty,
	// is a genuine full-coverage report and must not print the unknown notice.
	var full MailLoopReport
	if err := json.Unmarshal([]byte(`{"scanned":2,"judged":2,"unjudged":[]}`), &full); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if full.Unjudged == nil {
		t.Fatal("`\"unjudged\": []` decoded to absent — absent and empty must be distinguishable on the wire")
	}
	if got := full.Render(); strings.Contains(got, "Not judged") || strings.Contains(got, "UNKNOWN") {
		t.Errorf("a report that judged everything must not print a coverage caveat:\n%s", got)
	}

	// And the wire itself must carry the distinction, or no decoder can.
	empty, err := json.Marshal(MailLoopReport{Scanned: 2, Judged: 2, Unjudged: &[]MailLoopExclusion{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(empty), `"unjudged":[]`) {
		t.Errorf("an empty unjudged set must serialise as [] rather than being omitted:\n%s", empty)
	}
	absent, err := json.Marshal(MailLoopReport{Scanned: 2, Judged: 2})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(absent), `"unjudged":[]`) {
		t.Errorf("an unreported unjudged set must not serialise as an empty one:\n%s", absent)
	}
}

// TestMailLoopExclusionReason_DescribeKeepsAnUnknownReasonLegible guards the
// other direction of skew: a NEWER pogod naming a category this binary does not
// know must render as that category, not as a blank where a reason should be.
func TestMailLoopExclusionReason_DescribeKeepsAnUnknownReasonLegible(t *testing.T) {
	if got := MailLoopExclusionReason("prompt_unreadable").Describe(); !strings.Contains(got, "prompt_unreadable") {
		t.Errorf("Describe() of an unknown reason = %q; it must render the reason verbatim", got)
	}
	if got := MailLoopExclusionReason("").Describe(); got == "" {
		t.Error("Describe() of an empty reason = \"\"; a missing reason must read as missing, not as absent text")
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
	// The endpoint must SAY it reports the unjudged set, even when the set is
	// empty — that is the signal a client uses to tell this daemon apart from
	// one too old to have the field at all.
	if got.Unjudged == nil {
		t.Error("decoded Unjudged = nil; the endpoint must put `\"unjudged\": []` on the wire rather than omitting it")
	}
}
