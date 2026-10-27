package workitem

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindFromAvailable is the lookup the dispatch gate depends on: an item in
// available/, found by id, with its assignee readable.
func TestFindFromAvailable(t *testing.T) {
	root := setupTestWorkspace(t)

	item, found, err := FindFrom(root, "mg-0001")
	if err != nil {
		t.Fatalf("FindFrom: %v", err)
	}
	if !found {
		t.Fatal("mg-0001 is in available/ but was not found")
	}
	if item.ID != "mg-0001" {
		t.Errorf("ID = %q, want mg-0001", item.ID)
	}
	if item.Status != "available" {
		t.Errorf("Status = %q, want available", item.Status)
	}
	if item.Title != "Add user authentication" {
		t.Errorf("Title = %q", item.Title)
	}
}

// TestFindFromReadsAssignee is the field the gate actually decides on, so it is
// asserted directly rather than inferred from the struct being populated.
func TestFindFromReadsAssignee(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "available"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeItem(t, filepath.Join(root, "available", "mg-gate.md"), `---
id: mg-gate
type: task
assignee: human
---
# Manual QA pass
`)

	item, found, err := FindFrom(root, "mg-gate")
	if err != nil || !found {
		t.Fatalf("FindFrom = (%v, %v, %v), want found", item, found, err)
	}
	if item.Assignee != "human" {
		t.Errorf("Assignee = %q, want human", item.Assignee)
	}
}

// TestFindFromClaimedWithPidSuffix is the case ListFrom structurally cannot see.
// A claim renames the file to <id>.md.<pid>, which ListFrom's ".md" suffix filter
// skips — so a by-id lookup built on it would report a claimed item as ABSENT.
// For a gate, "absent" means "not gated", so this is the difference between the
// gate holding and the gate silently failing open.
func TestFindFromClaimedWithPidSuffix(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "claimed"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeItem(t, filepath.Join(root, "claimed", "mg-clmd.md.4242"), `---
id: mg-clmd
type: task
assignee: parked
---
# Claimed and parked
`)

	item, found, err := FindFrom(root, "mg-clmd")
	if err != nil {
		t.Fatalf("FindFrom: %v", err)
	}
	if !found {
		t.Fatal("claimed item with a .<pid> suffix was not found — the prefix fallback is not working")
	}
	if item.Assignee != "parked" || item.Status != "claimed" {
		t.Errorf("item = %+v, want assignee=parked status=claimed", item)
	}

	// Confirm the premise rather than trusting it: ListFrom must NOT see it.
	items, err := ListFrom(root, "claimed")
	if err != nil {
		t.Fatalf("ListFrom: %v", err)
	}
	for _, it := range items {
		if it.ID == "mg-clmd" {
			t.Fatal("ListFrom now sees .md.<pid> files; FindFrom's prefix fallback " +
				"rationale needs revisiting, not deleting")
		}
	}
}

// TestFindFromMissingIsNotAnError — a caller holding an id from an unknown source
// asks routinely about items that do not exist. The dispatch gate relies on this
// being (false, nil) and not an error, because it treats an error as loud.
func TestFindFromMissingIsNotAnError(t *testing.T) {
	root := setupTestWorkspace(t)

	item, found, err := FindFrom(root, "mg-nope")
	if err != nil {
		t.Errorf("FindFrom on a missing id returned an error: %v", err)
	}
	if found {
		t.Errorf("found a nonexistent item: %+v", item)
	}
}

// TestFindFromAbsentWorkspace covers a store that does not exist at all (a
// sandbox daemon, a fresh machine). Not an error, nothing found.
func TestFindFromAbsentWorkspace(t *testing.T) {
	_, found, err := FindFrom(filepath.Join(t.TempDir(), "no-such-workspace"), "mg-0001")
	if err != nil {
		t.Errorf("FindFrom on an absent workspace returned an error: %v", err)
	}
	if found {
		t.Error("found an item in a workspace that does not exist")
	}
}

