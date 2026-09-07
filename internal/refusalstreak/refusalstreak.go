// Package refusalstreak reads a harness session transcript and reports the
// TRAILING RUN of consecutive failing assistant turns — mg-6f3d, the successor
// mg-6616 was closed to produce.
//
// # The gap it closes
//
// An agent that cannot complete a turn is indistinguishable from an idle one by
// every instrument on the box except the transcript. `pogo agent list` says
// running with correct uptime; the process does not die, so restart_on_crash
// never fires; schedules keep firing and scheduler_fire_delivered keeps logging
// success. On 2026-07-22 it logged 647 of them while every consuming turn died
// instantly on an expired credential.
//
// What is not absent is the evidence. The failure text is in the transcript,
// once per failed turn, for the whole duration. Measured on this fleet's crew
// transcripts 2026-09-07 — 114 files, 76,541 assistant turns — 12,230 of those
// turns are failures. The longest unbroken run is 661 turns, mayor,
// 2026-08-14T08:25:23Z..2026-08-19T06:48:35Z: four days and twenty-two hours of
// one agent answering nothing, with nobody reading it.
//
// # Consecutive, not a rate, and why that is not a preference
//
// internal/synthfail counts failing turns in a trailing 30-minute WINDOW. That
// is the right shape for "is this agent burning its nudges right now" and the
// wrong shape for "has this agent stopped": a window's count is bounded by how
// busy the agent is, so the 661-turn outage above reads out of a 30m window as
// "2 errors in 30m", the same number a single bad afternoon produces.
//
// A run has no such ceiling. It is a position in the transcript, not a rate, so
// it says the same thing about a chatty agent and a quiet one.
//
// The threshold is measured, not chosen. Over the 92 corpus files that hold any
// failing turn at all, the longest run per file distributes as:
//
//	   1 turn   24 files      <- transients: a dropped socket, one bad call
//	   2 turns   8 files      <-
//	 3-9 turns   6 files
//	10-99        18 files
//	100+         36 files
//
// N=3 is where that distribution separates. It excludes all 32 files whose worst
// moment was one or two turns, and admits 60 files whose worst moment ran to
// hundreds. A single refusal is noise; three in a row with no work between them
// is a stopped agent.
//
// # There is no healthy default, and that is the whole design
//
// mg-6616's own classifier bucketed the failures it already knew and defaulted
// everything else to `work`. It scored 386 failures as healthy turns and turned
// one fully dead day (2026-08-21) into "41 work". The error points TOWARD
// health, which makes the surrounding days look like a solid baseline.
//
// So every assistant turn here lands in exactly one of four verdicts and none of
// them is reached by falling through. [VerdictWork] requires positive evidence —
// a tool call, or tokens actually spent on a reply that says nothing about
// failing. A turn that is neither established work nor an established failure is
// [VerdictUnclassified] and it does NOT break a run.
//
// # What it reports if the agent STOPPED
//
// The question this instrument is built to answer. A transcript that cannot be
// read, or that holds no assistant turns at all, is [StateUnavailable] — not
// health. A tail that is not positively work and has not reached the threshold
// is [StateUnwitnessed] — also not health. Only a most-recent turn that is
// established work returns [StateWorking].
package refusalstreak

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultMinStreak is the run length that alarms. See the package doc for the
// distribution it was read off.
const DefaultMinStreak = 3

// maxLineBytes caps how long a transcript line may be before it is skipped
// unread. A failing turn is ~1KB; a real assistant turn carrying tool results
// can be megabytes. Skipping the long ones is a large speedup and safe in one
// direction only — a skipped line cannot be a failure, so the count can only be
// low, never high.
//
// It is NOT safe in the other direction, and the run machinery accounts for
// that: a skipped line is still COUNTED as a turn that was not established as
// work, so an oversized line cannot silently break a run.
const maxLineBytes = 256 * 1024

// syntheticModel is the model attribution a harness writes when it answered a
// turn locally instead of calling the API.
const syntheticModel = "<synthetic>"

// State is the verdict. Its zero value is [StateUnavailable] — the no-claim
// answer — so a caller reading an empty struct cannot read health out of it.
type State int

