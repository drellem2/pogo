package midsessionwedge

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/agenttest"
	"github.com/drellem2/pogo/internal/events"
)

// The parked/spinning separation, against a REAL PTY.
//
// Everything else in this package tests the judgement over scripted readings.
// This tests the READING — that a hash of the live ring actually does what the
// design says, because the whole detector rests on one claim about a real
// terminal: a working agent's ring changes and a parked agent's ring is
// byte-identical, not merely slow-changing.
//
// That claim was measured on the live fleet before any of this was written
// (2026-09-08, 2.2s sampling, full 64KB rings, eight agents): six crew agents
// parked at a rendered composer held ONE hash for the whole observation window
// while last-activity aged monotonically, and every agent mid-turn changed hash
// on every single sample. This test is that measurement reduced to something CI
// can re-run, so a change to the ring, the digest or the output window that
// broke the separation would fail the build rather than silently disarm the
// detector.

// spinner never stops writing — an animating status line, which is what Claude
// Code draws while it works. It is the trap the ticket names: a spinner IS PTY
// output, so this agent's ring must read as CHANGING.
const spinner = `#!/bin/sh
i=0
while :; do
	i=$((i+1))
	printf '\033[2K\rworking %d' "$i"
	sleep 0.02
done
`

// parked prints once and then holds, producing nothing further — a rendered
// composer with no animation. Its ring must read as byte-identical.
const parked = `#!/bin/sh
printf 'ready\n> '
sleep 30
`

func spawn(t *testing.T, reg *agent.Registry, name, script string) *agent.Agent {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name+".sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := reg.Spawn(agent.SpawnRequest{
		Name:    name,
		Type:    agent.TypePolecat,
		Command: []string{"sh", path},
	})
	if err != nil {
		t.Fatalf("Spawn %s: %v", name, err)
	}
	return a
}

func digestOf(t *testing.T, src SourceFunc, name string) string {
	t.Helper()
	rs, err := src(time.Now())
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	for _, r := range rs {
		if r.Name == name {
			return r.Digest
		}
	}
	t.Fatalf("no reading for %s", name)
	return ""
}

func TestRingDigestSeparatesParkedFromSpinning(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real PTYs")
	}
	reg, err := agent.NewRegistry(agenttest.SocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	spawn(t, reg, "spins", spinner)
	spawn(t, reg, "parks", parked)
	src := RegistrySource(reg)

	// Wait for the parked agent's ring to SETTLE — two consecutive identical
	// non-empty reads — rather than for its first byte. The first read can land
	// midway through the harness's own opening write, and the assertion below
	// is about what the ring does once the agent is quiescent, not about
	// whether a write is atomic. Measuring the quiet run from an observed
	// CHANGE is also exactly what the production judgement does.
	var parkFirst string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a := digestOf(t, src, "parks")
		time.Sleep(100 * time.Millisecond)
		if a != "" && a == digestOf(t, src, "parks") {
			parkFirst = a
			break
		}
	}
	spinFirst := digestOf(t, src, "spins")
	if parkFirst == "" || spinFirst == "" {
		t.Fatalf("ring never settled within 10s (parked=%q spinning=%q)", parkFirst, spinFirst)
	}

	// Sample across a window many times the spinner's frame period.
	spinChanged := false
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		if digestOf(t, src, "spins") != spinFirst {
			spinChanged = true
			break
		}
	}
	if !spinChanged {
		t.Error("a continuously animating agent's ring digest never changed; the detector " +
			"treats an unchanged ring as 'nothing is happening' and would judge every " +
			"working agent wedged")
	}
	if got := digestOf(t, src, "parks"); got != parkFirst {
		t.Errorf("a parked agent's ring digest changed (%s -> %s) with nothing writing to "+
			"its PTY; the quiescence clause rests on identical meaning identical", parkFirst, got)
	}
}

// TestLiveAgentWithoutAReceiptHookIsSkippedNotJudged. A bare-registry spawn
// installs no submission-receipt hook, so its submits are unreadable. The
// watcher must decline it out loud rather than treat unreadable as zero — which
// is the incriminating direction.
func TestLiveAgentWithoutAReceiptHookIsSkippedNotJudged(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real PTYs")
	}
	reg, err := agent.NewRegistry(agenttest.SocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	a := spawn(t, reg, "hookless", parked)

	src := RegistrySource(reg)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && digestOf(t, src, "hookless") == "" {
		time.Sleep(50 * time.Millisecond)
	}
	if a.HasReceiptSignal() {
		t.Skip("this registry does install a receipt hook; the skip path needs another fixture")
	}

	var fired, skipped int
	var recovered int
	w := New(Options{
		Enabled:    true,
		Source:     src,
		Recover:    func(string) error { recovered++; return nil },
		Submits:    RegistrySubmits(reg),
		Interval:   time.Nanosecond,
		Quiescence: time.Millisecond,
		Emit: func(e events.Event) {
			switch e.EventType {
			case EventFired:
				fired++
			case EventSkipped:
				skipped++
			}
		},
	})
	now := time.Now()
	for i := 0; i < 5; i++ {
		w.Check(now.Add(time.Duration(i) * time.Minute))
	}
	if fired != 0 || recovered != 0 {
		t.Errorf("judged an agent whose submits cannot be read (fired=%d recovered=%d)", fired, recovered)
	}
	if skipped == 0 {
		t.Error("declined silently; could-not-judge must never read the same as healthy")
	}
}
