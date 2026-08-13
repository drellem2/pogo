package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/refinery"
)

// The refinery serves SEVERAL repositories from one queue, and until mg-ff3a
// `pogo refinery history` printed branch, author, status and time but not which
// repo a row belonged to. The field was in --json (`repo_path`) the whole time;
// only the human views omitted it. In one evening that omission produced three
// confident wrong conclusions from three different agents, none of them
// careless:
//
//   - four queued merge requests were cancelled on the belief that all were
//     pogo branches (the repo had been inferred from the WORK ITEM, which can
//     legitimately produce branches in three repos);
//   - "6 MRs report merged but main has not moved" was escalated as possible
//     lost merges — every one of them had landed in another repo's main;
//   - "refinery STALLED, nothing merged" was escalated while the refinery was
//     merging steadily in a repo the reader was not watching.
//
// The workaround — `pogo refinery show <id>` prints a Repo: line — costs one
// command per row and, worse, requires already suspecting the problem. All
// three readers above did not suspect it, and they are the population the fix
// has to serve. It also interacts badly with the "merged is not live" reflex
// the fleet is deliberately trained on: checking that a reported merge actually
// landed is the RIGHT habit, and it is the habit that manufactures the false
// alarm when the view hides which main to check.

// repoColumn renders a merge request's repo for a human row.
//
// It is the refinery's own lane key (the basename), so what a reader sees in
// this column is the thing merges actually contend for rather than a second,
// possibly-drifting notion of "which repo". The full path stays available in
// --json and in `pogo refinery show`.
//
// A merge request carrying no repo path renders as "?" and not as ".".
// filepath.Base("") is ".", which reads as a real relative path — a confident
// wrong answer of exactly the kind this column exists to stop.
func repoColumn(repoPath string) string {
	lane := refinery.RepoLane(repoPath)
	if strings.TrimSpace(repoPath) == "" || lane == "." || lane == string(filepath.Separator) {
		return "?"
	}
	return lane
}

// repoFilter is the parsed form of `--repo`. The zero value matches everything,
// so callers that were not given a filter need no special case.
type repoFilter struct {
	// raw is what the operator typed, kept for the messages: a note that says
	// only the resolved lane cannot be checked against the invocation.
	raw string
	// lane is the resolved lane name the rows are compared against.
	lane string
}

// parseRepoFilter resolves a --repo value to a lane.
//
// It accepts a bare repo name ("pogo"), a full path — so `repo_path` copied out
// of --json, or the path passed to `refinery submit --repo=`, works unchanged —
// and "." for the checkout you are standing in.
//
// "." is worth a warning it cannot issue here: run from an agent worktree it
// resolves to the WORKTREE's basename, which is not the source repo's lane and
// will match nothing. That is why noMatchNote below names the lane it derived
// and lists the lanes actually present, instead of printing an empty list.
func parseRepoFilter(v string) repoFilter {
	v = strings.TrimSpace(v)
	if v == "" {
		return repoFilter{}
	}
	f := repoFilter{raw: v}
	if v == "." || v == ".." || strings.ContainsRune(v, filepath.Separator) {
		if abs, err := filepath.Abs(v); err == nil {
			f.lane = refinery.RepoLane(abs)
			return f
		}
	}
	f.lane = refinery.RepoLane(v)
	return f
}

// active reports whether this filter narrows anything.
func (f repoFilter) active() bool { return f.lane != "" }

// matches reports whether a merge request belongs to the filtered repo.
func (f repoFilter) matches(mr refinery.MergeRequest) bool {
	if !f.active() {
		return true
	}
	return refinery.RepoLane(mr.RepoPath) == f.lane
}

// apply splits rows into the kept ones and the number dropped.
func (f repoFilter) apply(rows []refinery.MergeRequest) (kept []refinery.MergeRequest, dropped int) {
	if !f.active() {
		return rows, 0
	}
	kept = make([]refinery.MergeRequest, 0, len(rows))
	for _, mr := range rows {
		if f.matches(mr) {
			kept = append(kept, mr)
		} else {
			dropped++
		}
	}
	return kept, dropped
}

// repoLanes lists the distinct lanes present in rows, sorted, for the notes.
func repoLanes(rows []refinery.MergeRequest) []string {
	seen := map[string]bool{}
	for _, mr := range rows {
		seen[repoColumn(mr.RepoPath)] = true
	}
	lanes := make([]string, 0, len(seen))
	for l := range seen {
		lanes = append(lanes, l)
	}
	sort.Strings(lanes)
	return lanes
}

// otherLanes lists the lanes in rows that this filter EXCLUDES — the repos a
// filtered reader can no longer see. Never includes the filtered repo itself:
// "merges in pogo, onethird_program consumed the shared cap" told a --repo=pogo
// reader that their own repo was competing with itself.
func (f repoFilter) otherLanes(rows []refinery.MergeRequest) []string {
	var other []refinery.MergeRequest
	for _, mr := range rows {
		if !f.matches(mr) {
			other = append(other, mr)
		}
	}
	return repoLanes(other)
}

// hiddenNote states what a --repo filter removed from a view.
//
// It is printed whenever the filter is active and something was dropped, and it
// names the repos the dropped rows are in. A narrowed view that does not say it
// is narrowed is the same instrument defect one level up: the reader who filters
// to their own repo and then reasons about "the queue" is reasoning about a
// subset they can no longer see the edge of.
func hiddenNote(f repoFilter, dropped int, all []refinery.MergeRequest) string {
	if !f.active() || dropped == 0 {
		return ""
	}
	return fmt.Sprintf("(--repo=%s: %s hidden in %s — this is a filtered VIEW, not the whole pipeline)\n",
		f.raw, plural(dropped, "row"), strings.Join(f.otherLanes(all), ", "))
}

// noMatchNote explains an empty filtered result.
//
// This is the line that keeps the filter from becoming a fourth instance of the
// bug it fixes. "No merge requests." printed under a filter that matched nothing
// is byte-identical to an empty pipeline, and a mistyped repo name — or `.` run
// from a worktree whose basename is not the repo's — would read as "the
// refinery has nothing", which is the exact false conclusion the repo column
// exists to prevent. So the lane that was actually compared is named, and the
// lanes that ARE present are listed beside it.
func noMatchNote(f repoFilter, all []refinery.MergeRequest, subject string) string {
	derived := ""
	if f.raw != f.lane {
		derived = fmt.Sprintf(" (read as repo %q)", f.lane)
	}
	if len(all) == 0 {
		return fmt.Sprintf("No %s, in any repo — the --repo=%s filter is not why this is empty.\n", subject, f.raw)
	}
	return fmt.Sprintf("No %s for --repo=%s%s. %s in %s.\n",
		subject, f.raw, derived, plural(len(all), "row"), strings.Join(repoLanes(all), ", "))
}
