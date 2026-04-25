package refinery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRegisterHandlersFuncReflectsCurrentRefinery verifies that handlers
// registered via RegisterHandlersFunc resolve the active *Refinery on each
// request. This is the regression test for issue #9: SetRefineryStarter
// swapped the package-level mergeQueue var, but /refinery/queue kept serving
// items from the dead instance because handlers were bound to the original.
func TestRegisterHandlersFuncReflectsCurrentRefinery(t *testing.T) {
	originDir := initBareOrigin(t, "main")

	oldRef, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldRef.Submit(MergeRequest{
		RepoPath: originDir,
		Branch:   "old-feature",
		Author:   "old-cat",
	}); err != nil {
		t.Fatal(err)
	}

	// current is the package-level pointer the handlers should resolve on
	// every request. This mirrors how cmd/pogod's mergeQueue var works.
	current := oldRef
	mux := http.NewServeMux()
	RegisterHandlersFunc(mux, func() *Refinery { return current })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Sanity: queue should show the old item.
	got := fetchQueue(t, srv.URL+"/refinery/queue")
	if len(got) != 1 || got[0].Branch != "old-feature" {
		t.Fatalf("pre-restart: expected old-feature in queue, got %+v", got)
	}

	// Simulate orchestration restart: build a new Refinery, swap the
	// pointer (handlers must see the swap without re-registration).
	newRef, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newRef.Submit(MergeRequest{
		RepoPath: originDir,
		Branch:   "new-feature",
		Author:   "new-cat",
	}); err != nil {
		t.Fatal(err)
	}
	current = newRef

	got = fetchQueue(t, srv.URL+"/refinery/queue")
	if len(got) != 1 {
		t.Fatalf("post-restart: expected 1 item from new refinery, got %d: %+v", len(got), got)
	}
	if got[0].Branch != "new-feature" {
		t.Errorf("post-restart: expected branch new-feature (from new instance), got %s — handlers are bound to dead instance", got[0].Branch)
	}
}

// TestRegisterHandlersFuncNilRefinery verifies that the handlers respond
// 503 when the getter returns nil rather than panicking.
func TestRegisterHandlersFuncNilRefinery(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHandlersFunc(mux, func() *Refinery { return nil })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/refinery/queue")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when refinery is nil, got %d", resp.StatusCode)
	}
}

func fetchQueue(t *testing.T, url string) []MergeRequest {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	var queue []MergeRequest
	if err := json.NewDecoder(resp.Body).Decode(&queue); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	return queue
}
