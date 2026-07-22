package synthfail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// installFixture copies a testdata transcript into a fake home at the
// home-relative path a provider would declare, and returns (home, glob).
func installFixture(t *testing.T, fixtures ...string) (home, glob string) {
	t.Helper()
	home = t.TempDir()
	dir := filepath.Join(home, ".harness", "projects", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		data, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, filepath.Join(".harness", "projects", "agent", "*.jsonl")
}

// fixtureLast returns the timestamp of the last record in a fixture, so a test
// can place its window around the fixture's real timestamps rather than hard-
// coding a date that will rot.
func fixtureLast(t *testing.T, name string) time.Time {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var last time.Time
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil && ts.After(last) {
			last = ts
		}
	}
	if last.IsZero() {
		t.Fatalf("fixture %s has no parseable timestamps", name)
	}
	return last
}

// ---------------------------------------------------------------------------
// THE DETECTOR FIRES — one case per member of the class, all real transcript
// records. Detection is structural, so these also prove the reason strings are
// decoration: every one of them is found by the same three-part test.
// ---------------------------------------------------------------------------

func TestScan_FiresOnEveryMemberOfTheClass(t *testing.T) {
	cases := []struct {
		fixture string
		want    Reason
	}{
		{"auth-expired-2026-07-22.jsonl", ReasonAuthFailed},
		{"rate-limit.jsonl", ReasonRateLimit},
		{"weekly-limit.jsonl", ReasonWeeklyLimit},
		{"spend-limit.jsonl", ReasonSpendLimit},
	}
	for _, tc := range cases {
		t.Run(string(tc.want), func(t *testing.T) {
			home, glob := installFixture(t, tc.fixture)
			last := fixtureLast(t, tc.fixture)

			// Window wide enough to span the whole fixture run.
			got := Scan(home, []string{glob}, Options{Now: last.Add(time.Minute), Window: 72 * time.Hour})

			if got.State != StateFailing {
				t.Fatalf("state = %v, want StateFailing (report: %+v)", got.State, got)
			}
			if got.Reason != tc.want {
				t.Errorf("reason = %q, want %q", got.Reason, tc.want)
			}
			if got.Count < 2 {
				t.Errorf("count = %d, want the whole run", got.Count)
			}
			if got.Detail == "" {
				t.Error("detail is empty; the page has nothing to show a human")
			}
			if !got.SuppressRestart() {
				t.Error("SuppressRestart() = false; a restart cannot fix any member of this class")
			}
			if got.Unavailable != "" {
				t.Errorf("Unavailable = %q on a successful read; the field must be exclusive to StateUnavailable", got.Unavailable)
			}
		})
	}
}

