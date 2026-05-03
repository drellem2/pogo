//go:build !darwin && !linux

package sleep

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatchNoopReturnsNil verifies the unsupported-platform stub returns
// nil (signalling "no shim, fall back silently") and never invokes the
// hook.
func TestWatchNoopReturnsNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var fired int32
	if err := Watch(ctx, func() { atomic.AddInt32(&fired, 1) }); err != nil {
		t.Fatalf("expected nil error from no-op stub, got %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&fired); got != 0 {
		t.Errorf("hook should never fire on no-op stub, got %d calls", got)
	}
}
