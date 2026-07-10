package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// logState is a comparable snapshot of a log file's observable state. Absent
// files compare equal to each other and unequal to any written file, so a
// before/after == comparison reliably detects an append (or a create).
type logState struct {
	exists bool
	size   int64
	mtime  time.Time
}

func statLog(t *testing.T, path string) logState {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return logState{}
		}
		t.Fatalf("stat %s: %v", path, err)
	}
	return logState{exists: true, size: info.Size(), mtime: info.ModTime()}
}

// TestSchedulerEventsStayInItsOwnRoot is the mg-e06d regression guard.
//
// Before the fix, emitSchedulerRemovalEvent (and its siblings) called
// events.Emit, which resolves events.log GLOBALLY (to $POGO_HOME/events.log)
// regardless of the root the caller handed the scheduler. So `go test
// ./internal/scheduler` appended real schedule_removed records — cat-dead,
// cat-alsodead — to the operator's live ~/.pogo/events.log, polluting the
// audit log's aggregates for three weeks. The fix threads an explicit root
// (s.logPath, a sibling of the store) so a temp-rooted scheduler writes ONLY to
// its own root.
//
// The test has a POSITIVE CONTROL by design: it first proves the size/mtime and
// record checks can actually observe a write (a scheduler DOES write to its own
// root), so the negative assertion below is not vacuous. An assertion that
// would pass even without the fix is a check that cannot fail.
func TestSchedulerEventsStayInItsOwnRoot(t *testing.T) {
	now := fixedTime()

	// --- POSITIVE CONTROL --------------------------------------------------
	// A scheduler rooted at its own temp dir must write its removal event to
	// THAT root's events.log. If this half ever stops observing a write, the
	// negative half below is meaningless — so we assert the write is seen.
	sA := newSchedulerForTest(t, nil)
	ownLog := sA.logPath
	before := statLog(t, ownLog)
	addMailCheck(t, sA, "cat-dead", now)
	sA.SetLiveness(fakeLiveness{alive: map[string]bool{}})
	if n := sA.GCStaleMailChecks(now); n != 1 {
		t.Fatalf("positive control: reaped %d, want 1", n)
	}
	after := statLog(t, ownLog)
	if before == after {
		t.Fatalf("positive control FAILED: reap wrote nothing observable to the scheduler's own log %s; "+
			"the negative assertion below would be vacuous", ownLog)
	}
	if ev := findScheduleRemoved(t, ownLog, MailCheckIDPrefix+"cat-dead", "agent_gone"); ev == nil {
		t.Fatalf("positive control: agent_gone record for cat-dead not found in the scheduler's own log %s", ownLog)
	}

	// --- NEGATIVE (the actual regression guard) ----------------------------
	// Stand in for the operator's live ~/.pogo/events.log with a sentinel, and
	// install it as the GLOBAL Emit path — exactly the path the pre-fix emitter
	// resolved to. (We use the global override rather than the real file so the
	// test can't be corrupted by, or corrupt, a live pogod writing concurrently
	// on this machine.) A scheduler rooted ELSEWHERE must never touch it.
	sentinel := filepath.Join(t.TempDir(), "live-events.log")
	events.SetLogPathForTesting(sentinel)
	t.Cleanup(func() { events.SetLogPathForTesting("") })
	beforeSentinel := statLog(t, sentinel)

	sB := newSchedulerForTest(t, nil) // rooted at a different temp dir than the sentinel
	if sB.logPath == sentinel {
		t.Fatalf("test setup bug: scheduler root collides with the sentinel log %s", sentinel)
	}
	addMailCheck(t, sB, "cat-alsodead", now)
	sB.SetLiveness(fakeLiveness{alive: map[string]bool{}})
	if n := sB.GCStaleMailChecks(now); n != 1 {
		t.Fatalf("negative: reaped %d, want 1", n)
	}

	afterSentinel := statLog(t, sentinel)
	if beforeSentinel != afterSentinel {
		t.Fatalf("REGRESSION (mg-e06d): a temp-rooted scheduler wrote to the global events.log %s "+
			"(state %v -> %v). The emitter is resolving events.log globally instead of using its own root.",
			sentinel, beforeSentinel, afterSentinel)
	}
	// And confirm the event went to sB's own root instead — the write happened,
	// it just landed in the right place.
	if ev := findScheduleRemoved(t, sB.logPath, MailCheckIDPrefix+"cat-alsodead", "agent_gone"); ev == nil {
		t.Fatalf("negative: agent_gone record for cat-alsodead not found in the scheduler's own log %s", sB.logPath)
	}
}
