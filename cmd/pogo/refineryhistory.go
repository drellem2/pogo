package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// sinceDateLayouts are the absolute forms accepted by parseSince, tried in
// order. A bare date is interpreted in the local zone, matching how the rest of
// the CLI formats timestamps for humans.
var sinceDateLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseSince resolves a --since value to an absolute lower bound.
//
// It accepts a relative duration ("90m", "24h", and "7d"/"30d" which
// time.ParseDuration rejects) or an absolute date/timestamp. The question
// --since answers is usually time-scoped in days — "did anything fail last
// week" — so refusing "7d" and making the caller write "168h" would push the
// arithmetic onto exactly the person least able to check it.
//
// The bound is always in the past: a future --since would match nothing and
// print an empty window, which is the one output this whole change exists to
// stop being ambiguous.
func parseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("--since: empty value")
	}

	if d, ok := parseSinceDuration(s); ok {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("--since: duration must be positive, got %q", s)
		}
		return time.Now().Add(-d), nil
	}

	for _, layout := range sinceDateLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			if t.After(time.Now()) {
				return time.Time{}, fmt.Errorf("--since: %q is in the future; it would match nothing", s)
			}
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("--since: %q is neither a duration (90m, 24h, 7d) nor a date (2026-07-01, 2026-07-01T12:00:00Z)", s)
}

// parseSinceDuration parses a Go duration, additionally accepting a whole
// number of days ("7d"). Reports false when s is not a duration at all, so the
// caller can try the date forms.
func parseSinceDuration(s string) (time.Duration, bool) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		// Only a bare integer count of days. "1.5d" and "1h30md" fall through
		// to ParseDuration (which rejects them) rather than being guessed at.
		if n, err := strconv.Atoi(days); err == nil {
			return time.Duration(n) * 24 * time.Hour, true
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, true
	}
	return 0, false
}
