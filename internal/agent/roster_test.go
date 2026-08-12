package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// newRosterTestRegistry gives each test an isolated HOME, an initialised prompt
// tree, and a registry whose crew agents spawn as plain `cat` processes.
func newRosterTestRegistry(t *testing.T) *Registry {
	t.Helper()
	testsandbox.Isolate(t)
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	reg.SetCommandConfig(catCommandConfig{})
	return reg
}

func memberByName(t *testing.T, rep RosterReport, name string) RosterMember {
	t.Helper()
	for _, m := range rep.Members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("roster has no member %q; members=%v", name, rep.Members)
	return RosterMember{}
}

// TestRosterReport_AbsentConfiguredAgentIsAMember is mg-7d20's core claim: an
// agent that is configured and not running has to appear SOMEWHERE. Every other
// fleet instrument iterates the registry, so this is the only reading in which a
// stopped `doctor` is a row rather than a silence.
func TestRosterReport_AbsentConfiguredAgentIsAMember(t *testing.T) {
	reg := newRosterTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "doctorish", "+++\nauto_start = false\nrestart_on_crash = false\n+++\n# doctorish\n")

	rep, err := reg.RosterReport()
	if err != nil {
		t.Fatalf("RosterReport: %v", err)
	}
	if rep.Configured != 1 {
		t.Fatalf("expected 1 configured agent, got %d (%v)", rep.Configured, rep.Members)
	}
	if len(rep.Absent) != 1 || rep.Absent[0].Name != "doctorish" {
		t.Fatalf("expected doctorish absent, got %v", rep.Absent)
	}
	m := rep.Absent[0]
	if m.State != RosterAbsent {
		t.Errorf("state = %q, want %q", m.State, RosterAbsent)
	}
	if m.Class != RosterOnDemand {
		t.Errorf("class = %q, want %q", m.Class, RosterOnDemand)
	}
	if m.Identity != "crew-doctorish" {
		t.Errorf("identity = %q, want crew-doctorish", m.Identity)
	}
	if m.RestartOnCrash {
		t.Error("restart_on_crash = false in frontmatter must survive the crew default")
	}
	if rep.Complete() {
		t.Error("Complete() must be false while a configured agent is absent")
	}

	// And the reason the state is worth naming: the registry — the set every
	// other detector iterates — contains nothing at all.
	if got := len(reg.List()); got != 0 {
		t.Fatalf("registry should be empty, has %d entries", got)
	}
}

// TestRosterReport_RunningAgentIsPresent pins the other half: once the agent is
// up it is a present member, not a finding.
func TestRosterReport_RunningAgentIsPresent(t *testing.T) {
	reg := newRosterTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "upright", "+++\nauto_start = true\nrestart_on_crash = true\n+++\n# upright\n")

	if _, err := reg.StartCrewAgent("upright"); err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}

	rep, err := reg.RosterReport()
	if err != nil {
		t.Fatalf("RosterReport: %v", err)
	}
	if len(rep.Absent) != 0 {
		t.Fatalf("expected no absences, got %v", rep.Absent)
	}
	if rep.Present != 1 {
		t.Errorf("present = %d, want 1", rep.Present)
	}
	if !rep.Complete() {
		t.Error("Complete() should be true with every configured agent running")
	}
	m := memberByName(t, rep, "upright")
	if m.Class != RosterSupervised {
		t.Errorf("class = %q, want %q", m.Class, RosterSupervised)
	}
	if !m.RestartOnCrash {
		t.Error("restart_on_crash = true should be reported")
	}
}

// TestRosterReport_ParkedIsNotAFinding: park is the SUPPORTED way to be down.
// It is already visible in `pogo agent list` as status=parked, so counting it as
// an absence would be crying wolf over a state somebody declared on purpose.
func TestRosterReport_ParkedIsNotAFinding(t *testing.T) {
	reg := newRosterTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "napper", "+++\nauto_start = true\nrestart_on_crash = true\n+++\n# napper\n")
	if _, err := reg.StartCrewAgent("napper"); err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}
	if _, err := reg.Park("napper", 2*time.Second); err != nil {
		t.Fatalf("Park: %v", err)
	}

	rep, err := reg.RosterReport()
	if err != nil {
		t.Fatalf("RosterReport: %v", err)
	}
	if len(rep.Absent) != 0 {
		t.Fatalf("a parked agent must not be a finding, got %v", rep.Absent)
	}
	if rep.Parked != 1 {
		t.Errorf("parked = %d, want 1", rep.Parked)
	}
	if memberByName(t, rep, "napper").State != RosterParked {
		t.Errorf("state = %q, want %q", memberByName(t, rep, "napper").State, RosterParked)
	}
}

