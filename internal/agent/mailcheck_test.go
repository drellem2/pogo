package agent

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMailCheckRegistrar records RegisterMailCheck calls so tests can assert
// spawn-polecat auto-registers the mail-check loop with the right identity.
type fakeMailCheckRegistrar struct {
	mu    sync.Mutex
	calls []mailCheckCall
	err   error
}

type mailCheckCall struct {
	agent, workItemID, cron, message string
}

func (f *fakeMailCheckRegistrar) RegisterMailCheck(agent, workItemID, cron, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, mailCheckCall{agent, workItemID, cron, message})
	return f.err
}

func (f *fakeMailCheckRegistrar) recorded() []mailCheckCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mailCheckCall(nil), f.calls...)
}

// TestSpawnPolecatRegistersMailCheck locks in the mg-e633 fix: spawning a
// polecat auto-registers its mail-check loop, addressed to the polecat's bare
// registry name (the identity pogod delivers nudges to and reaps under) with a
// mail-check-<work-item-id> schedule id.
func TestSpawnPolecatRegistersMailCheck(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	writeTemplate(t, "plainpc", "# plain polecat\nbody {{.Id}}\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	fake := &fakeMailCheckRegistrar{}
	reg.SetMailCheckRegistrar(fake)

	spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-plain",
		Template: "plainpc",
		Id:       "wi-42",
	})

	calls := fake.recorded()
	if len(calls) != 1 {
		t.Fatalf("RegisterMailCheck called %d times, want 1: %+v", len(calls), calls)
	}
	c := calls[0]
	if c.agent != "pc-plain" {
		t.Errorf("mail-check agent = %q, want the polecat's bare name %q", c.agent, "pc-plain")
	}
	if c.workItemID != "wi-42" {
		t.Errorf("mail-check workItemID = %q, want %q", c.workItemID, "wi-42")
	}
	if c.cron != PolecatMailCheckCron {
		t.Errorf("mail-check cron = %q, want %q", c.cron, PolecatMailCheckCron)
	}
	if !strings.Contains(c.message, "mg mail list wi-42") {
		t.Errorf("mail-check message %q should tell the polecat to read `mg mail list wi-42`", c.message)
	}
}

// TestSpawnPolecatMailCheckFallsBackToName verifies that a polecat spawned
// without a work item id (e.g. a no-worktree in-place dispatch) still gets a
// specific, reap-matchable mail-check schedule keyed on its agent name.
func TestSpawnPolecatMailCheckFallsBackToName(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	writeTemplate(t, "noidpc", "# polecat no id\nbody\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	fake := &fakeMailCheckRegistrar{}
	reg.SetMailCheckRegistrar(fake)

	spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:       "pc-noid",
		Template:   "noidpc",
		NoWorktree: true,
	})

	calls := fake.recorded()
	if len(calls) != 1 {
		t.Fatalf("RegisterMailCheck called %d times, want 1", len(calls))
	}
	if calls[0].workItemID != "pc-noid" {
		t.Errorf("mail-check workItemID = %q, want fallback to agent name %q", calls[0].workItemID, "pc-noid")
	}
	if !strings.Contains(calls[0].message, "mg mail list pc-noid") {
		t.Errorf("mail-check message %q should reference the agent-name mailbox", calls[0].message)
	}
}

// TestSpawnPolecatMailCheckFailureNonFatal verifies that a mail-check
// registration error does not fail the spawn: the polecat is already running,
// so a missing mail-check only degrades reachability, it must not kill the
// worker.
func TestSpawnPolecatMailCheckFailureNonFatal(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	writeTemplate(t, "errpc", "# polecat\nbody {{.Id}}\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	reg.SetMailCheckRegistrar(&fakeMailCheckRegistrar{err: errRegistrarBoom})

	// spawnPolecatViaAPI already asserts a 201; reaching here means the spawn
	// succeeded despite the registrar error.
	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-err",
		Template: "errpc",
		Id:       "wi-err",
	})
	if a == nil {
		t.Fatal("expected polecat to spawn despite mail-check registration failure")
	}
}

// TestSpawnPolecatNoMailCheckRegistrar verifies spawn is nil-safe: a bare
// registry with no registrar (unit tests, or a daemon with the scheduler
// disabled) spawns polecats without panicking.
func TestSpawnPolecatNoMailCheckRegistrar(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	writeTemplate(t, "bareepc", "# polecat\nbody {{.Id}}\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	// No SetMailCheckRegistrar call.
	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-bare",
		Template: "bareepc",
		Id:       "wi-bare",
	})
	if a == nil {
		t.Fatal("expected polecat to spawn with no mail-check registrar configured")
	}
}

// TestPolecatMailCheckMessageMentionsReviewLoop guards the message contract:
// the nudge must both point at the mailbox and tell the polecat to act on the
// builder<->reviewer review-loop traffic this schedule exists to unblock.
func TestPolecatMailCheckMessageMentionsReviewLoop(t *testing.T) {
	msg := PolecatMailCheckMessage("mg-e633")
	if !strings.Contains(msg, "mg mail list mg-e633") {
		t.Errorf("message %q should reference `mg mail list mg-e633`", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "review") {
		t.Errorf("message %q should mention the review loop (reviewer findings / re-review)", msg)
	}
}

var errRegistrarBoom = boomError("mail-check registrar unavailable")

type boomError string

func (e boomError) Error() string { return string(e) }
