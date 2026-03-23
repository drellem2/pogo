package workspace

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
)

// RegisterHandlers registers workspace HTTP endpoints on the given mux.
func (m *Manager) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/workspace/symbols", m.handleSymbols)
}

func (m *Manager) handleSymbols(w http.ResponseWriter, r *http.Request) {
	log.Println("Visited /workspace/symbols")

	switch r.Method {
	case "GET":
		m.handleSymbolsGet(w, r)
	case "POST":
		m.handleSymbolsPost(w, r)
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func (m *Manager) handleSymbolsGet(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	if query == "" {
		http.Error(w, "query parameter is required", http.StatusBadRequest)
		return
	}

	q := SymbolQuery{
		Query:    query,
		RepoPath: r.URL.Query().Get("repo"),
	}

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil {
			q.Limit = n
		}
	}

	resp, err := m.QuerySymbols(context.Background(), q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (m *Manager) handleSymbolsPost(w http.ResponseWriter, r *http.Request) {
	var q SymbolQuery
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if q.Query == "" {
		http.Error(w, "query field is required", http.StatusBadRequest)
		return
	}

	resp, err := m.QuerySymbols(context.Background(), q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
