package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sabhiram/go-gitignore"
	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/query"

	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

// errTreeTooLarge is returned by the index walk when a project tree exceeds
// the configured per-tree file-count ceiling. The tree is registered but
// marked StatusSkippedTooLarge and is not deep-walked. See mg-d205.
var errTreeTooLarge = errors.New("project tree exceeds max_files_per_tree ceiling")

const saveFileName = "search_index.json"
const codeSearchIndexFileName = "code_search_index"
const indexStartCapacity = 50

type PogoChunkMatch struct {
	Line    uint32 `json:"line"`
	Content string `json:"content"`
}

type PogoFileMatch struct {
	Path    string           `json:"path"`
	Matches []PogoChunkMatch `json:"matches"`
}

type SearchResults struct {
	Files []PogoFileMatch `json:"files"`
}

// IndexingStatus represents the state of a project's search index.
type IndexingStatus string

const (
	StatusUnindexed       IndexingStatus = "unindexed"
	StatusIndexing        IndexingStatus = "indexing"
	StatusReady           IndexingStatus = "ready"
	StatusStale           IndexingStatus = "stale"
	StatusSkippedTooLarge IndexingStatus = "skipped_too_large"
)

type IndexedProject struct {
	Root        string            `json:"root"`
	Paths       []string          `json:"paths"`
	FileHashes  map[string]string `json:"file_hashes,omitempty"`
	FileMtimes  map[string]int64  `json:"file_mtimes,omitempty"`
	GitTreeHash string            `json:"git_tree_hash,omitempty"`
	Status      IndexingStatus    `json:"indexing_status"`
}

