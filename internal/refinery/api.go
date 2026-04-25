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

// RegisterHandlers registers refinery API endpoints on the given mux,
// bound to this Refinery instance. Prefer RegisterHandlersFunc when the
// underlying *Refinery may be replaced after registration (e.g. when
// orchestration is restarted via SetRefineryStarter).
func (r *Refinery) RegisterHandlers(mux *http.ServeMux) {
	RegisterHandlersFunc(mux, func() *Refinery { return r })
}

// RegisterHandlersFunc registers refinery API endpoints that resolve the
// current *Refinery via the given getter on every request. This is needed
// when the active Refinery instance may be swapped out at runtime (e.g. on
// orchestration restart): a stable mux registration keeps serving requests
// against whatever Refinery the getter returns at call time, instead of
// being permanently bound to the instance present at registration.
//
// If the getter returns nil, handlers respond 503 Service Unavailable.
func RegisterHandlersFunc(mux *http.ServeMux, get func() *Refinery) {
	wrap := func(fn func(*Refinery, http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			r := get()
			if r == nil {
				http.Error(w, "refinery is not running", http.StatusServiceUnavailable)
				return
			}
			fn(r, w, req)
		}
	}
	mux.HandleFunc("/refinery/status", wrap((*Refinery).handleStatus))
	mux.HandleFunc("/refinery/queue", wrap((*Refinery).handleQueue))
	mux.HandleFunc("/refinery/history", wrap((*Refinery).handleHistory))
	mux.HandleFunc("/refinery/submit", wrap((*Refinery).handleSubmit))
	mux.HandleFunc("/refinery/mr/{id}", wrap((*Refinery).handleMR))
	mux.HandleFunc("/refinery/cancel", wrap((*Refinery).handleCancel))
	mux.HandleFunc("/refinery/prune", wrap((*Refinery).handlePrune))
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

// CancelRequest is the JSON body for POST /refinery/cancel.
type CancelRequest struct {
	ID string `json:"id"`
}

func (r *Refinery) handleCancel(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	var cancelReq CancelRequest
	if err := json.NewDecoder(req.Body).Decode(&cancelReq); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}

	if err := r.Cancel(cancelReq.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": cancelReq.ID, "status": "cancelled"})
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
