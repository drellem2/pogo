package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// State schema version for ~/.pogo/schedules.json. Bump only on breaking field
// changes; additive fields stay at v1 and rely on Go's zero values for
// migration.
const StateVersion = 1

// ReplayPolicy controls what happens when the scheduler ticks and discovers
// that one or more fire times have passed since the previous tick (typical
// after host sleep, NTP step, or pogod restart).
//
//   - ReplayOnce  (default): fire exactly once, no matter how many fire
//     points were missed. The agent gets one nudge with a `missed_fires`
//     count so the prompt can decide whether to catch up or skip ahead.
//   - ReplayCount: fire once and record the missed count; same delivery as
//     ReplayOnce but conceptually distinct so a future implementation could
//     fan out N nudges. Today both behave the same — the slot is reserved.
//   - ReplaySkip:  do not fire at all if the original fire time is older
//     than one tick interval; just reschedule. Use for "catch the next one,
//     don't care about missed ones" semantics (e.g. health pollers).
type ReplayPolicy string

const (
	ReplayOnce  ReplayPolicy = "once"
	ReplayCount ReplayPolicy = "count"
	ReplaySkip  ReplayPolicy = "skip"
)

// DeliveryMode controls how a fire is delivered to the agent.
type DeliveryMode string

const (
	DeliveryNudge DeliveryMode = "nudge"
	DeliveryMail  DeliveryMode = "mail"
)

