package refusalstreak

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixtures below are shaped from the real corpus, not invented: the strings
// are verbatim from ~/.claude/projects/-Users-daniel--pogo-agents-*, and the
// structural fields (model "<synthetic>", zero usage, isApiErrorMessage) are the
// shape all 12,230 failing turns in that corpus carry.

const base = "2026-09-07T11:00:00Z"

func at(t *testing.T, offset time.Duration) string {
	t.Helper()
	b, err := time.Parse(time.RFC3339, base)
	if err != nil {
		t.Fatal(err)
	}
	return b.Add(offset).Format(time.RFC3339Nano)
}

// failTurn is a structurally-synthetic failure turn: the harness answered it
// locally, spent nothing, and flagged it an API error.
func failTurn(ts, text string) string {
	return line(map[string]any{
		"type": "assistant", "timestamp": ts, "isApiErrorMessage": true,
		"message": map[string]any{
			"model":   "<synthetic>",
			"content": []any{map[string]any{"type": "text", "text": text}},
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0,
				"cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
		},
	})
}

// workTurn is a real turn that spent tokens and made a tool call.
func workTurn(ts, text string) string {
	return line(map[string]any{
		"type": "assistant", "timestamp": ts,
		"message": map[string]any{
			"model": "claude-opus-5",
			"content": []any{
				map[string]any{"type": "text", "text": text},
				map[string]any{"type": "tool_use", "name": "Bash"},
			},
			"usage": map[string]any{"input_tokens": 12, "output_tokens": 34},
		},
	})
}

// talkTurn is a real, token-spending reply with no tool call.
func talkTurn(ts, text string) string {
	return line(map[string]any{
		"type": "assistant", "timestamp": ts,
		"message": map[string]any{
			"model":   "claude-opus-5",
			"content": []any{map[string]any{"type": "text", "text": text}},
			"usage":   map[string]any{"input_tokens": 900, "output_tokens": 40},
		},
	})
}

func line(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// writeSession lays a transcript out the way the harness does: under
// <home>/.claude/projects/<slug>/<session>.jsonl.
func writeSession(t *testing.T, home, session string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

var fixtureGlobs = []string{filepath.Join(".claude", "projects", "-fixture", "*.jsonl")}

func scan(t *testing.T, home string, opts Options) Report {
	t.Helper()
	if opts.Now.IsZero() {
		b, err := time.Parse(time.RFC3339, base)
		if err != nil {
			t.Fatal(err)
		}
		opts.Now = b.Add(time.Hour)
	}
	return Scan(home, fixtureGlobs, opts)
}

// ---------------------------------------------------------------- the class

func TestThreeConsecutiveFailingTurnsAlarm(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "s1",
		workTurn(at(t, 0), "getting on with it"),
		failTurn(at(t, 2*time.Minute), "Your organization has disabled Claude subscription access for Claude Code · Use an Anthropic API key instead, or ask your admin to enable access"),
		failTurn(at(t, 12*time.Minute), "Your organization has disabled Claude subscription access for Claude Code"),
		failTurn(at(t, 22*time.Minute), "Your organization has disabled Claude subscription access for Claude Code"),
	)
	rep := scan(t, home, Options{})
	if rep.State != StateStreaking {
		t.Fatalf("State = %s, want %s (unavailable = %q)", rep.State, StateStreaking, rep.Unavailable)
	}
	if rep.Streak != 3 {
		t.Errorf("Streak = %d, want 3", rep.Streak)
	}
	if rep.Reason != ReasonEntitlement {
		t.Errorf("Reason = %s, want %s", rep.Reason, ReasonEntitlement)
	}
	if !rep.Alarming() {
		t.Error("Alarming() = false on a run of 3")
	}
	// The brief is what travels. Both ends must be in it, absolutely.
	brief := rep.Brief()
	for _, want := range []string{"3 consecutive failing turns", "entitlement", "2026-09-07T11:02:00Z"} {
		if !strings.Contains(brief, want) {
			t.Errorf("Brief() = %q, want it to contain %q", brief, want)
		}
	}
}

// The threshold is a floor and two is below it. 8 of the 92 corpus files with
// any failing turn peaked at exactly two; none of them was an outage.
func TestTwoFailingTurnsIsNotAnAlarmAndIsNotHealth(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "s1",
		workTurn(at(t, 0), "working"),
		failTurn(at(t, time.Minute), "Request timed out"),
		failTurn(at(t, 11*time.Minute), "Request timed out"),
	)
	rep := scan(t, home, Options{})
	if rep.State != StateUnwitnessed {
		t.Fatalf("State = %s, want %s", rep.State, StateUnwitnessed)
	}
	if rep.State.Healthy() {
		t.Error("a tail of two failing turns reported Healthy() — this is the bucket mg-6616's classifier did not have")
	}
	if rep.Streak != 2 {
		t.Errorf("Streak = %d, want 2", rep.Streak)
	}
	if rep.Brief() != "" {
		t.Errorf("Brief() = %q on a sub-threshold run; the alarm string must be empty when there is no alarm", rep.Brief())
	}
}

