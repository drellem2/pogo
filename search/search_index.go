package main

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"

	"github.com/sabhiram/go-gitignore"

	pogoPlugin "github.com/marginalia-gaming/pogo/plugin"
)

const saveFileName = "search_index.json"
const indexStartCapacity = 50

type IndexedProject struct {
	Root  string   `json:"root"`
	Paths []string `json:"paths"`
}

func (g *BasicSearch) index(proj *IndexedProject, path string, gitIgnore *ignore.GitIgnore) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	dirnames, err2 := file.Readdirnames(0)
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
		if !gitIgnore.MatchesPath(newPath) && subFile != ".git" && subFile != ".pogo" {
			if fileInfo.IsDir() {
				err = g.index(proj, newPath, gitIgnore)
				if err != nil {
					g.logger.Warn(err.Error())
				}
			} else {
				files = append(files, newPath)
			}
		}
	}
	proj.Paths = append(proj.Paths, files...)
	return nil
}

func (g *BasicSearch) Index(req *pogoPlugin.IProcessProjectReq) {
	go func() {
		path := (*req).Path()
		proj := IndexedProject{
			Root:  path,
			Paths: make([]string, 0, indexStartCapacity),
		}

		// Read .gitignore if exists
		ignorePath := filepath.Join(path, ".gitignore")
		_, err4 := os.Lstat(ignorePath)
		var gitIgnore *ignore.GitIgnore
		if err4 != nil {
			if !errors.Is(err4, os.ErrNotExist) {
				g.logger.Error("Error getting file info for gitignore %v", err4)
			}
			g.logger.Info(".gitignore does not exist. Skipping.\n")
			gitIgnore = ignore.CompileIgnoreLines("")
		} else {
			gitIgnore, err4 = ignore.CompileIgnoreFile(ignorePath)
			if err4 != nil {
				g.logger.Error("Error parsing gitIgnore %v", err4)
				gitIgnore = ignore.CompileIgnoreLines("")
			} 
		}
		
		err := g.index(&proj, path, gitIgnore)
		if err != nil {
			g.logger.Warn(err.Error())
		}
		g.projects[path] = proj

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

	}()
}

func (g *BasicSearch) GetFiles(projectRoot string) (*IndexedProject, error) {
	searchDir := makeSearchDir(projectRoot)
	saveFilePath := filepath.Join(searchDir, saveFileName)
	_, err := os.Lstat(saveFilePath)
	skipImport := false
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			g.logger.Error("Error getting file info", "error", err)
		}
		g.logger.Warn("Search index does not exist", "path", saveFilePath)
		return nil, err
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
		g.logger.Error("Error deserializing index file.")
		return nil, err
	}
	return &indexStruct, nil
}
