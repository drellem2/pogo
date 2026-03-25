package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sabhiram/go-gitignore"
	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/query"

	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

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
	StatusUnindexed IndexingStatus = "unindexed"
	StatusIndexing  IndexingStatus = "indexing"
	StatusReady     IndexingStatus = "ready"
	StatusStale     IndexingStatus = "stale"
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

// computeFileHash returns the hex-encoded SHA-256 hash of a file's contents.
func computeFileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

/*
*

	Contains channels that can be written to in order to update the project.
*/
type ProjectUpdater struct {
	c        chan *IndexedProject
	addFw    chan string
	removeFw chan string
	quit     chan bool
	closed   bool
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
		c:        make(chan *IndexedProject),
		addFw:    make(chan string),
		removeFw: make(chan string),
		quit:     make(chan bool),
		closed:   false,
	}
	go g.write(u)
	return u
}

func (g *BasicSearch) write(u *ProjectUpdater) {
	for !u.closed {
		func() {
			select {
			case proj := <-u.c:
				g.mu.Lock()
				g.projects[proj.Root] = *proj
				g.mu.Unlock()
				g.serializeProjectIndex(proj)
			case p := <-u.addFw:
				if g.watcher == nil {
					g.logger.Warn("watcher is nil")
				} else if max := g.maxWatchers; max > 0 && g.watchCount.Load() >= max {
					g.logger.Warn("File watcher limit reached, skipping watch",
						"path", p, "limit", max)
				} else {
					w := g.watcher.Add(p)
					if w != nil {
						g.logger.Error("Error adding file watcher: %v", w)
					} else {
						g.watchCount.Add(1)
					}
				}
			case p := <-u.removeFw:
				if g.watcher == nil {
					g.logger.Warn("watcher is nil")
				}
				err := g.watcher.Remove(p)
				if err == nil {
					g.watchCount.Add(-1)
				}
			case <-u.quit:
				u.closed = true
			}
		}()
	}
}

