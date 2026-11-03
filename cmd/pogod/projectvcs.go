package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/vcs"
)

// SetVCSRequest is the body of POST /projects/vcs (pogo project set-vcs and
// pogo project add --vcs; enth-vcs §2, slice 4, mg-160e8).
type SetVCSRequest struct {
	Path string        `json:"path"`
	VCS  vcs.Kind      `json:"vcs"`
	Enth *vcs.EnthRepo `json:"enth,omitempty"`
}

// projectVCSHandler serves POST /projects/vcs against the live merge queue and
// agent registry.
func projectVCSHandler(w http.ResponseWriter, r *http.Request) {
	serveSetVCS(w, r, vcsBusy)
}

// serveSetVCS changes a project's recorded VCS. A change while the repo has
// open MRs or live polecats is refused with 409 and the reasons; a malformed
// selection is 400; an unregistered path is 404.
func serveSetVCS(w http.ResponseWriter, r *http.Request, busy project.BusyFunc) {
	if r.Method != http.MethodPost {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	var req SetVCSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	p, err := project.SetVCS(req.Path, req.VCS, req.Enth, busy)
	var be *project.BusyError
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
	case errors.As(err, &be):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, project.ErrNotRegistered):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// vcsBusy lists the work in flight for repoPath: every pending or in-flight MR
// submitted against it, and every live polecat working in it. Either is work
// started under the repo's current backend, so a change of backend waits for
// it (§2: "moving a repo is a handover, not a toggle").
func vcsBusy(repoPath string) []string {
	var mrs []refinery.MergeRequest
	if mq := mergeQueue; mq != nil {
		mrs = mq.QueueWithProcessing()
	}
	var pcs []agent.PolecatInfo
	if reg := agentRegistry; reg != nil {
		pcs = reg.Polecats()
	}
	return busyFrom(repoPath, mrs, pcs)
}

// busyFrom is vcsBusy over snapshots. The registry's path carries a trailing
// separator and the MR's and polecat's do not, so the comparison is on the
// cleaned path.
func busyFrom(repoPath string, mrs []refinery.MergeRequest, pcs []agent.PolecatInfo) []string {
	var out []string
	for _, mr := range mrs {
		if sameRepo(mr.RepoPath, repoPath) {
			out = append(out, fmt.Sprintf("open MR %s (%s -> %s, %s)", mr.ID, mr.WorkField(), mr.TargetRef, mr.Status))
		}
	}
	for _, pc := range pcs {
		if sameRepo(pc.SourceRepo, repoPath) {
			out = append(out, fmt.Sprintf("live polecat %s (pid %d, %s)", pc.Name, pc.PID, pc.WorkItemID))
		}
	}
	return out
}

func sameRepo(a, b string) bool {
	return a != "" && b != "" && filepath.Clean(a) == filepath.Clean(b)
}
