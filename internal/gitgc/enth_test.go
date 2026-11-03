package gitgc

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Enth checkouts under gitgc's guard (enth-vcs slice 5, mg-cc5cd).
//
// The `enth` these tests run is a stand-in on PATH that answers `enth status
// DIR` from DIR/.enth/fake-status (stdout) and DIR/.enth/fake-exit, so the
// guard's arms are exercised in every build, with or without a real enth.
// internal/vcs/enthvcs's enthd tests run the same Dirty against the real one.

func fakeEnthOnPath(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
[ "$1" = status ] || { echo '{"type":"error","code":"usage","message":"fake enth: status only"}' >&2; exit 2; }
d=$2
code=$(cat "$d/.enth/fake-exit" 2>/dev/null || echo 0)
if [ "$code" != 0 ]; then echo '{"type":"error","code":"io_error","message":"fake failure"}' >&2; exit "$code"; fi
cat "$d/.enth/fake-status"
`
	if err := os.WriteFile(filepath.Join(bin, "enth"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// enthTree makes an enth checkout at dir whose index holds a.txt, and whose
// fake status reports ops.
func enthTree(t *testing.T, dir string, ops ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".enth"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := `{"format":"enth-checkout","version":1,"base":"b0","origin":{"remote":"http://127.0.0.1:1","repo":"o/r"},"paths":[]}
{"path":"a.txt","mode":"file","blob":"x","size":1,"mtime":1,"ino":1}
`
	if err := os.WriteFile(filepath.Join(dir, ".enth", "checkout"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	setEnthStatus(t, dir, ops...)
}

func setEnthStatus(t *testing.T, dir string, ops ...string) {
	t.Helper()
	status := `{"type":"status","dir":"` + dir + `","base":"b0","origin":{},"paths":[],"ops":` + strconv.Itoa(len(ops)) + "}\n"
	for _, op := range ops {
		status += op + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, ".enth", "fake-status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeDirtyReadsAnEnthCheckout(t *testing.T) {
	fakeEnthOnPath(t)
	dir := filepath.Join(t.TempDir(), "p1")
	enthTree(t, dir,
		`{"path":"a.txt","op":"put","blob":"y","mode":"file"}`,
		`{"path":"new.go","op":"put","blob":"z","mode":"file"}`,
		`{"path":"gone.txt","op":"delete"}`)
	dirty, files, err := WorktreeDirty(dir)
	if err != nil || !dirty {
		t.Fatalf("WorktreeDirty = %v %v %v, want dirty", dirty, files, err)
	}
	if got := strings.Join(files, "|"); got != " M a.txt|?? new.go| D gone.txt" {
		t.Errorf("files = %q", got)
	}
	// The porcelain shape is what the guard's counts read.
	if m, u := countPorcelain(files); m != 2 || u != 1 {
		t.Errorf("countPorcelain = %d modified, %d untracked; want 2, 1", m, u)
	}

	setEnthStatus(t, dir)
	if dirty, files, err := WorktreeDirty(dir); err != nil || dirty || len(files) != 0 {
		t.Errorf("clean enth checkout: %v %v %v", dirty, files, err)
	}
}

func TestRemoveWorktreeGuardsAnEnthCheckout(t *testing.T) {
	fakeEnthOnPath(t)
	root := t.TempDir()

	dirty := filepath.Join(root, "dirty")
	enthTree(t, dirty, `{"path":"new.go","op":"put","blob":"z","mode":"file"}`)
	var dwe *DirtyWorktreeError
	if err := RemoveWorktree("/no/such/repo", dirty, OwnerUnproven); !errors.As(err, &dwe) {
		t.Fatalf("dirty enth checkout: RemoveWorktree = %v, want *DirtyWorktreeError", err)
	}
	if _, err := os.Stat(filepath.Join(dirty, "a.txt")); err != nil {
		t.Fatalf("a refused removal touched the tree: %v", err)
	}

	broken := filepath.Join(root, "broken")
	enthTree(t, broken)
	os.WriteFile(filepath.Join(broken, ".enth", "fake-exit"), []byte("4"), 0o644)
	var uwe *UndeterminedWorktreeError
	if err := RemoveWorktree("/no/such/repo", broken, OwnerUnproven); !errors.As(err, &uwe) {
		t.Fatalf("enth status failing: RemoveWorktree = %v, want *UndeterminedWorktreeError (cannot tell is never clean)", err)
	}
	if _, err := os.Stat(broken); err != nil {
		t.Fatalf("an undetermined tree was removed: %v", err)
	}

	// Clean: removed, with no git durability question asked (there is no
	// .git; enth Changes are durable on enthd).
	clean := filepath.Join(root, "clean")
	enthTree(t, clean)
	if err := RemoveWorktree("/no/such/repo", clean, OwnerUnproven); err != nil {
		t.Fatalf("clean enth checkout: RemoveWorktree = %v", err)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Fatalf("clean enth checkout not removed: %v", err)
	}
}

// TestSweepDoesNotTreatAnEnthCheckoutAsAnOrphan: phase 1b removes a .git-less
// polecat dir with a concluded ticket WITHOUT a dirty check, because an
// orphan has nothing to check. An enth checkout has no .git either, and it
// does have uncommitted edits to lose — so it must get the guard. The plain
// orphan beside it is the positive control that phase 1b still runs.
func TestSweepDoesNotTreatAnEnthCheckoutAsAnOrphan(t *testing.T) {
	fakeEnthOnPath(t)
	r := newTestRepo(t)
	polecats := t.TempDir()
	dirtyCo := filepath.Join(polecats, "aaaa")
	enthTree(t, dirtyCo, `{"path":"new.go","op":"put","blob":"z","mode":"file"}`)
	cleanCo := filepath.Join(polecats, "bbbb")
	enthTree(t, cleanCo)
	orphan := filepath.Join(polecats, "cccc")
	os.MkdirAll(orphan, 0o755)
	os.WriteFile(filepath.Join(orphan, "leftover"), []byte("x"), 0o644)

	res, err := Sweep(Options{
		Repo:        r.dir,
		Tickets:     TicketIndex{"mg-aaaa": TicketDone, "mg-bbbb": TicketDone, "mg-cccc": TicketDone},
		PolecatsDir: polecats,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("sweep errors: %v", res.Errors)
	}
	if _, err := os.Stat(filepath.Join(dirtyCo, "a.txt")); err != nil {
		t.Errorf("a DIRTY enth checkout with a done ticket was removed: %v", err)
	}
	if _, err := os.Stat(cleanCo); !os.IsNotExist(err) {
		t.Errorf("a clean enth checkout with a done ticket was kept: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("positive control: the plain orphan was not removed: %v", err)
	}
	kept := false
	for _, k := range res.WorktreesKept {
		if k.Path == dirtyCo {
			kept = true
		}
	}
	if !kept {
		t.Errorf("the dirty enth checkout is not reported as kept: %+v", res.WorktreesKept)
	}
}
