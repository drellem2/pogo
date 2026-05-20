package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/watch"
	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

const pogoDir = ".pogo"
const searchDir = "search"

// API Version for this plugin
const version = "0.0.1"

const UseWatchers = true

var SearchService = createBasicSearch()

type BasicSearch struct {
	mu       sync.RWMutex
	logger   hclog.Logger
	projects map[string]IndexedProject
	watcher  watch.Watcher
	updater  *ProjectUpdater
	// maxWatchers caps the number of watched project roots. With the FSEvents
	// backend the unit watched IS the tree, so this finally bounds the right
	// unit (mg-d205). 0 means unlimited.
	maxWatchers int32
	// maxFilesPerTree caps how many files are indexed per project tree. A tree
	// over the ceiling is registered, marked skipped-too-large, and not
	// deep-walked or watched. 0 means unlimited. See mg-d205.
	maxFilesPerTree int32
	// watchCount is the current number of watched roots.
	watchCount atomic.Int32
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
	g.logger.Debug("Returning version %s", version)
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
	g.logger.Info("Processing project %s", (*req).Path())
	proj, err := g.Load((*req).Path())
	if err != nil {
		g.logger.Error("Error processing project", "error", err)
	}
	if err != nil || len(proj.Paths) == 0 || proj.Status == StatusStale {
		go g.Index(req)
	}
	return nil
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

// SetMaxWatchers updates the cap on watched project roots.
// Call this after loading configuration to override the default.
func (g *BasicSearch) SetMaxWatchers(max int) {
	if max > 0 {
		g.maxWatchers = int32(max)
		g.logger.Info("Max watched roots set to "+strconv.Itoa(max), "max_watchers", max)
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

func (g *BasicSearch) Close() {
	if g.watcher != nil {
		g.watcher.Close()
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
	return fullSearchDir, nil
}

func createBasicSearch() *BasicSearch {
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      hclog.Info,
		Output:     os.Stderr,
		JSONFormat: true,
	})

	watcher, err := watch.New()
	if err != nil {
		logger.Error("Could not create file watcher. Index will run frequently.", "error", err)
		watcher = nil
	}

	maxW := int32(config.DefaultMaxWatchers)
	if mwStr := os.Getenv("POGO_MAX_WATCHERS"); mwStr != "" {
		if mw, err := strconv.Atoi(mwStr); err == nil && mw > 0 {
			maxW = int32(mw)
		}
	}

	maxF := int32(config.DefaultMaxFilesPerTree)
	if mfStr := os.Getenv("POGO_MAX_FILES_PER_TREE"); mfStr != "" {
		if mf, err := strconv.Atoi(mfStr); err == nil && mf > 0 {
			maxF = int32(mf)
		}
	}

	basicSearch := &BasicSearch{
		logger:          logger,
		projects:        make(map[string]IndexedProject),
		watcher:         watcher,
		updater:         nil,
		maxWatchers:     maxW,
		maxFilesPerTree: maxF,
	}
	basicSearch.updater = basicSearch.newProjectUpdater()

	if UseWatchers && watcher != nil {
		go func() {
			for {
				select {
				case event, ok := <-watcher.Events():
					if !ok {
						logger.Warn("File watcher event channel closed")
						return
					}
					if event.Has(watch.Create) || event.Has(watch.Remove) || event.Has(watch.Rename) || event.Has(watch.Write) {
						logger.Info("File update: " + event.String())
						// Check if this is a file or directory event.
						// For files, use ReIndexFile to avoid a full directory walk.
						fileInfo, statErr := os.Lstat(event.Path)
						isDir := statErr == nil && fileInfo.IsDir()
						if isDir {
							basicSearch.ReIndex(event.Path)
						} else {
							basicSearch.ReIndexFile(event.Path, event.Op)
						}
					}
				case err, ok := <-watcher.Errors():
					if !ok {
						return
					}
					logger.Error("File watcher error", "error", err)
				}
			}
		}()
	}
	return basicSearch
}
