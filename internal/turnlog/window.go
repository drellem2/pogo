package turnlog

// Counting completed turns inside an explicit time window (mg-7837).
//
// # Why Last is not enough
//
// LastIn answers "when did this agent most recently finish a turn", which is
// the right question for an ONGOING liveness read and the wrong one for a
// historical one. `pogo schedule list` renders `⚠ N unacked` and its own legend
// calls that "the one number in this column that does not saturate" — the
// number to act on. But an unacked fire has at least three readings and the
// counter cannot separate them:
//
//	the agent was not there                -> the fire went into silence
//	the agent was there and did the work   -> a reporting gap, nothing else
//	the agent was there and missed the fire -> the only causal reading
//
// On 2026-08-19 the mayor read `⚠ 6 unacked` on its own predeploy-quiesce and
// filed mg-7837 rather than a finding, explicitly because it could not choose
// between those three from the column. The discriminator it named — and the one
// that settled it — was whether the agent completed any turn around the fires.
// Every one of the six landed in the 2026-08-14T08:23Z..2026-08-19T06:52Z
// window in which all seven crew turnlogs are EMPTY; the mayor was not skipping
// the quiesce, it did not exist. That is a question about a past interval, so it
// needs an interval reader.
//
// # Whole file, not the tail
//
// LastIn reads the last few KB because it only ever wants the final line. A
// window can sit anywhere in the history, so this reads the file from the
// start. That is affordable at CLI cadence — the fleet's largest turnlog is
// ~400KB after a week — and it is deliberately NOT wired into anything running
// on a daemon clock.
//
// # An unreadable window is not an empty one
//
// The whole point is to stop a number reading the same in two different worlds,
// so this must not do the same thing itself. A missing turnlog, an unreadable
// one, and one that genuinely records zero turns in the interval are three
// states, and only the third is evidence of anything. A missing file returns an
// error satisfying errors.Is(err, os.ErrNotExist) — callers must render that as
// UNKNOWN and never as absence. Polecats write no turnlog at all, so "no file"
// is the common case rather than the exotic one.

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// Window counts the completed turns recorded for agent in the closed interval
// [from, to], under the live Dir().
func Window(agent string, from, to time.Time) (int, error) {
	return WindowIn(Dir(), agent, from, to)
}

// WindowIn is Window against an explicit root. See PathIn.
//
// The bounds are inclusive at both ends and compared as instants, so a caller
// may pass either zone: the turnlog stores UTC and the scheduler stores local
// offsets, and time.Time comparison is offset-independent. That mismatch is
// exactly the sort of thing that reads right and is wrong, so
// TestWindowIn_ComparesInstantsNotWallClock pins it.
//
// A `to` before `from` is an empty interval and returns 0, not an error: it is
// what a caller computing `to` from a clock and `from` from a stored timestamp
// gets when the two disagree, and zero turns in a zero-length window is the
// honest answer.
func WindowIn(root, agent string, from, to time.Time) (int, error) {
	if strings.TrimSpace(agent) == "" {
		return 0, fmt.Errorf("turnlog: no agent name")
	}
	path := PathIn(root, agent)
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n := 0
	seen := false
	var lastErr error
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		e, err := ParseLine(line)
		if err != nil {
			lastErr = err
			continue
		}
		seen = true
		if e.At.Before(from) || e.At.After(to) {
			continue
		}
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("turnlog: read %s: %w", path, err)
	}
	// A file with no parseable line at all has measured nothing. Reporting 0
	// would make "this agent completed no turns" indistinguishable from "this
	// instrument could not read its own artifact" — the failure this package
	// exists to end.
	if !seen {
		if lastErr != nil {
			return 0, lastErr
		}
		return 0, fmt.Errorf("turnlog: %s has no entries: %w", path, os.ErrNotExist)
	}
	return n, nil
}
