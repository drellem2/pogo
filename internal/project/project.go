// //////////////////////////////////////////////////////////////////////////////
// //////// Maintains a list of projects visited by the user. ///////////////////
// //////// Author: drellem    Date: 2022-10-15               ///////////////////
// //////////////////////////////////////////////////////////////////////////////
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-plugin"

	pogoPlugin "github.com/marginalia-gaming/pogo/plugin"
)

type Project struct {
	Id   int    `json:"id"`
	Path string `json:"path"`
}

type ProjectsSave struct {
	Projects []Project `json:"projects"`
}

var projectFile string
var projects []Project
var client *plugin.Client

// For now a noop.
func Init(client2 *plugin.Client) {
	client = client2
	projects = []Project{}
	home := os.Getenv("POGO_HOME")
	if home == "" {
		home = "."
	}
	fmt.Printf("POGO_HOME=%s\n", home)
	projectFile = home + "/projects.json"
	_, err := os.Lstat(projectFile)
	skipImport := false
	if err != nil {
		skipImport = true
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Printf("Error getting file info %v", err)
		}
		fmt.Printf("Save file %s does not exist.", projectFile)
	}
	if !skipImport {
		file, err2 := os.Open(projectFile)
		if err2 != nil {
			fmt.Printf("Error opening file info")
			return
		}
		defer file.Close()
		byteValue, _ := ioutil.ReadAll(file)
		var projectsStruct ProjectsSave
		json.Unmarshal(byteValue, &projectsStruct)
		projects = projectsStruct.Projects
		for _, p := range projects {
			addToPlugin(p)
		}
	}
}

func SaveProjects() {
	fmt.Printf("Saving projects to %s", projectFile)
	projectsStruct2 := ProjectsSave{Projects: projects}
	outBytes, err := json.Marshal(&projectsStruct2)
	if err != nil {
		fmt.Printf("Error saving projects: %v", err)
		return
	}
	err2 := os.WriteFile(projectFile, outBytes, 0644)
	if err2 != nil {
		fmt.Printf("Error saving projects: %v", err2)
		return
	}
}

const visitResponseRelativePath = "'path' cannot be relative."
const visitResponsePathMissing = "'path' field missing."

type ErrorResponse struct {
	Code    int
	Message string `json:"errorString"`
}

var internalErrorResponse = ErrorResponse{
	Code:    http.StatusInternalServerError,
	Message: "An internal error was encountered.",
}

var notFoundErrorResponse = ErrorResponse{
	Code:    http.StatusNotFound,
	Message: "The resource was not found.",
}

type VisitRequest struct {
	Path string `json:"path"`
}

type VisitResponse struct {
	ParentProject Project `json:"project"`
}

func Projects() []Project {
	return projects
}

func addToPlugin(p Project) {
	// Connect via RPC
	rpcClient, err := client.Client()
	if err != nil {
		log.Fatal(err)
	}

	// Request the plugin
	raw, err := rpcClient.Dispense("basicSearch")
	if err != nil {
		log.Fatal(err)
	}

	basicSearch := raw.(pogoPlugin.IPogoPlugin)
	req := pogoPlugin.ProcessProjectReq{PathVar: p.Path}
	ireq := pogoPlugin.IProcessProjectReq(req)
	err = basicSearch.ProcessProject(&ireq)
	if err != nil {
		log.Fatal(err)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func Add(p *Project) {
	if len(projects) == 0 {
		(*p).Id = 1
	} else {
		(*p).Id = projects[len(projects)-1].Id + 1
	}
	addToPlugin(*p)
	projects = append(projects, *p)
}

func AddAll(ps []Project) {
	var start int
	if len(projects) == 0 {
		start = 1
	} else {
		start = projects[len(projects)-1].Id + 1
	}
	for i, elem := range ps {
		elem.Id = start + i
		addToPlugin(elem)
	}
	projects = append(projects, ps...)
}

// If the path is within an existing project, returns the project. Otherwise
// traverses parent directories and creates a project if one is found.
func Visit(req VisitRequest) (*VisitResponse, *ErrorResponse) {
	path := req.Path

	if path == "" {
		err := new(ErrorResponse)
		err.Code = http.StatusBadRequest
		err.Message = visitResponsePathMissing
		return nil, err
	}

	// If within project, set project
	// Else traverse parents
	// If within project and file, and not in gitignore, add to index

	if !filepath.IsAbs(path) {
		err := new(ErrorResponse)
		err.Code = http.StatusBadRequest
		err.Message = visitResponseRelativePath
		return nil, err
	}

	var parentProj *Project

	// Check if in existing project
	for _, proj := range projects {
		if strings.HasPrefix(path, proj.Path) {
			parentProj = &proj
			break
		}
	}

	if parentProj != nil {
		return &VisitResponse{
			ParentProject: *parentProj,
		}, nil
	}

	// TODO add the .git search up the tree, maybe add `created boolean` to response
	fileInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println(err.Error())
		return nil, &notFoundErrorResponse
	} else if err != nil {
		fmt.Println(err.Error())
		return nil, &internalErrorResponse
	}

	dirPath := path

	if !fileInfo.IsDir() {
		dirPath = filepath.Dir(path)
	}

	proj, err2 := searchAndCreate(dirPath)
	if err2 != nil {
		fmt.Println(err.Error())
		return nil, &internalErrorResponse
	}

	return &VisitResponse{
		ParentProject: *proj,
	}, nil
}

// Searches for git repo in parent
func searchAndCreate(path string) (*Project, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	dirnames, err2 := file.Readdirnames(0)
	if err2 != nil {
		return nil, err2
	}

	if hasPogoStop(dirnames) {
		return nil, nil
	}

	if hasGit(dirnames) {
		var project = Project{
			Id:   0,
			Path: path,
		}
		Add(&project)
		return &project, nil
	}

	dirPath := filepath.Dir(path)
	if dirPath == path {
		return nil, nil
	}

	return searchAndCreate(dirPath)
}

// Whether the subdirectories includes .git
func hasGit(dirnames []string) bool {
	for _, name := range dirnames {
		if name == ".git" {
			return true
		}
	}
	return false
}

// Whether directory includes .pogo_stop
func hasPogoStop(dirnames []string) bool {
	for _, name := range dirnames {
		if name == ".pogo_stop" {
			return true
		}
	}
	return false
}
