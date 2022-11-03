package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marginalia-gaming/pogo/plugin"
)

const aService = "_testdata/a-service/"

func init() {
	os.Chdir("..")
}

func setUp(t *testing.T) *BasicSearch {
	return createBasicSearch()
}

func cleanUp(basicSearch *BasicSearch, t *testing.T) {
	basicSearch.watcher.Close()
}

func destroyPath(filePath string, t *testing.T) {
	err := os.RemoveAll(filePath)
	if err != nil {
		t.Errorf("Failed to clean up file: %s", filePath)
	}
}

func destroyFolder(dirPath string, t *testing.T) {
	t.Logf("Destroy all %s", dirPath)
	err := os.RemoveAll(dirPath)
	if err != nil {
		t.Errorf("Failed to clean up file: %s", dirPath)
	}
}

func TestNewFileCausesReIndex(t *testing.T) {
	aServicePath, err2 := absolute(aService)
	if err2 != nil {
		t.Errorf("Could not run tests, failed to construct absolute path of %s", aService)
		return
	}
	tests := []struct {
		name         string
		dirs         []string
		filename     string
		destroy      bool // Whether to destroy dirs as well as filepath
		destroyIndex int
	}{
		{
			name:     "Top-level file",
			dirs:     []string{},
			filename: "README2.md",
			destroy:  false,
		},

		{
			name:     "File in existing directory",
			dirs:     []string{"src"},
			filename: "file.txt",
			destroy:  false,
		},
		{
			name:     "File in non-existing directory",
			dirs:     []string{"build"},
			filename: "a.out",
			destroy:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			basicSearch := setUp(t)
			defer cleanUp(basicSearch, t)
			// Create directories
			fullDirPath := filepath.Join(append([]string{aServicePath}, tt.dirs...)...)
			err := os.MkdirAll(fullDirPath, os.ModePerm)
			if err != nil {
				t.Errorf("Could not create directories %s", fullDirPath)
				return
			}
			fullPath := filepath.Join(fullDirPath, tt.filename)
			if tt.destroy {
				defer destroyFolder(filepath.Join(aServicePath, tt.dirs[0]), t)
			} else {
				defer destroyPath(fullPath, t)
			}
			req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: aServicePath})
			basicSearch.Index(&req)
			fileCount := len(basicSearch.projects[aServicePath].Paths)
			_, err = os.Create(fullPath)
			if err != nil {
				t.Errorf("Could not create file %s", fullPath)
				return
			}
			success := false

			for i := 0; i < 10; i++ {
				time.Sleep(1 * time.Second)
				if len(basicSearch.projects[aServicePath].Paths) == fileCount+1 {
					success = true
					break
				}
			}

			if !success {
				t.Errorf("Expected %d files in index but found %d", fileCount+1, len(basicSearch.projects[aServicePath].Paths))
				t.Logf("Watch list: %v", basicSearch.watcher.WatchList())
				t.Logf("File: %v", basicSearch.projects[aServicePath].Paths)
				return
			}

		})

	}
}
