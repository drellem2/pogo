package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/scheduler"
)

// sandboxPogoHome points POGO_HOME (and HOME, which POGO_HOME defaults off of)
// at a temp dir and lays out the prompt tree, so a test can declare a desired
// state on disk without touching the machine's live fleet. This box exports
// POGO_HOME=$HOME from a stale shell profile, so setting HOME alone is not
// enough — both must be pinned.
func sandboxPogoHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	if err := agent.InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	return home
}

// writeCrewPrompt declares a crew agent in the desired state (or out of it).
func writeCrewPrompt(t *testing.T, name string, autoStart bool) {
	t.Helper()
	flag := "false"
	if autoStart {
		flag = "true"
	}
	path := filepath.Join(agent.CrewPromptDir(), name+".md")
	body := "+++\nauto_start = " + flag + "\n+++\n# " + name + "\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write crew prompt %s: %v", path, err)
	}
}

// TestPogodRestartKeepsCrewMailChecksAndReapsPolecats is THE acceptance test
// for mg-de08 — the one the whole design lives or dies on.
//
// It reconstructs the exact state a freshly-restarted pogod wakes up in: the
// fleet's mail-check-* schedules loaded from disk, and an EMPTY registry,
// because the successor process has not adopted (and cannot adopt) its
// predecessor's children, and its AutoStartAgents() sweep has not run yet. The
// old registry-only liveness answered "gone" for every agent in that state and
// reaped the entire fleet's mail loop within one heartbeat — a ~2h silent
// fleet-wide mail outage on every redeploy.
//
// It asserts BOTH directions, and both are load-bearing:
//
//  1. every auto_start crew agent's mail-check SURVIVES the first N ticks, and
//  2. a polecat's mail-check in the SAME window is STILL REAPED.
//
// (2) is not decoration. A build that simply never reaps anything would pass
// (1) and would have moved the bug rather than fixed it: orphan mail-checks
// firing at a dead polecat forever is the defect the reap exists to prevent.
// The line this test pins is exactly the desired-state line — crew are
// auto_start (EXPECTED → keep), polecats are not (GONE → reap).
func TestPogodRestartKeepsCrewMailChecksAndReapsPolecats(t *testing.T) {
	sandboxPogoHome(t)

	// The desired state: two auto_start PMs, plus a crew prompt that is NOT
	// auto_start (never expected to be running — its mail-check is as dead as
	// a polecat's).
	writeCrewPrompt(t, "pm-pogo", true)
	writeCrewPrompt(t, "pm-dealdesk", true)
	writeCrewPrompt(t, "lurker", false)

	now := time.Date(2026, 7, 17, 2, 0, 18, 0, time.UTC) // the live bounce
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}

	// The schedules that survived pogod's shutdown on disk. Crew register
	// under their bare name; polecats under their bare id. Both forms exist in
	// the live fleet, so both are covered here.
	add := func(agentName, id string) {
		t.Helper()
		if _, err := s.Add(scheduler.Entry{Agent: agentName, ID: id, Cron: "*/10 * * * *"}, now); err != nil {
			t.Fatalf("Add %s/%s: %v", agentName, id, err)
		}
	}
	add("pm-pogo", scheduler.MailCheckIDPrefix+"pm-pogo")
	add("pm-dealdesk", scheduler.MailCheckIDPrefix+"pm-dealdesk")
	add("crew-pm-pogo", scheduler.MailCheckIDPrefix+"pm-pogo-alias") // event-identity form
	add("lurker", scheduler.MailCheckIDPrefix+"lurker")
	add("de08", scheduler.MailCheckIDPrefix+"mg-de08") // a polecat
	// A crew sweep survives the reap because it is not a mail-check. This is
	// the schedule that made the outage quiet — the agent still LOOKED
	// scheduled. Assert it is untouched so the reap stays scoped.
	if _, err := s.Add(scheduler.Entry{Agent: "crew-pm-pogo", ID: "sweep-morning", Cron: "0 9 * * *"}, now); err != nil {
		t.Fatalf("Add sweep: %v", err)
	}

	// The successor pogod: registry constructed, EMPTY — no agent has been
	// adopted, no auto-start sweep has run.
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if got := len(reg.List()); got != 0 {
		t.Fatalf("precondition: registry has %d agents, want 0 (the restart state)", got)
	}
	s.SetLiveness(registryLiveness{reg: reg})

	// Part B's gate is opened here: this test is about Part A's invariant, and
	// an unopened gate would keep the sweep from running at all — which would
	// pass assertion (1) for the wrong reason and never exercise (2). Gate
	// behaviour has its own test (TestStartupGCGate).
	s.SetGCGate(func(time.Time) bool { return true })

	// Tick through the window the fleet was reaped in — every heartbeat for
	// the first ~10 minutes of the successor's life, spanning several fire
	// intervals.
	for i := 1; i <= 20; i++ {
		s.Tick(context.Background(), now.Add(time.Duration(i)*30*time.Second))
	}

	survived := map[string]bool{}
	for _, e := range s.List("") {
		survived[e.Agent+"/"+e.ID] = true
	}

	// (1) The fleet's mail loop is intact.
	for _, want := range []string{
		"pm-pogo/" + scheduler.MailCheckIDPrefix + "pm-pogo",
		"pm-dealdesk/" + scheduler.MailCheckIDPrefix + "pm-dealdesk",
		"crew-pm-pogo/" + scheduler.MailCheckIDPrefix + "pm-pogo-alias",
	} {
		if !survived[want] {
			t.Errorf("REGRESSION (mg-de08): auto_start crew mail-check %q was reaped across a pogod restart", want)
		}
	}
	if !survived["crew-pm-pogo/sweep-morning"] {
		t.Error("non-mail-check sweep was reaped; the GC must stay scoped to the mail-check prefix")
	}

	// (2) The GONE direction still works — or we moved the bug rather than
	// fixing it.
	if survived["de08/"+scheduler.MailCheckIDPrefix+"mg-de08"] {
		t.Error("polecat mail-check SURVIVED the sweep: a polecat is not in the desired state and must still be reaped as agent_gone (orphan-nudge prevention)")
	}
	if survived["lurker/"+scheduler.MailCheckIDPrefix+"lurker"] {
		t.Error("non-auto_start crew mail-check SURVIVED the sweep: an agent pogod never starts is not expected, and must still be reaped")
	}
}

