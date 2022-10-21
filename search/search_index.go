package main

import (
	"os"
	"path/filepath"
	"strconv"

	pogoPlugin "github.com/marginalia-gaming/pogo/plugin"
)

const indexStartCapacity = 50

type IndexedProject struct {
	Root  string   `json:"root"`
	Paths []string `json:"paths"`
}

func (g *BasicSearch) index(proj *IndexedProject, path string) error {
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
		if fileInfo.IsDir() {
			err = g.index(proj, newPath)
			if err != nil {
				g.logger.Warn(err.Error())
			}
		} else {
			files = append(files, newPath)
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
		err := g.index(&proj, path)
		if err != nil {
			g.logger.Warn(err.Error())
		}
		g.projects[path] = proj
		g.logger.Info("Indexed " + strconv.Itoa(len(g.projects[path].Paths)) + " files for " + path)
	}()
}
