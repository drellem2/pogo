package project

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/driver"
)

func TestScannerDiscoversNewRepo(t *testing.T) {
	ProjectFileName = "projects-scanner-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	// Create a temp parent dir to watch
	parentDir, err := os.MkdirTemp("", "pogo-scanner-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(parentDir)

	// Create an existing "repo" so the scanner watches parentDir
	existingRepo := filepath.Join(parentDir, "existing-repo")
	if err := os.MkdirAll(filepath.Join(existingRepo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create existing repo: %v", err)
	}

	// Register existing repo so scanner watches its parent
	p := Project{Id: 0, Path: addSlashToPath(existingRepo)}
	Add(&p)

	// Start scanner
	if err := StartScanner(); err != nil {
		t.Fatalf("failed to start scanner: %v", err)
	}
	defer StopScanner()

	// Verify the parent dir is being watched
	if !scanner.watched[parentDir] {
		t.Errorf("expected scanner to watch %s", parentDir)
	}

	numBefore := len(projects)

	// Create a new repo in the watched parent
	newRepo := filepath.Join(parentDir, "new-repo")
	if err := os.MkdirAll(filepath.Join(newRepo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create new repo: %v", err)
	}

	// Wait for the scanner to detect and register the new repo
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(projects) > numBefore {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	numAfter := len(projects)
	if numAfter != numBefore+1 {
		t.Errorf("expected %d projects, got %d", numBefore+1, numAfter)
	}

	// Verify the new repo was registered with the correct path
	found := GetProjectByPath(addSlashToPath(newRepo))
	if found == nil {
		t.Errorf("new repo not found in projects: %s", newRepo)
	}
}

func TestScannerRespectsPogoStop(t *testing.T) {
	ProjectFileName = "projects-scanner-stop-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	// Create a temp parent dir with .pogo_stop
	parentDir, err := os.MkdirTemp("", "pogo-scanner-stop-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(parentDir)

	// Place .pogo_stop in the parent dir
	stopFile := filepath.Join(parentDir, ".pogo_stop")
	if err := os.WriteFile(stopFile, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create .pogo_stop: %v", err)
	}

	// Create a repo inside
	existingRepo := filepath.Join(parentDir, "existing-repo")
	if err := os.MkdirAll(filepath.Join(existingRepo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}

	p := Project{Id: 0, Path: addSlashToPath(existingRepo)}
	Add(&p)

	if err := StartScanner(); err != nil {
		t.Fatalf("failed to start scanner: %v", err)
	}
	defer StopScanner()

	// Parent should NOT be watched due to .pogo_stop
	if scanner.watched[parentDir] {
		t.Errorf("scanner should not watch dir with .pogo_stop: %s", parentDir)
	}
}

func TestScannerSkipsNonGitDirs(t *testing.T) {
	ProjectFileName = "projects-scanner-nongit-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	parentDir, err := os.MkdirTemp("", "pogo-scanner-nongit-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(parentDir)

	existingRepo := filepath.Join(parentDir, "existing-repo")
	if err := os.MkdirAll(filepath.Join(existingRepo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}

	p := Project{Id: 0, Path: addSlashToPath(existingRepo)}
	Add(&p)

	if err := StartScanner(); err != nil {
		t.Fatalf("failed to start scanner: %v", err)
	}
	defer StopScanner()

	numBefore := len(projects)

	// Create a non-git directory (no .git inside)
	plainDir := filepath.Join(parentDir, "just-a-folder")
	if err := os.Mkdir(plainDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	// Wait briefly
	time.Sleep(500 * time.Millisecond)

	if len(projects) != numBefore {
		t.Errorf("non-git directory should not be registered, projects went from %d to %d", numBefore, len(projects))
	}
}
