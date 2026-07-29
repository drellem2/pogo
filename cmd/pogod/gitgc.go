package main

import (
	"context"
	"log"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/gitgc"
)

// startGitGC wires the polecat git garbage collector into pogod. It runs
// one sweep immediately — covering the gap where pogod itself died while
// polecats were running and so the per-exit cleanup never fired — and then
// a sweep every cfg.Interval as an ongoing backstop against future
// force-stops, crashes and stalls.
//
// The GC logic lives in internal/gitgc as a self-contained library; pogod
// only supplies the live-polecat exclusion set and the set of repos to
// sweep. See mg-30d5.
func startGitGC(ctx context.Context, reg *agent.Registry, cfg config.GitGCConfig) {
	if !cfg.Enabled {
		log.Printf("pogod: git GC disabled")
		return
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = config.DefaultGitGCInterval
	}
	log.Printf("pogod: git GC enabled (interval %s)", interval)
	go func() {
		runGitGCSweep(reg, cfg) // startup sweep
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runGitGCSweep(reg, cfg)
			}
		}
	}()
}

// runGitGCSweep performs one GC pass over every repo known to pogod:
// repos listed in config plus the source repo of every registered agent.
// The live-polecat set is passed as the do-not-touch exclusion so a sweep
// can never disturb a running polecat's branch or worktree.
func runGitGCSweep(reg *agent.Registry, cfg config.GitGCConfig) {
	repos := gitGCRepos(reg, cfg)
	if len(repos) == 0 {
		return
	}
	tickets, err := gitgc.LoadTicketIndex()
	if err != nil {
		log.Printf("pogod: git GC skipped — cannot load work items: %v", err)
		return
	}
	// Orphan polecat dirs (files left behind with no .git when a polecat's
	// exit cleanup never ran — e.g. pogod died mid-polecat, gh #31) are only
	// reachable through the polecats-dir scan; scanning on every repo's sweep
	// is idempotent, so no dedup is needed. The submit-time worktree unlink
	// that once stripped a live polecat's registration was deleted (gh #88),
	// so new orphans of that shape no longer accrue; the scan stays for the
	// legacy dirs it left behind and the pogod-died-mid-polecat case.
	polecatsDir, err := gitgc.DefaultPolecatsDir()
	if err != nil {
		log.Printf("pogod: git GC orphan-dir scan disabled: %v", err)
	}
	live, err := livePolecatSet(reg)
	if err != nil {
		// The witness store is on disk but unreadable. It is the ONLY guard a
		// restart-surviving polecat's worktree has (worktree removal has no
		// merge gate), and an unreadable store is not an empty fleet — reading
		// it as "no polecats live" would delete a running polecat's work. Skip
		// the sweep, exactly as an unreadable ticket index does above (mg-0130).
		log.Printf("pogod: git GC skipped — cannot read polecat witness: %v", err)
		return
	}
	liveWorktrees := livePolecatWorktrees(reg)
	for _, repo := range repos {
		res, err := gitgc.Sweep(gitgc.Options{
			Repo:          repo,
			LivePolecats:  live,
			LiveWorktrees: liveWorktrees,
			Tickets:       tickets,
			PolecatsDir:   polecatsDir,
		})
		if err != nil {
			log.Printf("pogod: git GC sweep of %s failed: %v", repo, err)
			continue
		}
		if len(res.BranchesDeleted) > 0 || len(res.WorktreesRemoved) > 0 || len(res.Errors) > 0 {
			log.Printf("pogod: git GC %s — deleted %d branches, removed %d worktrees, %d errors",
				repo, len(res.BranchesDeleted), len(res.WorktreesRemoved), len(res.Errors))
			// Then name each one. The counts alone are what made mg-3b7c take a
			// log grep plus a polecat's own incident report to diagnose: "removed
			// 1 worktrees" does not say WHICH tree vanished, and the agent whose
			// tree it was gets no signal at all. A destructive action should be
			// legible from the log that records it.
			for _, w := range res.WorktreesRemoved {
				log.Printf("pogod: git GC %s removed worktree %s (branch %s) — %s",
					repo, w.Path, orNone(w.Branch), w.Reason)
			}
			for _, b := range res.BranchesDeleted {
				log.Printf("pogod: git GC %s deleted branch %s (work item %s) — %s",
					repo, b.Branch, orNone(b.ID), b.Reason)
			}
			for _, e := range res.Errors {
				log.Printf("pogod: git GC %s error: %s", repo, e)
			}
		}
	}
}

