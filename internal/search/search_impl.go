package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/logging"
	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

const pogoDir = ".pogo"
const searchDir = "search"

// API Version for this plugin
const version = "0.0.1"

var SearchService = createBasicSearch()

type BasicSearch struct {
	mu       sync.RWMutex
	logger   hclog.Logger
	projects map[string]IndexedProject
	updater  *ProjectUpdater
	// maxFilesPerTree caps how many files are indexed per project tree. A tree
	// over the ceiling is registered, marked skipped-too-large, and not
	// deep-walked. 0 means unlimited. See mg-d205.
	maxFilesPerTree int32
	// carriedBytes tracks file content carried from hash walks to zoekt
	// builds, bounded by carriedContentBudget (gh #39).
	carriedBytes atomic.Int64
	// inflight counts index work that has not yet finished touching disk:
	// every Index/ReIndex call (including the goroutines ProcessProject and
	// ReIndex spawn) and every update queued to the write shards. Quiesce
	// waits for it to reach zero. See Quiesce for why this exists.
	inflight atomic.Int64
	// onIndexed, when set, is invoked after every completed index pass with
	// the project root and whether file content actually changed. The
	// periodic indexer uses it to drive backoff-on-unchanged scheduling
	// (mg-1236). Guarded by onIndexedMu; invoked from the write goroutine.
	onIndexedMu sync.RWMutex
	onIndexed   func(root string, contentChanged bool)
	// gitHashWarned records the project roots that have already logged the
	// "Could not read git tree hash" warning in this process. See
	// warnGitTreeHashOnce for why the warning is deduplicated rather than
	// demoted, and Evict for why entries are dropped with the project.
	// Guarded by gitHashWarnedMu, which is never held while g.mu is taken.
	gitHashWarnedMu sync.Mutex
	gitHashWarned   map[string]struct{}
	// unreadableWarned holds the absolute paths of regular files that the most
	// recent walk to reach them could not read, and that are therefore absent
	// from the index. Membership is what has already been announced; see
	// reconcileUnreadable for why the set is reconciled against each walk
	// rather than only added to. Guarded by unreadableMu, which is never held
	// while g.mu is taken.
	unreadableMu     sync.Mutex
	unreadableWarned map[string]struct{}
}

// msgUnreadableFile is the announcement a dropped file gets. It is a constant
// because the drop is the only notice anything downstream receives that a file
// stopped being searchable — a test that greps for a literal drifts from the
// call site, and a drifted grep is indistinguishable from "it never happened".
const msgUnreadableFile = "Skipping unreadable file; it is not in the search index"

// warnGitTreeHashOnce logs a failure to read root's git tree hash at Warn the
// first time this process sees it for that root, and stays silent for every
// later failure on the same root (gh#111).
//
// The level is deliberately unchanged: the first occurrence is real signal —
// it means the index cannot use its git fast path for that project and must
// fall back to hashing every file. What was wrong with the line is that the
// periodic re-indexer re-emitted it for the same repo on every tick, forever.
//
// The dedupe is keyed by PROJECT, not by call site. Both the save path and the
// load path can fail for the same repo, often in the same pass; a per-site
// dedupe would still warn twice for one project. Passing the message through a
// single method is what makes "once" mean once.
func (g *BasicSearch) warnGitTreeHashOnce(root string, err error) {
	g.gitHashWarnedMu.Lock()
	_, seen := g.gitHashWarned[root]
	if !seen {
		g.gitHashWarned[root] = struct{}{}
	}
	g.gitHashWarnedMu.Unlock()
	if seen {
		return
	}
	// The message text is unchanged from before the dedupe existed, so
	// anything already grepping pogod.log for it keeps matching.
	g.logger.Warn("Could not read git tree hash for " + root + ": " + err.Error())
}

// forgetGitTreeHashWarning drops root's dedupe entry so a project that comes
// back reports its first failure again.
//
// This is what keeps the map from being a slow leak. pogod runs for weeks and
// the map lives for the whole process, so an entry per root ever seen — with
// no matching removal — grows without bound in exactly the long-lived process
// the dedupe exists to protect. Tied to Evict, the map cannot outgrow
// g.projects.
func (g *BasicSearch) forgetGitTreeHashWarning(root string) {
	g.gitHashWarnedMu.Lock()
	delete(g.gitHashWarned, root)
	g.gitHashWarnedMu.Unlock()
}

// subtreePrefix renders a directory as a path prefix that cannot match a
// sibling. Without the trailing separator "/a/b" is a prefix of "/a/bc/d.go",
// and the two directories are unrelated.
func subtreePrefix(dir string) string {
	return strings.TrimSuffix(dir, "/") + "/"
}

