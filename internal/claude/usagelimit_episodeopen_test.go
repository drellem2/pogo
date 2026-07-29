package claude

import (
	"strings"
	"testing"
	"time"
)

// episodeOpen is the READ the wake-cycle policy pulls (mg-8184). These tests
// prove it answers in both directions — a detector accessor only ever observed
// saying "yes" has not been observed deciding.

// A coordinator with no episode open must say so. This is the answer that lets
// a wake fire, so getting it wrong would suppress every wake in the fleet.
func TestEpisodeOpen_QuietFleetIsNotAnEpisode(t *testing.T) {
	sink := &mailSink{}
	c, _, _ := newHeldCoordinator(sink.send)

	if open, detail := c.episodeOpen(); open {
		t.Fatalf("a quiet fleet is not an episode, got open=true detail=%q", detail)
	}
}

// An open episode reports open, and names itself well enough that a suppressed
// wake can say why.
func TestEpisodeOpen_ReportsOpenEpisodeWithDetail(t *testing.T) {
	sink := &mailSink{}
	c, _, _ := newHeldCoordinator(sink.send)
	now := fixedNow()()

	c.OnHit("cat-mg-7ffa", "mg-7ffa", now)

	open, detail := c.episodeOpen()
	if !open {
		t.Fatal("episode should read open once an agent is flagged")
	}
	if !strings.Contains(detail, "usage-limit episode") {
		t.Errorf("detail should name what kind of episode, got %q", detail)
	}
	if !strings.Contains(detail, "1 agent(s) rate-limited") {
		t.Errorf("detail should count the limited agents, got %q", detail)
	}
}

// Inside the hold-down the episode still reads OPEN. The hold-down governs
// whether a human is PAGED (mg-4904's flap suppression); the wake policy asks a
// different question — is an agent wedged on the modal right now — and during
// the hold-down the answer is yes.
func TestEpisodeOpen_OpenDuringHoldDown(t *testing.T) {
	sink := &mailSink{}
	c, _, _ := newHeldCoordinator(sink.send)
	now := fixedNow()()

	c.OnHit("cat-mg-7ffa", "mg-7ffa", now)
	if sink.count() != 0 {
		t.Fatalf("precondition: hit mail must not have fired yet, got %d", sink.count())
	}
	if open, _ := c.episodeOpen(); !open {
		t.Error("an episode inside its hold-down is still an episode to the wake policy")
	}
}

// The episode closes when the last limited agent clears, and the read follows
// it back down. Without this the first episode of a daemon's life would
// suppress every wake forever.
func TestEpisodeOpen_ClosesWithTheEpisode(t *testing.T) {
	sink := &mailSink{}
	c, h, _ := newHeldCoordinator(sink.send)
	now := fixedNow()()

	c.OnHit("cat-a", "mg-a", now)
	c.OnHit("cat-b", "mg-b", now.Add(time.Minute))
	h.fire()

	if open, _ := c.episodeOpen(); !open {
		t.Fatal("precondition: episode should be open with two agents limited")
	}

	// One of two clears: still an episode.
	c.OnClear("cat-a", now.Add(time.Hour))
	if open, detail := c.episodeOpen(); !open {
		t.Error("episode should stay open while another agent is still limited")
	} else if !strings.Contains(detail, "1 agent(s)") {
		t.Errorf("detail should track the shrinking roster, got %q", detail)
	}

	// The last one clears: not an episode any more.
	c.OnClear("cat-b", now.Add(2*time.Hour))
	if open, detail := c.episodeOpen(); open {
		t.Errorf("episode should close with the last limited agent, got open=true detail=%q", detail)
	}
}
