package scheduler

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/stallwatch"
)

// The scheduler used to record every successful Deliver as one indistinct
// scheduler_fire_delivered: a PTY nudge, a nudge typed mid-turn with no
// receipt, a mail fallback and a coalesced fallback that wrote nothing all
// looked the same (drellem2/pogo#204). These tests pin the channel keys each
// branch stamps, and that those keys describe the CHANNEL, never completion.

// runningLookup is an AgentLookup whose every agent is running. The PTY half
// is stubbed through PogodDeliverer.nudge, so no process is needed.
type runningLookup struct{}

func (runningLookup) Get(name string) *agent.Agent {
	return &agent.Agent{Name: name, Status: agent.StatusRunning}
}

// nudgeReturning is a PTY stub that returns err and counts calls.
func nudgeReturning(err error, calls *int) func(*agent.Agent, string, string) error {
	return func(*agent.Agent, string, string) error {
		*calls++
		return err
	}
}

func TestDeliverOutcomeNamesEachBranch(t *testing.T) {
	refusal := errors.New("agent still producing output after 30s")
	queued := fmt.Errorf("nudge p1: %w", agent.ErrNudgeQueued)
	suppressed := fmt.Errorf("%w: usage-limit episode ep-3 open", agent.ErrWakeSuppressed)

	cases := []struct {
		name      string
		delivery  DeliveryMode
		running   bool
		nudgeErr  error
		want      DeliveryOutcome
		wantMails int
	}{
		{name: "pty", running: true,
			want: DeliveryOutcome{Channel: NudgeDeliveryPTY}},
		{name: "pty_unconfirmed", running: true, nudgeErr: queued,
			want: DeliveryOutcome{Channel: NudgeDeliveryPTYUnconfirmed, NudgeError: queued.Error()}},
		{name: "nudge_failed", running: true, nudgeErr: refusal, wantMails: 1,
			want: DeliveryOutcome{Channel: NudgeDeliveryMailFallback, FallbackReason: fallbackReasonNudgeFail, NudgeError: refusal.Error()}},
		{name: "wake_suppressed", running: true, nudgeErr: suppressed, wantMails: 1,
			want: DeliveryOutcome{Channel: NudgeDeliveryMailFallback, FallbackReason: fallbackReasonSuppressed, NudgeError: suppressed.Error()}},
		{name: "agent_not_running", wantMails: 1,
			want: DeliveryOutcome{Channel: NudgeDeliveryMail, FallbackReason: fallbackReasonNotRunning}},
		{name: "delivery_mail", delivery: DeliveryMail, running: true, wantMails: 1,
			want: DeliveryOutcome{Channel: NudgeDeliveryMail}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mail := &recordingMail{}
			var nudges int
			d := &PogodDeliverer{
				Mail:    mail.send,
				LogPath: filepath.Join(t.TempDir(), "events.log"),
				nudge:   nudgeReturning(tc.nudgeErr, &nudges),
			}
			if tc.running {
				d.Registry = runningLookup{}
			}
			entry := mailCheckEntry("p1", "mail-check-p1")
			if tc.delivery != "" {
				entry.Delivery = tc.delivery
			}
			got, err := d.DeliverOutcome(context.Background(), entry, fixedTime())
			if err != nil {
				t.Fatalf("DeliverOutcome: %v", err)
			}
			if got != tc.want {
				t.Errorf("outcome = %+v, want %+v", got, tc.want)
			}
			if mail.count() != tc.wantMails {
				t.Errorf("mailbox copies = %d, want %d", mail.count(), tc.wantMails)
			}
		})
	}
}

