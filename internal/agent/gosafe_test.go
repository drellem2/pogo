package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Coverage for the goroutine panic-isolation sweep (drellem2/pogo#166, mg-38d1).
//
// Every test in this file would, before the sweep, have killed the TEST BINARY
// rather than failed: Go tears the process down on an unrecovered panic in any
// goroutine. That is the property being pinned — in pogod the process being torn
// down is the daemon, and a dead pogod cannot re-adopt its agents because their
// PTY masters died with it (orphan.go), so one hook panic would strand every
// agent on the host.

func TestGoSafeRecoversAndEmitsEvent(t *testing.T) {
	logPath := useTempEventLog(t)

	done := make(chan struct{})
	GoSafe("test.boom", func() {
		defer close(done)
		panic("boom")
	})
	<-done

	ev := waitForEvent(t, logPath, PanicEventType, "pogod", 2*time.Second)
	if ev == nil {
		t.Fatalf("no %s event emitted for a panicking guarded goroutine", PanicEventType)
	}
	details, _ := ev["details"].(map[string]any)
	if details == nil {
		t.Fatalf("event has no details: %v", ev)
	}
	if got := details["site"]; got != "test.boom" {
		t.Errorf("details.site = %v, want test.boom", got)
	}
	if got, _ := details["panic"].(string); !strings.Contains(got, "boom") {
		t.Errorf("details.panic = %q, want it to carry the panic value", got)
	}
	// The stack is what makes a recovered panic still findable. A guard that
	// swallowed quietly would trade a loud crash for an invisible one.
	if got, _ := details["stack"].(string); !strings.Contains(got, "gosafe_test.go") {
		t.Errorf("details.stack = %q, want the panicking frame", got)
	}
}

func TestSafelyReportsWhetherFnCompleted(t *testing.T) {
	useTempEventLog(t)

	if ok := Safely("test.ok", func() {}); !ok {
		t.Error("Safely returned false for a function that returned normally")
	}
	if ok := Safely("test.panicky", func() { panic("nope") }); ok {
		t.Error("Safely returned true for a function that panicked")
	}
}

// TestSpawnSurvivesPanickingPostSpawnHook is the triage packet's named
// acceptance test: inject a Provider whose PostSpawnHook panics, and the
// registry must survive with the agent still spawned and running.
func TestSpawnSurvivesPanickingPostSpawnHook(t *testing.T) {
	logPath := useTempEventLog(t)
	reg, _, codexP := newResolutionRegistry(t)
	defer reg.StopAll(2 * time.Second)

	entered := make(chan struct{})
	codexP.PostSpawnHook = func(a *Agent) {
		close(entered)
		panic("trust hook exploded")
	}

	a, err := reg.Spawn(SpawnRequest{
		Name: "panic-postspawn", Type: TypePolecat, Command: []string{"cat"}, Provider: codexP,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("PostSpawnHook never ran")
	}

	ev := waitForEvent(t, logPath, PanicEventType, a.EventAgent(), 5*time.Second)
	if ev == nil {
		t.Fatalf("no %s event for the panicking PostSpawnHook", PanicEventType)
	}
	if details, _ := ev["details"].(map[string]any); details["site"] != "provider.PostSpawnHook" {
		t.Errorf("details.site = %v, want provider.PostSpawnHook", details["site"])
	}

	// The daemon-level property: the registry still has this agent, running.
	if got := reg.Get("panic-postspawn"); got == nil {
		t.Fatal("agent gone from the registry after its post-spawn hook panicked")
	} else if got.Status != StatusRunning {
		t.Errorf("agent status = %q, want %q", got.Status, StatusRunning)
	}
}

// TestSpawnSurvivesPanickingSessionHook covers the hook's sibling — the
// lifetime session hook (the modal-dismissal watcher in production).
func TestSpawnSurvivesPanickingSessionHook(t *testing.T) {
	logPath := useTempEventLog(t)
	reg, _, codexP := newResolutionRegistry(t)
	defer reg.StopAll(2 * time.Second)

	entered := make(chan struct{})
	codexP.SessionHook = func(_ context.Context, a *Agent) {
		close(entered)
		panic("modal watcher exploded")
	}

	a, err := reg.Spawn(SpawnRequest{
		Name: "panic-session", Type: TypePolecat, Command: []string{"cat"}, Provider: codexP,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("SessionHook never ran")
	}

	if ev := waitForEvent(t, logPath, PanicEventType, a.EventAgent(), 5*time.Second); ev == nil {
		t.Fatalf("no %s event for the panicking SessionHook", PanicEventType)
	}
	if got := reg.Get("panic-session"); got == nil {
		t.Fatal("agent gone from the registry after its session hook panicked")
	}
}

// TestPanickingOnExitStillClosesDone pins the per-site decision in
// waitAndHandle. onExit is foreign code (in pogod it is the respawn/cleanup
// hook), and a guard that just recovered and returned would leave a.done unclosed
// — turning one panic into a permanent hang of Stop/StopAll and of every attach
// parked on Done(). Recovering AT the callback, plus an idempotent deferred
// close, is what keeps the exit path finishing.
func TestPanickingOnExitStillClosesDone(t *testing.T) {
	useTempEventLog(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	called := make(chan struct{})
	reg.SetOnExit(func(a *Agent, err error) {
		close(called)
		panic("onExit exploded")
	})

	a, err := reg.Spawn(SpawnRequest{
		Name: "panic-onexit", Type: TypePolecat, Command: []string{"sh", "-c", "exit 0"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	select {
	case <-called:
	case <-time.After(10 * time.Second):
		t.Fatal("onExit callback never fired")
	}
	select {
	case <-a.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Done() never closed after the onExit callback panicked — " +
			"every waiter (Stop, StopAll, attach conns) is parked forever")
	}
	if a.Status != StatusExited {
		t.Errorf("status = %q, want %q — the exit accounting ran before the callback", a.Status, StatusExited)
	}
}

// TestCloseDoneIsIdempotent covers the mechanism that makes the deferred close
// in waitAndHandle safe alongside the in-place one.
func TestCloseDoneIsIdempotent(t *testing.T) {
	a := &Agent{done: make(chan struct{})}
	a.closeDone()
	a.closeDone()
	select {
	case <-a.Done():
	default:
		t.Fatal("closeDone did not close done")
	}
}

// TestGoroutinePanicEventIsDocumented follows the repo convention that every
// emitted event type is pinned to its docs/event-log.md entry
// (cmd/pogod/reapverdictabsent_test.go, cmd/pogo/investigations_test.go). The
// catalog is how a reader learns this event exists, and for THIS event that is
// load-bearing: a recovered panic leaves a degraded daemon and no crash, so the
// log line is the only thing that says a goroutine is gone. An undocumented
// event type is one nobody knows to look for. The details fields are pinned too,
// since a reader who finds the event still has to know what `site` means.
func TestGoroutinePanicEventIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "event-log.md"))
	if err != nil {
		t.Skipf("event-log.md not readable from here: %v", err)
	}
	body := string(doc)
	if !strings.Contains(body, PanicEventType) {
		t.Fatalf("%s is emitted but absent from docs/event-log.md", PanicEventType)
	}
	for _, field := range []string{"`site`", "`panic`", "`stack`"} {
		if !strings.Contains(body, field) {
			t.Errorf("details field %s is emitted but not documented", field)
		}
	}
}

// goLaunch matches a bare goroutine launch statement. Comment lines cannot
// match: they start with "/".
var goLaunch = regexp.MustCompile(`(?m)^[[:space:]]*go [a-zA-Z(]`)

// TestGoroutineLaunchesAreGuarded makes the sweep's completeness EXECUTABLE
// rather than a claim in a commit message (the SME acceptance criterion on
// mg-7808): "all the ones I found" and "all of them" are different claims, and
// only the second justifies the change. It re-runs the enumeration the build
// used — every `^\s*go ` launch in internal/agent and internal/claude, excluding
// _test.go — and requires that the only survivors are inside gosafe.go, which is
// where the guard itself launches the goroutine.
//
// cmd/pogod is deliberately NOT swept: the approved recommendation scoped it to
// the one crash-path respawn scheduler, and its ~30 watcher launches are a
// separate question (noted on the PR, not silently widened here).
func TestGoroutineLaunchesAreGuarded(t *testing.T) {
	dirs := []string{".", filepath.Join("..", "claude")}
	guardFile := filepath.Join(".", "gosafe.go")

	var unguarded []string
	var guardOwn int
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", path, err)
			}
			for i, line := range strings.Split(string(src), "\n") {
				if !goLaunch.MatchString(line) {
					continue
				}
				// Exempted by PATH, not basename: a future
				// internal/claude/gosafe.go must NOT inherit this package's
				// exemption just by sharing a filename.
				if path == guardFile {
					guardOwn++
					continue
				}
				unguarded = append(unguarded, formatHit(path, i+1, line))
			}
		}
	}

	// Positive control: the scanner must find the guard's OWN two launches. If
	// it finds none, the instrument is broken and the empty `unguarded` above
	// says nothing about the rest of the tree.
	if guardOwn == 0 {
		t.Fatal("scanner found no goroutine launches in gosafe.go — the instrument is broken, " +
			"so the empty result for every other file is not evidence of anything")
	}
	if len(unguarded) > 0 {
		t.Errorf("unguarded goroutine launches (use GoSafe / a.GoSafe — see gosafe.go, drellem2/pogo#166):\n%s",
			strings.Join(unguarded, "\n"))
	}
}

func formatHit(path string, line int, text string) string {
	return path + ":" + strconv.Itoa(line) + ": " + strings.TrimSpace(text)
}