// TestRegistryLiveness_AgentState pins the tri-state directly: each answer, and
// the reason for it.
func TestRegistryLiveness_AgentState(t *testing.T) {
	sandboxPogoHome(t)
	writeCrewPrompt(t, "pm-pogo", true)
	writeCrewPrompt(t, "parked-pm", true)
	writeCrewPrompt(t, "lurker", false)

	// A parked agent is deliberately dormant across pogod restarts (mg-41e1),
	// so it is NOT in the desired state — park overrides auto_start, exactly as
	// it does in AutoStartAgents. Same predicate, so it cannot drift. The flag
	// is written directly: Registry.Park stops a running agent, and this one is
	// parked from a previous pogod's lifetime.
	parkPath := agent.ParkFilePath("parked-pm")
	if err := os.MkdirAll(filepath.Dir(parkPath), 0755); err != nil {
		t.Fatalf("mkdir park dir: %v", err)
	}
	if err := os.WriteFile(parkPath, []byte(`{"name":"parked-pm"}`), 0644); err != nil {
		t.Fatalf("write park flag: %v", err)
	}

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	l := registryLiveness{reg: reg}

	cases := []struct {
		name     string
		identity string
		want     scheduler.AgentState
		why      string
	}{
		{"unregistered auto_start crew (the mg-de08 case)", "pm-pogo", scheduler.AgentExpected,
			"an empty registry is UNKNOWN, not evidence of death — this is the whole ticket"},
		{"unregistered auto_start crew, event identity", "crew-pm-pogo", scheduler.AgentExpected,
			"schedules address crew both ways; both must resolve to the same answer"},
		{"parked crew", "parked-pm", scheduler.AgentGone,
			"park overrides auto_start: a parked agent is not in the desired state"},
		{"crew prompt without auto_start", "lurker", scheduler.AgentGone,
			"pogod never starts it, so nothing is expected back"},
		{"polecat", "cat-de08", scheduler.AgentGone,
			"polecats have no auto_start prompt — the GONE direction that must not regress"},
		{"agent that does not exist at all", "ghost", scheduler.AgentGone,
			"no prompt, no process, no claim on being expected"},
	}
	for _, tc := range cases {
		if got := l.AgentState(tc.identity); got != tc.want {
			t.Errorf("%s: AgentState(%q) = %v, want %v — %s", tc.name, tc.identity, got, tc.want, tc.why)
		}
	}
}

