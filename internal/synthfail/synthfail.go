// Package synthfail reads a harness session transcript and reports whether an
// agent is consuming its nudges and accomplishing nothing — the class of
// failure mg-18d0 named after the 23h30m fleet outage of 2026-07-22.
//
// # What it detects
//
// A harness that cannot reach the model answers the turn LOCALLY, in ~10ms,
// and writes it to the session transcript: a turn attributed to a synthetic
// model, with zero tokens in and zero tokens out, flagged as an API error.
// That is the whole class. Auth expiry is only its most spectacular member —
// across this fleet's history mg-18d0 counted ~5500 such turns, of which
// rate-limit was 2818 and login-expired 914. The detection here is therefore
// STRUCTURAL (synthetic model + zero usage + error flag) and never keys on a
// message string; the strings are used afterwards, to name a reason for a
// human, and nothing depends on them.
//
// # Why one reader distinguishes wedged from dead
//
// The two failure modes are opposites at the file level:
//
//   - a genuinely WEDGED agent writes NOTHING to its transcript;
//   - a synthetically-failing agent writes a NEW TURN ON EVERY NUDGE.
//
// mg-18d0 measured 143 failed turns per agent against ~141 expected scheduler
// fires: one per fire, none missed, none queued. So silence and this class are
// not shades of one signal, and a detector that only ever saw the failing case
// could not tell them apart. See TestScan_SilentTranscriptIsNotThisClass.
//
// # Why it must never trigger a restart
//
// No member of this class is fixable by restarting: a new session inherits the
// same expired credential, the same rate limit, the same spend cap. Each one
// needs a human or the passage of time. mg-18d0 quantified the harm of getting
// this wrong — at T_restart=120min over 23.5h, ~11 rounds x 6 agents = ~66
// restarts against a dead credential, each discarding a live session's context
// (pm-pogo held 2339 messages) and destroying the very transcripts the
// diagnosis rests on. Hence [Report.SuppressRestart]: this detector's output is
// a PAGE and a SUPPRESSION, never a remediation.
//
// # Why absence is not health
//
// The transcript path and schema are harness internals, not a contract pogo
// owns; they can change without notice and other harnesses have no such file
// at all. Following mg-5a06, the paths are provider-declared and arrive here as
// data (see agent.Provider.SessionTranscriptGlob). When nothing is readable the
// report is [StateUnavailable] — explicitly NOT [StateQuiet]. Reading a missing
// file as "no failures here" is the same absence-as-evidence error that let
// this incident run for a day; StateUnavailable degrades to pogo's existing
// behaviour and says so.
package synthfail

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Defaults for [Options]. They are deliberately loose: this detector's job is
// to answer "is this agent burning nudges right now", and a 30-minute window
// spans at least two fires of the fleet's */10 mail-check cadence.
const (
	// DefaultWindow is how far back a turn counts as current evidence.
	DefaultWindow = 30 * time.Minute

	// DefaultMinTurns is how many failing turns inside the window are needed
	// before the class is reported. Two, not one: a single synthetic turn is
	// an ordinary transient (a dropped socket, an unparseable tool call) that
	// the next turn recovers from, and paging on it would make this detector
	// noise. Two inside one window means the agent consumed a fire, failed,
	// consumed the next, and failed again — the mg-18d0 signature.
	DefaultMinTurns = 2

	// maxLineBytes caps how long a transcript line may be before it is skipped
	// unread. A synthetic failure turn is ~1KB; a real assistant turn carrying
	// tool results can be megabytes. Skipping the long ones is both a large
	// speedup over a multi-thousand-message transcript and safe, because no
	// line this detector cares about approaches the cap.
	maxLineBytes = 256 * 1024

	// futureTolerance is how far ahead of now a turn may be timestamped and
	// still count. The window needs an UPPER bound as well as a lower one —
	// without it "the last 30 minutes" silently means "the last 30 minutes and
	// all of the future", and a scan of any historical window sweeps in
	// everything after it. The tolerance is not zero because the transcript's
	// clock and pogod's clock are not the same clock, and a few seconds of skew
	// must not drop a live failure.
	futureTolerance = 2 * time.Minute

	// syntheticModel is the model attribution a harness writes when it answered
	// a turn locally instead of calling the API. Used only as a cheap
	// pre-filter and then re-verified structurally after decoding.
	syntheticModel = "<synthetic>"
)

