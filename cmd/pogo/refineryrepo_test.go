package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/refinery"
)

// multiRepoHistory is the arrangement all three mg-ff3a incidents were read
// from: several repos merging through one queue, with the reader's own repo a
// minority of the rows.
func multiRepoHistory() []refinery.MergeRequest {
	done := time.Date(2026, 8, 7, 22, 52, 0, 0, time.UTC)
	return []refinery.MergeRequest{
		{ID: "mr-a", RepoPath: "/Users/d/research/onethird_program", Branch: "polecat-paeba7", Author: "mg-aeba7", Status: refinery.StatusMerged, DoneTime: done},
		{ID: "mr-b", RepoPath: "/Users/d/research/onethird_program", Branch: "polecat-pa3e06", Author: "mg-a3e06", Status: refinery.StatusMerged, DoneTime: done.Add(time.Minute)},
		{ID: "mr-c", RepoPath: "/Users/d/dev/pogo", Branch: "polecat-pff3a", Author: "mg-ff3a", Status: refinery.StatusMerged, DoneTime: done.Add(2 * time.Minute)},
		{ID: "mr-d", RepoPath: "/Users/d/dev/bridget", Branch: "polecat-pb0db1", Author: "mg-b0db1", Status: refinery.StatusFailed, DoneTime: done.Add(3 * time.Minute)},
	}
}

// TestHistoryRowNamesItsRepo is the direct regression for mg-ff3a. `pogo
// refinery history` printed branch, author, status and time but never the repo,
// while the refinery served several repos from one queue — so a reader who
// checked whether a reported merge had actually landed checked the wrong main,
// found it unchanged, and escalated lost merges that were not lost.
func TestHistoryRowNamesItsRepo(t *testing.T) {
	for _, mr := range multiRepoHistory() {
		row := formatHistoryRow(mr)
		want := "repo=" + repoColumn(mr.RepoPath)
		if !strings.Contains(row, want) {
			t.Errorf("history row for %s must name its repo (%s), got:\n%s", mr.ID, want, row)
		}
	}

	// The discriminating case: two rows identical in every OTHER field are the
	// arrangement that made the field's absence invisible.
	done := time.Date(2026, 8, 7, 22, 52, 0, 0, time.UTC)
	a := formatHistoryRow(refinery.MergeRequest{ID: "mr-x", RepoPath: "/a/pogo", Branch: "b", Author: "mg-1", Status: refinery.StatusMerged, DoneTime: done})
	b := formatHistoryRow(refinery.MergeRequest{ID: "mr-x", RepoPath: "/a/onethird_program", Branch: "b", Author: "mg-1", Status: refinery.StatusMerged, DoneTime: done})
	if a == b {
		t.Fatalf("two merges into DIFFERENT repos still render identically:\n%s", a)
	}
}

// TestRepoColumnDoesNotInventADotRepo covers the unknown-repo rendering.
// filepath.Base("") is ".", which reads as a real relative path — the column
// would answer a question it has no answer to, which is the failure mode it was
// added to remove.
func TestRepoColumnDoesNotInventADotRepo(t *testing.T) {
	for _, in := range []string{"", "   ", "."} {
		if got := repoColumn(in); got != "?" {
			t.Errorf("repoColumn(%q) = %q, want %q — an unknown repo must not render as a path", in, got, "?")
		}
	}
	if got := repoColumn("/Users/d/dev/pogo"); got != "pogo" {
		t.Errorf("repoColumn = %q, want pogo", got)
	}
	// The lane key is the refinery's own, so a trailing separator resolves the
	// same way the merge scheduler resolves it.
	if got := repoColumn("/Users/d/dev/pogo/"); got != "pogo" {
		t.Errorf("repoColumn with trailing slash = %q, want pogo", got)
	}
}

