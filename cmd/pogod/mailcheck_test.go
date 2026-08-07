package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
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

// TestMailCheckRegistrar_SpawnMessagePassesTheMailboxGuard is the join between
// the two halves of the mg-aa96 fix. Part 1 changed what pogod registers
// (mailbox = agent name); part 2 made the scheduler refuse a mail-check pointed
// anywhere else. Wiring the REAL message builder into the REAL registrar and
// the REAL scheduler is what proves the two agree — a guard that rejected the
// message the fix now ships would take every polecat's reachability channel
// down at spawn.
//
// What it does NOT prove is that part 1 is still in place: it builds the
// message itself rather than going through registerPolecatMailCheck, so a
// regression there is caught by TestSpawnPolecatRegistersMailCheck (agent
// package) and its production consequence by the sibling test below.
//
// The fixture's agent name and work item deliberately differ, in exactly the
// shape the live fleet had: agent "waa96" working "mg-aa96".
func TestMailCheckRegistrar_SpawnMessagePassesTheMailboxGuard(t *testing.T) {
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	m := mailCheckRegistrar{sched: s}

	const name, workItem = "waa96", "mg-aa96"
	if err := m.RegisterMailCheck(name, workItem, agent.PolecatMailCheckCron, agent.PolecatMailCheckMessage(name, workItem)); err != nil {
		t.Fatalf("the message pogod registers at spawn was refused by the mailbox guard: %v", err)
	}
	got, ok := s.Get(name, scheduler.MailCheckIDPrefix+workItem)
	if !ok {
		t.Fatal("entry absent after a registration that reported success")
	}
	// Both boxes, and the agent's own among them. The agent name is the
	// identity replies are addressed to; the work-item box is the one a sender
	// who typed the work item id created, and mg has no registration that would
	// stop them (mg-4f8c). The guard requires the first; the second is why the
	// guard is a membership test.
	if !scheduler.ReadsMailbox(got.Message, name) {
		t.Errorf("registered message %q never opens the agent's own box %q — mail addressed to it would be invisible", got.Message, name)
	}
	if !scheduler.ReadsMailbox(got.Message, workItem) {
		t.Errorf("registered message %q never opens the work-item box %q — mail already delivered there stays unread (mg-4f8c)", got.Message, workItem)
	}
}

// TestMailCheckRegistrar_RejectsWorkItemDerivedMailbox is the positive control
// for the test above: the same registrar, the same scheduler, the same work
// item — only the mailbox reverts to the pre-mg-aa96 work-item-derived form.
// Registration must now FAIL. Without this, the test above would pass just as
// happily if the guard were removed entirely.
func TestMailCheckRegistrar_RejectsWorkItemDerivedMailbox(t *testing.T) {
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	var escalations int
	m := mailCheckRegistrar{sched: s, escalate: func(string, string) { escalations++ }}

	const name, workItem = "waa96", "mg-aa96"
	// The pre-mg-aa96 message: the mailbox derived from the work item and the
	// agent name absent entirely. Rendering it by passing workItem as the AGENT
	// argument is how the bug actually happened — the two identities were
	// collapsed into one — and PolecatMailCheckMessage then collapses the
	// now-redundant second name, leaving a message that opens `aa96` and
	// nothing else.
	err = m.RegisterMailCheck(name, workItem, agent.PolecatMailCheckCron, agent.PolecatMailCheckMessage(workItem, workItem))
	if err == nil {
		t.Fatal("registrar accepted the pre-mg-aa96 work-item-derived mailbox; the polecat would poll `aa96` while its mail arrives in `waa96`, and would read that as having no mail")
	}
	if _, ok := s.Get(name, scheduler.MailCheckIDPrefix+workItem); ok {
		t.Error("a refused mail-check must not be left registered")
	}
	// The registrar treats it as a persistent failure, which is the intended
	// landing: a polecat with no working reachability channel escalates to the
	// mayor and lands in schedule_register_failed telemetry, instead of running
	// for an hour on an inbox nobody writes to.
	if escalations != 1 {
		t.Errorf("escalate called %d times, want 1 — a refused mail-check leaves the polecat unreachable and must be loud", escalations)
	}
}