// A run is BROKEN by established work, and only by that. This is the property
// that makes the count a run rather than a rate.
func TestEstablishedWorkBreaksTheRun(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "s1",
		failTurn(at(t, 0), "Request timed out"),
		failTurn(at(t, 10*time.Minute), "Request timed out"),
		workTurn(at(t, 20*time.Minute), "back"),
		failTurn(at(t, 30*time.Minute), "Request timed out"),
	)
	rep := scan(t, home, Options{})
	if rep.Streak != 1 {
		t.Fatalf("Streak = %d, want 1 — the two failures before the work turn are a closed run", rep.Streak)
	}
	if rep.State != StateUnwitnessed {
		t.Errorf("State = %s, want %s", rep.State, StateUnwitnessed)
	}
}

func TestAWorkingTailIsTheOnlyHealthyAnswer(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "s1",
		failTurn(at(t, 0), "Request timed out"),
		failTurn(at(t, 10*time.Minute), "Request timed out"),
		failTurn(at(t, 20*time.Minute), "Request timed out"),
		workTurn(at(t, 30*time.Minute), "recovered"),
	)
	rep := scan(t, home, Options{})
	if rep.State != StateWorking {
		t.Fatalf("State = %s, want %s", rep.State, StateWorking)
	}
	if !rep.State.Healthy() {
		t.Error("StateWorking is not Healthy()")
	}
	// A healthy report must not carry a sub-threshold run a caller could render
	// as a live count.
	if rep.Streak != 0 || !rep.First.IsZero() || rep.Detail != "" {
		t.Errorf("a working tail carried run fields: streak=%d first=%v detail=%q", rep.Streak, rep.First, rep.Detail)
	}
}

// ------------------------------------------------- no default healthy bucket

// mg-6616's classifier had an implicit `work` default and scored 386 failures as
// healthy turns. This is that defect, constructed: a failure mode with no entry
// in the marker table.
func TestAnUnrecognisedFailureIsAFAILURENotHealth(t *testing.T) {
	home := t.TempDir()
	const novel = "Quota exceeded for the flux capacitor tier · contact your reseller"
	writeSession(t, home, "s1",
		workTurn(at(t, 0), "working"),
		failTurn(at(t, time.Minute), novel),
		failTurn(at(t, 11*time.Minute), novel),
		failTurn(at(t, 21*time.Minute), novel),
	)
	rep := scan(t, home, Options{})
	if rep.State != StateStreaking {
		t.Fatalf("State = %s, want %s — a failure mode nobody has enumerated is still a failure", rep.State, StateStreaking)
	}
	if rep.Reason != ReasonUnrecognised {
		t.Errorf("Reason = %s, want %s", rep.Reason, ReasonUnrecognised)
	}
	// The detail is the only field that can name a mode the table has no entry
	// for, so it must survive to the alarm.
	if !strings.Contains(rep.Detail, "flux capacitor") {
		t.Errorf("Detail = %q, want the harness's own words for the unnamed mode", rep.Detail)
	}
}

