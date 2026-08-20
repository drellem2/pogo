// Package health defines the structured response types for the
// pogod /health/full endpoint, shared between the daemon and clients.
package health

import "time"

// LivenessBody is the exact body GET /health returns. It is exported because
// it is the only thing that distinguishes pogod from ANY other process that
// happens to be answering on the daemon port: a `kubectl port-forward`, an
// `ssh -L`, a second daemon. A probe that treats "the TCP dial succeeded" or
// even "HTTP 200" as proof of pogod is exactly what let an interloper on
// ::1:10000 be read as a healthy pogod for ~20 minutes on 2026-07-31
// (drellem2/pogo#110), so the loopback-resolution check in `pogo doctor
// --check` matches on this string. Both the handler and that probe must read
// it from here — two copies of the literal is one copy that can go stale
// while the check keeps reporting pass.
const LivenessBody = "pogo is up and bouncing"

// FullResponse is the JSON response for GET /health/full.
type FullResponse struct {
	Pogod    Pogod    `json:"pogod"`
	Agents   Agents   `json:"agents"`
	Refinery Refinery `json:"refinery"`
}

// Pogod reports basic daemon health.
//
// PID is the pid of the process that SERVED this response, and it is here so
// that "what is pogod's pid" has an answer that does not go through `pgrep`.
// On macOS `pgrep`/`pkill` exclude the calling process and every one of its
// ancestors unless passed `-a` (`man pgrep`), and pogod is the ancestor of
// every agent it spawns — so `pgrep -x pogod` run from any agent returns empty
// at exit 1 while pogod is demonstrably serving on its port (measured
// 2026-08-20, mg-cbee; see docs/investigations/pgrep-cannot-see-pogod-2026-08-20.md).
// An empty pattern match is then substituted into whatever command was wrapped
// around it, which is where the silent part starts. This field is the
// instrument that construction was standing in for: it is served by pogod
// itself, so it cannot report a pid for a daemon that is not answering — the
// request fails instead.
type Pogod struct {
	Status string `json:"status"`
	Uptime string `json:"uptime"`
	Mode   string `json:"mode"`
	PID    int    `json:"pid"`
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
