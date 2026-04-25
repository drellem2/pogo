package refinery

import (
	"context"
	"strings"

	"github.com/drellem2/pogo/internal/events"
)

// gateOutputCap caps the gate_output_truncated field per docs/event-log.md
// (≈1 KB). Keep events small so concurrent writes stay atomic against the
// PIPE_BUF threshold; longer lines fall back to flock inside events.Emit.
const gateOutputCap = 1024

// reasonCap is the maximum length of the reason field in refinery_merge_failed
// events. The schema specifies a single line ≤ 200 chars.
const reasonCap = 200

// workItemIDFromAuthor derives the work_item_id field from the MR's author.
// Polecat naming is "cat-<work-item-id>" in some contexts; production submits
// pass the work item ID directly (e.g. "mg-287e"). Strip the cat- prefix when
// present so the event always carries the bare work item ID.
func workItemIDFromAuthor(author string) string {
	return strings.TrimPrefix(author, "cat-")
}

// truncate returns s capped at n bytes, on a UTF-8 boundary, with no trailing
// whitespace. Returns the input unchanged when shorter than n.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Step back to a UTF-8 boundary so we don't split a rune.
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// summarizeReason collapses an error to a single trimmed line capped at
// reasonCap characters, suitable for the refinery_merge_failed.reason field.
func summarizeReason(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return truncate(strings.TrimSpace(msg), reasonCap)
}

// emitMergeAttempted writes a refinery_merge_attempted event for a new attempt.
func emitMergeAttempted(mr *MergeRequest, attempt int) {
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_merge_attempted",
		Agent:      "refinery",
		WorkItemID: workItemIDFromAuthor(mr.Author),
		Repo:       mr.RepoPath,
		Details: map[string]any{
			"merge_request_id": mr.ID,
			"branch":           mr.Branch,
			"target":           mr.TargetRef,
			"attempt":          attempt,
			"author":           mr.Author,
		},
	})
}

// emitMerged writes a refinery_merged event for a successful merge.
func emitMerged(mr *MergeRequest, attempt int, mergeCommit string, durationSec float64) {
	details := map[string]any{
		"merge_request_id": mr.ID,
		"branch":           mr.Branch,
		"target":           mr.TargetRef,
		"merge_commit":     mergeCommit,
		"attempt":          attempt,
	}
	if durationSec > 0 {
		details["duration_seconds"] = durationSec
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_merged",
		Agent:      "refinery",
		WorkItemID: workItemIDFromAuthor(mr.Author),
		Repo:       mr.RepoPath,
		Details:    details,
	})
}

// emitMergeFailed writes a refinery_merge_failed event for a failed attempt.
// terminal=true means the refinery has given up on this MR (no more retries).
func emitMergeFailed(mr *MergeRequest, attempt int, stage string, err error, terminal bool, gateOutput string) {
	if stage == "" {
		stage = "unknown"
	}
	details := map[string]any{
		"merge_request_id": mr.ID,
		"branch":           mr.Branch,
		"target":           mr.TargetRef,
		"attempt":          attempt,
		"stage":            stage,
		"reason":           summarizeReason(err),
		"terminal":         terminal,
	}
	if gateOutput != "" {
		details["gate_output_truncated"] = truncate(gateOutput, gateOutputCap)
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_merge_failed",
		Agent:      "refinery",
		WorkItemID: workItemIDFromAuthor(mr.Author),
		Repo:       mr.RepoPath,
		Details:    details,
	})
}