// gitTreeHash returns the SHA of the tree object at HEAD for the given repo.
// This changes whenever any tracked file in the repo changes, making it ideal
// for cache invalidation without a fixed TTL.
func gitTreeHash(repoDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD^{tree}")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// deepCopyProject returns a deep copy of an IndexedProject,
// ensuring FileHashes and Paths are independent copies.
func deepCopyProject(p IndexedProject) IndexedProject {
	cp := IndexedProject{
		Root:        p.Root,
		GitTreeHash: p.GitTreeHash,
		Status:      p.Status,
	}
	cp.Paths = make([]string, len(p.Paths))
	copy(cp.Paths, p.Paths)
	cp.FileHashes = make(map[string]string, len(p.FileHashes))
	for k, v := range p.FileHashes {
		cp.FileHashes[k] = v
	}
	cp.FileMtimes = make(map[string]int64, len(p.FileMtimes))
	for k, v := range p.FileMtimes {
		cp.FileMtimes[k] = v
	}
	return cp
}

// carriedContentBudget caps the total bytes of file content carried from hash
// walks to zoekt builds across all in-flight index passes. Carrying the bytes
// lets a cold-start build reuse the read the hash already paid for instead of
// re-reading every file (gh #39); the cap keeps a cold start over many large
// repos — where every walk runs concurrently — from holding all their
// contents in memory at once. Files past the budget are re-read at build
// time, the pre-existing behavior.
const carriedContentBudget = 256 << 20 // 256 MiB

// reserveContent claims n bytes of the carried-content budget, reporting
// whether the claim fit.
func (g *BasicSearch) reserveContent(n int) bool {
	if g.carriedBytes.Add(int64(n)) > carriedContentBudget {
		g.carriedBytes.Add(-int64(n))
		return false
	}
	return true
}

// releaseContents returns the budget held by every entry of a carried-content
// map. Call it once the map is no longer needed (after the build consumed it,
// or when a walk errors out before reaching the build).
func (g *BasicSearch) releaseContents(contents map[string][]byte) {
	for _, data := range contents {
		g.carriedBytes.Add(-int64(len(data)))
	}
}

// indexWorkerPoolSize bounds how many zoekt index builds run concurrently.
// Builds used to funnel through a single writer goroutine, so N repos' cold
// start builds ran strictly one-at-a-time even though every walk was spawned
// in parallel (gh #39). More workers than CPUs just thrash: the build is
// CPU-bound (hashing + trigram construction).
var indexWorkerPoolSize = min(4, runtime.NumCPU())

// indexUpdate carries a finished walk to the build workers: the walked
// project plus any file contents the walk already read for hashing, so the
// zoekt build can reuse them instead of re-reading the file (gh #39). Every
// carried byte holds a reservation against carriedContentBudget; whoever
// drops the map must release it via releaseContents.
type indexUpdate struct {
	proj     *IndexedProject
	contents map[string][]byte
}

/*
*

	Contains channels that can be written to in order to update the project.
	Updates are sharded by project root: updates for one project always land
	on the same worker, keeping per-project ordering and build serialization,
	while distinct projects build in parallel (gh #39).
*/
type ProjectUpdater struct {
	shards []chan *indexUpdate
	quit   chan struct{}
}

// send routes an update to the worker that owns its project root.
func (u *ProjectUpdater) send(upd *indexUpdate) {
	u.shards[u.shardIndex(upd.proj.Root)] <- upd
}

// shardIndex maps a project root to its owning worker.
func (u *ProjectUpdater) shardIndex(root string) int {
	h := fnv.New32a()
	h.Write([]byte(root))
	return int(h.Sum32() % uint32(len(u.shards)))
}

func absolute(path string) (string, error) {
	str, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err2 := os.Lstat(path)
	if err2 != nil {
		return "", err2
	}
	if info.IsDir() {
		return str + "/", nil
	}
	return str, nil
}

/*
*

	Returns some channels that can be written to in order to update the project.
	Starts a goroutine that will read these channels.
*/
func (g *BasicSearch) newProjectUpdater() *ProjectUpdater {
	u := &ProjectUpdater{
		shards: make([]chan *indexUpdate, indexWorkerPoolSize),
		quit:   make(chan struct{}),
	}
	for i := range u.shards {
		u.shards[i] = make(chan *indexUpdate)
		go g.write(u, u.shards[i])
	}
	return u
}

func (g *BasicSearch) write(u *ProjectUpdater, c chan *indexUpdate) {
	for {
		select {
		case upd := <-c:
			g.applyUpdate(upd)
		case <-u.quit:
			return
		}
	}
}

// applyUpdate stores a finished index pass and persists it. It releases the
// inflight count index() took out before queueing the update — after the
// on-disk writes, so Quiesce cannot report idle while .pogo/search is still
// being written.
func (g *BasicSearch) applyUpdate(upd *indexUpdate) {
	defer g.inflight.Add(-1)
	proj := upd.proj
	// Capture the previous entry under the same lock as the store:
	// serializeProjectIndex compares against it to decide whether
	// file content actually changed. Reading the map after the
	// store would compare the new index against itself and always
	// report "unchanged" — which skipped every zoekt rebuild and
	// pinned search results to the first build (mg-1236).
	g.mu.Lock()
	prev, prevKnown := g.projects[proj.Root]
	g.projects[proj.Root] = *proj
	g.mu.Unlock()
	changed := g.serializeProjectIndex(proj, &prev, prevKnown, upd.contents)
	g.notifyIndexed(proj.Root, changed)
}

// queueUpdate hands a finished walk to the write shard that owns its project
// root, taking out the inflight count applyUpdate releases once the pass has
// hit disk.
func (g *BasicSearch) queueUpdate(u *ProjectUpdater, upd *indexUpdate) {
	g.inflight.Add(1)
	u.send(upd)
}

// Should only be called by index.
// prevHashes and prevMtimes hold data from a previous index run; when a file's
// mtime has not changed we reuse the old hash instead of re-reading the file.
//
// If the tree exceeds the configured per-tree file-count ceiling, indexRec
// stops walking and returns errTreeTooLarge so the caller can mark the project
// skipped-too-large rather than indexing an unbounded number of files.
//
// unreadable collects, by absolute path, the regular files whose read failed —
// the entries deliberately left out of the census. The caller reconciles it
// against the set it already announced; see reconcileUnreadable. May be nil.
func (g *BasicSearch) indexRec(proj *IndexedProject, path string,
	gitIgnore *ignore.GitIgnore,
	prevHashes map[string]string, prevMtimes map[string]int64,
	contents map[string][]byte, unreadable map[string]error) error {
	// Enforce the per-tree file-count ceiling before descending further.
	if g.maxFilesPerTree > 0 && int32(len(proj.Paths)) >= g.maxFilesPerTree {
		return errTreeTooLarge
	}
	// First index all files in the project
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	dirnames, err := file.Readdirnames(0)
	g.logger.Debug("Found dirs", "dirs", dirnames)
	if err != nil {
		return err
	}
	if len(dirnames) == 0 {
		return nil
	}
	files := make([]string, 0, len(dirnames)/2)
	for _, subFile := range dirnames {
		newPath := filepath.Join(path, subFile)
		fileInfo, err := os.Lstat(newPath)
		if err != nil {
			g.logger.Warn(err.Error())
			continue
		}
		// Remove projectRoot prefix from newPath
		relativePath := strings.TrimPrefix(newPath, proj.Root)

		// Skip gitignored/pogoignored paths and default-excluded directories
		// (VCS metadata, dependency and build-artifact trees). Also refuse to
		// descend into macOS TCC-protected home subtrees (Documents, Downloads,
		// …) even if reached transitively — reading them fires permission
		// popups on every tick (mg-5cd6).
		if gitIgnore.MatchesPath(relativePath) || IsExcludedDir(subFile) || IsProtectedHomePath(newPath) {
			continue
		}

		if fileInfo.IsDir() {
			err = g.indexRec(proj, newPath, gitIgnore, prevHashes, prevMtimes, contents, unreadable)
			if errors.Is(err, errTreeTooLarge) {
				proj.Paths = append(proj.Paths, files...)
				return err
			}
			if err != nil {
				g.logger.Warn(err.Error())
			}
		} else {
			// Index only nodes that resolve to a regular file. A socket,
			// FIFO, device node, dangling symlink or symlink-to-directory is
			// not readable as a file, so os.ReadFile below fails and logs at
			// ERROR — and because FileHashes is populated only on read
			// success, the mtime shortcut can never fire for it. The entry is
			// therefore re-read and re-logged on every rebuild, in perpetuity
			// (gh#136). A FIFO is worse than noise: os.ReadFile on one with no
			// writer BLOCKS, wedging the walk.
			//
			// The check must stat THROUGH the link. fileInfo comes from
			// os.Lstat, for which a symlink to a regular file is not regular —
			// testing fileInfo's own mode would silently stop indexing every
			// symlinked source file, which is indexed today
			// (TestSymlinkToRegularFileIsStillIndexed). Lstat-regular already
			// settles the common case, so the extra syscall is paid only by
			// the entries that are not plainly regular files.
			if !fileInfo.Mode().IsRegular() {
				target, terr := os.Stat(newPath)
				if terr != nil || !target.Mode().IsRegular() {
					continue
				}
			}

			mtime := fileInfo.ModTime().UnixNano()

			// Stop once the tree exceeds the ceiling. This in-loop check
			// guards against a single huge flat directory — mg-d205 saw one
			// holding 28,760 files in a single readdir. The `+1` counts this
			// entry, which is no longer appended ahead of the check: a path
			// now enters the census only once its content is accounted for
			// (see below). Counting it here keeps the ceiling exactly where it
			// was, and keeps the walk from reading a file it is about to stop
			// short of.
			if g.maxFilesPerTree > 0 && int32(len(proj.Paths)+len(files)+1) >= g.maxFilesPerTree {
				files = append(files, relativePath)
				proj.FileMtimes[relativePath] = mtime
				proj.Paths = append(proj.Paths, files...)
				return errTreeTooLarge
			}

			// A path enters the census only after its content is in hand —
			// either a cached hash the mtime shortcut let us reuse, or a
			// successful read. Appending first and reading second is the
			// gh#136 mechanism itself: a file the read fails on ends up in
			// Paths with no FileHashes entry, so the shortcut can never fire
			// for it and the zoekt build re-reads and re-logs it at ERROR on
			// every rebuild, in perpetuity. mg-f32a closed that for nodes that
			// are not regular files; this closes it for a REGULAR file that
			// cannot be read, which passes any mode check (mg-9c6b).
			cached := false
			if oldMtime, ok := prevMtimes[relativePath]; ok && oldMtime == mtime {
				if oldHash, hok := prevHashes[relativePath]; hok {
					proj.FileHashes[relativePath] = oldHash
					cached = true
				}
			}
			if !cached {
				// File is new or modified — read it once: hash the bytes and,
				// budget permitting, carry them to the zoekt build so it does
				// not have to read the file again (gh #39).
				data, herr := os.ReadFile(newPath)
				if herr != nil {
					// Dropped, not carried. The read is re-attempted on every
					// pass regardless, so a file whose permissions are
					// repaired is picked up on the next tick; the caller
					// announces the drop once. See reconcileUnreadable.
					if unreadable != nil {
						unreadable[newPath] = herr
					}
					continue
				}
				h := sha256.Sum256(data)
				proj.FileHashes[relativePath] = hex.EncodeToString(h[:])
				if contents != nil && g.reserveContent(len(data)) {
					contents[relativePath] = data
				}
			}

			files = append(files, relativePath)
			proj.FileMtimes[relativePath] = mtime
		}
	}
	proj.Paths = append(proj.Paths, files...)
	return nil
}

// Try to index all files in the project, then create a code search index.
// The first is table stakes - so we error on failure. If the second fails, we log it and return.
// prevHashes/prevMtimes are from a previous index run and enable incremental indexing.
func (g *BasicSearch) index(proj *IndexedProject, path string,
	gitIgnore *ignore.GitIgnore,
	prevHashes map[string]string, prevMtimes map[string]int64) {

	u := g.updater

	if proj.FileHashes == nil {
		proj.FileHashes = make(map[string]string)
	}
	if proj.FileMtimes == nil {
		proj.FileMtimes = make(map[string]int64)
	}

	contents := make(map[string][]byte)
	unreadable := make(map[string]error)
	err := g.indexRec(proj, path, gitIgnore, prevHashes, prevMtimes, contents, unreadable)
	// Only a walk that ran to completion saw every file under path, so only it
	// can prune what it did not find. A truncated or errored walk records what
	// it did find and leaves the rest of the set alone — forgetting a subtree
	// it never reached would re-announce it on the next pass.
	g.reconcileUnreadable(path, unreadable, err == nil)
	if errors.Is(err, errTreeTooLarge) {
		g.logger.Warn("Project tree exceeds max_files_per_tree; skipping deep index",
			"root", proj.Root, "limit", g.maxFilesPerTree, "files_indexed", len(proj.Paths))
		proj.Status = StatusSkippedTooLarge
		g.queueUpdate(u, &indexUpdate{proj: proj, contents: contents})
		return
	}
	if err != nil {
		g.logger.Warn("Error indexing project", "error", err)
		g.releaseContents(contents)
		return
	}
	g.queueUpdate(u, &indexUpdate{proj: proj, contents: contents})
}

func (g *BasicSearch) ReIndex(path string) {
	fileInfo, e := os.Lstat(path)
	if e != nil {
		g.logger.Error("Error getting path info", "path", path, "error", e)
		return
	}
	if !fileInfo.IsDir() {
		path = filepath.Dir(path)
	}
	// Debug, not Info: a re-index is announced once per project per tick
	// whether or not the pass finds anything to do, and that narration was
	// 98.8% of pogod's stderr (gh#111). The pass that actually rebuilds an
	// index still says so at Info — see serializeProjectIndex.
	//
	// The trailing argument is a named field rather than part of the message.
	// hclog's Info takes msg plus key/value pairs, so `Info("Reindexing ",
	// path)` emitted {"@message":"Reindexing ","EXTRA_VALUE_AT_END":"<path>"}
	// — the one thing a reader wants from the line was in neither the message
	// nor a field they could query.
	g.logger.Debug("Reindexing", "root", path)
	g.inflight.Add(1)
	go func() {
		defer g.inflight.Add(-1)
		fullPath, err2 := absolute(path)
		if err2 != nil {
			g.logger.Error("Error getting absolute path", "root", path, "error", err2)
			return
		}

		// Take a deep copy of the matching project under lock,
		// then release before doing channel sends or I/O.
		g.mu.RLock()
		var matchedRoot string
		var indexed IndexedProject
		for projectRoot, idx := range g.projects {
			if strings.HasPrefix(fullPath, projectRoot) {
				matchedRoot = projectRoot
				indexed = deepCopyProject(idx)
				break
			}
		}
		g.mu.RUnlock()

		if matchedRoot == "" {
			return
		}

		/* Below is a golang idiom for removing elements with a given prefix
		from the slice. We drop the stale entries under the reindexed path so
		index() re-adds only the files that still exist. */
		relativePath := strings.TrimPrefix(fullPath, matchedRoot)

		// A caller may pass a path inside a default-excluded directory (e.g.
		// pogo's own .pogo/search index files). Re-walking such a subtree
		// would index artifacts that indexRec deliberately skips, so bail out.
		if hasExcludedComponent(relativePath) {
			return
		}

		// Keep the pre-walk hash/mtime cache aside and hand it to index() as
		// the previous state: indexRec reuses a cached hash whenever a file's
		// mtime is unchanged, so an unchanged file costs one Lstat instead of
		// a full read+hash. The pruned maps rebuilt below must be separate
		// objects — before mg-1236 the prune deleted from the only copy, which
		// for a project-root reindex (relativePath == "") emptied the cache
		// and silently turned every periodic tick into a full re-read of
		// every file in the tree.
		prevHashes := indexed.FileHashes
		prevMtimes := indexed.FileMtimes
		indexed.FileHashes = make(map[string]string, len(prevHashes))
		indexed.FileMtimes = make(map[string]int64, len(prevMtimes))
		paths := indexed.Paths
		paths2 := paths
		paths = paths[:0]
		for _, p := range paths2 {
			if !strings.HasPrefix(p, relativePath) {
				paths = append(paths, p)
				if h, ok := prevHashes[p]; ok {
					indexed.FileHashes[p] = h
				}
				if m, ok := prevMtimes[p]; ok {
					indexed.FileMtimes[p] = m
				}
			}
		}
		indexed.Paths = paths

		gitIgnore, err := ParseGitIgnore(matchedRoot)
		if err != nil {
			g.logger.Error("Error parsing gitignore", "root", matchedRoot, "error", err)
		}
		g.index(&indexed, fullPath, gitIgnore, prevHashes, prevMtimes)
	}()
}

// hasExcludedComponent reports whether any path component of a project-relative
// path is a default-excluded directory.
func hasExcludedComponent(relPath string) bool {
	for _, part := range strings.Split(filepath.ToSlash(relPath), "/") {
		if part != "" && IsExcludedDir(part) {
			return true
		}
	}
	return false
}

/*
ParseGitIgnore builds an ignore matcher from a repo's .gitignore and
.pogoignore files. .pogoignore uses gitignore-style globs and lets users carve
generated-data subtrees out of pogo's index without affecting git itself
(mg-d205). Even if a file is missing or unreadable, this always returns a
non-nil GitIgnore — at worst one that matches nothing.
*/
func ParseGitIgnore(path string) (*ignore.GitIgnore, error) {
	var lines []string
	var firstErr error
	for _, name := range []string{".gitignore", ".pogoignore"} {
		data, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		lines = append(lines, strings.Split(string(data), "\n")...)
	}
	return ignore.CompileIgnoreLines(lines...), firstErr
}

func (g *BasicSearch) deleteIndexFile(p *IndexedProject) error {
	searchDir, err := p.makeSearchDir()
	if err != nil {
		g.logger.Error("Error making search dir", "root", p.Root, "error", err)
		return err
	}
	indexPath := filepath.Join(searchDir, codeSearchIndexFileName)
	// First check if indexPath exists
	_, err = os.Lstat(indexPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		} else {
			return err
		}
	}
	return os.Remove(indexPath)
}

