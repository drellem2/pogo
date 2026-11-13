package stallwatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// persistentEnv is testEnv with a blocked-reminder state file, plus a restart
// function that builds a fresh Watcher over the same roots and state file — the
// shape of a pogod restart (drellem2/pogo#300).
func persistentEnv(t *testing.T, cfg config.StallWatchConfig) (w *Watcher, rec *recorder, workRoot, mailRoot, statePath string, restart func() *Watcher) {
	t.Helper()
	root := t.TempDir()
	workRoot = filepath.Join(root, "work")
	mailRoot = filepath.Join(root, "mail")
	statePath = BlockedStatePath(filepath.Join(root, "pogo"))
	for _, d := range []string{"available", "claimed", "done"} {
		if err := os.MkdirAll(filepath.Join(workRoot, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rec = &recorder{}
	restart = func() *Watcher {
		return New(cfg, Options{
			WorkRoot:         workRoot,
			MailRoot:         mailRoot,
			Nudge:            rec.nudge,
			Emit:             rec.emit,
			BlockedStatePath: statePath,
		})
	}
	return restart(), rec, workRoot, mailRoot, statePath, restart
}

// TestBlockedReminderReBlockStaysUnderTheCap is drellem2/pogo#300 verbatim: an
// item flipped out of `blocked:` and back every hour for 40 hours used to read
// as new on every re-block and start the four notices over. Keyed on (item,
// blocked-on) with a retention window, the whole run draws at most max_notices.
func TestBlockedReminderReBlockStaysUnderTheCap(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	start := time.Now()
	for h := 0; h < 40; h++ {
		now := start.Add(time.Duration(h) * time.Hour)
		writeItem(t, workRoot, "mg-flap", "blocked:pm-pogo", now)
		w.Check(now)
		// Unblocked for a few minutes: reassigned, then blocked again.
		writeItem(t, workRoot, "mg-flap", "mayor", now)
		w.Check(now.Add(5 * time.Minute))
	}
	max := config.DefaultBlockedReminderMaxNotices
	if got := len(blockedNudges(rec)); got > max {
		t.Fatalf("re-blocking drew %d notices, want at most max_notices=%d", got, max)
	}
	if got := len(blockedNudges(rec)); got == 0 {
		t.Fatal("no notice at all — the positive control failed")
	}
}

// TestBlockedReminderReBlockResumesBackoff: a re-block on the same agent a minute
// after the first notice is inside that notice's cooldown, so it draws nothing —
// no fresh "first notice".
func TestBlockedReminderReBlockResumesBackoff(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now)
	writeItem(t, workRoot, "mg-a", "mayor", now)
	w.Check(now.Add(time.Minute))
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now.Add(2 * time.Minute))
	if got := len(blockedNudges(rec)); got != 1 {
		t.Fatalf("got %d notices, want 1: a re-block inside the cooldown is not a new block", got)
	}

	// Once the cooldown passes, the next notice is numbered as a repeat.
	w.Check(now.Add(2 * time.Hour))
	got := blockedNudges(rec)
	if len(got) != 2 || !strings.Contains(got[1].message, "notice #2") {
		t.Fatalf("want a second notice marked as #2, got %+v", got)
	}
}

// TestBlockedReminderDifferentBlockerStartsFresh: a re-block on a different agent
// is a different key. That agent has never been told, so it gets a first notice
// at once even though the item is capped against the first agent.
func TestBlockedReminderDifferentBlockerStartsFresh(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	makeMailbox(t, mailRoot, "architect")
	now := time.Now()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	for i := 0; i < 12; i++ {
		w.Check(now.Add(time.Duration(i) * 4 * time.Hour))
	}
	max := config.DefaultBlockedReminderMaxNotices
	if got := len(blockedNudges(rec)); got != max {
		t.Fatalf("setup: got %d notices to pm-pogo, want the cap %d", got, max)
	}

	later := now.Add(48*time.Hour + time.Minute)
	writeItem(t, workRoot, "mg-a", "blocked:architect", later)
	w.Check(later)
	got := blockedNudges(rec)
	if len(got) != max+1 || got[max].agent != "architect" {
		t.Fatalf("a re-block on a different agent drew %+v, want a fresh notice to architect", got)
	}
	if strings.Contains(got[max].message, "[repeat]") {
		t.Errorf("architect's first notice is marked as a repeat: %q", got[max].message)
	}
}

// TestBlockedReminderCountExpiresAfterTTL: once the block has been clear for the
// retention window, a re-block on the same agent is a new block again.
func TestBlockedReminderCountExpiresAfterTTL(t *testing.T) {
	cfg := blockedCfg()
	cfg.BlockedReminderCountTTL = 2 * time.Hour
	w, rec, workRoot, mailRoot := testEnv(t, cfg)
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now)
	writeItem(t, workRoot, "mg-a", "mayor", now)
	w.Check(now.Add(time.Minute))                 // retired here
	w.Check(now.Add(2*time.Hour + 2*time.Minute)) // past the TTL: dropped
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now.Add(2*time.Hour + 3*time.Minute))
	got := blockedNudges(rec)
	if len(got) != 2 || strings.Contains(got[1].message, "[repeat]") {
		t.Fatalf("after the TTL a re-block should draw a fresh first notice, got %+v", got)
	}
}

