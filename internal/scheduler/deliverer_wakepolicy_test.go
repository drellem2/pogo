package scheduler

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/agenttest"
)

// recordingMail captures what the deliverer sent when the PTY did not carry the
// fire.
type recordingMail struct {
	mu   sync.Mutex
	sent []struct{ to, subject, body string }
}

func (r *recordingMail) send(to, from, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, struct{ to, subject, body string }{to, subject, body})
	return nil
}

func (r *recordingMail) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (r *recordingMail) last() (to, subject, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sent[len(r.sent)-1]
	return s.to, s.subject, s.body
}

// spawnLiveAgent gives the deliverer a real running agent to nudge. `cat` is
// the standard stand-in in this codebase: it has a PTY and echoes.
func spawnLiveAgent(t *testing.T, name string) (*agent.Registry, *agent.Agent) {
	t.Helper()
	reg, err := agent.NewRegistry(agenttest.SocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	a, err := reg.Spawn(agent.SpawnRequest{
		Name:    name,
		Type:    agent.TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Prime the agent into the state a wait-idle nudge can actually be
	// delivered to: it must have produced SOME output (Agent.IsIdle reports
	// false for an agent that has never written) and then gone quiet for longer
	// than the provider's IdleThreshold. Without this the pass-through control
	// would sit out the full nudge timeout and fall back to mail for a reason
	// that has nothing to do with the wake policy.
	if err := a.SendRaw("primed\r"); err != nil {
		t.Fatalf("SendRaw: %v", err)
	}
	time.Sleep(2500 * time.Millisecond)
	if !a.IsIdle(time.Second) {
		t.Fatal("precondition: agent should be idle after priming")
	}
	return reg, a
}

// A fire that lands on a live PTY while a limit episode is open must NOT be
// written to the terminal — and must still reach the recipient by mail, saying
// that the wake was suppressed rather than that a nudge failed. The wake-cycle
// policy suppresses the terminal wake, not the information.
//
// The fire is a sweep: its content is something the agent cannot reconstruct by
// reading its mail. A suppressed MAIL-CHECK fire writes no copy (see
// TestDelivererDropsSuppressedMailCheckInsideLimitEpisode below).
func TestDelivererSuppressesWakeInsideLimitEpisode(t *testing.T) {
	reg, a := spawnLiveAgent(t, "sched-suppressed")

	agent.SetLimitEpisodeQuery(func() (bool, string) { return true, "usage-limit episode ep-7 open" })
	t.Cleanup(func() { agent.SetLimitEpisodeQuery(nil) })

	mail := &recordingMail{}
	d := &PogodDeliverer{Registry: reg, Mail: mail.send}

	entry := Entry{
		ID:       "sweep-evening-x",
		Agent:    a.Name,
		Message:  "Check your mail.",
		Cron:     "3 17 * * *",
		Delivery: DeliveryNudge,
	}
	if err := d.Deliver(context.Background(), entry, time.Now()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if mail.count() != 1 {
		t.Fatalf("suppressed wake should fall back to mail exactly once, got %d", mail.count())
	}
	to, _, body := mail.last()
	if to != a.Name {
		t.Errorf("mail to = %q, want %q", to, a.Name)
	}
	if !strings.Contains(body, "terminal wake suppressed") {
		t.Errorf("mail should say the wake was suppressed, not that a nudge failed; got:\n%s", body)
	}
	if !strings.Contains(body, "ep-7") {
		t.Errorf("mail should carry the reason the policy gave, got:\n%s", body)
	}

	time.Sleep(200 * time.Millisecond)
	if out := string(a.RecentOutput(4096)); strings.Contains(out, "Check your mail.") {
		t.Errorf("suppressed wake must not have been written to the PTY, output: %q", out)
	}
}

// The pass-through control: same deliverer, same live agent, no episode. The
// fire reaches the terminal and no mail is sent. Without this the test above
// would only prove that something went wrong, not that the policy decided.
func TestDelivererDeliversWakeWhenNoEpisode(t *testing.T) {
	reg, a := spawnLiveAgent(t, "sched-delivered")

	agent.SetLimitEpisodeQuery(func() (bool, string) { return false, "" })
	t.Cleanup(func() { agent.SetLimitEpisodeQuery(nil) })

	mail := &recordingMail{}
	d := &PogodDeliverer{Registry: reg, Mail: mail.send}

	entry := Entry{
		ID:       "mail-check-y",
		Agent:    a.Name,
		Message:  "Check your mail.",
		Cron:     "*/10 * * * *",
		Delivery: DeliveryNudge,
	}
	if err := d.Deliver(context.Background(), entry, time.Now()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if mail.count() != 0 {
		_, _, body := mail.last()
		t.Fatalf("a delivered wake needs no mail fallback, got %d:\n%s", mail.count(), body)
	}
	time.Sleep(300 * time.Millisecond)
	if out := string(a.RecentOutput(4096)); !strings.Contains(out, "Check your mail.") {
		t.Errorf("wake should have reached the PTY, output: %q", out)
	}
}

// The same live agent and the same open limit episode, but the fire is a
// mail-check: it reaches neither the terminal nor the mailbox. Its whole content
// is "check your mail", so a mailed copy is a no-op turn (drellem2/pogo#299).
// The sweep test above is this test's control — same policy, same suppression,
// only the schedule's kind differs.
func TestDelivererDropsSuppressedMailCheckInsideLimitEpisode(t *testing.T) {
	reg, a := spawnLiveAgent(t, "sched-mc-suppressed")

	agent.SetLimitEpisodeQuery(func() (bool, string) { return true, "usage-limit episode ep-8 open" })
	t.Cleanup(func() { agent.SetLimitEpisodeQuery(nil) })

	mail := &recordingMail{}
	d := &PogodDeliverer{Registry: reg, Mail: mail.send}

	entry := Entry{
		ID:       "mail-check-z",
		Agent:    a.Name,
		Message:  "Check your mail.",
		Cron:     "2,32 * * * *",
		Delivery: DeliveryNudge,
	}
	out, err := d.DeliverOutcome(context.Background(), entry, time.Now())
	if err != nil {
		t.Fatalf("DeliverOutcome: %v", err)
	}
	if mail.count() != 0 {
		_, _, body := mail.last()
		t.Fatalf("a suppressed mail-check fire wrote %d mailbox copies, want 0:\n%s", mail.count(), body)
	}
	if out.Channel != NudgeDeliverySuppressed || out.FallbackReason != fallbackReasonSuppressed {
		t.Errorf("outcome = %+v, want channel %q reason %q", out, NudgeDeliverySuppressed, fallbackReasonSuppressed)
	}
	if !strings.Contains(out.NudgeError, "ep-8") {
		t.Errorf("outcome should carry the reason the policy gave, got %q", out.NudgeError)
	}

	time.Sleep(200 * time.Millisecond)
	if got := string(a.RecentOutput(4096)); strings.Contains(got, "Check your mail.") {
		t.Errorf("suppressed wake must not have been written to the PTY, output: %q", got)
	}
}
