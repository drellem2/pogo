package workitem

import (
	"encoding/json"
	"log"
	"net/http"
)

// HandleWorkItems handles GET /workitems requests.
// It returns a JSON array of work items from the macguffin workspace.
// Supports optional ?status= query parameter to filter by status.
func HandleWorkItems(w http.ResponseWriter, r *http.Request) {
	log.Println("Visited /workitems")

	if r.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	// Optional status filter, applied before the walk so filtered requests
	// only read the matching status directory (an unknown status walks
	// nothing and returns an empty array).
	var statuses []string
	if statusFilter := r.URL.Query().Get("status"); statusFilter != "" {
		statuses = append(statuses, statusFilter)
	}

	items, err := List(statuses...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if items == nil {
		items = []WorkItem{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}
