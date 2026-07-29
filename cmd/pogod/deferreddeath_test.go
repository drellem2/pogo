package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// This file constructs the deferred-death window end to end, against a real
// process and a real macguffin store, rather than reasoning about it.
//
// The window (mg-c8d5): a polecat in the deferred / PR-flow lane merges. pogod
// deliberately does NOT run `mg done` or stop it — the polecat owes a pull
// request and its own `mg done`. Then the process dies. Because a self-exit
// never goes through Registry.Stop, nothing released the claim, and the work
// item sat in work/claimed/<id>.md.<dead-pid>: never dispatched again, and
// invisible to stall-watch, which scans available/ only.
//
// The reap_test.go unit tests drive the same decision against a fake. They
// establish that the code takes the branch; only this one establishes that the
// claim file is actually gone from a store `mg` agrees with. A fix for a path
// only ever reasoned about has not been observed working.

// mgSandboxStore builds an isolated macguffin store and returns its root. Every
// caller here pins MGClaimReleaser.Root at it explicitly, so no part of this
// test can reach the developer's live ~/.macguffin.
func mgSandboxStore(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("mg"); err != nil {
		t.Skip("mg not on PATH; the claim-release path needs the real macguffin CLI")
	}
	// Not t.TempDir(): mg writes a git snapshot repo under the root, and macOS
	// temp paths are long enough to matter for the sockets alongside it.
	root := filepath.Join(filepath.Dir(shortSocketDir(t)), "mgstore")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if out, err := exec.Command("mg", "--root", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("mg init: %v: %s", err, out)
	}
	return root
}

var mgNewID = regexp.MustCompile(`\b(mg-[0-9a-f]+)\b`)

// mgClaimedItem creates a work item in the sandbox store and claims it, leaving
// the store in the state a polecat mid-flow leaves it in.
func mgClaimedItem(t *testing.T, root, title string) string {
	t.Helper()
	out, err := exec.Command("mg", "--root", root, "new", title).CombinedOutput()
	if err != nil {
		t.Fatalf("mg new: %v: %s", err, out)
	}
	m := mgNewID.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("could not parse a work item id out of %q", out)
	}
	id := m[1]
	if out, err := exec.Command("mg", "--root", root, "claim", id).CombinedOutput(); err != nil {
		t.Fatalf("mg claim %s: %v: %s", id, err, out)
	}
	return id
}

// mgItemStatus reports which status directory holds id, or "" if none does. It
// reads the directory rather than asking `mg list` because the claim FILE —
// work/claimed/<id>.md.<pid> — is the leak under test.
func mgItemStatus(t *testing.T, root, id string) string {
	t.Helper()
	for _, dir := range []string{"available", "claimed", "done", "pending", "shelved", "archive"} {
		entries, err := os.ReadDir(filepath.Join(root, "work", dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), id+".md") {
				return dir
			}
		}
	}
	return ""
}

// mgDispatchable reports whether mg itself considers id available — the
// downstream property that actually matters. A claim stranded under a dead pid
// is not dispatchable; an item back in available/ is.
func mgDispatchable(t *testing.T, root, id string) bool {
	t.Helper()
	out, err := exec.Command("mg", "--root", root, "list", "--status=available").CombinedOutput()
	if err != nil {
		t.Fatalf("mg list --status=available: %v: %s", err, out)
	}
	return strings.Contains(string(out), id)
}

