package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/ghwatch"
)

type condRec struct {
	raised  map[string]pogodCondition
	cleared map[string]int
}

func newCondRec() *condRec {
	return &condRec{raised: map[string]pogodCondition{}, cleared: map[string]int{}}
}

func (c *condRec) Raise(p pogodCondition, now time.Time) { c.raised[p.ID] = p }
func (c *condRec) Clear(id string, now time.Time) {
	delete(c.raised, id)
	c.cleared[id]++
}

func witnessWith(f ghwatch.File, err error) *ghWatchWitness {
	return &ghWatchWitness{
		home:            "/h",
		teardownEnabled: true, intakeEnabled: true, driftEnabled: true,
		teardownTo: "pm", intakeTo: "coord", coordinator: "coord",
		read: func(string) (ghwatch.File, error) { return f, err },
	}
}

// tickFor drives the witness on a 30s heartbeat for d, as pogod does.
func tickFor(w *ghWatchWitness, c conditionSink, start time.Time, d time.Duration) time.Time {
	now := start
	for ; !now.After(start.Add(d)); now = now.Add(30 * time.Second) {
		w.Check(c, now)
	}
	return now
}

// A job that never wrote a record is NOT a clean one — but it is judged only
// after pogod has been awake for the whole window, so a boot (or a wake) does
// not raise it before launchd has had a chance to fire the job.
func TestWitnessRaisesNotReportingOnlyAfterAFullAwakeWindow(t *testing.T) {
	w := witnessWith(ghwatch.File{}, fmt.Errorf("open: %w", os.ErrNotExist))
	c := newCondRec()
	t0 := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	now := tickFor(w, c, t0, 50*time.Minute)
	if _, ok := c.raised[rowA13GHWatchNotReporting]; ok {
		t.Fatal("raised not-reporting 50m after boot — before the job had an hour of awake time")
	}
	tickFor(w, c, now, 15*time.Minute)
	got, ok := c.raised[rowA13GHWatchNotReporting]
	if !ok {
		t.Fatal("a job with no record after >1h awake was not annunciated — every gh detector is dark and nothing says so")
	}
	if got.To != "coord" || !strings.Contains(got.Detail, "never completed") {
		t.Errorf("not-reporting condition = to %q detail %q", got.To, got.Detail)
	}
}

// A sleep resets the awake window: the first check after a wake must not see
// the pre-sleep record as stale, because launchd has not fired the job yet.
func TestWitnessDoesNotAlarmOnWake(t *testing.T) {
	t0 := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	w := witnessWith(ghwatch.File{SchemaVersion: 1, FinishedAt: t0}, nil)
	c := newCondRec()
	tickFor(w, c, t0, 10*time.Minute)
	// The host sleeps 8h; pogod's heartbeat resumes on wake.
	wake := t0.Add(8 * time.Hour)
	tickFor(w, c, wake, 20*time.Minute)
	if _, ok := c.raised[rowA13GHWatchNotReporting]; ok {
		t.Fatal("raised not-reporting within minutes of a wake — a false alarm on every sleep")
	}
	// Control: still no fresh record an hour into the awake period IS the fault.
	tickFor(w, c, wake.Add(20*time.Minute), 45*time.Minute)
	if _, ok := c.raised[rowA13GHWatchNotReporting]; !ok {
		t.Fatal("control: a stale record after a full awake hour was not annunciated")
	}
}

func TestWitnessClearsOnAFreshRecord(t *testing.T) {
	t0 := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	f := ghwatch.File{SchemaVersion: 1, FinishedAt: t0,
		Teardown: ghwatch.Detector{Arming: ghwatch.Armed},
		Intake:   ghwatch.Detector{Arming: ghwatch.Armed}}
	w := witnessWith(f, nil)
	c := newCondRec()
	c.raised[rowA13GHWatchNotReporting] = pogodCondition{}
	w.Check(c, t0.Add(time.Minute))
	if len(c.raised) != 0 {
		t.Errorf("fresh armed record left conditions raised: %v", c.raised)
	}
}

