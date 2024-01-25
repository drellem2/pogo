package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sabhiram/go-gitignore"
	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/query"

	pogoPlugin "github.com/drellem2/pogo/plugin"
)

const saveFileName = "search_index.json"
const codeSearchIndexFileName = "code_search_index"
const indexStartCapacity = 50

type PogoChunkMatch struct {
	Line uint32 `json:"line"`
	// TODO reenable content when I can get it to marshal without segfault
	Content []byte `json:"-"`
}

type PogoFileMatch struct {
	Path    string           `json:"path"`
	Matches []PogoChunkMatch `json:"matches"`
}

type SearchResults struct {
	Files []PogoFileMatch `json:"files"`
}

type IndexedProject struct {
	Root  string   `json:"root"`
	Paths []string `json:"paths"`
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
				g.projects[proj.Root] = *proj
				g.serializeProjectIndex(proj)
			case p := <-u.addFw:
				if g.watcher == nil {
					g.logger.Warn("watcher is nil")
				}
				w := g.watcher.Add(p)
				if w != nil {
					g.logger.Error("Error adding file watcher: %v", w)
				}
			case p := <-u.removeFw:
				if g.watcher == nil {
					g.logger.Warn("watcher is nil")
				}
				g.watcher.Remove(p)
			case <-u.quit:
				u.closed = true
			}
		}()
	}
}

// Should only be called by index
func (g *BasicSearch) indexRec(proj *IndexedProject, path string,
	gitIgnore *ignore.GitIgnore, u *ProjectUpdater) error {
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

		if !gitIgnore.MatchesPath(relativePath) && subFile != ".git" && subFile != ".pogo" {
			if fileInfo.IsDir() {
				u.addFw <- newPath
				err = g.indexRec(proj, newPath, gitIgnore, u)
				if err != nil {
					g.logger.Warn(err.Error())
				}
			} else {
				files = append(files, relativePath)
			}
		}
	}
	proj.Paths = append(proj.Paths, files...)
	return nil
}

// Try to index all files in the project, then create a code search index.
// The first is table stakes - so we error on failure. If the second fails, we log it and return.
func (g *BasicSearch) index(proj *IndexedProject, path string,
	gitIgnore *ignore.GitIgnore) {

	u := g.updater

	err := g.indexRec(proj, path, gitIgnore, u)
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
		for projectRoot, indexed := range g.projects {
			if strings.HasPrefix(fullPath, projectRoot) {
				/* Below is a golang idiom for removing
				elements with prefix from the slice. We
				want to remove all file watchers before
				reindexing, so we only add back the files
				that still exist. */
				relativePath := strings.TrimPrefix(fullPath, projectRoot)
				paths := indexed.Paths
				paths2 := paths
				paths = paths[:0]
				u := g.updater
				for _, p := range paths2 {
					if !strings.HasPrefix(p, relativePath) {
						paths = append(paths, p)
					} else {
						u.removeFw <- p
					}
				}
				indexed.Paths = paths

				gitIgnore, err := ParseGitIgnore(projectRoot)
				if err != nil {
					g.logger.Error("Error parsing gitignore %v", err)
				}
				g.index(&indexed, fullPath, gitIgnore)
				break
			}
		}
	}()
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
	_, ok := g.projects[path]
	if ok {
		g.logger.Info("Already indexed ", path)
		return
	}
	proj := IndexedProject{
		Root:  path,
		Paths: make([]string, 0, indexStartCapacity),
	}
	gitIgnore, err := ParseGitIgnore(path)
	if err != nil {
		// Non-fatal error
		g.logger.Error("Error parsing gitignore", err)
	}
	g.index(&proj, path, gitIgnore)
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
	g.logger.Info("Indexed " + strconv.Itoa(len(proj.Paths)) + " files for " + proj.Root)

	// Now serialize zoekt index

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
			bytes, err := ioutil.ReadFile(absPath)
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

func (g *BasicSearch) GetFiles(projectRoot string) (*IndexedProject, error) {
	project, ok := g.projects[projectRoot]
	if !ok {
		return nil, errors.New("Project not indexed " + projectRoot)
	}
	searchDir, err := project.makeSearchDir()
	if err != nil {
		g.logger.Error("Error making search dir: ", err)
		return nil, err
	}
	saveFilePath := filepath.Join(searchDir, saveFileName)
	_, err = os.Lstat(saveFilePath)
	file, err2 := os.Open(saveFilePath)
	if err2 != nil {
		g.logger.Error("Error opening index file.")
		return nil, err2
	}
	defer file.Close()
	byteValue, _ := ioutil.ReadAll(file)
	var indexStruct IndexedProject
	err = json.Unmarshal(byteValue, &indexStruct)
	if err != nil {
		g.logger.Error("Error deserializing index file: %v", err)
		return nil, err
	}
	return &indexStruct, nil
}

func (g *BasicSearch) Search(projectRoot string, data string, duration string) (*SearchResults, error) {
	project, ok := g.projects[projectRoot]
	if !ok {
		return nil, errors.New("Unknown project " + projectRoot)
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
		content := make([]byte, 0)
		chunkMatches := make([]PogoChunkMatch, len(file.ChunkMatches))
		for j, match := range file.ChunkMatches {
			if match.Content != nil && len(match.Content) > 0 {
				copy(content, match.Content)
			}
			chunkMatches[j] = PogoChunkMatch{
				Line:    match.ContentStart.LineNumber,
				Content: content,
			}
		}
		fileMatches[i] = PogoFileMatch{
			Path:    file.FileName,
			Matches: chunkMatches,
		}
	}
	return &SearchResults{
		Files: fileMatches,
	}, nil
}
