package refinery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDisabledStatus(t *testing.T) {
	mux := http.NewServeMux()
	RegisterDisabledHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/refinery/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var st Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Enabled {
		t.Error("expected enabled=false")
	}
	if st.Running {
		t.Error("expected running=false")
	}
}

func TestDisabledQueueAndHistory(t *testing.T) {
	mux := http.NewServeMux()
	RegisterDisabledHandlers(mux)

	for _, path := range []string{"/refinery/queue", "/refinery/history"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", path, w.Code)
		}
		var arr []MergeRequest
		if err := json.Unmarshal(w.Body.Bytes(), &arr); err != nil {
			t.Errorf("%s: expected valid JSON array, got %v: %s", path, err, w.Body.String())
		}
		if len(arr) != 0 {
			t.Errorf("%s: expected empty array, got %d items", path, len(arr))
		}
	}
}

func TestDisabledSubmitFailsFast(t *testing.T) {
	mux := http.NewServeMux()
	RegisterDisabledHandlers(mux)

	req := httptest.NewRequest(http.MethodPost, "/refinery/submit", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "refinery is disabled") {
		t.Errorf("expected error message about being disabled, got %q", body)
	}
	if !strings.Contains(body, "config.toml") {
		t.Errorf("expected error message to point at config file, got %q", body)
	}
}

func TestDisabledCancelAndPruneFailFast(t *testing.T) {
	mux := http.NewServeMux()
	RegisterDisabledHandlers(mux)

	for _, path := range []string{"/refinery/cancel", "/refinery/prune"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: expected 503, got %d", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "refinery is disabled") {
			t.Errorf("%s: expected disabled error, got %q", path, w.Body.String())
		}
	}
}

func TestDisabledMRReturnsNotFound(t *testing.T) {
	mux := http.NewServeMux()
	RegisterDisabledHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/refinery/mr/abc123", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "refinery is disabled") {
		t.Errorf("expected disabled message, got %q", w.Body.String())
	}
}