// A coalesced fire rides a copy already in the box and writes nothing, so it
// must not be labelled mail — that would claim a mailbox copy that does not
// exist.
func TestCoalescedFallbackIsNotLabelledMail(t *testing.T) {
	mail := &recordingMail{}
	d, _ := outageDeliverer(t, mail.send)
	entry := mailCheckEntry("architect", "mail-check-architect")

	first, err := d.DeliverOutcome(context.Background(), entry, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.DeliverOutcome(context.Background(), entry, fixedTime().Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if mail.count() != 1 {
		t.Fatalf("precondition: two fires wrote %d copies, want 1", mail.count())
	}
	if first.Channel != NudgeDeliveryMail {
		t.Errorf("the fire that wrote the copy = %q, want %q", first.Channel, NudgeDeliveryMail)
	}
	if second.Channel == NudgeDeliveryMail {
		t.Fatalf("a coalesced fire that wrote nothing was labelled %q", NudgeDeliveryMail)
	}
	if second.Channel != NudgeDeliveryMailCoalesced || second.FallbackReason != fallbackReasonNotRunning {
		t.Errorf("coalesced outcome = %+v, want channel %q reason %q",
			second, NudgeDeliveryMailCoalesced, fallbackReasonNotRunning)
	}
}

// A failed send carried nothing: the error reaches the scheduler, and the
// outcome names no channel.
func TestFailedFallbackSendNamesNoChannel(t *testing.T) {
	mail := &failingMail{fail: 1}
	d, _ := outageDeliverer(t, mail.send)
	got, err := d.DeliverOutcome(context.Background(), mailCheckEntry("p1", "mail-check-p1"), fixedTime())
	if err == nil {
		t.Fatal("a refused send must surface as a delivery error")
	}
	if got.Channel != "" {
		t.Errorf("channel = %q on a send that wrote nothing, want empty", got.Channel)
	}
}

// channelAckingDeliverer wraps the real deliverer and, when ack is set, redeems the
// fire's token before returning — an agent answering inside the delivery
// window, the fastest an ack can ever land relative to the delivered event.
type channelAckingDeliverer struct {
	inner *PogodDeliverer
	s     *Scheduler
	ack   bool
}

func (d *channelAckingDeliverer) Deliver(ctx context.Context, e Entry, t time.Time) error {
	_, err := d.DeliverOutcome(ctx, e, t)
	return err
}

func (d *channelAckingDeliverer) DeliverOutcome(ctx context.Context, e Entry, t time.Time) (DeliveryOutcome, error) {
	out, err := d.inner.DeliverOutcome(ctx, e, t)
	if err == nil && d.ack {
		if _, aerr := d.s.Ack(e.Agent, e.ID, e.PendingToken, t.Add(time.Second)); aerr != nil {
			return out, fmt.Errorf("ack: %w", aerr)
		}
	}
	return out, err
}

// Each channel value reaches scheduler_fire_delivered and FireResult, and reads
// the same whether or not the agent went on to ack the fire: nudge_delivery is
// the CHANNEL, and never stands in for completion (pm-pogo's requirement on
// drellem2/pogo#204). The completion counters move; the channel keys do not.
func TestFireDeliveredStampsTheChannelNotCompletion(t *testing.T) {
	queued := fmt.Errorf("nudge: %w", agent.ErrNudgeQueued)
	refusal := errors.New("agent still producing output after 30s")

	cases := []struct {
		name     string
		running  bool
		nudgeErr error
		prime    bool // deliver one fire first so this one coalesces
		want     map[string]any
	}{
		{name: "pty", running: true,
			want: map[string]any{"nudge_delivery": "pty"}},
		{name: "pty_unconfirmed", running: true, nudgeErr: queued,
			want: map[string]any{"nudge_delivery": "pty_unconfirmed", "nudge_error": queued.Error()}},
		{name: "mail_fallback", running: true, nudgeErr: refusal,
			want: map[string]any{"nudge_delivery": "mail_fallback", "nudge_fallback_reason": "nudge_failed", "nudge_error": refusal.Error()}},
		{name: "mail", running: false,
			want: map[string]any{"nudge_delivery": "mail", "nudge_fallback_reason": "agent_not_running"}},
		{name: "mail_coalesced", prime: true,
			want: map[string]any{"nudge_delivery": "mail_coalesced", "nudge_fallback_reason": "agent_not_running"}},
	}
	for _, tc := range cases {
		for _, acked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/acked=%v", tc.name, acked), func(t *testing.T) {
				mail := &recordingMail{}
				var nudges int
				inner := &PogodDeliverer{
					Mail:    mail.send,
					LogPath: filepath.Join(t.TempDir(), "coalesce-events.log"),
					nudge:   nudgeReturning(tc.nudgeErr, &nudges),
				}
				if tc.running {
					inner.Registry = runningLookup{}
				}
				s := newSchedulerForTest(t, nil)
				d := &channelAckingDeliverer{inner: inner, s: s}
				s.deliverer = d

				now := fixedTime()
				addFiring(t, s, "p1", "mail-check-p1", now)
				if tc.prime {
					// The first fire opens the run (and is never acked); the
					// fire under test rides it.
					s.Tick(context.Background(), now)
					now = now.Add(time.Minute)
				}
				d.ack = acked
				res := s.Tick(context.Background(), now)
				if len(res) != 1 || !res[0].Delivered {
					t.Fatalf("Tick = %+v, want one delivered fire", res)
				}

				evs := eventsOfType(t, s.logPath, "scheduler_fire_delivered")
				if len(evs) == 0 {
					t.Fatal("no scheduler_fire_delivered event")
				}
				det := details(t, evs[len(evs)-1])
				for _, k := range []string{"nudge_delivery", "nudge_fallback_reason", "nudge_error"} {
					want, wantOK := tc.want[k]
					got, gotOK := det[k]
					if wantOK != gotOK || got != want {
						t.Errorf("%s = %v (present %v), want %v (present %v)", k, got, gotOK, want, wantOK)
					}
				}
				if res[0].Delivery.Channel != tc.want["nudge_delivery"] {
					t.Errorf("FireResult.Delivery.Channel = %q, want %v", res[0].Delivery.Channel, tc.want["nudge_delivery"])
				}

				// Positive control that the ack arm really differs: the
				// completion counter moved, and only it did.
				completed, _ := det["fires_completed"].(float64)
				if acked && completed != 1 {
					t.Errorf("acked arm: fires_completed = %v, want 1 — the ack did not land", det["fires_completed"])
				}
				if !acked && completed != 0 {
					t.Errorf("un-acked arm: fires_completed = %v, want 0", det["fires_completed"])
				}
			})
		}
	}
}

