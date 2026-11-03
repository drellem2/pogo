package refinery

import (
	"os/exec"
	"strings"
	"testing"
)

// TestRefineryPRLookupIsPinnedToOrigin: ghpr.Lookup is shared with the
// stranded-work detector, and pinning it to origin's repository
// (drellem2/pogo#259) changes the refinery's PR lookups too. The refinery
// pushes to origin, so origin is the repository whose PR it means; an
// `upstream` remote, which gh would otherwise prefer, must not redirect it.
func TestRefineryPRLookupIsPinnedToOrigin(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "git@github.com:drellem2/pogo.git"},
		{"remote", "add", "upstream", "https://github.com/someone-else/pogo.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	logPath := fakeGHLog(t, `echo '{"state":"OPEN","number":7}'`)
	if n, state, err := lookupPR(dir, "polecat-x"); err != nil || n != 7 || state != "OPEN" {
		t.Fatalf("lookupPR = (%d, %q, %v), want (7, OPEN, nil)", n, state, err)
	}
	if n, err := openPRNumber(dir, "polecat-x"); err != nil || n != 7 {
		t.Fatalf("openPRNumber = (%d, %v), want (7, nil)", n, err)
	}
	calls := ghCalls(t, logPath)
	if len(calls) != 2 {
		t.Fatalf("gh calls = %q, want 2", calls)
	}
	for _, c := range calls {
		if !strings.HasSuffix(c, "-R drellem2/pogo") {
			t.Errorf("refinery gh call %q is not pinned to origin's drellem2/pogo", c)
		}
	}
}

// TestClosePRAndReapClosesInTheRepoItLookedUp: the close is a write addressed
// by NUMBER, so it must carry the same -R as the lookup that produced the
// number. Unpinned, gh would pick its own base repo (the upstream remote here)
// and close PR #7 THERE. The push URL is a local bare repo so the branch reap
// runs without the network; `remote get-url origin` still reads the github.com
// fetch URL the pin is derived from.
func TestClosePRAndReapClosesInTheRepoItLookedUp(t *testing.T) {
	dir := t.TempDir()
	bare := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "--bare", bare)
	run("-C", dir, "init", "-q")
	run("-C", dir, "remote", "add", "origin", "git@github.com:fork-owner/pogo.git")
	run("-C", dir, "remote", "set-url", "--push", "origin", bare)
	run("-C", dir, "remote", "add", "upstream", "https://github.com/drellem2/pogo.git")

	logPath := fakeGHLog(t, `case "$1 $2" in
  "pr view") echo '{"state":"OPEN","number":7}' ;;
  *) : ;;
esac`)
	mr := &MergeRequest{ID: "mr-test", Branch: "polecat-x", TargetRef: "main", RepoPath: dir}
	(&Refinery{}).closePRAndReap(dir, mr, "abc1234")

	// The close comment spans lines, so the log is split per call on the
	// "pr " that starts each invocation rather than on newlines.
	log := strings.Join(ghCalls(t, logPath), "\n")
	view, close, ok := strings.Cut(log, "\npr close ")
	if !ok || !strings.HasPrefix(view, "pr view polecat-x") || !strings.HasPrefix(close, "7 ") {
		t.Fatalf("gh log = %q, want a pr view then a pr close 7", log)
	}
	if !strings.HasSuffix(view, "-R fork-owner/pogo") {
		t.Errorf("lookup %q is not pinned to origin's fork-owner/pogo", view)
	}
	if !strings.HasSuffix(close, "-R fork-owner/pogo") {
		t.Errorf("close %q is not pinned to the repo the lookup used (fork-owner/pogo)", close)
	}
}
