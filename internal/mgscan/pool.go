// Package mgscan holds the plumbing shared by pogod's whole-store work-item
// scans — ghintake's carrier census and carrierdrift's live-carrier re-read
// (mg-e353). Each scan holds its OWN Cache; see carrierdrift.ItemCache for why
// the two do not share one.
//
// Both scans do the same thing: list the items they cover with `mg list --json`, then
// fork `mg show <id> --json` per item to read a body `mg list` does not emit. On
// a store of a few thousand items that is a few thousand processes per pass, and
// drellem2/pogo#179 measured what the original shape of that fan-out cost: one
// goroutine per item started up front, all but a handful parked on a semaphore
// (4,038 of 4,247 goroutines in the reporter's profile). This package gives the
// scans a bounded worker pool (Pool) and a per-item result cache keyed on the
// mtime `mg list` already reports (Cache), so an unchanged item costs no fork.
package mgscan

import (
	"runtime"
	"sync"
)

// MaxDefaultWorkers caps DefaultWorkers. The work is process spawns against a
// local store; past a handful of concurrent forks the scan is bound by the
// kernel, not by parallelism.
const MaxDefaultWorkers = 8

// DefaultWorkers is the pool size a scan uses when none is configured: the CPU
// count, clamped to [1, MaxDefaultWorkers].
func DefaultWorkers() int {
	n := runtime.NumCPU()
	if n > MaxDefaultWorkers {
		n = MaxDefaultWorkers
	}
	if n < 1 {
		n = 1
	}
	return n
}

// Pool calls fn(i) for every i in [0, n), from at most workers goroutines, and
// returns when every call has returned.
//
// The goroutine count is min(workers, n) for the whole run — NOT n goroutines
// gated by a semaphore, which is the shape #179 reported: the gate bounds the
// concurrent forks but not the goroutines waiting to fork, so a 4,000-item store
// parked 4,000 stacks every pass. Here the indexes are handed out from a shared
// counter, so the pool's footprint is independent of the store's size.
//
// fn must be safe to call concurrently; writing its result to a slot of a slice
// indexed by i needs no further locking. workers < 1 is treated as 1.
func Pool(n, workers int, fn func(i int)) {
	if n <= 0 {
		return
	}
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	var (
		mu   sync.Mutex
		next int
		wg   sync.WaitGroup
	)
	take := func() (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		if next >= n {
			return 0, false
		}
		i := next
		next++
		return i, true
	}
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i, ok := take()
				if !ok {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}