// A deliverer that does not report a channel leaves the keys off rather than
// guessing one.
func TestPlainDelivererStampsNoChannel(t *testing.T) {
	s := newSchedulerForTest(t, &recorder{})
	now := fixedTime()
	addFiring(t, s, "p1", "mail-check-p1", now)
	s.Tick(context.Background(), now)
	evs := eventsOfType(t, s.logPath, "scheduler_fire_delivered")
	if len(evs) != 1 {
		t.Fatalf("delivered events = %d, want 1", len(evs))
	}
	det := details(t, evs[0])
	for _, k := range []string{"nudge_delivery", "nudge_fallback_reason", "nudge_error"} {
		if v, ok := det[k]; ok {
			t.Errorf("%s = %v from a deliverer that reports no channel, want absent", k, v)
		}
	}
}

// stall-watch and the scheduler stamp the same nudge_delivery key, so the same
// situation must carry the same value in both, or a query across the two
// detectors splits one event class in two (drellem2/pogo#204 review). A running
// agent whose PTY refused -> mail_fallback; an agent not running -> mail.
// The not-running arm is the positive control: it pins that the split is by
// situation, not that every mail became mail_fallback.
func TestFallbackChannelMatchesStallWatch(t *testing.T) {
	refusal := errors.New("agent still producing output after 30s")
	suppressed := fmt.Errorf("%w: usage-limit episode ep-3 open", agent.ErrWakeSuppressed)
	cases := []struct {
		name     string
		running  bool
		nudgeErr error
		want     string
	}{
		{"running, nudge failed", true, refusal, stallwatch.DeliveryMailFallback},
		{"running, wake suppressed", true, suppressed, stallwatch.DeliveryMailFallback},
		{"not running", false, nil, stallwatch.DeliveryMail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var nudges int
			d := &PogodDeliverer{
				Mail:    (&recordingMail{}).send,
				LogPath: filepath.Join(t.TempDir(), "events.log"),
				nudge:   nudgeReturning(tc.nudgeErr, &nudges),
			}
			if tc.running {
				d.Registry = runningLookup{}
			}
			got, err := d.DeliverOutcome(context.Background(), mailCheckEntry("p1", "mail-check-p1"), fixedTime())
			if err != nil {
				t.Fatal(err)
			}
			if got.Channel != tc.want {
				t.Errorf("scheduler nudge_delivery = %q, stall-watch uses %q for the same situation", got.Channel, tc.want)
			}
		})
	}
}