// reconcileUnreadable brings the remembered set of unreadable files under
// scope into line with what the walk just found there, announcing each file
// once — as it enters the set.
//
// WHY THE FILE IS DROPPED AND THE DROP IS ANNOUNCED. A regular file whose read
// fails (mode 0000 is the common case) used to be carried in Paths with no
// FileHashes entry, which is what made gh#136 perpetual: the mtime shortcut
// can never fire for a path with no cached hash, so every rebuild re-read the
// file and logged `Error reading file` at ERROR, one per file per pass,
// forever. Leaving it out of the census stops that at the source. Doing so
// silently would trade a noisy defect for an invisible one — the file simply
// stops being searchable and nothing says so — so the drop is announced. It is
// announced ONCE, because a line per pass is the same defect wearing a Warn
// badge instead of an ERROR one.
//
// WHY NOT A PERSISTED FAILURE MARKER. The other candidate remedy was to record
// a negative result the mtime shortcut could consult, so the retry stops. The
// retry is worth keeping and costs nothing: the walk re-attempts the read on
// every pass either way, so a repaired file is picked up on the very next tick
// with no marker to invalidate. A marker would need an invalidation rule, and
// the only key this walk holds is mtime — which a chmod does not change. The
// marker would outlive the repair and keep the file out of the index until its
// CONTENT changed, which is strictly worse than the state it replaces. See
// TestRepairingPermissionsDoesNotChangeMtime, which pins that premise.
//
// prune says whether found is an exhaustive answer for scope. A walk that ran
// to completion visited every file under it, so a remembered path missing from
// found is readable again (or gone) and must be forgotten — otherwise the same
// file failing next month would be silent. A walk that stopped early
// (errTreeTooLarge, or a directory it could not open) reached only part of the
// subtree, and forgetting what it never saw would re-announce it next pass.
//
// Forgetting is also what bounds the set. pogod runs for weeks, and here the
// entries are per FILE rather than per project, so an add-only set is the slow
// leak forgetGitTreeHashWarning exists to avoid, with a far higher ceiling.
// Reconciled against the walk, the set holds only files that are unreadable
// now.
func (g *BasicSearch) reconcileUnreadable(scope string, found map[string]error, prune bool) {
	prefix := subtreePrefix(scope)

	var fresh []string
	g.unreadableMu.Lock()
	for path := range found {
		if _, seen := g.unreadableWarned[path]; !seen {
			g.unreadableWarned[path] = struct{}{}
			fresh = append(fresh, path)
		}
	}
	if prune {
		for path := range g.unreadableWarned {
			if !strings.HasPrefix(path, prefix) {
				continue
			}
			if _, still := found[path]; !still {
				delete(g.unreadableWarned, path)
			}
		}
	}
	g.unreadableMu.Unlock()

	// Sorted so a pass that finds several reports them in a stable order.
	sort.Strings(fresh)
	for _, path := range fresh {
		g.logger.Warn(msgUnreadableFile, "path", path, "error", found[path])
	}
}

// SetOnIndexed registers a callback invoked after each completed index pass
// (initial index, load, or re-index) with the project root and whether file
// content changed relative to the previous pass. Passing nil clears it.
func (g *BasicSearch) SetOnIndexed(fn func(root string, contentChanged bool)) {
	g.onIndexedMu.Lock()
	g.onIndexed = fn
	g.onIndexedMu.Unlock()
}

// notifyIndexed invokes the registered onIndexed callback, if any.
func (g *BasicSearch) notifyIndexed(root string, contentChanged bool) {
	g.onIndexedMu.RLock()
	fn := g.onIndexed
	g.onIndexedMu.RUnlock()
	if fn != nil {
		fn(root, contentChanged)
	}
}

// Input to an "Execute" call should be a serialized SearchRequest
type SearchRequest struct {
	// Values: "search" or "files"
	Type        string `json:"type"`
	ProjectRoot string `json:"projectRoot"`
	// Command timeout duration - only for 'search'-type requests
	Duration string `json:"string"`
	Data     string `json:"data"`
}

type SearchResponse struct {
	Index   IndexedProject `json:"index"`
	Results SearchResults  `json:"results"`
	Error   string         `json:"error"`
}

type ErrorResponse struct {
	ErrorCode int    `json:"errorCode"`
	Error     string `json:"error"`
}

func New() func() (pogoPlugin.IPogoPlugin, error) {
	return func() (pogoPlugin.IPogoPlugin, error) {
		return SearchService, nil
	}
}

func clean(path string) string {
	// Append a trailing delimiter if it doesn't exist
	p := filepath.Clean(path)
	if p[len(p)-1] != filepath.Separator {
		p += string(filepath.Separator)
	}
	return p
}

