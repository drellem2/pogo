package refinery

import "github.com/rs/xid"

// generateID returns a short, sortable, unique ID for merge requests.
func generateID() string {
	return "mr-" + xid.New().String()
}