// orNone renders an empty optional field as a word rather than a blank gap in
// the middle of a log line.
func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// gitGCRepos returns the deduplicated set of repositories to sweep:
// configured repos unioned with the source repo of every registered agent.
func gitGCRepos(reg *agent.Registry, cfg config.GitGCConfig) []string {
	seen := map[string]bool{}
	var repos []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			repos = append(repos, p)
		}
	}
	for _, r := range cfg.Repos {
		add(r)
	}
	for _, a := range reg.List() {
		add(a.SourceRepo)
	}
	return repos
}

// livePolecatSet returns the names of every polecat a sweep must treat as live
// and therefore never disturb. A polecat's name equals its branch's "polecat-"
// suffix and its worktree basename, so gitgc.Sweep matches exclusions directly
// against it.
//
// It unions TWO sources, because neither is complete alone (mg-0130):
//
//   - the in-memory registry — authoritative while pogod has run continuously,
//     but EMPTY after a restart, permanently, because the registry has no
//     adopt/reattach path (mg-13a3);
//   - the persisted polecat witness — which survives a restart and answers on
//     (pid, start_time), so a polecat that outlived the pogod that spawned it
//     stays protected.
//
// Without the witness union a restart empties the live set while startGitGC's
// startup sweep runs, and a polecat whose ticket is already done but whose
// process is still alive — every polecat's NORMAL end state (`mg done`, then
// await the mayor's stop) — loses its sole worktree guard. Worktree removal is
// gated on the live set alone, with no merge gate to catch the mistake, unlike
// branch deletion; so the worktree is removed out from under a running polecat.
//
// A witnessed polecat counts as live when its process is provably ours
// (WitnessAlive) OR when its identity cannot be established (WitnessUnreadable):
// the asymmetry favours keeping a running polecat's work over reclaiming a dead
// one's disk, matching the mail-check reaper, which likewise never reaps on
// Unreadable (registryLiveness). WitnessDead and WitnessNoRecord add nothing —
// a provably-dead polecat is exactly what the sweep exists to clean up.
//
// A witness READ error is returned, not swallowed: the caller must skip the
// sweep rather than sweep against a live set it knows is missing survivors.
func livePolecatSet(reg *agent.Registry) (map[string]bool, error) {
	live := map[string]bool{}
	for _, a := range reg.List() {
		if a.Type == agent.TypePolecat {
			live[a.Name] = true
		}
	}
	verdicts, err := agent.WitnessedPolecatVerdicts()
	if err != nil {
		return nil, err
	}
	for name, v := range verdicts {
		if v == agent.WitnessAlive || v == agent.WitnessUnreadable {
			live[name] = true
		}
	}
	return live, nil
}

// livePolecatWorktrees returns the worktree DIRECTORY of every polecat in the
// registry, which is what gitgc uses to decide a tree is in use. pogod has
// known this all along — it records WorktreeDir at spawn and logs it ("polecat
// <name>: created worktree at <path>") — but never handed it to the sweep,
// which was left inferring occupancy from the checked-out branch instead and
// deleted a running polecat's tree because of it (mg-3b7c).
//
// Registry-only on purpose: the persisted witness records names and PIDs, not
// paths, so it cannot answer this. That gap is closed inside gitgc, which
// re-derives <PolecatsDir>/<name> for every live NAME — covering the polecat
// that outlived the pogod which spawned it, whose registry entry is gone.
// Exited agents are excluded; their trees are what the sweep is for.
func livePolecatWorktrees(reg *agent.Registry) map[string]bool {
	paths := map[string]bool{}
	for _, a := range reg.List() {
		if a.Type == agent.TypePolecat && a.Status != agent.StatusExited && a.WorktreeDir != "" {
			paths[a.WorktreeDir] = true
		}
	}
	return paths
}