func (g *BasicSearch) printSearchResponse(response SearchResponse) string {
	// Instead of marshalling the obect, write code to go through all fields
	// and concatenate them into a string.
	var str string
	str += "Index: " + response.Index.Root + "\n"
	str += "Paths: " + "\n"
	for _, path := range response.Index.Paths {
		str += path + "\n"
	}
	str += "Results: " + "\n"
	for _, result := range response.Results.Files {
		str += "\t" + result.Path + "\n"
		for _, match := range result.Matches {
			// Convert match.Content bytes to string
			var lineStr = strconv.FormatUint(uint64(match.Line), 10)
			str += "\t\t" + lineStr + "\n"
			if len(match.Content) > 0 {
				// str += "\t\t" + string(match.Content) + "\n"
				str += "\t\t" + match.Content + "\n"
			} else {
				str += "\t\t" + "No content" + "\n"
			}
		}
	}
	str += "Error: " + response.Error + "\n"
	return str
}

func (g *BasicSearch) errorResponse(code int, message string) string {
	resp := ErrorResponse{ErrorCode: code, Error: message}
	bytes, err := json.Marshal(&resp)
	if err != nil {
		g.logger.Error("Error writing error response")
		panic(err)
	}
	return string(bytes)
}

func (g *BasicSearch) searchResponse(index *IndexedProject, results *SearchResults) string {
	var response SearchResponse
	if index == nil {
		indexedProject := IndexedProject{Root: "", Paths: []string{}}
		response.Index = indexedProject
	} else {
		response.Index = *index
	}
	if results == nil {
		g.logger.Info("Search response was nil")
		searchResults := SearchResults{}
		response.Results = searchResults
	} else {
		response.Results = *results
	}
	response.Error = ""

	bytes, err := json.Marshal(&response)
	if err != nil {
		g.logger.Error("Error writing search response")
		return g.errorResponse(500, "Error writing search response")
	}
	return string(bytes)
}

func (g *BasicSearch) Info() *pogoPlugin.PluginInfoRes {
	g.logger.Debug("Returning version", "version", version)
	return &pogoPlugin.PluginInfoRes{Version: version}
}

// Executes a command sent to this plugin.
func (g *BasicSearch) Execute(req string) string {
	g.logger.Debug("Executing request.")
	var searchRequest SearchRequest
	err := json.Unmarshal([]byte(req), &searchRequest)
	if err != nil {
		g.logger.Info("400 Invalid request.", "error", err)
		return g.errorResponse(400, "Invalid request.")
	}

	switch reqType := searchRequest.Type; reqType {
	case "search":
		searchRequest.ProjectRoot = clean(searchRequest.ProjectRoot)
		results, err := g.Search(searchRequest.ProjectRoot,
			searchRequest.Data, searchRequest.Duration)
		if err != nil {
			g.logger.Error("500 Error executing search.", "error", err)
			return g.errorResponse(500, "Error executing search.")
		}
		return g.searchResponse(nil, results)
	case "files":
		searchRequest.ProjectRoot = clean(searchRequest.ProjectRoot)
		proj, err3 := g.GetFiles(searchRequest.ProjectRoot)
		if err3 != nil {
			g.logger.Error("500 Error retrieving files.", "error", err3)
			return g.errorResponse(500, "Error retrieving files.")
		}
		return g.searchResponse(proj, nil)
	default:
		g.logger.Info("404 Unknown request type.", "type", searchRequest.Type)
		return g.errorResponse(404, "Unknown request type.")
	}

}

func (g *BasicSearch) ProcessProject(req *pogoPlugin.IProcessProjectReq) error {
	g.logger.Info("Processing project", "path", (*req).Path())
	proj, err := g.Load((*req).Path())
	if err != nil {
		g.logger.Error("Error processing project", "error", err)
	}
	if err != nil || len(proj.Paths) == 0 || proj.Status == StatusStale {
		// Count the goroutine before starting it, not inside it: between the
		// `go` statement and Index's first instruction the pass is real work
		// in flight, and a Quiesce in that window must not report idle.
		g.inflight.Add(1)
		go func() {
			defer g.inflight.Add(-1)
			g.Index(req)
		}()
	}
	return nil
}

