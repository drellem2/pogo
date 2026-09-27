package main

import (
	"os"
	"strings"
	"testing"
)

// TestScheduleLong_StatesAgentLifecycle pins the agent-lifecycle case in
// `pogo schedule --help` (drellem2/pogo#205). The help text listed three things
// a schedule survives. It did not say that a mail-check row is reaped when its
// agent exits and pogod will not respawn it, so a reader took the row to be
// scoped to the seat.
func TestScheduleLong_StatesAgentLifecycle(t *testing.T) {
	for _, want := range []string{
		// The three survivals are still stated.
		"survive host sleep, NTP steps, and pogod restarts",
		// The lifecycle case: which schedules, and scoped to what.
		"a mail-check-* schedule lives only while pogod supervises its\nagent",
		"NOT scoped to the seat",
		// What keeps the row, and what removes it.
		"A supervised respawn (restart_on_crash=true)\nkeeps the row",
		"park/wake removes it and puts it back",
		"reason=agent_gone",
		"restart_on_crash=false",
		"a respawn suppressed by the synthetic-failure detector",
		"Schedules of any other\nkind are never removed by an agent's exit",
		// The durable fix, and why repeating it is safe.
		"re-register its schedules at startup",
		"restarted outside pogod",
		"Pass --id",
		"does not add a\nduplicate",
	} {
		if !strings.Contains(scheduleLong, want) {
			t.Errorf("schedule --help is missing %q:\n%s", want, scheduleLong)
		}
	}
}

// TestScheduleLong_DoesNotOverclaimSurvival refuses any wording that promises
// a mail-check row survives an agent exit or restart. An unsupervised exit
// reaps it, and a restart outside pogod may or may not come after that reap.
// Only re-registration at startup is guaranteed to work.
func TestScheduleLong_DoesNotOverclaimSurvival(t *testing.T) {
	low := strings.ToLower(scheduleLong)
	for _, bad := range []string{
		"survive agent restart",
		"survives agent restart",
		"survive an agent restart",
		"survives an agent restart",
		"survive agent exit",
		"survives agent exit",
		"is scoped to the seat",
		"are scoped to the seat",
	} {
		if strings.Contains(low, bad) {
			t.Errorf("schedule --help overclaims survival (%q):\n%s", bad, scheduleLong)
		}
	}
}

// TestScheduleCmd_UsesScheduleLong checks that the text the tests above read
// is the text the command ships. Without this check, the cobra literal could
// drift back into main.go and those tests would keep passing on a const
// nothing uses.
func TestScheduleCmd_UsesScheduleLong(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	i := strings.Index(s, `Use:   "schedule <agent>",`)
	if i < 0 {
		t.Fatal(`main.go no longer declares Use: "schedule <agent>"`)
	}
	decl := s[i:min(len(s), i+400)]
	if !strings.Contains(decl, "Long:  scheduleLong,") {
		t.Errorf("pogo schedule's Long is not scheduleLong:\n%s", decl)
	}
}
