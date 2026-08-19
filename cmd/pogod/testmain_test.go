package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/events"
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
var testEventLogPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pogod-test-events")
	if err == nil {
		defer os.RemoveAll(dir)
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

	events.SetLogPathForTesting("")
	os.Exit(code)
}
