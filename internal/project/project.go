// //////////////////////////////////////////////////////////////////////////////
// //////// Maintains a list of projects visited by the user. ///////////////////
// //////// Author: drellem    Date: 2022-10-15               ///////////////////
// //////////////////////////////////////////////////////////////////////////////
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-hclog"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/search"
	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
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
var ProjectFileName string

// indexRoots is the optional index-roots allowlist. When non-empty, only git
// repos under one of these paths may be auto-registered. Empty (the default)
// keeps pogo's zero-config behavior. Configured via SetIndexRoots.
var indexRoots []string

var logger = hclog.New(&hclog.LoggerOptions{
	Level:      hclog.Info,
	Output:     os.Stderr,
	JSONFormat: true,
})

func Init() {
	projects = []Project{}
	// config.PogoHome resolves $POGO_HOME (default ~/.pogo) with the legacy
	// POGO_HOME=$HOME value normalized to $HOME/.pogo. Pre-mg-ff8b this fell
	// back to "." which silently scattered per-shell projects.json files
	// wherever pogod was first launched.
	home := config.PogoHome()
	fmt.Printf("POGO_HOME=%s\n", home)
	if ProjectFileName == "" {
		ProjectFileName = "projects.json"
	}
	projectFile = filepath.Join(home, ProjectFileName)
	migrateLegacyProjectFile(projectFile)
	if err := os.MkdirAll(home, 0755); err != nil {
		fmt.Printf("Error creating pogo home %s: %v", home, err)
	}
	_, err := os.Lstat(projectFile)
	skipImport := false
	if err != nil {
		skipImport = true
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Printf("Error getting file info %v", err)
		}
		fmt.Printf("Save file %s does not exist.\n", projectFile)
		// Create the file
		_, err2 := os.Create(projectFile)
		if err2 != nil {
			fmt.Printf("Error creating file %v", err2)
			return
		}

	}
	if !skipImport {
		file, err2 := os.Open(projectFile)
		if err2 != nil {
			fmt.Printf("Error opening file info")
			return
		}
		defer file.Close()
		byteValue, _ := io.ReadAll(file)
		var projectsStruct ProjectsSave
		json.Unmarshal(byteValue, &projectsStruct)
		projects = projectsStruct.Projects
	}
}

// migrateLegacyProjectFile copies projects.json from a raw $POGO_HOME that
// config.PogoHome normalized away (the legacy POGO_HOME=$HOME layout kept
// projects.json at the home dir root). One-time and best-effort: it only
// runs when the canonical file is absent and the legacy one exists, so the
// visited-projects registry survives the normalization instead of silently
// resetting (mg-3dc3). The legacy file is left in place.
func migrateLegacyProjectFile(canonical string) {
	raw := os.Getenv("POGO_HOME")
	if raw == "" || filepath.Clean(raw) == filepath.Clean(config.PogoHome()) {
		return
	}
	if _, err := os.Lstat(canonical); !errors.Is(err, os.ErrNotExist) {
		return
	}
	legacy := filepath.Join(raw, ProjectFileName)
	data, err := os.ReadFile(legacy)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(canonical), 0755); err != nil {
		return
	}
	if err := os.WriteFile(canonical, data, 0644); err == nil {
		fmt.Printf("Migrated %s to %s\n", legacy, canonical)
	}
}

// IndexAll processes all loaded projects through plugins in the background.
// Call this after Init() and after the HTTP server is listening.
func IndexAll() {
	for _, p := range projects {
		addToPlugin(p)
	}
}

// SetIndexRoots configures the optional index-roots allowlist. When roots is
// empty, auto-registration keeps its default zero-config behavior.
func SetIndexRoots(roots []string) {
	indexRoots = roots
	if len(roots) > 0 {
		logger.Info("project: index-roots allowlist active", "roots", roots)
	}
}

// PruneRegistry drops stale registry entries. Call it once on startup, after
// Init() and before IndexAll(). It removes:
//   - projects whose path no longer exists on disk;
//   - projects under ephemeral roots (OS temp dirs, polecat worktrees).
//
// The cleaned registry is written back to disk. Without this, projects.json
// accumulates dead entries indefinitely — mg-d205 found it had grown to 356
// projects, ~280 of them stale polecat worktrees, and pogod re-indexed every
// one on every startup.
func PruneRegistry() {
	if len(projects) == 0 {
		return
	}
	kept := make([]Project, 0, len(projects))
	pruned := 0
	for _, p := range projects {
		reason := ""
		if isEphemeralPath(p.Path) {
			reason = "ephemeral path"
		} else if _, err := os.Stat(p.Path); errors.Is(err, os.ErrNotExist) {
			reason = "path no longer exists"
		}
		if reason != "" {
			logger.Info("registry GC: pruning project entry", "path", p.Path, "reason", reason)
			search.SearchService.Evict(p.Path)
			pruned++
			continue
		}
		kept = append(kept, p)
	}
	if pruned == 0 {
		return
	}
	projects = kept
	SaveProjects()
	logger.Info("registry GC: pruned stale entries", "pruned", pruned, "remaining", len(projects))
}

