package turnlog

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// The reading that produced this file. On 2026-08-19 `pogo schedule list`
// showed the mayor `predeploy-quiesce-mayor ... ⚠ 6 unacked` on the schedule
// that guards the deploy drain, and mg-2def had measured 4 of 4 deploy failures
// dying at or before that drain. The marker cannot say whether the quiesce was
// skipped, done-and-unreported, or delivered into a fleet that did not exist.
// The turnlogs answer it: every crew agent's file is empty across
// 2026-08-14T08:23Z..2026-08-19T06:52Z. That is a question about a past
// interval, and LastIn — which reads only the file's tail for the newest line —
// cannot ask it.

func writeTurns(t *testing.T, root, agent string, at ...time.Time) {
	t.Helper()
	for _, a := range at {
		if err := AppendIn(root, agent, "", a); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWindowIn_CountsOnlyTurnsInsideTheInterval(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 8, 19, 1, 30, 0, 0, time.UTC)
	writeTurns(t, root, "mayor",
		base.Add(-time.Hour),              // before
		base,                              // on the lower bound — inclusive
		base.Add(time.Hour),               // inside
		base.Add(3*time.Hour),             // on the upper bound — inclusive
		base.Add(3*time.Hour+time.Second), // after
	)
	n, err := WindowIn(root, "mayor", base, base.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("WindowIn = %d, want 3 (both bounds inclusive)", n)
	}
}

func TestWindowIn_ComparesInstantsNotWallClock(t *testing.T) {
	// The scheduler persists PendingSince with a local offset
	// ("2026-08-19T02:30:10.890845+01:00") and the turnlog stores UTC. A reader
	// that compared the wall-clock digits would be off by the host's offset,
	// which is the kind of defect that reads right in every review and is wrong
	// on every machine that is not on UTC.
	root := t.TempDir()
	writeTurns(t, root, "mayor", time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC))

	plusOne := time.FixedZone("BST", 3600)
	// 02:30+01:00 == 01:30Z, so a window of [01:30Z, 04:30Z] expressed in the
	// offset zone must still contain the 02:00Z turn.
	from := time.Date(2026, 8, 19, 2, 30, 0, 0, plusOne)
	n, err := WindowIn(root, "mayor", from, from.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("WindowIn = %d, want 1 — the window was compared as wall clock, not as an instant", n)
	}
}

func TestWindowIn_MissingTurnlogIsNotZeroTurns(t *testing.T) {
	// The whole point of the caller is to stop one number meaning two things,
	// so this must not do it either. A polecat writes no turnlog at all; "no
	// file" answering 0 would let a caller announce silence over an agent that
	// was never instrumented.
	root := t.TempDir()
	n, err := WindowIn(root, "never-wrote-one", time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatalf("a missing turnlog returned %d and no error", n)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want one satisfying errors.Is(err, os.ErrNotExist) so callers can render UNKNOWN", err)
	}
}

func TestWindowIn_EmptyFileIsNotZeroTurns(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PathIn(root, "wedged"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := WindowIn(root, "wedged", time.Now().Add(-time.Hour), time.Now()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an existing-but-empty turnlog must read as unmeasured, got %v", err)
	}
}

func TestWindowIn_UnparseableFileIsAnError_NotACleanZero(t *testing.T) {
	// An instrument that cannot read its own artifact has measured nothing.
	// Returning 0 here would be the "looked and saw nothing" reading of a
	// "could not look" state — the collapse this package exists to end.
	root := t.TempDir()
	if err := os.WriteFile(PathIn(root, "garbled"), []byte("not a turn line\nnor this one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := WindowIn(root, "garbled", time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("an unparseable turnlog read as a clean zero")
	}
	if !strings.Contains(err.Error(), "turnlog:") {
		t.Errorf("err = %v, want a turnlog-attributed error", err)
	}
}

func TestWindowIn_ReadsPastTheTailLastInWouldStopAt(t *testing.T) {
	// LastIn reads only the final tailBytes because it only ever wants the last
	// line. The mayor's question was about six fires spread over five days, in a
	// file that had since grown well past that — a window reader built on the
	// same tail read would have answered zero for a reason that has nothing to
	// do with the agent.
	root := t.TempDir()
	old := time.Date(2026, 8, 14, 0, 47, 0, 0, time.UTC)
	writeTurns(t, root, "mayor", old)
	// Push the interesting line far outside the tail window.
	filler := make([]time.Time, 0, 400)
	for i := 0; i < 400; i++ {
		filler = append(filler, old.Add(time.Duration(i+1)*time.Minute))
	}
	writeTurns(t, root, "mayor", filler...)
	if fi, err := os.Stat(PathIn(root, "mayor")); err != nil {
		t.Fatal(err)
	} else if fi.Size() <= tailBytes {
		t.Fatalf("fixture is only %d bytes, which does not exceed the %d-byte tail read", fi.Size(), tailBytes)
	}

	n, err := WindowIn(root, "mayor", old, old.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("WindowIn = %d, want 2 — the reader did not see past the tail", n)
	}
}