// The string is the NAME, not the predicate. 21 turns in the 76,541-turn corpus
// match a failure string and are real model turns — agents writing about the
// outage. A string-only detector calls those failures.
func TestARealTurnWritingABOUTTheFailureIsNotAFailure(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "s1",
		failTurn(at(t, 0), "Please run /login · API Error: 403"),
		failTurn(at(t, 10*time.Minute), "Please run /login · API Error: 403"),
		talkTurn(at(t, 20*time.Minute),
			"The transcripts show `Please run /login` on every turn, so the credential is the cause."),
	)
	rep := scan(t, home, Options{})
	if rep.State == StateStreaking {
		t.Fatalf("State = %s: a real, token-spending turn that MENTIONS a failure string was counted as one", rep.State)
	}
	if rep.Ambiguous != 1 {
		t.Errorf("Ambiguous = %d, want 1", rep.Ambiguous)
	}
	// And it is not health either: it did not break the run.
	if rep.Streak != 2 {
		t.Errorf("Streak = %d, want 2 — an ambiguous turn must neither build a run nor break one", rep.Streak)
	}
	if rep.State.Healthy() {
		t.Error("a tail of ambiguous turns reported healthy")
	}
}

// The interleaved count travels, because 3 failures threaded through 40
// unclassified turns is a different fact from a clean run of 3.
func TestInterleavedNonWorkTurnsAreReportedNotDropped(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "s1",
		workTurn(at(t, 0), "working"),
		failTurn(at(t, time.Minute), "Request timed out"),
		line(map[string]any{"type": "assistant", "timestamp": at(t, 2*time.Minute),
			"message": map[string]any{"model": "claude-opus-5", "content": []any{}}}),
		failTurn(at(t, 3*time.Minute), "Request timed out"),
		failTurn(at(t, 4*time.Minute), "Request timed out"),
	)
	rep := scan(t, home, Options{})
	if rep.State != StateStreaking || rep.Streak != 3 {
		t.Fatalf("State = %s streak = %d, want %s / 3", rep.State, rep.Streak, StateStreaking)
	}
	if rep.Unclassified != 1 {
		t.Fatalf("Unclassified = %d, want 1", rep.Unclassified)
	}
	if !strings.Contains(rep.Brief(), "neither work nor failure") {
		t.Errorf("Brief() = %q, want the interleaved count stated — a number with a hidden denominator is not a reading", rep.Brief())
	}
}

// ------------------------------------------------------ absence is not health

func TestNoTranscriptIsUnavailableNotHealthy(t *testing.T) {
	home := t.TempDir()
	rep := scan(t, home, Options{})
	if rep.State != StateUnavailable {
		t.Fatalf("State = %s, want %s", rep.State, StateUnavailable)
	}
	if rep.State.Healthy() {
		t.Error("an absent transcript reported healthy — the absence-as-evidence error this whole class turns on")
	}
	if rep.Unavailable == "" {
		t.Error("StateUnavailable with no explanation; 'we could not look' must never render as 'we looked and saw nothing'")
	}
}

func TestNoDeclaredTranscriptPathSaysSo(t *testing.T) {
	rep := Scan(t.TempDir(), nil, Options{Now: time.Now()})
	if rep.State != StateUnavailable {
		t.Fatalf("State = %s, want %s", rep.State, StateUnavailable)
	}
	if !strings.Contains(rep.Unavailable, "declares no session transcript path") {
		t.Errorf("Unavailable = %q, want it to name the missing declaration", rep.Unavailable)
	}
}

// A file that exists and holds no assistant turns is a session that has answered
// nothing. That is not health.
func TestATranscriptWithNoAssistantTurnsIsUnavailable(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "s1",
		line(map[string]any{"type": "user", "timestamp": at(t, 0)}),
		line(map[string]any{"type": "summary"}),
	)
	rep := scan(t, home, Options{})
	if rep.State != StateUnavailable {
		t.Fatalf("State = %s, want %s", rep.State, StateUnavailable)
	}
	if rep.Files != 1 {
		t.Errorf("Files = %d, want 1 — the file WAS readable, which is a different fact from there being none", rep.Files)
	}
}

// The zero Report must not read as health. A caller that forgets to scan gets
// "no claim", not "fine".
func TestTheZeroReportIsNotAHealthClaim(t *testing.T) {
	var rep Report
	if rep.State != StateUnavailable {
		t.Fatalf("zero Report.State = %s, want %s", rep.State, StateUnavailable)
	}
	if rep.State.Healthy() || rep.Alarming() {
		t.Error("the zero Report claims something")
	}
}

// ------------------------------------------------------------- per session

