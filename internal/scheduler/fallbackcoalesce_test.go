package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// failingMail is a MailSender that refuses the first `fail` sends and then
// succeeds, recording everything it accepted.
type failingMail struct {
	mu       sync.Mutex
	fail     int
	attempts int
	sent     []string // bodies of the sends that succeeded
}

func (m *failingMail) send(to, from, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	if m.fail > 0 {
		m.fail--
		return errors.New("mg: transport down")
	}
	m.sent = append(m.sent, body)
	return nil
}

func (m *failingMail) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

// outageDeliverer builds a deliverer with no registry, so every fire takes the
// agent-not-running branch — the shape of the 2026-08-07..09 outage that filled
// architect's mailbox.
func outageDeliverer(t *testing.T, mail MailSender) (*PogodDeliverer, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "events.log")
	return &PogodDeliverer{Mail: mail, LogPath: logPath}, logPath
}

func mailCheckEntry(agentName, id string) Entry {
	return Entry{
		ID:       id,
		Agent:    agentName,
		Message:  "Check your mail with mg mail list " + agentName,
		Cron:     "*/10 * * * *",
		Delivery: DeliveryNudge,
	}
}

// The measured defect, at the measured scale (mg-af83).
//
// architect's mailbox held 264 unread scheduler messages against 1 real one on
// 2026-08-09. All 264 were a single unbroken run of `*/10` fires that could not
// be delivered as a nudge because the fleet was down — every one of them the
// same "check your mail" reminder, into the listing it was burying.
//
// One copy discharges the fallback's whole purpose. This drives the same 264
// fires and asserts the recipient gets one.
func TestFallbackMailIsCoalescedAcrossAnOutage(t *testing.T) {
	const fires = 264
	mail := &failingMail{}
	d, logPath := outageDeliverer(t, mail.send)
	entry := mailCheckEntry("architect", MailCheckIDPrefix+"architect")

	now := fixedTime()
	for i := 0; i < fires; i++ {
		if err := d.Deliver(context.Background(), entry, now.Add(time.Duration(i)*10*time.Minute)); err != nil {
			t.Fatalf("Deliver fire %d: %v", i, err)
		}
	}

	// 264 fires span 44 hours, so the daily refresh permits exactly two copies:
	// the one that opened the run and one at the 24h mark. Both bounds matter —
	// the fix is not "write less mail", it is "one copy per run, never stale by
	// more than a day".
	if got := mail.count(); got != 2 {
		t.Fatalf("%d undelivered fires over 44h wrote %d mailbox copies, want 2 "+
			"(one opening the run, one at the %s refresh)", fires, got, fallbackRefreshInterval)
	}

	coalesced := eventsOfType(t, logPath, EventFallbackCoalesced)
	if len(coalesced) != fires-2 {
		t.Fatalf("coalesced events = %d, want %d — every copy that was not written must leave a record, "+
			"or a suppression is indistinguishable from the delivery bug it prevents", len(coalesced), fires-2)
	}
	last := details(t, coalesced[len(coalesced)-1])
	if last["reason"] != fallbackReasonNotRunning {
		t.Errorf("coalesced reason = %v, want %q", last["reason"], fallbackReasonNotRunning)
	}
	if last["schedule_id"] != entry.ID || last["to"] != entry.Agent {
		t.Errorf("coalesced event does not name the schedule it suppressed: %v", last)
	}
	if n, _ := last["copies_suppressed"].(float64); n < 2 {
		t.Errorf("copies_suppressed = %v, want the run length so far", last["copies_suppressed"])
	}

	// The copy that IS written has to say it stands for the ones behind it.
	// Read alone it would otherwise claim the schedule fired once.
	if !strings.Contains(mail.sent[0], "only mailbox copy") {
		t.Errorf("the surviving copy does not say it is the only one:\n%s", mail.sent[0])
	}
}

// A failed send leaves nothing in the mailbox, so it must NOT open a run.
// Suppressing later copies against a message that does not exist would trade a
// noisy mailbox for a silently undeliverable schedule — the strictly worse
// fault, and the one this ordering exists to prevent.
func TestFallbackRunDoesNotOpenOnAFailedSend(t *testing.T) {
	mail := &failingMail{fail: 2}
	d, _ := outageDeliverer(t, mail.send)
	entry := mailCheckEntry("pm-pogo", MailCheckIDPrefix+"pm-pogo")

	now := fixedTime()
	var errs int
	for i := 0; i < 4; i++ {
		if err := d.Deliver(context.Background(), entry, now.Add(time.Duration(i)*10*time.Minute)); err != nil {
			errs++
		}
	}
	if errs != 2 {
		t.Fatalf("failed sends surfaced = %d, want 2 — a send error must reach the scheduler as a delivery failure", errs)
	}
	if mail.attempts != 3 {
		t.Fatalf("send attempts = %d, want 3 (two refused, one accepted, then the run suppresses the fourth)", mail.attempts)
	}
	if mail.count() != 1 {
		t.Fatalf("mailbox copies = %d, want 1", mail.count())
	}
}

