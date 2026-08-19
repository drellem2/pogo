package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/testtmp"
)

// This package had no TestMain, and the merged-not-closed alert (mg-9d4e) is
// what made one necessary rather than merely tidy.
//
// The alert fires from reapMergedPolecat whenever the post-merge `mg done` does
// not apply, and this package already drives that path with a failing `complete`
// — filernotify_test.go does it on purpose, because the already-done race is
// part of what it covers. Without the isolation below, running `go test
// ./cmd/pogod` writes a work_item_merged_not_closed record into the developer's
// live ~/.pogo/events.log and asks the live macguffin store about a fabricated
// work item. Both are the shape of defect internal/agent's TestMain documents:
// a unit test manufacturing an operator alarm.
//
// It is deliberately NARROW. It redirects the event log and stubs the two
// process-global sinks the alert reaches through; it does not pin HOME or
// POGO_HOME. A wider envelope may well be right for this package, but it would
// change what every existing test here reads, and that is a separate change from
// stopping this one from shouting.

// testEventLogPath is the throwaway log TestMain installs, exposed so a test can
// assert that a record reached the spine. Empty when the temp dir could not be
// made, which those tests skip on rather than falling back to the live log.
//
// IT IS ALSO THE VALUE EVERY PER-TEST REDIRECT MUST RESTORE, and that is not
// bookkeeping. Several tests here point the spine at a private file and used to
// restore `""` afterwards — but `""` is not "the envelope TestMain set up", it is
// "no override", which resolves to the developer's LIVE ~/.pogo/events.log. So
// the first such test to run silently un-did this file's whole purpose for every
// test after it, and the failure was visible only as an assertion about a
// spine-write that had gone somewhere else: TestReportMergedButOpenEmitsBeforeIt-
// Mails passed alone and failed in a full-package run. Restore this, never "".
var testEventLogPath string

func TestMain(m *testing.M) {
	// internal/testtmp, NOT os.MkdirTemp — and the reason is the os.Exit this
	// function ends on. A `defer os.RemoveAll(dir)` here NEVER RUNS: os.Exit does not run deferred functions, so the naive version
	// of this leaks one directory into $TMPDIR on every single run, whether the
	// suite passes or fails. scripts/tmpdir-leak-guard.sh catches exactly that
	// and failed the gate on this file's first pass through it; on 2026-08-13 the
	// same shape reached ~5,000 directories, filled the volume, and failed every
	// merge gate on the host (mg-60eb). testtmp.Dir nests under one root reaped
	// by PID OWNERSHIP, so removal does not depend on any code being reached.
	dir, err := testtmp.Dir("events")
	if err == nil {
		testEventLogPath = filepath.Join(dir, "events.log")
		events.SetLogPathForTesting(testEventLogPath)
	}

	// The store probe. Its default shells out to `mg show`, so an un-stubbed run
	// asks the real store about ids that exist only in a test table. "done" is
	// the quiet answer: it takes the benign branch, so a test that has not opted
	// into the alert does not raise one.
	mergedOpenItemDone = func(string) (bool, error) { return true, nil }

	// The mail sink. defaultMergedOpenAlertMail refuses to send under a test
	// binary on its own (testing.Testing()), so this is the second of two
	// independent guards rather than the only one — the point of the pair is
	// that dropping either still leaves the coordinator's inbox protected.
	mergedOpenAlertMail = func(mergedOpenAlert) {}

	code := m.Run()

	// Here "" is correct and it is the only place it is: the process is ending,
	// the temp dir is about to go, and there is no envelope left to restore to.
	events.SetLogPathForTesting("")
	os.Exit(code)
}

// TestPerTestEventLogRedirectsRestoreTheThrowawayLog is the guard for the note
// above. A redirect that restores "" points the spine at the developer's live
// ~/.pogo/events.log for every test that runs after it, and nothing in a passing
// run says so — the symptom is another test's spine assertion failing only in a
// full-package run, which reads as a flake.
//
// Source-level rather than behavioural because the defect is a value written at
// one call site and observed at a different one, in a different test, sometimes.
func TestPerTestEventLogRedirectsRestoreTheThrowawayLog(t *testing.T) {
	entries, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(entries) == 0 {
		t.Skip("no test sources visible from here")
	}
	for _, f := range entries {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("read %s: %v", f, err)
			continue
		}
		body := string(raw)
		if f == "testmain_test.go" {
			// TestMain's own final reset is the one legitimate "" — see above.
			continue
		}
		if strings.Contains(body, `SetLogPathForTesting("")`) {
			t.Errorf("%s restores the event log to \"\", which is the LIVE ~/.pogo/events.log and not "+
				"the throwaway log TestMain installed — restore testEventLogPath instead", f)
		}
	}
}