// TestRepoFilterAcceptsNameAndPath: the value most likely to be pasted in is a
// full path — out of `--json`'s repo_path, or out of the `refinery submit
// --repo=` invocation that created the row.
func TestRepoFilterAcceptsNameAndPath(t *testing.T) {
	rows := multiRepoHistory()
	for _, v := range []string{"pogo", "/Users/d/dev/pogo", "/Users/d/dev/pogo/"} {
		kept, dropped := parseRepoFilter(v).apply(rows)
		if len(kept) != 1 || kept[0].ID != "mr-c" {
			t.Errorf("--repo=%s kept %d rows, want just mr-c", v, len(kept))
		}
		if dropped != 3 {
			t.Errorf("--repo=%s dropped %d, want 3", v, dropped)
		}
	}
	// An empty filter is the no-flag case and must not narrow anything.
	kept, dropped := parseRepoFilter("").apply(rows)
	if len(kept) != len(rows) || dropped != 0 {
		t.Errorf("empty filter narrowed the view: kept %d dropped %d", len(kept), dropped)
	}
	// A prefix is not a match. onethird would otherwise silently capture
	// onethird_program, and a reader would take a foreign repo's rows as
	// their own — the mg-ff3a error with an extra step.
	if kept, _ := parseRepoFilter("onethird").apply(rows); len(kept) != 0 {
		t.Errorf("--repo=onethird must not match onethird_program, kept %d", len(kept))
	}
}

// TestFilteredEmptyResultIsNotAnEmptyRefinery is the check that keeps the
// remedy from exhibiting the defect it remedies. "No merge history." under a
// filter that matched nothing is byte-identical to a refinery that has done
// nothing — so a mistyped repo name, or '.' run from a worktree whose basename
// is not the repo's, would produce the exact confident-wrong-conclusion this
// ticket is about.
func TestFilteredEmptyResultIsNotAnEmptyRefinery(t *testing.T) {
	rows := multiRepoHistory()
	f := parseRepoFilter("pff3a") // a worktree basename: matches nothing
	note := noMatchNote(f, rows, "merge history")

	if !strings.Contains(note, "pff3a") {
		t.Errorf("the note must name the repo that was asked for, got: %s", note)
	}
	for _, lane := range []string{"pogo", "onethird_program", "bridget"} {
		if !strings.Contains(note, lane) {
			t.Errorf("the note must list the repos that ARE present (missing %s), got: %s", lane, note)
		}
	}
	if !strings.Contains(note, "4 rows") {
		t.Errorf("the note must say how many rows exist unfiltered, got: %s", note)
	}

	// A genuinely empty pipeline says so, and says the filter is not the
	// reason — the opposite mistake, and just as expensive.
	empty := noMatchNote(f, nil, "merge history")
	if !strings.Contains(empty, "in any repo") || !strings.Contains(empty, "not why this is empty") {
		t.Errorf("an unfiltered-empty result must exonerate the filter, got: %s", empty)
	}
}

// TestFilteredViewSaysItIsFiltered. A narrowed view that does not announce the
// narrowing is the same instrument defect one level up: the reader filters to
// their own repo and then reasons about "the queue".
func TestFilteredViewSaysItIsFiltered(t *testing.T) {
	rows := multiRepoHistory()
	f := parseRepoFilter("pogo")
	_, dropped := f.apply(rows)
	note := hiddenNote(f, dropped, rows)

	if !strings.Contains(note, "3 rows hidden") {
		t.Errorf("the note must count what it hid, got: %s", note)
	}
	if !strings.Contains(note, "onethird_program") || !strings.Contains(note, "bridget") {
		t.Errorf("the note must name where the hidden rows are, got: %s", note)
	}
	if !strings.Contains(note, "not the whole pipeline") {
		t.Errorf("the note must say the view is a subset, got: %s", note)
	}
	// The filtered repo is never listed among what was hidden from it — "3 rows
	// hidden in pogo, onethird_program, bridget" tells a --repo=pogo reader
	// their own repo is among the things they cannot see.
	if strings.Contains(note, "in pogo") || strings.Contains(note, ", pogo") {
		t.Errorf("the filtered repo must not appear in its own hidden-in list, got: %s", note)
	}
	if got := f.otherLanes(rows); strings.Join(got, ",") != "bridget,onethird_program" {
		t.Errorf("otherLanes = %v, want the excluded lanes only", got)
	}

	// Nothing hidden, nothing to say.
	if n := hiddenNote(f, 0, rows); n != "" {
		t.Errorf("a filter that hid nothing must be silent, got: %s", n)
	}
	if n := hiddenNote(repoFilter{}, 0, rows); n != "" {
		t.Errorf("no filter, no note, got: %s", n)
	}
}

