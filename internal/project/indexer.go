package project

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/drellem2/pogo/internal/search"
)

// fallbackIndexInterval is used when StartPeriodicIndexer is handed a
// non-positive interval. cmd/pogod always passes config.DefaultIndexInterval
// (or a configured value), so this only guards direct/test callers.
const fallbackIndexInterval = 2 * time.Minute

// StartPeriodicIndexer launches the timer-driven incremental re-indexer: a
// background goroutine that, every interval, scans the configured index_roots
// for new repos and re-indexes every registered project through the existing
// incremental path. It replaces the event-based filesystem watcher — see
// docs/design/indexing-strategy.md and mg-5b0d.
//
// The re-index is incremental: indexRec reuses cached hashes on an unchanged
// mtime, and serializeProjectIndex skips the zoekt rebuild when no content
// changed, so a no-change tick is cheap. The goroutine stops when ctx is
// cancelled.
func StartPeriodicIndexer(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = fallbackIndexInterval
	}
	logger.Info("project: periodic indexer started", "interval", interval.String())
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				logger.Info("project: periodic indexer stopped")
				return
			case <-ticker.C:
				reindexTick()
			}
		}
	}()
}

// reindexTick runs one pass of the periodic indexer: discover new repos under
// index_roots, then re-index every registered project.
func reindexTick() {
	discoverNewRepos()
	for _, p := range Projects() {
		if search.SearchService.GetStatus(p.Path) == nil {
			// Not yet loaded into the search service (e.g. registered while
			// startup indexing was still running) — run the load+index path.
			addToPlugin(p)
			continue
		}
		// Already known: re-walk the tree. ReIndex hits the incremental path,
		// so unchanged files cost only an Lstat.
		search.SearchService.ReIndex(p.Path)
	}
}

// discoverNewRepos scans each configured index_root for git repositories that
// are not yet registered and adds them. This replaces the watch-driven
// Scanner's sibling-repo auto-discovery (mg-5b0d). With no index_roots
// configured — the zero-config default — it is a no-op; repos are then
// registered explicitly via `pogo visit`.
func discoverNewRepos() {
	for _, root := range indexRoots {
		scanRootForRepos(root)
	}
}

// scanRootForRepos registers any unregistered git repo that is a direct child
// of root. A direct-child scan is deliberately cheap and bounded — one
// directory read per index_root per tick — and matches the discovery scope
// the old Scanner restricted itself to.
func scanRootForRepos(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		logger.Warn("periodic indexer: cannot scan index_root", "path", root, "error", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		repo := filepath.Join(root, e.Name())
		if !pathExists(filepath.Join(repo, ".git")) {
			continue
		}
		normalized := addSlashToPath(repo)
		if GetProjectByPath(normalized) != nil {
			continue
		}
		// A repo may opt out of indexing with a .pogo_stop marker. No
		// ephemeral-path check is needed here: index_roots is an explicit,
		// user-configured allowlist — unlike Visit, which auto-registers
		// arbitrary visited paths and so must guard against transient repos.
		if pathExists(filepath.Join(repo, ".pogo_stop")) {
			logger.Info("periodic indexer: skipping repo with .pogo_stop", "path", normalized)
			continue
		}
		logger.Info("periodic indexer: discovered new repo", "path", normalized)
		Add(&Project{Id: 0, Path: normalized})
	}
}

// pathExists reports whether a file or directory exists at path.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
