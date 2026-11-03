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
