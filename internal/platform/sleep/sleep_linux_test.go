//go:build linux

package sleep

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatchLinuxNoopReturnsNil verifies the placeholder Linux stub
// returns nil and never invokes the hook. When ticket #6 lands a real
// systemd-suspend / DBus PrepareForSleep listener, this test will be
// replaced by a behavior test for that implementation.
func TestWatchLinuxNoopReturnsNil(t *testing.T) {
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
