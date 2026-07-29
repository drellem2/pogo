package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
)

// TestGitGCLogfCarriesTheAction is the pogod half of gh #94's diagnosability
// requirement. internal/gitgc has always assembled a full description of every
// decision — path, owner, branch, reason — and pogod threw all of it away,
// logging four counts per sweep. One worktree removal sits in a 5.5MB log on
// the reporting host and there is no way to tell whether it was legitimate.
//
// The assertion is deliberately about the ASSEMBLED line pogod writes, not
// about the format string it passes down: the defect was a wiring omission, so
// a test that stopped at "Options.Logf is non-nil" would pass on the broken
// version too.
func TestGitGCLogfCarriesTheAction(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(oldFlags) })

	logf := gitGCLogf("/Users/x/dev/pogo")
	logf("removed worktree %s", gitgc.WorktreeAction{
		Path:   "/Users/x/.pogo/polecats/caa65",
		Owner:  "caa65",
		Branch: "polecat-dccb",
		Reason: "ticket archived",
	}.String())

	line := strings.TrimSpace(buf.String())
	for _, want := range []string{
		"git GC",
		"/Users/x/dev/pogo",             // WHICH repo was swept
		"removed worktree",              // WHAT happened
		"/Users/x/.pogo/polecats/caa65", // to WHICH tree
		"owner caa65",                   // WHOSE tree it was
		"branch polecat-dccb",           // what was checked out in it
		"ticket archived",               // and WHY
	} {
		if !strings.Contains(line, want) {
			t.Errorf("git GC log line missing %q — an operator cannot judge this removal.\ngot: %s", want, line)
		}
	}
}