// The run is keyed per SCHEDULE, not per agent. An agent with a mail-check and
// a morning sweep must get one copy of each while it is unreachable: coalescing
// them together would drop the sweep's instruction on the floor, which is a
// different message, not a duplicate.
func TestFallbackRunsAreKeyedPerSchedule(t *testing.T) {
	mail := &failingMail{}
	d, _ := outageDeliverer(t, mail.send)
	now := fixedTime()

	for i := 0; i < 3; i++ {
		at := now.Add(time.Duration(i) * 10 * time.Minute)
		if err := d.Deliver(context.Background(), mailCheckEntry("pa", MailCheckIDPrefix+"pa"), at); err != nil {
			t.Fatalf("Deliver mail-check: %v", err)
		}
		sweep := mailCheckEntry("pa", "sweep-morning-pa")
		sweep.Message = "Run your morning sweep."
		if err := d.Deliver(context.Background(), sweep, at); err != nil {
			t.Fatalf("Deliver sweep: %v", err)
		}
	}
	if mail.count() != 2 {
		t.Fatalf("two schedules to one agent wrote %d copies, want 2 (one each)", mail.count())
	}
	var sawSweep bool
	for _, b := range mail.sent {
		if strings.Contains(b, "Run your morning sweep.") {
			sawSweep = true
		}
	}
	if !sawSweep {
		t.Errorf("the sweep's own copy was swallowed by the mail-check's run; bodies: %v", mail.sent)
	}
}

// A schedule whose delivery mode IS mail is not using a fallback — mail is the
// requested channel. Coalescing it would silently drop fires the operator asked
// to receive that way.
func TestDeliveryMailIsNeverCoalesced(t *testing.T) {
	mail := &failingMail{}
	d, _ := outageDeliverer(t, mail.send)
	entry := mailCheckEntry("doctor", "report-doctor")
	entry.Delivery = DeliveryMail

	now := fixedTime()
	for i := 0; i < 5; i++ {
		if err := d.Deliver(context.Background(), entry, now.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	}
	if mail.count() != 5 {
		t.Fatalf("delivery=mail wrote %d of 5 fires; an explicit mail schedule must never be coalesced", mail.count())
	}
}

// The run ends when a fire actually reaches the agent's terminal, so the next
// undelivered one writes its own copy. Without this the fix would suppress
// forever after a single outage: an agent that went down in May would still be
// riding that copy in August.
//
// The nudge is driven through the real wake policy — a limit episode opens and
// closes around the middle fire — because that is the only seam that produces a
// genuine delivered/undelivered transition on one live agent.
func TestFallbackRunClosesWhenAFireReachesThePTY(t *testing.T) {
	reg, a := spawnLiveAgent(t, "sched-coalesce")

	episode := true
	agent.SetLimitEpisodeQuery(func() (bool, string) { return episode, "usage-limit episode ep-9 open" })
	t.Cleanup(func() { agent.SetLimitEpisodeQuery(nil) })

	mail := &failingMail{}
	logPath := filepath.Join(t.TempDir(), "events.log")
	d := &PogodDeliverer{Registry: reg, Mail: mail.send, LogPath: logPath}
	entry := mailCheckEntry(a.Name, MailCheckIDPrefix+a.Name)

	now := fixedTime()
	// Two undelivered fires: one copy.
	for i := 0; i < 2; i++ {
		if err := d.Deliver(context.Background(), entry, now.Add(time.Duration(i)*10*time.Minute)); err != nil {
			t.Fatalf("Deliver suppressed fire %d: %v", i, err)
		}
	}
	if mail.count() != 1 {
		t.Fatalf("two suppressed fires wrote %d copies, want 1", mail.count())
	}

	// The episode clears and the next fire lands on the PTY. No mail, and the
	// run is closed.
	episode = false
	if err := d.Deliver(context.Background(), entry, now.Add(20*time.Minute)); err != nil {
		t.Fatalf("Deliver landed fire: %v", err)
	}
	if mail.count() != 1 {
		t.Fatalf("a fire that reached the PTY wrote a mailbox copy (%d total); the success path must never double-write",
			mail.count())
	}
	time.Sleep(300 * time.Millisecond)
	if out := string(a.RecentOutput(4096)); !strings.Contains(out, "Check your mail") {
		t.Fatalf("precondition: the middle fire did not reach the PTY, output: %q", out)
	}

	// A new outage writes a new copy rather than riding the old one.
	episode = true
	if err := d.Deliver(context.Background(), entry, now.Add(30*time.Minute)); err != nil {
		t.Fatalf("Deliver post-recovery fire: %v", err)
	}
	if mail.count() != 2 {
		t.Fatalf("mailbox copies = %d, want 2 — a delivered fire must close the run so the next outage is reported",
			mail.count())
	}
}
