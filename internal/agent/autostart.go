package agent

import (
	"errors"
	"log"
	"sort"
	"strings"
)

// AutoStartStatus describes the outcome of considering one prompt file for
// auto-start during pogod boot.
type AutoStartStatus string

const (
	// AutoStartStatusStarted means the agent was spawned by AutoStartAgents.
	AutoStartStatusStarted AutoStartStatus = "started"
	// AutoStartStatusSkippedRunning means a same-named agent was already
	// registered, so we left it alone (idempotent restart).
	AutoStartStatusSkippedRunning AutoStartStatus = "skipped_running"
	// AutoStartStatusSkippedNoFlag means the prompt did not declare
	// auto_start = true.
	AutoStartStatusSkippedNoFlag AutoStartStatus = "skipped_no_flag"
	// AutoStartStatusSkippedParked means the agent has a park flag on disk
	// (see Registry.Park); parked agents stay dormant across pogod restarts
	// regardless of auto_start.
	AutoStartStatusSkippedParked AutoStartStatus = "skipped_parked"
	// AutoStartStatusFailed means we tried to start the agent but the spawn
	// itself errored out.
	AutoStartStatusFailed AutoStartStatus = "failed"
)

// AutoStartResult records what happened for one prompt file during auto-start.
type AutoStartResult struct {
	Name     string          `json:"name"`
	Path     string          `json:"path"`
	Category string          `json:"category"`
	Status   AutoStartStatus `json:"status"`
	Error    string          `json:"error,omitempty"`
}

// autoStartCandidate is one crew/mayor prompt considered against pogod's
// desired state.
type autoStartCandidate struct {
	name     string
	path     string
	category string
}

// autoStartCandidates returns every crew/mayor prompt, sorted by name for
// deterministic order. Polecat templates are not eligible — they are dispatched
// per work item, never auto-started.
func autoStartCandidates() ([]autoStartCandidate, error) {
	prompts, err := ListPrompts()
	if err != nil {
		return nil, err
	}
	var cands []autoStartCandidate
	for _, p := range prompts {
		if p.Category != "mayor" && p.Category != "crew" {
			continue
		}
		cands = append(cands, autoStartCandidate{name: p.Name, path: p.Path, category: p.Category})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].name < cands[j].name })
	return cands, nil
}

// expectedStatus classifies one candidate against pogod's DESIRED STATE: the
// set of agents pogod is supposed to be running. It returns an empty status
// when the agent is EXPECTED, and otherwise the reason it is not — parked, no
// auto_start flag, or an unreadable prompt.
//
// This is the single source of truth for "should this agent be running?", and
// it deliberately consults only on-disk declarations (prompt frontmatter + the
// park flag), never the registry. Two consumers depend on that independence
// (mg-de08):
//
//   - the mail-check reap, which must not treat an empty registry as evidence
//     of death (see registryLiveness in cmd/pogod);
//   - diagnose, which must flag an EXPECTED agent that has no mail-check loop.
//
// One predicate, two consumers: the reap removes schedules for agents NOT in
// desired state, diagnose flags agents IN desired state with no schedule.
func expectedStatus(c autoStartCandidate) (AutoStartStatus, error) {
	// The park flag wins over auto_start: a parked agent stays dormant
	// across pogod restarts until explicitly woken (mg-41e1).
	if IsParked(c.name) {
		return AutoStartStatusSkippedParked, nil
	}
	meta, _, err := ParsePromptFrontmatter(c.path)
	if err != nil {
		return AutoStartStatusFailed, err
	}
	if meta == nil || !meta.AutoStart {
		return AutoStartStatusSkippedNoFlag, nil
	}
	return "", nil
}

// ExpectedAgents returns the names of every agent in pogod's desired state:
// crew/mayor prompts declaring auto_start = true that are not parked. This is
// exactly the set AutoStartAgents starts, computed without starting anything.
//
// A prompt that cannot be classified is omitted; callers that must not guess
// should use DesiredStateFor, which reports that case as an error rather than
// as a "no".
func ExpectedAgents() []string {
	cands, err := autoStartCandidates()
	if err != nil {
		log.Printf("expected-agents: list prompts failed: %v", err)
		return nil
	}
	var out []string
	for _, c := range cands {
		if st, _ := expectedStatus(c); st == "" {
			out = append(out, c.name)
		}
	}
	return out
}

