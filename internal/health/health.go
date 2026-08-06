// Package health defines the structured response types for the
// pogod /health/full endpoint, shared between the daemon and clients.
package health

import "time"

// FullResponse is the JSON response for GET /health/full.
type FullResponse struct {
	Pogod    Pogod    `json:"pogod"`
	Agents   Agents   `json:"agents"`
	Refinery Refinery `json:"refinery"`
}

// Pogod reports basic daemon health.
type Pogod struct {
	Status string `json:"status"`
	Uptime string `json:"uptime"`
	Mode   string `json:"mode"`
}

// AgentDetail is a summary of one agent.
type AgentDetail struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Restarts int    `json:"restarts,omitempty"`
	Uptime   string `json:"uptime"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// Agents summarises the agent fleet.
type Agents struct {
	Total   int           `json:"total"`
	Running int           `json:"running"`
	Exited  int           `json:"exited"`
	Details []AgentDetail `json:"details"`
}

// Refinery summarises the refinery state.
type Refinery struct {
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`
	// QueueLength counts PENDING requests only — the one being processed is
	// not among them. On its own it therefore cannot distinguish a refinery
	// grinding through a merge from one that has stopped: both report the same
	// number, unchanged across polls. Processing is what separates them and
	// must be reported beside it, never instead of it (mg-48d8).
	QueueLength int `json:"queue_length"`
	// Processing is the id of the merge request holding the serial slot, empty
	// when none is. ProcessingSince is when it took the slot.
	//
	// The absence of this field is why six agents independently reported the
	// refinery as stalled in one evening: every count they could see was of the
	// queue, the queue excludes the slot-holder, and "N queued, 0 processing"
	// is what a perfectly healthy serial pipeline looks like from there. Each
	// of them measured correctly and inferred wrongly, which makes it a defect
	// in the instrument rather than six mistakes.
	Processing      string    `json:"processing,omitempty"`
	ProcessingSince time.Time `json:"processing_since,omitempty"`
	RecentFailures  int       `json:"recent_failures"`
	HistoryLength   int       `json:"history_length"`
	PollInterval    string    `json:"poll_interval,omitempty"`
}