// TestBlockedReminderWaiverToggleDoesNotResetTheCap updates the gh#161 rule that
// leaving the candidate set forgets the count: a notified item that is waived and
// then un-waived resumes its count rather than starting over.
func TestBlockedReminderWaiverToggleDoesNotResetTheCap(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now)
	writeWaiverItem(t, workRoot, "mg-a", "blocked:pm-pogo", []string{"waiting-on-purpose"}, now)
	w.Check(now.Add(time.Minute))
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now.Add(2 * time.Minute))
	if got := len(blockedNudges(rec)); got != 1 {
		t.Fatalf("waive + un-waive drew %d notices, want 1 (the count resumes)", got)
	}
}

// TestBlockedReminderCountsSurviveRestart: the nightly deploy restarts pogod, and
// before #300 every restart restarted every cap. A fresh Watcher over the same
// state file resumes the counts.
func TestBlockedReminderCountsSurviveRestart(t *testing.T) {
	w, rec, workRoot, mailRoot, statePath, restart := persistentEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file not written: %v", err)
	}

	w = restart()
	w.Check(now.Add(time.Minute))
	if got := len(blockedNudges(rec)); got != 1 {
		t.Fatalf("after a restart: %d notices, want 1 — the restart forgot the count", got)
	}

	// Drive to the cap, restarting before every tick: still capped.
	for i := 1; i < 20; i++ {
		w = restart()
		w.Check(now.Add(time.Duration(i) * 4 * time.Hour))
	}
	if got, max := len(blockedNudges(rec)), config.DefaultBlockedReminderMaxNotices; got != max {
		t.Fatalf("restarting before every tick drew %d notices, want the cap %d", got, max)
	}
}

// TestBlockedReminderRetiredCountSurvivesRestart: a record retired (block
// cleared) before a restart is still resumed by a re-block after it.
func TestBlockedReminderRetiredCountSurvivesRestart(t *testing.T) {
	w, rec, workRoot, mailRoot, _, restart := persistentEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now)
	writeItem(t, workRoot, "mg-a", "mayor", now)
	w.Check(now.Add(time.Minute))

	w = restart()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now.Add(2 * time.Minute))
	if got := len(blockedNudges(rec)); got != 1 {
		t.Fatalf("re-block after a restart drew %d notices, want 1", got)
	}
}

// TestBlockedReminderBadStateFileFailsOpen: a corrupt or wrong-version store must
// not crash the watcher or silence it — it starts with no counts and notifies.
func TestBlockedReminderBadStateFileFailsOpen(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":       "{not json",
		"wrong version": `{"version": 99, "records": [{"item": "mg-a", "blocked_on": "pm-pogo", "count": 9, "last": "2099-01-01T00:00:00Z"}]}`,
		"empty":         "",
	} {
		t.Run(name, func(t *testing.T) {
			_, rec, workRoot, mailRoot, statePath, restart := persistentEnv(t, blockedCfg())
			if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			w := restart()
			makeMailbox(t, mailRoot, "pm-pogo")
			now := time.Now()
			writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
			w.Check(now)
			if got := len(blockedNudges(rec)); got != 1 {
				t.Fatalf("bad state file: %d notices, want 1 (fail open)", got)
			}
		})
	}
}

