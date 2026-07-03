package project

import (
	"testing"
	"time"
)

// interval returns the current backoff interval for path, or 0 if untracked.
func (s *reindexScheduler) intervalFor(path string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.state[path]; ok {
		return st.interval
	}
	return 0
}

func TestBackoffDoublesOnUnchangedAndResetsOnChange(t *testing.T) {
	base := time.Minute
	s := newReindexScheduler(base)
	now := time.Now()
	const p = "/repo/"

	want := []time.Duration{2 * base, 4 * base, 8 * base, 16 * base, 16 * base, 16 * base}
	for i, w := range want {
		s.reschedule(p, false, now)
		if got := s.intervalFor(p); got != w {
			t.Errorf("after %d unchanged passes: interval = %s, want %s", i+1, got, w)
		}
	}

	s.reschedule(p, true, now)
	if got := s.intervalFor(p); got != base {
		t.Errorf("after a changed pass: interval = %s, want base %s", got, base)
	}
}

func TestDueGating(t *testing.T) {
	base := time.Minute
	s := newReindexScheduler(base)
	now := time.Now()
	const p = "/repo/"

	if !s.due(p, now) {
		t.Errorf("a never-seen project must be due immediately")
	}
	s.markFired(p, now)
	if s.due(p, now) {
		t.Errorf("a just-fired project must not be due again within its interval")
	}
	if s.due(p, now.Add(base-time.Second)) {
		t.Errorf("project became due before its interval elapsed")
	}
	if !s.due(p, now.Add(base)) {
		t.Errorf("project must be due once its interval has elapsed")
	}
}

func TestMarkActivityResetsBackoff(t *testing.T) {
	base := time.Minute
	s := newReindexScheduler(base)
	now := time.Now()
	const p = "/repo/"

	// Back the project off, then record activity.
	s.reschedule(p, false, now)
	s.reschedule(p, false, now)
	if s.due(p, now) {
		t.Fatalf("backed-off project should not be due")
	}
	s.markActivity(p, now)
	if !s.due(p, now) {
		t.Errorf("a visited project must be due at the next tick")
	}
	if got := s.intervalFor(p); got != base {
		t.Errorf("a visit must reset the interval to base: got %s, want %s", got, base)
	}
}

func TestPruneDropsUnregisteredProjects(t *testing.T) {
	s := newReindexScheduler(time.Minute)
	now := time.Now()
	s.markFired("/keep/", now)
	s.markFired("/gone/", now)

	s.prune(map[string]bool{"/keep/": true})

	s.mu.Lock()
	_, kept := s.state["/keep/"]
	_, gone := s.state["/gone/"]
	s.mu.Unlock()
	if !kept {
		t.Errorf("prune removed a still-registered project")
	}
	if gone {
		t.Errorf("prune kept an unregistered project")
	}
}