// State is the tri-state verdict. Its zero value is [StateUnavailable] — the
// "no claim" answer — so a caller that forgets to run the scan cannot
// accidentally read health out of an empty struct.
type State int

const (
	// StateUnavailable means no transcript could be read: the harness declares
	// no transcript path, the directory does not exist, or it was unreadable.
	// This is NOT a health claim in either direction. Callers must degrade to
	// whatever they did before this detector existed.
	StateUnavailable State = iota

	// StateQuiet means a transcript WAS read and contains no failing turns in
	// the window. An agent that is also silent is wedged in the ordinary sense
	// and the existing stall handling — including restart — applies unchanged.
	StateQuiet

	// StateFailing means the agent is answering turns locally and failing them:
	// it is alive, responsive, and accomplishing nothing. PAGE A HUMAN. DO NOT
	// RESTART.
	StateFailing
)

// String renders the state for logs, `pogo agent diagnose`, and events.
func (s State) String() string {
	switch s {
	case StateQuiet:
		return "quiet"
	case StateFailing:
		return "failing"
	default:
		return "unavailable"
	}
}

// Reason names which member of the class was seen. It is descriptive only:
// detection never depends on it, so a harness that renames its error codes
// degrades this to ReasonUnclassified rather than going blind.
type Reason string

const (
	ReasonUnclassified   Reason = "unclassified"
	ReasonAuthFailed     Reason = "auth_failed"
	ReasonRateLimit      Reason = "rate_limit"
	ReasonWeeklyLimit    Reason = "weekly_limit"
	ReasonSpendLimit     Reason = "spend_limit"
	ReasonServerError    Reason = "server_error"
	ReasonInvalidRequest Reason = "invalid_request"
)

// Human returns a one-line description suitable for a page.
func (r Reason) Human() string {
	switch r {
	case ReasonAuthFailed:
		return "the harness credential is expired or rejected — a human must re-authenticate"
	case ReasonRateLimit:
		return "the provider is rate-limiting requests — this clears with time"
	case ReasonWeeklyLimit:
		return "the account's weekly usage limit is exhausted — this clears at the stated reset"
	case ReasonSpendLimit:
		return "the account's spend limit is reached — a human must raise it"
	case ReasonServerError:
		return "the provider is returning server-side errors — this usually clears with time"
	case ReasonInvalidRequest:
		return "the harness is rejecting the request itself (e.g. context too long)"
	default:
		return "the harness is failing turns locally for an unrecognised reason"
	}
}

// Report is one agent's verdict.
type Report struct {
	// State is the tri-state verdict. Read this before anything else.
	State State `json:"state"`

	// Reason names the class member, when State is StateFailing. It is the
	// most frequent reason in the window, so a mixed window (a rate limit that
	// decays into an auth failure) reports the dominant one rather than
	// whichever happened to be last.
	Reason Reason `json:"reason,omitempty"`

	// Detail is the harness's own text for a representative failing turn,
	// truncated. Diagnostic colour for the page; nothing keys on it.
	Detail string `json:"detail,omitempty"`

	// Count is how many failing turns fell inside the window.
	Count int `json:"count,omitempty"`

	// First and Last bound those turns.
	First time.Time `json:"first,omitempty"`
	Last  time.Time `json:"last,omitempty"`

	// Reasons counts every reason seen in the window, so a page can show a
	// mixed picture instead of flattening it to the winner.
	Reasons map[Reason]int `json:"reasons,omitempty"`

	// Files is how many transcript files were opened and read.
	Files int `json:"files,omitempty"`

	// Unavailable explains why State is StateUnavailable. It is always set in
	// that state and always empty otherwise, so "we could not look" is never
	// silently rendered as "we looked and saw nothing".
	Unavailable string `json:"unavailable,omitempty"`
}