// TestRosterReport_UnreadablePromptIsItsOwnClass: a prompt that exists and
// cannot be parsed is NOT quietly filed as on-demand. We know the agent was
// configured and we do not know what was wanted for it, and guessing in the
// direction of silence is the founding bug of this lineage.
func TestRosterReport_UnreadablePromptIsItsOwnClass(t *testing.T) {
	reg := newRosterTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "garbled", "+++\nauto_start = yes\n+++\n# garbled\n")

	rep, err := reg.RosterReport()
	if err != nil {
		t.Fatalf("RosterReport: %v", err)
	}
	m := memberByName(t, rep, "garbled")
	if m.Class != RosterUnclassifiable {
		t.Fatalf("class = %q, want %q", m.Class, RosterUnclassifiable)
	}
	if m.Error == "" {
		t.Error("an unclassifiable member must carry the parse error")
	}
	if !m.Absent() {
		t.Error("an unreadable prompt with no registry entry is still absent")
	}
}

// TestRosterReport_UnreadableTreeIsAnErrorNotACleanRoster: if we could not
// enumerate the configured set, we cannot say anybody is missing — and must not
// say nobody is.
func TestRosterReport_UnreadableTreeIsAnErrorNotACleanRoster(t *testing.T) {
	reg := newRosterTestRegistry(t)
	crew := CrewPromptDir()
	if err := os.Chmod(crew, 0o000); err != nil {
		t.Skipf("cannot chmod %s: %v", crew, err)
	}
	t.Cleanup(func() { _ = os.Chmod(crew, 0o755) })
	if _, err := os.ReadDir(crew); err == nil {
		t.Skip("this filesystem/user can still read a 0000 directory")
	}

	rep, err := reg.RosterReport()
	if err == nil {
		t.Fatalf("expected an error for an unreadable prompt tree, got a report: %+v", rep)
	}
	if rep.Complete() {
		t.Error("a failed read must never render as a complete roster")
	}
}

// TestRosterReport_NilRegistryIsAnError keeps the same rule at the other end:
// no registry is no judgement, not a clean one.
func TestRosterReport_NilRegistryIsAnError(t *testing.T) {
	var reg *Registry
	if _, err := reg.RosterReport(); err == nil {
		t.Fatal("expected ErrNoRosterJudgement from a nil registry")
	}
}

// TestRosterReport_EmptyIsNotComplete: zero configured agents is not a
// complete roster, and Render says so instead of printing the healthy line.
func TestRosterReport_EmptyIsNotComplete(t *testing.T) {
	reg := newRosterTestRegistry(t)

	rep, err := reg.RosterReport()
	if err != nil {
		t.Fatalf("RosterReport: %v", err)
	}
	if rep.Configured != 0 {
		t.Skipf("prompt tree was not empty (%d configured); nothing to assert", rep.Configured)
	}
	if rep.Complete() {
		t.Error("an empty roster must not report as complete")
	}
	if out := rep.Render(); !strings.Contains(out, "NOTHING CONFIGURED") {
		t.Errorf("Render() should say the roster was empty, got:\n%s", out)
	}
}

// TestRosterReport_RenderNamesTheDenominator: every branch reports how many
// agents there should have been. A reader's mistake is not misreading a row, it
// is not knowing how many rows to expect (drellem2/pogo#127, one detector over).
func TestRosterReport_RenderNamesTheDenominator(t *testing.T) {
	reg := newRosterTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "here", "+++\nauto_start = true\nrestart_on_crash = true\n+++\n# here\n")
	writePrompt(t, CrewPromptDir(), "gone", "+++\nauto_start = false\nrestart_on_crash = false\n+++\n# gone\n")
	if _, err := reg.StartCrewAgent("here"); err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}

	rep, err := reg.RosterReport()
	if err != nil {
		t.Fatalf("RosterReport: %v", err)
	}
	out := rep.Render()
	for _, want := range []string{"gone", "2 configured", "1 running", "1 absent", "auto_start = false"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "here ") {
		// A present agent is a member, not a finding: it must not be listed
		// under the absent heading.
		t.Errorf("Render() should not list the running agent as absent:\n%s", out)
	}
}