func (g *BasicSearch) getSearchFile(p *IndexedProject, filename string) (*os.File, error) {
	path := p.Root
	searchDir, err := p.makeSearchDir()
	if err != nil {
		g.logger.Error("Error making search dir", "root", path, "error", err)
		return nil, err
	}
	indexPath := filepath.Join(searchDir, filename)
	indexFile, err := os.OpenFile(indexPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		g.logger.Error("Error opening index file", "root", path, "index_path", indexPath, "error", err)
		return nil, err
	}
	return indexFile, nil
}

func (g *BasicSearch) getIndexFile(p *IndexedProject) (*os.File, error) {
	return g.getSearchFile(p, codeSearchIndexFileName)
}

func (g *BasicSearch) Index(req *pogoPlugin.IProcessProjectReq) {
	// Held across the whole walk so Quiesce cannot report idle between a
	// synchronous Index call starting and queueUpdate taking its own count.
	g.inflight.Add(1)
	defer g.inflight.Add(-1)
	path := (*req).Path()
	g.mu.RLock()
	p, ok := g.projects[path]
	g.mu.RUnlock()
	if ok && p.Status == StatusReady && p.Paths != nil && len(p.Paths) > 0 {
		g.logger.Info("Already indexed", "root", path)
		return
	}

	// Preserve previous hashes/mtimes for incremental indexing
	var prevHashes map[string]string
	var prevMtimes map[string]int64
	if ok && len(p.FileHashes) > 0 {
		prevHashes = p.FileHashes
		prevMtimes = p.FileMtimes
		g.logger.Info("Incremental index: reusing cached hashes", "hashes", len(prevHashes), "root", path)
	}

	proj := IndexedProject{
		Root:       path,
		Paths:      make([]string, 0, indexStartCapacity),
		FileHashes: make(map[string]string),
		FileMtimes: make(map[string]int64),
		Status:     StatusIndexing,
	}
	// The in-progress placeholder gets its own maps: storing proj itself
	// would alias the maps the walk below mutates, and write()'s prev-capture
	// would then compare the finished index against itself and always report
	// "unchanged" (mg-1236).
	g.mu.Lock()
	g.projects[path] = IndexedProject{
		Root:       path,
		Paths:      make([]string, 0),
		FileHashes: make(map[string]string),
		FileMtimes: make(map[string]int64),
		Status:     StatusIndexing,
	}
	g.mu.Unlock()
	gitIgnore, err := ParseGitIgnore(path)
	if err != nil {
		// Non-fatal error
		g.logger.Error("Error parsing gitignore", "root", path, "error", err)
	}
	g.index(&proj, path, gitIgnore, prevHashes, prevMtimes)
}