// Concatenating a directory's sessions would join a dead session's failing tail
// to a live session's healthy head. They are different processes.
func TestRunsAreComputedPerSessionNotAcrossTheDirectory(t *testing.T) {
	home := t.TempDir()
	// The OLD session ends in a long run.
	writeSession(t, home, "aaa-old",
		failTurn(at(t, -3*time.Hour), "Request timed out"),
		failTurn(at(t, -2*time.Hour), "Request timed out"),
		failTurn(at(t, -1*time.Hour), "Request timed out"),
	)
	// The LIVE session — later turns — is working.
	writeSession(t, home, "zzz-live",
		workTurn(at(t, 0), "fresh session"),
		workTurn(at(t, time.Minute), "still working"),
	)
	rep := scan(t, home, Options{})
	if rep.State != StateWorking {
		t.Fatalf("State = %s, want %s — the live session is the one being written", rep.State, StateWorking)
	}
	if !strings.Contains(rep.Session, "zzz-live") {
		t.Errorf("Session = %q, want the live session", rep.Session)
	}
	if rep.Files != 2 {
		t.Errorf("Files = %d, want 2", rep.Files)
	}
}

// And the reverse: a stale healthy session must not exonerate a live failing one.
func TestAStaleHealthySessionDoesNotExonerateTheLiveOne(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "aaa-old",
		workTurn(at(t, -3*time.Hour), "was fine"),
	)
	writeSession(t, home, "zzz-live",
		failTurn(at(t, 0), "Login expired · Please run /login"),
		failTurn(at(t, 10*time.Minute), "Login expired · Please run /login"),
		failTurn(at(t, 20*time.Minute), "Login expired · Please run /login"),
	)
	rep := scan(t, home, Options{})
	if rep.State != StateStreaking {
		t.Fatalf("State = %s, want %s", rep.State, StateStreaking)
	}
	if rep.Reason != ReasonLoginPrompt {
		t.Errorf("Reason = %s, want %s", rep.Reason, ReasonLoginPrompt)
	}
}

// ----------------------------------------------------------------- markers

// `API Error:` is a prefix several other modes also carry. Ordered wrong, it
// renames an auth outage as a network one.
func TestTheLogin403IsNamedLoginNotAPIError(t *testing.T) {
	home := t.TempDir()
	const text = "Please run /login · API Error: 403 The socket connection was closed unexpectedly. " +
		"For more information, pass `verbose: true` in the second argument to fetch()"
	writeSession(t, home, "s1",
		failTurn(at(t, 0), text), failTurn(at(t, time.Minute), text), failTurn(at(t, 2*time.Minute), text))
	rep := scan(t, home, Options{})
	if rep.Reason != ReasonLoginPrompt {
		t.Fatalf("Reason = %s, want %s — 297 turns in the corpus carry both strings and the remedy is /login, not DNS",
			rep.Reason, ReasonLoginPrompt)
	}
}

// Every mode named in mg-6616's classification must be reachable, and each one's
// Human() text must actually say something.
func TestEveryMarkerClassifiesAndNamesARemedy(t *testing.T) {
	for _, m := range DefaultMarkers {
		got, hit := match("prefix "+m.Text+" suffix", nil)
		if !hit {
			t.Errorf("marker %q did not match its own text", m.Text)
			continue
		}
		if got != m.Reason {
			t.Errorf("marker %q classified as %s, want %s", m.Text, got, m.Reason)
		}
		if h := m.Reason.Human(); h == "" || len(h) < 20 {
			t.Errorf("Reason %s renders %q; the alarm is read by a person who must know what to DO", m.Reason, h)
		}
		if m.Seen <= 0 {
			t.Errorf("marker %q records Seen=%d; an entry with no measured occurrences is a hunch", m.Text, m.Seen)
		}
	}
	if _, hit := match("an ordinary sentence about nothing", nil); hit {
		t.Error("an ordinary sentence matched a failure marker")
	}
}

// ------------------------------------------------------------- robustness

