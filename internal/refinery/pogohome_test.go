package refinery

import (
	"strings"
	"testing"
)

// TestSubmitRefusesPogoHome is mg-752a3's test: a submit whose repo is the
// test's POGO_HOME is refused at submit time, with a message naming why and
// the live-commit path, and nothing is queued; a sibling repo carrying the
// same pushed branch is accepted.
//
// The live tree here is a working clone of a bare origin with the branch
// pushed, so everything else Submit checks would pass: the refusal can only
// come from the $POGO_HOME check. The last arm proves it by pointing
// POGO_HOME elsewhere and submitting the very same repo successfully.
func TestSubmitRefusesPogoHome(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	seedBranch(t, originDir, "polecat-live")

	liveTree := t.TempDir()
	run(t, liveTree, "git", "clone", originDir, ".")
	sibling := t.TempDir()
	run(t, sibling, "git", "clone", originDir, ".")

	t.Setenv("POGO_HOME", liveTree)

	for _, repo := range []string{liveTree, liveTree + "/"} {
		r := newBranchValidationRefinery(t)
		id, err := r.Submit(MergeRequest{RepoPath: repo, Branch: "polecat-live", TargetRef: "main", Author: "mg-752a3"})
		if err == nil {
			t.Fatalf("submit into $POGO_HOME (%s) was accepted as MR %q", repo, id)
		}
		for _, want := range []string{"$POGO_HOME", "never pulls", "live checkout", "commit directly"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal for %s does not say %q: %v", repo, want, err)
			}
		}
		if n := len(r.Queue()); n != 0 {
			t.Errorf("a refused submit left %d item(s) queued", n)
		}
	}

	r := newBranchValidationRefinery(t)
	if _, err := r.Submit(MergeRequest{RepoPath: sibling, Branch: "polecat-live", TargetRef: "main", Author: "mg-752a3"}); err != nil {
		t.Fatalf("submit into a sibling repo was refused: %v", err)
	}

	// Positive control: the same live-tree submit, with POGO_HOME elsewhere.
	t.Setenv("POGO_HOME", t.TempDir())
	r = newBranchValidationRefinery(t)
	if _, err := r.Submit(MergeRequest{RepoPath: liveTree, Branch: "polecat-live", TargetRef: "main", Author: "mg-752a3"}); err != nil {
		t.Fatalf("control: the live-tree repo was refused with POGO_HOME pointed elsewhere, so the refusal above is not the $POGO_HOME check: %v", err)
	}
}