// TestScan_DetectionSurvivesAnUnknownReason is the anti-hard-coding proof. A
// harness that invents a new error code must still be DETECTED — it is still a
// synthetic zero-token failure turn — and merely fall back to an unnamed reason.
// If detection ever moves into the reason table, this test fails.
func TestScan_DetectionSurvivesAnUnknownReason(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".harness", "projects", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	line := func(ts time.Time) string {
		return `{"type":"assistant","timestamp":"` + ts.Format(time.RFC3339Nano) + `",` +
			`"error":"some_code_that_did_not_exist_in_2026","isApiErrorMessage":true,` +
			`"message":{"model":"<synthetic>","role":"assistant",` +
			`"content":[{"type":"text","text":"A brand new failure nobody has seen"}],` +
			`"usage":{"input_tokens":0,"output_tokens":0}}}`
	}
	body := line(now.Add(-20*time.Minute)) + "\n" + line(now.Add(-10*time.Minute)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Scan(home, []string{filepath.Join(".harness", "projects", "agent", "*.jsonl")}, Options{Now: now})
	if got.State != StateFailing {
		t.Fatalf("state = %v, want StateFailing: an unrecognised error code is still this class", got.State)
	}
	if got.Reason != ReasonUnclassified {
		t.Errorf("reason = %q, want %q", got.Reason, ReasonUnclassified)
	}
	if !got.SuppressRestart() {
		t.Error("SuppressRestart() = false for an unnamed member of the class")
	}
}

// ---------------------------------------------------------------------------
// THE DETECTOR STAYS SILENT — the half that makes it a discriminator rather
// than an alarm. Each of these is a way pogo could be wrong in the direction
// that matters least visibly.
// ---------------------------------------------------------------------------

// TestScan_SilentTranscriptIsNotThisClass is the negative control the whole
// design turns on. A genuinely wedged agent stops writing; it must report
// QUIET, so the existing wedge handling — including restart, which is the right
// answer for a real wedge — applies unchanged.
func TestScan_SilentTranscriptIsNotThisClass(t *testing.T) {
	home, glob := installFixture(t, "wedged-silent.jsonl")
	last := fixtureLast(t, "wedged-silent.jsonl")

	// Scan as of two hours after the transcript went quiet: exactly the moment
	// the mayor's 120-minute rule would be deciding whether to restart.
	got := Scan(home, []string{glob}, Options{Now: last.Add(2 * time.Hour), Window: 72 * time.Hour})

	if got.State != StateQuiet {
		t.Fatalf("state = %v, want StateQuiet: a wedged agent writes NOTHING and must stay restartable (report: %+v)", got.State, got)
	}
	if got.SuppressRestart() {
		t.Fatal("SuppressRestart() = true for a genuinely wedged agent — this would disable the one remediation that DOES work on a wedge")
	}
	if got.Count != 0 {
		t.Errorf("count = %d, want 0", got.Count)
	}
}

// TestScan_RealWorkTurnsAreNotFailures guards the other direction: a busy agent
// writing real turns, including a large one, must never trip the detector.
func TestScan_RealWorkTurnsAreNotFailures(t *testing.T) {
	home, glob := installFixture(t, "wedged-silent.jsonl")
	last := fixtureLast(t, "wedged-silent.jsonl")

	got := Scan(home, []string{glob}, Options{Now: last.Add(time.Minute), Window: time.Hour})
	if got.State != StateQuiet {
		t.Fatalf("state = %v, want StateQuiet for an agent doing real work", got.State)
	}
}

// TestScan_ErrorTurnThatSpentTokensIsNotThisClass separates a REAL API call that
// errored from a turn the harness answered locally. The first reached the model
// and cost tokens; only the second is the class. Without the usage test, an
// ordinary mid-turn API hiccup would suppress restarts fleet-wide.
func TestScan_ErrorTurnThatSpentTokensIsNotThisClass(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".harness", "projects", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	// Everything says failure EXCEPT the token spend.
	line := `{"type":"assistant","timestamp":"` + now.Add(-5*time.Minute).Format(time.RFC3339Nano) + `",` +
		`"error":"rate_limit","isApiErrorMessage":true,` +
		`"message":{"model":"<synthetic>","role":"assistant",` +
		`"content":[{"type":"text","text":"API Error"}],` +
		`"usage":{"input_tokens":1804,"output_tokens":0}}}`
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(line+"\n"+line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Scan(home, []string{filepath.Join(".harness", "projects", "agent", "*.jsonl")}, Options{Now: now})
	if got.State != StateQuiet {
		t.Fatalf("state = %v, want StateQuiet: a turn that spent input tokens reached the model", got.State)
	}
}

// TestScan_OneStrayFailureIsNotAnEpisode. A single synthetic turn is an ordinary
// transient the next turn recovers from. Paging on it would make the detector
// noise, and a noisy detector gets muted before the run that matters.
func TestScan_OneStrayFailureIsNotAnEpisode(t *testing.T) {
	home, glob := installFixture(t, "auth-expired-2026-07-22.jsonl")
	first := time.Date(2026, 7, 22, 0, 0, 24, 880000000, time.UTC)

	// A window that contains exactly the first record.
	got := Scan(home, []string{glob}, Options{Now: first.Add(time.Minute), Window: 2 * time.Minute})
	if got.State != StateQuiet {
		t.Fatalf("state = %v, want StateQuiet for a single stray failure turn (count=%d)", got.State, got.Count)
	}
	if got.SuppressRestart() {
		t.Error("one transient must not suppress restarts")
	}
}

// TestScan_OldFailuresDoNotPageToday. mg-18d0 counted ~5500 of these turns
// across fleet HISTORY. If history could fire the detector, it would page on
// every startup forever.
func TestScan_OldFailuresDoNotPageToday(t *testing.T) {
	home, glob := installFixture(t, "auth-expired-2026-07-22.jsonl")
	last := fixtureLast(t, "auth-expired-2026-07-22.jsonl")

	got := Scan(home, []string{glob}, Options{Now: last.Add(30 * 24 * time.Hour)})
	if got.State != StateQuiet {
		t.Fatalf("state = %v, want StateQuiet: a month-old episode is history, not news", got.State)
	}
}

// TestScan_HistoryDoesNotMaskALiveFailure is the inverse: real work turns
// earlier in the same file must not make a current failure invisible.
func TestScan_HistoryDoesNotMaskALiveFailure(t *testing.T) {
	home, glob := installFixture(t, "healthy-then-failing.jsonl")
	last := fixtureLast(t, "healthy-then-failing.jsonl")

	got := Scan(home, []string{glob}, Options{Now: last.Add(time.Minute), Window: 72 * time.Hour})
	if got.State != StateFailing {
		t.Fatalf("state = %v, want StateFailing", got.State)
	}
	if got.Reason != ReasonAuthFailed {
		t.Errorf("reason = %q, want %q", got.Reason, ReasonAuthFailed)
	}
}

// ---------------------------------------------------------------------------
// DEGRADATION — absence must never read as health. These are the tests that
// stop this detector from repeating the error it was built to catch.
// ---------------------------------------------------------------------------

func TestScan_DegradesWhenNothingToRead(t *testing.T) {
	cases := []struct {
		name  string
		home  string
		globs []string
	}{
		{"no globs declared", t.TempDir(), nil},
		{"empty glob declared", t.TempDir(), []string{""}},
		{"declared path does not exist", t.TempDir(), []string{".harness/projects/agent/*.jsonl"}},
		{"no home", "", []string{".harness/projects/agent/*.jsonl"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Scan(tc.home, tc.globs, Options{})

			if got.State != StateUnavailable {
				t.Fatalf("state = %v, want StateUnavailable — 'we could not look' must never render as 'we looked and saw nothing'", got.State)
			}
			if got.Unavailable == "" {
				t.Error("Unavailable is empty; the reason we could not look must always be stated")
			}
			if got.SuppressRestart() {
				t.Error("SuppressRestart() = true with no evidence: this would disable wedge recovery for every harness that has no transcript")
			}
		})
	}
}

// TestState_ZeroValueIsUnavailable. The zero Report must be the no-claim
// answer, so a caller that forgets to run a scan cannot read health out of an
// empty struct.
func TestState_ZeroValueIsUnavailable(t *testing.T) {
	var r Report
	if r.State != StateUnavailable {
		t.Fatalf("zero Report state = %v, want StateUnavailable", r.State)
	}
	if r.SuppressRestart() {
		t.Fatal("zero Report suppresses restarts")
	}
}

// TestScan_UnreadableTranscriptIsUnavailableNotQuiet. A directory that exists
// but whose files cannot be opened must degrade, not report all-clear.
func TestScan_UnreadableTranscriptIsUnavailableNotQuiet(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny reads")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".harness", "projects", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A glob that matches only a directory, never a readable file.
	if err := os.MkdirAll(filepath.Join(dir, "notafile.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Scan(home, []string{filepath.Join(".harness", "projects", "agent", "*.jsonl")}, Options{})
	if got.State != StateUnavailable {
		t.Fatalf("state = %v, want StateUnavailable", got.State)
	}
}

// ---------------------------------------------------------------------------
// Locate — the same containment discipline memcheck.Locate enforces (mg-5a06).
// ---------------------------------------------------------------------------

func TestLocate_JoinsUnderHomeAndDeDupes(t *testing.T) {
	home, glob := installFixture(t, "auth-expired-2026-07-22.jsonl")

	got := Locate(home, []string{glob, glob, ""}, time.Time{})
	if len(got) != 1 {
		t.Fatalf("Locate returned %d paths, want 1 (overlapping globs must de-dupe): %v", len(got), got)
	}
	if !strings.HasPrefix(got[0], home) {
		t.Errorf("Locate returned %q, which is outside home %q", got[0], home)
	}
}

func TestLocate_ModifiedSinceFiltersStaleFiles(t *testing.T) {
	home, glob := installFixture(t, "auth-expired-2026-07-22.jsonl")

	if got := Locate(home, []string{glob}, time.Now().Add(-time.Hour)); len(got) != 1 {
		t.Fatalf("fresh file filtered out: %v", got)
	}
	if got := Locate(home, []string{glob}, time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatalf("stale file kept: %v", got)
	}
}

// TestScan_LongLinesAreSkippedNotFatal. Real transcripts carry multi-megabyte
// tool-result turns. They must not stop the scan, and the failing turns around
// them must still be found.
func TestScan_LongLinesAreSkippedNotFatal(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".harness", "projects", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	fail := func(ts time.Time) string {
		return `{"type":"assistant","timestamp":"` + ts.Format(time.RFC3339Nano) + `",` +
			`"error":"authentication_failed","isApiErrorMessage":true,` +
			`"message":{"model":"<synthetic>","role":"assistant",` +
			`"content":[{"type":"text","text":"Login expired"}],` +
			`"usage":{"input_tokens":0,"output_tokens":0}}}`
	}
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("x", 2*maxLineBytes) + `"}}`
	body := fail(now.Add(-20*time.Minute)) + "\n" + huge + "\n" + fail(now.Add(-10*time.Minute)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Scan(home, []string{filepath.Join(".harness", "projects", "agent", "*.jsonl")}, Options{Now: now})
	if got.State != StateFailing || got.Count != 2 {
		t.Fatalf("state = %v count = %d, want StateFailing with both failures found across the oversized line", got.State, got.Count)
	}
}

func TestReason_HumanIsAlwaysPopulated(t *testing.T) {
	for _, r := range []Reason{
		ReasonUnclassified, ReasonAuthFailed, ReasonRateLimit,
		ReasonWeeklyLimit, ReasonSpendLimit, ReasonServerError, ReasonInvalidRequest,
	} {
		if r.Human() == "" {
			t.Errorf("Reason(%q).Human() is empty; a page with no explanation is a page nobody can act on", r)
		}
	}
}
