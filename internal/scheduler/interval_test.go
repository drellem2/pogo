package scheduler

import (
	"testing"
	"time"
)

func TestEntryCronInterval(t *testing.T) {
	ref := time.Date(2026, 5, 3, 12, 1, 0, 0, time.UTC)
	cases := []struct {
		name  string
		entry Entry
		want  time.Duration
	}{
		{
			name:  "every 5 minutes",
			entry: Entry{Cron: "*/5 * * * *"},
			want:  5 * time.Minute,
		},
		{
			name:  "every 30 minutes",
			entry: Entry{Cron: "*/30 * * * *"},
			want:  30 * time.Minute,
		},
		{
			name:  "hourly",
			entry: Entry{Cron: "7 * * * *"},
			want:  time.Hour,
		},
		{
			name:  "daily",
			entry: Entry{Cron: "0 9 * * *"},
			want:  24 * time.Hour,
		},
		{
			name:  "one-shot has no interval",
			entry: Entry{OneShot: true, NextFire: ref.Add(time.Minute)},
			want:  0,
		},
		{
			name:  "empty cron has no interval",
			entry: Entry{},
			want:  0,
		},
		{
			name:  "unparseable cron has no interval",
			entry: Entry{Cron: "not a cron"},
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.CronInterval(ref); got != tc.want {
				t.Errorf("CronInterval = %v, want %v", got, tc.want)
			}
		})
	}
}
