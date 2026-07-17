package main

import (
	"encoding/json"
	"strings"
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
	if !strings.Contains(out, want) {
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
	if strings.Contains(string(data), stale) {
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
	if strings.Contains(string(data), agent.WitnessAliveGrep("cat-9f21", 41207)) {
		t.Errorf("the gate passes against a witness reporting NOBODY alive — a stale alert would still "+
			"fire its kill at a recycled pid.\noutput was:\n%s", string(data))
	}
}
