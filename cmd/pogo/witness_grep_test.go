package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// The orphan alert's re-verify instruction, pinned to what `pogo agent witness
// --json` actually prints (mg-da48).
//
// WHY THIS TEST IS IN THIS PACKAGE. mailOrphanAlert (internal/agent) hands the
// mayor a command to run before killing anything:
//
//	pogo agent witness --json | grep -q '<agent.WitnessAliveGrep(name, pid)>' && kill <pid> && ...
//
// The grep is the whole safety property. This mail repeats hourly and is read at
// an unbounded delay, by which time the pid may belong to an unrelated process —
// so the kill is gated on the witness still naming that (name, pid) as alive.
// But the pattern is built in internal/agent while the output it must match is
// built HERE, in witnessCLIReport. Nothing in the compiler couples them. If the
// JSON tags, the field order, or printCompactJSON's compactness ever changes,
// the pattern silently stops matching and the wired-in check quietly becomes a
// command that never kills anything — a failure that reads as "the alert is
// wrong" and gets the grep dropped, which is the actual hazard.
//
// So the coupling gets a test at the seam, on the real marshaller. This is the
// same posture as orphan_alert_test.go's fake-mg control: an instruction the
// daemon emits but never executes is a claim until something executes it.

// TestWitnessAliveGrepMatchesRealOutput is the control. The pattern the mail
// hands out must match the report the command actually prints.
func TestWitnessAliveGrepMatchesRealOutput(t *testing.T) {
	report := witnessCLIReport{
		WitnessPath:    "/home/u/.pogo/polecat-witness.json",
		WitnessPresent: true,
		AliveCount:     1,
		Alive:          []witnessCLIEntry{{Name: "cat-9f21", PID: 41207, WorkItemID: "mg-9f21"}},
	}
	// json.Marshal, not MarshalIndent: printCompactJSON is what the command
	// uses, and the pattern has no spaces in it because that output has none.
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	out := string(data)

	want := agent.WitnessAliveGrep("cat-9f21", 41207)
	if !grepQ(t, want, data) {
		t.Fatalf("the orphan alert tells the mayor to gate a kill on `grep -q %q`, but the real "+
			"`pogo agent witness --json` output does not contain it. The gate would ALWAYS fail, the "+
			"kill would never run, and the next reader to notice would delete the grep — leaving the "+
			"bare `kill <pid>` mg-da48 removed.\noutput was:\n%s", want, out)
	}
}

// TestWitnessAliveGrepDoesNotMatchADifferentPid is the half that matters most.
// A pattern that matches too little is safe; one that matches too much kills the
// wrong process. A polecat name is REUSED — RecordPolecatWitness replaces a
// record by name on respawn — so a name-only pattern would pass against a live
// SUCCESSOR and gate the kill open on its pid. The dead one's alert must not be
// satisfiable by its replacement.
func TestWitnessAliveGrepDoesNotMatchADifferentPid(t *testing.T) {
	// The successor: same name, new pid. This is what the witness holds after
	// the orphan in the mail has died and been respawned.
	report := witnessCLIReport{
		WitnessPresent: true,
		AliveCount:     1,
		Alive:          []witnessCLIEntry{{Name: "cat-9f21", PID: 88888, WorkItemID: "mg-9f21"}},
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	stale := agent.WitnessAliveGrep("cat-9f21", 41207) // the mail's pid: dead
	if grepQ(t, stale, data) {
		t.Errorf("the pattern for the DEAD orphan (pid 41207) matches a witness holding only its live "+
			"successor (pid 88888) — the mail's `grep && kill 41207` would pass and kill whatever now "+
			"holds 41207. The identity is (pid, start_time), never the name alone.\noutput was:\n%s",
			string(data))
	}
}

// TestWitnessAliveGrepDoesNotMatchAnAbsentPolecat pins the stale-alert case the
// mail's prose calls the DEFAULT reading: by the time an hourly alert is read,
// the survivor has usually exited. An empty `alive` list must not satisfy the
// gate.
func TestWitnessAliveGrepDoesNotMatchAnAbsentPolecat(t *testing.T) {
	report := witnessCLIReport{WitnessPresent: true, AliveCount: 0, Alive: []witnessCLIEntry{}}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if grepQ(t, agent.WitnessAliveGrep("cat-9f21", 41207), data) {
		t.Errorf("the gate passes against a witness reporting NOBODY alive — a stale alert would still "+
			"fire its kill at a recycled pid.\noutput was:\n%s", string(data))
	}
}

// grepQ runs the pattern through the same instrument the mail hands out —
// `grep -q '<pattern>'`, a basic regular expression — against data, and
// reports whether it matched. The pattern is a regex (mg-d451), so
// strings.Contains is no longer a faithful model of the gate; the gate is what
// gets tested. Exit 1 is "no match"; anything else is a broken instrument and
// fails the test rather than reading as a negative.
func grepQ(t *testing.T, pattern string, data []byte) bool {
	t.Helper()
	cmd := exec.Command("grep", "-q", pattern)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false
	}
	t.Fatalf("grep -q %q did not run cleanly: %v: %s", pattern, err, stderr.String())
	return false
}