// deathWindow spawns a real polecat holding a real claim, merges it in the
// PR-flow lane, and returns the registry, the backstop and the live agent —
// with the OnExit hook wired exactly as pogod wires it in main.go. The caller
// decides how the polecat ends.
func deathWindow(t *testing.T, root, id string, escalations *[]string) (*agent.Registry, *deferredBackstop, *agent.Agent) {
	t.Helper()
	sandboxPogoHome(t)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	reg.SetClaimReleaser(agent.MGClaimReleaser{Root: root})

	backstop := newDeferredBackstop(15*time.Minute, reg, func(mr *refinery.MergeRequest) {
		*escalations = append(*escalations, "linger:"+mr.Author)
	})
	backstop.escalateDeath = func(mr *refinery.MergeRequest, a *agent.Agent, released bool, releaseErr error) {
		*escalations = append(*escalations, "death:"+a.WorkItemID)
	}
	// The one line of pogod's OnExit hook this window turns on (main.go).
	reg.SetOnExit(func(a *agent.Agent, _ error) { backstop.cancel(a) })

	// `cat` blocks forever: the polecat is alive and mid-flow, past its merge
	// and short of `mg done`.
	a, err := reg.Spawn(agent.SpawnRequest{
		Name:       "deathcat",
		Type:       agent.TypePolecat,
		Command:    []string{"cat"},
		WorkItemID: id,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// The merge lands on an integration branch — the PR-flow lane, which gh #92
	// made the default for a merged polecat. pogod must leave it running.
	mr := &refinery.MergeRequest{ID: "mr-92", Branch: "polecat-" + id, Author: id, TargetRef: "integration", PRFlow: true}
	reapMergedPolecat(reg, mr, func(string, string) error {
		t.Error("PR flow: pogod must not run mg done at merge — the polecat owes a PR first")
		return nil
	}, postMergeVerdict{}, backstop)

	if got := mgItemStatus(t, root, id); got != "claimed" {
		t.Fatalf("precondition: %s should still be claimed after a PR-flow merge, got %q", id, got)
	}
	return reg, backstop, a
}

// TestDeferredPolecatDeathReleasesClaim is the constructed window. The polecat
// dies — killed outright, as a crash or an OOM or an operator kill would —
// after its merge and before `mg done`, and the claim must be released.
func TestDeferredPolecatDeathReleasesClaim(t *testing.T) {
	root := mgSandboxStore(t)
	id := mgClaimedItem(t, root, "a PR-flow polecat that dies before mg done")
	var escalations []string
	_, _, a := deathWindow(t, root, id, &escalations)

	// The death. SIGKILL, not a stop: nothing tells the registry to tear the
	// agent down, so Registry.Stop's claim release (mg-fb13) never runs. This is
	// exactly the door mg-fb13 left open.
	proc, err := os.FindProcess(a.PID)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", a.PID, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill polecat: %v", err)
	}
	select {
	case <-a.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("polecat did not exit after SIGKILL")
	}

	if got := mgItemStatus(t, root, id); got != "available" {
		t.Errorf("work item %s is in %q after its deferred polecat died, want %q — "+
			"a claim stranded under a dead pid is never dispatched and stall-watch does not scan claimed/", id, got, "available")
	}
	if !mgDispatchable(t, root, id) {
		t.Errorf("work item %s is absent from `mg list --status=available` — it cannot be re-dispatched", id)
	}
	if len(escalations) != 1 || escalations[0] != "death:"+id {
		t.Errorf("expected one deferred-death escalation for %s, got %v", id, escalations)
	}
}

// TestDeferredPolecatCompletionKeepsItemDone is the other direction, and the
// control that keeps the release above from being a blunt instrument: the
// polecat finishes its PR flow, runs `mg done` itself, and only then exits. The
// item must stay done — not be bounced back to available/ — and nobody is paged.
func TestDeferredPolecatCompletionKeepsItemDone(t *testing.T) {
	root := mgSandboxStore(t)
	id := mgClaimedItem(t, root, "a PR-flow polecat that completes its flow")
	var escalations []string
	_, _, a := deathWindow(t, root, id, &escalations)

	// The polecat opens its PR and ends its own lifecycle.
	if out, err := exec.Command("mg", "--root", root, "done", id).CombinedOutput(); err != nil {
		t.Fatalf("mg done: %v: %s", err, out)
	}
	proc, err := os.FindProcess(a.PID)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", a.PID, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill polecat: %v", err)
	}
	select {
	case <-a.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("polecat did not exit")
	}

	if got := mgItemStatus(t, root, id); got != "done" {
		t.Errorf("work item %s is in %q after a completed PR flow, want %q", id, got, "done")
	}
	if mgDispatchable(t, root, id) {
		t.Errorf("work item %s was returned to available/ — a done item must not be re-dispatched", id)
	}
	if len(escalations) != 0 {
		t.Errorf("a completed PR flow must not escalate, got %v", escalations)
	}
}
