package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The LOUD half, asserted rather than assumed (mg-0b77).
//
// This ticket's ruling is that "loud" must mean OBSERVABLE FROM OUTSIDE THE
// THING THAT FAILED — a mail, not a log line — because the survivor's only
// other signal is scheduler_fire_failed in events.log, which nobody reads
// unless they are already suspicious. That ruling is worth exactly as much as
// the mail send is real. defaultOrphanAlert shells out to `mg`, and an
// unexercised shell-out is how "loud" stays a claim: a typo in the argv, a
// wrong flag name, or a subcommand that never existed all fail silently and
// best-effort, leaving the log line as the only signal — the very thing the
// ruling rejects. This drives the REAL sink against a fake `mg` on PATH.
//
// It also guards the mg-76e5 posture at the assembly layer: mailDriftAlert and
// mailOrphanAlert are deliberately best-effort (a leaked agent must not take
// the daemon down), which means a broken invocation CANNOT announce itself. The
// only place that can catch it is here.

// fakeMG puts a recording `mg` at the front of PATH and returns the path of the
// file it appends its argv to. It is a real executable, invoked by the real
// exec.Command in the real sink — the point is to exercise the wiring, not to
// stub past it.
func fakeMG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\n" +
		"{ for a in \"$@\"; do printf '%s\\n' \"$a\"; done; printf -- '---\\n'; } >> " + record + "\n"
	if err := os.WriteFile(filepath.Join(dir, "mg"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake mg: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

// TestMailOrphanAlert_ActuallySendsMail is the control that makes "loud"
// falsifiable. Break the argv and this goes RED; without it, a broken sink is
// indistinguishable from a working one because the failure path is a log line
// nobody reads.
func TestMailOrphanAlert_ActuallySendsMail(t *testing.T) {
	record := fakeMG(t)

	mailOrphanAlert(OrphanedPolecat{
		Name:       "cat-9f21",
		PID:        41207,
		StartTime:  time.Date(2026, 7, 17, 2, 14, 0, 0, time.UTC),
		WorkItemID: "mg-9f21",
	})

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the sink never invoked `mg` at all — the LOUD half of this fix does not exist, and "+
			"the survivor's only signal is a log line nobody reads (mg-0b77): %v", err)
	}
	got := string(raw)

	// The invocation must be a mail send, addressed to the coordinator.
	for _, want := range []string{"mail", "send", CoordinatorName()} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("`mg` argv is missing %q — a mail that is not addressed and not a send is not a "+
				"signal.\nargv was:\n%s", want, got)
		}
	}

	// The payload must carry what makes it actionable. A human resolving this
	// needs to find the process and the work item; an alert that says only
	// "something is orphaned" moves the search cost onto its reader and gets
	// ignored, which is the failure this whole ticket is about.
	for _, want := range []string{"cat-9f21", "41207", "mg-9f21"} {
		if !strings.Contains(got, want) {
			t.Errorf("the mail does not mention %q — the alert must name the polecat, its pid, and its "+
				"work item, or the reader cannot act on it.\nargv was:\n%s", want, got)
		}
	}
}

// TestDefaultOrphanAlert_EmitsAndMails covers the ASSEMBLED sink — the function
// production actually calls — rather than its halves.
//
// This test exists because its absence was caught, not theorised: with only
// mailOrphanAlert under test, deleting `mailOrphanAlert(p)` from
// defaultOrphanAlert left the ENTIRE unit suite green. The loud half could have
// been removed wholesale and nothing would have gone red — the fleet would emit
// a polecat_orphaned event into events.log (which nobody reads, the premise of
// this whole ticket) and mail no one, while the tests reported success.
//
// That is mg-c02d's ruling: the pure-function tests do not cover the wiring, so
// the wiring needs its own control. A test per half plus an untested join is
// two green halves and a silent gap where the fix was supposed to be.
func TestDefaultOrphanAlert_EmitsAndMails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	record := fakeMG(t)

	defaultOrphanAlert(OrphanedPolecat{
		Name:       "cat-assembled",
		PID:        31337,
		StartTime:  time.Date(2026, 7, 17, 2, 14, 0, 0, time.UTC),
		WorkItemID: "mg-assembled",
	})

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the production sink emitted its event but never mailed anyone — the survivor's only "+
			"signal is then a line in events.log, which is the silence mg-0b77 exists to remove. "+
			"'Loud' must mean observable from OUTSIDE the thing that failed: %v", err)
	}
	if !strings.Contains(string(raw), "cat-assembled") {
		t.Errorf("the assembled sink's mail does not name the survivor.\nargv was:\n%s", string(raw))
	}
}

