package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/project"
)

// TestSearchReportsDisabledWhenNoPlugin: with search indexing off (the
// mg-cabf4 default) pogod lists no search plugin, and pose / pogo refs /
// pogo deps must say how to enable it rather than "search plugin not found".
func TestSearchReportsDisabledWhenNoPlugin(t *testing.T) {
	var pluginCalls int
	newFakePogod(t, 1, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/projects":
			json.NewEncoder(w).Encode([]project.Project{{Id: 1, Path: "/repo/"}})
		case "/plugins":
			json.NewEncoder(w).Encode([]string{"pogo-plugin-diagnostics"})
		case "/plugin":
			pluginCalls++
			http.Error(w, "unexpected", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	})

	if _, err := GetSearchPlugin(); !errors.Is(err, ErrSearchDisabled) {
		t.Errorf("GetSearchPlugin err = %v, want ErrSearchDisabled", err)
	}
	err := SearchAllStreaming("x", func(*SearchResponse) {})
	if !errors.Is(err, ErrSearchDisabled) {
		t.Fatalf("SearchAllStreaming err = %v, want ErrSearchDisabled", err)
	}
	if !strings.Contains(err.Error(), "[search] enabled = true") {
		t.Errorf("error %q does not say how to enable search", err)
	}
	if err.Error() != config.SearchDisabledMessage {
		t.Errorf("error %q drifted from config.SearchDisabledMessage", err)
	}
	if pluginCalls != 0 {
		t.Errorf("%d search requests sent with search disabled", pluginCalls)
	}
}
