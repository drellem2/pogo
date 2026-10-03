package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/wakewatch"
)

// config's [wake_watch] defaults and wakewatch's own zero-value fallbacks are
// two spellings of the design's starting values (mg-5496); config cannot import
// wakewatch (wakewatch -> events -> config), so this pins them equal.
func TestWakeWatchConfigDefaultsMatchPackageDefaults(t *testing.T) {
	got := wakeWatchParams(config.WakeWatchConfig{
		Coalesce:         config.DefaultWakeWatchCoalesce,
		RecoveryInterval: config.DefaultWakeWatchRecoveryInterval,
		RenudgeAfter:     config.DefaultWakeWatchRenudgeAfter,
		RenudgeEvery:     config.DefaultWakeWatchRenudgeEvery,
		MaxRenudges:      config.DefaultWakeWatchMaxRenudges,
		Lookback:         config.DefaultWakeWatchLookback,
	})
	want := wakewatch.DefaultParams()
	// Not configurable: Fresh and PollInterval, and the failed-pointer retry
	// backoff and budget (mg-35a7e).
	got.Fresh, got.PollInterval = want.Fresh, want.PollInterval
	got.RetryAfter, got.MaxRetries = want.RetryAfter, want.MaxRetries
	if got != want {
		t.Fatalf("config defaults %+v != wakewatch defaults %+v", got, want)
	}
}

// The assignee of a work.created comes from the item's frontmatter, because
// today's macguffin does not put it on the event.
func TestWakeWatchItemReadsAssigneeAndTitle(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "claimed")
	os.MkdirAll(dir, 0o755)
	body := "---\nid: mg-1234\ntype: task\nassignee: blocked:pm-pogo\n---\n\n# Fix the parser\n\nbody\n"
	os.WriteFile(filepath.Join(dir, "mg-1234.md.4242"), []byte(body), 0o644)
	it, ok := wakeWatchItem(root)("mg-1234")
	if !ok || it.Assignee != "blocked:pm-pogo" || it.Title != "Fix the parser" {
		t.Fatalf("item = %+v ok=%v", it, ok)
	}
	if _, ok := wakeWatchItem(root)("mg-9999"); ok {
		t.Error("absent item reported found")
	}
}

func TestWakeWatchNudgeWithNoRegistryFails(t *testing.T) {
	out, err := wakeWatchNudge(nil)("pe00c", "x")
	if err == nil || out != wakewatch.OutcomeFailed {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// The armed banner is the first thing an operator reads to decide whether the
// waker is live. It said "SHADOW" and "mail-check timers stay ON" long after
// both stopped being true (mg-e7f4a); this pins the whole line so neither can
// return without a test edit.
func TestWakeWatchArmedBanner(t *testing.T) {
	got := wakeWatchArmedBanner(wakewatch.Params{
		Coalesce:     30 * time.Second,
		RenudgeAfter: 15 * time.Minute,
		RenudgeEvery: 10 * time.Minute,
		MaxRenudges:  2,
	}, "mayor")
	want := "pogod: wake-watch armed — enabled by [wake_watch] (mg-e00c); pointers <=100 bytes on mail/assignment arrival, " +
		"coalesce=30s, re-nudge after 15m0s every 10m0s up to 2 then mail mayor; bounces to non-running recipients"
	if got != want {
		t.Fatalf("banner:\n got %q\nwant %q", got, want)
	}
	for _, stale := range []string{"SHADOW", "shadow", "timers stay", "mail-check timer"} {
		if strings.Contains(got, stale) {
			t.Errorf("banner contains stale phrase %q: %q", stale, got)
		}
	}
}
