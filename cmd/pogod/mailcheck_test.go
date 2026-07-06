package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/scheduler"
)

// TestMailCheckRegistrar_RegisterAndReap drives the mg-e633 acceptance through
// the real scheduler and the pogod adapter: registering a polecat's mail-check
// makes a `mail-check-<work-item-id>` entry appear (addressed to the bare agent
// name), and the same reap path pogod runs on exit removes it. Name ("spwsch")
// deliberately differs from the work item id ("mg-e633") to prove the schedule
// id keys on the work item while the agent — the reap/delivery identity — is
// the bare name.
func TestMailCheckRegistrar_RegisterAndReap(t *testing.T) {
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	m := mailCheckRegistrar{sched: s}

	// Mirror handleSpawnPolecat → registerPolecatMailCheck: agent = bare name,
	// work item id keys the schedule id.
	const name, workItem = "spwsch", "mg-e633"
	if err := m.RegisterMailCheck(name, workItem, "*/10 * * * *", "check your mail"); err != nil {
		t.Fatalf("RegisterMailCheck: %v", err)
	}

	// Acceptance: `pogo schedule list` shows mail-check-<wi> for <name>.
	entries := s.List(name)
	if len(entries) != 1 {
		t.Fatalf("List(%q) returned %d entries, want 1: %+v", name, len(entries), entries)
	}
	got := entries[0]
	if want := scheduler.MailCheckIDPrefix + workItem; got.ID != want {
		t.Errorf("schedule id = %q, want %q", got.ID, want)
	}
	if got.Agent != name {
		t.Errorf("schedule agent = %q, want bare name %q", got.Agent, name)
	}
	if got.Delivery != scheduler.DeliveryNudge {
		t.Errorf("delivery = %q, want nudge", got.Delivery)
	}
	if got.ReplayPolicy != scheduler.ReplayOnce {
		t.Errorf("replay policy = %q, want once", got.ReplayPolicy)
	}

	// Re-registering is idempotent (same (agent, id) key), so the polecat's own
	// step-2 self-registration never stacks a duplicate on the spawn entry.
	if err := m.RegisterMailCheck(name, workItem, "*/10 * * * *", "check your mail again"); err != nil {
		t.Fatalf("RegisterMailCheck (idempotent): %v", err)
	}
	if got := len(s.List(name)); got != 1 {
		t.Fatalf("after re-register have %d entries, want 1 (must be idempotent)", got)
	}

	// Acceptance: on reap the schedule is gone. This is exactly the call the
	// onExit hook makes: RemoveMailChecksForAgent(now, a.Name, a.EventAgent()).
	n := s.RemoveMailChecksForAgent(time.Now(), name, "cat-"+name)
	if n != 1 {
		t.Fatalf("reap removed %d entries, want 1", n)
	}
	if got := len(s.List("")); got != 0 {
		t.Fatalf("%d schedules left after reap, want 0", got)
	}
}