const (
	// StateUnavailable means nothing could be judged: no declared transcript
	// path, no readable file, or a file with no assistant turns in it. NOT a
	// health claim. Callers degrade to whatever they did before this existed.
	StateUnavailable State = iota

	// StateWorking means the most recent assistant turn is established work —
	// it made a tool call, or it spent tokens on a reply that carries no failure
	// string. This is the ONLY healthy answer this package gives.
	StateWorking

	// StateUnwitnessed means turns were read, the tail is not established work,
	// and the failing run has not reached MinStreak. It is deliberately its own
	// state rather than being folded into StateWorking: it is the bucket that
	// mg-6616's classifier did not have, and giving it a name is what stops an
	// unrecognised failure from being counted as a healthy turn.
	StateUnwitnessed

	// StateStreaking means MinStreak or more consecutive failing assistant
	// turns, with no established work between them. ALARM.
	StateStreaking
)

func (s State) String() string {
	switch s {
	case StateWorking:
		return "working"
	case StateUnwitnessed:
		return "unwitnessed"
	case StateStreaking:
		return "streaking"
	default:
		return "unavailable"
	}
}

// Healthy reports whether this state is a positive claim of health. Only
// StateWorking is. Written as a method so no caller has to remember that three
// of the four states are not.
func (s State) Healthy() bool { return s == StateWorking }

// Verdict is one assistant turn's classification. There are four and none of
// them is a fall-through.
type Verdict int

const (
	// VerdictUnclassified is a turn with no positive evidence in either
	// direction. It neither builds a run nor breaks one.
	VerdictUnclassified Verdict = iota
	// VerdictWork is established work: a tool call, or tokens spent on a reply
	// with no failure string in it. This is the only verdict that breaks a run.
	VerdictWork
	// VerdictFailure is a structurally-synthetic turn: the harness answered it
	// locally, spending nothing, and flagged it an API error.
	VerdictFailure
	// VerdictAmbiguous is a real, token-spending turn whose text matches a
	// failure string — an agent writing ABOUT the outage. Measured at 21 turns
	// in 76,541. It is not a failure and it is not evidence of health, so like
	// VerdictUnclassified it neither builds nor breaks a run.
	VerdictAmbiguous
)

func (v Verdict) String() string {
	switch v {
	case VerdictWork:
		return "work"
	case VerdictFailure:
		return "failure"
	case VerdictAmbiguous:
		return "ambiguous"
	default:
		return "unclassified"
	}
}

// Report is one agent's reading.
type Report struct {
	// State is the verdict. Read this before anything else.
	State State `json:"state"`

	// Streak is the number of consecutive FAILING assistant turns at the tail
	// of the live session, uninterrupted by established work.
	Streak int `json:"streak,omitempty"`

	// Reason names the dominant failure mode in the run.
	Reason Reason `json:"reason,omitempty"`

	// Reasons counts every mode in the run, so a mixed run (a timeout decaying
	// into an entitlement refusal) is not flattened to its winner.
	Reasons map[Reason]int `json:"reasons,omitempty"`

	// Ambiguous and Unclassified count the turns in the run that were neither
	// established work nor established failures. They are reported rather than
	// dropped: a run of 3 failures with 40 unclassified turns threaded through
	// it is a different fact from a clean run of 3, and a reader who cannot see
	// the difference is reading a number with a hidden denominator.
	Ambiguous    int `json:"ambiguous,omitempty"`
	Unclassified int `json:"unclassified,omitempty"`

	// Detail is the harness's own text for the most recent failing turn,
	// truncated. It is the only field that can name a mode the marker table has
	// no entry for, so it is always carried when there is a run.
	Detail string `json:"detail,omitempty"`

	// First and Last bound the failing turns in the run. Last is what a caller
	// tests for staleness: a run whose most recent failure is hours old is
	// history, not an outage.
	First time.Time `json:"first,omitempty"`
	Last  time.Time `json:"last,omitempty"`

	// Turns is how many assistant turns were classified, and Files how many
	// transcript files were opened. Turns==0 with Files>0 is the readable-but-
	// says-nothing case, and it reports StateUnavailable.
	Turns int `json:"turns,omitempty"`
	Files int `json:"files,omitempty"`

	// Session is the transcript file the run was read from. The run is computed
	// PER FILE (see Scan) and this names which one won.
	Session string `json:"session,omitempty"`

	// ScannedAt is the clock this scan ran against, and MinStreak the threshold
	// it applied. Both are stamped on every report including the unavailable
	// one, because "we looked at 11:41 and the run was 3" and "we could not look
	// at 11:41" are both claims about a moment; a reader who cannot date one
	// will date it to now.
	ScannedAt time.Time `json:"scanned_at,omitempty"`
	MinStreak int       `json:"min_streak,omitempty"`

	// Unavailable explains why State is StateUnavailable. Always set in that
	// state and always empty otherwise, so "we could not look" is never
	// rendered as "we looked and saw nothing".
	Unavailable string `json:"unavailable,omitempty"`

	// lastTurn is the timestamp of the last assistant turn of ANY verdict in
	// this file — the tie-break that picks the live session out of a directory
	// of them. It is not Last, which bounds the failing run only: a session
	// whose tail is healthy has a lastTurn and no Last at all.
	lastTurn time.Time
	// tailIsWork records whether the most recent classified turn was
	// established work. It is the only positive health signal in this struct,
	// and it is deliberately not derivable from Streak==0 — a tail of three
	// unclassified turns also has Streak==0 and is not health.
	tailIsWork bool
}