// Entry is a single scheduled fire. Persisted as a JSON object inside
// ~/.pogo/schedules.json; field names are snake_case to match the rest of the
// pogo on-disk format.
//
// On-disk schema (one element of the `schedules` array):
//
//	{
//	  "id":            "research-poll",        // unique slug, agent-scoped
//	  "agent":         "crew-research",        // recipient
//	  "cron":          "*/15 * * * *",         // empty for one-shot
//	  "one_shot":      false,                  // true → deleted after firing
//	  "next_fire":     "2026-05-03T13:30:00Z", // absolute wall-clock time
//	  "replay_policy": "once",                 // see ReplayPolicy
//	  "delivery":      "nudge",                // "nudge" | "mail"
//	  "message":       "...",                  // optional body delivered on fire
//	  "created_at":    "2026-05-03T08:32:10Z",
//	  "last_fire":     "2026-05-03T13:15:00Z", // zero if never fired
//	  "missed_fires":  0                       // accumulated missed count for "count" policy
//	}
type Entry struct {
	ID           string       `json:"id"`
	Agent        string       `json:"agent"`
	Cron         string       `json:"cron,omitempty"`
	OneShot      bool         `json:"one_shot,omitempty"`
	NextFire     time.Time    `json:"next_fire"`
	ReplayPolicy ReplayPolicy `json:"replay_policy,omitempty"`
	Delivery     DeliveryMode `json:"delivery"`
	Message      string       `json:"message,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	LastFire     time.Time    `json:"last_fire,omitempty"`
	MissedFires  int          `json:"missed_fires,omitempty"`
}

// Clone returns a shallow copy. Used to hand entries out of the Scheduler
// without exposing internal state to mutation.
func (e Entry) Clone() Entry { return e }

// Validate returns nil if the entry is internally consistent and ready to
// schedule, or a descriptive error otherwise.
func (e *Entry) Validate() error {
	if strings.TrimSpace(e.Agent) == "" {
		return errors.New("scheduler: agent is required")
	}
	if e.OneShot {
		if e.Cron != "" {
			return errors.New("scheduler: one_shot entries must not set cron")
		}
		if e.NextFire.IsZero() {
			return errors.New("scheduler: one_shot entries must set next_fire")
		}
	} else {
		if strings.TrimSpace(e.Cron) == "" {
			return errors.New("scheduler: recurring entries require a cron expression")
		}
		if _, err := ParseCron(e.Cron); err != nil {
			return err
		}
	}
	switch e.Delivery {
	case "", DeliveryNudge, DeliveryMail:
	default:
		return fmt.Errorf("scheduler: unknown delivery %q (want nudge|mail)", e.Delivery)
	}
	switch e.ReplayPolicy {
	case "", ReplayOnce, ReplayCount, ReplaySkip:
	default:
		return fmt.Errorf("scheduler: unknown replay_policy %q (want once|count|skip)", e.ReplayPolicy)
	}
	return nil
}

func (e *Entry) applyDefaults() {
	if e.Delivery == "" {
		e.Delivery = DeliveryNudge
	}
	if e.ReplayPolicy == "" {
		e.ReplayPolicy = ReplayOnce
	}
}

// Deliverer abstracts the side of the scheduler that talks to the rest of
// pogod. Production wires this to a NudgeOrMail-style helper; tests substitute
// a recorder.
type Deliverer interface {
	Deliver(ctx context.Context, entry Entry, fireTime time.Time) error
}

// DelivererFunc adapts an ordinary function to the Deliverer interface so the
// pogod main loop can pass a closure without a wrapper struct.
type DelivererFunc func(ctx context.Context, entry Entry, fireTime time.Time) error

// Deliver satisfies the Deliverer interface.
func (f DelivererFunc) Deliver(ctx context.Context, entry Entry, fireTime time.Time) error {
	return f(ctx, entry, fireTime)
}

// FireResult records what happened for a single entry on a single Tick.
// Returned to the caller (mostly for tests + observability).
type FireResult struct {
	Entry       Entry
	OriginalDue time.Time // the next_fire we tripped on
	FiredAt     time.Time // wall-clock now passed to Tick
	Missed      int       // count of additional periods between OriginalDue and FiredAt
	Delivered   bool      // false if Deliverer returned an error or Skip policy short-circuited
	DeliverErr  error     // set when delivery failed
	Skipped     bool      // true when ReplaySkip elided the fire
}

// Scheduler owns the live set of scheduled entries and persists them to
// ~/.pogo/schedules.json. Safe for concurrent use.
type Scheduler struct {
	store     *store
	deliverer Deliverer

	// SkipWindow is how recent a fire must be (relative to "now") to fire
	// under ReplaySkip. Defaults to 2 × tick interval — wide enough to cover
	// normal scheduling jitter, tight enough to drop fires from a long sleep.
	// Configurable so tests can pin it.
	SkipWindow time.Duration

	mu      sync.Mutex
	entries map[string]*Entry
}

// New loads the scheduler state from path, creating an empty store if the file
// does not yet exist. deliverer may be nil (tests can leave it unset and check
// FireResult.Skipped/Delivered directly via TickResults).
func New(path string, deliverer Deliverer) (*Scheduler, error) {
	st := &store{path: path}
	st.applyDefaults()
	loaded, err := st.load()
	if err != nil {
		return nil, err
	}
	s := &Scheduler{
		store:     st,
		deliverer: deliverer,
		entries:   make(map[string]*Entry, len(loaded)),
	}
	for _, e := range loaded {
		entry := e
		entry.applyDefaults()
		s.entries[entry.ID] = &entry
	}
	return s, nil
}

// Add inserts (or replaces) an entry, persists, and returns the canonical
// stored copy. If entry.ID is empty a slug is generated. If entry.NextFire is
// zero for a recurring entry, it is computed from the cron expression relative
// to now.
func (s *Scheduler) Add(entry Entry, now time.Time) (Entry, error) {
	entry.applyDefaults()
	if entry.ID == "" {
		entry.ID = generateID()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	if !entry.OneShot && entry.NextFire.IsZero() {
		c, err := ParseCron(entry.Cron)
		if err != nil {
			return Entry{}, err
		}
		entry.NextFire = c.Next(now)
		if entry.NextFire.IsZero() {
			return Entry{}, fmt.Errorf("scheduler: cron %q has no next fire within bounds", entry.Cron)
		}
	}
	if err := entry.Validate(); err != nil {
		return Entry{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	stored := entry
	s.entries[entry.ID] = &stored
	if err := s.persistLocked(); err != nil {
		delete(s.entries, entry.ID)
		return Entry{}, err
	}
	return stored.Clone(), nil
}

// Remove deletes the entry by id. Returns false if no such id exists.
func (s *Scheduler) Remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; !ok {
		return false, nil
	}
	saved := s.entries[id]
	delete(s.entries, id)
	if err := s.persistLocked(); err != nil {
		s.entries[id] = saved
		return false, err
	}
	return true, nil
}

// Get returns a copy of the entry by id, or zero + false.
func (s *Scheduler) Get(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return Entry{}, false
	}
	return e.Clone(), true
}

// List returns all entries (optionally filtered by agent), sorted by next_fire
// ascending so the output matches the natural "what's coming up" mental model.
func (s *Scheduler) List(agent string) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if agent != "" && e.Agent != agent {
			continue
		}
		out = append(out, e.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextFire.Before(out[j].NextFire) })
	return out
}

// Tick fires every entry whose NextFire is ≤ now and reschedules (or removes,
// for one-shots). Returns a FireResult per fired entry — useful to tests and
// to log what a tick did. Failures from Deliverer do not stop subsequent fires
// or the reschedule (we still want to advance NextFire so a flaky deliverer
// doesn't pin the same fire forever).
func (s *Scheduler) Tick(ctx context.Context, now time.Time) []FireResult {
	s.mu.Lock()
	due := s.dueLocked(now)
	s.mu.Unlock()

	if len(due) == 0 {
		return nil
	}

	skipWindow := s.SkipWindow
	if skipWindow <= 0 {
		skipWindow = 2 * time.Minute
	}

	results := make([]FireResult, 0, len(due))
	var changed bool
	for _, id := range due {
		s.mu.Lock()
		entry, ok := s.entries[id]
		if !ok {
			s.mu.Unlock()
			continue
		}
		fire := entry.Clone()
		s.mu.Unlock()

		// Compute how many additional periods we missed (for the count
		// policy and for inclusion in the delivered payload).
		missed := 0
		if fire.Cron != "" {
			c, err := ParseCron(fire.Cron)
			if err == nil {
				cursor := fire.NextFire
				for {
					n := c.Next(cursor)
					if n.IsZero() || !n.Before(now) && !n.Equal(now) {
						break
					}
					missed++
					cursor = n
				}
			}
		}

		res := FireResult{
			Entry:       fire,
			OriginalDue: fire.NextFire,
			FiredAt:     now,
			Missed:      missed,
		}

		// Apply replay policy.
		shouldFire := true
		if fire.ReplayPolicy == ReplaySkip {
			if now.Sub(fire.NextFire) > skipWindow {
				shouldFire = false
				res.Skipped = true
				emitSchedulerEvent("scheduler_fire_skipped", fire, now, missed, nil)
			}
		}

		if shouldFire {
			var derr error
			if s.deliverer != nil {
				derr = s.deliverer.Deliver(ctx, fire, now)
			}
			res.DeliverErr = derr
			res.Delivered = derr == nil
			if derr != nil {
				log.Printf("scheduler: deliver %s to %s failed: %v", fire.ID, fire.Agent, derr)
				emitSchedulerEvent("scheduler_fire_failed", fire, now, missed, derr)
			} else {
				emitSchedulerEvent("scheduler_fire_delivered", fire, now, missed, nil)
			}
		}

		// Update or remove the entry.
		s.mu.Lock()
		entry, ok = s.entries[id]
		if !ok {
			// Deleted concurrently — leave it gone.
			s.mu.Unlock()
			results = append(results, res)
			continue
		}
		if fire.OneShot {
			delete(s.entries, id)
			changed = true
		} else {
			c, err := ParseCron(entry.Cron)
			if err != nil {
				log.Printf("scheduler: cron %q now unparseable, removing entry %s: %v", entry.Cron, id, err)
				delete(s.entries, id)
				changed = true
			} else {
				entry.LastFire = now
				if fire.ReplayPolicy == ReplayCount {
					entry.MissedFires += missed
				}
				entry.NextFire = c.Next(now)
				if entry.NextFire.IsZero() {
					log.Printf("scheduler: cron %q has no future fire, removing entry %s", entry.Cron, id)
					delete(s.entries, id)
				}
				changed = true
			}
		}
		s.mu.Unlock()
		results = append(results, res)
	}

	if changed {
		s.mu.Lock()
		_ = s.persistLocked()
		s.mu.Unlock()
	}
	return results
}

// dueLocked returns IDs of entries whose NextFire is at or before now, ordered
// by NextFire ascending. Caller must hold s.mu.
func (s *Scheduler) dueLocked(now time.Time) []string {
	type pair struct {
		id   string
		when time.Time
	}
	var pairs []pair
	for id, e := range s.entries {
		if !e.NextFire.IsZero() && (e.NextFire.Before(now) || e.NextFire.Equal(now)) {
			pairs = append(pairs, pair{id: id, when: e.NextFire})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].when.Before(pairs[j].when) })
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.id
	}
	return out
}

func (s *Scheduler) persistLocked() error {
	entries := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e.Clone())
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Agent != entries[j].Agent {
			return entries[i].Agent < entries[j].Agent
		}
		return entries[i].ID < entries[j].ID
	})
	return s.store.save(entries)
}

// generateID produces an 8-char hex slug. We don't need cryptographic strength
// — just collision avoidance within a single ~/.pogo/schedules.json file.
func generateID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; fall back to nano-time so we still produce something.
		t := time.Now().UnixNano()
		return fmt.Sprintf("sched-%x", t)
	}
	return "sch-" + hex.EncodeToString(b[:])
}

// emitSchedulerEvent writes a structured event for fire delivery / skip /
// failure. Best-effort; events.Emit never blocks the caller.
func emitSchedulerEvent(eventType string, e Entry, fireTime time.Time, missed int, err error) {
	details := map[string]any{
		"schedule_id":   e.ID,
		"to":            e.Agent,
		"delivery":      string(e.Delivery),
		"original_due":  e.NextFire.Format(time.RFC3339),
		"fired_at":      fireTime.Format(time.RFC3339),
		"missed_fires":  missed,
		"replay_policy": string(e.ReplayPolicy),
		"one_shot":      e.OneShot,
	}
	if e.Cron != "" {
		details["cron"] = e.Cron
	}
	if err != nil {
		details["error"] = err.Error()
	}
	events.Emit(context.Background(), events.Event{
		EventType: eventType,
		Agent:     "pogod",
		Details:   details,
	})
}
