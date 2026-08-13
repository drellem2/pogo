package main

import (
	"os/exec"
	"testing"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/mgcontract"
	"github.com/drellem2/pogo/internal/refinery"
)

// THE DEFECT AND THE FIX, AGAINST THE REAL mg BINARY (mg-2b71).
//
// The unit tests drive reapMergedPolecat through an injected closer, so they
// pin what the reap path DOES with an outcome. They cannot pin the premise the
// whole design rests on: that `mg done` refuses an item which is not in
// claimed/. That refusal is why mg-be37's close-at-merge path failed by
// construction in the case it was built for — a branch submitted by hand, whose
// item is unclaimed — and it is why the fix claims first.
//
// Here the store is real, the binary is real, and the assertion is the item's
// own status afterwards rather than anything this code reports about itself.
func TestTheMergeCloseActuallyClosesAnUnclaimedItem(t *testing.T) {
	root := mgSandboxStore(t)
	// The premise, declared rather than assumed. If mg ever accepts `done` on an
	// unclaimed item, this control stops proving what it claims to and the
	// clause says so instead of the test quietly passing.
	mgcontract.Require(t, mgcontract.DoneRefusesAnUnclaimedItem)
	// CloseMGWorkItemAtMerge shells out to mg with no --root, so the sandbox is
	// pinned through the environment or this test writes to the developer's
	// live store.
	t.Setenv("MG_ROOT", root)

	id := mgAvailableItem(t, root, "a branch submitted by hand for an unclaimed item", "")

	// The reproduced shape: a merged MR whose author names a work item, with NO
	// polecat in the registry.
	filer := &capturingFiler{}
	mr := &refinery.MergeRequest{ID: "mr-" + id, Branch: "polecat-hand", Author: id,
		TargetRef: "main", MergedSHA: "1a0240a"}
	reapMergedPolecat(&fakeReaper{}, mr, client.CloseMGWorkItemAtMerge, postMergeVerdict{}, nil, filer)

	if got := mgItemStatus(t, root, id); got != "done" {
		t.Fatalf("the item is in %q, want done — the close-at-merge path still cannot close the case it exists "+
			"for (mg-be37/mg-2b71)", got)
	}
	got := filer.all()
	if len(got) != 1 || !got[0].Closed {
		t.Fatalf("the item closed; the notice must say so: %+v", got)
	}
}

// FIX DIRECTION 4, END TO END. The reproduced item was `parked` — pm-onethird
// had ruled that the substantive work stays open — and the correct behaviour
// was to merge, leave the item alone, and SAY SO. Two assertions, because
// either one alone can be satisfied by the defect: the item is untouched, and
// the report does not call it a completion.
func TestAGatedItemIsLeftAloneAndSaidSo(t *testing.T) {
	root := mgSandboxStore(t)
	mgcontract.Require(t, mgcontract.DoneRefusesAnUnclaimedItem)
	t.Setenv("MG_ROOT", root)

	id := mgAvailableItem(t, root, "work somebody parked on purpose", "parked")

	filer := &capturingFiler{}
	mr := &refinery.MergeRequest{ID: "mr-" + id, Branch: "polecat-c479c", Author: id,
		TargetRef: "main", MergedSHA: "1a0240a"}
	reapMergedPolecat(&fakeReaper{}, mr, client.CloseMGWorkItemAtMerge, postMergeVerdict{}, nil, filer)

	if got := mgItemStatus(t, root, id); got != "available" {
		t.Fatalf("a parked item nobody holds is in %q — pogod closed work that was stopped on purpose", got)
	}
	got := filer.all()
	if len(got) != 1 {
		t.Fatalf("the filer is still owed the report: %+v", got)
	}
	if got[0].Closed {
		t.Fatalf("THE DEFECT: the item is untouched in available/ and the notice calls it COMPLETED: %+v", got[0])
	}
	if got[0].NotClosedReason == "" {
		t.Error("a decision that leaves an item open must say why; silence here is the half of the defect that " +
			"made the false notice unactionable")
	}
}

// mgAvailableItem files an item and leaves it UNCLAIMED — the state a
// hand-submitted branch's item is in at merge time, and the one `mg done`
// refuses. assignee is optional; "parked" produces the gated shape.
func mgAvailableItem(t *testing.T, root, title, assignee string) string {
	t.Helper()
	args := []string{"--root", root, "new", "--no-repo", "--title=" + title}
	if assignee != "" {
		args = append(args, "--assignee="+assignee)
	}
	out, err := exec.Command("mg", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("mg new: %v: %s", err, out)
	}
	m := mgNewID.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("could not parse a work item id out of %q", out)
	}
	return m[1]
}