// Alarming reports whether this reading is the condition mg-6f3d alarms on.
func (r Report) Alarming() bool { return r.State == StateStreaking }

// Brief renders the run in ABSOLUTE terms only — nothing in it decays, so it is
// the string that may be mailed, logged, or written to a ledger:
//
//	3 consecutive failing turns (login), 2026-09-07T11:30:10Z–11:41:11Z
//
// Empty for any state but StateStreaking.
func (r Report) Brief() string {
	if r.State != StateStreaking || r.Streak == 0 {
		return ""
	}
	out := fmt.Sprintf("%d consecutive failing turns", r.Streak)
	if r.Reason != "" {
		out += " (" + string(r.Reason) + ")"
	}
	if span := spanString(r.First, r.Last); span != "" {
		out += ", " + span
	}
	if r.Ambiguous+r.Unclassified > 0 {
		out += fmt.Sprintf(" [+%d turn(s) neither work nor failure]", r.Ambiguous+r.Unclassified)
	}
	return out
}

// spanString bounds the run. Both ends UTC and explicit; the second drops its
// date only when it shares the first's, so a run crossing midnight — the
// founding 661-turn one spanned five days — never renders as a short one.
func spanString(first, last time.Time) string {
	if first.IsZero() || last.IsZero() {
		return ""
	}
	f, l := first.UTC(), last.UTC()
	if f.Year() == l.Year() && f.YearDay() == l.YearDay() {
		return f.Format(time.RFC3339) + "–" + l.Format("15:04:05Z")
	}
	return f.Format(time.RFC3339) + "–" + l.Format(time.RFC3339)
}

// Options tunes a scan. The zero value means the defaults.
type Options struct {
	// Now is the clock. Zero means time.Now.
	Now time.Time
	// MinStreak is the run length that reports StateStreaking. Zero means
	// DefaultMinStreak; values below 1 are raised to 1.
	MinStreak int
	// Markers overrides the failure-string table. Nil means DefaultMarkers.
	Markers []Marker
}

func (o Options) resolve() (time.Time, int) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	min := o.MinStreak
	if min == 0 {
		min = DefaultMinStreak
	}
	if min < 1 {
		min = 1
	}
	return now, min
}

// turn is the narrow view of a transcript record this detector needs. Every
// field is one a harness must write for its own rendering; nothing here is a
// pogo-specific extension.
type turn struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	IsAPIErr  bool   `json:"isApiErrorMessage"`
	Message   struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// spentTokens reports whether the turn cost anything in either direction. A
// turn that spent nothing did not reach the model.
func (t *turn) spentTokens() bool {
	u := t.Message.Usage
	return u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheCreationTokens != 0 || u.CacheReadTokens != 0
}

// synthetic applies the structural test that is the actual failure predicate:
// the harness attributed the turn to a synthetic model, spent no tokens either
// way, and flagged it an API error. All three, because each alone has an
// innocent reading.
func (t *turn) synthetic() bool {
	return t.Message.Model == syntheticModel && !t.spentTokens() && t.IsAPIErr
}

func (t *turn) hasToolUse() bool {
	for _, c := range t.Message.Content {
		if c.Type == "tool_use" {
			return true
		}
	}
	return false
}

