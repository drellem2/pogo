////////////////////////////////////////////////////////////////////////////////
////////// Maintains a list of projects visited by the user. ///////////////////
////////// Author: drellem    Date: 2022-10-15               ///////////////////
////////////////////////////////////////////////////////////////////////////////
package project

import (
	"errors"
	"fmt"
    "net/http"
	"os"
    "path/filepath"
    "strings"
)

type Project struct {
    Id int `json:"id"`
    Path string `json:"path"`
}

// For now a noop.
func Init() {
    
}

const visitResponseRelativePath = "'path' cannot be relative."
const visitResponsePathMissing = "'path' field missing."

type ErrorResponse struct {
     Code int
     Message string `json:"errorString"`
}

var internalErrorResponse = ErrorResponse{
	Code: http.StatusInternalServerError,
	Message: "An internal error was encountered.",
}

var notFoundErrorResponse = ErrorResponse{
	Code: http.StatusNotFound,
	Message: "The resource was not found.",
}

type VisitRequest struct {
     Path string `json:"path"`
}

type VisitResponse struct {
    ParentProject Project `json:"project"`	
}

// TODO: serialize/deserialize somewhere
var projects []Project

func Projects()  []Project {
     return projects
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
    for _, proj := range(projects) {
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

	dirnames, err2 := file.Readdirnames(0)
	if err2 != nil {
		return nil, err2
	}

	if hasPogoStop(dirnames) {
		return nil, nil
	}

	if hasGit(dirnames) {
		var project Project
		if len(projects) == 0 {
			project = Project{
				Id: 1,
				Path: path,
			}
		} else {
			project = Project{
				Id: projects[len(projects)-1].Id+1,
				Path: path,
			}
		}
		projects = append(projects, project)
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
	for _, name := range(dirnames) {
		if name == ".git" {
			return true
		}
	}
	return false
}

// Whether directory includes .pogo_stop
func hasPogoStop(dirnames []string) bool {
	for _, name := range(dirnames) {
		if name == ".pogo_stop" {
			return true
		}
	}
	return false
}