// isEphemeralPath reports whether path lives under a directory whose contents
// are transient by nature — OS temp dirs and the polecat worktree dir. Such
// repos must never persist in the registry: they are created and destroyed
// constantly, and re-indexing them every startup is wasted work (mg-d205).
func isEphemeralPath(path string) bool {
	clean := filepath.Clean(path)

	tempRoots := []string{
		filepath.Clean(os.TempDir()),
		"/tmp",
		"/private/tmp",
		"/private/var/folders",
	}
	for _, root := range tempRoots {
		if root == "" || root == "." {
			continue
		}
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}

	// A polecat worktree root is a direct child of $POGO_HOME/polecats.
	// Matching only the immediate child (not arbitrary descendants) keeps
	// this from flagging repos nested inside a worktree, e.g. test fixtures.
	polecatsDir := filepath.Join(config.PogoHome(), "polecats")
	if filepath.Dir(clean) == polecatsDir {
		return true
	}
	return false
}

// withinIndexRoots reports whether path is eligible under the optional
// index-roots allowlist. With no allowlist configured (the default) every path
// is eligible, preserving pogo's zero-config behavior.
func withinIndexRoots(path string) bool {
	if len(indexRoots) == 0 {
		return true
	}
	clean := filepath.Clean(path)
	for _, root := range indexRoots {
		root = filepath.Clean(root)
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
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

func GetProject(id int) *search.IndexedProject {
	for _, p := range projects {
		if p.Id == id {
			resp, err := search.SearchService.GetFiles(p.Path)
			if err != nil {
				logger.Error("Error finding files in project", err)
				return nil
			}
			return resp
		}
	}
	return nil
}

type ProjectStatusResponse struct {
	Id        int                   `json:"id"`
	Path      string                `json:"path"`
	Status    search.IndexingStatus `json:"indexing_status"`
	FileCount int                   `json:"file_count"`
}

func GetProjectStatuses() []ProjectStatusResponse {
	statuses := make([]ProjectStatusResponse, 0, len(projects))
	for _, p := range projects {
		s := search.SearchService.GetStatus(p.Path)
		status := ProjectStatusResponse{
			Id:   p.Id,
			Path: p.Path,
		}
		if s != nil {
			status.Status = s.Status
			status.FileCount = s.FileCount
		} else {
			status.Status = search.StatusUnindexed
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func GetProjectByPath(path string) *Project {
	for _, p := range projects {
		if p.Path == path {
			return &p
		}
	}
	return nil
}

func addToPlugin(p Project) {
	req := pogoPlugin.ProcessProjectReq{PathVar: p.Path}
	ireq := pogoPlugin.IProcessProjectReq(req)
	err := driver.GetPluginManager().ProcessProject(&ireq)
	if err != nil {
		logger.Error("Error adding project to plugin", err)
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
	defer SaveProjects()
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

	dirPath = addSlashToPath(dirPath)

	// Check if in existing project
	for _, proj := range projects {
		if strings.HasPrefix(dirPath, proj.Path) {
			parentProj = &proj
			break
		}
	}

	if parentProj != nil {
		// A visit is the activity signal for the reindex backoff scheduler:
		// it resets the project to base cadence so the next tick re-walks it
		// even if unchanged passes had backed it off (mg-1236).
		MarkProjectActivity(parentProj.Path)
		return &VisitResponse{
			ParentProject: *parentProj,
		}, nil
	}

	proj, err2 := searchAndCreate(dirPath)
	if err2 != nil {
		fmt.Println(err.Error())
		return nil, &internalErrorResponse
	}

	if proj == nil {
		return nil, &notFoundErrorResponse
	}

	return &VisitResponse{
		ParentProject: *proj,
	}, nil
}

func addSlashToPath(path string) string {
	if path[len(path)-1:][0] == filepath.Separator {
		return path
	}
	return path + string(filepath.Separator)
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

	if hasGit(dirnames) {
		repoPath := addSlashToPath(path)
		// Index-scope control (mg-d205): never auto-register transient repos
		// or, when an allowlist is configured, repos outside it.
		if isEphemeralPath(repoPath) {
			logger.Info("project: refusing to auto-register ephemeral repo", "path", repoPath)
			return nil, nil
		}
		if !withinIndexRoots(repoPath) {
			logger.Info("project: repo outside configured index_roots; not auto-registering", "path", repoPath)
			return nil, nil
		}
		var project = Project{
			Id:   0,
			Path: repoPath,
		}
		Add(&project)
		return &project, nil
	}

	if hasPogoStop(dirnames) {
		return nil, nil
	}

	dirPath := filepath.Dir(path)
	if dirPath == path {
		fmt.Printf("Filepath %s is same as %s\n", path, dirPath)
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

// Remove removes a project by path and persists the change.
// Returns true if the project was found and removed.
func Remove(path string) bool {
	path = addSlashToPath(path)
	for i, p := range projects {
		if p.Path == path {
			projects = append(projects[:i], projects[i+1:]...)
			SaveProjects()
			// Drop the search service's in-memory index state too, or the
			// removed project's paths/hashes stay resident forever (gh #39).
			search.SearchService.Evict(path)
			return true
		}
	}
	return false
}

func RemoveSaveFile() {
	os.Remove(projectFile)
}