// TestWitnessAliveGrepDoesNotPrefixMatchALongerPid is mg-d451. A pid is a
// decimal prefix of other pids: before the pattern was terminated, the gate for
// a dead `cat-9f21` on pid 7052 passed against a witness holding a live
// `cat-9f21` on pid 70527, and the mail's `&& kill 7052` fired at whatever now
// holds 7052. The pair is tested both ways round, with and without the
// work_item_id that decides whether `,` or `}` follows the pid, and each
// negative is paired with the positive control that proves the same grep, on
// the same report shape, does match the pid it names.
func TestWitnessAliveGrepDoesNotPrefixMatchALongerPid(t *testing.T) {
	for _, item := range []string{"mg-9f21", ""} { // "" omits work_item_id: `}` follows the pid
		report := func(pid int) []byte {
			data, err := json.Marshal(witnessCLIReport{
				WitnessPresent: true,
				AliveCount:     1,
				Alive:          []witnessCLIEntry{{Name: "cat-9f21", PID: pid, WorkItemID: item}},
			})
			if err != nil {
				t.Fatalf("marshal report: %v", err)
			}
			return data
		}
		for _, c := range []struct{ mail, alive int }{{7052, 70527}, {70527, 7052}} {
			// Positive control: the gate matches its own pid in this shape.
			if !grepQ(t, agent.WitnessAliveGrep("cat-9f21", c.mail), report(c.mail)) {
				t.Fatalf("work_item_id=%q: the gate for pid %d does not match a witness holding pid %d "+
					"itself — the negative below would say nothing.\noutput was:\n%s",
					item, c.mail, c.mail, report(c.mail))
			}
			if grepQ(t, agent.WitnessAliveGrep("cat-9f21", c.mail), report(c.alive)) {
				t.Errorf("work_item_id=%q: the gate for DEAD pid %d passes against a witness holding only "+
					"pid %d — the mail's `grep && kill %d` would kill whatever now holds %d.\noutput was:\n%s",
					item, c.mail, c.alive, c.mail, c.mail, report(c.alive))
			}
		}
	}
}

// TestWitnessAliveGrepEscapesTheName: the pattern is a BRE, so a `.` in a name
// must not match any character — a gate for `cat.9f21` must not pass on a live
// `catX9f21` holding the same pid. Positive control included.
func TestWitnessAliveGrepEscapesTheName(t *testing.T) {
	report := func(name string) []byte {
		data, err := json.Marshal(witnessCLIReport{
			WitnessPresent: true,
			AliveCount:     1,
			Alive:          []witnessCLIEntry{{Name: name, PID: 41207}},
		})
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		return data
	}
	gate := agent.WitnessAliveGrep("cat.9f21", 41207)
	if !grepQ(t, gate, report("cat.9f21")) {
		t.Fatalf("the gate %q does not match its own name — escaping broke the positive case", gate)
	}
	if grepQ(t, gate, report("catX9f21")) {
		t.Errorf("the gate %q matches a DIFFERENT name, catX9f21: the `.` was read as a wildcard", gate)
	}
}