// TestRegistryLiveness_UnreadablePromptIsUnknownNotGone pins the fail-safe
// edge: a crew prompt that EXISTS but cannot be parsed tells us the agent was
// configured and nothing else. That is UNKNOWN, and the reap must decline to
// act on it. Reading a broken prompt as "not in the desired state" would turn
// a corrupt file into a silent mail outage for that agent — mg-de08's failure
// mode wearing a different hat.
func TestRegistryLiveness_UnreadablePromptIsUnknownNotGone(t *testing.T) {
	sandboxPogoHome(t)

	// A prompt whose frontmatter is malformed: the delimiter opens, the TOML
	// inside does not parse.
	broken := filepath.Join(agent.CrewPromptDir(), "pm-broken.md")
	if err := os.WriteFile(broken, []byte("+++\nauto_start = = true\n+++\n# pm-broken\n"), 0644); err != nil {
		t.Fatalf("write broken prompt: %v", err)
	}

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	l := registryLiveness{reg: reg}

	if got := l.AgentState("pm-broken"); got != scheduler.AgentUnknown {
		t.Errorf("AgentState(pm-broken) = %v, want %v — an unparseable prompt is not evidence of death", got, scheduler.AgentUnknown)
	}

	// ...and UNKNOWN does not reap. This is the assertion that matters; the one
	// above only names the reason.
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	now := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	if _, err := s.Add(scheduler.Entry{
		Agent: "pm-broken", ID: scheduler.MailCheckIDPrefix + "pm-broken", Cron: "*/10 * * * *",
	}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Positive control in the same scheduler: a genuinely GONE agent's
	// mail-check, which MUST be reaped by the same sweep. Without it, "the
	// broken prompt's schedule survived" would also be true of a sweep that
	// did nothing at all.
	if _, err := s.Add(scheduler.Entry{
		Agent: "de08", ID: scheduler.MailCheckIDPrefix + "mg-de08", Cron: "*/10 * * * *",
	}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	s.SetLiveness(l)
	s.SetGCGate(func(time.Time) bool { return true })

	if n := s.GCStaleMailChecks(now); n != 1 {
		t.Fatalf("GC reaped %d, want exactly 1 (the polecat, not the unclassifiable agent)", n)
	}
	if _, ok := s.Get("pm-broken", scheduler.MailCheckIDPrefix+"pm-broken"); !ok {
		t.Error("an agent with an unparseable prompt had its mail-check reaped; UNKNOWN must not reap")
	}
	if _, ok := s.Get("de08", scheduler.MailCheckIDPrefix+"mg-de08"); ok {
		t.Error("positive control: the polecat's mail-check survived, so this sweep proves nothing")
	}
}

// TestStartupGCGate covers PART B: the reap stays shut until pogod's first
// auto-start sweep has completed AND the settle window has elapsed.
func TestStartupGCGate(t *testing.T) {
	now := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	g := newStartupGCGate(30 * time.Second)

	// Before the sweep the gate is shut no matter how much time passes: the
	// missing input is the desired state, not the clock.
	if g.open(now) {
		t.Error("gate open before the auto-start sweep ran")
	}
	if g.open(now.Add(time.Hour)) {
		t.Error("gate opened by the passage of time alone; it must wait for the auto-start sweep")
	}

	g.markAutoStartComplete(now)

	if g.open(now) {
		t.Error("gate open immediately after the sweep; the settle window must elapse first")
	}
	if g.open(now.Add(29 * time.Second)) {
		t.Error("gate open inside the settle window")
	}
	if !g.open(now.Add(30 * time.Second)) {
		t.Error("gate still shut at the settle boundary")
	}
	if !g.open(now.Add(time.Hour)) {
		t.Error("gate shut long after settling; it must open permanently, not blink")
	}

	// A later sweep must not re-close the gate or push the deadline out: it
	// opens once and stays open.
	g.markAutoStartComplete(now.Add(time.Hour))
	if !g.open(now.Add(time.Hour)) {
		t.Error("a repeat markAutoStartComplete re-armed the settle window; the first sweep is the one that counts")
	}
}

// TestGCGateBlocksReapDuringStartupWindow proves the gate is wired to the sweep
// and that it protects the reap's inputs — including the case Part A cannot
// cover on its own: a schedule whose target is GONE is also held during the
// startup window, because during that window pogod cannot yet tell gone from
// merely-not-loaded. It fails safe: a delayed reap is harmless, a premature one
// is the outage.
func TestGCGateBlocksReapDuringStartupWindow(t *testing.T) {
	sandboxPogoHome(t)

	now := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	if _, err := s.Add(scheduler.Entry{
		Agent: "de08", ID: scheduler.MailCheckIDPrefix + "mg-de08", Cron: "*/10 * * * *",
	}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	s.SetLiveness(registryLiveness{reg: reg})

	g := newStartupGCGate(30 * time.Second)
	s.SetGCGate(g.open)

	if n := s.GCStaleMailChecks(now); n != 0 {
		t.Fatalf("GC reaped %d before the gate opened, want 0", n)
	}

	g.markAutoStartComplete(now)
	if n := s.GCStaleMailChecks(now.Add(10 * time.Second)); n != 0 {
		t.Fatalf("GC reaped %d inside the settle window, want 0", n)
	}

	// Positive control: once the gate opens the same GONE schedule IS reaped.
	// Without this the assertions above would also pass on a scheduler that
	// never reaps anything.
	if n := s.GCStaleMailChecks(now.Add(31 * time.Second)); n != 1 {
		t.Fatalf("GC reaped %d after the gate opened, want 1 (positive control: the gate must delay the reap, not disable it)", n)
	}
}

// writeCrewPromptFull declares a crew agent with BOTH lifecycle keys set
// explicitly — the shape the mg-8677 corpse case needs (auto_start = true,
// restart_on_crash = false).
func writeCrewPromptFull(t *testing.T, name string, autoStart, restartOnCrash bool) {
	t.Helper()
	path := filepath.Join(agent.CrewPromptDir(), name+".md")
	body := fmt.Sprintf("+++\nauto_start = %t\nrestart_on_crash = %t\n+++\n# %s\n",
		autoStart, restartOnCrash, name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write crew prompt %s: %v", path, err)
	}
}

// spawnCorpse registers an agent, lets its process exit, and returns once the
// registry holds a terminally-exited entry for it. waitAndHandle does NOT
// remove the entry (only Stop and the onExit hook do), so this is precisely the
// state the sweep must classify: a REGISTERED body.
func spawnCorpse(t *testing.T, reg *agent.Registry, name string, restartOnCrash bool) *agent.Agent {
	t.Helper()
	a, err := reg.Spawn(agent.SpawnRequest{
		Name:           name,
		Type:           agent.TypeCrew,
		Command:        []string{"sh", "-c", "exit 0"},
		RestartOnCrash: restartOnCrash,
	})
	if err != nil {
		t.Fatalf("Spawn %s: %v", name, err)
	}
	select {
	case <-a.Done():
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never exited", name)
	}
	// Precondition, asserted rather than assumed: the corpse is still
	// REGISTERED. If the registry dropped the entry, this test would be
	// exercising the unregistered path and could never see the mg-8677 defect.
	found := false
	for _, e := range reg.List() {
		if e.Name == name {
			found = true
		}
	}
	if !found {
		t.Fatalf("precondition: %s is not in the registry after exit; this test cannot see the corpse path", name)
	}
	if a.Alive() {
		t.Fatalf("precondition: %s still alive after Done()", name)
	}
	return a
}

// TestRegistryLiveness_AutoStartNeverOverridesACorpse is THE acceptance test
// for mg-8677 — the precedence rule, stated as an assertion.
//
//	Consult desired state ONLY when the registry yields NO evidence.
//	Evidence beats expectation, always. Never let auto_start override a corpse.
//
// A REGISTERED, terminally-exited, restart_on_crash=false agent IS positive
// evidence of death — the registry looked and found a body. Before this fix it
// fell through to DesiredStateFor and came back EXPECTED on the strength of
// auto_start = true, so its mail-check survived forever, firing at a corpse and
// accumulating unbounded scheduler_fire_failed noise.
//
// The defect was LATENT on the live fleet only because no prompt paired
// auto_start = true with restart_on_crash = false — one word on crew/doctor.md
// away, which is why the case is pinned here rather than left to the fleet.
func TestRegistryLiveness_AutoStartNeverOverridesACorpse(t *testing.T) {
	sandboxPogoHome(t)

	// The arming pair: "start me at boot, don't respawn me if I die" — an
	// entirely reasonable prompt (it is half-written on crew/doctor.md today).
	writeCrewPromptFull(t, "pm-corpse", true, false)
	// The control that must NOT change: auto_start + restart_on_crash, the
	// shape all 12 live auto_start prompts have. Its registry entry is held
	// through a mid-restart window and must still read ALIVE.
	writeCrewPromptFull(t, "pm-restarter", true, true)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	l := registryLiveness{reg: reg}

	spawnCorpse(t, reg, "pm-corpse", false)
	spawnCorpse(t, reg, "pm-restarter", true)

	// Sanity: the desired state really does say "expected" for the corpse. If
	// this ever stops holding, the test below would pass for the wrong reason
	// — GONE would be trivially correct and the precedence rule untested.
	expected, err := agent.DesiredStateFor("pm-corpse")
	if err != nil || !expected {
		t.Fatalf("precondition: DesiredStateFor(pm-corpse) = %v, %v; want true, nil — "+
			"the whole point is that desired state SAYS expected and must lose to the registry anyway", expected, err)
	}

	if got := l.AgentState("pm-corpse"); got != scheduler.AgentGone {
		t.Errorf("AgentState(pm-corpse) = %v, want %v — a registered, terminally-exited, "+
			"restart_on_crash=false agent is positive evidence of death; auto_start must not override a corpse (mg-8677)", got, scheduler.AgentGone)
	}
	if got := l.AgentState("crew-pm-corpse"); got != scheduler.AgentGone {
		t.Errorf("AgentState(crew-pm-corpse) = %v, want %v — the event-identity form must resolve identically", got, scheduler.AgentGone)
	}
	// mg-de08's invariant, unmoved: a restart_on_crash entry the registry
	// still holds is a mid-restart window, not a corpse.
	if got := l.AgentState("pm-restarter"); got != scheduler.AgentAlive {
		t.Errorf("REGRESSION (mg-de08): AgentState(pm-restarter) = %v, want %v — "+
			"a restart_on_crash agent mid-respawn must not be reaped", got, scheduler.AgentAlive)
	}
	// ...and the unregistered auto_start agent still resolves via the desired
	// state. The fix narrows WHEN desired state is consulted; it must not stop
	// it being consulted where it is the only source.
	writeCrewPromptFull(t, "pm-unregistered", true, false)
	if got := l.AgentState("pm-unregistered"); got != scheduler.AgentExpected {
		t.Errorf("REGRESSION (mg-de08): AgentState(pm-unregistered) = %v, want %v — "+
			"an empty registry is UNKNOWN, so the desired state must still answer", got, scheduler.AgentExpected)
	}
}

// TestGCReapsCorpseMailCheckDespiteAutoStart drives the same defect through the
// real sweep, because AgentState returning GONE only matters if a schedule
// actually dies. This is the observable the ticket describes: the mail-check
// that "survives forever, accumulating scheduler_fire_failed noise".
func TestGCReapsCorpseMailCheckDespiteAutoStart(t *testing.T) {
	sandboxPogoHome(t)
	writeCrewPromptFull(t, "pm-corpse", true, false)
	writeCrewPromptFull(t, "pm-restarter", true, true)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	spawnCorpse(t, reg, "pm-corpse", false)
	spawnCorpse(t, reg, "pm-restarter", true)

	now := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	add := func(agentName, id string) {
		t.Helper()
		if _, err := s.Add(scheduler.Entry{Agent: agentName, ID: id, Cron: "*/10 * * * *"}, now); err != nil {
			t.Fatalf("Add %s/%s: %v", agentName, id, err)
		}
	}
	add("pm-corpse", scheduler.MailCheckIDPrefix+"pm-corpse")
	add("pm-restarter", scheduler.MailCheckIDPrefix+"pm-restarter")
	s.SetLiveness(registryLiveness{reg: reg})
	s.SetGCGate(func(time.Time) bool { return true })

	if n := s.GCStaleMailChecks(now); n != 1 {
		t.Errorf("GC reaped %d, want exactly 1 (the corpse's mail-check, not the restarter's)", n)
	}

	survived := map[string]bool{}
	for _, e := range s.List("") {
		survived[e.Agent+"/"+e.ID] = true
	}
	if survived["pm-corpse/"+scheduler.MailCheckIDPrefix+"pm-corpse"] {
		t.Error("corpse's mail-check SURVIVED the sweep: it will fire at a dead agent forever, " +
			"accumulating scheduler_fire_failed noise — auto_start must not override a corpse (mg-8677)")
	}
	if !survived["pm-restarter/"+scheduler.MailCheckIDPrefix+"pm-restarter"] {
		t.Error("REGRESSION (mg-de08): restart_on_crash agent's mail-check was reaped mid-respawn")
	}
}
