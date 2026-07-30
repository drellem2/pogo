package refinery

import (
	"fmt"
	"sort"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// HistoryWindow is a reconstruction of completed merge requests from the
// durable event log, plus an explicit statement of what the reconstruction does
// and does not cover.
//
// It exists because the in-memory history is not an archive. pruneHistoryLocked
// DELETES entries past MaxHistoryLen / MaxHistoryAge and persists the deletion,
// so `History()` cannot be asked for a wider window — there is nothing behind
// the 100th row to widen to. The event log is the durable record
// (docs/event-log.md: "survives pogod restarts (unlike the in-memory refinery
// history). This is the durable observability spine"), and this type is how a
// question about last week gets answered from it.
//
// Coverage is part of the answer, not a footnote. A caller that reconstructs a
// window and cannot tell whether the window was fully covered is in exactly the
// position this type was built to end: it would read an empty result as "no
// failures" when the honest reading is "no failures I could see". Complete is
// therefore returned alongside Requests, and the CLI turns Complete=false into
// a non-zero exit so a shell consumer cannot ignore it.
type HistoryWindow struct {
	// Requests holds one entry per merge request, oldest first — the same
	// ordering and the same type as History(), so a consumer's pipeline is
	// identical whether it reads the live window or this one.
	Requests []MergeRequest
	// Since is the lower bound that was asked for.
	Since time.Time
	// Oldest is the timestamp of the oldest record found in the log files
	// read, of any event type.
	//
	// Zero when no records were found at all.
	Oldest time.Time
	// Spilled reports whether rotation may have discarded records. It is what
	// separates "the log starts at Oldest" from "the log was CUT OFF at
	// Oldest": when false, Oldest is simply the first thing that ever happened
	// and a window reaching further back misses nothing.
	Spilled bool
	// Complete reports whether the log's coverage reaches back to Since. When
	// false, Requests is a truncated answer and must not be read as a complete
	// one: records between Since and Oldest were discarded and are gone.
	Complete bool
	// Unresolved counts merge requests whose events are in the window but which
	// carry no terminal outcome — a merge still in flight, or one whose
	// terminal event has not been written yet. They appear in Requests with
	// StatusProcessing so they are neither counted as merged nor as failed.
	Unresolved int
	// Files lists the log files read, oldest first.
	Files []string
}

// mergeLifecycleTypes are the event types that carry a merge request through
// its life. refinery_merge_attempted is included because it is the only one
// that records the author, and because it is what makes an in-flight MR
// visible rather than absent.
var mergeLifecycleTypes = map[string]bool{
	"refinery_merge_attempted": true,
	"refinery_merged":          true,
	"refinery_merge_failed":    true,
	"refinery_merge_cancelled": true,
}

// HistoryFromLog reconstructs completed merge requests from the durable event
// log at livePath and its surviving rotated files, keeping those whose most
// recent event is at or after since.
//
// A zero since means "everything the log still holds" — which is bounded by
// rotation, not unbounded, and the returned HistoryWindow says so.
//
// Fidelity limits, stated because a reconstruction that overstates itself is
// the defect this function exists to fix:
//
//   - SubmitTime is the earliest event seen for the MR, i.e. its first merge
//     ATTEMPT. The true submit time is slightly earlier and is not in the log.
//   - Author comes from the refinery_merge_attempted detail when that event is
//     in the window, and otherwise falls back to the event's work_item_id —
//     which is the author with any "cat-" prefix already stripped.
//   - GateOutput is always empty. The log carries only a ~1 KB truncation of
//     gate output, and putting that in a field named GateOutput would claim
//     more than it holds; use `pogo events list --type=refinery_merge_failed`
//     for it.
func HistoryFromLog(livePath string, since time.Time) (*HistoryWindow, error) {
	files := events.LogFiles(livePath)
	w := &HistoryWindow{Since: since, Files: files, Spilled: events.LogSpilled(livePath)}

	type builder struct {
		mr        MergeRequest
		first     time.Time
		last      time.Time
		terminal  bool
		failures  int
		seenOrder int
	}
	byID := map[string]*builder{}
	order := 0

	for _, f := range files {
		err := events.ScanFile(f, func(ev events.Event) {
			ts, terr := time.Parse(time.RFC3339Nano, ev.Timestamp)
			if terr != nil {
				// An unparseable timestamp cannot be placed in or out of the
				// window, so it is not evidence either way. It still counts
				// toward nothing rather than being silently trusted.
				return
			}
			// Every record, of any type, informs the coverage floor: the log's
			// start is what bounds the answer, not the first refinery event.
			if w.Oldest.IsZero() || ts.Before(w.Oldest) {
				w.Oldest = ts
			}
			if !mergeLifecycleTypes[ev.EventType] {
				return
			}
			id, _ := ev.Details["merge_request_id"].(string)
			if id == "" {
				return
			}
			b := byID[id]
			if b == nil {
				b = &builder{first: ts, seenOrder: order}
				order++
				b.mr.ID = id
				b.mr.Status = StatusProcessing
				byID[id] = b
			}
			if ts.Before(b.first) {
				b.first = ts
			}
			if ts.After(b.last) {
				b.last = ts
			}
			b.mr.RepoPath = ev.Repo
			if s, ok := ev.Details["branch"].(string); ok && s != "" {
				b.mr.Branch = s
			}
			if s, ok := ev.Details["target"].(string); ok && s != "" {
				b.mr.TargetRef = s
			}
			if s, ok := ev.Details["author"].(string); ok && s != "" {
				b.mr.Author = s
			} else if b.mr.Author == "" && ev.WorkItemID != "" {
				b.mr.Author = ev.WorkItemID
			}

			switch ev.EventType {
			case "refinery_merged":
				b.mr.Status = StatusMerged
				b.mr.DoneTime = ts
				b.terminal = true
				if v, ok := ev.Details["already_merged"].(bool); ok {
					b.mr.AlreadyMerged = v
				}
			case "refinery_merge_cancelled":
				b.mr.Status = StatusCancelled
				b.mr.DoneTime = ts
				b.terminal = true
			case "refinery_merge_failed":
				b.failures++
				// Only a terminal failure means the refinery gave up. A
				// non-terminal one is a retry, and treating it as an outcome
				// would report every branch that ever needed a second attempt
				// as failed.
				if v, ok := ev.Details["terminal"].(bool); ok && v {
					b.mr.Status = StatusFailed
					b.mr.DoneTime = ts
					b.terminal = true
				}
				if s, ok := ev.Details["reason"].(string); ok {
					b.mr.Error = s
				}
			}
		})
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
	}

	switch {
	case len(files) == 0, w.Oldest.IsZero():
		// No log, or a log holding no readable records. This cannot be told
		// apart from "the log is missing", and on a fleet whose refinery emits
		// on every merge the second is by far the likelier. So only a zero
		// `since` — "whatever you have" — is answerable here.
		w.Complete = since.IsZero()
	case since.IsZero():
		w.Complete = true
	case !w.Spilled:
		// Rotation has never discarded a chunk, so Oldest is the beginning of
		// recorded history rather than a cut. A window starting before it is
		// still fully answered: there was nothing there to miss.
		w.Complete = true
	default:
		w.Complete = !w.Oldest.After(since)
	}

	kept := make([]*builder, 0, len(byID))
	for _, b := range byID {
		if !since.IsZero() && b.last.Before(since) {
			continue
		}
		kept = append(kept, b)
	}
	// Oldest first, matching History(). Ties fall back to first-seen file order
	// so the ordering is total and stable across runs.
	sort.Slice(kept, func(i, j int) bool {
		if !kept[i].last.Equal(kept[j].last) {
			return kept[i].last.Before(kept[j].last)
		}
		return kept[i].seenOrder < kept[j].seenOrder
	})

	w.Requests = make([]MergeRequest, 0, len(kept))
	for _, b := range kept {
		b.mr.SubmitTime = b.first
		b.mr.FailureCount = b.failures
		if !b.terminal {
			w.Unresolved++
		}
		w.Requests = append(w.Requests, b.mr)
	}
	return w, nil
}

// CoverageNote renders the window's bound as one operator-facing line.
//
// It is always non-empty: a reader is told the bound whether or not it bites,
// because "no bound stated" is indistinguishable from "no bound" and that
// confusion is the whole defect. The incomplete case leads with the word
// TRUNCATED so it is not skimmed past.
func (w *HistoryWindow) CoverageNote() string {
	span := "the log holds no records"
	if !w.Oldest.IsZero() {
		verb := "log starts"
		if w.Spilled {
			verb = "log cut off at"
		}
		span = fmt.Sprintf("%s %s", verb, w.Oldest.Format(time.RFC3339))
	}
	suffix := ""
	if w.Unresolved > 0 {
		suffix = fmt.Sprintf("; %d still in flight (status=processing, counted as neither merged nor failed)", w.Unresolved)
	}
	if !w.Complete {
		return fmt.Sprintf("TRUNCATED: %d merge requests, but the event log does not reach back to the requested window (%s) — records in between were discarded and are gone. This answer is NOT complete%s.",
			len(w.Requests), span, suffix)
	}
	return fmt.Sprintf("%d merge requests from the event log; window fully covered (%s)%s.",
		len(w.Requests), span, suffix)
}
