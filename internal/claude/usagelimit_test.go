package claude

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// mailSink captures coordinator mails for assertions.
type mailSink struct {
	mu    sync.Mutex
	mails []sentMail
}

type sentMail struct {
	to, from, subject, body string
}

func (s *mailSink) send(to, from, subject, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mails = append(s.mails, sentMail{to, from, subject, body})
	return nil
}

func (s *mailSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.mails)
}

func (s *mailSink) at(i int) sentMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mails[i]
}

func fixedNow() func() time.Time {
	t := time.Unix(1_700_000_000, 0).UTC()
	return func() time.Time { return t }
}

// A single agent hitting and clearing produces exactly one hit + one clear
// mail, both to human.
func TestUsageLimitCoordinator_SingleAgentHitAndClear(t *testing.T) {
	sink := &mailSink{}
	c := newUsageLimitCoordinator(sink.send, fixedNow())
	now := fixedNow()()

	c.OnHit("cat-mg-7ffa", "mg-7ffa", now)
	if sink.count() != 1 {
		t.Fatalf("expected 1 hit mail, got %d", sink.count())
	}
	hit := sink.at(0)
	if hit.to != "human" {
		t.Errorf("hit mail to = %q, want human", hit.to)
	}
	if !strings.Contains(hit.subject, "hit") {
		t.Errorf("hit subject = %q, want it to mention hit", hit.subject)
	}
	if !strings.Contains(hit.body, "cat-mg-7ffa") {
		t.Errorf("hit body should name the agent, got %q", hit.body)
	}

	c.OnClear("cat-mg-7ffa", now.Add(time.Hour))
	if sink.count() != 2 {
		t.Fatalf("expected 2 mails after clear, got %d", sink.count())
	}
	clear := sink.at(1)
	if clear.to != "human" {
		t.Errorf("clear mail to = %q, want human", clear.to)
	}
	if !strings.Contains(clear.subject, "cleared") {
		t.Errorf("clear subject = %q, want it to mention cleared", clear.subject)
	}
	// Resume checklist must name the agent + a recovery command.
	if !strings.Contains(clear.body, "cat-mg-7ffa") ||
		!strings.Contains(clear.body, "pogo nudge mg-7ffa") {
		t.Errorf("clear body missing resume checklist entry, got:\n%s", clear.body)
	}
}

// Two agents hitting the same episode produce ONE hit mail (not two), and one
// clear mail that lists BOTH agents in the roster.
func TestUsageLimitCoordinator_CoalescesFleetEpisode(t *testing.T) {
	sink := &mailSink{}
	c := newUsageLimitCoordinator(sink.send, fixedNow())
	now := fixedNow()()

	c.OnHit("cat-mg-aaaa", "mg-aaaa", now)
	c.OnHit("cat-mg-bbbb", "mg-bbbb", now.Add(time.Minute))
	c.OnHit("cat-mg-aaaa", "mg-aaaa", now.Add(2*time.Minute)) // duplicate, no-op

	if sink.count() != 1 {
		t.Fatalf("expected exactly 1 coalesced hit mail for the episode, got %d", sink.count())
	}

	// First agent clears — episode still open (second agent active), no mail.
	c.OnClear("cat-mg-aaaa", now.Add(time.Hour))
	if sink.count() != 1 {
		t.Fatalf("clearing one of two agents should not send a mail, got %d", sink.count())
	}

	// Second agent clears — episode closes, one clear mail listing both.
	c.OnClear("cat-mg-bbbb", now.Add(2*time.Hour))
	if sink.count() != 2 {
		t.Fatalf("expected clear mail after last agent clears, got %d", sink.count())
	}
	clear := sink.at(1)
	if !strings.Contains(clear.body, "cat-mg-aaaa") || !strings.Contains(clear.body, "cat-mg-bbbb") {
		t.Errorf("clear roster should list both agents, got:\n%s", clear.body)
	}
	if !strings.Contains(clear.subject, "2 agent") {
		t.Errorf("clear subject should report 2 agents, got %q", clear.subject)
	}
}

// A second, separate episode after the first fully cleared gets its own hit
// mail — the roster is reset between episodes.
func TestUsageLimitCoordinator_SecondEpisodeAfterClear(t *testing.T) {
	sink := &mailSink{}
	c := newUsageLimitCoordinator(sink.send, fixedNow())
	now := fixedNow()()

	c.OnHit("cat-a", "", now)
	c.OnClear("cat-a", now.Add(time.Hour)) // episode 1 closes (2 mails)
	c.OnHit("cat-b", "", now.Add(2*time.Hour))

	if sink.count() != 3 {
		t.Fatalf("expected hit+clear+hit = 3 mails, got %d", sink.count())
	}
	// The second episode's clear mail must NOT still carry agent a.
	c.OnClear("cat-b", now.Add(3*time.Hour))
	clear := sink.at(3)
	if strings.Contains(clear.body, "cat-a") {
		t.Errorf("episode 2 clear should not list episode 1's agent, got:\n%s", clear.body)
	}
	if !strings.Contains(clear.body, "cat-b") {
		t.Errorf("episode 2 clear should list cat-b, got:\n%s", clear.body)
	}
}

// Clearing an agent that was never hit is a no-op (no mail, no panic).
func TestUsageLimitCoordinator_ClearUnknownAgentNoop(t *testing.T) {
	sink := &mailSink{}
	c := newUsageLimitCoordinator(sink.send, fixedNow())
	c.OnClear("cat-ghost", fixedNow()())
	if sink.count() != 0 {
		t.Fatalf("clearing an unknown agent should send no mail, got %d", sink.count())
	}
}

func TestAgentNameFromID(t *testing.T) {
	cases := map[string]string{
		"cat-mg-7ffa": "mg-7ffa",
		"crew-arch":   "arch",
		"mayor":       "mayor",
		"human":       "human",
	}
	for in, want := range cases {
		if got := agentNameFromID(in); got != want {
			t.Errorf("agentNameFromID(%q) = %q, want %q", in, got, want)
		}
	}
}