// writeSaveFile persists proj's census to its save file. Both failures are
// logged rather than returned: the in-memory index is still usable, and the
// caller's own error handling is about the zoekt build, not this.
func (g *BasicSearch) writeSaveFile(proj *IndexedProject, saveFilePath string) {
	outBytes, err := json.Marshal(proj)
	if err != nil {
		g.logger.Error("Error serializing index to json", "index", *proj)
	}
	if err := os.WriteFile(saveFilePath, outBytes, 0644); err != nil {
		g.logger.Error("Error saving index", "save_path", saveFilePath)
	}
}

// dropPaths removes root-relative paths from proj's census and from both
// caches keyed on it.
//
// Removing the FileHashes entry is the load-bearing half: it is what stops the
// next walk's mtime shortcut from firing and forces the read to be re-attempted
// (see the caller). FileMtimes goes with it so the maps stay in step with
// Paths — a path with an mtime and no hash is the half-state gh#136 lived in.
//
// The kept paths go into a FRESH slice rather than being compacted in place.
// applyUpdate has already stored a struct copy of proj in g.projects, and a
// struct copy shares the slice's backing array while keeping the old length —
// so an in-place filter would leave that entry the right length over a
// compacted array, duplicating its tail for any reader holding g.mu between
// here and the caller's re-store.
func dropPaths(proj *IndexedProject, drop []string) {
	dropped := make(map[string]struct{}, len(drop))
	for _, p := range drop {
		dropped[p] = struct{}{}
		delete(proj.FileHashes, p)
		delete(proj.FileMtimes, p)
	}
	kept := make([]string, 0, len(proj.Paths))
	for _, p := range proj.Paths {
		if _, gone := dropped[p]; !gone {
			kept = append(kept, p)
		}
	}
	proj.Paths = kept
}