// DesiredStateFor classifies one agent identity against pogod's desired state.
// identity may be a bare agent name ("mayor") or a crew event identity
// ("crew-mayor") — schedules address agents both ways.
//
// Three answers, and the third one matters:
//
//   - (true, nil)  — in the desired state: an auto_start, not-parked prompt.
//   - (false, nil) — definitively NOT: no prompt at all (a polecat, or config
//     that was removed), no auto_start flag, or parked. Callers may act on this.
//   - (false, err) — a prompt EXISTS for this agent but could not be read or
//     parsed. This is UNKNOWN, not "no": we know the agent was configured, we
//     just cannot read the flag. A caller whose action is irreversible — the
//     mail-check reap — must not act on it (mg-de08).
//
// A polecat identity ("cat-de08") is never expected: polecats have no
// auto_start prompt, they are dispatched per work item and die with their work.
// That is what keeps the reap's GONE direction intact across a pogod restart —
// crew are EXPECTED and keep their mail-check, polecats are GONE and lose it.
func DesiredStateFor(identity string) (bool, error) {
	if identity == "" {
		return false, nil
	}
	cands, err := autoStartCandidates()
	if err != nil {
		// The prompt tree itself is unreadable — we cannot classify ANY agent.
		return false, err
	}
	// Only the crew- form is unwrapped. A cat- identity is a polecat and can
	// never name a crew prompt, so leaving it wrapped is the safe reading.
	bare := strings.TrimPrefix(identity, "crew-")
	for _, c := range cands {
		if c.name != identity && c.name != bare {
			continue
		}
		st, err := expectedStatus(c)
		if st == AutoStartStatusFailed {
			return false, err
		}
		return st == "", nil
	}
	// No prompt by that name: never configured, or the config was removed.
	// Both are the desired state saying "not this agent" — evidence, not
	// absence of it.
	return false, nil
}

// IsExpectedAgent reports whether the given agent identity is in pogod's
// desired state, collapsing DesiredStateFor's unknown case to false. Use it
// where a wrong "no" is harmless (diagnose stays quiet rather than reporting a
// false RED); use DesiredStateFor where it is not (the reap).
func IsExpectedAgent(identity string) bool {
	expected, err := DesiredStateFor(identity)
	return expected && err == nil
}

// AutoStartAgents scans crew prompt files under ~/.pogo/agents/ and starts
// every agent whose prompt declares auto_start = true in its TOML frontmatter
// and is not parked — that is, every agent in the desired state described by
// expectedStatus.
//
// It is idempotent: agents already registered (e.g. on pogod restart) are
// skipped rather than double-started. Polecat templates are skipped — only
// the mayor and crew prompts are eligible.
//
// Prompts are processed alphabetically by name. Errors starting any single
// agent are logged and reported in the result slice; they do not abort the
// scan.
func (r *Registry) AutoStartAgents() []AutoStartResult {
	cands, err := autoStartCandidates()
	if err != nil {
		log.Printf("autostart: list prompts failed: %v", err)
		return nil
	}

	var results []AutoStartResult
	for _, c := range cands {
		if st, err := expectedStatus(c); st != "" {
			switch st {
			case AutoStartStatusSkippedParked:
				log.Printf("autostart: %s is parked; skipping (wake with 'pogo agent wake %s')", c.name, c.name)
			case AutoStartStatusFailed:
				log.Printf("autostart: parse %s failed: %v", c.path, err)
			}
			res := AutoStartResult{
				Name:     c.name,
				Path:     c.path,
				Category: c.category,
				Status:   st,
			}
			if err != nil {
				res.Error = err.Error()
			}
			results = append(results, res)
			continue
		}

		// Skip if an agent with this name is already registered. Spawn would
		// also reject the duplicate, but checking up-front keeps the result
		// status precise and avoids a noisy "already running" error log on
		// every pogod restart.
		if r.Get(c.name) != nil {
			results = append(results, AutoStartResult{
				Name:     c.name,
				Path:     c.path,
				Category: c.category,
				Status:   AutoStartStatusSkippedRunning,
			})
			continue
		}

		a, err := r.StartCrewAgent(c.name)
		if err != nil {
			// Treat "already running" as skipped rather than failed in case
			// of a race between Get above and Spawn.
			if errors.Is(err, ErrPromptNotFound) {
				log.Printf("autostart: %s prompt missing: %v", c.name, err)
			} else {
				log.Printf("autostart: %s start failed: %v", c.name, err)
			}
			results = append(results, AutoStartResult{
				Name:     c.name,
				Path:     c.path,
				Category: c.category,
				Status:   AutoStartStatusFailed,
				Error:    err.Error(),
			})
			continue
		}
		log.Printf("autostart: started %s (pid=%d)", c.name, a.PID)
		results = append(results, AutoStartResult{
			Name:     c.name,
			Path:     c.path,
			Category: c.category,
			Status:   AutoStartStatusStarted,
		})
	}
	return results
}
