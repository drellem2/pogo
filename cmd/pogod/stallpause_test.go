package main

import (
	"testing"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/server"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// TestStallWatchPausedFollowsIndexOnlyMode pins pogod's half of gh
// drellem2/pogo#190 (c): the stall watcher's Paused hook reads the server's
// mode, late, so it is silent exactly while pogod is index-only.
func TestStallWatchPausedFollowsIndexOnlyMode(t *testing.T) {
	testsandbox.Isolate(t)
	saved := currentServer()
	t.Cleanup(func() { srvRef.Store(saved) })

	srvRef.Store(nil)
	if stallWatchPaused() {
		t.Fatal("no server built yet must not read as paused")
	}

	srv := server.New(nil, nil)
	srvRef.Store(srv)
	if stallWatchPaused() {
		t.Fatal("a full-mode server must not pause the stall watcher")
	}
	if err := srv.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !stallWatchPaused() {
		t.Fatal("an index-only server must pause the stall watcher")
	}
	if err := srv.SetMode(config.ModeFull); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if stallWatchPaused() {
		t.Fatal("resuming full mode must un-pause the stall watcher")
	}
}

// TestServerPublishedWhileHeartbeatReads pins mg-4d5e: main publishes the
// server after the heartbeat is already running, and the heartbeat's
// goroutines read it (stallWatchPaused here; orchResume's closure is the same
// read). Under `go test -race` a plain package var failed this; it only
// asserts anything when the race detector is on.
func TestServerPublishedWhileHeartbeatReads(t *testing.T) {
	testsandbox.Isolate(t)
	saved := currentServer()
	t.Cleanup(func() { srvRef.Store(saved) })
	srvRef.Store(nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			stallWatchPaused()
		}
	}()
	srvRef.Store(server.New(nil, nil))
	<-done
	if stallWatchPaused() {
		t.Fatal("a full-mode server must not pause the stall watcher")
	}
}