// The arming conditions come from the job's record, routed as A13 always was:
// teardown to its own box, intake to its own, and exactly one intake condition
// live at once.
func TestWitnessRaisesArmingConditionsFromTheRecord(t *testing.T) {
	t0 := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		td, in    ghwatch.Arming
		wantRaise []string
	}{
		{"no gh", ghwatch.NoGHBinary, ghwatch.NoGHBinary, []string{rowA13TeardownNotArmed, rowA13IntakeNotArmed}},
		{"no credential", ghwatch.Armed, ghwatch.NoCredential, []string{rowA13IntakeNoCred}},
		{"armed", ghwatch.Armed, ghwatch.Armed, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ghwatch.File{SchemaVersion: 1, FinishedAt: t0,
				Teardown: ghwatch.Detector{Arming: tc.td, ArmingDetail: "d"},
				Intake:   ghwatch.Detector{Arming: tc.in, ArmingDetail: "d"}}
			c := newCondRec()
			// A stale condition from the other intake arm must be cleared.
			c.raised[rowA13IntakeNoCred] = pogodCondition{}
			c.raised[rowA13IntakeNotArmed] = pogodCondition{}
			witnessWith(f, nil).Check(c, t0.Add(time.Minute))
			if len(c.raised) != len(tc.wantRaise) {
				t.Errorf("raised %v, want %v", keys(c.raised), tc.wantRaise)
			}
			for _, id := range tc.wantRaise {
				p, ok := c.raised[id]
				if !ok {
					t.Errorf("%s not raised", id)
					continue
				}
				wantTo := "coord"
				if id == rowA13TeardownNotArmed {
					wantTo = "pm"
				}
				if p.To != wantTo {
					t.Errorf("%s routed to %q, want %q", id, p.To, wantTo)
				}
			}
		})
	}
}

// An unreadable record says nothing about arming: leave those conditions be.
func TestWitnessLeavesArmingAloneWhenTheRecordIsUnreadable(t *testing.T) {
	w := witnessWith(ghwatch.File{}, errors.New("parsing: bad json"))
	c := newCondRec()
	c.raised[rowA13IntakeNoCred] = pogodCondition{ID: rowA13IntakeNoCred}
	w.Check(c, time.Now())
	if _, ok := c.raised[rowA13IntakeNoCred]; !ok || c.cleared[rowA13IntakeNoCred] != 0 {
		t.Error("an unreadable record cleared an arming condition it had no evidence about")
	}
}

func TestWitnessWithEveryDetectorDisabledClearsAndReadsNothing(t *testing.T) {
	w := &ghWatchWitness{read: func(string) (ghwatch.File, error) {
		t.Fatal("read the record although no detector is enabled")
		return ghwatch.File{}, nil
	}}
	c := newCondRec()
	c.raised[rowA13GHWatchNotReporting] = pogodCondition{}
	w.Check(c, time.Now())
	if len(c.raised) != 0 {
		t.Errorf("left %v raised with every detector disabled", keys(c.raised))
	}
}

// pogod must no longer run the gh-issue watchers, and must drive the witness.
// main() is not callable from a test, so this is asserted against the source.
func TestMainRunsNoGHWatcherAndDrivesTheWitness(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, gone := range []string{
		"ghintake.New(", "ghteardown.New(", "carrierdrift.New(",
		`exec.LookPath("gh")`, "GHOpenIssues", "GHLookup", "GHSnapshot",
	} {
		if strings.Contains(body, gone) {
			t.Errorf("main.go still contains %q — the gh-issue watchers run in `pogo gh-watch` now (mg-257a8)", gone)
		}
	}
	for _, want := range []string{"ghWatch = &ghWatchWitness{", "ghWatch.Check(conditions, now)"} {
		if !strings.Contains(body, want) {
			t.Errorf("main.go does not contain %q; nothing annunciates a dark gh-watch job", want)
		}
	}
}

func keys(m map[string]pogodCondition) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
