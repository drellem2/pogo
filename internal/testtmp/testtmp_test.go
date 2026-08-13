package testtmp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The load-bearing case in this file is TestReapKeepsALiveOwnersEntry.
//
// Every other assertion here is about reclaiming disk, and the worst outcome of
// getting one wrong is that the leak this package exists to stop carries on —
// visible, measurable, and no worse than before it was written. Getting the LIVE
// direction wrong is a different kind of failure: this box runs several polecats
// and a refinery gate at once, so a sweep that deletes a running suite's
// fixtures surfaces as a branch defect against code that is fine, which is the
// exact shape of mg-b41f. A sweep observed only deleting things has not been
// observed working.

// TestReapKeepsALiveOwnersEntry — the direction that must never regress.
func TestReapKeepsALiveOwnersEntry(t *testing.T) {
	root := t.TempDir()
	// This process is the one pid the test can be certain is alive.
	live := filepath.Join(root, entryName("witness", os.Getpid(), 1))
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	// Old enough that any age-based rule would delete it. Ownership must win.
	old := time.Now().Add(-100 * StaleAfter)
	if err := os.Chtimes(live, old, old); err != nil {
		t.Fatal(err)
	}

	Reap(root)

	if _, err := os.Stat(live); err != nil {
		t.Fatalf("Reap deleted a LIVE process's directory (%s): %v\n"+
			"This is the failure the package exists to prevent arriving by a new route: "+
			"a running suite loses its fixtures and reports a branch defect.", live, err)
	}
}

// TestReapRemovesADeadOwnersEntry is the reclaiming direction. Without it the
// package is a rename of the problem.
func TestReapRemovesADeadOwnersEntry(t *testing.T) {
	root := t.TempDir()
	dead := filepath.Join(root, entryName("events", deadPID(t), 1))
	if err := os.MkdirAll(filepath.Join(dead, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Brand new, so nothing here is reclaimed by age. Ownership is the reason.
	Reap(root)

	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("Reap kept a dead process's directory (%s): stat err = %v", dead, err)
	}
}

// TestReapAgesOutOnlyEntriesThatCarryNoPID pins the fallback branch in both
// directions. An entry this package did not name is reclaimed by age and by
// nothing else — reaping it eagerly would let the sweep delete whatever else a
// future caller decides to keep in Root().
func TestReapAgesOutOnlyEntriesThatCarryNoPID(t *testing.T) {
	root := t.TempDir()
	recent := filepath.Join(root, "not-one-of-ours")
	stale := filepath.Join(root, "also-not-ours")
	for _, d := range []string{recent, stale} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-StaleAfter - time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	Reap(root)

	if _, err := os.Stat(recent); err != nil {
		t.Errorf("Reap deleted an unowned entry that is younger than StaleAfter (%s): %v", recent, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("Reap kept an unowned entry older than StaleAfter (%s): stat err = %v", stale, err)
	}
}

// TestReapTouchesNothingOutsideTheRootItWasGiven. The sweep runs on a box whose
// $TMPDIR holds tens of thousands of other people's directories; "it only ever
// looks inside Root()" is the property that makes it safe to run unattended.
func TestReapTouchesNothingOutsideTheRootItWasGiven(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	// A sibling of root, named exactly like a reapable entry, owned by a dead
	// pid, and old. Everything about it says "delete me" except its location.
	sibling := filepath.Join(parent, entryName("events", deadPID(t), 7))
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-100 * StaleAfter)
	if err := os.Chtimes(sibling, old, old); err != nil {
		t.Fatal(err)
	}

	Reap(root)

	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("Reap left its root and deleted a sibling (%s): %v", sibling, err)
	}
}