// TestFindFromRejectsPathEscapes keeps an unvalidated id from walking out of the
// workspace. The id reaching the gate comes off an HTTP request body.
func TestFindFromRejectsPathEscapes(t *testing.T) {
	root := setupTestWorkspace(t)

	// A readable file outside the workspace that a traversal could reach.
	outside := filepath.Join(filepath.Dir(root), "escaped.md")
	writeItem(t, outside, `---
id: escaped
assignee: human
---
# Should never be reachable by id
`)

	for _, id := range []string{
		"",
		"..",
		"../escaped",
		"../../etc/passwd",
		`..\escaped`,
		"available/mg-0001",
	} {
		t.Run("id="+id, func(t *testing.T) {
			item, found, err := FindFrom(root, id)
			if err != nil {
				t.Errorf("FindFrom(%q) errored: %v", id, err)
			}
			if found {
				t.Errorf("FindFrom(%q) resolved to %+v; ids must not traverse", id, item)
			}
		})
	}
}

// TestFindFromPrefersEarliestStatus pins the search order when the same id
// somehow exists in two status directories (a torn move). available/ wins, which
// is the copy dispatch would be acting on.
func TestFindFromPrefersEarliestStatus(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"available", "done"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeItem(t, filepath.Join(root, "available", "mg-dup.md"), `---
id: mg-dup
assignee: human
---
# Available copy
`)
	writeItem(t, filepath.Join(root, "done", "mg-dup.md"), `---
id: mg-dup
assignee: bob
---
# Done copy
`)

	item, found, err := FindFrom(root, "mg-dup")
	if err != nil || !found {
		t.Fatalf("FindFrom = (%v, %v)", found, err)
	}
	if item.Status != "available" || item.Assignee != "human" {
		t.Errorf("item = %+v, want the available/ copy", item)
	}
}

// TestFindFiledFromReadsShelvedAndSnooze is the lookup wakewatch makes before
// re-pointing an assignment: it must reach shelved/, which FindLiveFrom
// deliberately does not, and it must tell a snoozed pending item from one
// waiting on its depends.
func TestFindFiledFromReadsShelvedAndSnooze(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"available", "claimed", "pending", "shelved"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeItem(t, filepath.Join(root, "shelved", "mg-shlf.md"), "---\nid: mg-shlf\nassignee: pm-pogo\n---\n# Shelved\n")
	writeItem(t, filepath.Join(root, "pending", "mg-snz.md"), "---\nid: mg-snz\nsnooze: 2026-11-01T09:00:00Z\n---\n# Snoozed\n")
	writeItem(t, filepath.Join(root, "pending", "mg-dep.md"), "---\nid: mg-dep\ndepends: [mg-shlf]\n---\n# Pending\n")

	it, found, err := FindFiledFrom(root, "mg-shlf")
	if err != nil || !found || it.Status != "shelved" || it.Assignee != "pm-pogo" {
		t.Errorf("FindFiledFrom(mg-shlf) = (%+v, %v, %v), want shelved", it, found, err)
	}
	// The control: the depends gate's lookup must still not see shelved/.
	if _, found, err := FindLiveFrom(root, "mg-shlf"); err != nil || found {
		t.Errorf("FindLiveFrom(mg-shlf) found = %v, err = %v; shelved/ must stay outside it", found, err)
	}
	it, found, _ = FindFiledFrom(root, "mg-snz")
	if !found || it.Status != "pending" || it.Snooze != "2026-11-01T09:00:00Z" {
		t.Errorf("FindFiledFrom(mg-snz) = %+v, want pending with snooze", it)
	}
	it, found, _ = FindFiledFrom(root, "mg-dep")
	if !found || it.Status != "pending" || it.Snooze != "" {
		t.Errorf("FindFiledFrom(mg-dep) = %+v, want pending without snooze", it)
	}
}
