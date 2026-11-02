package project

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/search"
)

// disableSearch turns search indexing off for one test, the pogod default
// since mg-cabf4. TestMain opts the package in; this restores that.
func disableSearch(t *testing.T) {
	t.Helper()
	prev := driver.SearchEnabled()
	t.Cleanup(func() { driver.SetSearchEnabled(prev) })
	driver.SetSearchEnabled(false)
}

// TestProjectIndexWorksWithSearchDisabled is the mg-cabf4 guarantee that
// turning search off leaves the project index alone: `pogo visit` registers
// a repo, the registry lists and persists it, /status reports it as
// "disabled" rather than failing, and remove still works.
func TestProjectIndexWorksWithSearchDisabled(t *testing.T) {
	disableSearch(t)
	ProjectFileName = "projects-search-disabled-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	file, err := absolute(bService + readme)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	// SearchService is process-global: an earlier test in this binary may
	// have indexed b-service with search on. Start from no state for it.
	// Quiesce first, or an index pass still in flight re-inserts it.
	search.SearchService.Quiesce(30 * time.Second)
	if fixtureRoot, err := absolute(bService); err == nil {
		search.SearchService.Evict(fixtureRoot)
	}
	resp, verr := Visit(VisitRequest{Path: file})
	if verr != nil {
		t.Fatalf("Visit with search disabled: %d %s", verr.Code, verr.Message)
	}
	root := resp.ParentProject.Path
	if GetProjectByPath(root) == nil {
		t.Fatalf("visited repo %s not in the registry", root)
	}

	// Persisted: a fresh load of the registry file still has it.
	projects = nil
	Init()
	if GetProjectByPath(root) == nil {
		t.Fatalf("visited repo %s not persisted to %s", root, ProjectFileName)
	}

	found := false
	for _, st := range GetProjectStatuses() {
		if st.Status != search.StatusDisabled {
			t.Errorf("status for %s = %q, want %q", st.Path, st.Status, search.StatusDisabled)
		}
		if st.Path == root {
			found = true
		}
	}
	if !found {
		t.Errorf("/status omitted the visited project %s", root)
	}

	if s := search.SearchService.GetStatus(root); s != nil {
		t.Errorf("search service holds state for %s with search disabled: %+v", root, *s)
	}

	if !Remove(root) {
		t.Fatalf("Remove(%s) = false", root)
	}
	if GetProjectByPath(root) != nil {
		t.Fatalf("project %s still registered after Remove", root)
	}
}

// TestSearchDisabledWritesNoIndex is the cost half of mg-cabf4: with search
// off, neither registration nor a periodic tick may walk the tree or write
// <repo>/.pogo/search. The positive control enables search on the same repo
// and watches the same directory appear, so the absence is the gate and not
// an instrument that never fires.
func TestSearchDisabledWritesNoIndex(t *testing.T) {
	disableSearch(t)
	drainSearch(t)
	ProjectFileName = "projects-search-disabled-noindex-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	repo := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	root := addSlashToPath(repo)
	indexDir := filepath.Join(repo, ".pogo", "search")

	Add(&Project{Id: 0, Path: root})
	s := newReindexScheduler(time.Minute)
	reindexTick(s, time.Now())
	IndexAll()
	time.Sleep(300 * time.Millisecond) // index passes are asynchronous

	if _, err := os.Stat(indexDir); !os.IsNotExist(err) {
		t.Errorf("%s exists with search disabled (stat err=%v)", indexDir, err)
	}
	if st := search.SearchService.GetStatus(root); st != nil {
		t.Errorf("search service indexed %s with search disabled: %+v", root, *st)
	}

	// Positive control.
	driver.SetSearchEnabled(true)
	driver.Init()
	reindexTick(s, time.Now())
	if !waitForStatus(root, search.StatusReady, 15*time.Second) {
		t.Fatalf("control: project never indexed with search enabled")
	}
	if _, err := os.Stat(indexDir); err != nil {
		t.Fatalf("control: %s not written with search enabled: %v", indexDir, err)
	}
	Remove(root)
}
