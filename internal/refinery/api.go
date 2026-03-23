package refinery

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// SubmitRequest is the JSON body for POST /refinery/submit.
type SubmitRequest struct {
	RepoPath  string `json:"repo_path"`
	Branch    string `json:"branch"`
	TargetRef string `json:"target_ref,omitempty"` // default: "main"
	Author    string `json:"author,omitempty"`
}

// RegisterHandlers registers refinery API endpoints on the given mux.
func (r *Refinery) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/refinery/status", r.handleStatus)
	mux.HandleFunc("/refinery/queue", r.handleQueue)
	mux.HandleFunc("/refinery/history", r.handleHistory)
	mux.HandleFunc("/refinery/submit", r.handleSubmit)
	mux.HandleFunc("/refinery/mr/{id}", r.handleMR)
	mux.HandleFunc("/refinery/prune", r.handlePrune)
}

func (r *Refinery) handlePrune(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	results := r.PruneWorktrees()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (r *Refinery) handleStatus(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(r.GetStatus())
}

func (r *Refinery) handleQueue(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(r.Queue())
}

func (r *Refinery) handleHistory(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(r.History())
}

func (r *Refinery) handleSubmit(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	var submitReq SubmitRequest
	if err := json.NewDecoder(req.Body).Decode(&submitReq); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}

	mr := MergeRequest{
		RepoPath:  submitReq.RepoPath,
		Branch:    submitReq.Branch,
		TargetRef: submitReq.TargetRef,
		Author:    submitReq.Author,
	}

	id, err := r.Submit(mr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func (r *Refinery) handleMR(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	id := req.PathValue("id")
	mr := r.Get(id)
	if mr == nil {
		http.Error(w, fmt.Sprintf("MR %q not found", id), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mr)
}