// SuppressRestart reports whether restart-based remediation must be withheld
// for this agent.
//
// True ONLY for StateFailing. Not for StateUnavailable — an unreadable
// transcript tells us nothing, and suppressing restarts on no evidence would
// disable the existing wedge recovery for every harness that has no transcript
// at all. Not for StateQuiet — that agent's silence is an ordinary wedge, which
// is exactly what restart is for.
func (r Report) SuppressRestart() bool { return r.State == StateFailing }

// Options tunes a scan. The zero value means the defaults.
type Options struct {
	// Now is the clock. Zero means time.Now.
	Now time.Time
	// Window is how far back turns count. Zero means DefaultWindow.
	Window time.Duration
	// MinTurns is the count needed to report the class. Zero means
	// DefaultMinTurns. Values below 1 are raised to 1.
	MinTurns int
}

func (o Options) resolve() (now time.Time, window time.Duration, minTurns int) {
	now = o.Now
	if now.IsZero() {
		now = time.Now()
	}
	window = o.Window
	if window <= 0 {
		window = DefaultWindow
	}
	minTurns = o.MinTurns
	if minTurns == 0 {
		minTurns = DefaultMinTurns
	}
	if minTurns < 1 {
		minTurns = 1
	}
	return now, window, minTurns
}

// turn is the narrow view of a transcript record this detector needs. Every
// field it reads is one a harness must write for its own rendering; nothing
// here is a pogo-specific extension.
type turn struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Error     string `json:"error"`
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

// isSyntheticFailure applies the structural test, which is the whole
// discriminator: the harness attributed the turn to a synthetic model, spent
// no tokens in either direction, and flagged it as an API error. A real turn
// fails all three; a real turn that merely errored still spent input tokens.
func (t *turn) isSyntheticFailure() bool {
	if t.Message.Model != syntheticModel {
		return false
	}
	u := t.Message.Usage
	if u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheCreationTokens != 0 || u.CacheReadTokens != 0 {
		return false
	}
	return t.IsAPIErr
}

func (t *turn) text() string {
	for _, c := range t.Message.Content {
		if c.Type == "text" && c.Text != "" {
			return c.Text
		}
	}
	return ""
}

// classify maps a failing turn to a Reason. The harness's machine-readable
// error code is primary; the message text only sub-divides a limit, because
// the harness reports weekly limits, spend limits and ordinary throttling under
// one code while a human needs to know which. Unrecognised codes land on
// ReasonUnclassified — the turn is still detected and still suppresses restart,
// it just cannot be named.
func classify(code, text string) Reason {
	lower := strings.ToLower(text)
	switch code {
	case "authentication_failed":
		return ReasonAuthFailed
	case "rate_limit":
		switch {
		case strings.Contains(lower, "weekly limit"):
			return ReasonWeeklyLimit
		case strings.Contains(lower, "spend limit"):
			return ReasonSpendLimit
		default:
			return ReasonRateLimit
		}
	case "server_error":
		return ReasonServerError
	case "invalid_request":
		return ReasonInvalidRequest
	default:
		return ReasonUnclassified
	}
}

