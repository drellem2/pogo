package main

import (
	"log"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
)

// pinAndResolveRoles runs the default-migration guard on an existing install and
// then resolves the process-wide coordinator/worker names from the pinned
// config, returning the reloaded Config. It must be called before ANY consumer
// reads a role name off cfg — prompt refresh, crew auto-start, the stall
// watcher, and the refinery's coordinator mail all take their name from here
// (mg-bc47).
//
// The existing-install signal is cfg.Source != "" — a config file exists — and
// deliberately NOT config.IsExistingInstall(). That helper also reports true off
// a stamped prompt with no config.toml, which would make a prompts-but-no-config
// daemon CREATE config.toml, flipping on the opt-in orchestration this daemon is
// supposed to skip (mg-3dc3). cfg.Source is the same signal pogod already trusts
// to gate prompt refresh and auto-start.
//
// A pin failure is logged, never fatal: an unpinnable config.toml must not stop
// the daemon from booting.
func pinAndResolveRoles(cfg *config.Config) *config.Config {
	if cfg.Source != "" {
		pinned, pinRes, err := config.PinAndLoad(true)
		cfg = pinned
		if err != nil {
			log.Printf("pogod: role-default pin failed: %v", err)
		} else if len(pinRes.Pinned) > 0 {
			log.Printf("pogod: pinned current role default(s) [%s] in %s",
				strings.Join(pinRes.Pinned, ", "), pinRes.Path)
		}
	}

	// Resolve the coordinator agent's name ([agents] coordinator) before any
	// prompt synthesis or autostart happens — it decides which agent name maps
	// to the coordinator prompt and what prompts call the role. The worker's
	// name rides along; it is display-only, feeding prompt prose and never a
	// mailbox, schedule id, or agent-type key.
	agent.SetCoordinatorName(cfg.Agents.Coordinator)
	agent.SetWorkerName(cfg.Agents.Worker)
	return cfg
}
