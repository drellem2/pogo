package project

import (
	"path/filepath"
	"testing"
)

const aService = "_testdata/a-service"
const readme = "/README.md"


func setUp(t *testing.T) (string, error) {
	aServiceAbs, err := filepath.Abs(aService)
	if err != nil {
		return "", err
	}
	projects = []Project {
        Project { Id: 1, Path: aServiceAbs },
	}
	Init()
	return aServiceAbs, nil
}

func testFileInExistingProjectRecognized(path string, t *testing.T) {
	t.Logf("Starting test TestFileInExistingProjectRecognized, path=%s", path)
	aServiceAbs, err := setUp(t)
	if err != nil {
		t.Errorf("Failed test set-up %v", err)
	}
	numProj := len(projects)
	fileAbs, err2 := filepath.Abs(aService+path)
	if err2 != nil {
		t.Errorf("Could not construct absolute path: %v", err2)
	}
	resp, err3 := Visit(VisitRequest{ Path: fileAbs })
	t.Logf("Response: %#v", resp)
	if err3 != nil {
		t.Errorf("Error visiting file: %v", err3)
	}
	if resp.ParentProject.Id != 1 {
		t.Errorf("Wrong Project Id, expected 1 but found %d", resp.ParentProject.Id)
	}
	if resp.ParentProject.Path != aServiceAbs {
		t.Errorf("Wrong Project Path, expected %s but found %s", aServiceAbs, resp.ParentProject.Path)
	}
	numProj2 := len(projects)
	if numProj != numProj2 {
		t.Errorf("Project number should not have changed from %d to %d", numProj, numProj2)
	}

}

func TestFileInExistingProjectRecognized(t *testing.T) {
	files := []string{
		"",
		readme,
	}
	for _, file := range(files) {
		testFileInExistingProjectRecognized(file, t)
	}
}
