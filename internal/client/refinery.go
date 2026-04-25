package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/drellem2/pogo/internal/refinery"
)

// GetRefineryStatus returns the refinery status summary.
func GetRefineryStatus() (*refinery.Status, error) {
	r, err := http.Get(serverURL + "/refinery/status")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	var status refinery.Status
	if err := json.NewDecoder(r.Body).Decode(&status); err != nil {
		return nil, err
	}
	return &status, nil
}

// GetRefineryQueue returns all queued merge requests.
func GetRefineryQueue() ([]refinery.MergeRequest, error) {
	r, err := http.Get(serverURL + "/refinery/queue")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	var queue []refinery.MergeRequest
	if err := json.NewDecoder(r.Body).Decode(&queue); err != nil {
		return nil, err
	}
	return queue, nil
}

// GetRefineryHistory returns completed merge requests (most recent first).
func GetRefineryHistory() ([]refinery.MergeRequest, error) {
	r, err := http.Get(serverURL + "/refinery/history")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	var history []refinery.MergeRequest
	if err := json.NewDecoder(r.Body).Decode(&history); err != nil {
		return nil, err
	}
	return history, nil
}

// GetRefineryMR returns a single merge request by ID.
func GetRefineryMR(id string) (*refinery.MergeRequest, error) {
	r, err := http.Get(serverURL + "/refinery/mr/" + id)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("merge request %s not found", id)
	}
	var mr refinery.MergeRequest
	if err := json.NewDecoder(r.Body).Decode(&mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

// PruneWorktrees asks the refinery to prune merged branches from worktree clones.
func PruneWorktrees() ([]refinery.PruneResult, error) {
	r, err := http.Post(serverURL+"/refinery/prune", "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("prune failed: %s", string(msg))
	}
	var results []refinery.PruneResult
	if err := json.NewDecoder(r.Body).Decode(&results); err != nil {
		return nil, err
	}
	return results, nil
}

// CancelMerge cancels a queued merge request, removing it from the queue.
func CancelMerge(id string) error {
	body, err := json.Marshal(refinery.CancelRequest{ID: id})
	if err != nil {
		return err
	}
	r, err := http.Post(serverURL+"/refinery/cancel", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("cancel failed: %s", string(msg))
	}
	return nil
}

// SubmitMerge submits a branch to the refinery merge queue.
// Returns a wrapped error containing refinery.DisabledMessage when the
// daemon has refinery disabled in config.
func SubmitMerge(req refinery.SubmitRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	r, err := http.Post(serverURL+"/refinery/submit", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(r.Body)
		return "", fmt.Errorf("submit failed: %s", strings.TrimSpace(string(msg)))
	}
	var result map[string]string
	if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
		return "", err
	}
	return result["id"], nil
}