// A megabyte tool-result turn must not silently break a run: an unread line is
// an unclassified turn, not nothing.
func TestAnOversizedLineIsUnclassifiedNotWork(t *testing.T) {
	home := t.TempDir()
	huge := line(map[string]any{
		"type": "assistant", "timestamp": at(t, 5*time.Minute),
		"message": map[string]any{"model": "claude-opus-5",
			"content": []any{map[string]any{"type": "text", "text": strings.Repeat("x", maxLineBytes+10)}},
			"usage":   map[string]any{"input_tokens": 5, "output_tokens": 5}},
	})
	writeSession(t, home, "s1",
		workTurn(at(t, 0), "working"),
		failTurn(at(t, time.Minute), "Request timed out"),
		huge,
		failTurn(at(t, 10*time.Minute), "Request timed out"),
		failTurn(at(t, 20*time.Minute), "Request timed out"),
	)
	rep := scan(t, home, Options{})
	if rep.State != StateStreaking || rep.Streak != 3 {
		t.Fatalf("State = %s streak = %d, want %s / 3 — an unread line broke the run", rep.State, rep.Streak, StateStreaking)
	}
	if rep.Unclassified != 1 {
		t.Errorf("Unclassified = %d, want 1", rep.Unclassified)
	}
}

// Every report is dated, including the ones that say nothing, because a reader
// who cannot date a claim dates it to now.
func TestEveryStateIsStamped(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name  string
		setup func()
	}{
		{"unavailable", func() {}},
		{"working", func() { writeSession(t, home, "s1", workTurn(at(t, 0), "ok")) }},
		{"streaking", func() {
			writeSession(t, home, "s1",
				failTurn(at(t, 0), "Request timed out"),
				failTurn(at(t, time.Minute), "Request timed out"),
				failTurn(at(t, 2*time.Minute), "Request timed out"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			rep := Scan(home, fixtureGlobs, Options{Now: now})
			if !rep.ScannedAt.Equal(now) {
				t.Errorf("ScannedAt = %v, want %v", rep.ScannedAt, now)
			}
			if rep.MinStreak != DefaultMinStreak {
				t.Errorf("MinStreak = %d, want %d", rep.MinStreak, DefaultMinStreak)
			}
		})
	}
}

// A run that crosses midnight must not render as a short one. The founding
// 661-turn run spanned 2026-08-14 to 2026-08-19.
func TestBriefKeepsBothDatesOnAMultiDayRun(t *testing.T) {
	rep := Report{
		State: StateStreaking, Streak: 661, Reason: ReasonSpendLimit,
		First: time.Date(2026, 8, 14, 8, 25, 23, 0, time.UTC),
		Last:  time.Date(2026, 8, 19, 6, 48, 35, 0, time.UTC),
	}
	got := rep.Brief()
	for _, want := range []string{"2026-08-14T08:25:23Z", "2026-08-19T06:48:35Z", "661"} {
		if !strings.Contains(got, want) {
			t.Errorf("Brief() = %q, want %q in it", got, want)
		}
	}
}

// The MinStreak knob is honoured, and a caller cannot disable the floor by
// passing zero or a negative.
func TestMinStreakFloorsAtOne(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "s1", workTurn(at(t, 0), "ok"), failTurn(at(t, time.Minute), "Request timed out"))
	if rep := scan(t, home, Options{MinStreak: -5}); rep.State != StateStreaking {
		t.Errorf("MinStreak=-5 gave State = %s, want %s (floored to 1)", rep.State, StateStreaking)
	}
	if rep := scan(t, home, Options{MinStreak: 2}); rep.State == StateStreaking {
		t.Error("MinStreak=2 alarmed on a run of 1")
	}
}

func TestVerdictAndStateStringsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range []State{StateUnavailable, StateWorking, StateUnwitnessed, StateStreaking} {
		if seen[s.String()] {
			t.Errorf("duplicate State string %q", s)
		}
		seen[s.String()] = true
	}
	seen = map[string]bool{}
	for _, v := range []Verdict{VerdictUnclassified, VerdictWork, VerdictFailure, VerdictAmbiguous} {
		if seen[v.String()] {
			t.Errorf("duplicate Verdict string %q", v)
		}
		seen[v.String()] = true
	}
}

// A directory of files that cannot be opened must report unavailable rather than
// quiet. Constructed by removing read permission.
func TestAnUnreadableTranscriptIsUnavailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; a 0000 file is still readable and this construction proves nothing")
	}
	home := t.TempDir()
	p := writeSession(t, home, "s1", workTurn(at(t, 0), "ok"))
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	rep := scan(t, home, Options{})
	if rep.State != StateUnavailable {
		t.Fatalf("State = %s, want %s", rep.State, StateUnavailable)
	}
	if !strings.Contains(rep.Unavailable, "could not be read") {
		t.Errorf("Unavailable = %q, want it to say the read failed", rep.Unavailable)
	}
}

