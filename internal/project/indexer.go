package project

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// fallbackDiscoveryInterval is used when StartRepoDiscovery is handed a
// non-positive interval. cmd/pogod always passes config.DefaultIndexInterval
// (or a configured index_interval), so this only guards direct/test callers.
const fallbackDiscoveryInterval = 2 * time.Minute

// StartRepoDiscovery launches the timer-driven index_roots scan: a background
// goroutine that, every interval, registers git repos newly created directly
// under a configured index_root. With no index_roots configured (the
// zero-config default) each tick is a no-op and repos are registered only via
// `pogo visit` / `pogo project add`.
//
// Until mg-14647 this loop was the periodic file-search re-indexer
// (StartPeriodicIndexer) with a per-project backoff scheduler; the search
// indexer was deleted and only the discovery half — which is part of the
// project index — remains. The goroutine stops when ctx is cancelled; the
// returned channel closes once it has fully exited, letting tests sequence
// shutdown deterministically.
func StartRepoDiscovery(ctx context.Context, interval time.Duration) <-chan struct{} {
	if interval <= 0 {
		interval = fallbackDiscoveryInterval
	}
	logger.Info("project: index_roots discovery started", "interval", interval.String())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				logger.Info("project: index_roots discovery stopped")
				return
			case <-ticker.C:
				discoverNewRepos()
			}
		}
	}()
	return done
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
		logger.Warn("repo discovery: cannot scan index_root", "path", root, "error", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		repo := filepath.Join(root, e.Name())
		// .git or an enth checkout (.enth/checkout, X1, enth-vcs slice 5); a
		// directory holding both is refused rather than guessed at (X2).
		git, enth := pathExists(filepath.Join(repo, ".git")), enthvcs.IsCheckout(repo)
		if git && enth {
			logger.Warn("repo discovery: skipping a directory holding both .git and .enth/checkout", "path", repo)
			continue
		}
		if !git && !enth {
			continue
		}
		normalized := addSlashToPath(repo)
		if GetProjectByPath(normalized) != nil {
			continue
		}
		// Never register a repo in $HOME or a macOS TCC-protected home subtree
		// (Desktop/Documents/Downloads/…): reading it fires permission popups
		// (mg-5cd6, regression of mg-cbc5).
		if IsProtectedHomePath(repo) {
			logger.Info("repo discovery: skipping repo in macOS-protected home dir (would trigger TCC popups)", "path", normalized)
			continue
		}
		// A repo may opt out of indexing with a .pogo_stop marker. No
		// ephemeral-path check is needed here: index_roots is an explicit,
		// user-configured allowlist — unlike Visit, which auto-registers
		// arbitrary visited paths and so must guard against transient repos.
		if pathExists(filepath.Join(repo, ".pogo_stop")) {
			logger.Info("repo discovery: skipping repo with .pogo_stop", "path", normalized)
			continue
		}
		logger.Info("repo discovery: discovered new repo", "path", normalized)
		Add(&Project{Id: 0, Path: normalized})
	}
}

// pathExists reports whether a file or directory exists at path.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
