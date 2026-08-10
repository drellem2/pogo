package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/stallwatch"
)

// The mg-61ce suite. The defect these cover is not that the mail fallback is
// noisy — it is that the fallback's load RISES with the load it is responding
// to, because the fallback fires precisely when the recipient is too busy to go
// idle and answers by adding work to that recipient's inbox. There was no
// damping term anywhere in that loop.
//
// The unread_mail category makes it a closed loop rather than merely a perverse
// one: its notice reads "your inbox is too full" and is delivered AS one more
// message in that inbox, so the remedy re-arms its own trigger. On the box this
// was measured on, 179 such self-referential messages were sitting in the
// coordinator's mailbox at once.

// TestStallFallbackDamperBoundsTheLoopItsRemedyCreates is the headline
// regression test. It drives the fallback road repeatedly against one recipient
// that never goes idle — the exact production condition — and asserts the number
// of mails is bounded by the cap rather than tracking the number of fires.
//
// Before mg-61ce these two numbers were equal by construction, and that equality
// IS the defect: fires rise with coordinator load, so mails rose with
// coordinator load, so the remedy's cost was proportional to the problem it was
// remedying. Asserting `mails < fires` would be too weak to catch a regression
// that merely slowed the growth; the assertion is the exact cap, because a
// damping term with no fixed ceiling is not a damping term.
func TestStallFallbackDamperBoundsTheLoopItsRemedyCreates(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	spawnBusyAgent(t, reg, "busy-mayor")

	mails := 0
	const capLimit = 3
	nudge := newStallNudgerWithTimeoutAndDamper(reg, func(to, from, subject, body string) error {
		mails++
		return nil
	}, 150*time.Millisecond, newStallFallbackDamper(capLimit))

	const fires = 10
	suppressed := 0
	for i := 0; i < fires; i++ {
		delivery, err := nudge("busy-mayor", fmt.Sprintf("stall notice %d", i))
		// Suppression is a decision, not a failure: it must never surface as an
		// error, or `fire` would stamp nudge_error and the log would read as if
		// every channel had been tried and failed.
		if err != nil {
			t.Fatalf("fire %d: suppression must not report an error: %v", i, err)
		}
		if delivery.Channel == stallwatch.DeliverySuppressed {
			suppressed++
			if delivery.SuppressedConsecutive != i+1 {
				t.Errorf("fire %d: SuppressedConsecutive = %d, want %d — the counter must keep climbing "+
					"across suppressed fires, since a run that keeps growing is the escalation signal",
					i, delivery.SuppressedConsecutive, i+1)
			}
			if delivery.FallbackReason == "" {
				t.Errorf("fire %d: a suppressed fire must still record WHY the PTY refused it", i)
			}
		}
	}

	if mails != capLimit {
		t.Errorf("mails = %d over %d fires, want exactly %d (the cap). Before mg-61ce this was %d — "+
			"one mail per fire, so the remedy's load rose with the load it was responding to",
			mails, fires, capLimit, fires)
	}
	if suppressed != fires-capLimit {
		t.Errorf("suppressed = %d, want %d", suppressed, fires-capLimit)
	}
}

// TestStallFallbackDamperResetsOnPTYDelivery proves the term is damping, not a
// latch. A successful PTY delivery is direct evidence the agent went idle — the
// precise condition that means it is between turns and able to drain its inbox —
// so it clears the run and the next fallback starts fresh.
//
// Without this the cap would be a one-way ratchet: a coordinator that recovered
// would stay permanently silenced, which trades a flood for a blackout.
func TestStallFallbackDamperResetsOnPTYDelivery(t *testing.T) {
	d := newStallFallbackDamper(2)

	if n, allow := d.admit("mayor"); !allow || n != 1 {
		t.Fatalf("first fallback: n=%d allow=%v, want 1/true", n, allow)
	}
	if n, allow := d.admit("mayor"); !allow || n != 2 {
		t.Fatalf("second fallback: n=%d allow=%v, want 2/true", n, allow)
	}
	if n, allow := d.admit("mayor"); allow || n != 3 {
		t.Fatalf("third fallback: n=%d allow=%v, want 3/false (over cap 2)", n, allow)
	}

	// The agent went idle: reachable, and able to drain.
	d.reset("mayor")

	if n, allow := d.admit("mayor"); !allow || n != 1 {
		t.Fatalf("after PTY delivery the run must restart: n=%d allow=%v, want 1/true", n, allow)
	}
	// The loud-log latch resets too, so a SECOND saturation episode is announced
	// rather than swallowed by the first episode's flag.
	d.admit("mayor")
	d.admit("mayor")
	if !d.announce("mayor") {
		t.Error("a new saturation episode must announce itself; the once-per-run latch has to clear on reset")
	}
}

// TestStallFallbackDamperIsPerRecipient proves one saturated recipient does not
// silence the others. The counter is keyed per recipient because the condition
// it measures — "this agent has not gone idle once since I started mailing it" —
// is a property of that agent, not of the daemon. A global counter would let a
// wedged coordinator suppress the blocked-reminder notices addressed to entirely
// different agents (stallwatch fireTo), which are a different fact reaching a
// different reader.
func TestStallFallbackDamperIsPerRecipient(t *testing.T) {
	d := newStallFallbackDamper(1)

	if _, allow := d.admit("mayor"); !allow {
		t.Fatal("mayor's first fallback must be allowed")
	}
	if _, allow := d.admit("mayor"); allow {
		t.Fatal("mayor's second fallback must be suppressed at cap 1")
	}
	if _, allow := d.admit("pm-onethird"); !allow {
		t.Fatal("a saturated mayor must not silence a different recipient — the counter is keyed per agent")
	}
}

