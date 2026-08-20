package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/promptedit"
)

// TestPromptEditSeamsFitTheRealImplementations is the compile-time half of the
// wiring proof: the REAL embedded corpus, the REAL prompt directory and the
// REAL mail sender fit the runner's signatures. Without it, a rename on either
// side breaks the daemon while every unit test in internal/promptedit still
// passes against its fixtures.
func TestPromptEditSeamsFitTheRealImplementations(t *testing.T) {
	w := promptedit.New(promptedit.Options{
		Enabled:     true,
		Root:        agent.PromptDir(),
		ShippedFS:   agent.DefaultPromptsFS(),
		Coordinator: agent.CoordinatorName(),
		Mail:        client.SendMGMail,
		StatePath:   promptedit.NoticesPath(t.TempDir()),
	})
	if w == nil {
		t.Fatal("unreachable: the construction above is the assertion")
	}
}

// TestPromptEditIsWiredToTheHeartbeat is the half that matters most for THIS
// detector, and it is the whole point of mg-0c96.
//
// The ticket's finding is not that the instrument was missing. The stamp has
// recorded the body hash since it was introduced and the mismatch reading was
// verified by hand on two files (mg-0635). The finding is that NOTHING RAN IT:
// it fired once, by luck, because a shipped update happened to collide with an
// edited region. A detector for "an armed check with no runner" that is itself
// constructed and never called would be the same defect one level up — and that
// is not hypothetical here, it is what mg-10e3 documents about
// `pogo doctor --check` and what internal/verdictwatch cost the family before
// it.
func TestPromptEditIsWiredToTheHeartbeat(t *testing.T) {
	src := stripGoComments(readSourceFile(t, "main.go"))
	if !strings.Contains(src, "promptedit.New(") {
		t.Error("pogod never constructs the prompt hand-edit detector")
	}
	if !strings.Contains(src, "promptEditWatcher.Check(now)") {
		t.Error("pogod constructs the prompt hand-edit detector but never calls Check — an armed " +
			"detector with no runner is precisely the condition mg-0c96 exists to remove, " +
			"reproduced one level up")
	}
	if !strings.Contains(src, "cfg.PromptEdit.Enabled") {
		t.Error("the prompt hand-edit detector is not gated on its config switch, so it cannot be turned off")
	}
	if !strings.Contains(src, "ShippedFS:     agent.DefaultPromptsFS()") {
		t.Error("the detector is not wired to the real embedded corpus, so its DOMAIN is not the " +
			"shipped prompt set and the four-way classification means nothing")
	}
	if !strings.Contains(src, "StatePath:     promptedit.NoticesPath(") {
		t.Error("the detector has no on-disk suppression store, so every pogod restart re-announces " +
			"every finding — roughly daily on this host")
	}
}

// TestPromptEditHasNoRepairSeamInPogod. The report-only posture is not a
// convention here, it is the reason this is a detector at all: a repair that
// carries a local line forward stales the stamp, and the stamp cannot be
// recomputed without the installer's exact canonicalisation, so a repairing
// tool would silently certify a body it never validated. p0635 hit exactly this
// and correctly stopped rather than guess.
//
// It is easy to "fix" by pattern-matching a neighbouring block that passes a
// KickFunc or an installer, so it is pinned here rather than left to a comment.
func TestPromptEditHasNoRepairSeamInPogod(t *testing.T) {
	src := stripGoComments(readSourceFile(t, "main.go"))
	start := strings.Index(src, "promptedit.New(")
	if start < 0 {
		t.Fatal("pogod never constructs the prompt hand-edit detector")
	}
	end := strings.Index(src[start:], "})")
	if end < 0 {
		t.Fatal("could not find the end of the promptedit.New call")
	}
	block := src[start : start+end]
	// Precondition: prove the slice above is the construction call and not an
	// empty or mis-bounded fragment, which would make every assertion below pass
	// vacuously.
	if !strings.Contains(block, "Mail:") || !strings.Contains(block, "ShippedFS:") {
		t.Fatalf("the sliced promptedit.New block does not look like the construction call, so the "+
			"assertions below would pass without testing anything:\n%s", block)
	}
	for _, banned := range []string{"InstallPrompts", "Repair", "Restamp", "Kick", "Write"} {
		if strings.Contains(block, banned) {
			t.Errorf("the prompt hand-edit detector was given %q. It must have no seam through "+
				"which a prompt could be rewritten or re-stamped: recomputing a stamp without the "+
				"installer's canonicalisation certifies a body nothing validated (mg-0c96)", banned)
		}
	}
}

// TestPromptSyncAddresseeStillRoutesAfterTheTableMoved. The routing table moved
// to internal/agent when this detector became its second caller. The move is
// invisible to the notifier's own tests if they only exercise the wrapper, so
// this pins the two callers against each other: whatever pogod's declined-sync
// notifier resolves, the detector must resolve identically, because both mail
// the same agent about the same file.
func TestPromptSyncAddresseeStillRoutesAfterTheTableMoved(t *testing.T) {
	for _, rel := range []string{"mayor.md", "crew/doctor.md", "templates/polecat.md", "pm/pm-template.md"} {
		wantTo, wantOwned := promptSyncAddressee(rel, "ringmaster")
		gotTo, gotOwned := agent.PromptAddressee(rel, "ringmaster")
		if wantTo != gotTo || wantOwned != gotOwned {
			t.Errorf("%s: notifier routes to %q(owned=%v), detector routes to %q(owned=%v) — "+
				"two copies of a routing table are two chances to misroute", rel, wantTo, wantOwned, gotTo, gotOwned)
		}
	}
}
