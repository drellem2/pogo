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
)

// fakePauser is a SchedulePauser recorder for park/wake tests.
type fakePauser struct {
	pauseAliases []string
	pauseReturn  []json.RawMessage
	restored     [][]json.RawMessage
}

func (f *fakePauser) PauseForAgent(aliases ...string) ([]json.RawMessage, error) {
	f.pauseAliases = append([]string(nil), aliases...)
	return f.pauseReturn, nil
}

func (f *fakePauser) RestoreForAgent(entries []json.RawMessage) (int, error) {
	f.restored = append(f.restored, entries)
	return len(entries), nil
}

// newParkTestRegistry sets up an isolated HOME, prompt dirs, and a registry
// whose crew agents spawn as plain `cat` processes.
func newParkTestRegistry(t *testing.T) *Registry {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
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

// TestPark_StopsRunningAgentAndPersistsFlag covers the core park contract:
// the process is stopped, the registry entry is removed (no supervisor
// respawn pending), and the park flag survives on disk.
func TestPark_StopsRunningAgentAndPersistsFlag(t *testing.T) {
	reg := newParkTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "sleepy", "+++\nrestart_on_crash = true\n+++\n# sleepy\n")

	a, err := reg.StartCrewAgent("sleepy")
	if err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}
	if !a.RestartOnCrash {
		t.Fatal("test premise: agent should be restart_on_crash=true")
	}

	paused, err := reg.Park("sleepy", 2*time.Second)
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	if paused != 0 {
		t.Errorf("paused = %d, want 0 (no pauser installed)", paused)
	}
	if !IsParked("sleepy") {
		t.Error("IsParked = false after Park")
	}
	if reg.Get("sleepy") != nil {
		t.Error("agent still registered after Park; a pending respawn could resurrect it")
	}
	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("agent process did not exit after Park")
	}
}

// TestPark_NotRunningAgent verifies parking a dormant agent works (the flag
// still gates autostart) but a name with no crew prompt is refused.
func TestPark_NotRunningAgent(t *testing.T) {
	reg := newParkTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "lurker", "+++\nauto_start = false\n+++\n# lurker\n")

	if _, err := reg.Park("lurker", time.Second); err != nil {
		t.Fatalf("Park of stopped crew agent: %v", err)
	}
	if !IsParked("lurker") {
		t.Error("IsParked = false after parking a stopped agent")
	}

	if _, err := reg.Park("ghost", time.Second); err == nil {
		t.Error("Park of unknown agent should fail (no crew prompt)")
	}
}

// TestPark_RefusesPolecat verifies park applies to crew agents only.
func TestPark_RefusesPolecat(t *testing.T) {
	reg := newParkTestRegistry(t)
	if _, err := reg.Spawn(SpawnRequest{Name: "cat1", Type: TypePolecat, Command: []string{"cat"}}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	_, err := reg.Park("cat1", time.Second)
	if err == nil || !strings.Contains(err.Error(), "polecat") {
		t.Errorf("Park(polecat) error = %v, want polecat refusal", err)
	}
	if IsParked("cat1") {
		t.Error("polecat must not end up with a park flag")
	}
}

// TestParkWake_SchedulesRoundTrip verifies park records the paused schedules
// in the park file and wake hands them back for restoration, clears the flag,
// and restarts the agent.
func TestParkWake_SchedulesRoundTrip(t *testing.T) {
	reg := newParkTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "pm-idle", "+++\nrestart_on_crash = true\n+++\n# pm\n")

	entries := []json.RawMessage{
		json.RawMessage(`{"id":"daily-sweep","agent":"crew-pm-idle","cron":"0 9 * * *"}`),
		json.RawMessage(`{"id":"mail-check-pm-idle","agent":"pm-idle","cron":"*/10 * * * *"}`),
	}
	pauser := &fakePauser{pauseReturn: entries}
	reg.SetSchedulePauser(pauser)

	if _, err := reg.StartCrewAgent("pm-idle"); err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}

	paused, err := reg.Park("pm-idle", 2*time.Second)
	if err != nil {
		t.Fatalf("Park: %v", err)
	}
	if paused != len(entries) {
		t.Errorf("paused = %d, want %d", paused, len(entries))
	}
	wantAliases := []string{"pm-idle", "crew-pm-idle"}
	if len(pauser.pauseAliases) != 2 || pauser.pauseAliases[0] != wantAliases[0] || pauser.pauseAliases[1] != wantAliases[1] {
		t.Errorf("pause aliases = %v, want %v", pauser.pauseAliases, wantAliases)
	}

	// The recorded schedules must be in the on-disk park file (that is what
	// survives a pogod restart).
	st, err := ReadParkState("pm-idle")
	if err != nil || st == nil {
		t.Fatalf("ReadParkState: st=%v err=%v", st, err)
	}
	if len(st.Schedules) != len(entries) {
		t.Fatalf("park file records %d schedules, want %d", len(st.Schedules), len(entries))
	}

	a, restored, err := reg.Wake("pm-idle")
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if restored != len(entries) {
		t.Errorf("restored = %d, want %d", restored, len(entries))
	}
	if len(pauser.restored) != 1 || len(pauser.restored[0]) != len(entries) {
		t.Errorf("RestoreForAgent calls = %v, want one call with %d entries", pauser.restored, len(entries))
	}
	if IsParked("pm-idle") {
		t.Error("IsParked = true after Wake")
	}
	if a == nil || a.GetStatus() != StatusRunning {
		t.Errorf("agent not running after Wake: %+v", a)
	}
	if got := reg.Get("pm-idle"); got == nil {
		t.Error("agent not registered after Wake")
	}
}

