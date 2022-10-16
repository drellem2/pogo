package project

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

const aService = "../_testdata/a-service" // In initial state
const bService = "../_testdata/b-service" // Not in initial state
const zService = "../_testdata/z-service" // Doesn't exist

const readme = "/README.md"
const mainC = "/src/main.c"

func setUp(t *testing.T) (string, error) {
	aServiceAbs, err := filepath.Abs(aService)
	if err != nil {
		return "", err
	}
	t.Logf("a-service at: %s", aServiceAbs)
	Init()
	projects = []Project{
		Project{Id: 1, Path: aServiceAbs},
	}
	return aServiceAbs, nil
}

func testFileInExistingProjectRecognized(path string, t *testing.T) {
	t.Logf("Starting test TestFileInExistingProjectRecognized, path=%s", path)
	aServiceAbs, err := setUp(t)
	if err != nil {
		t.Errorf("Failed test set-up %v", err)
		return
	}
	numProj := len(projects)
	fileAbs, err2 := filepath.Abs(aService + path)
	if err2 != nil {
		t.Errorf("Could not construct absolute path: %v", err2)
		return
	}
	resp, err3 := Visit(VisitRequest{Path: fileAbs})
	t.Logf("Response: %#v", resp)
	if err3 != nil {
		t.Errorf("Error visiting file: %v", err3)
		return
	}
	if resp.ParentProject.Id != 1 {
		t.Errorf("Wrong Project Id, expected 1 but found %d", resp.ParentProject.Id)
		return
	}
	if resp.ParentProject.Path != aServiceAbs {
		t.Errorf("Wrong Project Path, expected %s but found %s", aServiceAbs, resp.ParentProject.Path)
		return
	}
	numProj2 := len(projects)
	if numProj != numProj2 {
		t.Errorf("Project number should not have changed from %d to %d", numProj, numProj2)
		return
	}
}

func TestFileInExistingProjectRecognized(t *testing.T) {
	files := []string{
		"",
		readme,
	}
	for _, file := range files {
		testFileInExistingProjectRecognized(file, t)
	}
}

func testFileMissingReturns404(path string, t *testing.T) {
	t.Logf("Starting test TestFileMissingReturns404, path=%s", path)
	_, err := setUp(t)
	if err != nil {
		t.Errorf("Failed test set-up %v", err)
		return
	}
	numProj := len(projects)
	fileAbs, err2 := filepath.Abs(path)
	if err2 != nil {
		t.Errorf("Could not construct absolute path: %v", err2)
		return
	}
	resp, err3 := Visit(VisitRequest{Path: fileAbs})
	if resp != nil {
		t.Errorf("Response should be nil: %#v", resp)
		return
	}
	if err3 == nil {
		t.Errorf("Error should not be nil")
		return
	}
	if (*err3).Code != notFoundErrorResponse.Code {
		t.Errorf("Wrong Error Code, expected %d but found %d", notFoundErrorResponse.Code, (*err3).Code)
		return
	}
	numProj2 := len(projects)
	if numProj != numProj2 {
		t.Errorf("Project number should not have changed from %d to %d", numProj, numProj2)
		return
	}
}

func TestFileMissingReturns404(t *testing.T) {
	files := []string{
		zService,
	}
	for _, file := range files {
		testFileMissingReturns404(file, t)
	}
}

func testFileInNewProjectAddsProject(path string, t *testing.T) {
	t.Logf("Starting test TestFileInNewProjectAddsProject, path=%s", path)
	_, err := setUp(t)
	if err != nil {
		t.Errorf("Failed test set-up %v", err)
		return
	}
	bServiceAbs, err4 := filepath.Abs(bService)
	if err4 != nil {
		t.Errorf("Could not construct absolute path: %v", err4)
		return
	}
	numProj := len(projects)
	fileAbs, err2 := filepath.Abs(bService + path)
	if err2 != nil {
		t.Errorf("Could not construct absolute path: %v", err2)
		return
	}
	resp, err3 := Visit(VisitRequest{Path: fileAbs})
	t.Logf("Response: %#v", resp)
	if err3 != nil {
		t.Errorf("Error visiting file: %v", err3)
		return
	}
	if resp.ParentProject.Id != numProj+1 {
		t.Errorf("Wrong Project Id, expected %d but found %d", numProj+1, resp.ParentProject.Id)
		return
	}
	if resp.ParentProject.Path != bServiceAbs {
		t.Errorf("Wrong Project Path, expected %s but found %s", bServiceAbs, resp.ParentProject.Path)
		return
	}
	numProj2 := len(projects)
	if numProj+1 != numProj2 {
		t.Errorf("Project number expected %d but found %d", numProj+1, numProj2)
		return
	}
}

func TestFileInNewProjectAddsProject(t *testing.T) {
	files := []string{
		"",
		readme,
		mainC,
	}
	for _, file := range files {
		testFileInNewProjectAddsProject(file, t)
	}
}

func testRelativePathReturnsReturns400(path string, t *testing.T) {
	t.Logf("Starting test RelativePathReturns400, path=%s", path)
	_, err := setUp(t)
	if err != nil {
		t.Errorf("Failed test set-up %v", err)
		return
	}
	numProj := len(projects)
	resp, err2 := Visit(VisitRequest{Path: path})
	if resp != nil {
		t.Errorf("Response should be nil: %#v", resp)
		return
	}
	if err2 == nil {
		t.Errorf("Error should not be nil")
		return
	}
	if (*err2).Code != http.StatusBadRequest {
		t.Errorf("Wrong Error Code, expected %d but found %d", http.StatusBadRequest, (*err2).Code)
		return
	}
	numProj2 := len(projects)
	if numProj != numProj2 {
		t.Errorf("Project number should not have changed from %d to %d", numProj, numProj2)
		return
	}
}

func TestRelativePathReturns400(t *testing.T) {
	files := []string{
		aService,
	}
	for _, file := range files {
		testRelativePathReturnsReturns400(file, t)
	}
}

func TestSaveProjects(t *testing.T) {
	setUp(t)
	projectNum := len(projects)
	SaveProjects()
	Init()
	projectNum2 := len(projects)
	if projectNum != projectNum2 {
		t.Errorf("Project number expected %d but found  %d", projectNum, projectNum2)
	}
	// Cleanup
	os.Remove(projectFile)
}
