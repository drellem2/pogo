package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/gitgc"
)

// captureLog redirects the standard logger into a buffer for the duration of
// one test and returns a reader for what was written.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })
	return buf.String
}

// TestRunGitGCSweepLogsTheAction is the guard for the ONE LINE that delivers
// gh #94's diagnosability half: `Logf: gitGCLogf(repo)` in runGitGCSweep.
//
// It drives runGitGCSweep itself — not the format helper — against a real
// throwaway repo, and asserts the line pogod actually writes. Deleting the
// Logf wiring makes this test fail; nothing else in the tree does, which is
// what the review of round 1 caught. The sweep's counts summary survives that
// deletion untouched, so the assertion has to be for the per-ACTION line and
// not merely for "the GC said something".
//
// Diagnosability is the deliverable that was promised to the reporter on the
// issue. This PR exists because a guard's real protection lived somewhere
// other than where it appeared to; a test whose stated purpose a one-line
// deletion falsifies is that same shape, so this one drives the wiring.
func TestRunGitGCSweepLogsTheAction(t *testing.T) {
	sandboxPogoHome(t)
	logged := captureLog(t)

	// A concluded, non-live polecat: a normal exit's leftovers, which is the
	// removal an operator most needs to be able to audit after the fact.
	repo := newPolecatRepo(t)
	wt := repo.addPolecatWorktree("0047")

	// The sweep's first act is `mg list`, unreachable from a sandbox — see
	// loadTicketIndexFn. Without this the sweep returns before the wiring.
	orig := loadTicketIndexFn
	t.Cleanup(func() { loadTicketIndexFn = orig })
	loadTicketIndexFn = func() (gitgc.TicketIndex, error) {
		return gitgc.TicketIndex{"mg-0047": gitgc.TicketArchived}, nil
	}

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	runGitGCSweep(reg, config.GitGCConfig{Enabled: true, Repos: []string{repo.dir}}, "")

	// Precondition: the sweep actually ran and actually removed something. A
	// sweep that did nothing would satisfy no assertion below for the right
	// reason, so fail loudly here rather than in the log check.
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("precondition: the sweep did not remove the concluded polecat's worktree (stat err = %v) — "+
			"nothing was destroyed, so there is no action to log:\n%s", err, logged())
	}

	out := logged()
	line := findLogLine(out, "removed worktree")
	if line == "" {
		t.Fatalf("pogod destroyed a worktree and logged no line naming it. The per-action log wiring "+
			"(Logf: gitGCLogf(repo) in runGitGCSweep) is what makes a removal auditable; without it a "+
			"deletion is a bare count in a multi-megabyte log (gh #94).\nlog was:\n%s", out)
	}
	for _, want := range []string{
		"git GC",              // the subsystem
		repo.dir,              // WHICH repo was swept
		wt,                    // WHICH tree was destroyed
		"owner 0047",          // WHOSE tree it was
		"branch polecat-0047", // what was checked out in it
		"ticket archived",     // and WHY
	} {
		if !strings.Contains(line, want) {
			t.Errorf("git GC removal line missing %q — an operator cannot judge this removal.\ngot: %s", want, line)
		}
	}
}

// TestRunGitGCSweepLogsForeignBranchRemovalDistinctly is the diagnosability
// case gh #94 was actually reported from: owner and branch DISAGREE. A log
// line that printed only one of them could not distinguish "this polecat's own
// tree was reaped" from "somebody else's tree was taken", which is precisely
// the question the reporter could not answer from their 5.5MB log.
func TestRunGitGCSweepLogsForeignBranchRemovalDistinctly(t *testing.T) {
	sandboxPogoHome(t)
	logged := captureLog(t)

	// A dead polecat 0047 parked on the foreign, concluded branch of 02eb.
	repo := newPolecatRepo(t)
	repo.git("branch", gitgc.BranchPrefix+"02eb")
	wt := filepath.Join(filepath.Dir(repo.dir), "polecats", "0047")
	repo.git("worktree", "add", "-q", wt, gitgc.BranchPrefix+"02eb")

	orig := loadTicketIndexFn
	t.Cleanup(func() { loadTicketIndexFn = orig })
	loadTicketIndexFn = func() (gitgc.TicketIndex, error) {
		return gitgc.TicketIndex{"mg-02eb": gitgc.TicketArchived}, nil
	}

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	runGitGCSweep(reg, config.GitGCConfig{Enabled: true, Repos: []string{repo.dir}}, "")

	line := findLogLine(logged(), "removed worktree")
	if line == "" {
		t.Fatalf("no removal line logged; log was:\n%s", logged())
	}
	if !strings.Contains(line, "owner 0047") || !strings.Contains(line, "branch polecat-02eb") {
		t.Errorf("owner and branch must BOTH appear and be distinguishable — when they differ you are "+
			"looking at exactly the gh #94 situation.\ngot: %s", line)
	}
}

// TestGitGCLogfFormat covers the format helper alone: the repo tag and the
// assembled action text. It deliberately does NOT claim to guard the wiring —
// it cannot, since it never reaches runGitGCSweep. TestRunGitGCSweepLogsTheAction
// above is what goes red when the wiring is deleted.
func TestGitGCLogfFormat(t *testing.T) {
	logged := captureLog(t)

	gitGCLogf("/Users/x/dev/pogo")("removed worktree %s", gitgc.WorktreeAction{
		Path:   "/Users/x/.pogo/polecats/caa65",
		Owner:  "caa65",
		Branch: "polecat-dccb",
		Reason: "ticket archived",
	}.String())

	line := strings.TrimSpace(logged())
	for _, want := range []string{
		"git GC",
		"/Users/x/dev/pogo",
		"removed worktree",
		"/Users/x/.pogo/polecats/caa65",
		"owner caa65",
		"branch polecat-dccb",
		"ticket archived",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("git GC log line missing %q.\ngot: %s", want, line)
		}
	}
}

// findLogLine returns the first captured log line containing needle.
func findLogLine(out, needle string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}
