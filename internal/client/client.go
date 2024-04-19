////////////////////////////////////////////////////////////////////////////////
////////// Http client for pogod ///////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
	"github.com/drellem2/pogo/internal/project"
)

type ClientResp interface {
	[]project.Project | *project.VisitResponse | *SearchResponse | []string
}

type PogoChunkMatch struct {
	Line uint32 `json:"line"`
	Content string `json:"content"`
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

type SearchResponse struct {
	Index   IndexedProject `json:"index"`
	Results SearchResults  `json:"results"`
	Error   string         `json:"error"`
}

type SearchRequest struct {
	// Values: "search" or "files"
	Type        string `json:"type"`
	ProjectRoot string `json:"projectRoot"`
	// Command timeout duration - only for 'search'-type requests
	Duration string `json:"string"`
	Data     string `json:"data"`
}

func HealthCheck() error {
	_, err := http.Post("http://localhost:10000/health", "application/json",
		nil)
	return err
}

func StartServer() error {
	// Store the result of os.exec("pogod") in a variable and describe its type
	// If the type is a pointer to a process, then the server is running
	cmd := exec.Command("pogod")
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

// Run closure with health check
func RunWithHealthCheck[T ClientResp](run func() (T, error)) (T, error) {
	err := HealthCheck()
	if err != nil {
		err = StartServer()
		if err != nil {
			return nil, err
		}
		success := false
		// Loop for up to half a second to check if the server is running
		// Get current time
		startTime := time.Now()
		// Inside for loop, check current time against startTime
		for ;time.Now().Sub(startTime) < 2000*time.Millisecond; {
			err = HealthCheck()
			if err == nil {				
				fmt.Println("Health check successful")
				success = true
				break
			}
			// Wait 500ms to give the server time to start
			time.Sleep(500 * time.Millisecond)
		}
		if !success {
			return nil, err
		}
	}	
	return run()
}

func GetProjects() ([]project.Project, error) {
	projs, err := RunWithHealthCheck(func() ([]project.Project, error) {
		r, err := http.Get("http://localhost:10000/projects")
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		// Deserialize projResp
		// Do json demarshal from http response
		var projs []project.Project
		err = json.Unmarshal(body, &projs)
		if err != nil {
			return nil, err
		}
		return projs, nil
	})
	if err != nil {
		return nil, err
	}
	return projs, nil
}

func GetPlugins() ([]string, error) {
	plugins, err := RunWithHealthCheck(func() ([]string, error) {
		r, err := http.Get("http://localhost:10000/plugins")
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		// Deserialize projResp
		// Do json demarshal from http response
		var plugins []string
		err = json.Unmarshal(body, &plugins)
		if err != nil {
			return nil, err
		}
		return plugins, nil
	})
	if err != nil {
		return nil, err
	}
	return plugins, nil
}

func GetSearchPlugin() (string, error) {
	plugins, err := GetPlugins()
	if err != nil {
		return "", err
	}
	for _, plugin := range plugins {
		if strings.Contains(plugin, "pogo-plugin-search") {
			return plugin, nil
		}
	}
	return "", errors.New("search plugin not found")
}

// dir may be inside of a project path. First we have to look up the
func Search(query string, dir string) (*SearchResponse, error) {
	// corresponding project root, if any
	projectResp, err := Visit(dir)
	if err != nil {
		return nil, err
	}
	if projectResp == nil {
		return nil, errors.New("response nil")
	}
	projectRoot := projectResp.ParentProject.Path
	searchPluginPath, err := GetSearchPlugin()
	if err != nil {
		return nil, err
	}
	var searchRequest = SearchRequest{
		Type:        "search",
		ProjectRoot: projectRoot,
		Duration:    "10s",
		Data:        query,
	}
	// Marshal searchRequest to json
	searchRequestJson, err := json.Marshal(searchRequest)
	if err != nil {
		return nil, err
	}
	// urlencode searchRequestJson
	encodedRequest := url.QueryEscape(string(searchRequestJson))
	
	results, err := RunWithHealthCheck(func() (*SearchResponse, error) {
		client := &http.Client{}
		
		req, err := http.NewRequest("POST", "http://localhost:10000/plugin",
			strings.NewReader(
				fmt.Sprintf(`{"plugin": "%s",
                                              "value": "%s"}`,
					searchPluginPath,
					encodedRequest)))
		if err != nil {
			return nil, err
		}
		req.Close = true
		req.Header.Set("Content-Type", "application/json")
		r, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		// Deserialize projResp
		// Do json demarshal from http response
		var dataObject pogoPlugin.DataObject
		err = json.Unmarshal(body, &dataObject)
		if err != nil {
			return nil, err
		}
		respJson, err := url.QueryUnescape(dataObject.Value)
		if err != nil {
			return nil, err
		}
		var results SearchResponse
		err = json.Unmarshal([]byte(respJson), &results)
		if err != nil {
			return nil, err
		}
		return &results, nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func Visit(path string) (*project.VisitResponse, error) {
	visitResp, err := RunWithHealthCheck(func() (*project.VisitResponse, error) {
		r, err := http.Post("http://localhost:10000/file",
			"application/json",
			strings.NewReader(
				fmt.Sprintf(`{"path": "%s"}`, path)))
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var resp project.VisitResponse
		err = json.Unmarshal(body, &resp)
		if err != nil {
			return nil, err
		}		
		return &resp, nil
	})
	if err != nil {
		return nil, err
	}
	return visitResp, nil
}
