package mgscan

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolCallsEveryIndexExactlyOnce(t *testing.T) {
	for _, tc := range []struct{ n, workers int }{
		{0, 4}, {1, 4}, {3, 8}, {1000, 4}, {17, 1}, {5, 0}, {5, -3},
	} {
		counts := make([]int32, tc.n)
		Pool(tc.n, tc.workers, func(i int) { atomic.AddInt32(&counts[i], 1) })
		for i, c := range counts {
			if c != 1 {
				t.Errorf("n=%d workers=%d: index %d called %d times, want 1", tc.n, tc.workers, i, c)
			}
		}
	}
}

// The #179 shape: n goroutines started up front, all but `workers` of them
// parked on a semaphore. The pool must hold its goroutine count at the worker
// count however large n is, so it is measured WHILE the pool is running, from
// inside the work function, against a baseline taken before it started.
func TestPoolGoroutineCountIsBoundedByWorkersNotItems(t *testing.T) {
	const n, workers = 2000, 4
	base := runtime.NumGoroutine()

	var (
		mu        sync.Mutex
		peakGo    int
		inFlight  int32
		peakFlght int32
	)
	Pool(n, workers, func(i int) {
		cur := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for {
			p := atomic.LoadInt32(&peakFlght)
			if cur <= p || atomic.CompareAndSwapInt32(&peakFlght, p, cur) {
				break
			}
		}
		if i%100 == 0 {
			g := runtime.NumGoroutine()
			mu.Lock()
			if g > peakGo {
				peakGo = g
			}
			mu.Unlock()
			time.Sleep(time.Millisecond) // give the others a chance to overlap
		}
	})

	if extra := peakGo - base; extra > workers {
		t.Errorf("peak goroutines during the pool = %d above baseline, want <= %d workers "+
			"(one goroutine per item is the #179 defect)", extra, workers)
	}
	// Positive control for the measurement: the sample must have seen the
	// workers themselves, or it was taken somewhere they could not appear.
	if peakGo-base < 1 {
		t.Errorf("peak goroutines %d never rose above baseline %d — the sample did not observe the pool", peakGo, base)
	}
	if peakFlght > workers {
		t.Errorf("peak concurrent calls = %d, want <= %d", peakFlght, workers)
	}
}

func TestDefaultWorkersIsClamped(t *testing.T) {
	if w := DefaultWorkers(); w < 1 || w > MaxDefaultWorkers {
		t.Fatalf("DefaultWorkers() = %d, want within [1, %d]", w, MaxDefaultWorkers)
	}
}
