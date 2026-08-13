package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/filernotify"
)

// A notifier that is constructed and never reached is the defect it exists to
// close, one layer up: the completion path would still be silent, and the only
// evidence would be an absence. So both observers of a close are pinned at the
// wiring, not just at the unit tests that drive them through a fake.
func TestTheCompletionNotifierIsWiredToBothClosePaths(t *testing.T) {
	src := stripGoComments(readSourceFile(t, "main.go"))

	if !strings.Contains(src, "filerNotify := newFilerNotifier(coordinator, agentRegistry)") {
		t.Error("pogod does not construct the completion notifier over the real coordinator and registry (mg-f120)")
	}
	// The merge close — the path the observed instance (mg-145f) took.
	//
	// The closer is CloseMGWorkItemAtMerge and not CompleteMGWorkItem, and the
	// difference is load-bearing rather than cosmetic (mg-2b71): the plain
	// wrapper runs `mg done` and reports only that it failed, which on an
	// unclaimed item is every hand-submitted branch. The merge closer claims
	// first where that is the right move, declines where it is not, and tells
	// the caller which of those happened — the fact the notification below is
	// now conditional on.
	if !strings.Contains(src, "reapMergedPolecat(agentRegistry, mr, client.CloseMGWorkItemAtMerge, postMerge, deferBackstop, filerNotify)") {
		t.Error("the merge-close path does not reach the completion notifier through the merge closer, so a merged " +
			"item's filer is either told by nobody (mg-f120) or told COMPLETED about an item that never closed (mg-2b71)")
	}
	// The non-merge close — triage, audit and investigation items, which the
	// refinery never hears about at all.
	if !strings.Contains(src, "doneReap.SetFilerNotifier(filerNotify)") {
		t.Error("the done-item reaper does not reach the completion notifier, so an item that closes without " +
			"a merge is still silent to the agent that commissioned it (mg-f120)")
	}
}

// The compile-time half: the real notifier satisfies the seam both reap paths
// take, and the real probes fit the constructor. A rename on either side would
// otherwise break the daemon while every test against the fake still passed.
func TestTheRealNotifierSatisfiesTheReapSeam(t *testing.T) {
	var seam filerNotifier = newFilerNotifier("mayor", nil)
	if seam == nil {
		t.Fatal("unreachable: the assignment above is the assertion")
	}
	// nil-safe at the seam, because a wiring fault must surface as one missing
	// notification rather than as a panic on the merge path.
	notifyFiler(nil, filernotify.Completion{ItemID: "mg-1234"})
}
