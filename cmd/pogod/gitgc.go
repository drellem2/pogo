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
	// Orphan polecat dirs (worktree unlinked at submit time, exit cleanup
	// never ran — gh #31) are only reachable through the polecats-dir scan;
	// scanning on every repo's sweep is idempotent, so no dedup is needed.
	polecatsDir, err := gitgc.DefaultPolecatsDir()
	if err != nil {
		log.Printf("pogod: git GC orphan-dir scan disabled: %v", err)
	}
	live := livePolecatSet(reg)
	for _, repo := range repos {
		res, err := gitgc.Sweep(gitgc.Options{
			Repo:         repo,
			LivePolecats: live,
			Tickets:      tickets,
			PolecatsDir:  polecatsDir,
		})
		if err != nil {
			log.Printf("pogod: git GC sweep of %s failed: %v", repo, err)
			continue
		}
		if len(res.BranchesDeleted) > 0 || len(res.WorktreesRemoved) > 0 || len(res.Errors) > 0 {
			log.Printf("pogod: git GC %s — deleted %d branches, removed %d worktrees, %d errors",
				repo, len(res.BranchesDeleted), len(res.WorktreesRemoved), len(res.Errors))
			for _, e := range res.Errors {
				log.Printf("pogod: git GC %s error: %s", repo, e)
			}
		}
	}
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

// livePolecatSet returns the names of all running polecats. A polecat's
// name equals its branch's "polecat-" suffix and its worktree basename, so
// gitgc.Sweep can match exclusions directly against it.
func livePolecatSet(reg *agent.Registry) map[string]bool {
	live := map[string]bool{}
	for _, a := range reg.List() {
		if a.Type == agent.TypePolecat {
			live[a.Name] = true
		}
	}
	return live
}
