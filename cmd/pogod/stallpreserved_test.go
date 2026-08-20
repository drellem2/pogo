package main

import (
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
)

// TestStallPreservedFromCarriesTheSplitAndTheUnreadFlag pins the translation
// between gitgc's record and the notice's.
//
// The MODIFIED/UNTRACKED SPLIT has to survive: it is what separates a
// recoverable edit (HEAD still has a version) from the only copy of a file on
// the machine, and the reader deciding whether to override is deciding exactly
// that. Flattening it to a total is the shape of defect mg-d45b already fixed
// one layer down.
func TestStallPreservedFromCarriesTheSplitAndTheUnreadFlag(t *testing.T) {
	rep := gitgc.PreservedItemReport{Trees: map[string][]gitgc.PreservedTree{
		"mg-516e": {{
			Path: "/polecats/p516e", Branch: "polecat-p516e",
			Outcome: "preserved", Total: 16, Modified: 14, Untracked: 2,
		}},
		"mg-fbaf": {{
			Path: "/polecats/pfbaf", Outcome: "undetermined",
			StatusError: "fatal: not a git repository",
		}},
	}}

	held := stallPreservedFrom(rep)

	got := held.Items["mg-516e"]
	if len(got) != 1 {
		t.Fatalf("want 1 tree for mg-516e, got %d", len(got))
	}
	if got[0].Modified != 14 || got[0].Untracked != 2 {
		t.Errorf("split = %d/%d, want 14/2", got[0].Modified, got[0].Untracked)
	}
	if got[0].Branch != "polecat-p516e" {
		t.Errorf("Branch = %q, want polecat-p516e", got[0].Branch)
	}
	if got[0].Outcome != "preserved" {
		t.Errorf("Outcome = %q, want \"preserved\" — a positively-read dirty tree must not read "+
			"as unestablished", got[0].Outcome)
	}

	unread := held.Items["mg-fbaf"]
	if len(unread) != 1 || unread[0].Outcome != "undetermined" {
		t.Fatalf("a tree git could not read must carry its own outcome, got %+v", unread)
	}
	// And it must NOT be described as "0 modified, 0 untracked": that is a
	// claim about the tree, and the wrong one.
	if unread[0].Modified != 0 || unread[0].Untracked != 0 {
		t.Errorf("counts on an unread tree = %d/%d, want 0/0 with the outcome carried — the "+
			"outcome is what stops the zeroes being read as an empty tree",
			unread[0].Modified, unread[0].Untracked)
	}
}

// TestStallPreservedFromReportsErrorsAsUncertaintyNotSilence. A non-fatal read
// failure must never suppress a finding: an incomplete snapshot can only cause
// a held item to be missed, so the caveat rides along with the answer.
func TestStallPreservedFromReportsErrorsAsUncertaintyNotSilence(t *testing.T) {
	rep := gitgc.PreservedItemReport{
		Trees: map[string][]gitgc.PreservedTree{
			"mg-516e": {{Path: "/polecats/p516e", Outcome: "preserved", Modified: 1}},
		},
		Errors: []string{"re-read status of /polecats/p9d4e: EACCES"},
	}
	held := stallPreservedFrom(rep)
	if len(held.Items["mg-516e"]) != 1 {
		t.Fatal("an error suppressed the finding it travelled with")
	}
	if held.Uncertain == "" {
		t.Error("a partial read must say so; silence here reads as a complete scan")
	}
}

// TestStallPreservedFromCarriesTheCommittedHalf pins mg-fcba's half of the
// translation. The counts are all zero on such a tree — that is the premise —
// so if the commits do not survive this hop the notice says nothing at all
// about an item whose work is on disk.
func TestStallPreservedFromCarriesTheCommittedHalf(t *testing.T) {
	rep := gitgc.PreservedItemReport{Trees: map[string][]gitgc.PreservedTree{
		"mg-3d0e": {{
			Path: "/polecats/p3d0e", Branch: "polecat-p3d0e", Outcome: "unpushed",
			Commits: &gitgc.WorktreeCommitFinding{
				Verdict: gitgc.DurabilityLocalOnly,
				Commits: []string{"aaa1111 one", "bbb2222 two"},
			},
		}},
		"mg-6b2d": {{
			// git's literal "HEAD" for a detached head — the case with no ref
			// anywhere, which is why it is derived here rather than assumed.
			Path: "/polecats/p6b2d", Branch: "HEAD", Outcome: "unpushed",
			Commits: &gitgc.WorktreeCommitFinding{Verdict: gitgc.DurabilityLocalOnly},
		}},
	}}

	held := stallPreservedFrom(rep)

	got := held.Items["mg-3d0e"]
	if len(got) != 1 {
		t.Fatalf("want 1 tree for mg-3d0e, got %d", len(got))
	}
	if got[0].Outcome != "unpushed" {
		t.Errorf("Outcome = %q, want \"unpushed\"", got[0].Outcome)
	}
	if got[0].CommitsFinding != "local-only" || got[0].Commits != 2 {
		t.Errorf("commits = %d/%q, want 2/local-only", got[0].Commits, got[0].CommitsFinding)
	}
	if got[0].Detached {
		t.Error("a tree on a named branch must not read as detached")
	}

	det := held.Items["mg-6b2d"]
	if len(det) != 1 || !det[0].Detached {
		t.Fatalf("a tree whose branch reads \"HEAD\" is detached — that is the case no ref scan "+
			"can see; got %+v", det)
	}
}
