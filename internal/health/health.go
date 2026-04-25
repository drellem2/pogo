// Package health defines the structured response types for the
// pogod /health/full endpoint, shared between the daemon and clients.
package health

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
	Enabled        bool   `json:"enabled"`
	Running        bool   `json:"running"`
	QueueLength    int    `json:"queue_length"`
	RecentFailures int    `json:"recent_failures"`
	HistoryLength  int    `json:"history_length"`
	PollInterval   string `json:"poll_interval,omitempty"`
}
