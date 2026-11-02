package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/search"
)

// TestSearchEndpointsWithSearchDisabled pins the mg-cabf4 contract for the
// search side of pogod's HTTP surface when [search] enabled is false (the
// default): /plugins lists no search plugin, a search request to /plugin and a
// project file listing at /projects/{id} answer 503 with the how-to-enable
// message instead of hanging or 500ing, and /status reports "disabled".
func TestSearchEndpointsWithSearchDisabled(t *testing.T) {
	t.Setenv("POGO_PLUGIN_PATH", t.TempDir())
	prev := driver.SearchEnabled()
	t.Cleanup(func() { driver.SetSearchEnabled(prev) })
	driver.SetSearchEnabled(false)
	driver.Init()
	defer driver.Kill()

	rec := httptest.NewRecorder()
	plugins(rec, httptest.NewRequest("GET", "/plugins", nil))
	var listed []string
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatalf("/plugins body: %v", err)
	}
	for _, p := range listed {
		if strings.Contains(p, driver.SearchPluginName) {
			t.Errorf("/plugins lists %q with search disabled", p)
		}
	}

	checkDisabled := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503", name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), config.SearchDisabledMessage) {
			t.Errorf("%s: body %q lacks the how-to-enable message", name, rec.Body.String())
		}
	}

	rec = httptest.NewRecorder()
	plugin(rec, httptest.NewRequest("GET", "/plugin?path="+driver.SearchPluginName, nil))
	checkDisabled("GET /plugin", rec)

	rec = httptest.NewRecorder()
	body := `{"plugin":"/usr/local/lib/pogo-plugin-search","value":"{}"}`
	plugin(rec, httptest.NewRequest("POST", "/plugin", strings.NewReader(body)))
	checkDisabled("POST /plugin", rec)

	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/projects/1", nil)
	req.SetPathValue("projectId", "1")
	projectById(rec, req)
	checkDisabled("GET /projects/{id}", rec)

	rec = httptest.NewRecorder()
	status(rec, httptest.NewRequest("GET", "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/status: %d", rec.Code)
	}
	var statuses []project.ProjectStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&statuses); err != nil {
		t.Fatalf("/status body: %v", err)
	}
	for _, s := range statuses {
		if s.Status != search.StatusDisabled {
			t.Errorf("/status %s = %q, want %q", s.Path, s.Status, search.StatusDisabled)
		}
	}

	// Positive control: with search on, /plugins lists it and /plugin answers.
	driver.SetSearchEnabled(true)
	driver.Init()
	rec = httptest.NewRecorder()
	plugins(rec, httptest.NewRequest("GET", "/plugins", nil))
	if !strings.Contains(rec.Body.String(), driver.SearchPluginName) {
		t.Fatalf("control: /plugins = %s with search enabled", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	plugin(rec, httptest.NewRequest("GET", "/plugin?path="+driver.SearchPluginName, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("control: GET /plugin = %d with search enabled", rec.Code)
	}
}