// serializeProjectIndex persists the index save file, rebuilds the zoekt
// index when content changed (or its file is missing), and reports whether
// file content actually changed relative to prev — the map entry that was in
// place before this pass, captured by write(). The returned signal drives the
// periodic indexer's backoff-on-unchanged scheduling (mg-1236).
//
// contents optionally carries file bytes the hash walk already read; the
// zoekt build uses them instead of re-reading those files, so a cold start
// reads each file exactly once (gh #39). Files not present (unchanged files
// whose hash was cached, or reads past the carried-content budget) fall back
// to a read here. May be nil (e.g. on Load, which rebuilds only if the zoekt
// index file went missing).
func (g *BasicSearch) serializeProjectIndex(proj *IndexedProject, prev *IndexedProject, prevKnown bool, contents map[string][]byte) bool {
	// The carried bytes are only needed within this pass; whatever the build
	// below does or doesn't consume, their budget reservation ends here.
	defer g.releaseContents(contents)
	searchDir, err := proj.makeSearchDir()
	if err != nil {
		g.logger.Error("Error making search dir", "root", proj.Root, "error", err)
		// Conservative: an errored pass reports "changed" so the periodic
		// indexer retries at base cadence instead of backing off.
		return true
	}
	saveFilePath := filepath.Join(searchDir, saveFileName)
	g.writeSaveFile(proj, saveFilePath)
	// Check if file content actually changed by comparing hashes with the
	// previous index. If nothing changed and the zoekt index file exists,
	// skip the expensive rebuild.
	contentChanged := !prevKnown || len(prev.FileHashes) != len(proj.FileHashes)
	if !contentChanged && prev.FileHashes != nil {
		for path, hash := range proj.FileHashes {
			if oldHash, ok := prev.FileHashes[path]; !ok || oldHash != hash {
				contentChanged = true
				break
			}
		}
	}

	// Capture the current git tree hash so we can detect changes on next Load.
	if h, err := gitTreeHash(proj.Root); err == nil {
		proj.GitTreeHash = h
	} else {
		g.warnGitTreeHashOnce(proj.Root, err)
	}

	// Preserve a skipped-too-large marker; otherwise the project is ready.
	if proj.Status != StatusSkippedTooLarge {
		proj.Status = StatusReady
	}
	g.mu.Lock()
	g.projects[proj.Root] = *proj
	g.mu.Unlock()
	// Level SPLIT rather than a flat demotion (gh#111). A pass that actually
	// rebuilt this project's index still reports at Info; a pass that found
	// nothing to do is the no-op narration and drops to Debug. Demoting both
	// arms would leave the indexer entirely silent at the default level, so a
	// daemon that stopped indexing would look identical to one that is idle —
	// the mg-c3f0 "correct warning nobody hears" failure in miniature.
	indexedMsg := "Indexed " + strconv.Itoa(len(proj.Paths)) + " files for " + proj.Root
	if contentChanged {
		g.logger.Info(indexedMsg)
	} else {
		g.logger.Debug(indexedMsg)
	}

	if !contentChanged {
		// Verify zoekt index file actually exists before skipping rebuild
		indexPath := filepath.Join(searchDir, codeSearchIndexFileName)
		if _, err := os.Lstat(indexPath); err == nil {
			// The skip is the no-op case by definition: Debug.
			g.logger.Debug("No content changes detected, skipping zoekt rebuild for " + proj.Root)
			return contentChanged
		}
		g.logger.Info("Zoekt index missing, rebuilding for " + proj.Root)
	}

	// Now serialize zoekt index

	// First delete the old index
	g.deleteIndexFile(proj)

	// Root-relative paths the build could not read after all; see the drop
	// below the loop.
	var unbuildable []string

	indexer, err := zoekt.NewIndexBuilder(nil)
	if err != nil {
		g.logger.Error("Error creating search index")
		return contentChanged
	}

	// Next create the code search index
	// TODO - add some useful repository metadata
	for _, path := range proj.Paths {
		// Prepend Root to path
		fullPath := filepath.Join(proj.Root, path)
		// Reuse bytes carried from the hash walk when we have them; the walk
		// stored them keyed by the same root-relative path, and Root already
		// carries its trailing separator, so fullPath matches what absolute()
		// would return for an existing file.
		if data, ok := contents[path]; ok {
			indexer.AddFile(fullPath, data)
			continue
		}
		// Both failure arms below name their path as a field rather than
		// passing it as a bare trailing argument. hclog's Error takes msg plus
		// key/value pairs, so `Error("Error getting absolute path - file may
		// not exist", path)` and `Error("Error reading file ", absPath)` each
		// put the one thing a reader wants from the line into a synthetic
		// EXTRA_VALUE_AT_END field, queryable by nothing. Same repair as the
		// Reindexing line above (mg-f3ae); the rest of the package was swept
		// by mg-6698, whose TestHclogCallsInThisPackageArePaired now keeps
		// both arms paired.
		absPath, err := absolute(fullPath)
		if err != nil {
			g.logger.Error("Error getting absolute path - file may not exist", "path", path)
			unbuildable = append(unbuildable, path)
		} else {
			bytes, err := os.ReadFile(absPath)
			if err != nil {
				g.logger.Error("Error reading file", "path", absPath)
				unbuildable = append(unbuildable, path)
			} else {
				indexer.AddFile(absPath, bytes)
			}
		}
	}
	// A path that failed above was in the census with a valid cached hash: the
	// walk read it successfully, and between the walk and here the file
	// stopped being readable — a chmod, a delete, a mount going away.
	//
	// That is the SAME forever-state as gh#136, reached from the other side,
	// and the walk-side fix alone does not close it: a cached hash is exactly
	// what makes the mtime shortcut fire, so every later walk skips the read
	// and never notices, while every rebuild re-reads and re-logs at ERROR.
	// Dropping the path — from Paths and from both maps — hands the next walk
	// an entry with no cached hash, which it must therefore read: it either
	// succeeds, or it fails, is announced once, and stays out (mg-9c6b).
	if len(unbuildable) > 0 {
		dropPaths(proj, unbuildable)
		// The census on disk was written before the build ran, so it still
		// lists what the build just dropped; a Load from it would restore
		// them. Rewriting is cheap and happens only on the pass that drops.
		g.writeSaveFile(proj, saveFilePath)
		g.mu.Lock()
		g.projects[proj.Root] = *proj
		g.mu.Unlock()
	}
	indexFile, err := g.getIndexFile(proj)
	if err != nil {
		g.logger.Error("Error getting index file", "root", proj.Root, "error", err)
		return contentChanged
	}
	defer indexFile.Close()
	err = indexer.Write(indexFile)
	if err != nil {
		// One line, not two: the root and the error describe the same failure,
		// and the second call's whole message was "Error: ".
		g.logger.Error("Error writing index file", "root", proj.Root, "error", err)
		return contentChanged
	}
	return contentChanged
}

