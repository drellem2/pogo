package main

import (
	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
)

// resolveRoles reads config and resolves the process-wide coordinator/worker
// names from it, without touching disk. Every `pogo` subcommand needs the names
// (prompt show/list synthesize client-side, in this process), but only `pogo
// install` may write to config.toml — so the read path and the pin path are
// separate calls.
func resolveRoles() *config.Config {
	cfg, _ := config.GuardRunningCoordinator(config.Load())
	agent.SetCoordinatorName(cfg.Agents.Coordinator)
	agent.SetWorkerName(cfg.Agents.Worker)
	return cfg
}

// pinAndResolveRoles runs the default-migration guard on an existing install and
// then RE-resolves the process-wide role names from the pinned config. It is the
// `pogo install` path: main() already resolved names from config.Load(), which
// fills empty [agents] keys with the LIVE Default* consts, so on the first
// install of a build that flipped those defaults (mg-ce47) this process holds
// the NEW names while pinning the OLD ones to disk — and would render prompts
// and print "next steps" under a coordinator name the pinned config disowns
// (mg-bc47).
//
// Call it BEFORE agent.InstallPrompts: prompt synthesis expands {{.Coordinator}}
// / {{.Worker}} from the process-wide names, so a late pin writes prompts naming
// a role this install does not have.
//
// existing must be snapshotted before InstallPrompts too, for the opposite
// reason: afterwards a brand-new machine carries stamped prompts and
// IsExistingInstall reads true, pinning legacy names onto a fresh install that
// is meant to adopt the new defaults.
//
// Re-resolving is unconditional, not gated on len(PinResult.Pinned) > 0: `pogo
// install` starts pogod first, and that daemon now pins on boot — so by the time
// we get here the keys can already be present, making our own pin a no-op while
// this process still holds the stale names read at startup.
//
// The rename guard runs last and outranks both. `pogo install` on a machine whose
// coordinator is up must not synthesize prompts naming a role that install does
// not have, nor tell the operator to start a coordinator under a new name while
// the old one is still answering mail (mg-cf9e).
func pinAndResolveRoles(existing bool) (config.PinResult, *config.RenameRefusal, error) {
	cfg, res, err := config.PinAndLoad(existing)
	cfg, refusal := config.GuardRunningCoordinator(cfg)
	agent.SetCoordinatorName(cfg.Agents.Coordinator)
	agent.SetWorkerName(cfg.Agents.Worker)
	return res, refusal, err
}
