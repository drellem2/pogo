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
	saved := srv
	t.Cleanup(func() { srv = saved })

	srv = nil
	if stallWatchPaused() {
		t.Fatal("no server built yet must not read as paused")
	}

	srv = server.New(nil, nil)
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