// TestWake_NotParked verifies waking an unparked agent is an explicit error.
func TestWake_NotParked(t *testing.T) {
	reg := newParkTestRegistry(t)
	_, _, err := reg.Wake("nobody")
	if err == nil || !strings.Contains(err.Error(), "not parked") {
		t.Errorf("Wake(unparked) error = %v, want 'not parked'", err)
	}
}

// TestShouldRespawn_SuppressedWhenParked covers the supervisor-side check the
// pogod OnExit hook uses: restart_on_crash respawns unless a park flag is on
// disk at exit time.
func TestShouldRespawn_SuppressedWhenParked(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	a := &Agent{Name: "sleepy", RestartOnCrash: true}
	if !a.ShouldRespawn() {
		t.Error("ShouldRespawn = false for unparked restart_on_crash agent")
	}
	if err := writeParkState(&ParkState{Name: "sleepy", ParkedAt: time.Now()}); err != nil {
		t.Fatalf("writeParkState: %v", err)
	}
	if a.ShouldRespawn() {
		t.Error("ShouldRespawn = true for parked agent")
	}
	if (&Agent{Name: "other", RestartOnCrash: false}).ShouldRespawn() {
		t.Error("ShouldRespawn = true for restart_on_crash=false agent")
	}
}

// TestOnExit_SeesParkFlagBeforeExit verifies the ordering guarantee that
// makes park race-free: the flag is written before the stop signal, so by the
// time the OnExit callback fires, ShouldRespawn is already false.
func TestOnExit_SeesParkFlagBeforeExit(t *testing.T) {
	reg := newParkTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "sleepy", "+++\nrestart_on_crash = true\n+++\n# sleepy\n")

	respawnDecision := make(chan bool, 1)
	reg.SetOnExit(func(a *Agent, err error) {
		respawnDecision <- a.ShouldRespawn()
	})

	if _, err := reg.StartCrewAgent("sleepy"); err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}
	if _, err := reg.Park("sleepy", 2*time.Second); err != nil {
		t.Fatalf("Park: %v", err)
	}

	select {
	case shouldRespawn := <-respawnDecision:
		if shouldRespawn {
			t.Error("OnExit saw ShouldRespawn=true during park; supervisor would respawn the parked agent")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnExit never fired")
	}
}

