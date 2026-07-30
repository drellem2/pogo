package refinery

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// errTestGate stands in for a gate failure in event-emission tests.
var errTestGate = errors.New("gate failed")

// writeLogLines writes raw JSONL event lines to path.
func writeLogLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// logLine renders one event log line.
func logLine(t *testing.T, ts time.Time, eventType, workItem, repo string, details map[string]any) string {
	t.Helper()
	rec := map[string]any{
		"schema_version": 1,
		"timestamp":      ts.Format(time.RFC3339Nano),
		"event_type":     eventType,
		"agent":          "refinery",
		"work_item_id":   workItem,
		"repo":           repo,
		"details":        details,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// mergedLine renders the attempt+merged pair for one MR.
func mergedLines(t *testing.T, ts time.Time, id, branch, author string) []string {
	t.Helper()
	return []string{
		logLine(t, ts, "refinery_merge_attempted", author, "/repo", map[string]any{
			"merge_request_id": id, "branch": branch, "target": "main", "attempt": 1, "author": author,
		}),
		logLine(t, ts.Add(time.Minute), "refinery_merged", author, "/repo", map[string]any{
			"merge_request_id": id, "branch": branch, "target": "main", "attempt": 1, "author": author,
			"merge_commit": "deadbeef",
		}),
	}
}

// failedLines renders the attempt+terminal-failure pair for one MR.
func failedLines(t *testing.T, ts time.Time, id, branch, author string) []string {
	t.Helper()
	return []string{
		logLine(t, ts, "refinery_merge_attempted", author, "/repo", map[string]any{
			"merge_request_id": id, "branch": branch, "target": "main", "attempt": 1, "author": author,
		}),
		logLine(t, ts.Add(time.Minute), "refinery_merge_failed", author, "/repo", map[string]any{
			"merge_request_id": id, "branch": branch, "target": "main", "attempt": 1, "author": author,
			"stage": "test", "reason": "gate failed", "terminal": true,
		}),
	}
}

func findByID(w *HistoryWindow, id string) *MergeRequest {
	for i := range w.Requests {
		if w.Requests[i].ID == id {
			return &w.Requests[i]
		}
	}
	return nil
}

// TestHistoryFromLogSeesPastTheRetentionCap is the ticket's acceptance test: an
// MR older than the retained window is ABSENT from History() and PRESENT once
// the event log is read for a wider window.
//
// It deliberately does not assert on row counts alone. A count of MaxHistoryLen
// is what the defect looks like and what a full-but-honest window looks like,
// so the assertion is about a NAMED old MR: gone from one, found in the other.
func TestHistoryFromLogSeesPastTheRetentionCap(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	base := time.Now().Add(-30 * 24 * time.Hour).Truncate(time.Second)

	// One old orphaned failure, then enough merges to push it past a cap of 5.
	var lines []string
	lines = append(lines, failedLines(t, base, "mr-ancient", "polecat-ancient", "mg-ancient")...)
	for i := 0; i < 20; i++ {
		ts := base.Add(time.Duration(i+1) * time.Hour)
		lines = append(lines, mergedLines(t, ts, fmt.Sprintf("mr-%02d", i), fmt.Sprintf("polecat-%02d", i), fmt.Sprintf("mg-%02d", i))...)
	}
	writeLogLines(t, logPath, lines...)

	// Replay the same completions into a Refinery whose cap is 5, so its
	// retained history is the truncated window a consumer sees today.
	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir(), MaxHistoryLen: 5, MaxHistoryAge: -1})
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.history = append(r.history, &MergeRequest{ID: "mr-ancient", Branch: "polecat-ancient", Status: StatusFailed, DoneTime: base})
	for i := 0; i < 20; i++ {
		r.history = append(r.history, &MergeRequest{
			ID: fmt.Sprintf("mr-%02d", i), Branch: fmt.Sprintf("polecat-%02d", i),
			Status: StatusMerged, DoneTime: base.Add(time.Duration(i+1) * time.Hour),
		})
	}
	r.pruneHistoryLocked()
	r.mu.Unlock()

	retained := r.History()
	if len(retained) != 5 {
		t.Fatalf("retained history: want 5 (the cap), got %d", len(retained))
	}
	for _, mr := range retained {
		if mr.ID == "mr-ancient" {
			t.Fatal("mr-ancient is still in the retained window; the fixture does not reproduce the cap")
		}
	}
	// The check the ticket says reports empty either way: a failed MR with no
	// later merge on the same branch. Over the retained window it finds nothing.
	if got := orphanedFailures(retained); len(got) != 0 {
		t.Fatalf("retained window found orphaned failures %v; the fixture is meant to be blind here", got)
	}

	// Same question, durable log, wider window.
	w, err := HistoryFromLog(logPath, base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("HistoryFromLog: %v", err)
	}
	if !w.Complete {
		t.Errorf("window should be complete: log starts at %s, asked for %s", w.Oldest, w.Since)
	}
	ancient := findByID(w, "mr-ancient")
	if ancient == nil {
		t.Fatalf("mr-ancient absent from the event-log window; ids=%v", ids(w))
	}
	if ancient.Status != StatusFailed {
		t.Errorf("mr-ancient status: want %q, got %q", StatusFailed, ancient.Status)
	}
	if ancient.Branch != "polecat-ancient" || ancient.Author != "mg-ancient" {
		t.Errorf("mr-ancient branch/author: got %q/%q", ancient.Branch, ancient.Author)
	}
	if got := orphanedFailures(w.Requests); len(got) != 1 || got[0] != "polecat-ancient" {
		t.Errorf("event-log window orphaned failures: want [polecat-ancient], got %v", got)
	}
}

