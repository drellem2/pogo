package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// TestCrewResetPopulationIsOnlyAgentsWhoseStopIsAReset: [crew_reset] asks an
// agent to stop itself, so it must ask only agents the supervisor will bring
// back — running crew with restart_on_crash, auto_start, and no park flag.
// Asking any other agent would turn a context reset into an outage (mg-5b58d).
func TestCrewResetPopulationIsOnlyAgentsWhoseStopIsAReset(t *testing.T) {
	sandboxPogoHome(t)
	writeCrewPrompt(t, "pm-in", true)
	writeCrewPrompt(t, "pm-ondemand", false)
	writeCrewPrompt(t, "pm-norestart", true)
	writeCrewPrompt(t, "pm-parked", true)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	spawn := func(name string, typ agent.AgentType, restart bool) {
		t.Helper()
		if _, err := reg.Spawn(agent.SpawnRequest{
			Name: name, Type: typ, Command: []string{"cat"}, RestartOnCrash: restart,
		}); err != nil {
			t.Fatalf("Spawn %s: %v", name, err)
		}
	}
	spawn("pm-in", agent.TypeCrew, true)
	spawn("pm-ondemand", agent.TypeCrew, true)
	spawn("pm-norestart", agent.TypeCrew, false)
	spawn("pm-parked", agent.TypeCrew, true)
	spawn("cat-5b58", agent.TypePolecat, false)

	park := agent.ParkFilePath("pm-parked")
	if err := os.MkdirAll(filepath.Dir(park), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(park, []byte(`{"name":"pm-parked"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	pop, err := crewResetPopulation(reg)()
	if err != nil {
		t.Fatalf("population: %v", err)
	}
	var names []string
	for _, p := range pop {
		names = append(names, p.Name)
		if p.StartedAt.IsZero() {
			t.Errorf("%s has no start time; crew-reset could never measure its session", p.Name)
		}
	}
	sort.Strings(names)
	if want := []string{"pm-in"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("population = %v, want %v", names, want)
	}
}
