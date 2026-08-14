package synthfail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The defect these tests guard (mg-c058): a Report carries a COUNT over a
// trailing window, and every renderer of it printed the count — or just the
// word "failing_turns" — with no window attached. A count with no window is
// read as a present-tense claim about capacity, which is how a two-error
// network blip paged a sleeping human with "AGENTS ARE FAILING EVERY TURN"
// naming an agent that was completing turns.

func at(h, m, s int) time.Time {
	return time.Date(2026, 8, 14, h, m, s, 0, time.UTC)
}

// The exact reading from the incident. If Brief ever drops the count or the
// window, this is the string that stops being distinguishable from a live
// fleet-wide credential failure.
func TestBrief_CarriesCountWindowAndSpan(t *testing.T) {
	r := Report{
		State:         StateFailing,
		Reason:        ReasonServerError,
		Count:         2,
		First:         at(2, 24, 50),
		Last:          at(2, 33, 27),
		WindowSeconds: 1800,
	}
	got := r.Brief()
	want := "2 errors in 30m, 2026-08-14T02:24:50Z–02:33:27Z"
	if got != want {
		t.Fatalf("Brief() = %q, want %q", got, want)
	}
	// Each part earns its place separately, so a future edit that keeps the
	// shape but drops a component still fails here.
	for _, part := range []string{"2 error", "30m", "02:24:50Z", "02:33:27Z"} {
		if !strings.Contains(got, part) {
			t.Errorf("Brief() = %q, missing %q — a reader cannot bound the count without it", got, part)
		}
	}
}

// Brief is what travels into mail and events, so it must contain nothing that
// decays. A relative age is wrong the moment it is stored: the 2026-08-14 page
// was noticed by the delivering daemon 16m26s after it was sent.
func TestBrief_ContainsNoDecayingAge(t *testing.T) {
	r := Report{State: StateFailing, Count: 2, First: at(2, 24, 50), Last: at(2, 33, 27), WindowSeconds: 1800}
	if strings.Contains(r.Brief(), " ago") {
		t.Fatalf("Brief() = %q contains a relative age; Brief is the persistable form and must be absolute", r.Brief())
	}
}

func TestReading_AddsRecencyAndScanAgeOnlyWhenLive(t *testing.T) {
	r := Report{
		State:         StateFailing,
		Count:         2,
		First:         at(2, 24, 50),
		Last:          at(2, 33, 27),
		WindowSeconds: 1800,
		ScannedAt:     at(2, 44, 0),
	}
	// A zero clock must NOT quietly become time.Now(): a caller with no clock
	// gets the absolute form, not an age measured against a clock it did not
	// choose.
	if got := r.Reading(time.Time{}); got != r.Brief() {
		t.Errorf("Reading(zero) = %q, want Brief() = %q", got, r.Brief())
	}

	got := r.Reading(at(2, 47, 27))
	if !strings.Contains(got, "last 14m ago") {
		t.Errorf("Reading() = %q, want a recency of the last error (14m)", got)
	}
	// The scan behind a failing agent's diagnose is served from the watcher's
	// cache and can be minutes stale. Unsaid, the reader dates it to now.
	if !strings.Contains(got, "scan 3m old") {
		t.Errorf("Reading() = %q, want the scan's own age — a cached verdict read as live is the same defect one layer down", got)
	}
}

func TestReading_FreshScanDoesNotClutterTheLine(t *testing.T) {
	r := Report{State: StateFailing, Count: 2, First: at(2, 24, 50), Last: at(2, 33, 27), WindowSeconds: 1800, ScannedAt: at(2, 47, 20)}
	if got := r.Reading(at(2, 47, 27)); strings.Contains(got, "scan") {
		t.Errorf("Reading() = %q, want no scan age for a 7s-old scan", got)
	}
}

// The transcript's clock and pogod's clock are not the same clock. A few
// seconds of skew must not render "last -2m0s ago", which reads as the future.
func TestReading_ClampsNegativeSkew(t *testing.T) {
	r := Report{State: StateFailing, Count: 2, First: at(2, 24, 50), Last: at(2, 33, 27), WindowSeconds: 1800}
	got := r.Reading(at(2, 33, 0))
	if !strings.Contains(got, "last 0s ago") {
		t.Errorf("Reading() = %q, want the skewed age clamped to 0s", got)
	}
	if strings.Contains(got, "last -") {
		t.Errorf("Reading() = %q renders a negative age, which reads as the future", got)
	}
}

