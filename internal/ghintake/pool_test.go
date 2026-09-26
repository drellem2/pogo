package ghintake

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Carriers must run its `mg show`s on a fixed pool (mgscan.Pool), not one
// goroutine per item gated by a semaphore — the shape drellem2/pogo#179 profiled
// at 4,038 parked goroutines per pass. Both shapes bound the concurrent FORKS, so
// counting forks cannot tell them apart; counting GOROUTINES while the forks are
// held open can.
//
// The stub's `show` blocks until the test opens a gate, so the scan is frozen
// with its full footprint live. With Workers=2 and 200 items, a pool adds a
// handful of goroutines (two workers plus exec's per-process copiers); a
// goroutine per item adds ~200.
func TestCarriersRunsAFixedPoolNotAGoroutinePerItem(t *testing.T) {
	const items, workers = 200, 2
	dir := t.TempDir()
	gate := filepath.Join(dir, "gate")
	started := filepath.Join(dir, "started")
	var rows strings.Builder
	for i := 0; i < items; i++ {
		fmt.Fprintf(&rows, `{"id":"mg-%04x","status":"archived","mtime":"2026-01-01T00:00:00Z"}`+"\n", i)
	}
	listing := filepath.Join(dir, "listing")
	if err := os.WriteFile(listing, []byte(rows.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := mgStub(t, `
case "$*" in
  *"list --status=archived"*) cat '`+listing+`' ;;
  *list*) ;;
  *show*) echo x >> '`+started+`'
          while [ ! -f '`+gate+`' ]; do sleep 0.01; done
          echo '{"status":"archived","body":"nothing"}' ;;
esac
exit 0
`)
	// Open the gate however this test exits, or 200 stub processes would spin
	// on it for the life of the host.
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })

	src := MGSource{Bin: stub, Root: filepath.Join(dir, "store"), Workers: workers}
	baseline := runtime.NumGoroutine()

	done := make(chan error, 1)
	go func() {
		_, scanned, _, err := src.Carriers()
		if err == nil && scanned != items {
			err = fmt.Errorf("scanned %d items, want %d", scanned, items)
		}
		done <- err
	}()

	// Wait until every worker is inside a blocked `mg show`.
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, _ := os.ReadFile(started)
		if strings.Count(string(raw), "x") >= workers {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the scan returned before any show was held open: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the scan never had %d shows in flight", workers)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Settle: a goroutine-per-item scan would still be starting goroutines.
	time.Sleep(200 * time.Millisecond)
	extra := runtime.NumGoroutine() - baseline
	t.Logf("%d goroutines above baseline with %d-item scan frozen mid-pass", extra, items)

	raw, _ := os.ReadFile(started)
	inFlight := strings.Count(string(raw), "x")

	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if inFlight > workers {
		t.Errorf("%d `mg show`s were in flight with Workers=%d", inFlight, workers)
	}
	// Generous: two workers, the scan goroutine, and a few per live exec.
	if limit := 40; extra > limit {
		t.Errorf("%d goroutines were live during a %d-item scan with Workers=%d, want at most %d — "+
			"the scan is starting a goroutine per item again (drellem2/pogo#179)", extra, items, workers, limit)
	}
}
