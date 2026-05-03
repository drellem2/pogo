package scheduler

import (
	"os"
	"testing"
	"time"
)

func TestCronParseAndNext(t *testing.T) {
	cases := []struct {
		expr     string
		from     time.Time
		wantNext time.Time
	}{
		// Every 5 minutes, starting from a top-of-hour moment.
		{"*/5 * * * *",
			time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 3, 12, 5, 0, 0, time.UTC)},
		// Hourly at :07 — minute 7 hits next at the upcoming :07.
		{"7 * * * *",
			time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 3, 12, 7, 0, 0, time.UTC)},
		// Hourly at :00 — top of next hour.
		{"0 * * * *",
			time.Date(2026, 5, 3, 12, 5, 0, 0, time.UTC),
			time.Date(2026, 5, 3, 13, 0, 0, 0, time.UTC)},
		// Daily at 09:00 — when current is later in the day, jump to tomorrow.
		{"0 9 * * *",
			time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)},
		// Weekday-only at 09:00 from a Sunday — next is Monday.
		{"0 9 * * 1-5",
			time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC), // Sunday
			time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)},
		// First of the month at 00:00 from mid-month — next month.
		{"0 0 1 * *",
			time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
		// Comma list of minutes.
		{"5,15,25 * * * *",
			time.Date(2026, 5, 3, 12, 10, 0, 0, time.UTC),
			time.Date(2026, 5, 3, 12, 15, 0, 0, time.UTC)},
		// Range with step.
		{"0-30/10 * * * *",
			time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 3, 12, 10, 0, 0, time.UTC)},
		// Sunday alias 7 → 0.
		{"0 9 * * 7",
			time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),  // Monday
			time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC)}, // following Sunday
	}
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			cron, err := ParseCron(c.expr)
			if err != nil {
				t.Fatalf("ParseCron(%q): %v", c.expr, err)
			}
			got := cron.Next(c.from)
			if !got.Equal(c.wantNext) {
				t.Errorf("Next(%s): want %s, got %s", c.from, c.wantNext, got)
			}
		})
	}
}

func TestCronParseRejectsBadInput(t *testing.T) {
	bad := []string{
		"",
		"* * * *",     // 4 fields
		"* * * * * *", // 6 fields
		"60 * * * *",  // minute > 59
		"* 24 * * *",  // hour > 23
		"* * 0 * *",   // dom < 1
		"* * 32 * *",  // dom > 31
		"* * * 13 *",  // month > 12
		"* * * * 9",   // dow > 7
		"*/0 * * * *", // step zero
		"abc * * * *", // garbage
		"5-3 * * * *", // inverted range
	}
	for _, expr := range bad {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) should have errored", expr)
		}
	}
}

// TestCronNextStrictAdvance asserts that Next is strictly greater than the
// input — calling Next(t) twice on the same boundary moves forward, never
// repeats. This is the property the scheduler relies on after a fire to find
// the *next* slot.
func TestCronNextStrictAdvance(t *testing.T) {
	cron, err := ParseCron("0 12 * * *")
	if err != nil {
		t.Fatal(err)
	}
	t1 := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	got := cron.Next(t1)
	want := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Next(at-boundary): want %s, got %s", want, got)
	}
}

// readFile is a tiny helper used by the persistence test in scheduler_test.go.
// Defined here to avoid t.TempDir cleanup races between the two files.
func readFile(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(path)
}
