package ghpr

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeGH puts a stub `gh` running script first on PATH for the test.
func fakeGH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestLookup covers the gh output states Lookup distinguishes. "No PR" and
// "could not ask" must stay different answers: one caller suppresses on the
// first and must alert on the second (strandedwork.Finding.CheckOpenPR).
func TestLookup(t *testing.T) {
	dir := t.TempDir()

	fakeGH(t, `echo '{"state":"OPEN","number":42}'`)
	if n, state, err := Lookup(dir, "b", time.Minute); err != nil || n != 42 || state != "OPEN" {
		t.Errorf("open PR: got (%d, %s, %v), want (42, OPEN, nil)", n, state, err)
	}
	if n, err := OpenNumber(dir, "b", time.Minute); err != nil || n != 42 {
		t.Errorf("OpenNumber, open PR: got (%d, %v), want (42, nil)", n, err)
	}

	fakeGH(t, `echo '{"state":"MERGED","number":42}'`)
	if n, err := OpenNumber(dir, "b", time.Minute); err != nil || n != 0 {
		t.Errorf("OpenNumber, merged PR: got (%d, %v), want (0, nil)", n, err)
	}

	fakeGH(t, `echo 'no pull requests found for branch "b"' >&2; exit 1`)
	if n, state, err := Lookup(dir, "b", time.Minute); err != nil || n != 0 || state != "" {
		t.Errorf("no PR: got (%d, %q, %v), want (0, \"\", nil)", n, state, err)
	}

	fakeGH(t, `echo "gh: could not determine base repo" >&2; exit 1`)
	if _, err := OpenNumber(dir, "b", time.Minute); err == nil {
		t.Error("hard failure: want an error, got nil")
	}

	fakeGH(t, `echo 'not json'`)
	if _, err := OpenNumber(dir, "b", time.Minute); err == nil {
		t.Error("bad JSON: want an error, got nil")
	}
}

// TestLookupIsBounded: a gh that hangs is an error at the timeout, not a hang —
// the stranded-work caller sits on the polecat-stop path.
func TestLookupIsBounded(t *testing.T) {
	fakeGH(t, `exec sleep 30`)
	start := time.Now()
	if _, err := OpenNumber(t.TempDir(), "b", 300*time.Millisecond); err == nil {
		t.Error("hung gh: want an error, got nil")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("hung gh took %s, want about the 300ms timeout", d)
	}
}