func (t *turn) text() string {
	var parts []string
	for _, c := range t.Message.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, " ")
}

// classify places one assistant turn in exactly one verdict. Read the order:
// nothing here falls through to VerdictWork.
func classify(t *turn, markers []Marker) (Verdict, Reason) {
	if t.synthetic() {
		reason, _ := match(t.text(), markers)
		// A synthetic failure that matches no marker keeps ReasonUnrecognised.
		// It is still a failure. This branch is the one mg-6616 did not have.
		return VerdictFailure, reason
	}
	if reason, hit := match(t.text(), markers); hit {
		// THE SCHEMA-DRIFT BRANCH. The structural test above names two harness
		// internals — the "<synthetic>" attribution and isApiErrorMessage — and
		// pogo owns neither. Rename either one and every failing turn falls
		// through to here, which is how a detector goes silent without going
		// wrong. So a turn that carries a failure string AND SPENT NOTHING is
		// still a failure: no real model turn costs zero tokens in both
		// directions, and the 21 corpus turns that merely WRITE about an outage
		// all spent thousands.
		if !t.spentTokens() {
			return VerdictFailure, reason
		}
		// A real turn that says a failure thing. 21 of these in 76,541 — agents
		// writing about the outage. Not a failure, and not evidence of health.
		return VerdictAmbiguous, ReasonUnrecognised
	}
	if t.hasToolUse() || t.spentTokens() {
		return VerdictWork, ReasonUnrecognised
	}
	return VerdictUnclassified, ReasonUnrecognised
}

// Scan reads the given home-relative transcript globs and returns the reading.
//
// The run is computed PER FILE and the file holding the most recent assistant
// turn wins. Concatenating a directory's sessions would join a dead session's
// failing tail to a live session's healthy head — the two are different
// processes, and a run that spans them is not a run.
func Scan(home string, globs []string, opts Options) Report {
	now, minStreak := opts.resolve()
	stamp := func(r Report) Report {
		r.ScannedAt = now.UTC()
		r.MinStreak = minStreak
		return r
	}

	if home == "" {
		return stamp(Report{Unavailable: "no home directory to resolve transcript paths against"})
	}
	paths := Locate(home, globs)
	if len(paths) == 0 {
		if len(nonEmpty(globs)) == 0 {
			return stamp(Report{Unavailable: "this harness declares no session transcript path"})
		}
		return stamp(Report{Unavailable: "no session transcript found at the declared path"})
	}

	best := Report{}
	files := 0
	turns := 0
	var readErr error
	for _, p := range paths {
		rep, err := scanFile(p, opts.Markers)
		if err != nil {
			readErr = err
			continue
		}
		files++
		turns += rep.Turns
		if rep.Turns == 0 {
			continue
		}
		// "Most recent turn" is the tie-break, not "longest run": the live
		// session is the one that is still being written, and a longer run in an
		// abandoned session is history.
		if best.Session == "" || rep.lastTurn.After(best.lastTurn) {
			rep.Session = p
			best = rep
		}
	}

	if files == 0 {
		msg := "session transcript could not be read"
		if readErr != nil {
			msg += ": " + readErr.Error()
		}
		return stamp(Report{Unavailable: msg})
	}
	if best.Session == "" {
		// Files opened, nothing to judge. This is NOT health: a transcript with
		// no assistant turns is a session that has not answered anything.
		return stamp(Report{
			Files:       files,
			Unavailable: "the session transcript holds no assistant turns to classify",
		})
	}

	best.Files = files
	best.Turns = turns
	switch {
	case best.Streak >= minStreak:
		best.State = StateStreaking
		best.Reason = dominant(best.Reasons)
	case best.tailIsWork:
		best.State = StateWorking
		// A working tail carries no run, so the run fields are dropped rather
		// than left holding a sub-threshold count a caller might render.
		best.Streak, best.Reasons, best.Detail = 0, nil, ""
		best.First, best.Last = time.Time{}, time.Time{}
		best.Ambiguous, best.Unclassified = 0, 0
	default:
		best.State = StateUnwitnessed
		best.Reason = dominant(best.Reasons)
	}
	return stamp(best)
}

