package main

import (
	"context"
	"fmt"
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

// loadTicketIndexFn is the work-item lookup a sweep runs on, indirected so a
// test can drive runGitGCSweep end to end. It is the FIRST thing the sweep
// does and it shells out to `mg`, which a sandboxed test cannot reach — so
// without this seam the sweep returns at "cannot load work items" and nothing
// below it, including the Logf wiring, is reachable from a test at all. That
// unreachability is how the wiring came to have zero coverage in round 1 of
// this ticket's review: deleting the Logf line left the whole package green.
// Same shape as gitgc.removeWorktreeFn. Production always uses
// gitgc.LoadTicketIndex.
var loadTicketIndexFn = gitgc.LoadTicketIndex

// runGitGCSweep performs one GC pass over every repo known to pogod:
// repos listed in config plus the source repo of every registered agent.
// The live-polecat set is passed as the do-not-touch exclusion so a sweep
// can never disturb a running polecat's branch or worktree.
func runGitGCSweep(reg *agent.Registry, cfg config.GitGCConfig) {
	repos := gitGCRepos(reg, cfg)
	if len(repos) == 0 {
		return
	}
	tickets, err := loadTicketIndexFn()
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
	for _, repo := range repos {
		res, err := gitgc.Sweep(gitgc.Options{
			Repo:         repo,
			LivePolecats: live,
			Tickets:      tickets,
			PolecatsDir:  polecatsDir,
			// One line per ACTION, not just the counts below. The sweep
			// already assembles path, owner, branch and reason for every
			// decision and used to throw all of it away — so a removal in a
			// multi-megabyte log was a bare number, and "did the GC take my
			// worktree, and why" was unanswerable after the fact for any past
			// incident (gh #94). Only actions log: a keep-because-live line
			// per polecat per tick would be pure noise, and the two decisions
			// worth reading — something was destroyed, something dirty was
			// preserved — are exactly the ones the sweep emits.
			Logf: gitGCLogf(repo),
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

// gitGCLogf returns the per-action logger for one repo's sweep, tagging every
// line with the repo so a multi-repo sweep stays readable and greppable
// alongside the summary line runGitGCSweep already emits.
func gitGCLogf(repo string) func(string, ...any) {
	return func(format string, args ...any) {
		log.Printf("pogod: git GC %s — %s", repo, fmt.Sprintf(format, args...))
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

// livePolecatSet returns the names of every polecat a sweep must treat as live
// and therefore never disturb: this pogod's registry, unioned with the
// restart-surviving polecat witness.
//
// The union itself lives in agent.LivePolecatSet, which is where the whole
// argument for it is written down (mg-0130) and, since mg-1403, where `pogo gc`
// gets the same answer from. This function is only the registry projection —
// pogod holds a *Registry, the CLI holds pogod's /agents reply, and the shared
// function takes the one thing both can produce: the names.
func livePolecatSet(reg *agent.Registry) (map[string]bool, error) {
	var names []string
	for _, a := range reg.List() {
		if a.Type == agent.TypePolecat {
			names = append(names, a.Name)
		}
	}
	return agent.LivePolecatSet(names)
}