// TestRespawn_RefusesParked covers the backstop for a respawn goroutine
// scheduled before the park flag landed: Respawn itself must check the flag.
func TestRespawn_RefusesParked(t *testing.T) {
	reg := newParkTestRegistry(t)
	a, err := reg.Spawn(SpawnRequest{
		Name:           "sleepy",
		Type:           TypeCrew,
		Command:        []string{"cat"},
		RestartOnCrash: true,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Crash the process (not via Stop, so the registry entry stays — the
	// state a pending OnExit respawn goroutine would find).
	if err := a.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not exit after kill")
	}

	if err := writeParkState(&ParkState{Name: "sleepy", ParkedAt: time.Now()}); err != nil {
		t.Fatalf("writeParkState: %v", err)
	}
	if _, err := reg.Respawn("sleepy"); err == nil || !strings.Contains(err.Error(), "parked") {
		t.Errorf("Respawn(parked) error = %v, want parked refusal", err)
	}
}

// TestListParked enumerates park flags on disk.
func TestListParked(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if got, err := ListParked(); err != nil || len(got) != 0 {
		t.Fatalf("ListParked on empty home = %v, %v; want empty, nil", got, err)
	}
	for _, name := range []string{"pm-a", "pm-b"} {
		if err := writeParkState(&ParkState{Name: name, ParkedAt: time.Now()}); err != nil {
			t.Fatalf("writeParkState(%s): %v", name, err)
		}
	}
	got, err := ListParked()
	if err != nil {
		t.Fatalf("ListParked: %v", err)
	}
	if len(got) != 2 || got[0].Name != "pm-a" || got[1].Name != "pm-b" {
		t.Errorf("ListParked = %+v, want pm-a, pm-b", got)
	}
}

// newAgentsMux returns a mux with the registry's handlers registered, so
// requests resolve {name} path values the same way pogod's mux does.
func newAgentsMux(reg *Registry) *http.ServeMux {
	mux := http.NewServeMux()
	reg.RegisterHandlers(mux)
	return mux
}

// TestHandleAgents_ListsParked verifies GET /agents surfaces parked agents
// with status=parked so the mayor's stall-watch can skip them mechanically.
func TestHandleAgents_ListsParked(t *testing.T) {
	reg := newParkTestRegistry(t)
	if err := writeParkState(&ParkState{Name: "pm-idle", ParkedAt: time.Now()}); err != nil {
		t.Fatalf("writeParkState: %v", err)
	}

	rr := httptest.NewRecorder()
	newAgentsMux(reg).ServeHTTP(rr, httptest.NewRequest("GET", "/agents", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agents status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var infos []AgentInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &infos); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found *AgentInfo
	for i := range infos {
		if infos[i].Name == "pm-idle" {
			found = &infos[i]
		}
	}
	if found == nil {
		t.Fatalf("parked agent missing from /agents: %+v", infos)
	}
	if found.Status != StatusParked {
		t.Errorf("status = %q, want %q", found.Status, StatusParked)
	}
	if found.ParkedAt == "" {
		t.Error("parked entry has no parked_at timestamp")
	}
}

// TestHandleStart_RefusesParked verifies a parked agent cannot be plain-started
// (which would leave the park flag silently suppressing the next respawn).
func TestHandleStart_RefusesParked(t *testing.T) {
	reg := newParkTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "pm-idle", "+++\n+++\n# pm\n")
	if err := writeParkState(&ParkState{Name: "pm-idle", ParkedAt: time.Now()}); err != nil {
		t.Fatalf("writeParkState: %v", err)
	}

	body := strings.NewReader(`{"name":"pm-idle"}`)
	rr := httptest.NewRecorder()
	newAgentsMux(reg).ServeHTTP(rr, httptest.NewRequest("POST", "/agents/start", body))
	if rr.Code != http.StatusConflict {
		t.Fatalf("start of parked agent status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "wake") {
		t.Errorf("error should point at wake: %s", rr.Body.String())
	}
	if reg.Get("pm-idle") != nil {
		t.Error("parked agent was started anyway")
	}
}

// TestParkWakeHandlers_EndToEnd drives the park and wake HTTP endpoints the
// way the CLI does.
func TestParkWakeHandlers_EndToEnd(t *testing.T) {
	reg := newParkTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "pm-idle", "+++\nrestart_on_crash = true\n+++\n# pm\n")
	pauser := &fakePauser{pauseReturn: []json.RawMessage{json.RawMessage(`{"id":"s1","agent":"crew-pm-idle","cron":"0 9 * * *"}`)}}
	reg.SetSchedulePauser(pauser)
	mux := newAgentsMux(reg)

	if _, err := reg.StartCrewAgent("pm-idle"); err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/agents/pm-idle/park", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("park status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var parkResp ParkAPIResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &parkResp); err != nil {
		t.Fatalf("decode park response: %v", err)
	}
	if parkResp.Status != "parked" || parkResp.SchedulesPaused != 1 {
		t.Errorf("park response = %+v, want parked with 1 schedule", parkResp)
	}
	if !IsParked("pm-idle") {
		t.Error("agent not parked after POST /park")
	}

	// Waking an unparked name is a 409.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/agents/nobody/wake", nil))
	if rr.Code != http.StatusConflict {
		t.Errorf("wake of unparked agent status = %d, want 409", rr.Code)
	}

	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/agents/pm-idle/wake", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("wake status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var wakeResp WakeAPIResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &wakeResp); err != nil {
		t.Fatalf("decode wake response: %v", err)
	}
	if wakeResp.Status != "woken" || wakeResp.SchedulesRestored != 1 || wakeResp.PID <= 0 {
		t.Errorf("wake response = %+v, want woken with 1 schedule and a pid", wakeResp)
	}
	if IsParked("pm-idle") {
		t.Error("agent still parked after POST /wake")
	}
}

