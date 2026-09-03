package logliveness

import (
	"strings"
	"testing"
	"time"
)

const logPath = "/Users/x/Library/Logs/pogo/pogod.log"

func base() Observation {
	return Observation{
		LogPath:     logPath,
		DaemonPID:   6610,
		DaemonPIDOK: true,
		StderrPath:  logPath,
		StderrOK:    true,
		LogExists:   true,
		LogMtime:    time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC),
		LogSize:     8902369,
		Now:         time.Date(2026, 9, 3, 20, 1, 0, 0, time.UTC),
	}
}

func TestCheckVerdicts(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Observation)
		want Verdict
	}{
		{"stderr is the log", func(o *Observation) {}, Live},
		{"stderr is a tty — the 2026-09 state", func(o *Observation) { o.StderrPath = "/dev/ttys007" }, Detached},
		{"stderr is /dev/null", func(o *Observation) { o.StderrPath = "/dev/null" }, Detached},
		{"stderr is a different file", func(o *Observation) { o.StderrPath = "/tmp/other.log" }, Detached},
		{"named log absent", func(o *Observation) { o.LogExists = false }, Detached},
		{"no lock holder", func(o *Observation) { o.DaemonPIDOK = false }, Unknown},
		{"stderr unreadable", func(o *Observation) { o.StderrOK = false; o.StderrPath = "" }, Unknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := base()
			tc.mut(&obs)
			if got := Check(obs).Verdict; got != tc.want {
				t.Errorf("Check() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestUnknownIsNotAPass. The whole package exists because a check that goes
// green on an unmeasured input reproduces the defect it was built to catch. An
// unreadable descriptor must not arrive at a caller as the same value as "the
// log is live".
func TestUnknownIsNotAPass(t *testing.T) {
	obs := base()
	obs.StderrOK = false
	res := Check(obs)
	if res.OK() {
		t.Error("an unreadable stderr reports OK() — that is the green-because-unmeasured failure, one level up")
	}
	if res.Verdict == Live {
		t.Error("an unreadable stderr reads LIVE")
	}
}

// TestMtimeIsNotAVerdictInput is the load-bearing pin of this package.
//
// The obvious instrument — "is pogod.log fresh?" — read GREEN for thirteen
// hours on this box while the running daemon wrote nothing to it, because a
// launchd respawn loop was appending two lock-refusal lines every ten seconds
// (runs = 4639). So the verdict must be identical across every mtime, size and
// existence-age the file can have, holding the descriptor fixed. A future edit
// that lets recency contribute to Live reintroduces exactly that instrument,
// and this test is what stops it compiling into a pass.
func TestMtimeIsNotAVerdictInput(t *testing.T) {
	mtimes := []time.Time{
		time.Date(2026, 9, 3, 20, 0, 59, 0, time.UTC), // one second old
		time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC),  // a minute old
		time.Date(2026, 9, 2, 1, 5, 57, 0, time.UTC),  // the measured freeze
		time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),   // ancient
		{}, // unread
	}
	for _, detached := range []bool{false, true} {
		var want Verdict = Live
		var first string
		for _, mt := range mtimes {
			obs := base()
			obs.LogMtime = mt
			obs.LogSize = 0
			if detached {
				obs.StderrPath = "/dev/ttys007"
				want = Detached
			}
			res := Check(obs)
			if res.Verdict != want {
				t.Errorf("mtime %v flipped the verdict to %s (want %s) — mtime reached the judgement", mt, res.Verdict, want)
			}
			if first == "" {
				first = res.Reason
			} else if res.Reason != first {
				t.Errorf("mtime %v changed the REASON text; the reason must be about the descriptor, not the clock", mt)
			}
		}
	}
}

// TestTextSaysWhatTheContextDoesNotProve. The context readings are the ones
// that have already been misread on this box, so printing them without the
// disclaimer would hand a reader the exact instrument that failed.
func TestTextSaysWhatTheContextDoesNotProve(t *testing.T) {
	obs := base()
	obs.StderrPath = "/dev/ttys007"
	txt := Check(obs).Text()
	for _, want := range []string{
		"NOT A VERDICT INPUT",
		"does not mean the running daemon wrote it",
		"4,639",
		"/dev/ttys007",
		logPath,
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("Text() is missing %q:\n%s", want, txt)
		}
	}
}

// TestTextNamesTheStrongerNegativeWhenItHolds. "The daemon has never written to
// this file" is a different and more actionable statement than "the file is
// stale", and it is only true when the mtime predates the daemon's start. It
// must appear exactly when it holds and never otherwise — an unconditional
// version would be a claim the reading does not support.
func TestTextNamesTheStrongerNegativeWhenItHolds(t *testing.T) {
	const marker = "written nothing to this file for its entire life"

	never := base()
	never.StderrPath = "/dev/ttys007"
	never.DaemonStart, never.DaemonStartOK = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC), true
	never.LogMtime = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if r := Check(never); !r.MtimePredatesDaemon() || !strings.Contains(r.Text(), marker) {
		t.Errorf("an mtime older than the daemon's start does not report the stronger negative:\n%s", r.Text())
	}

	// The MEASURED 2026-09 shape: the daemon started Sep 1 12:07 and the file's
	// last write was Sep 2 01:05, by the respawn loop. Detached, but the
	// daemon did NOT predate it, and claiming otherwise would be false.
	measured := base()
	measured.StderrPath = "/dev/ttys007"
	measured.DaemonStart, measured.DaemonStartOK = time.Date(2026, 9, 1, 12, 7, 50, 0, time.UTC), true
	measured.LogMtime = time.Date(2026, 9, 2, 1, 5, 57, 0, time.UTC)
	r := Check(measured)
	if r.Verdict != Detached {
		t.Fatalf("the measured shape reads %s, want DETACHED", r.Verdict)
	}
	if r.MtimePredatesDaemon() || strings.Contains(r.Text(), marker) {
		t.Errorf("the measured shape claims the daemon predates the mtime; it does not — the respawn loop wrote after the daemon started:\n%s", r.Text())
	}

	// No start reading at all: the stronger claim is unsupported and must not
	// be made.
	noStart := base()
	noStart.StderrPath = "/dev/ttys007"
	noStart.LogMtime = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if r := Check(noStart); r.MtimePredatesDaemon() {
		t.Error("the stronger negative is claimed with no daemon start reading")
	}
}

// TestTextFlagsPlistDrift. A reader following mayor.md greps the path the
// LOADED job names. If the judged path and the job's path disagree, the verdict
// is about a file they are not going to read, and saying so is the difference
// between a check and a decoration.
func TestTextFlagsPlistDrift(t *testing.T) {
	obs := base()
	obs.JobLogPath = "/Users/x/Library/Logs/pogo/elsewhere.log"
	txt := Check(obs).Text()
	if !strings.Contains(txt, "DRIFT") || !strings.Contains(txt, "elsewhere.log") {
		t.Errorf("a job naming a different path is not flagged:\n%s", txt)
	}

	obs.JobLogPath = obs.LogPath
	if strings.Contains(Check(obs).Text(), "DRIFT") {
		t.Error("agreement between the job and the judged path is reported as drift")
	}
}
