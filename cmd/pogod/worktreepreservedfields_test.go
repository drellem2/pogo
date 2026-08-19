package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/drellem2/pogo/internal/events"
)

// preservedEvent runs a preservation against a private spine and returns the
// single worktree_preserved event it produced.
func preservedEvent(t *testing.T, setup func(t *testing.T, repoDir, wt string)) events.Event {
	t.Helper()
	spine := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(spine)
	t.Cleanup(func() { events.SetLogPathForTesting(testEventLogPath) })

	repo, wt := wtRepo(t)
	setup(t, repo, wt)
	cleanupAgentWorktree(catAgent("qbe37", "mg-be37", repo, wt), "mayor",
		func(to, from, subject, body string) error { return nil })

	found, err := events.ReadFiltered(spine, events.Filter{Type: "worktree_preserved"})
	if err != nil {
		t.Fatalf("reading the spine: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one worktree_preserved event, got %d: %+v", len(found), found)
	}
	return found[0]
}

// TestPreservedEventCarriesTheBranch covers the first of the two fields mg-d45b
// found missing from the record mg-32e3 landed.
//
// A preserved worktree keeps its branch CHECKED OUT — that is what pinning
// means — so the branch is simultaneously the first thing a rescuer needs and
// the thing whose deletion this tree is blocking. The mail says it; the record
// did not, so any consumer built on the event had to go back to the mailbox for
// it, which is the split this ticket exists to close.
func TestPreservedEventCarriesTheBranch(t *testing.T) {
	ev := preservedEvent(t, func(t *testing.T, repo, wt string) {
		if err := os.WriteFile(filepath.Join(wt, "wip.go"), []byte("package wip\n"), 0644); err != nil {
			t.Fatal(err)
		}
	})

	branch, ok := ev.Details["branch"].(string)
	if !ok {
		t.Fatalf("details.branch missing or not a string: %+v — a rescuer cannot start "+
			"without the branch, and it is the one this tree is pinning", ev.Details)
	}
	if branch != "polecat-cat1" {
		t.Errorf("branch = %q, want polecat-cat1 (the branch wtRepo checks out)", branch)
	}
	if _, bad := ev.Details["branch_error"]; bad {
		t.Errorf("branch_error present on a readable tree: %v — the two keys are "+
			"alternatives, and emitting both makes neither meaningful", ev.Details["branch_error"])
	}
}

// TestPreservedEventSplitsModifiedFromUntracked covers the second missing field.
//
// `dirty_paths: 16` fuses two facts with different consequences: a modified
// tracked file still has its committed version in the object store, while an
// untracked file is on no branch, in no stash and on no remote — qbe37's tree
// was the ONLY copy of a 1450-line package that way. A consumer that cannot tell
// them apart has to open the tree, which is the by-hand reconstruction this
// event replaces.
func TestPreservedEventSplitsModifiedFromUntracked(t *testing.T) {
	const wantModified, wantUntracked = 2, 3
	ev := preservedEvent(t, func(t *testing.T, repo, wt string) {
		// seed.txt is the one file wtRepo commits, so it is the only tracked
		// path available to modify; a second comes from committing one more.
		if err := os.WriteFile(filepath.Join(wt, "seed.txt"), []byte("edited\n"), 0644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, wt, "commit", "-qam", "second tracked file setup")
		if err := os.WriteFile(filepath.Join(wt, "seed.txt"), []byte("edited again\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, "tracked2.txt"), []byte("v1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, wt, "add", "tracked2.txt")
		gitIn(t, wt, "commit", "-qm", "tracked2")
		if err := os.WriteFile(filepath.Join(wt, "tracked2.txt"), []byte("v2\n"), 0644); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < wantUntracked; i++ {
			name := "untracked" + strconv.Itoa(i) + ".go"
			if err := os.WriteFile(filepath.Join(wt, name), []byte("package x\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	})

	modified := intDetail(t, ev, "modified_paths")
	untracked := intDetail(t, ev, "untracked_paths")
	total := intDetail(t, ev, "dirty_paths")

	if modified != wantModified {
		t.Errorf("modified_paths = %d, want %d", modified, wantModified)
	}
	if untracked != wantUntracked {
		t.Errorf("untracked_paths = %d, want %d — this is the count that makes a preserved "+
			"tree urgent: an untracked path has no copy on any branch, stash or remote",
			untracked, wantUntracked)
	}
	if modified+untracked != total {
		t.Errorf("modified(%d) + untracked(%d) = %d, want dirty_paths %d — a split that does "+
			"not sum silently loses paths", modified, untracked, modified+untracked, total)
	}
}

// TestUnreadableBranchIsReportedNotOmitted is the control that keeps this fix
// from reproducing, one layer down, the defect it repairs.
//
// The tempting shape is to set `branch` when the read works and say nothing when
// it does not. That makes an unreadable branch and an unimplemented field the
// same artifact to every consumer — and "the field is missing exactly when
// something went wrong" is precisely how three days of preservations became
// unqueryable in the first place. So a failed read is REPORTED, the same rule
// workItemLine already applies to a missing work item.
//
// The undetermined path is where this actually bites: `git status` has already
// failed there, and rev-parse normally fails for the same underlying reason.
func TestUnreadableBranchIsReportedNotOmitted(t *testing.T) {
	ev := preservedEvent(t, func(t *testing.T, repo, wt string) {
		// Break the worktree's link to the repo: status and rev-parse both fail.
		if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /nonexistent\n"), 0644); err != nil {
			t.Fatal(err)
		}
	})

	if ev.Details["outcome"] != "undetermined" {
		t.Fatalf("outcome = %v, want undetermined — this fixture is meant to break the "+
			"status read", ev.Details["outcome"])
	}
	msg, ok := ev.Details["branch_error"].(string)
	if !ok || msg == "" {
		t.Fatalf("details.branch_error missing on an unreadable tree: %+v — a branch key "+
			"that merely disappears is indistinguishable from one nobody implemented",
			ev.Details)
	}
	if _, both := ev.Details["branch"]; both {
		t.Errorf("branch AND branch_error both present (%v) — they are alternatives, and a "+
			"consumer reading `branch` must be able to trust it was actually read",
			ev.Details["branch"])
	}
	// The counts stay absent here, because nothing was counted. Reporting 0
	// would assert a clean tree, which is the claim mg-4d45 exists to prevent.
	for _, k := range []string{"dirty_paths", "modified_paths", "untracked_paths"} {
		if _, present := ev.Details[k]; present {
			t.Errorf("%s present on an undetermined tree (%v) — we did not read the tree, "+
				"and a zero here reads as 'clean', which is a claim nobody established",
				k, ev.Details[k])
		}
	}
}

// gitIn runs a git command inside a worktree, failing the test if it errors.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// intDetail reads a numeric details field, which survives the JSONL round trip
// as a float64.
func intDetail(t *testing.T, ev events.Event, key string) int {
	t.Helper()
	v, ok := ev.Details[key].(float64)
	if !ok {
		t.Fatalf("details.%s missing or not numeric: %+v", key, ev.Details)
	}
	return int(v)
}
