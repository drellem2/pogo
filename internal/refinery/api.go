package refinery

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/drellem2/pogo/internal/vcs"
)

// SubmitRequest is the JSON body for POST /refinery/submit.
type SubmitRequest struct {
	RepoPath  string `json:"repo_path"`
	Branch    string `json:"branch"`
	TargetRef string `json:"target_ref,omitempty"` // default: "main"
	Author    string `json:"author,omitempty"`
	// AutoCreateTargetRef opts into branching the target ref off the repo's
	// default branch when it does not exist on origin. Default false.
	AutoCreateTargetRef bool `json:"auto_create_target_ref,omitempty"`
	// DeferDone opts the submitter into owning its own post-merge lifecycle:
	// pogod skips the auto-done + auto-stop it normally applies at merge time
	// so a --branch (PR-flow) polecat can finish its post-merge work (open the
	// PR, verify, mail) and call `mg done` itself (gh drellem2/pogo #81).
	// Default false.
	//
	// It is only needed to force deferral for a merge onto the *default*
	// branch. A TargetRef that is not the default branch is classified as PR
	// flow by Submit and deferred regardless of this field (mg-7746) — the
	// deferral must not depend on a caller remembering a flag. There is
	// deliberately no way to request PR flow from here: it is derived.
	DeferDone bool `json:"defer_done,omitempty"`
	// PostMergeTag asks the REFINERY to create this git tag on the commit the
	// merge lands as, and push it, before the author's reap can fire
	// (mg-6879). Empty means no post-merge step.
	//
	// This is the opposite instrument to DeferDone. DeferDone leaves the
	// post-merge work with the author and merely stops it being killed;
	// PostMergeTag moves the work to an actor that both sees the merged SHA
	// and outlives the author. Use DeferDone for work only the author can do
	// (open a PR from its branch, mail its report); use this for work that
	// just needs the merged commit.
	PostMergeTag string `json:"post_merge_tag,omitempty"`
	// Verdict is the submitter's own result for its work item, carried through
	// the merge and written into the item's result sidecar (mg-dfea). Must be a
	// non-empty JSON object; Submit rejects anything else while the submitter
	// is still alive to hear about it. See MergeRequest.Verdict for why submit
	// time is the only moment an auto-done author can record one.
	Verdict json.RawMessage `json:"verdict,omitempty"`
	// Item is the work item the submission is for (`submit --item`,
	// mg-30da0). On git it resolves an empty Branch through the item's live
	// polecat. An enth repo is refused whatever is given: its workers land
	// their own work (POST /refinery/land, mg-5b904).
	Item string `json:"item,omitempty"`
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
	mux.HandleFunc("/refinery/land", wrap((*Refinery).handleLand))
	mux.HandleFunc("/refinery/ref-create", wrap((*Refinery).handleRefCreate))
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
	// The in-flight request leads the list. It used to be omitted, which made
	// a busy refinery serve the same static array of queued rows as a wedged
	// one (mg-0c51). The shape is unchanged — still an array of merge requests
	// — so existing consumers keep working; the new row is distinguished by
	// its status field, which they already read.
	json.NewEncoder(w).Encode(r.QueueWithProcessing())
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
		RepoPath:            submitReq.RepoPath,
		Branch:              submitReq.Branch,
		TargetRef:           submitReq.TargetRef,
		Author:              submitReq.Author,
		AutoCreateTargetRef: submitReq.AutoCreateTargetRef,
		DeferDone:           submitReq.DeferDone,
		PostMergeTag:        submitReq.PostMergeTag,
		Verdict:             submitReq.Verdict,
		Item:                submitReq.Item,
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

// LandRequest is the JSON body for POST /refinery/land: an enth worker's own
// land, recorded so the work item closes as a merge closes it (enth-vcs §6,
// mg-5b904). See RecordSelfLand.
type LandRequest struct {
	RepoPath  string `json:"repo_path"`
	Item      string `json:"item"`
	Author    string `json:"author,omitempty"`     // default: item
	TargetRef string `json:"target_ref,omitempty"` // default: the repo's trunk
	// Landed is `enth land`'s stdout, verbatim.
	Landed    string          `json:"landed"`
	RequestID string          `json:"request_id,omitempty"`
	Verdict   json.RawMessage `json:"verdict,omitempty"`
	// PostMergeTag: tag what the land landed as (enth tag), slice 12.
	PostMergeTag string `json:"post_merge_tag,omitempty"`
}

// LandResponse is POST /refinery/land's answer.
type LandResponse struct {
	// ID is the merged MR the land is recorded as.
	ID string `json:"id"`
	// AlreadyRecorded is true when an earlier call recorded the same land:
	// nothing was fired again.
	AlreadyRecorded bool `json:"already_recorded,omitempty"`
	// Landed is the land record.
	Landed *vcs.LandRecord `json:"landed"`
	// PostMergeTag and PostMergeError report a declared tag: made (or
	// already there) when PostMergeError is empty.
	PostMergeTag   string `json:"post_merge_tag,omitempty"`
	PostMergeError string `json:"post_merge_error,omitempty"`
}

func (r *Refinery) handleLand(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	var lr LandRequest
	if err := json.NewDecoder(req.Body).Decode(&lr); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}
	res, err := r.RecordSelfLand(SelfLandRequest{RepoPath: lr.RepoPath, Item: lr.Item, Author: lr.Author,
		TargetRef: lr.TargetRef, Landed: []byte(lr.Landed), RequestID: lr.RequestID, Verdict: lr.Verdict,
		PostMergeTag: lr.PostMergeTag})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if res.AlreadyRecorded {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
	json.NewEncoder(w).Encode(LandResponse{ID: res.MR.ID, AlreadyRecorded: res.AlreadyRecorded, Landed: res.MR.Enth,
		PostMergeTag: res.MR.PostMergeTag, PostMergeError: res.MR.PostMergeError})
}

func (r *Refinery) handleRefCreate(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	var rc RefCreateRequest
	if err := json.NewDecoder(req.Body).Decode(&rc); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}
	res, err := r.CreateRef(rc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if res.Existed {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
	json.NewEncoder(w).Encode(res)
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

	outcome, err := r.Cancel(cancelReq.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// A processing MR is not cancelled yet, only asked to stop, so the
	// response says which of the two happened rather than reporting both as
	// "cancelled".
	resp := CancelResponse{ID: cancelReq.ID, Outcome: outcome, Status: "cancelled"}
	if outcome == CancelRequestedInFlight {
		resp.Status = "cancel_requested"
		resp.Note = InFlightCancelNote
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// CancelResponse is the JSON body returned by POST /refinery/cancel.
type CancelResponse struct {
	ID string `json:"id"`
	// Outcome distinguishes a queued MR removed outright from a processing one
	// asked to stop. See CancelOutcome.
	Outcome CancelOutcome `json:"outcome"`
	// Status is "cancelled" when the MR is already terminal, and
	// "cancel_requested" when the outcome is not yet decided.
	Status string `json:"status"`
	// Note carries the caller-facing explanation for the undecided case.
	Note string `json:"note,omitempty"`
}

func (r *Refinery) handleMR(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	id := req.PathValue("id")
	mr := r.Get(id)
	if mr == nil {
		// Distinguish "lost across a restart" (410 Gone, resubmittable) and
		// "pruned from history by age/count limits" from a plain 404 —
		// callers react differently to each (see templates/polecat.md).
		if le := r.LostInfo(id); le != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			json.NewEncoder(w).Encode(map[string]string{
				"id":     id,
				"status": string(StatusLost),
				"branch": le.Branch,
				"author": le.Author,
				"reason": le.Reason,
			})
			return
		}
		if r.WasPruned(id) {
			http.Error(w, fmt.Sprintf("MR %q pruned from history (completed, then aged out of the retention window)", id), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("MR %q not found", id), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mr)
}