// TestRosterReport_MayorPromptCounts: the coordinator lives at the top of the
// prompt tree rather than in crew/, and it is the single agent whose absence
// matters most. Pin that it is a roster member.
func TestRosterReport_MayorPromptCounts(t *testing.T) {
	reg := newRosterTestRegistry(t)
	if err := os.WriteFile(filepath.Join(PromptDir(), "mayor.md"),
		[]byte("+++\nauto_start = true\nrestart_on_crash = true\n+++\n# mayor\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := reg.RosterReport()
	if err != nil {
		t.Fatalf("RosterReport: %v", err)
	}
	m := memberByName(t, rep, CoordinatorName())
	if m.Category != "mayor" {
		t.Errorf("category = %q, want mayor", m.Category)
	}
	if !m.Absent() {
		t.Errorf("an unstarted coordinator is absent, got state %q", m.State)
	}
}

// TestRosterHandler_ServesTheReportAndRoutesPastTheWildcard.
//
// Two things at once, and the second is the one that would break silently:
// /agents/roster must serve the report, and it must not be swallowed by the
// /agents/{name} wildcard registered beside it — which would answer
// `404 agent "roster" not found` for a healthy daemon.
func TestRosterHandler_ServesTheReportAndRoutesPastTheWildcard(t *testing.T) {
	reg := newRosterTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "absentee", "+++\nauto_start = true\nrestart_on_crash = true\n+++\n# absentee\n")

	mux := http.NewServeMux()
	reg.RegisterHandlers(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/agents/roster")
	if err != nil {
		t.Fatalf("GET /agents/roster: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the wildcard /agents/{name} route must not shadow this", res.StatusCode)
	}
	var got RosterReport
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Absent) != 1 || got.Absent[0].Name != "absentee" {
		t.Fatalf("absent = %+v, want the one configured, unstarted agent", got.Absent)
	}
	if got.Configured != 1 {
		t.Errorf("configured = %d, want 1 — the denominator must survive the wire", got.Configured)
	}
}

// TestRosterHandler_RejectsNonGET keeps the endpoint read-only. There is
// deliberately no seam here through which a caller could START an absent agent:
// the reason an agent left is the part worth knowing.
func TestRosterHandler_RejectsNonGET(t *testing.T) {
	reg := newRosterTestRegistry(t)
	rr := httptest.NewRecorder()
	reg.handleRoster(rr, httptest.NewRequest(http.MethodPost, "/agents/roster", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestRosterReport_CoupledFlagsAreReportedEvenWhileRunning is the config
// invariant with a live reader.
//
// mg-8677's defect is ARCHIVED and latent: no prompt in this fleet carries
// auto_start = true with restart_on_crash = false, so nothing is watching for it
// to become live. The embedded-prompt test enforces the rule over the prompts
// pogo SHIPS; a deployment's own ~/.pogo tree is out of that test's reach, and
// this is its reader.
//
// The RUNNING case is the one that matters: the fault the pairing predicts only
// lands after the agent next exits, so reporting it only on an absent agent would
// mean reporting it only once it is too late to be a warning.
func TestRosterReport_CoupledFlagsAreReportedEvenWhileRunning(t *testing.T) {
	reg := newRosterTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "halfsupervised", "+++\nauto_start = true\nrestart_on_crash = false\n+++\n# halfsupervised\n")
	if _, err := reg.StartCrewAgent("halfsupervised"); err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}

	rep, err := reg.RosterReport()
	if err != nil {
		t.Fatalf("RosterReport: %v", err)
	}
	if len(rep.Absent) != 0 {
		t.Fatalf("the agent is running; it must not be an absence: %v", rep.Absent)
	}
	if len(rep.Coupled) != 1 || rep.Coupled[0].Name != "halfsupervised" {
		t.Fatalf("coupled = %+v, want the one agent with the forbidden pairing", rep.Coupled)
	}
	if !strings.Contains(rep.Coupled[0].LifecycleWarning, "mg-8677") {
		t.Errorf("the warning must cite the defect it predicts, got %q", rep.Coupled[0].LifecycleWarning)
	}
	out := rep.Render()
	if !strings.Contains(out, "COUPLED") || !strings.Contains(out, "halfsupervised") {
		t.Errorf("Render() must surface the coupling on a COMPLETE roster too, got:\n%s", out)
	}
}

// TestRosterReport_HealthyFlagCombinationsAreNotWarnedAbout. A warning that
// fires on the shapes every healthy agent already has is one people learn to
// skip, which is how a real one gets missed.
func TestRosterReport_HealthyFlagCombinationsAreNotWarnedAbout(t *testing.T) {
	reg := newRosterTestRegistry(t)
	// Both-true (every healthy crew agent), doctor's on-demand pair,
	// representative's shape, and auto_start alone — which INHERITS the crew
	// always-on default and is therefore already both-true.
	writePrompt(t, CrewPromptDir(), "bothtrue", "+++\nauto_start = true\nrestart_on_crash = true\n+++\n# x\n")
	writePrompt(t, CrewPromptDir(), "ondemand", "+++\nauto_start = false\nrestart_on_crash = false\n+++\n# x\n")
	writePrompt(t, CrewPromptDir(), "repshape", "+++\nauto_start = false\nrestart_on_crash = true\n+++\n# x\n")
	writePrompt(t, CrewPromptDir(), "inherits", "+++\nauto_start = true\n+++\n# x\n")

	rep, err := reg.RosterReport()
	if err != nil {
		t.Fatalf("RosterReport: %v", err)
	}
	if len(rep.Coupled) != 0 {
		t.Fatalf("no healthy combination may warn, got %+v", rep.Coupled)
	}
	if strings.Contains(rep.Render(), "COUPLED") {
		t.Errorf("Render() warned over a clean tree:\n%s", rep.Render())
	}
}
