package agent

import (
	"os"
	"testing"
	"time"
)

// activityAgent builds a live registry entry whose PTY last wrote lastWrite ago.
// A zero lastWrite means "never wrote", the case PolecatActivity.HasOutput
// exists to keep distinguishable.
func activityAgent(name, workItem string, typ AgentType, idle time.Duration, wrote bool) *Agent {
	buf := NewRingBuffer(1024)
	if wrote {
		buf.Write([]byte("output"))
		buf.mu.Lock()
		buf.lastWrite = time.Now().Add(-idle)
		buf.mu.Unlock()
	}
	return &Agent{
		Name:       name,
		WorkItemID: workItem,
		Type:       typ,
		PID:        os.Getpid(),
		Status:     StatusRunning,
		StartTime:  time.Now().Add(-time.Hour),
		outputBuf:  buf,
		done:       make(chan struct{}),
	}
}

func TestPolecatActivityAtReportsIdleness(t *testing.T) {
	r := &Registry{agents: map[string]*Agent{
		"d764": activityAgent("d764", "mg-d764", TypePolecat, 7*time.Minute+16*time.Second, true),
		"e9ee": activityAgent("e9ee", "mg-e9ee", TypePolecat, 42*time.Minute, true),
	}}

	now := time.Now()
	got := r.PolecatActivityAt(now)
	if len(got) != 2 {
		t.Fatalf("want 2 polecats, got %d (%+v)", len(got), got)
	}
	// Sorted by name, so the order is pinned.
	if got[0].Name != "d764" || got[1].Name != "e9ee" {
		t.Fatalf("snapshot should be sorted by name, got %s then %s", got[0].Name, got[1].Name)
	}
	if got[0].WorkItemID != "mg-d764" {
		t.Errorf("WorkItemID = %q, want mg-d764", got[0].WorkItemID)
	}
	if !got[0].HasOutput || !got[1].HasOutput {
		t.Fatalf("both polecats wrote output; HasOutput = %v, %v", got[0].HasOutput, got[1].HasOutput)
	}
	// Allow a second of slop for test execution time.
	if d := got[0].IdleFor; d < 7*time.Minute || d > 7*time.Minute+20*time.Second {
		t.Errorf("d764 IdleFor = %s, want ~7m16s", d)
	}
	if d := got[1].IdleFor; d < 41*time.Minute || d > 43*time.Minute {
		t.Errorf("e9ee IdleFor = %s, want ~42m", d)
	}
}

// TestPolecatActivityAtNoOutputIsUnmeasurable — a polecat that has never
// written must report HasOutput=false and NOT an idle time of zero. The two
// point opposite ways: zero idle reads as "just wrote", while the truth is
// "cannot tell", which is what keeps a freshly-spawned or pre-first-turn wedged
// polecat (mg-ce61) out of any idleness-gated action.
func TestPolecatActivityAtNoOutputIsUnmeasurable(t *testing.T) {
	r := &Registry{agents: map[string]*Agent{
		"fresh": activityAgent("fresh", "mg-fresh", TypePolecat, 0, false),
	}}
	got := r.PolecatActivityAt(time.Now())
	if len(got) != 1 {
		t.Fatalf("want 1 polecat, got %d", len(got))
	}
	if got[0].HasOutput {
		t.Error("HasOutput should be false for a polecat that never wrote")
	}
	if got[0].IdleFor != 0 {
		t.Errorf("IdleFor should stay zero when unmeasurable, got %s", got[0].IdleFor)
	}
}

// TestPolecatActivityAtExcludesCrew — crew agents are long-lived and not tied
// to a single work item, so they must never appear in a snapshot whose consumer
// stops things on the strength of an item being done.
func TestPolecatActivityAtExcludesCrew(t *testing.T) {
	r := &Registry{agents: map[string]*Agent{
		"mayor": activityAgent("mayor", "", TypeCrew, time.Hour, true),
		"cat1":  activityAgent("cat1", "mg-cat1", TypePolecat, time.Minute, true),
	}}
	got := r.PolecatActivityAt(time.Now())
	if len(got) != 1 || got[0].Name != "cat1" {
		t.Fatalf("want only the polecat, got %+v", got)
	}
}

// TestPolecatActivityAtExcludesDeadProcesses — a registry entry whose process
// has ended is not a slot holder, and stopping it is the OnExit path's job.
func TestPolecatActivityAtExcludesDeadProcesses(t *testing.T) {
	dead := activityAgent("dead", "mg-dead", TypePolecat, time.Hour, true)
	close(dead.done)
	r := &Registry{agents: map[string]*Agent{"dead": dead}}
	if got := r.PolecatActivityAt(time.Now()); len(got) != 0 {
		t.Fatalf("a polecat whose process ended must not be in the live snapshot, got %+v", got)
	}
}