// TestStartCrewAgent_StubRestartOverride drives the fix end-to-end through
// real extends synthesis: a PM-tier stub declaring restart_on_crash = false
// must win over the template's restart_on_crash = true, which before mg-41e1
// was resolved from the synthesized prompt and silently ignored the stub.
func TestStartCrewAgent_StubRestartOverride(t *testing.T) {
	reg := newParkTestRegistry(t)

	pmDir := filepath.Join(PromptDir(), "pm")
	if err := os.MkdirAll(pmDir, 0755); err != nil {
		t.Fatalf("mkdir pm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pmDir, "pm-template.md"), []byte("+++\nauto_start = true\nrestart_on_crash = true\n+++\n# PM template\n"), 0644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pmDir, "idle.toml"), []byte("name = \"pm-idle\"\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	writePrompt(t, CrewPromptDir(), "pm-idle", "+++\nauto_start = true\nrestart_on_crash = false\n+++\n\nextends pm-template with config pm/idle.toml\n")

	a, err := reg.StartCrewAgent("pm-idle")
	if err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}
	if !strings.Contains(a.PromptFile, "synthesized") {
		t.Fatalf("test premise: prompt should be synthesized, got %s", a.PromptFile)
	}
	if a.RestartOnCrash {
		t.Error("stub restart_on_crash=false ignored: synthesized template frontmatter won")
	}
}

// TestResolveRestartOnCrashWithStub verifies the stub's explicit
// restart_on_crash overrides the synthesized prompt's (which inherits the
// shared template frontmatter, always true for the PM tier).
func TestResolveRestartOnCrashWithStub(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	synthTrue := write("synth.md", "+++\nrestart_on_crash = true\n+++\n# synthesized\n")
	stubFalse := write("stub-false.md", "+++\nrestart_on_crash = false\n+++\nextends pm-template with config pm/x.toml\n")
	stubSilent := write("stub-silent.md", "+++\nauto_start = true\n+++\nextends pm-template with config pm/x.toml\n")

	if got := ResolveRestartOnCrashWithStub(stubFalse, synthTrue, TypeCrew); got {
		t.Error("stub restart_on_crash=false must override synthesized true")
	}
	if got := ResolveRestartOnCrashWithStub(stubSilent, synthTrue, TypeCrew); !got {
		t.Error("silent stub must fall through to synthesized prompt's true")
	}
	// No synthesis (stub == prompt): behaves exactly like ResolveRestartOnCrash.
	if got := ResolveRestartOnCrashWithStub(stubFalse, stubFalse, TypeCrew); got {
		t.Error("unsynthesized prompt with restart_on_crash=false must resolve false")
	}
	if got := ResolveRestartOnCrashWithStub("", synthTrue, TypeCrew); !got {
		t.Error("empty stub must fall through to prompt resolution")
	}
}