// TestBlockedReminderDropsCountWhenItemCloses: a closed item's record goes at
// once, without waiting out the TTL.
func TestBlockedReminderDropsCountWhenItemCloses(t *testing.T) {
	w, _, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now)
	// Positive control: unblocked but still open, the record is retained.
	writeItem(t, workRoot, "mg-a", "mayor", now)
	w.Check(now.Add(30 * time.Second))
	w.mu.Lock()
	kept := len(w.blocked)
	w.mu.Unlock()
	if kept != 1 {
		t.Fatalf("an unblocked open item holds %d records, want 1 (retained for the TTL)", kept)
	}
	removeItem(t, workRoot, "available", "mg-a")
	writeItemIn(t, workRoot, "done", "mg-a", "pm-pogo", "", now)
	w.Check(now.Add(time.Minute))
	w.mu.Lock()
	n := len(w.blocked)
	w.mu.Unlock()
	if n != 0 {
		t.Fatalf("closed item still holds %d blocked-reminder record(s)", n)
	}
}

// TestBlockedReminderRetiredCountExpiresAcrossDowntime is the mg-78e34 follow-up
// to #300's review: a record retired before pogod stopped, with pogod down for
// longer than the TTL and the item re-blocked meanwhile, reached the first tick
// after restart as a LIVE pair. The prune only visits pairs that are not live,
// so the stale record was resumed — cooldown and count included — instead of
// starting fresh. No tick ran while the block was clear past the window.
func TestBlockedReminderRetiredCountExpiresAcrossDowntime(t *testing.T) {
	cfg := blockedCfg()
	cfg.BlockedReminderCountTTL = 2 * time.Hour
	w, rec, workRoot, mailRoot, _, restart := persistentEnv(t, cfg)
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now)
	writeItem(t, workRoot, "mg-a", "mayor", now)
	w.Check(now.Add(time.Minute)) // retired, then pogod stops

	// Down for longer than the TTL; re-blocked while down.
	w = restart()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now.Add(3 * time.Hour))
	got := blockedNudges(rec)
	if len(got) != 2 {
		t.Fatalf("re-block after >TTL downtime drew %d notices total, want 2 (a fresh first notice)", len(got))
	}
	if strings.Contains(got[1].message, "[repeat]") {
		t.Errorf("the post-downtime notice is marked as a repeat: %q", got[1].message)
	}

	// Positive control: the same downtime INSIDE the window still resumes.
	cfg2 := blockedCfg()
	cfg2.BlockedReminderCountTTL = 6 * time.Hour
	w2, rec2, workRoot2, mailRoot2, _, restart2 := persistentEnv(t, cfg2)
	makeMailbox(t, mailRoot2, "pm-pogo")
	writeItem(t, workRoot2, "mg-a", "blocked:pm-pogo", now)
	w2.Check(now)
	writeItem(t, workRoot2, "mg-a", "mayor", now)
	w2.Check(now.Add(time.Minute))
	w2 = restart2()
	writeItem(t, workRoot2, "mg-a", "blocked:pm-pogo", now)
	w2.Check(now.Add(3 * time.Hour))
	got2 := blockedNudges(rec2)
	if len(got2) != 2 || !strings.Contains(got2[1].message, "[repeat]") {
		t.Fatalf("inside the TTL the re-block should resume (second notice a repeat), got %+v", got2)
	}
}

// TestBlockedReminderCountsSurvivePauseResume pins a decision, not an accident
// (mg-78e34): unlike lastNudge, the blocked-reminder's counts are NOT forgotten
// when index-only mode lifts. Those notices go to the blocked agents, not to the
// coordinator a resume restarts, and a cap reset by every pause-resume is the
// per-period cap #300 removed. See pausedNow.
func TestBlockedReminderCountsSurvivePauseResume(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	paused := false
	w.paused = func() bool { return paused }
	now := time.Now()
	writeItem(t, workRoot, "mg-a", "blocked:pm-pogo", now)
	w.Check(now)
	if got := len(blockedNudges(rec)); got != 1 {
		t.Fatalf("positive control: %d notices before the pause, want 1", got)
	}
	paused = true
	w.Check(now.Add(time.Minute))
	paused = false
	w.Check(now.Add(2 * time.Minute)) // inside the first notice's cooldown
	if got := len(blockedNudges(rec)); got != 1 {
		t.Fatalf("pause-resume drew %d notices, want 1 — the resume reset the count", got)
	}
	w.mu.Lock()
	n := len(w.blocked)
	w.mu.Unlock()
	if n != 1 {
		t.Fatalf("after pause-resume %d blocked records, want 1", n)
	}
}