// Locate expands home-relative globs into readable transcript files, newest
// last. Globs are joined UNDER home exactly as memcheck.Locate does (mg-5a06),
// so a provider cannot reach outside the user's home; an empty glob is skipped
// and a glob that matches nothing contributes nothing without erroring.
//
// modifiedSince, when non-zero, drops files whose mtime predates it. A file
// untouched since before the window cannot hold a turn inside it, so this is a
// pure speedup on a fleet with years of transcript history.
func Locate(home string, globs []string, modifiedSince time.Time) []string {
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
			if !modifiedSince.IsZero() && st.ModTime().Before(modifiedSince) {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// Scan reads the given home-relative transcript globs and returns the verdict.
//
// It is the whole public entry point. Callers pass provider-declared globs
// (see providers.SessionTranscriptGlobs) and never a literal path — this
// package names no harness.
func Scan(home string, globs []string, opts Options) Report {
	now, window, minTurns := opts.resolve()
	cutoff := now.Add(-window)

	if home == "" {
		return Report{Unavailable: "no home directory to resolve transcript paths against"}
	}
	if len(nonEmpty(globs)) == 0 {
		return Report{Unavailable: "this harness declares no session transcript path"}
	}

	// Locate deliberately does NOT filter by mtime here. A file whose last turn
	// is old still proves the transcript is readable, which is the difference
	// between StateQuiet and StateUnavailable — and that distinction is the
	// point of the whole package. The mtime filter is applied per-file below,
	// after the file has counted as evidence-of-readability.
	paths := Locate(home, globs, time.Time{})
	if len(paths) == 0 {
		return Report{Unavailable: "no session transcript found at the declared path"}
	}

	horizon := now.Add(futureTolerance)
	rep := Report{State: StateQuiet, Reasons: map[Reason]int{}}
	var readErr error
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			readErr = err
			continue
		}
		rep.Files++
		// A file untouched since before the window cannot contain a turn inside
		// it. It still counted as readable evidence above.
		if st.ModTime().Before(cutoff) {
			continue
		}
		if err := scanFile(p, cutoff, horizon, &rep); err != nil {
			readErr = err
		}
	}

	if rep.Files == 0 {
		msg := "session transcript could not be read"
		if readErr != nil {
			msg += ": " + readErr.Error()
		}
		return Report{Unavailable: msg}
	}

	if rep.Count < minTurns {
		// Below threshold is QUIET, not failing — but the turns we did see are
		// dropped from the report so a caller cannot mistake a stray transient
		// for the class.
		return Report{State: StateQuiet, Files: rep.Files}
	}

	rep.State = StateFailing
	rep.Reason = dominant(rep.Reasons)
	return rep
}

// scanFile appends the failing turns in one transcript file to rep.
func scanFile(path string, cutoff, horizon time.Time, rep *Report) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := readLine(r)
		if len(line) > 0 {
			consider(line, cutoff, horizon, rep)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// consider decodes one line if it could plausibly be a synthetic failure turn.
// The substring pre-filter is an optimisation only: everything it lets through
// is re-verified structurally by isSyntheticFailure, so a harness that renames
// the marker loses speed, not correctness — it would report StateQuiet, which
// is the same degraded-to-today's-behaviour answer as an absent file.
func consider(line []byte, cutoff, horizon time.Time, rep *Report) {
	if !containsSynthetic(line) {
		return
	}
	var t turn
	if err := json.Unmarshal(line, &t); err != nil {
		return
	}
	if !t.isSyntheticFailure() {
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, t.Timestamp)
	if err != nil {
		// A turn we cannot date cannot be placed inside the window. Counting it
		// would let ancient history page a live human.
		return
	}
	// The window is bounded at BOTH ends. A turn before the cutoff is history;
	// one after the horizon is either a clock-skew artefact beyond tolerance or
	// a scan of a historical window that would otherwise sweep in everything
	// that happened since.
	if ts.Before(cutoff) || ts.After(horizon) {
		return
	}

	text := t.text()
	rep.Count++
	rep.Reasons[classify(t.Error, text)]++
	if rep.First.IsZero() || ts.Before(rep.First) {
		rep.First = ts
	}
	if ts.After(rep.Last) {
		rep.Last = ts
		rep.Detail = truncate(text, 200)
	}
}

func containsSynthetic(line []byte) bool {
	return strings.Contains(string(line), syntheticModel)
}

// readLine returns the next line without its terminator, skipping any line
// longer than maxLineBytes. The returned error is io.EOF on the last line.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	oversized := false
	for {
		chunk, isPrefix, err := r.ReadLine()
		if !oversized {
			if len(buf)+len(chunk) > maxLineBytes {
				oversized = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err != nil {
			return buf, err
		}
		if !isPrefix {
			return buf, nil
		}
	}
}

// dominant returns the most frequent reason, breaking ties by name so the
// answer is stable across runs.
func dominant(counts map[Reason]int) Reason {
	best := ReasonUnclassified
	bestN := -1
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
