package project

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drellem2/pogo/internal/search"
)

// fallbackIndexInterval is used when StartPeriodicIndexer is handed a
// non-positive interval. cmd/pogod always passes config.DefaultIndexInterval
// (or a configured value), so this only guards direct/test callers.
const fallbackIndexInterval = 2 * time.Minute

// backoffMaxMultiplier caps how far a project's reindex interval can back
// off: base interval × multiplier. With the default 2m base that is 32m —
// the worst-case staleness for a repo that changes without any pogo visit
// (e.g. a cron-driven git fetch). Activity and detected changes snap the
// interval back to base.
const backoffMaxMultiplier = 16

// sched holds the live backoff scheduler once StartPeriodicIndexer has run.
// Atomic because Visit (HTTP goroutines) reads it via MarkProjectActivity
// while pogod's startup goroutine installs it.
var sched atomic.Pointer[reindexScheduler]

// reindexScheduler decides, per project, when the next incremental re-index
// is due. Every project starts at the base interval; each pass that finds no
// content change doubles the project's interval (capped at base ×
// backoffMaxMultiplier), and a pass that finds changes — or a visit to the
// project — resets it to base. Idle repos thus converge to a cheap slow scan
// while active ones stay at base cadence (mg-1236, gh #39).
type reindexScheduler struct {
	mu    sync.Mutex
	base  time.Duration
	max   time.Duration
	state map[string]*projectBackoff
}

type projectBackoff struct {
	interval time.Duration
	nextDue  time.Time
}

func newReindexScheduler(base time.Duration) *reindexScheduler {
	return &reindexScheduler{
		base:  base,
		max:   base * backoffMaxMultiplier,
		state: make(map[string]*projectBackoff),
	}
}

// ensure returns the state for path, creating it at base cadence (due
// immediately) if absent. Callers must hold s.mu.
func (s *reindexScheduler) ensure(path string) *projectBackoff {
	st, ok := s.state[path]
	if !ok {
		st = &projectBackoff{interval: s.base}
		s.state[path] = st
	}
	return st
}

// due reports whether path should be re-indexed at now. Paths never seen
// before are due immediately.
func (s *reindexScheduler) due(path string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.state[path]
	if !ok {
		return true
	}
	return !now.Before(st.nextDue)
}

// markFired advances path's next-due time by its current interval. Called
// when a re-index is kicked off, so ticks arriving while a slow walk is
// still in flight don't re-fire it; the completion callback (onIndexed)
// then reschedules based on the outcome.
func (s *reindexScheduler) markFired(path string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure(path)
	st.nextDue = now.Add(st.interval)
}

// onIndexed is the completion callback registered with the search service:
// a pass that changed content resets the project to base cadence, an
// unchanged pass doubles its interval up to the cap.
func (s *reindexScheduler) onIndexed(path string, contentChanged bool) {
	s.reschedule(path, contentChanged, time.Now())
}

func (s *reindexScheduler) reschedule(path string, contentChanged bool, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure(path)
	if contentChanged {
		st.interval = s.base
	} else {
		st.interval *= 2
		if st.interval > s.max {
			st.interval = s.max
		}
	}
	st.nextDue = now.Add(st.interval)
}

// markActivity resets path to base cadence and makes it due immediately, so
// the next tick re-indexes it. Fired on `pogo visit` — the shell/editor
// integrations visit on every cd, making visits a cheap activity signal that
// undoes any accumulated backoff.
func (s *reindexScheduler) markActivity(path string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure(path)
	st.interval = s.base
	st.nextDue = now
}

// prune drops scheduling state for paths no longer in the registry, keeping
// the state map from accumulating entries for removed projects.
func (s *reindexScheduler) prune(active map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for path := range s.state {
		if !active[path] {
			delete(s.state, path)
		}
	}
}

// MarkProjectActivity records a visit to a registered project, resetting its
// reindex backoff so the next tick picks it up at base cadence. A no-op until
// StartPeriodicIndexer has run.
func MarkProjectActivity(path string) {
	if s := sched.Load(); s != nil {
		s.markActivity(path, time.Now())
	}
}

// StartPeriodicIndexer launches the timer-driven incremental re-indexer: a
// background goroutine that, every interval, scans the configured index_roots
// for new repos and re-indexes registered projects that are due. It replaces
// the event-based filesystem watcher — see docs/design/indexing-strategy.md
// and mg-5b0d.
//
// interval is the base cadence. Each project backs off exponentially while
// its content is unchanged (up to backoffMaxMultiplier × interval) and snaps
// back to base on a detected change or a visit, so idle repos are not
// re-walked every tick (mg-1236). The re-index itself is incremental:
// indexRec reuses cached hashes on an unchanged mtime, and
// serializeProjectIndex skips the zoekt rebuild when no content changed. The
// goroutine stops when ctx is cancelled; the returned channel closes once it
// has fully exited, letting tests sequence shutdown deterministically.
func StartPeriodicIndexer(ctx context.Context, interval time.Duration) <-chan struct{} {
	if interval <= 0 {
		interval = fallbackIndexInterval
	}
	s := newReindexScheduler(interval)
	sched.Store(s)
	search.SearchService.SetOnIndexed(s.onIndexed)
	logger.Info("project: periodic indexer started",
		"interval", interval.String(), "max_backoff", s.max.String())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				logger.Info("project: periodic indexer stopped")
				return
			case <-ticker.C:
				reindexTick(s, time.Now())
			}
		}
	}()
	return done
}

// reindexTick runs one pass of the periodic indexer: discover new repos under
// index_roots, then re-index every registered project that is due per the
// backoff scheduler.
func reindexTick(s *reindexScheduler, now time.Time) {
	discoverNewRepos()
	active := make(map[string]bool, len(projects))
	for _, p := range Projects() {
		active[p.Path] = true
		if search.SearchService.GetStatus(p.Path) == nil {
			// Not yet loaded into the search service (e.g. registered while
			// startup indexing was still running) — run the load+index path.
			s.markFired(p.Path, now)
			addToPlugin(p)
			continue
		}
		if !s.due(p.Path, now) {
			continue
		}
		// Due: re-walk the tree. ReIndex hits the incremental path, so
		// unchanged files cost only an Lstat; the onIndexed callback
		// reschedules the project based on whether content changed.
		s.markFired(p.Path, now)
		search.SearchService.ReIndex(p.Path)
	}
	s.prune(active)
	// Evict search-service state for projects no longer registered, the
	// in-memory counterpart of s.prune. Remove() evicts directly; this sweep
	// additionally catches entries re-inserted by an index pass that was in
	// flight when its project was removed (gh #39).
	for _, st := range search.SearchService.GetAllStatuses() {
		if !active[st.Root] {
			search.SearchService.Evict(st.Root)
		}
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