// orphanedFailures is the step-3 relationship check in Go: a branch with a
// failed MR and no later merged MR. It mirrors the jq recipe in mayor.md so the
// test exercises the question the recipe asks, not just the data shape.
func orphanedFailures(rows []MergeRequest) []string {
	failed := map[string]bool{}
	merged := map[string]bool{}
	for _, mr := range rows {
		switch mr.Status {
		case StatusFailed:
			failed[mr.Branch] = true
		case StatusMerged:
			merged[mr.Branch] = true
		}
	}
	var out []string
	for b := range failed {
		if !merged[b] {
			out = append(out, b)
		}
	}
	return out
}

func ids(w *HistoryWindow) []string {
	out := make([]string, 0, len(w.Requests))
	for _, mr := range w.Requests {
		out = append(out, mr.ID)
	}
	return out
}

// TestHistoryFromLogIncompleteWindowIsLoud is the other half of acceptance: a
// window the log cannot cover must be reported as truncated, not returned as a
// short answer. A consumer that cannot tell is back where it started.
//
// The fixture fills the last rotation slot, which is what makes the log's first
// record a CUT rather than a beginning — see events.LogSpilled.
func TestHistoryFromLogIncompleteWindowIsLoud(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	floor := fillRotationSlots(t, logPath, start)
	writeLogLines(t, logPath, mergedLines(t, start.Add(90*time.Minute), "mr-1", "polecat-1", "mg-1")...)

	// Asking for a week when the log was cut off two hours ago.
	w, err := HistoryFromLog(logPath, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !w.Spilled {
		t.Fatal("fixture should look spilled; the truncation claim depends on it")
	}
	if w.Complete {
		t.Error("window claims complete but the log was cut off well after the requested since")
	}
	note := w.CoverageNote()
	if !strings.HasPrefix(note, "TRUNCATED") {
		t.Errorf("coverage note should lead with TRUNCATED, got %q", note)
	}
	if !strings.Contains(note, "cut off") {
		t.Errorf("note should say the log was cut off, not that it starts there: %q", note)
	}

	// And the covered case is reported as covered.
	w2, err := HistoryFromLog(logPath, floor.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !w2.Complete {
		t.Errorf("window should be complete; oldest=%s since=%s", w2.Oldest, w2.Since)
	}
	if strings.Contains(w2.CoverageNote(), "TRUNCATED") {
		t.Errorf("covered window should not say TRUNCATED: %q", w2.CoverageNote())
	}
}

// TestHistoryFromLogUnrotatedLogCoversEverything is the counterpart, and the
// distinction that keeps --since usable: a log that has never rotated has
// discarded nothing, so asking for a year is fully answered by a log two hours
// old. Reporting TRUNCATED here would cry wolf on every fresh install and train
// consumers to ignore the signal that matters.
func TestHistoryFromLogUnrotatedLogCoversEverything(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.log")
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	writeLogLines(t, logPath, mergedLines(t, start, "mr-1", "polecat-1", "mg-1")...)

	w, err := HistoryFromLog(logPath, time.Now().Add(-365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if w.Spilled {
		t.Fatal("a single unrotated log has not spilled")
	}
	if !w.Complete {
		t.Error("nothing was ever discarded, so the window is covered however far back it asks")
	}
	if strings.Contains(w.CoverageNote(), "TRUNCATED") {
		t.Errorf("note should not claim truncation: %q", w.CoverageNote())
	}
}

// fillRotationSlots writes a full set of rotated log files so the log looks
// spilled — i.e. so its oldest surviving record is a cut, not a beginning. Slot
// .N holds older records than .N-1, as real rotation leaves them. Returns the
// oldest timestamp written, which is the coverage floor the caller should
// measure against.
func fillRotationSlots(t *testing.T, logPath string, newest time.Time) time.Time {
	t.Helper()
	// events.maxRotatedFiles is unexported; probe upward until LogSpilled agrees
	// so this fixture cannot silently stop reproducing if that constant moves.
	floor := newest
	for i := 1; i <= 64; i++ {
		ts := newest.Add(-time.Duration(i) * time.Minute)
		writeLogLines(t, fmt.Sprintf("%s.%d", logPath, i),
			mergedLines(t, ts, fmt.Sprintf("mr-rot%d", i), fmt.Sprintf("polecat-rot%d", i), fmt.Sprintf("mg-rot%d", i))...)
		floor = ts
		if events.LogSpilled(logPath) {
			return floor
		}
	}
	t.Fatalf("could not make %s look spilled after 64 rotation slots", logPath)
	return time.Time{}
}

// TestHistoryFromLogEmptyLogCannotClaimCoverage guards the case that would
// otherwise be the most dangerous: no records at all. An empty result over a
// window nothing can vouch for must not report itself as complete.
func TestHistoryFromLogEmptyLogCannotClaimCoverage(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.log")

	w, err := HistoryFromLog(logPath, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("missing log should not error: %v", err)
	}
	if len(w.Requests) != 0 {
		t.Errorf("want no requests, got %d", len(w.Requests))
	}
	if w.Complete {
		t.Error("an empty log cannot vouch for a 24h window, but Complete is true")
	}

	// A zero `since` asks only for "whatever you have", which an empty log can
	// honestly answer.
	w2, err := HistoryFromLog(logPath, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !w2.Complete {
		t.Error("zero since means 'whatever the log holds'; that is answerable even when empty")
	}
}

// TestHistoryFromLogRetriedFailureIsNotAnOrphan is the distinction the step-3
// check turns on. A branch that failed once, was retried and landed must read as
// merged — counting the non-terminal failure as an outcome would report every
// branch that ever needed a second attempt.
func TestHistoryFromLogRetriedFailureIsNotAnOrphan(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.log")
	base := time.Now().Add(-6 * time.Hour).Truncate(time.Second)

	writeLogLines(t, logPath,
		logLine(t, base, "refinery_merge_attempted", "mg-retry", "/repo", map[string]any{
			"merge_request_id": "mr-retry", "branch": "polecat-retry", "target": "main", "attempt": 1, "author": "mg-retry",
		}),
		logLine(t, base.Add(time.Minute), "refinery_merge_failed", "mg-retry", "/repo", map[string]any{
			"merge_request_id": "mr-retry", "branch": "polecat-retry", "target": "main", "attempt": 1,
			"stage": "rebase", "reason": "conflict", "terminal": false,
		}),
		logLine(t, base.Add(2*time.Minute), "refinery_merged", "mg-retry", "/repo", map[string]any{
			"merge_request_id": "mr-retry", "branch": "polecat-retry", "target": "main", "attempt": 2,
			"merge_commit": "cafe", "author": "mg-retry",
		}),
	)

	w, err := HistoryFromLog(logPath, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	mr := findByID(w, "mr-retry")
	if mr == nil {
		t.Fatal("mr-retry missing")
	}
	if mr.Status != StatusMerged {
		t.Errorf("status: want %q, got %q", StatusMerged, mr.Status)
	}
	if mr.FailureCount != 1 {
		t.Errorf("FailureCount should record the retry: want 1, got %d", mr.FailureCount)
	}
	if got := orphanedFailures(w.Requests); len(got) != 0 {
		t.Errorf("a retried-and-landed branch is not an orphaned failure, got %v", got)
	}
	if w.Unresolved != 0 {
		t.Errorf("Unresolved: want 0, got %d", w.Unresolved)
	}
}

// TestHistoryFromLogUnresolvedIsNeitherOutcome covers an MR that is still in
// flight: its events are in the window but no terminal one is. It must appear —
// dropping it silently is the defect class — and it must count as neither
// merged nor failed.
func TestHistoryFromLogUnresolvedIsNeitherOutcome(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.log")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)

	writeLogLines(t, logPath,
		logLine(t, base, "refinery_merge_attempted", "mg-live", "/repo", map[string]any{
			"merge_request_id": "mr-live", "branch": "polecat-live", "target": "main", "attempt": 1, "author": "mg-live",
		}),
	)

	w, err := HistoryFromLog(logPath, base.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	mr := findByID(w, "mr-live")
	if mr == nil {
		t.Fatal("in-flight MR was dropped from the window; it must be visible, not absent")
	}
	if mr.Status != StatusProcessing {
		t.Errorf("status: want %q, got %q", StatusProcessing, mr.Status)
	}
	if w.Unresolved != 1 {
		t.Errorf("Unresolved: want 1, got %d", w.Unresolved)
	}
	if !strings.Contains(w.CoverageNote(), "still in flight") {
		t.Errorf("coverage note should name the in-flight MR: %q", w.CoverageNote())
	}
	if got := orphanedFailures(w.Requests); len(got) != 0 {
		t.Errorf("an in-flight MR is not a failure, got %v", got)
	}
}

// TestHistoryFromLogReadsRotatedFiles proves the coverage claim is not
// understated: records that have rotated into events.log.1 are still found, so
// --since does not report TRUNCATED for a window the log can actually answer.
func TestHistoryFromLogReadsRotatedFiles(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	old := time.Now().Add(-10 * 24 * time.Hour).Truncate(time.Second)
	recent := time.Now().Add(-time.Hour).Truncate(time.Second)

	writeLogLines(t, logPath+".2", failedLines(t, old, "mr-rotated", "polecat-rotated", "mg-rotated")...)
	writeLogLines(t, logPath+".1", mergedLines(t, old.Add(24*time.Hour), "mr-mid", "polecat-mid", "mg-mid")...)
	writeLogLines(t, logPath, mergedLines(t, recent, "mr-new", "polecat-new", "mg-new")...)

	w, err := HistoryFromLog(logPath, old.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !w.Complete {
		t.Errorf("rotated files cover the window; Complete should be true (oldest=%s)", w.Oldest)
	}
	if len(w.Files) != 3 {
		t.Errorf("want 3 log files read, got %d (%v)", len(w.Files), w.Files)
	}
	if findByID(w, "mr-rotated") == nil {
		t.Errorf("rotated-out MR not found; ids=%v", ids(w))
	}
	// Oldest first, across files.
	if got := ids(w); len(got) != 3 || got[0] != "mr-rotated" || got[2] != "mr-new" {
		t.Errorf("want [mr-rotated mr-mid mr-new], got %v", got)
	}
	if orph := orphanedFailures(w.Requests); len(orph) != 1 || orph[0] != "polecat-rotated" {
		t.Errorf("want the rotated failure flagged, got %v", orph)
	}
}

// TestHistoryFromLogSinceExcludesOlderOutcomes checks the filter itself: an MR
// whose last event predates `since` is out of the window.
func TestHistoryFromLogSinceExcludesOlderOutcomes(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.log")
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	recent := time.Now().Add(-time.Hour).Truncate(time.Second)

	var lines []string
	lines = append(lines, mergedLines(t, old, "mr-old", "polecat-old", "mg-old")...)
	lines = append(lines, mergedLines(t, recent, "mr-recent", "polecat-recent", "mg-recent")...)
	writeLogLines(t, logPath, lines...)

	w, err := HistoryFromLog(logPath, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if findByID(w, "mr-old") != nil {
		t.Error("mr-old is older than --since and should be filtered out")
	}
	if findByID(w, "mr-recent") == nil {
		t.Error("mr-recent is inside the window and should be present")
	}
	// The log reaches back further than asked, so the window is covered.
	if !w.Complete {
		t.Errorf("Complete should be true; oldest=%s since=%s", w.Oldest, w.Since)
	}
}

// TestStatusReportsRetentionBound checks that the count a client prints comes
// with the cap that bounds it, and that HistoryTruncated fires exactly when the
// cap has bitten.
func TestStatusReportsRetentionBound(t *testing.T) {
	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir(), MaxHistoryLen: 3, MaxHistoryAge: -1})
	if err != nil {
		t.Fatal(err)
	}
	st := r.GetStatus()
	if st.MaxHistoryLen == nil || *st.MaxHistoryLen != 3 {
		t.Errorf("MaxHistoryLen: want 3, got %v", st.MaxHistoryLen)
	}
	if st.MaxHistoryAge == nil || *st.MaxHistoryAge == "" {
		t.Error("MaxHistoryAge should be reported so the age bound is visible too")
	}
	if got := st.HistoryTruncation(); got != HistoryTruncationNone {
		t.Errorf("empty history is not truncated, got %v", got)
	}
	if got := st.RetentionSummary(); got != "max 3 entries / -1ns" {
		t.Errorf("RetentionSummary: got %q", got)
	}

	r.mu.Lock()
	for i := 0; i < 3; i++ {
		r.history = append(r.history, &MergeRequest{ID: fmt.Sprintf("mr-%d", i), Status: StatusMerged})
	}
	r.mu.Unlock()
	if got := r.GetStatus().HistoryTruncation(); got != HistoryTruncationAtCap {
		t.Errorf("history at the cap must report truncated (the next completion deletes the oldest row), got %v", got)
	}

	// Defaults must be reported too, since that is the configuration in
	// production — an unreported cap would print "max 0 entries".
	rd, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	got := rd.GetStatus().MaxHistoryLen
	if got == nil || *got != DefaultMaxHistoryLen {
		t.Errorf("default MaxHistoryLen should be reported: want %d, got %v", DefaultMaxHistoryLen, got)
	}
}

// TestStatusUnreportedRetentionIsUnknownNotUnbounded is the case that made the
// first cut of this change silently useless: a client talking to a pogod older
// than mg-e9ee gets no retention fields. Decoding that absence as "no cap" put
// the silence straight back — the notice vanished against the very daemon most
// likely to be truncating. It must classify as UNKNOWN, and say so.
func TestStatusUnreportedRetentionIsUnknownNotUnbounded(t *testing.T) {
	// Exactly what an older daemon's /refinery/status body decodes to.
	var st Status
	if err := json.Unmarshal([]byte(`{"enabled":true,"running":true,"poll_interval":"30s","queue_len":0,"history_len":100}`), &st); err != nil {
		t.Fatal(err)
	}
	if st.MaxHistoryLen != nil {
		t.Fatalf("an absent max_history_len must decode to nil, got %v", *st.MaxHistoryLen)
	}
	if got := st.HistoryTruncation(); got != HistoryTruncationUnknown {
		t.Errorf("unreported retention must be UNKNOWN, not %v — 100 rows with no cap reported is exactly the ambiguity this change removes", got)
	}
	if got := st.RetentionSummary(); !strings.Contains(got, "not reported") {
		t.Errorf("RetentionSummary should say the cap was not reported, got %q", got)
	}

	// A daemon that genuinely does not prune by count is a DIFFERENT answer.
	noCap := -1
	age := "168h0m0s"
	st2 := Status{HistoryLen: 100, MaxHistoryLen: &noCap, MaxHistoryAge: &age}
	if got := st2.HistoryTruncation(); got != HistoryTruncationNone {
		t.Errorf("an explicit no-count-cap is not truncated, got %v", got)
	}
	if got := st2.RetentionSummary(); got != "no count cap / 168h0m0s" {
		t.Errorf("RetentionSummary: got %q", got)
	}
}

// TestOutcomeEventsCarryAuthor guards the field HistoryFromLog needs to name an
// author when the attempt event has rotated out from under the outcome.
func TestOutcomeEventsCarryAuthor(t *testing.T) {
	path := useTempEventLog(t)
	mr := &MergeRequest{ID: "mr-a", Branch: "polecat-a", TargetRef: "main", Author: "cat-mg-a", RepoPath: "/repo"}

	emitMerged(mr, 1, "abc123", 1.5, false)
	emitMergeFailed(mr, 1, "test", errTestGate, true, "")
	emitMergeCancelled(mr, 1, "test", "")

	for _, ev := range readEvents(t, path) {
		got, _ := ev.Details["author"].(string)
		if got != "cat-mg-a" {
			t.Errorf("%s: author detail want %q, got %q", ev.EventType, "cat-mg-a", got)
		}
		if ev.WorkItemID != "mg-a" {
			t.Errorf("%s: work_item_id want %q, got %q — author and work_item_id are different strings, which is why both are carried", ev.EventType, "mg-a", ev.WorkItemID)
		}
	}
}

// TestStatusEndpointCarriesRetentionOverTheWire closes the seam the pointer
// encoding exists for. The classification is only useful if the retention
// actually survives JSON to the client — and the client is the thing that
// decides whether to warn.
func TestStatusEndpointCarriesRetentionOverTheWire(t *testing.T) {
	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir(), MaxHistoryLen: 2, MaxHistoryAge: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.history = append(r.history,
		&MergeRequest{ID: "mr-0", Status: StatusMerged},
		&MergeRequest{ID: "mr-1", Status: StatusMerged})
	r.mu.Unlock()

	mux := http.NewServeMux()
	r.RegisterHandlers(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/refinery/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}

	var got Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if got.MaxHistoryLen == nil || *got.MaxHistoryLen != 2 {
		t.Errorf("max_history_len did not survive the wire: %s", rec.Body.String())
	}
	if got.MaxHistoryAge == nil || *got.MaxHistoryAge != "48h0m0s" {
		t.Errorf("max_history_age did not survive the wire: %s", rec.Body.String())
	}
	if c := got.HistoryTruncation(); c != HistoryTruncationAtCap {
		t.Errorf("a client decoding this payload must see AT CAP, got %v (%s)", c, rec.Body.String())
	}

	// The disabled daemon reports no refinery and therefore no cap. That must
	// read as UNKNOWN rather than as a reassuring "not truncated" — there is no
	// history to truncate, but there is also nothing vouching for it.
	dmux := http.NewServeMux()
	RegisterDisabledHandlers(dmux)
	drec := httptest.NewRecorder()
	dmux.ServeHTTP(drec, httptest.NewRequest(http.MethodGet, "/refinery/status", nil))
	var dis Status
	if err := json.Unmarshal(drec.Body.Bytes(), &dis); err != nil {
		t.Fatal(err)
	}
	if dis.HistoryTruncation() != HistoryTruncationUnknown {
		t.Errorf("a disabled refinery reports no cap; that is UNKNOWN, got %v", dis.HistoryTruncation())
	}
}
