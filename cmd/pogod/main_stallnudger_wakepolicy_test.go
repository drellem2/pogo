package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/stallwatch"
)

// spawnIdleAgent starts an agent that speaks once and then goes quiet — the
// state a stall fire is FOR. `cat` echoes what is written to the PTY and
// otherwise says nothing, so priming it and waiting out the idle threshold
// leaves an agent a wait-idle nudge can genuinely be delivered to. Without the
// priming Agent.IsIdle reports false (an agent that has never written is not
// idle), and every case below would fail for a reason unrelated to the policy.
func spawnIdleAgent(t *testing.T, reg *agent.Registry, name string) *agent.Agent {
	t.Helper()
	a, err := reg.Spawn(agent.SpawnRequest{
		Name:    name,
		Type:    agent.TypeCrew,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SendRaw("primed\r"); err != nil {
		t.Fatalf("SendRaw: %v", err)
	}
	time.Sleep(2500 * time.Millisecond)
	if !a.IsIdle(time.Second) {
		t.Fatal("precondition: primed agent should be idle")
	}
	return a
}

// TestStallNudgerSuppressedInsideLimitEpisode is the wake-cycle policy's rule 2
// at the call site it was written for: the stall fire is the canonical WAKE.
//
// An agent inside a known limit episode is wedged on the provider's modal and
// cannot act on anything written to its terminal, so the wake is declined — and
// the notice takes the durable road instead. Report-only is untouched: the
// detector still only reports, and what changed is that the actor asked before
// acting.
func TestStallNudgerSuppressedInsideLimitEpisode(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a := spawnIdleAgent(t, reg, "limited-mayor")

	agent.SetLimitEpisodeQuery(func() (bool, string) { return true, "usage-limit episode ep-3 open" })
	t.Cleanup(func() { agent.SetLimitEpisodeQuery(nil) })

	var body string
	mailed := false
	nudge := newStallNudgerWithTimeout(reg, func(to, from, subject, b string) error {
		mailed = true
		body = b
		return nil
	}, 5*time.Second)

	const sentinel = "STALL_WAKE_SENTINEL"
	delivery, err := nudge("limited-mayor", sentinel)
	if err != nil {
		t.Fatalf("a suppressed wake must still be delivered by mail, not fail: %v", err)
	}
	if !mailed {
		t.Fatal("suppressing the terminal wake must not suppress the notice")
	}
	if delivery.Channel != stallwatch.DeliveryMailFallback {
		t.Errorf("delivery channel = %q, want %q", delivery.Channel, stallwatch.DeliveryMailFallback)
	}
	if !strings.Contains(delivery.FallbackReason, agent.RuleLimitEpisode) {
		t.Errorf("fallback reason should name the suppressing rule, got %q", delivery.FallbackReason)
	}
	if !strings.Contains(body, sentinel) {
		t.Errorf("mail body lost the stall message: %q", body)
	}

	time.Sleep(300 * time.Millisecond)
	if strings.Contains(string(a.RecentOutput(1<<16)), sentinel) {
		t.Error("a wake suppressed by the limit-episode rule reached the PTY anyway")
	}
}

// TestStallNudgerDeliversWhenNoEpisode is the pass-through control for the test
// above, and it is the half that matters most: a suppression only ever observed
// suppressing has not been observed deciding. Same agent, same nudger, episode
// closed — the wake lands on the terminal and nothing is mailed.
func TestStallNudgerDeliversWhenNoEpisode(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a := spawnIdleAgent(t, reg, "quiet-mayor")

	agent.SetLimitEpisodeQuery(func() (bool, string) { return false, "" })
	t.Cleanup(func() { agent.SetLimitEpisodeQuery(nil) })

	mailed := false
	nudge := newStallNudgerWithTimeout(reg, func(to, from, subject, body string) error {
		mailed = true
		return nil
	}, 5*time.Second)

	const sentinel = "STALL_WAKE_DELIVERED"
	delivery, err := nudge("quiet-mayor", sentinel)
	if err != nil {
		t.Fatalf("nudge: %v", err)
	}
	if mailed {
		t.Error("a wake that reached the PTY needs no mail fallback")
	}
	if delivery.Channel != stallwatch.DeliveryPTY {
		t.Errorf("delivery channel = %q, want %q", delivery.Channel, stallwatch.DeliveryPTY)
	}
	time.Sleep(300 * time.Millisecond)
	if !strings.Contains(string(a.RecentOutput(1<<16)), sentinel) {
		t.Error("wake should have reached the idle agent's PTY")
	}
}

// TestStallNudgerWakesEachSilenceOnce is rule 1 at the same call site, in both
// directions: the first stall fire into a silence lands on the terminal, an
// immediate second fire into the SAME silence is declined to mail, and once the
// agent has spoken again a fire lands on the terminal once more.
//
// The middle case is the one the echo defeats if the settle window is dropped —
// `cat` echoes the first wake straight back, which is exactly what a wedged
// harness re-rendering its composer looks like.
func TestStallNudgerWakesEachSilenceOnce(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a := spawnIdleAgent(t, reg, "silent-mayor")
	agent.SetLimitEpisodeQuery(nil)

	mails := 0
	nudge := newStallNudgerWithTimeout(reg, func(to, from, subject, body string) error {
		mails++
		return nil
	}, 5*time.Second)

	// First fire into this silence: the terminal takes it.
	delivery, err := nudge("silent-mayor", "WAKE_ONE")
	if err != nil {
		t.Fatalf("first wake: %v", err)
	}
	if delivery.Channel != stallwatch.DeliveryPTY {
		t.Fatalf("first wake should reach the PTY, got channel %q", delivery.Channel)
	}
	time.Sleep(300 * time.Millisecond)

	// Second fire into the SAME silence — the only output since is the echo.
	delivery, err = nudge("silent-mayor", "WAKE_TWO")
	if err != nil {
		t.Fatalf("second wake should fall back to mail, not fail: %v", err)
	}
	if delivery.Channel != stallwatch.DeliveryMailFallback {
		t.Errorf("second wake of the same silence should be suppressed, got channel %q", delivery.Channel)
	}
	if !strings.Contains(delivery.FallbackReason, agent.RuleWakeSilenceOnce) {
		t.Errorf("fallback reason should name the wake-once rule, got %q", delivery.FallbackReason)
	}
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(string(a.RecentOutput(1<<16)), "WAKE_TWO") {
		t.Error("the suppressed second wake reached the PTY anyway")
	}

	// The agent speaks again, well past the settle window: a NEW silence.
	time.Sleep(2500 * time.Millisecond)
	if err := a.SendRaw("agent spoke\r"); err != nil {
		t.Fatalf("SendRaw: %v", err)
	}
	time.Sleep(2500 * time.Millisecond)

	delivery, err = nudge("silent-mayor", "WAKE_THREE")
	if err != nil {
		t.Fatalf("third wake: %v", err)
	}
	if delivery.Channel != stallwatch.DeliveryPTY {
		t.Errorf("a new silence is owed its own wake, got channel %q", delivery.Channel)
	}
	if mails != 1 {
		t.Errorf("exactly one fire (the suppressed one) should have taken the mail road, got %d", mails)
	}
}
