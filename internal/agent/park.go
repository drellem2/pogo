package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Park / wake: the supported crew dormancy lifecycle (mg-88a8 / mg-41e1).
//
// restart_on_crash=true is an always-on contract — pogod respawns the agent
// on ANY exit, including an explicit `pogo agent stop`. Before park existed
// there was no supported way to permanently stop such an agent mid-session:
// the respawn goroutine wins the race in ~2-3s, and for PM-tier agents even a
// stub-level restart_on_crash=false was ignored (the flag was resolved from
// the synthesized prompt whose frontmatter comes from pm-template.md).
//
// Park collapses the interim three-step pattern (stub flag edit + schedule
// removal + stand-down nudge) into one command:
//
//   - a park flag is persisted at ~/.pogo/agents/<name>/.parked BEFORE the
//     process is stopped, so the OnExit respawn check can never lose the race;
//   - the agent's pogod schedules are removed and recorded in the park file
//     so wake can restore them;
//   - AutoStartAgents skips parked agents regardless of auto_start, so the
//     flag survives pogod restarts;
//   - `pogo agent list` reports parked agents with status=parked so the
//     mayor's stall-watch can skip them mechanically.
//
// Wake reverses all of it: start the agent, restore the recorded schedules,
// clear the flag.

// StatusParked is the AgentInfo status reported for a parked (dormant) crew
// agent. Parked agents have no process and no registry entry; the status is
// synthesized from the on-disk park flag when listing agents.
const StatusParked AgentStatus = "parked"

// parkFileName is the park flag file inside an agent's stable working
// directory (~/.pogo/agents/<name>/).
const parkFileName = ".parked"

// ParkState is the on-disk record for a parked agent, stored at
// ParkFilePath(name). Its presence alone is the park flag; the fields carry
// what wake needs to reverse the park.
type ParkState struct {
	Name     string    `json:"name"`
	ParkedAt time.Time `json:"parked_at"`
	// Schedules holds the scheduler entries that were removed at park time
	// (one raw JSON object per entry), recorded so wake can restore them.
	// Serialized opaquely because the agent package must not import the
	// scheduler package (the scheduler imports agent).
	Schedules []json.RawMessage `json:"schedules,omitempty"`
}

// SchedulePauser removes and restores an agent's pogod schedules across a
// park/wake cycle. pogod backs this with its scheduler; the entries travel as
// raw JSON so this package stays free of a scheduler import (the scheduler
// already imports agent).
type SchedulePauser interface {
	// PauseForAgent removes every schedule addressed to any of the given
	// agent aliases (bare name and crew-<name> event identity) and returns
	// the removed entries, one JSON object each, for the park record.
	PauseForAgent(aliases ...string) ([]json.RawMessage, error)
	// RestoreForAgent re-adds entries previously returned by PauseForAgent,
	// recomputing fire times where appropriate. Returns how many entries were
	// restored; a partial failure restores what it can and reports the first
	// error.
	RestoreForAgent(entries []json.RawMessage) (int, error)
}

// SetSchedulePauser installs the scheduler adapter used by Park/Wake to pause
// and restore an agent's schedules. A nil pauser (bare registry, scheduler
// disabled) makes park skip schedule handling — the flag and stop still work.
func (r *Registry) SetSchedulePauser(p SchedulePauser) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.schedulePauser = p
}

func (r *Registry) getSchedulePauser() SchedulePauser {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.schedulePauser
}

// ParkFilePath returns the park flag path for an agent:
// ~/.pogo/agents/<name>/.parked
func ParkFilePath(name string) string {
	return filepath.Join(PromptDir(), name, parkFileName)
}

// IsParked reports whether the named agent has a park flag on disk.
func IsParked(name string) bool {
	_, err := os.Stat(ParkFilePath(name))
	return err == nil
}

// ReadParkState loads the park record for an agent. Returns (nil, nil) when
// the agent is not parked. A corrupt park file still counts as parked (the
// flag is its presence); the state comes back with only the name set so wake
// can proceed without schedule restoration.
func ReadParkState(name string) (*ParkState, error) {
	data, err := os.ReadFile(ParkFilePath(name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read park state: %w", err)
	}
	var st ParkState
	if err := json.Unmarshal(data, &st); err != nil {
		log.Printf("agent %s: park file %s is corrupt (%v); treating as parked with no recorded schedules", name, ParkFilePath(name), err)
		return &ParkState{Name: name}, nil
	}
	if st.Name == "" {
		st.Name = name
	}
	return &st, nil
}

// writeParkState persists the park record, creating the agent dir if needed.
func writeParkState(st *ParkState) error {
	dir := filepath.Dir(ParkFilePath(st.Name))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create agent dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal park state: %w", err)
	}
	if err := os.WriteFile(ParkFilePath(st.Name), data, 0644); err != nil {
		return fmt.Errorf("write park state: %w", err)
	}
	return nil
}

// clearParkState removes the park flag. Missing file is not an error so wake
// stays idempotent.
func clearParkState(name string) error {
	if err := os.Remove(ParkFilePath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear park state: %w", err)
	}
	return nil
}

// ListParked scans ~/.pogo/agents/*/ for park flags and returns the park
// records, sorted by directory order (os.ReadDir is lexical). An absent
// prompt dir yields an empty list.
func ListParked() ([]ParkState, error) {
	entries, err := os.ReadDir(PromptDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var parked []ParkState
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st, err := ReadParkState(e.Name())
		if err != nil || st == nil {
			continue
		}
		parked = append(parked, *st)
	}
	return parked, nil
}

