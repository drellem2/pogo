package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The carrier re-read watcher's source must carry an item cache, and the cache
// must survive the way main() uses it: src.Carriers is a method value taken off
// a struct copy. Driven against a counting stub so what is asserted is the
// forks, not the field (mg-e353; the sibling of
// TestIntakeCarrierSourceCachesAcrossPasses).
func TestCarrierDriftSourceCachesAcrossPasses(t *testing.T) {
	src := newCarrierDriftSource(true)
	if src.Cache == nil {
		t.Fatal("the carrier re-read source has no item cache; every pass forks `mg show` for every live item (drellem2/pogo#179)")
	}
	if !src.IncludeShelved {
		t.Fatal("newCarrierDriftSource dropped the include-shelved setting")
	}

	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "mg")
	stub := `#!/bin/sh
echo "$*" >> '` + log + `'
case "$*" in
  *"list --status=available"*) echo '{"id":"mg-a","status":"available","mtime":"2026-01-01T00:00:00Z"}' ;;
  *"list --status=shelved"*)   echo '{"id":"mg-b","status":"shelved","mtime":"2026-01-02T00:00:00Z"}' ;;
  *list*) ;;
  *"show mg-a"*) printf '%s\n' '{"id":"mg-a","status":"available","body":"workflow: gh-issue\ngh: drellem2/pogo#1"}' ;;
  *"show mg-b"*) echo '{"id":"mg-b","status":"shelved","body":"nothing"}' ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
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

	for pass, want := range []int{2, 0, 0} {
		carriers := src.Carriers
		cs, scanned, err := carriers()
		if err != nil || scanned != 2 || len(cs) != 1 {
			t.Fatalf("pass %d: carriers=%+v scanned=%d err=%v", pass+1, cs, scanned, err)
		}
		if got := shows(); got != want {
			t.Errorf("pass %d forked %d `mg show`s, want %d — an unchanged store must be answered from the cache", pass+1, got, want)
		}
	}
}

// newCarrierDriftSource is only worth testing if main() is what calls it, and
// if the source it returns is the one the watcher scans.
func TestMainScansWithTheCachedCarrierDriftSource(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	body := string(raw)
	const build = "src := newCarrierDriftSource(cfg.CarrierDrift.IncludeShelved)"
	const scan = "Source:  src.Carriers,"
	b := strings.Index(body, build)
	if b < 0 {
		t.Fatalf("main.go does not build the carrier re-read source with %q; the cache wiring is unasserted", build)
	}
	c := strings.Index(body[b:], scan)
	if c < 0 {
		t.Fatalf("main.go builds the cached carrier re-read source but no %q follows it", scan)
	}
	if between := body[b+len(build) : b+c]; strings.Contains(between, "src :=") || strings.Contains(between, "src = ") {
		t.Errorf("main.go rebinds src between newCarrierDriftSource() and the watcher, so it may not scan the cached source")
	}
}
