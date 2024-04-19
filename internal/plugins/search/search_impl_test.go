package search

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/kinbiko/jsonassert"

	"github.com/drellem2/pogo/pkg/plugin"
)

const aService = "_testdata/a-service/"

func init() {
	os.Chdir("..")
}

func setUp(t *testing.T) *BasicSearch {
	return createBasicSearch()
}

func cleanPogoFolder(t *testing.T, projectRoot string) {
	pogoFolder := filepath.Join(projectRoot, ".pogo")
	destroyFolder(pogoFolder, t)
}

func cleanUp(basicSearch *BasicSearch, t *testing.T, projectRoot string) {
	basicSearch.watcher.Close()
	cleanPogoFolder(t, projectRoot)
}

func destroyPath(filePath string, t *testing.T) {
	err := os.RemoveAll(filePath)
	if err != nil {
		t.Errorf("Failed to clean up file: %s", filePath)
		t.Errorf(err.Error())
	}
}

func destroyFolder(dirPath string, t *testing.T) {
	t.Logf("Destroy all %s", dirPath)
	err := os.RemoveAll(dirPath)
	if err != nil {
		t.Errorf("Failed to clean up file: %s", dirPath)
	}
}

func pathFormat(path string) string {
	path = strings.Replace(path, "/", string(os.PathSeparator), -1)
	if os.PathSeparator == '\\' {
		path = strings.Replace(path, "\\", "\\\\", -1)
	}
	return path
}

func TestSearch(t *testing.T) {
	aServicePath, err2 := absolute(aService)
	if err2 != nil {
		t.Errorf("Could not run tests, failed to construct absolute path of %s", aService)
		return
	}
	cleanPogoFolder(t, aServicePath)
	basicSearch := setUp(t)
	defer cleanUp(basicSearch, t, aServicePath)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: aServicePath})
	basicSearch.Index(&req)
	time.Sleep(1 * time.Second)
	// Make string to execute
	searchRequest := SearchRequest{
		Type:        "search",
		ProjectRoot: aServicePath,
		Data:        "query",
	}
	// Serialize searchRequest as json string
	searchRequestJson, err := json.Marshal(searchRequest)
	if err != nil {
		t.Errorf("Could not serialize search request as json")
		return
	}
	// urlEncode searchRequestJson
	searchRequestJsonUrlEncoded := url.QueryEscape(string(searchRequestJson))

	// Sleep 500ms to allow the search index to be built
	time.Sleep(500 * time.Millisecond)
	resp := basicSearch.Execute(searchRequestJsonUrlEncoded)
	if err != nil {
		t.Errorf("Could not execute search request")
		return
	}

	respDecoded, err := url.QueryUnescape(resp)
	if err != nil {
		t.Errorf("Could not url decode response")
		return
	}
	// Unmarshal respDecoded into type SearchResponse
	var searchResponse SearchResponse
	t.Logf("Search Response: %s", respDecoded)
	err = json.Unmarshal([]byte(respDecoded), &searchResponse)
	if err != nil {
		t.Errorf("Could not unmarshal response")
		return
	}
	// Print current directory
	d, _ := os.Getwd()
	d = pathFormat(d)
	// Replace forward slash with the os path separator

	expectedResTemplate := pathFormat(`
          {
            "index":{
              "root":"",
              "paths":[
                
              ]
            },
            "results":{
              "files":[
                {
                  "path":"src/a.c",
                  "matches":[
                    {
                      "line":2,
		      "content":"// Example query"
                    }
                  ]
                },
                {
                  "path":"README.md",
                  "matches":[
                    {
                      "line":3,
		      "content":"It will contain code to query."
                    }
                  ]
                }
              ]
            },
            "error":""
          }`)
	var buff bytes.Buffer
	templ := template.Must(template.New("Json Response").Parse(expectedResTemplate))
	err = templ.Execute(&buff, map[string]interface{}{
		"current_dir": d,
	})
	if err != nil {
		t.Errorf("Could not execute template")
		return
	}
	jsonassert.New(t).Assertf(respDecoded, buff.String())
}

func TestNewFileCausesReIndex(t *testing.T) {
	if !UseWatchers {
		return
	}
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
			cleanPogoFolder(t, aServicePath)
			basicSearch := setUp(t)
			defer cleanUp(basicSearch, t, aServicePath)
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
			time.Sleep(1 * time.Second)
			fileCount := len(basicSearch.projects[aServicePath].Paths)
			f, err := os.Create(fullPath)
			if err != nil {
				t.Errorf("Could not create file %s", fullPath)
				return
			}
			f.Close()
			success := false
			basicSearch.ReIndex(aServicePath)
			for i := 0; i < 10; i++ {
				time.Sleep(1 * time.Second)
				if len(basicSearch.projects[aServicePath].Paths) == fileCount+1 {
					success = true
					break
				}
			}

			if !success {
				t.Errorf("Error executing test %s", tt.name)
				t.Errorf("Expected %d files in index but found %d", fileCount+1, len(basicSearch.projects[aServicePath].Paths))
				t.Logf("Watch list: %v", basicSearch.watcher.WatchList())
				t.Logf("File: %v", basicSearch.projects[aServicePath].Paths)
				return
			}
		})
	}
}