func (g *BasicSearch) Load(projectRoot string) (*IndexedProject, error) {
	project := &IndexedProject{
		Root:   projectRoot,
		Paths:  make([]string, 0, indexStartCapacity),
		Status: StatusUnindexed,
	}
	searchDir, err := project.makeSearchDir()
	if err != nil {
		g.logger.Error("Error making search dir", "root", projectRoot, "error", err)
		return nil, err
	}
	saveFilePath := filepath.Join(searchDir, saveFileName)
	_, err = os.Lstat(saveFilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			g.mu.Lock()
			g.projects[projectRoot] = *project
			g.mu.Unlock()
			// Return empty struct
			return project, nil
		}
		return nil, err
	}
	file, err := os.Open(saveFilePath)
	if err != nil {
		g.logger.Error("Error opening index file.")
		return nil, err
	}
	defer file.Close()
	byteValue, _ := io.ReadAll(file)
	err = json.Unmarshal(byteValue, project)
	if err != nil {
		g.logger.Error("Error deserializing index file", "save_path", saveFilePath, "error", err)
		return nil, err
	}
	// Initialize maps for backward compatibility with old index files
	if project.FileHashes == nil {
		project.FileHashes = make(map[string]string)
	}
	if project.FileMtimes == nil {
		project.FileMtimes = make(map[string]int64)
	}

	// Check if index is stale by comparing stored git tree hash against
	// the current HEAD tree hash. This detects actual repo changes (commits,
	// branch switches) without relying on a fixed TTL.
	currentHash, hashErr := gitTreeHash(projectRoot)
	if hashErr != nil {
		g.warnGitTreeHashOnce(projectRoot, hashErr)
	}
	if hashErr != nil || project.GitTreeHash == "" || project.GitTreeHash != currentHash {
		if hashErr == nil {
			g.logger.Info("Index is stale for " + projectRoot + " (tree hash changed), will re-index incrementally")
		} else {
			g.logger.Info("Index is stale for " + projectRoot + " (could not verify tree hash), will re-index incrementally")
		}
		project.Status = StatusStale
		g.mu.Lock()
		g.projects[projectRoot] = *project
		g.mu.Unlock()
		return project, nil
	}

	project.Status = StatusReady
	// Store before sending so the write goroutine's prev-capture sees an
	// identical entry: the content compare then reports "unchanged" and the
	// zoekt rebuild is skipped unless its index file is missing — a clean
	// load validated by the tree-hash match above must not trigger a rebuild.
	g.mu.Lock()
	g.projects[projectRoot] = *project
	g.mu.Unlock()
	g.logger.Info("Loaded " + strconv.Itoa(len(project.Paths)) + " files for " + projectRoot)
	g.updater.send(&indexUpdate{proj: project})
	return project, nil
}