// TestMailOrphanAlert_IsReVerifiableAtReadTime is the acceptance test for the
// second half of mg-da48.
//
// The alert repeats hourly and is read at an unbounded delay, so by the time
// anyone acts on it the survivor has usually exited and its pid is recyclable —
// three such mails on 2026-07-17 named pids that were already gone, one of them
// pid 438, wrapped and low, on a box demonstrably reusing pids. A body that says
// only `kill 438` is an instruction that is safe in the second it was written
// and delivered by a channel that guarantees it will be read later.
//
// The detector itself is NOT fooled by a recycled pid — witnessVerdict matches
// (pid, start_time) and resolves GONE. That protection just never reached the
// one consumer told to run `kill`. So the body must carry the recorded start
// time AND route the reader to the instrument that re-probes it.
func TestMailOrphanAlert_IsReVerifiableAtReadTime(t *testing.T) {
	record := fakeMG(t)
	start := time.Date(2026, 7, 17, 2, 14, 0, 0, time.UTC)

	mailOrphanAlert(OrphanedPolecat{
		Name:       "cat-9f21",
		PID:        41207,
		StartTime:  start,
		WorkItemID: "mg-9f21",
	})

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read fake mg record: %v", err)
	}
	got := string(raw)

	// The recorded start time must survive the trip to the reader. It is the
	// half of the identity that distinguishes our polecat from a recycled pid,
	// and stripping it is what left the reader holding a bare pid.
	if !strings.Contains(got, start.Format(time.RFC3339)) {
		t.Errorf("the mail does not carry the recorded start_time — the reader is left with a bare pid, "+
			"which is not an identity. The detector's (pid, start_time) protection does not survive the "+
			"trip to the one consumer told to run `kill` (mg-da48).\nbody was:\n%s", got)
	}

	// It must name the instrument that re-checks at READ time, rather than
	// asking the reader to trust a claim of unknown age.
	if !strings.Contains(got, "pogo agent witness") {
		t.Errorf("the mail never points the reader at `pogo agent witness` — the one command that "+
			"re-probes (pid, start_time) NOW, using the same verdict the detector used to send this. "+
			"Without it the reader cannot re-confirm identity at all.\nbody was:\n%s", got)
	}

	// And the kill must be GATED, not merely preceded by advice. Prose asking
	// the reader to check first is skippable; a `grep && kill` is not.
	gate := WitnessAliveGrep("cat-9f21", 41207)
	if !strings.Contains(got, gate) {
		t.Errorf("the mail's kill is not gated on the witness pattern %q — an instruction that can only "+
			"be safely executed in the second it was written must not be handed out ungated by a "+
			"channel with an hourly repeat.\nbody was:\n%s", gate, got)
	}
	// No PASTEABLE line may start with a bare kill. The body discusses `kill`
	// in prose (explaining the hazard), which is fine and necessary — the thing
	// that must not exist is a line a reader can copy straight out of the mail
	// and run, with nothing between it and the process. Those lines are the
	// indented commands, and every one of them must lead with the re-check.
	for _, line := range strings.Split(got, "\n") {
		cmd := strings.TrimSpace(line)
		if !strings.HasPrefix(cmd, "kill ") {
			continue
		}
		t.Errorf("this line is directly pasteable and runs a kill with nothing checked first:\n\t%s\n"+
			"Every runnable kill in this body must be gated on `pogo agent witness`, or a reader acting "+
			"on an hours-old alert fires it at a pid that has since been recycled (mg-da48).", cmd)
	}
}

// TestMailOrphanAlert_SurvivesAMissingMG pins the best-effort posture. `mg` not
// being on PATH must not panic or kill the daemon: the polecat_orphaned event
// is already on the durable spine by this point, and a leaked agent must never
// take pogod down with it. The failure is logged and swallowed — which is
// exactly why the test above has to exist.
func TestMailOrphanAlert_SurvivesAMissingMG(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no `mg` anywhere
	mailOrphanAlert(OrphanedPolecat{Name: "cat-gone", PID: 1, WorkItemID: "mg-gone"})
	// Reaching here without a panic is the assertion.
}

// TestMailOrphanAlert_NamesAnUnknownWorkItemRatherThanBlank pins the small
// honesty that keeps the alert readable: a witness written without a work item
// must say so, not render an empty field. "Work item:" followed by nothing
// reads as a bug in the alert and gets dismissed; "(unknown — ...)" reads as a
// fact about the survivor and gets acted on.
func TestMailOrphanAlert_NamesAnUnknownWorkItemRatherThanBlank(t *testing.T) {
	record := fakeMG(t)

	mailOrphanAlert(OrphanedPolecat{Name: "cat-nowork", PID: 999})

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read fake mg record: %v", err)
	}
	if !strings.Contains(string(raw), "unknown") {
		t.Errorf("a survivor with no recorded work item must SAY its work item is unknown; a blank "+
			"field reads as a broken alert and gets dismissed.\nargv was:\n%s", string(raw))
	}
}

// TestMailOrphanAlert_UnknownWorkItemStaysPasteClean pins the OTHER half of an
// unknown work item: it may be prose in the header and must not be prose in the
// command.
//
// This is what the fleet actually put in front of the mayor on 2026-07-17:
//
//	kill 438 && mg unclaim (unknown — no work item recorded in its witness)
//
// which is not a command, it is a shell syntax error wearing one. That matters
// more than it looks. What makes this alert safe is a `grep -q ... &&` gate the
// reader must not remove — and a line that errors as written trains the reader
// to edit before running, at which point the first casualty is the part they did
// not understand. Commands that run as written are what keep the gate on.
func TestMailOrphanAlert_UnknownWorkItemStaysPasteClean(t *testing.T) {
	record := fakeMG(t)

	mailOrphanAlert(OrphanedPolecat{Name: "cadence", PID: 438})

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read fake mg record: %v", err)
	}
	saw := false
	for _, line := range strings.Split(string(raw), "\n") {
		cmd := strings.TrimSpace(line)
		if !strings.HasPrefix(cmd, "pogo agent witness --json | grep") {
			continue
		}
		saw = true
		// A bare `(` opens a subshell: the prose marker for an absent work item
		// does not merely read badly here, it makes the whole line unparseable.
		if strings.Contains(cmd, "unclaim (") {
			t.Errorf("the runnable line carries the prose work-item marker and is not a command:\n\t%s",
				cmd)
		}
		if strings.HasSuffix(cmd, "mg unclaim") {
			t.Errorf("the runnable line ends in a bare `mg unclaim` with no argument:\n\t%s", cmd)
		}
	}
	if !saw {
		t.Error("the alert offered no gated command at all for a survivor with no work item")
	}
}