// TestDirNestsEverythingUnderOneRoot is the acceptance criterion in miniature:
// $TMPDIR's entry count must stop growing with the number of directories a test
// binary needs.
func TestDirNestsEverythingUnderOneRoot(t *testing.T) {
	root, err := Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	for _, purpose := range []string{"witness", "events", "witness"} {
		dir, err := Dir(purpose)
		if err != nil {
			t.Fatalf("Dir(%q): %v", purpose, err)
		}
		if filepath.Dir(dir) != root {
			t.Errorf("Dir(%q) = %s, which is not directly inside Root() = %s", purpose, dir, root)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("Dir(%q) returned a path that does not exist: %v", purpose, err)
		}
		if pid, ok := ownerPID(filepath.Base(dir)); !ok || pid != os.Getpid() {
			t.Errorf("Dir(%q) = %s: name does not encode this process's pid (%d), so Reap could "+
				"only ever age it out", purpose, dir, os.Getpid())
		}
	}
}

// TestDirRefusesAPurposeReapCouldNotParse. A dot in the label silently converts
// an owned entry into one the sweep can only age out — the failure is not the
// bad name, it is that the name still works.
func TestDirRefusesAPurposeReapCouldNotParse(t *testing.T) {
	for _, purpose := range []string{"", "a.b", "a/b", "sandbox.agent"} {
		if _, err := Dir(purpose); err == nil {
			t.Errorf("Dir(%q) was accepted; a name Reap cannot parse must be refused at the call", purpose)
		}
	}
}

// TestOwnerPIDReadsBackWhatEntryNameWrote — the two halves of the naming are the
// only contract between a process that creates a directory and a process that
// decides whether to delete it.
func TestOwnerPIDReadsBackWhatEntryNameWrote(t *testing.T) {
	name := entryName("sandbox-agent", 4242, 9)
	if !strings.HasPrefix(name, "sandbox-agent.") {
		t.Errorf("entryName = %q; the purpose must lead so `ls` groups by what a directory is", name)
	}
	pid, ok := ownerPID(name)
	if !ok || pid != 4242 {
		t.Errorf("ownerPID(%q) = %d, %v; want 4242, true", name, pid, ok)
	}
	for _, bad := range []string{"witness", "witness.notanumber.1", "witness.0.1", "witness.-3.1", "a.1.2.3"} {
		if _, ok := ownerPID(bad); ok {
			t.Errorf("ownerPID(%q) claimed an owner; an unparseable name must fall to the age rule", bad)
		}
	}
}

// TestDirClearsADirectoryLeftByARecycledPID.
//
// The remedy is an artifact of the same kind as the defect, so it is subject to
// it: the sweep keeps every entry whose pid is alive, and once this process has
// been handed a recycled pid, a DEAD namesake's directory reads as live and is
// kept forever. Reusing it would hand this binary a stale witness store or
// events log, and the phantom records would be read as a defect in whichever
// test found them — mg-da48's symptom, reached from the fix for mg-de3c.
func TestDirClearsADirectoryLeftByARecycledPID(t *testing.T) {
	root, err := Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	// Stage exactly what a namesake leaves behind: the name Dir is about to
	// mint, already present, with state inside it.
	next := entryName("recycled", os.Getpid(), seq.Load()+1)
	stale := filepath.Join(root, next)
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	ghost := filepath.Join(stale, "events.log")
	if err := os.WriteFile(ghost, []byte("a dead run's records\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir, err := Dir("recycled")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if dir != stale {
		t.Skipf("another Dir call raced this one; got %s, wanted the staged %s", dir, stale)
	}
	if _, err := os.Stat(ghost); !os.IsNotExist(err) {
		t.Fatalf("Dir handed back a recycled pid's directory with the dead run's state still in it "+
			"(%s): stat err = %v", ghost, err)
	}
}

// TestRootRefusesASymlink. os.TempDir falls back to a world-writable /tmp when
// TMPDIR is unset, which is the case in CI; MkdirAll follows a link there and
// reports success, so the sweep would go on to delete a tree of somebody else's
// choosing. The refusal has to be explicit, and the only way to observe it is
// against a root this test supplies.
func TestRootRefusesASymlink(t *testing.T) {
	// Root memoises per process, so the check is exercised through a private
	// TMPDIR in a child `go test` rather than by re-entering Root here.
	base := t.TempDir()
	elsewhere := filepath.Join(base, "somebody-elses-tree")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(base, RootName)); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	// Any test name will do: the refusal is raised the first time anything in
	// the child asks for a directory, which is its TestMain — earlier than any
	// assertion could run, and reported through testsandbox's setup-failure
	// banner rather than as a test result. That is the intended shape. A run
	// that cannot get a private root must make no claim about the tree.
	cmd := exec.Command(os.Args[0], "-test.run=TestOwnerPIDReadsBackWhatEntryNameWrote", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "TMPDIR="+base)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("the child accepted a symlinked root; the sweep would delete through it:\n%s", out)
	}
	if !strings.Contains(string(out), "is a symlink") {
		t.Errorf("the child failed, but not with the symlink refusal — so this case is not "+
			"observing what it claims to:\n%s", out)
	}
	if _, err := os.Stat(elsewhere); err != nil {
		t.Errorf("the symlink target was disturbed: %v", err)
	}
}

