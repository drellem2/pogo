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