// scanFile computes the trailing run for one transcript file.
func scanFile(path string, markers []Marker) (Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	defer f.Close()

	rep := Report{Reasons: map[Reason]int{}}
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, oversized, err := readLine(r)
		if len(line) > 0 || oversized {
			consider(line, oversized, markers, &rep)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return rep, nil
			}
			return rep, err
		}
	}
}

// consider folds one transcript line into the running state.
//
// An OVERSIZED line — one skipped for length — is treated as an assistant turn
// that could not be classified rather than as nothing. Treating it as nothing
// would let a megabyte tool-result turn silently break a run, which is a
// suppression, and this detector's one rule is that it does not suppress.
func consider(line []byte, oversized bool, markers []Marker, rep *Report) {
	if oversized {
		rep.Turns++
		rep.Unclassified++
		rep.tailIsWork = false
		return
	}
	if !isAssistantLine(line) {
		return
	}
	var t turn
	if err := json.Unmarshal(line, &t); err != nil {
		// An assistant line we cannot decode is an assistant turn we cannot
		// classify. Same rule as oversized: not nothing, not work.
		rep.Turns++
		rep.Unclassified++
		rep.tailIsWork = false
		return
	}
	if t.Type != "assistant" {
		return
	}
	rep.Turns++
	ts, tsErr := time.Parse(time.RFC3339Nano, t.Timestamp)
	if tsErr == nil {
		rep.lastTurn = ts
	}

	v, reason := classify(&t, markers)
	switch v {
	case VerdictWork:
		// The ONLY thing that breaks a run.
		rep.Streak, rep.Ambiguous, rep.Unclassified = 0, 0, 0
		rep.Reasons = map[Reason]int{}
		rep.First, rep.Last, rep.Detail = time.Time{}, time.Time{}, ""
		rep.tailIsWork = true
	case VerdictFailure:
		rep.Streak++
		rep.Reasons[reason]++
		rep.tailIsWork = false
		if tsErr == nil {
			if rep.First.IsZero() {
				rep.First = ts
			}
			rep.Last = ts
		}
		rep.Detail = truncate(t.text(), 200)
	case VerdictAmbiguous:
		rep.Ambiguous++
		rep.tailIsWork = false
	default:
		rep.Unclassified++
		rep.tailIsWork = false
	}
}

// isAssistantLine is a cheap pre-filter. Everything it lets through is
// re-verified after decoding, so it can only cost speed.
func isAssistantLine(line []byte) bool {
	return strings.Contains(string(line), `"assistant"`)
}

// readLine returns the next line without its terminator. Lines longer than
// maxLineBytes come back empty with oversized set, so the caller can count them
// rather than lose them.
func readLine(r *bufio.Reader) (buf []byte, oversized bool, err error) {
	for {
		chunk, isPrefix, rerr := r.ReadLine()
		if !oversized {
			if len(buf)+len(chunk) > maxLineBytes {
				oversized = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if rerr != nil {
			return buf, oversized, rerr
		}
		if !isPrefix {
			return buf, oversized, nil
		}
	}
}

// dominant returns the most frequent reason, breaking ties by name so the
// answer is stable across runs.
func dominant(counts map[Reason]int) Reason {
	best := ReasonUnrecognised
	bestN := 0
	names := make([]string, 0, len(counts))
	for r := range counts {
		names = append(names, string(r))
	}
	sort.Strings(names)
	for _, n := range names {
		if c := counts[Reason(n)]; c > bestN {
			best, bestN = Reason(n), c
		}
	}
	return best
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Locate expands home-relative globs into readable transcript files, sorted.
//
// Globs are joined UNDER home exactly as synthfail.Locate does, so a provider
// cannot reach outside the user's home; an empty glob is skipped and a glob that
// matches nothing contributes nothing without erroring.
//
// There is deliberately NO mtime filter. synthfail has one because its window is
// a trailing 30 minutes and a file untouched since before it cannot hold a turn
// inside it. A run has no window: the 661-turn run of 2026-08-14 ended five days
// after it began, and a file filtered out for being stale is the exact file that
// proves an agent stopped.
func Locate(home string, globs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, g := range globs {
		if g == "" {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(home, filepath.FromSlash(g)))
		if err != nil {
			// A malformed pattern from one provider must not stop the others.
			continue
		}
		for _, m := range matches {
			if seen[m] {
				continue
			}
			st, err := os.Stat(m)
			if err != nil || st.IsDir() {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}