// ShouldRespawn reports whether the supervisor (pogod's OnExit hook) should
// bring this agent back after an exit: restart_on_crash must be set AND the
// agent must not be parked. The park flag is checked on disk at exit time —
// it is written before the park stop is issued, so the respawn goroutine can
// never win the race against a park.
func (a *Agent) ShouldRespawn() bool {
	return a.RestartOnCrash && !IsParked(a.Name)
}

// crewPromptPath returns the on-disk prompt file for a crew agent name (the
// coordinator's mayor.md lives in PromptDir, everyone else's under crew/).
// Returns a *PromptNotFoundError when the file is missing.
func crewPromptPath(name string) (string, error) {
	var promptFile string
	if name == CoordinatorName() {
		promptFile = filepath.Join(PromptDir(), "mayor.md")
	} else {
		promptFile = filepath.Join(CrewPromptDir(), name+".md")
	}
	if _, err := os.Stat(promptFile); os.IsNotExist(err) {
		return "", &PromptNotFoundError{Path: promptFile}
	}
	return promptFile, nil
}

// Park puts a crew agent into supported dormancy: it pauses (removes and
// records) the agent's pogod schedules, persists the park flag, and stops the
// process. The flag is written BEFORE the stop so pogod's OnExit hook — which
// checks ShouldRespawn — suppresses the restart_on_crash respawn instead of
// racing it. Returns the number of schedules paused.
//
// Parking an agent that is not currently running is valid (the flag still
// gates auto-start and respawn); the name must then resolve to a known crew
// prompt so a typo doesn't park a ghost. Parking an already-parked agent is
// idempotent — any schedules that reappeared since the first park are folded
// into the existing record.
func (r *Registry) Park(name string, timeout time.Duration) (int, error) {
	a := r.Get(name)
	if a != nil && a.Type == TypePolecat {
		return 0, fmt.Errorf("agent %q is a polecat; park applies to crew agents only", name)
	}
	if a == nil && !IsParked(name) {
		if _, err := crewPromptPath(name); err != nil {
			return 0, err
		}
	}

	st, err := ReadParkState(name)
	if err != nil {
		return 0, err
	}
	if st == nil {
		st = &ParkState{Name: name, ParkedAt: time.Now()}
	}

	// Pause schedules first so the recorded entries land in the same park
	// file write as the flag itself.
	var paused []json.RawMessage
	pauser := r.getSchedulePauser()
	if pauser != nil {
		paused, err = pauser.PauseForAgent(name, "crew-"+name)
		if err != nil {
			// Roll the removals back rather than leaving schedules half-gone
			// with no park record; the operator retries the park.
			if _, rerr := pauser.RestoreForAgent(paused); rerr != nil {
				log.Printf("agent %s: park aborted and schedule rollback failed: %v", name, rerr)
			}
			return 0, fmt.Errorf("pause schedules: %w", err)
		}
	}
	st.Schedules = append(st.Schedules, paused...)

	if err := writeParkState(st); err != nil {
		if pauser != nil && len(paused) > 0 {
			if _, rerr := pauser.RestoreForAgent(paused); rerr != nil {
				log.Printf("agent %s: park aborted and schedule rollback failed: %v", name, rerr)
			}
		}
		return 0, err
	}

	if a != nil {
		if err := r.Stop(name, timeout); err != nil {
			return len(paused), fmt.Errorf("park flag set but stop failed: %w", err)
		}
	}
	log.Printf("agent %s: parked (%d schedule(s) paused)", name, len(paused))
	return len(paused), nil
}

// Wake reverses a Park: it starts the crew agent, restores the schedules
// recorded in the park file, and clears the flag. The flag is cleared LAST so
// a failed start leaves the park intact and retryable (a re-run of wake
// resumes from the same record). An agent that is somehow already running is
// treated as started. Returns the running agent and the number of schedules
// restored.
//
// The agent also re-registers its own schedules per the crew startup
// contract; the scheduler's add is keyed on (agent, id) and replaces, so
// restoration and re-registration never stack duplicates.
func (r *Registry) Wake(name string) (*Agent, int, error) {
	st, err := ReadParkState(name)
	if err != nil {
		return nil, 0, err
	}
	if st == nil {
		return nil, 0, fmt.Errorf("agent %q is not parked", name)
	}

	a, err := r.StartCrewAgent(name)
	if err != nil {
		if !strings.Contains(err.Error(), "already running") {
			return nil, 0, err
		}
		a = r.Get(name)
		if a == nil {
			return nil, 0, err
		}
	}

	restored := 0
	pauser := r.getSchedulePauser()
	if pauser != nil && len(st.Schedules) > 0 {
		restored, err = pauser.RestoreForAgent(st.Schedules)
		if err != nil {
			// The agent is up; a partially-restored schedule set is not worth
			// re-parking over. Log and continue — the agent's own startup
			// contract re-registers its schedules anyway.
			log.Printf("agent %s: wake restored %d/%d schedule(s): %v", name, restored, len(st.Schedules), err)
		}
	}

	if err := clearParkState(name); err != nil {
		return a, restored, err
	}
	log.Printf("agent %s: woken (pid=%d, %d schedule(s) restored)", name, a.PID, restored)
	return a, restored, nil
}
