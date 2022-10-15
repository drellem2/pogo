////////////////////////////////////////////////////////////////////////////////
////////// Maintains a list of projects visited by the user. ///////////////////
////////// Author: drellem    Date: 2022-10-15              ///////////////////
////////////////////////////////////////////////////////////////////////////////
package pogo

import (
    "net/http"
    "path/filepath"
    "strings"
)

type Project struct {
    Id int `json:"id"`
    Path string `json:"path"`
}

// For now a test function.
func Init() {
    projects = []Project {
        Project { Id: 1, Path: "/Users/drellem/dev/pogod" },
	Project { Id: 2, Path: "/Users/drellem/dev/interpreter" },
    }
}

const visitResponseRelativePath = "'path' cannot be relative."
const visitResponsePathMissing = "'path' field missing."

type ErrorResponse struct {
     Code int
     Message string `json:"errorString"`
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
    
    return nil, nil
}
