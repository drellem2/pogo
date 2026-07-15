package main

import (
	"os"
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

// TestMailCheckRegistrar_HappyPathNoEscalation verifies the verify-after-register
// success path (mg-6fe0): a registration that persists and verifies present
// returns nil and does NOT escalate to the mayor.
func TestMailCheckRegistrar_HappyPathNoEscalation(t *testing.T) {
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	var escalated int
	m := mailCheckRegistrar{sched: s, escalate: func(string, string) { escalated++ }}

	if err := m.RegisterMailCheck("pc-ok", "wi-ok", "*/10 * * * *", "check your mail"); err != nil {
		t.Fatalf("RegisterMailCheck: %v", err)
	}
	if escalated != 0 {
		t.Errorf("escalate called %d times on the happy path, want 0", escalated)
	}
	if _, ok := s.Get("pc-ok", scheduler.MailCheckIDPrefix+"wi-ok"); !ok {
		t.Errorf("entry not present after a successful RegisterMailCheck")
	}
}

// TestMailCheckRegistrar_PersistentFailureEscalates verifies the persistent
// post-retry path (mg-6fe0): when the scheduler cannot persist (its state dir is
// read-only, so every Add fails), RegisterMailCheck retries once, then escalates
// to the mayor and returns the error — a live polecat left with no reachability
// channel must be loud, not silent.
func TestMailCheckRegistrar_PersistentFailureEscalates(t *testing.T) {
	dir := t.TempDir()
	s, err := scheduler.New(filepath.Join(dir, "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}

	// Make the state dir read-only so the store's temp-file+rename save fails on
	// every Add. Restore perms on cleanup so t.TempDir can remove it.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	var escalations []string
	m := mailCheckRegistrar{sched: s, escalate: func(agentName, scheduleID string) {
		escalations = append(escalations, agentName+"/"+scheduleID)
	}}

	err = m.RegisterMailCheck("pc-dark", "wi-dark", "*/10 * * * *", "check your mail")
	if err == nil {
		t.Fatal("RegisterMailCheck returned nil, want a persistent-failure error")
	}
	if len(escalations) != 1 {
		t.Fatalf("escalate called %d times, want exactly 1: %v", len(escalations), escalations)
	}
	if want := "pc-dark/" + scheduler.MailCheckIDPrefix + "wi-dark"; escalations[0] != want {
		t.Errorf("escalation = %q, want %q", escalations[0], want)
	}
}
