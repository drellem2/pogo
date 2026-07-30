package main

import (
	"testing"
	"time"
)

func TestParseSinceDurations(t *testing.T) {
	now := time.Now()
	cases := []struct {
		in   string
		back time.Duration
	}{
		{"90m", 90 * time.Minute},
		{"24h", 24 * time.Hour},
		// Days are accepted even though time.ParseDuration rejects them: the
		// question --since answers is usually scoped in days, and making the
		// caller convert to hours moves the arithmetic to whoever can check it
		// least.
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"1h30m", 90 * time.Minute},
		{" 6h ", 6 * time.Hour},
	}
	for _, c := range cases {
		got, err := parseSince(c.in)
		if err != nil {
			t.Errorf("parseSince(%q): %v", c.in, err)
			continue
		}
		want := now.Add(-c.back)
		if d := got.Sub(want); d > 5*time.Second || d < -5*time.Second {
			t.Errorf("parseSince(%q) = %s, want ~%s", c.in, got, want)
		}
	}
}

func TestParseSinceDates(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"2026-07-01", time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)},
		{"2026-07-01 12:30", time.Date(2026, 7, 1, 12, 30, 0, 0, time.Local)},
		{"2026-07-01 12:30:45", time.Date(2026, 7, 1, 12, 30, 45, 0, time.Local)},
		{"2026-07-01T12:30:45Z", time.Date(2026, 7, 1, 12, 30, 45, 0, time.UTC)},
	}
	for _, c := range cases {
		got, err := parseSince(c.in)
		if err != nil {
			t.Errorf("parseSince(%q): %v", c.in, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("parseSince(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestParseSinceRejectsUnanswerable covers the values whose only possible output
// is an empty window — the one result this change exists to make unambiguous.
// Silently accepting them would put a caller right back to reading empty as
// healthy.
func TestParseSinceRejectsUnanswerable(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"tomorrow",
		"last week",
		"0h",
		"-3h",
		"1.5d",   // not a whole number of days; guessing at it would be worse
		"7 days", // spaces are not a duration we parse
	} {
		if _, err := parseSince(in); err == nil {
			t.Errorf("parseSince(%q) should have failed", in)
		}
	}

	// A future timestamp is rejected for the same reason: it can only ever
	// match nothing.
	future := time.Now().Add(48 * time.Hour).Format("2006-01-02T15:04:05Z07:00")
	if _, err := parseSince(future); err == nil {
		t.Errorf("parseSince(%q) should reject a future bound", future)
	}
}