// A failed fallback send used to log scheduler_fire_failed with only the
// error, dropping why the PTY refused and why the fire fell back. The failure
// event carries those keys now — and still names no channel, since nothing was
// carried. The delivered arm on the same deliverer is the positive control that
// the keys come from the outcome, not from the failure path inventing them.
func TestFireFailedKeepsTheOutcome(t *testing.T) {
	refusal := errors.New("agent still producing output after 30s")
	for _, tc := range []struct {
		name      string
		running   bool
		sendFails bool
		event     string
		want      map[string]any
	}{
		{name: "running, send fails", running: true, sendFails: true, event: "scheduler_fire_failed",
			want: map[string]any{"nudge_fallback_reason": "nudge_failed", "nudge_error": refusal.Error()}},
		{name: "not running, send fails", sendFails: true, event: "scheduler_fire_failed",
			want: map[string]any{"nudge_fallback_reason": "agent_not_running"}},
		{name: "running, send succeeds", running: true, event: "scheduler_fire_delivered",
			want: map[string]any{"nudge_delivery": "mail_fallback", "nudge_fallback_reason": "nudge_failed", "nudge_error": refusal.Error()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fail := 0
			if tc.sendFails {
				fail = 1
			}
			var nudges int
			d := &PogodDeliverer{
				Mail:    (&failingMail{fail: fail}).send,
				LogPath: filepath.Join(t.TempDir(), "events.log"),
				nudge:   nudgeReturning(refusal, &nudges),
			}
			if tc.running {
				d.Registry = runningLookup{}
			}
			s := newSchedulerForTest(t, nil)
			s.deliverer = d
			now := fixedTime()
			addFiring(t, s, "p1", "mail-check-p1", now)
			res := s.Tick(context.Background(), now)
			if len(res) != 1 || res[0].Delivered == tc.sendFails {
				t.Fatalf("Tick = %+v, want one fire with Delivered=%v", res, !tc.sendFails)
			}
			evs := eventsOfType(t, s.logPath, tc.event)
			if len(evs) != 1 {
				t.Fatalf("%s events = %d, want 1", tc.event, len(evs))
			}
			det := details(t, evs[0])
			for _, k := range []string{"nudge_delivery", "nudge_fallback_reason", "nudge_error"} {
				want, wantOK := tc.want[k]
				got, gotOK := det[k]
				if wantOK != gotOK || got != want {
					t.Errorf("%s = %v (present %v), want %v (present %v)", k, got, gotOK, want, wantOK)
				}
			}
			if tc.sendFails {
				if _, ok := det["error"]; !ok {
					t.Error("failed event lost its error")
				}
				if res[0].Delivery.Channel != "" || res[0].Delivery.FallbackReason != tc.want["nudge_fallback_reason"] {
					t.Errorf("FireResult.Delivery = %+v, want no channel and reason %v", res[0].Delivery, tc.want["nudge_fallback_reason"])
				}
			}
		})
	}
}

// A delivery: mail schedule whose send fails wrote nothing, so neither the
// outcome nor the failure event may say nudge_delivery=mail.
func TestFailedMailScheduleNamesNoChannel(t *testing.T) {
	d, _ := outageDeliverer(t, (&failingMail{fail: 1}).send)
	entry := mailCheckEntry("p1", "mail-check-p1")
	entry.Delivery = DeliveryMail
	got, err := d.DeliverOutcome(context.Background(), entry, fixedTime())
	if err == nil {
		t.Fatal("a refused send must surface as a delivery error")
	}
	if got.Channel != "" {
		t.Errorf("channel = %q on a send that wrote nothing, want empty", got.Channel)
	}
}