// TestStallFallbackDamperNegativeCapDisablesDamping covers the documented escape
// hatch. A negative cap restores pre-mg-61ce behaviour exactly, which matters
// because the damping withholds notices: an operator who decides the flood is
// preferable to any suppression must be able to say so, and be able to tell
// from the startup line that they did.
//
// Zero is NOT that switch — zero means "unset" and resolves to the default,
// matching every other stall-watch knob. A config typo of 0 must not silently
// disarm the damper.
func TestStallFallbackDamperNegativeCapDisablesDamping(t *testing.T) {
	off := newStallFallbackDamper(-1)
	for i := 0; i < 100; i++ {
		if n, allow := off.admit("mayor"); !allow || n != 0 {
			t.Fatalf("negative cap must disable damping entirely: fallback %d got n=%d allow=%v", i, n, allow)
		}
	}

	zero := newStallFallbackDamper(0)
	if zero.cap != config.DefaultStallMailFallbackBacklogCap {
		t.Errorf("cap 0 = %d, want the default %d — zero means unset, not disabled",
			zero.cap, config.DefaultStallMailFallbackBacklogCap)
	}
}

// TestStallFallbackDamperAnnouncesOncePerRun proves the loud log line is itself
// damped. This is the remedy-audits-itself check: a suppression notice printed
// on every fire would reproduce, in the log, the exact flood-under-load shape
// the damper exists to remove — the same defect wearing a different channel.
func TestStallFallbackDamperAnnouncesOncePerRun(t *testing.T) {
	d := newStallFallbackDamper(1)
	d.admit("mayor")
	d.admit("mayor")

	if !d.announce("mayor") {
		t.Fatal("the first suppression of a run must announce")
	}
	for i := 0; i < 10; i++ {
		if d.announce("mayor") {
			t.Fatalf("suppression %d re-announced; the loud line must fire once per run, not once per fire", i)
		}
	}
}

// TestStallFallbackDampingDoesNotTouchTheOfflineRoad pins the documented scope
// boundary. The offline road (recipient not running) has the same flooding
// shape — 303 of the measured fires took it — but it has no reset signal: an
// offline agent never produces the PTY success that clears the counter, so a cap
// there would latch permanently the first time a coordinator went down, which is
// a blackout rather than a damping term. It is left undamped on purpose, and a
// future change that quietly extends the cap to cover it should fail here and be
// made to argue for itself.
func TestStallFallbackDampingDoesNotTouchTheOfflineRoad(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	mails := 0
	nudge := newStallNudgerWithTimeoutAndDamper(reg, func(to, from, subject, body string) error {
		mails++
		return nil
	}, 50*time.Millisecond, newStallFallbackDamper(1))

	const fires = 5
	for i := 0; i < fires; i++ {
		// No such agent in the registry: the offline road.
		delivery, err := nudge("ghost-mayor", "stall notice")
		if err != nil {
			t.Fatalf("fire %d: %v", i, err)
		}
		if delivery.Channel != stallwatch.DeliveryMail {
			t.Fatalf("fire %d: channel = %q, want %q", i, delivery.Channel, stallwatch.DeliveryMail)
		}
	}
	if mails != fires {
		t.Errorf("offline road: mails = %d over %d fires, want %d — the cap bounds the FALLBACK road only",
			mails, fires, fires)
	}
}

// TestStallFallbackDamperClearsRunWhenRecipientGoesOffline covers the second
// reset signal, which is both a correctness rule and the damper's only bound on
// its own memory.
//
// Correctness: a coordinator that died and came back is a new process with a new
// PTY. A fallback run accumulated against the old one says nothing about the new
// one, and carrying it over would suppress the first notices to a freshly
// restarted, perfectly reachable agent — turning a crash into a silence.
//
// Housekeeping: recipients are not a fixed set. `blocked:<agent>` reminders
// (mg-3844) address polecats, and polecat names are unique per spawn, so without
// this prune the map would gain a permanent entry for every polecat that ever
// drew a suppressed reminder and never a PTY success. Every such agent
// eventually stops running and takes the offline road, so this is where the
// entry goes.
func TestStallFallbackDamperClearsRunWhenRecipientGoesOffline(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	damper := newStallFallbackDamper(1)
	nudge := newStallNudgerWithTimeoutAndDamper(reg, func(to, from, subject, body string) error {
		return nil
	}, 50*time.Millisecond, damper)

	// Saturate the recipient by hand: two fallbacks at cap 1 leaves it
	// suppressed and announced.
	damper.admit("gone-mayor")
	damper.admit("gone-mayor")
	if _, allow := damper.admit("gone-mayor"); allow {
		t.Fatal("precondition: the recipient should be suppressed before it goes offline")
	}

	// It is not in the registry, so this fire takes the offline road.
	if _, err := nudge("gone-mayor", "stall notice"); err != nil {
		t.Fatalf("offline nudge: %v", err)
	}

	if n := len(damper.consecutive); n != 0 {
		t.Errorf("damper retained %d recipient entries after the offline road; the map must be pruned "+
			"there or it grows one entry per polecat name for the daemon's whole lifetime", n)
	}
	if n, allow := damper.admit("gone-mayor"); !allow || n != 1 {
		t.Errorf("after going offline the run must restart: n=%d allow=%v, want 1/true — a restarted "+
			"coordinator is reachable, and inheriting the dead process's run would silence it", n, allow)
	}
}
