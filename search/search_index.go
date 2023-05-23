package main

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sabhiram/go-gitignore"

	pogoPlugin "github.com/marginalia-gaming/pogo/plugin"
)

const saveFileName = "search_index.json"
const indexStartCapacity = 50

type IndexedProject struct {
	Root  string   `json:"root"`
	Paths []string `json:"paths"`
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

func (g *BasicSearch) index(proj *IndexedProject, path string, gitIgnore *ignore.GitIgnore) error {
	e := g.watcher.Add(path)
	if e != nil {
		g.logger.Error("Error adding file watcher: %v")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	dirnames, err2 := file.Readdirnames(0)
	g.logger.Debug("Found dirs: ", dirnames)
	if err2 != nil {
		return err2
	}
	if len(dirnames) == 0 {
		return nil
	}
	files := make([]string, 0, len(dirnames)/2)
	for _, subFile := range dirnames {
		newPath := filepath.Join(path, subFile)
		fileInfo, err3 := os.Lstat(newPath)
		if err3 != nil {
			g.logger.Warn(err.Error())
			continue
		}
		// Remove projectRoot prefix from newPath
		relativePath := strings.TrimPrefix(newPath, proj.Root)
		
		if !gitIgnore.MatchesPath(relativePath) && subFile != ".git" && subFile != ".pogo" {
			if fileInfo.IsDir() {
				if g.watcher != nil {
					err = g.watcher.Add(newPath)
					if err != nil {
						g.logger.Error("Error adding file watcher: %v", err)
					}
				}
				err = g.index(proj, newPath, gitIgnore)
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

func (g *BasicSearch) ReIndex(path string) {
	fileInfo, e := os.Lstat(path)
	if e != nil {
		g.logger.Error("Error getting path info: ", e)
		return
	}
	if !fileInfo.IsDir() {
		path = filepath.Dir(path)
	}
	g.logger.Debug("Reindexing ", path)
	go func() {
		fullPath, err2 := absolute(path)
		if err2 != nil {
			g.logger.Error("Error getting absolute path", path)
			return
		}
		g.mu.Lock()
		for projectRoot, indexed := range g.projects {
			if strings.HasPrefix(fullPath, projectRoot) {
				relativePath := strings.TrimPrefix(fullPath, projectRoot)
				paths := indexed.Paths
				paths2 := paths
				paths = paths[:0]
				for _, p := range paths2 {
					if !strings.HasPrefix(p, relativePath) {
						paths = append(paths, p)
					} else if g.watcher != nil {
						g.watcher.Remove(p)
					}
				}
				indexed.Paths = paths

				gitIgnore, err := ParseGitIgnore(projectRoot)
				if err != nil {
					g.logger.Error("Error parsing gitignore %v", err)
				}
				err = g.index(&indexed, fullPath, gitIgnore)
				if err != nil {
					g.logger.Error("Error indexing updated path ", fullPath)
					g.logger.Error("Error: ", err)
				}
				g.projects[projectRoot] = indexed
				break
			}
		}
		g.mu.Unlock()
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

func (g *BasicSearch) Index(req *pogoPlugin.IProcessProjectReq) {
	path := (*req).Path()
	proj := IndexedProject{
		Root:  path,
		Paths: make([]string, 0, indexStartCapacity),
	}

	gitIgnore, err7 := ParseGitIgnore(path)
	if err7 != nil {
		g.logger.Error("Error parsing gitignore", err7)
	}

	err := g.index(&proj, path, gitIgnore)
	if err != nil {
		g.logger.Warn(err.Error())
	}
	g.mu.Lock()
	g.projects[path] = proj
	g.mu.Unlock()

	// Serialize index
	searchDir := makeSearchDir(path)
	saveFilePath := filepath.Join(searchDir, saveFileName)
	outBytes, err2 := json.Marshal(&proj)
	if err2 != nil {
		g.logger.Error("Error serializing index to json", "index", proj)
	}
	err3 := os.WriteFile(saveFilePath, outBytes, 0644)
	if err3 != nil {
		g.logger.Error("Error saving index", "save_path", saveFilePath)
	}
	g.logger.Info("Indexed " + strconv.Itoa(len(g.projects[path].Paths)) + " files for " + path)

}

func (g *BasicSearch) GetFiles(projectRoot string) (*IndexedProject, error) {
	searchDir := makeSearchDir(projectRoot)
	saveFilePath := filepath.Join(searchDir, saveFileName)
	_, err := os.Lstat(saveFilePath)
	skipImport := false
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			g.logger.Error("Error getting file info", "error", err)
			return nil, err
		}
		g.logger.Warn("Search index does not exist", "path", saveFilePath)
		skipImport = true
	}

	if skipImport {
		projectReq := pogoPlugin.ProcessProjectReq{PathVar: projectRoot}
		iProjectReq := pogoPlugin.IProcessProjectReq(projectReq)
		g.Index(&iProjectReq)
		return nil, nil
	}

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