// The duration renderer is the smallest piece of this remedy and the one that
// can silently corrupt every reading built on it. The first version trimmed a
// "0m" tail unconditionally and turned "30m" into "3" — the ticket's own defect,
// a misleading rendering, committed inside its fix.
func TestCompactDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-2 * time.Minute, "0s"},
		{27 * time.Second, "27s"},
		{30 * time.Minute, "30m"},
		{14 * time.Minute, "14m"},
		{time.Hour, "1h0m"},
		{90 * time.Minute, "1h30m"},
		{23*time.Hour + 30*time.Minute, "23h30m"},
		{25 * time.Hour, "25h0m"},
		{49*time.Hour + 20*time.Minute, "49h0m"},
	}
	for _, c := range cases {
		if got := compactDuration(c.in); got != c.want {
			t.Errorf("compactDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The founding 23h30m outage spanned midnight. A span that drops the second
// date renders it as a thirteen-minute window — the exact class of
// under-reading this ticket is about.
func TestBrief_SpanKeepsBothDatesAcrossMidnight(t *testing.T) {
	r := Report{
		State:         StateFailing,
		Count:         143,
		First:         time.Date(2026, 7, 21, 23, 10, 26, 0, time.UTC),
		Last:          time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		WindowSeconds: 1800,
	}
	got := r.Brief()
	if !strings.Contains(got, "2026-07-21T23:10:26Z") || !strings.Contains(got, "2026-07-22T12:00:00Z") {
		t.Fatalf("Brief() = %q, want both dates spelled out across a midnight boundary", got)
	}
}

// There is no window to report when there were no failing turns, and an empty
// string renders as nothing rather than as a confident zero.
func TestBrief_EmptyForNonFailingStates(t *testing.T) {
	for _, r := range []Report{
		{State: StateQuiet, WindowSeconds: 1800},
		{State: StateUnavailable, Unavailable: "no transcript", WindowSeconds: 1800},
		{State: StateFailing, Count: 0, WindowSeconds: 1800},
	} {
		if got := r.Brief(); got != "" {
			t.Errorf("Brief() = %q for state %v count %d, want empty", got, r.State, r.Count)
		}
	}
}

// Every answer Scan can give is a claim about a moment. An unstamped one has no
// moment attached, and a reader supplies "now" for the missing moment every
// time — including for "we could not look", which is the answer most likely to
// be re-read hours later.
func TestScan_StampsEveryStateWithItsWindow(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "t")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := at(2, 47, 3)
	opts := Options{Now: now, Window: 30 * time.Minute}

	quietPath := filepath.Join(dir, "quiet.jsonl")
	if err := os.WriteFile(quietPath, []byte("{\"type\":\"assistant\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(quietPath, now, now); err != nil {
		t.Fatal(err)
	}

	cases := map[string]Report{
		"no globs":     Scan(home, nil, opts),
		"no home":      Scan("", []string{"t/*.jsonl"}, opts),
		"no such file": Scan(home, []string{"nope/*.jsonl"}, opts),
		"quiet":        Scan(home, []string{"t/*.jsonl"}, opts),
	}
	for name, rep := range cases {
		if !rep.ScannedAt.Equal(now) {
			t.Errorf("%s: ScannedAt = %v, want %v — an undated verdict gets dated to whenever it is read", name, rep.ScannedAt, now)
		}
		if rep.WindowSeconds != 1800 {
			t.Errorf("%s: WindowSeconds = %d, want 1800", name, rep.WindowSeconds)
		}
	}
}

func TestScan_FailingReportIsStampedAndRenders(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "t")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := at(2, 47, 3)
	line := func(ts string) string {
		return `{"type":"assistant","timestamp":"` + ts + `","isApiErrorMessage":true,"error":"server_error","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Can't reach the API server (ENOTFOUND)"}],"usage":{"input_tokens":0,"output_tokens":0}}}`
	}
	body := line("2026-08-14T02:24:50Z") + "\n" + line("2026-08-14T02:33:27Z") + "\n"
	p := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, now, now); err != nil {
		t.Fatal(err)
	}

	rep := Scan(home, []string{"t/*.jsonl"}, Options{Now: now, Window: 30 * time.Minute})
	if rep.State != StateFailing {
		t.Fatalf("State = %v, want failing (%d turns counted)", rep.State, rep.Count)
	}
	if rep.WindowSeconds != 1800 || !rep.ScannedAt.Equal(now) {
		t.Fatalf("stamps = (%d, %v), want (1800, %v)", rep.WindowSeconds, rep.ScannedAt, now)
	}
	// This is the reading that, rendered as the bare token "failing_turns",
	// became "AGENTS ARE FAILING EVERY TURN — mayor" at 02:44Z.
	want := "2 errors in 30m, 2026-08-14T02:24:50Z–02:33:27Z"
	if rep.Brief() != want {
		t.Errorf("Brief() = %q, want %q", rep.Brief(), want)
	}
	if rep.Reason != ReasonServerError {
		t.Errorf("Reason = %q, want server_error", rep.Reason)
	}
}
