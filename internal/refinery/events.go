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
// alreadyMerged marks the no-op resolution of a re-submitted branch that had
// already landed on the target (gh #34) — no gates ran, nothing was pushed.
func emitMerged(mr *MergeRequest, attempt int, mergeCommit string, durationSec float64, alreadyMerged bool) {
	details := map[string]any{
		"merge_request_id": mr.ID,
		"branch":           mr.Branch,
		"target":           mr.TargetRef,
		"merge_commit":     mergeCommit,
		"attempt":          attempt,
		// author is carried on the outcome events, not only on
		// refinery_merge_attempted, so HistoryFromLog can name the author of a
		// merge whose attempt event has rotated out from under it. work_item_id
		// is close but not the same string — it is the author with any "cat-"
		// prefix stripped.
		"author": mr.Author,
	}
	if durationSec > 0 {
		details["duration_seconds"] = durationSec
	}
	if alreadyMerged {
		details["already_merged"] = true
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_merged",
		Agent:      "refinery",
		WorkItemID: workItemIDFromAuthor(mr.Author),
		Repo:       mr.RepoPath,
		Details:    details,
	})
}

// emitDeployAttempted writes a refinery_deploy_attempted event before the
// per-repo post-merge deploy command runs.
func emitDeployAttempted(mr *MergeRequest, command string) {
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_deploy_attempted",
		Agent:      "refinery",
		WorkItemID: workItemIDFromAuthor(mr.Author),
		Repo:       mr.RepoPath,
		Details: map[string]any{
			"merge_request_id": mr.ID,
			"repo":             mr.RepoPath,
			"command":          command,
		},
	})
}

// emitDeployed writes a refinery_deployed event for a successful deploy.
func emitDeployed(mr *MergeRequest, durationSec float64) {
	details := map[string]any{
		"merge_request_id": mr.ID,
		"repo":             mr.RepoPath,
	}
	if durationSec > 0 {
		details["duration_seconds"] = durationSec
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_deployed",
		Agent:      "refinery",
		WorkItemID: workItemIDFromAuthor(mr.Author),
		Repo:       mr.RepoPath,
		Details:    details,
	})
}

// emitDeployFailed writes a refinery_deploy_failed event when the deploy
// command exits non-zero. The merge is already landed at this point — the
// event is for diagnostics and operator alerting.
func emitDeployFailed(mr *MergeRequest, err error, output string) {
	details := map[string]any{
		"merge_request_id": mr.ID,
		"repo":             mr.RepoPath,
		"reason":           summarizeReason(err),
	}
	if output != "" {
		details["output_truncated"] = truncate(output, gateOutputCap)
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_deploy_failed",
		Agent:      "refinery",
		WorkItemID: workItemIDFromAuthor(mr.Author),
		Repo:       mr.RepoPath,
		Details:    details,
	})
}

// emitPostMergeTagged writes a refinery_post_merge_tagged event when the
// refinery has created and pushed a declared post-merge tag. It names the SHA
// so the log answers "what did v0.8.0 land on" without a git round-trip —
// which is the question that went unanswerable when the tag step lived in a
// worker that was reaped before performing it (mg-6879).
func emitPostMergeTagged(mr *MergeRequest, tag, sha string) {
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_post_merge_tagged",
		Agent:      "refinery",
		WorkItemID: workItemIDFromAuthor(mr.Author),
		Repo:       mr.RepoPath,
		Details: map[string]any{
			"merge_request_id": mr.ID,
			"repo":             mr.RepoPath,
			"tag":              tag,
			"merged_sha":       sha,
			"target_ref":       mr.TargetRef,
		},
	})
}

// emitPostMergeTagFailed writes a refinery_post_merge_tag_failed event when a
// declared post-merge tag could not be created or pushed. The merge has
// already landed; this is the record that its deliverable has not, and it is
// paired with pogod refusing to complete the work item.
func emitPostMergeTagFailed(mr *MergeRequest, tag, sha string, err error) {
	details := map[string]any{
		"merge_request_id": mr.ID,
		"repo":             mr.RepoPath,
		"tag":              tag,
		"target_ref":       mr.TargetRef,
		"reason":           summarizeReason(err),
	}
	if sha != "" {
		details["merged_sha"] = sha
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_post_merge_tag_failed",
		Agent:      "refinery",
		WorkItemID: workItemIDFromAuthor(mr.Author),
		Repo:       mr.RepoPath,
		Details:    details,
	})
}

// emitRecoveryLost writes a refinery_mr_lost event when restart recovery
// could not resolve an in-flight MR (branch deleted, remote unreachable).
// The MR moves to the lost list; `refinery show` answers 410/status=lost so
// the author can resubmit.
func emitRecoveryLost(mr *MergeRequest, err error) {
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_mr_lost",
		Agent:      "refinery",
		WorkItemID: workItemIDFromAuthor(mr.Author),
		Repo:       mr.RepoPath,
		Details: map[string]any{
			"merge_request_id": mr.ID,
			"branch":           mr.Branch,
			"target":           mr.TargetRef,
			"author":           mr.Author,
			"reason":           summarizeReason(err),
		},
	})
}

// emitMergeCancelled writes a refinery_merge_cancelled event for a merge an
// operator stopped.
//
// It is deliberately NOT a refinery_merge_failed. A cancelled merge did not
// fail on its merits, and an event log that says otherwise is the same class of
// defect as the one mg-8595 documents: a record that cannot separate two states
// needing opposite responses. Anything counting merge failures — an author's
// failure streak, a reliability trend — would otherwise be counting operator
// actions as branch defects.
func emitMergeCancelled(mr *MergeRequest, attempt int, stage string, gateOutput string) {
	if stage == "" {
		stage = "unknown"
	}
	details := map[string]any{
		"merge_request_id": mr.ID,
		"branch":           mr.Branch,
		"target":           mr.TargetRef,
		"attempt":          attempt,
		"author":           mr.Author,
		// stage names where the pipeline stopped, so a cancel that landed
		// during the gates is distinguishable from one that landed between
		// attempts.
		"stage": stage,
	}
	if gateOutput != "" {
		details["gate_output_truncated"] = truncate(gateOutput, gateOutputCap)
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "refinery_merge_cancelled",
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
		"author":           mr.Author,
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