// Should only be called by index.
// prevHashes and prevMtimes hold data from a previous index run; when a file's
// mtime has not changed we reuse the old hash instead of re-reading the file.
func (g *BasicSearch) indexRec(proj *IndexedProject, path string,
	gitIgnore *ignore.GitIgnore, u *ProjectUpdater,
	prevHashes map[string]string, prevMtimes map[string]int64) error {
	// First index all files in the project
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	dirnames, err := file.Readdirnames(0)
	g.logger.Debug("Found dirs: ", dirnames)
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

		if !gitIgnore.MatchesPath(relativePath) && subFile != ".git" && subFile != ".pogo" && subFile != "node_modules" {
			if fileInfo.IsDir() {
				u.addFw <- newPath
				err = g.indexRec(proj, newPath, gitIgnore, u, prevHashes, prevMtimes)
				if err != nil {
					g.logger.Warn(err.Error())
				}
			} else {
				files = append(files, relativePath)
				mtime := fileInfo.ModTime().UnixNano()
				proj.FileMtimes[relativePath] = mtime

				// If mtime unchanged and we have a previous hash, reuse it
				if oldMtime, ok := prevMtimes[relativePath]; ok && oldMtime == mtime {
					if oldHash, hok := prevHashes[relativePath]; hok {
						proj.FileHashes[relativePath] = oldHash
						continue
					}
				}
				// File is new or modified — compute hash
				if hash, herr := computeFileHash(newPath); herr == nil {
					proj.FileHashes[relativePath] = hash
				}
			}
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

	err := g.indexRec(proj, path, gitIgnore, u, prevHashes, prevMtimes)
	if err != nil {
		g.logger.Warn("Error indexing project: ", err.Error())
		return
	}
	u.c <- proj
}

func (g *BasicSearch) ReIndex(path string) {
	fileInfo, e := os.Lstat(path)
	if e != nil {
		g.logger.Error("Error getting path info: ", e)
		return
	}
	if !fileInfo.IsDir() {
		path = filepath.Dir(path)
	}
	g.logger.Info("Reindexing ", path)
	go func() {
		fullPath, err2 := absolute(path)
		if err2 != nil {
			g.logger.Error("Error getting absolute path", path)
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

		/* Below is a golang idiom for removing
		elements with prefix from the slice. We
		want to remove all file watchers before
		reindexing, so we only add back the files
		that still exist. */
		relativePath := strings.TrimPrefix(fullPath, matchedRoot)
		paths := indexed.Paths
		paths2 := paths
		paths = paths[:0]
		u := g.updater
		for _, p := range paths2 {
			if !strings.HasPrefix(p, relativePath) {
				paths = append(paths, p)
			} else {
				u.removeFw <- p
				delete(indexed.FileHashes, p)
				delete(indexed.FileMtimes, p)
			}
		}
		indexed.Paths = paths

		gitIgnore, err := ParseGitIgnore(matchedRoot)
		if err != nil {
			g.logger.Error("Error parsing gitignore %v", err)
		}
		g.index(&indexed, fullPath, gitIgnore, indexed.FileHashes, indexed.FileMtimes)
	}()
}

// ReIndexFile handles a single-file event without walking the directory tree.
// This avoids a full directory re-walk when only one file changed.
func (g *BasicSearch) ReIndexFile(path string, op fsnotify.Op) {
	fullPath, err := filepath.Abs(path)
	if err != nil {
		g.logger.Error("Error getting absolute path: ", path)
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

	relativePath := strings.TrimPrefix(fullPath, matchedRoot)
	u := g.updater
	changed := false

	if indexed.FileMtimes == nil {
		indexed.FileMtimes = make(map[string]int64)
	}

	if op.Has(fsnotify.Remove) || op.Has(fsnotify.Rename) {
		// Remove file from index
		paths := indexed.Paths[:0]
		for _, p := range indexed.Paths {
			if p != relativePath {
				paths = append(paths, p)
			}
		}
		if len(paths) != len(indexed.Paths) {
			indexed.Paths = paths
			delete(indexed.FileHashes, relativePath)
			delete(indexed.FileMtimes, relativePath)
			u.removeFw <- fullPath
			changed = true
		}
	} else if op.Has(fsnotify.Create) || op.Has(fsnotify.Write) {
		// Check gitignore
		gitIgnore, _ := ParseGitIgnore(matchedRoot)
		if gitIgnore.MatchesPath(relativePath) {
			return
		}

		hash, herr := computeFileHash(fullPath)
		if herr != nil {
			g.logger.Error("Error computing file hash: ", herr)
			return
		}

		// Skip if content unchanged
		if oldHash, exists := indexed.FileHashes[relativePath]; exists && oldHash == hash {
			g.logger.Debug("File unchanged (hash match), skipping reindex: ", relativePath)
			return
		}

		indexed.FileHashes[relativePath] = hash
		if fi, serr := os.Lstat(fullPath); serr == nil {
			indexed.FileMtimes[relativePath] = fi.ModTime().UnixNano()
		}

		if op.Has(fsnotify.Create) {
			// Add to paths if not already present
			found := false
			for _, p := range indexed.Paths {
				if p == relativePath {
					found = true
					break
				}
			}
			if !found {
				indexed.Paths = append(indexed.Paths, relativePath)
				u.addFw <- fullPath
			}
		}
		changed = true
	}

	if changed {
		g.logger.Info("File changed, reindexing: ", relativePath)
		u.c <- &indexed
	}
}

/*
Even if this function encounters an error, it will always at least return a
GitIgnore that matches nothing.
*/
func ParseGitIgnore(path string) (*ignore.GitIgnore, error) {
	// Read .gitignore if exists
	ignorePath := filepath.Join(path, ".gitignore")
	var err error
	_, err = os.Lstat(ignorePath)
	var gitIgnore *ignore.GitIgnore
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		gitIgnore = ignore.CompileIgnoreLines("")
	} else {
		gitIgnore, err = ignore.CompileIgnoreFile(ignorePath)
		if err != nil {
			gitIgnore = ignore.CompileIgnoreLines("")
		}
	}
	return gitIgnore, err
}

func (g *BasicSearch) deleteIndexFile(p *IndexedProject) error {
	searchDir, err := p.makeSearchDir()
	if err != nil {
		g.logger.Error("Error making search dir: ", err)
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
		g.logger.Error("Error making search dir: ", err)
		return nil, err
	}
	indexPath := filepath.Join(searchDir, filename)
	indexFile, err := os.OpenFile(indexPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		g.logger.Error("Error opening index file ", path)
		return nil, err
	}
	return indexFile, nil
}

func (g *BasicSearch) getIndexFile(p *IndexedProject) (*os.File, error) {
	return g.getSearchFile(p, codeSearchIndexFileName)
}

func (g *BasicSearch) Index(req *pogoPlugin.IProcessProjectReq) {
	path := (*req).Path()
	g.mu.RLock()
	p, ok := g.projects[path]
	g.mu.RUnlock()
	if ok && p.Status == StatusReady && p.Paths != nil && len(p.Paths) > 0 {
		g.logger.Info("Already indexed ", path)
		return
	}

	// Preserve previous hashes/mtimes for incremental indexing
	var prevHashes map[string]string
	var prevMtimes map[string]int64
	if ok && len(p.FileHashes) > 0 {
		prevHashes = p.FileHashes
		prevMtimes = p.FileMtimes
		g.logger.Info("Incremental index: reusing ", strconv.Itoa(len(prevHashes)), " cached hashes for ", path)
	}

	proj := IndexedProject{
		Root:       path,
		Paths:      make([]string, 0, indexStartCapacity),
		FileHashes: make(map[string]string),
		FileMtimes: make(map[string]int64),
		Status:     StatusIndexing,
	}
	g.mu.Lock()
	g.projects[path] = proj
	g.mu.Unlock()
	gitIgnore, err := ParseGitIgnore(path)
	if err != nil {
		// Non-fatal error
		g.logger.Error("Error parsing gitignore", err)
	}
	g.index(&proj, path, gitIgnore, prevHashes, prevMtimes)
}

// Here is the method where we extract the code above
func (g *BasicSearch) serializeProjectIndex(proj *IndexedProject) {
	searchDir, err := proj.makeSearchDir()
	if err != nil {
		g.logger.Error("Error making search dir: ", err)
		return
	}
	saveFilePath := filepath.Join(searchDir, saveFileName)
	outBytes, err2 := json.Marshal(proj)
	if err2 != nil {
		g.logger.Error("Error serializing index to json", "index", *proj)
	}
	err3 := os.WriteFile(saveFilePath, outBytes, 0644)
	if err3 != nil {
		g.logger.Error("Error saving index", "save_path", saveFilePath)
	}
	// Check if file content actually changed by comparing hashes with previous index.
	// If nothing changed and the zoekt index file exists, skip the expensive rebuild.
	g.mu.RLock()
	oldProj, exists := g.projects[proj.Root]
	g.mu.RUnlock()
	contentChanged := !exists || len(oldProj.FileHashes) != len(proj.FileHashes)
	if !contentChanged && oldProj.FileHashes != nil {
		for path, hash := range proj.FileHashes {
			if oldHash, ok := oldProj.FileHashes[path]; !ok || oldHash != hash {
				contentChanged = true
				break
			}
		}
	}

	// Capture the current git tree hash so we can detect changes on next Load.
	if h, err := gitTreeHash(proj.Root); err == nil {
		proj.GitTreeHash = h
	} else {
		g.logger.Warn("Could not read git tree hash for " + proj.Root + ": " + err.Error())
	}

	proj.Status = StatusReady
	g.mu.Lock()
	g.projects[proj.Root] = *proj
	g.mu.Unlock()
	g.logger.Info("Indexed " + strconv.Itoa(len(proj.Paths)) + " files for " + proj.Root)

	if !contentChanged {
		// Verify zoekt index file actually exists before skipping rebuild
		indexPath := filepath.Join(searchDir, codeSearchIndexFileName)
		if _, err := os.Lstat(indexPath); err == nil {
			g.logger.Info("No content changes detected, skipping zoekt rebuild for " + proj.Root)
			return
		}
		g.logger.Info("Zoekt index missing, rebuilding for " + proj.Root)
	}

	// Now serialize zoekt index

	// First delete the old index
	g.deleteIndexFile(proj)

	indexer, err := zoekt.NewIndexBuilder(nil)
	if err != nil {
		g.logger.Error("Error creating search index")
		return
	}

	// Next create the code search index
	// TODO - add some useful repository metadata
	for _, path := range proj.Paths {
		// Prepend Root to path
		fullPath := filepath.Join(proj.Root, path)
		absPath, err := absolute(fullPath)
		if err != nil {
			g.logger.Error("Error getting absolute path - file may not exist", path)
		} else {
			bytes, err := os.ReadFile(absPath)
			if err != nil {
				g.logger.Error("Error reading file ", absPath)
			} else {
				indexer.AddFile(absPath, bytes)
			}
		}
	}
	indexFile, err := g.getIndexFile(proj)
	if err != nil {
		g.logger.Error("Error getting index file ", proj.Root)
		return
	}
	defer indexFile.Close()
	err = indexer.Write(indexFile)
	if err != nil {
		g.logger.Error("Error writing index file ", proj.Root)
		g.logger.Error("Error: ", err.Error())
		return
	}
}

func (g *BasicSearch) Load(projectRoot string) (*IndexedProject, error) {
	project := &IndexedProject{
		Root:   projectRoot,
		Paths:  make([]string, 0, indexStartCapacity),
		Status: StatusUnindexed,
	}
	searchDir, err := project.makeSearchDir()
	if err != nil {
		g.logger.Error("Error making search dir: ", err)
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
		g.logger.Error("Error deserializing index file: %v", err)
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
		g.logger.Warn("Could not read git tree hash for " + projectRoot + ": " + hashErr.Error())
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
	g.logger.Info("Loaded " + strconv.Itoa(len(project.Paths)) + " files for " + projectRoot)
	g.updater.c <- project
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
		g.logger.Error("Error making search dir: ", err)
		return nil, err
	}
	indexPath := filepath.Join(searchDir, codeSearchIndexFileName)
	indexFile, err := os.Open(indexPath)
	if err != nil {
		g.logger.Error("Error opening index file ", indexPath)
		return nil, err
	}
	defer indexFile.Close()
	index, err2 := zoekt.NewIndexFile(indexFile)
	if err2 != nil {
		g.logger.Error("Error reading index file ", indexPath)
		return nil, err2
	}
	// Search
	searcher, err := zoekt.NewSearcher(index)
	if err != nil {
		g.logger.Error("Error creating searcher", err)
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
