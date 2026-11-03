package gitgc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// fakeEnthCheckout writes an enth checkout's state file and pogod's record
// into dir, as a slice 5 spawn leaves them.
func fakeEnthCheckout(t *testing.T, dir, repo string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, ".enth"), 0o755)
	os.WriteFile(filepath.Join(dir, enthvcs.StateRelPath),
		[]byte(`{"format":"enth-checkout","version":1,"base":"b0","origin":{"remote":"http://127.0.0.1:1","repo":"o/r"},"paths":[]}`+"\n"+
			`{"path":"a.txt","mode":"file"}`+"\n"), 0o644)
	if err := enthvcs.WriteRecord(dir, enthvcs.Record{Repo: repo, Remote: "http://127.0.0.1:1", EnthRepo: "o/r", Ref: "refs/heads/main", Base: "b0"}); err != nil {
		t.Fatal(err)
	}
}

// TestScanPreservedListsEnthCheckouts is pm-pogo's carry-over 2 (mg-30da0): the
// preserved scan used to count every enth checkout as "not a worktree" (no
// .git), so a dirty one was retained by gc and absent from the listing of
// retained trees. A stub enth answers `enth status` (a new file in the dirty
// tree, nothing in the clean one).
func TestScanPreservedListsEnthCheckouts(t *testing.T) {
	root := t.TempDir()
	polecats := filepath.Join(root, "polecats")
	dirty, clean, shell := filepath.Join(polecats, "pdirty"), filepath.Join(polecats, "pclean"), filepath.Join(polecats, "pshell")
	for _, d := range []string{dirty, clean, shell} {
		os.MkdirAll(d, 0o755)
	}
	fakeEnthCheckout(t, dirty, "/src/enthproj")
	fakeEnthCheckout(t, clean, "/src/enthproj")
	os.WriteFile(filepath.Join(dirty, "new.txt"), []byte("n"), 0o644)

	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	stub := `#!/bin/sh
[ "$1" = status ] || exit 2
printf '%s\n' '{"type":"status","ops":0}'
case "$2" in *pdirty) printf '%s\n' '{"path":"new.txt","op":"put","blob":"x","mode":"file"}' ;; esac
`
	os.WriteFile(filepath.Join(bin, "enth"), []byte(stub), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	rep, err := ScanPreserved(PreservedScanOptions{PolecatsDir: polecats, Tickets: TicketIndex{}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.NotWorktreeCount != 1 {
		t.Errorf("not-a-worktree = %d, want 1 (only the shell with neither marker)", rep.NotWorktreeCount)
	}
	if rep.CleanCount != 1 {
		t.Errorf("clean = %d, want 1 (the clean enth checkout)", rep.CleanCount)
	}
	if len(rep.Retained) != 1 {
		t.Fatalf("retained = %+v, want the dirty enth checkout", rep.Retained)
	}
	tr := rep.Retained[0]
	if tr.Path != dirty || tr.Repo != "/src/enthproj" || tr.Outcome != "preserved" || strings.Join(tr.Files, ",") != "?? new.txt" ||
		tr.Branch != "" || tr.BranchError != "" || tr.Commits != nil || tr.ScaffoldingOnly {
		t.Errorf("retained tree = %+v", tr)
	}
	if len(rep.Errors) != 0 {
		t.Errorf("errors = %v; no git probe may run in an enth tree", rep.Errors)
	}
	// The repo filter applies to an enth tree as to a git one.
	rep, _ = ScanPreserved(PreservedScanOptions{PolecatsDir: polecats, Tickets: TicketIndex{}, Repo: "/src/other"})
	if len(rep.Retained) != 0 || rep.OtherRepoCount != 2 {
		t.Errorf("filtered: retained %d other %d, want 0 and 2", len(rep.Retained), rep.OtherRepoCount)
	}
}