// A positive control for the whole fixture rig: if the corpus-shaped failure
// turn stopped matching the structural test, every negative test above would go
// green for the wrong reason.
func TestTheFixtureFailureTurnIsStructurallyAFailure(t *testing.T) {
	var tn turn
	if err := json.Unmarshal([]byte(failTurn(at(t, 0), "Request timed out")), &tn); err != nil {
		t.Fatal(err)
	}
	if !tn.synthetic() {
		t.Fatal("the fixture failure turn does not satisfy the structural test; every negative in this file is vacuous")
	}
	v, r := classify(&tn, nil)
	if v != VerdictFailure || r != ReasonTimeout {
		t.Fatalf("classify = %s/%s, want %s/%s", v, r, VerdictFailure, ReasonTimeout)
	}
	var wt turn
	if err := json.Unmarshal([]byte(workTurn(at(t, 0), "hello")), &wt); err != nil {
		t.Fatal(err)
	}
	if v, _ := classify(&wt, nil); v != VerdictWork {
		t.Fatalf("the fixture work turn classified as %s, want %s", v, VerdictWork)
	}
}

func ExampleReport_Brief() {
	rep := Report{
		State: StateStreaking, Streak: 3, Reason: ReasonLoginPrompt,
		First: time.Date(2026, 9, 7, 11, 30, 10, 0, time.UTC),
		Last:  time.Date(2026, 9, 7, 11, 41, 11, 0, time.UTC),
	}
	fmt.Println(rep.Brief())
	// Output: 3 consecutive failing turns (login), 2026-09-07T11:30:10Z–11:41:11Z
}

// ------------------------------------------------------- the drift branch

// The structural predicate names two harness internals pogo does not own. This
// is what happens when one of them changes: the detector must keep firing, not
// go quiet. Constructed by renaming the synthetic-model attribution, which is
// the single most likely thing to move.
func TestAHarnessSchemaChangeDoesNotSilenceTheDetector(t *testing.T) {
	home := t.TempDir()
	drifted := func(ts string) string {
		return line(map[string]any{
			// isApiErrorMessage gone, model renamed — everything the structural
			// test keys on, moved at once.
			"type": "assistant", "timestamp": ts,
			"message": map[string]any{
				"model":   "<local-error>",
				"content": []any{map[string]any{"type": "text", "text": "Request timed out"}},
				"usage":   map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		})
	}
	writeSession(t, home, "s1",
		workTurn(at(t, 0), "working"),
		drifted(at(t, time.Minute)), drifted(at(t, 11*time.Minute)), drifted(at(t, 21*time.Minute)))
	rep := scan(t, home, Options{})
	if rep.State != StateStreaking {
		t.Fatalf("State = %s, want %s — the detector went silent on a harness rename, which is how a detector "+
			"stops working without ever reporting that it stopped", rep.State, StateStreaking)
	}
	if rep.Reason != ReasonTimeout {
		t.Errorf("Reason = %s, want %s", rep.Reason, ReasonTimeout)
	}
}

// And the control that keeps that branch honest: the same string, in a turn that
// actually cost tokens, must stay ambiguous. Without this, the drift branch is
// just string-matching with extra steps and the 21 corpus turns come back.
func TestTheDriftBranchStillSpendsTokensToBeCalledWork(t *testing.T) {
	home := t.TempDir()
	writeSession(t, home, "s1",
		talkTurn(at(t, 0), "I am writing about `Request timed out` and what it means"),
		talkTurn(at(t, time.Minute), "Still writing about `Request timed out`"),
		talkTurn(at(t, 2*time.Minute), "And again: `Request timed out`"),
	)
	rep := scan(t, home, Options{})
	if rep.State == StateStreaking {
		t.Fatalf("three real, token-spending turns that MENTION a failure string alarmed; the drift branch has "+
			"widened into plain string-matching (streak=%d)", rep.Streak)
	}
	if rep.Ambiguous != 3 {
		t.Errorf("Ambiguous = %d, want 3", rep.Ambiguous)
	}
}