// deadPID returns the pid of a process that has certainly exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for a throwaway process: %v", err)
	}
	if pidAlive(pid) {
		t.Skipf("pid %d was reused before the assertion could run", pid)
	}
	return pid
}

// TestReapRemovesAReadOnlyModuleCache is the case the sweep was silently losing
// (mg-60eb).
//
// A sandbox root under this package is a fake $HOME, and a test in it that
// shells out to `go build` populates $HOME/go/pkg/mod. Go writes that cache
// READ-ONLY — 0444 files inside 0555 directories — so os.RemoveAll stops at the
// first one with EACCES. Reap swallows errors, which it must (a sweep may never
// be the reason a test fails), and the two together made an unremovable entry
// indistinguishable from a reclaimed one: 148 such roots and 120 MB were sitting
// in $TMPDIR/pogo-test-tmp on this box, every one of them selected for removal
// on every sweep since mg-de3c and removed by none of them.
//
// The shape is reproduced exactly rather than approximated — 0444 file, 0555
// parent — because the parent's missing write bit is what actually blocks the
// unlink, and a fixture with only the file made read-only passes against the
// unfixed code.
func TestReapRemovesAReadOnlyModuleCache(t *testing.T) {
	root := t.TempDir()
	dead := filepath.Join(root, entryName("sandbox-agent", deadPID(t), 1))
	modcache := filepath.Join(dead, "home", "go", "pkg", "mod", "example.com", "dep@v1.0.0")
	if err := os.MkdirAll(modcache, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(modcache, "dep.go")
	if err := os.WriteFile(file, []byte("package dep\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(modcache, 0o555); err != nil {
		t.Fatal(err)
	}
	// t.TempDir's own cleanup hits the same wall, so put the bits back whatever
	// this test concludes.
	t.Cleanup(func() {
		os.Chmod(modcache, 0o700)
		os.Chmod(file, 0o600)
	})

	// The premise, asserted rather than assumed: if a plain RemoveAll could
	// clear this fixture, the test would pass against the unfixed sweep and
	// prove nothing.
	if err := os.RemoveAll(dead); err == nil {
		t.Skip("this runner's os.RemoveAll clears a read-only tree; the case cannot be constructed here")
	}

	Reap(root)

	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("Reap kept a dead owner's read-only module cache (%s): stat err = %v\n"+
			"An entry the sweep cannot remove is one it reports as reclaimed forever.", dead, err)
	}
}

// TestRemoveAllLeavesPermissionsAloneWhenItDoesNotHaveTo. The chmod is a
// last-resort widening of permissions inside a tree already condemned, and it
// must not be the ordinary path — a helper that chmods first and asks later is
// one nobody can reason about at a call site.
func TestRemoveAllLeavesPermissionsAloneWhenItDoesNotHaveTo(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "plain")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(root, "sibling")
	if err := os.MkdirAll(sibling, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sibling, 0o700) })

	if err := RemoveAll(target); err != nil {
		t.Fatalf("RemoveAll(%s) = %v, want nil", target, err)
	}
	fi, err := os.Stat(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o500 {
		t.Errorf("RemoveAll widened permissions outside its argument: sibling is %v, want 0500", fi.Mode().Perm())
	}
}
