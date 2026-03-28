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

	items, err := List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Optional status filter
	if statusFilter := r.URL.Query().Get("status"); statusFilter != "" {
		filtered := items[:0]
		for _, item := range items {
			if item.Status == statusFilter {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}

	if items == nil {
		items = []WorkItem{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}
