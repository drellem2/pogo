package agent

import (
	"errors"
	"log"
	"sort"
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

// AutoStartAgents scans crew prompt files under ~/.pogo/agents/ and starts
// every agent whose prompt declares auto_start = true in its TOML frontmatter.
//
// It is idempotent: agents already registered (e.g. on pogod restart) are
// skipped rather than double-started. Polecat templates are skipped — only
// the mayor and crew prompts are eligible.
//
// Prompts are processed alphabetically by name. Errors starting any single
// agent are logged and reported in the result slice; they do not abort the
// scan.
func (r *Registry) AutoStartAgents() []AutoStartResult {
	prompts, err := ListPrompts()
	if err != nil {
		log.Printf("autostart: list prompts failed: %v", err)
		return nil
	}

	// Filter to crew-eligible prompts and sort by name for deterministic order.
	type candidate struct {
		name     string
		path     string
		category string
	}
	var cands []candidate
	for _, p := range prompts {
		if p.Category != "mayor" && p.Category != "crew" {
			continue
		}
		cands = append(cands, candidate{name: p.Name, path: p.Path, category: p.Category})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].name < cands[j].name })

	var results []AutoStartResult
	for _, c := range cands {
		meta, _, err := ParsePromptFrontmatter(c.path)
		if err != nil {
			log.Printf("autostart: parse %s failed: %v", c.path, err)
			results = append(results, AutoStartResult{
				Name:     c.name,
				Path:     c.path,
				Category: c.category,
				Status:   AutoStartStatusFailed,
				Error:    err.Error(),
			})
			continue
		}
		if meta == nil || !meta.AutoStart {
			results = append(results, AutoStartResult{
				Name:     c.name,
				Path:     c.path,
				Category: c.category,
				Status:   AutoStartStatusSkippedNoFlag,
			})
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
