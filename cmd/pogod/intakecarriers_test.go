package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The intake watcher's carrier source must carry a ref cache, and the cache
// must survive the way main() uses it: src.Carriers is a method value taken off
// a struct copy on every sample. Driven against a counting stub so what is
// asserted is the forks, not the field.
//
// Before this test the only evidence for main.go's cache wiring was that it was
// there to read; deleting `Cache:` from the literal compiled and passed the
// whole suite (review mg-66cb of drellem2/pogo#194).
func TestIntakeCarrierSourceCachesAcrossPasses(t *testing.T) {
	src := newIntakeCarrierSource()
	if src.Cache == nil {
		t.Fatal("the intake carrier source has no ref cache; every 15m pass forks `mg show` for the whole store (drellem2/pogo#179)")
	}

	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "mg")
	stub := `#!/bin/sh
echo "$*" >> '` + log + `'
case "$*" in
  *"list --status=archived"*) echo '{"id":"mg-a","status":"archived","mtime":"2026-01-01T00:00:00Z"}'
                              echo '{"id":"mg-b","status":"archived","mtime":"2026-01-02T00:00:00Z"}' ;;
  *list*) ;;
  *"show mg-a"*) echo '{"id":"mg-a","status":"archived","body":"gh: drellem2/pogo#1"}' ;;
  *"show mg-b"*) echo '{"id":"mg-b","status":"archived","body":"nothing"}' ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	// Only the fields a test must set to stay off the real binary and store; the
	// cache is the one newIntakeCarrierSource built.
	src.Bin = bin
	src.Root = filepath.Join(dir, "store")

	shows := func() int {
		t.Helper()
		raw, err := os.ReadFile(log)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(log, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(" "+line+" ", " show ") {
				n++
			}
		}
		return n
	}

	// Each pass binds the method value afresh off the captured struct, exactly
	// as main()'s Source closure does.
	for pass, want := range []int{2, 0, 0} {
		carriers := src.Carriers
		refs, scanned, bad, err := carriers()
		if err != nil || scanned != 2 || len(bad) != 0 || len(refs) != 1 {
			t.Fatalf("pass %d: refs=%+v scanned=%d bad=%v err=%v", pass+1, refs, scanned, bad, err)
		}
		if got := shows(); got != want {
			t.Errorf("pass %d forked %d `mg show`s, want %d — an unchanged store must be answered from the cache", pass+1, got, want)
		}
	}
}

// newIntakeCarrierSource is only worth testing if main() is what calls it, and
// if the source it returns is the one the watcher's Collect scans. main() is not
// callable from a test, so this is asserted against the source — the technique
// TestMainConsultsTheArmingDecision uses for the same reason.
func TestMainScansWithTheCachedIntakeSource(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	body := string(raw)
	const build = "src := newIntakeCarrierSource()"
	const scan = "ghintake.Collect(repos, ghintake.GHOpenIssues, src.Carriers,"
	b := strings.Index(body, build)
	if b < 0 {
		t.Fatalf("main.go does not build the intake source with %q; the cache wiring is unasserted", build)
	}
	c := strings.Index(body[b:], scan)
	if c < 0 {
		t.Fatalf("main.go builds the cached intake source but no %q follows it; the watcher scans something else", scan)
	}
	if between := body[b+len(build) : b+c]; strings.Contains(between, "src :=") || strings.Contains(between, "src = ") {
		t.Errorf("main.go rebinds src between newIntakeCarrierSource() and the Collect call, so the watcher may not scan the cached source")
	}
}