func (g *BasicSearch) GetFiles(projectRoot string) (*IndexedProject, error) {
	g.mu.RLock()
	project, ok := g.projects[projectRoot]
	g.mu.RUnlock()
	if !ok {
		return nil, errors.New("Project not indexed " + projectRoot)
	}
	return &project, nil
}

func (g *BasicSearch) Search(projectRoot string, data string, duration string) (*SearchResults, error) {
	g.mu.RLock()
	project, ok := g.projects[projectRoot]
	var knownProjects string
	for k := range g.projects {
		knownProjects += k
	}
	g.mu.RUnlock()
	if !ok {
		return nil, errors.New("Unknown project " + projectRoot + ". Known projects: " + knownProjects)
	}
	// Open index file
	searchDir, err := project.makeSearchDir()
	if err != nil {
		g.logger.Error("Error making search dir", "root", projectRoot, "error", err)
		return nil, err
	}
	indexPath := filepath.Join(searchDir, codeSearchIndexFileName)
	indexFile, err := os.Open(indexPath)
	if err != nil {
		g.logger.Error("Error opening index file", "index_path", indexPath, "error", err)
		return nil, err
	}
	defer indexFile.Close()
	index, err2 := zoekt.NewIndexFile(indexFile)
	if err2 != nil {
		g.logger.Error("Error reading index file", "index_path", indexPath, "error", err2)
		return nil, err2
	}
	// Search
	searcher, err := zoekt.NewSearcher(index)
	if err != nil {
		g.logger.Error("Error creating searcher", "index_path", indexPath, "error", err)
		return nil, err
	}
	defer searcher.Close()

	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	timeout, err := time.ParseDuration(duration)
	if err == nil {
		// The request has a timeout, so create a context that is
		// canceled automatically when the timeout expires.
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	query, err := query.Parse(data)
	if err != nil {
		g.logger.Error("Error parsing query")
		return nil, err
	}

	queryOptions := &zoekt.SearchOptions{
		ChunkMatches: true,
	}

	result, err := searcher.Search(ctx, query, queryOptions)
	if err != nil {
		g.logger.Error("Error searching index")
		return nil, err
	}

	// Create PogoFileMatch array of same size as result.Files
	fileMatches := make([]PogoFileMatch, len(result.Files))

	for i, file := range result.Files {
		chunkMatches := make([]PogoChunkMatch, len(file.ChunkMatches))
		for j, match := range file.ChunkMatches {
			chunkMatches[j] = PogoChunkMatch{
				Line:    match.ContentStart.LineNumber,
				Content: "",
			}
			if len(match.Content) > 0 {
				chunkMatches[j].Content = strings.TrimSpace(string(match.Content))
			}
		}
		fileMatches[i] = PogoFileMatch{
			Path:    strings.Replace(file.FileName, projectRoot, "", 1),
			Matches: chunkMatches,
		}
	}
	return &SearchResults{
		Files: fileMatches,
	}, nil
}