// Quiesce blocks until every in-flight index pass has finished writing, or
// timeout elapses; it reports whether the service went idle.
//
// Indexing is asynchronous end to end. ProcessProject spawns `go Index`, and
// the sharded write goroutine creates <root>/.pogo/search, the index save
// file and the zoekt shard *after* the project's status has already flipped
// to Ready. A caller that stops as soon as its assertions pass — a test
// returning into t.TempDir()'s RemoveAll — therefore races those writes and
// fails with "directory not empty" (mg-36d9). Quiesce is the barrier such
// callers need: it waits for the work itself to finish, rather than retrying
// an assertion until the race happens to fall the right way.
func (g *BasicSearch) Quiesce(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for g.inflight.Load() > 0 {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
	return true
}

type ProjectStatus struct {
	Root      string         `json:"root"`
	Status    IndexingStatus `json:"indexing_status"`
	FileCount int            `json:"file_count"`
}

func (g *BasicSearch) GetAllStatuses() []ProjectStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()
	statuses := make([]ProjectStatus, 0, len(g.projects))
	for _, p := range g.projects {
		statuses = append(statuses, ProjectStatus{
			Root:      p.Root,
			Status:    p.Status,
			FileCount: len(p.Paths),
		})
	}
	return statuses
}

func (g *BasicSearch) GetStatus(projectRoot string) *ProjectStatus {
	g.mu.RLock()
	p, ok := g.projects[projectRoot]
	g.mu.RUnlock()
	if !ok {
		return nil
	}
	return &ProjectStatus{
		Root:      p.Root,
		Status:    p.Status,
		FileCount: len(p.Paths),
	}
}

// Evict drops a project from the in-memory index map, releasing its paths,
// hashes and mtimes. Without eviction the map held every repo ever registered
// for the daemon's lifetime (gh #39). On-disk index files under the project's
// .pogo dir are left in place, so a re-registered project reloads instead of
// re-indexing. An index pass already in flight for the root re-inserts it on
// completion; callers that evict on a schedule (the periodic indexer) simply
// evict it again next pass.
func (g *BasicSearch) Evict(projectRoot string) {
	g.mu.Lock()
	_, existed := g.projects[projectRoot]
	delete(g.projects, projectRoot)
	g.mu.Unlock()
	// Drop the git-hash warning dedupe entry with the project, so the map
	// cannot accumulate roots the daemon no longer holds. The unreadable-file
	// set goes the same way: nothing will walk this subtree again to prune it,
	// so an evicted project's entries would be permanent. A nil `found` with
	// prune set is exactly "everything remembered under here is stale".
	g.forgetGitTreeHashWarning(projectRoot)
	g.reconcileUnreadable(projectRoot, nil, true)
	if existed {
		g.logger.Info("Evicted project from in-memory index map: " + projectRoot)
	}
}

// SetMaxFilesPerTree updates the per-tree file-count ceiling.
// Call this after loading configuration to override the default.
func (g *BasicSearch) SetMaxFilesPerTree(max int) {
	if max > 0 {
		g.maxFilesPerTree = int32(max)
		g.logger.Info("Max files per tree set to "+strconv.Itoa(max), "max_files_per_tree", max)
	}
}

// handshakeConfigs are used to just do a basic handshake betw1een
// a plugin and host. If the handshake fails, a user friendly error is shown.
// This prevents users from executing bad plugins or executing a plugin
// directory. It is a UX feature, not a security feature.
var handshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  2,
	MagicCookieKey:   "SEARCH_PLUGIN",
	MagicCookieValue: "93f6bc9f97c03ed00fa85c904aca15a92752e549",
}

// Ensure's plugin directory exists in project config
// Returns full path of search dir
func (p *IndexedProject) makeSearchDir() (string, error) {
	fullSearchDir := filepath.Join(p.Root, pogoDir, searchDir)
	err := os.MkdirAll(fullSearchDir, os.ModePerm)
	if err != nil {
		return "", err
	}
	// The index dir lives inside the repo working tree; keep it out of
	// `git status` for every repo pogo touches (gh #40).
	ensurePogoGitExcluded(p.Root)
	return fullSearchDir, nil
}

func createBasicSearch() *BasicSearch {
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      logging.Level(),
		Output:     os.Stderr,
		JSONFormat: true,
	})

	maxF := int32(config.DefaultMaxFilesPerTree)
	if mfStr := os.Getenv("POGO_MAX_FILES_PER_TREE"); mfStr != "" {
		if mf, err := strconv.Atoi(mfStr); err == nil && mf > 0 {
			maxF = int32(mf)
		}
	}

	basicSearch := &BasicSearch{
		logger:           logger,
		projects:         make(map[string]IndexedProject),
		updater:          nil,
		maxFilesPerTree:  maxF,
		gitHashWarned:    make(map[string]struct{}),
		unreadableWarned: make(map[string]struct{}),
	}
	basicSearch.updater = basicSearch.newProjectUpdater()

	return basicSearch
}