// TestFilteredQueueKeepsTheAlarmWholePipeline is the third incident encoded as
// a test. pe2a0 read "12 queued, none processing, nothing merged" as a stalled
// refinery while merges were landing steadily in another repo. If --repo
// narrowed the health statement as well as the rows, this view would MANUFACTURE
// that reading for anyone whose own lane happens to be idle.
func TestFilteredQueueKeepsTheAlarmWholePipeline(t *testing.T) {
	now := time.Now()
	queue := []refinery.MergeRequest{
		// Merging steadily — in a repo the reader is not watching.
		{ID: "mr-run", RepoPath: "/r/onethird_program", Branch: "b-run", Status: refinery.StatusProcessing, StartTime: now.Add(-time.Minute)},
		{ID: "mr-mine", RepoPath: "/r/pogo", Branch: "b-mine", Status: refinery.StatusQueued, SubmitTime: now.Add(-time.Minute)},
	}

	out := formatQueueFiltered(queue, parseRepoFilter("pogo"), now)
	t.Logf("FILTERED QUEUE:\n%s", out)

	if strings.Contains(out, "NOTHING IN FLIGHT") {
		t.Errorf("a merge IS in flight, in another repo; the filtered view must not report a stall:\n%s", out)
	}
	if !strings.Contains(out, "mr-mine") {
		t.Errorf("the filtered view must show the matching row:\n%s", out)
	}
	if strings.Contains(out, "mr-run") {
		t.Errorf("the filtered view must not show another repo's row:\n%s", out)
	}
	if !strings.Contains(out, "1 row hidden in onethird_program") {
		t.Errorf("the filtered view must say what it hid and where:\n%s", out)
	}

	// And the alarm still fires when it is genuinely true, filter or not.
	stalled := []refinery.MergeRequest{
		{ID: "mr-1", RepoPath: "/r/pogo", Branch: "b-1", Status: refinery.StatusQueued, SubmitTime: now.Add(-time.Hour)},
		{ID: "mr-2", RepoPath: "/r/onethird_program", Branch: "b-2", Status: refinery.StatusQueued, SubmitTime: now.Add(-time.Hour)},
	}
	if out := formatQueueFiltered(stalled, parseRepoFilter("pogo"), now); !strings.Contains(out, "NOTHING IN FLIGHT") {
		t.Errorf("nothing is in flight anywhere; the alarm must survive filtering:\n%s", out)
	}

	// A filter matching nothing must not read as an empty pipeline either...
	out = formatQueueFiltered(queue, parseRepoFilter("riemann"), now)
	if !strings.Contains(out, "onethird_program") {
		t.Errorf("an unmatched filter must name the repos that ARE queued:\n%s", out)
	}
	// ...and a merge IS running here, so it must not raise a stall.
	if strings.Contains(out, "NOTHING IN FLIGHT") {
		t.Errorf("a merge is in flight; an unmatched filter must not report a stall:\n%s", out)
	}

	// The converse, and the reason the no-match path does not just return early:
	// --repo must not SUPPRESS a real alarm for a reader who happened to filter
	// to a repo with no rows. The state below is genuinely stalled.
	out = formatQueueFiltered(stalled, parseRepoFilter("riemann"), now)
	if !strings.Contains(out, "NOTHING IN FLIGHT anywhere") {
		t.Errorf("nothing is in flight anywhere; an unmatched filter must still say so:\n%s", out)
	}
	if !strings.Contains(out, "NOT specific to --repo=riemann") {
		t.Errorf("the alarm must say it is about the pipeline, not the filtered repo:\n%s", out)
	}
	if !strings.Contains(out, "2 merge requests pending across all repos") {
		t.Errorf("the alarm must count the whole pipeline, not the filtered subset:\n%s", out)
	}
}

// TestUnfilteredQueueIsUnchanged: formatQueue is the no-flag path and every
// existing reader of it must see exactly what it saw before.
func TestUnfilteredQueueIsUnchanged(t *testing.T) {
	now := time.Now()
	queue := []refinery.MergeRequest{
		{ID: "mr-1", RepoPath: "/r/pogo", Branch: "b-1", Status: refinery.StatusQueued, SubmitTime: now},
		{ID: "mr-2", RepoPath: "/r/onethird_program", Branch: "b-2", Status: refinery.StatusQueued, SubmitTime: now},
	}
	if got, want := formatQueue(queue, now), formatQueueFiltered(queue, repoFilter{}, now); got != want {
		t.Errorf("formatQueue diverged from the unfiltered path:\n%s\n---\n%s", got, want)
	}
	if strings.Contains(formatQueue(queue, now), "hidden") {
		t.Errorf("an unfiltered view must not mention hiding anything")
	}
}
