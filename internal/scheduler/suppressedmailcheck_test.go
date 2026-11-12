package scheduler

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// A mail-check fire whose terminal wake the wake-cycle policy declined used to
// fall back to a mailbox copy saying "check your mail" — into the box it was
// telling the agent to read. That copy is a no-op turn: measured on one fleet
// over a week, ~107 a day, essentially all with reason wake_suppressed
// (drellem2/pogo#299). So a suppressed mail-check fire now writes nothing.
//
// The other side is pinned in the same test and is the reason the change is
// keyed on the schedule's KIND: a suppressed sweep or one-shot still mails,
// because for those the copy is the only delivery of something the agent cannot
// reconstruct by reading its mail (mg-8184). Each side is the other's control —
// the same deliverer, the same suppression error, the same running agent; only
// the kind differs.
func TestSuppressedWakeFallbackDependsOnKind(t *testing.T) {
	suppressed := fmt.Errorf("%w: already woken 4m ago", agent.ErrWakeSuppressed)

	cases := []struct {
		name      string
		entry     Entry
		wantMails int
		wantChan  string
	}{
		{name: "mail-check (inferred kind)", entry: mailCheckEntry("crew-a", "mail-check-crew-a"),
			wantMails: 0, wantChan: NudgeDeliverySuppressed},
		{name: "mail-check (stamped kind, unconventional id)",
			entry:     func() Entry { e := mailCheckEntry("crew-a", "inbox-poll"); e.Kind = KindMailCheck; return e }(),
			wantMails: 0, wantChan: NudgeDeliverySuppressed},
		{name: "sweep", entry: func() Entry {
			e := mailCheckEntry("pm-x", "sweep-evening-pm-x")
			e.Message, e.Cron = "sweep", "3 17 * * *"
			return e
		}(), wantMails: 1, wantChan: NudgeDeliveryMailFallback},
		{name: "one-shot reminder", entry: func() Entry {
			e := mailCheckEntry("mayor", "gate-lift-mg-1234")
			e.Message, e.Cron = "lift the gate on mg-1234", ""
			return e
		}(), wantMails: 1, wantChan: NudgeDeliveryMailFallback},
		{name: "ad-hoc kind", entry: func() Entry {
			e := mailCheckEntry("crew-r", "research-poll")
			e.Message = "poll the research queue"
			return e
		}(), wantMails: 1, wantChan: NudgeDeliveryMailFallback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mail := &recordingMail{}
			var nudges int
			d := &PogodDeliverer{
				Registry: runningLookup{},
				Mail:     mail.send,
				LogPath:  filepath.Join(t.TempDir(), "events.log"),
				nudge:    nudgeReturning(suppressed, &nudges),
			}
			got, err := d.DeliverOutcome(context.Background(), tc.entry, fixedTime())
			if err != nil {
				t.Fatalf("DeliverOutcome: %v", err)
			}
			if nudges != 1 {
				t.Fatalf("precondition: PTY tried %d times, want 1", nudges)
			}
			if mail.count() != tc.wantMails {
				t.Fatalf("mailbox copies = %d, want %d", mail.count(), tc.wantMails)
			}
			if got.Channel != tc.wantChan || got.FallbackReason != fallbackReasonSuppressed || got.NudgeError != suppressed.Error() {
				t.Errorf("outcome = %+v, want channel %q reason %q error %q",
					got, tc.wantChan, fallbackReasonSuppressed, suppressed.Error())
			}
			if tc.wantMails == 1 {
				_, _, body := mail.last()
				if !strings.Contains(body, "terminal wake suppressed") {
					t.Errorf("the copy should say the wake was suppressed, got:\n%s", body)
				}
			}
		})
	}
}

// A suppressed mail-check fire is still recorded as a fire with a channel, so
// the event log keeps one row per fire: nudge_delivery=suppressed with the
// reason and the policy's refusal, never a silent gap.
func TestSuppressedMailCheckFireStampsItsChannel(t *testing.T) {
	suppressed := fmt.Errorf("%w: already woken 4m ago", agent.ErrWakeSuppressed)
	var nudges int
	inner := &PogodDeliverer{
		Registry: runningLookup{},
		Mail:     (&recordingMail{}).send,
		LogPath:  filepath.Join(t.TempDir(), "events.log"),
		nudge:    nudgeReturning(suppressed, &nudges),
	}
	s := newSchedulerForTest(t, nil)
	s.deliverer = inner
	now := fixedTime()
	addFiring(t, s, "p1", "mail-check-p1", now)
	res := s.Tick(context.Background(), now)
	if len(res) != 1 || !res[0].Delivered {
		t.Fatalf("Tick = %+v, want one recorded fire", res)
	}
	evs := eventsOfType(t, s.logPath, "scheduler_fire_delivered")
	if len(evs) != 1 {
		t.Fatalf("scheduler_fire_delivered events = %d, want 1", len(evs))
	}
	det := details(t, evs[0])
	want := map[string]any{
		"nudge_delivery":        NudgeDeliverySuppressed,
		"nudge_fallback_reason": fallbackReasonSuppressed,
		"nudge_error":           suppressed.Error(),
	}
	for k, v := range want {
		if det[k] != v {
			t.Errorf("%s = %v, want %v", k, det[k], v)
		}
	}
}
