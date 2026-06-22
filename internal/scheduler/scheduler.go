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

// CronInterval returns the spacing between consecutive firings of the entry's
// cron expression, sampled just after ref. It is zero for one-shot entries, an
// empty cron, or a cron that fails to parse or has no two future firings.
//
// Stall detection uses this to tell a cron-driven agent's by-design idle (the
// gap between firings) from a genuine wedge: an agent within one CronInterval
// of its last scheduled firing is idle on purpose, not stalled (mg-5b23).
func (e Entry) CronInterval(ref time.Time) time.Duration {
	if e.OneShot || strings.TrimSpace(e.Cron) == "" {
		return 0
	}
	c, err := ParseCron(e.Cron)
	if err != nil {
		return 0
	}
	n1 := c.Next(ref)
	if n1.IsZero() {
		return 0
	}
	n2 := c.Next(n1)
	if n2.IsZero() {
		return 0
	}
	return n2.Sub(n1)
}

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

// MailCheckIDPrefix is the schedule-id prefix every per-agent mail-check loop
// uses (polecats and crew agents register `mail-check-<agent>` so the mayor can
// reach them mid-task). Such a schedule is only meaningful while its target
// agent's process is alive; once the agent is gone the schedule is dead weight
// that fires every interval into a scheduler_fire_failed event. The GC sweep
// below reaps them. See gh drellem2/macguffin #15.
const MailCheckIDPrefix = "mail-check-"

// AgentLiveness reports whether the agent addressed by a schedule still has a
// live process. The scheduler consults it to garbage-collect mail-check-*
// schedules whose target agent has vanished (gh drellem2/macguffin #15). pogod
// backs this with its agent registry; an agent it doesn't know about — e.g.
// after a pogod restart killed every child — counts as "not alive".
type AgentLiveness interface {
	IsAlive(scheduleAgent string) bool
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

// entryKey is the composite (agent, id) key for the live entries map.
// Schedules are scoped per-agent — two agents may register the same id
// without collision. See ErrAmbiguousID for the disambiguation contract
// when callers only know the id.
type entryKey struct {
	Agent string
	ID    string
}

// ErrAmbiguousID is returned by id-only lookups (HTTP, CLI) when the same id
// is registered for more than one agent. Callers must disambiguate by passing
// an agent. The error message lists the agents that own a matching entry so
// operators can pick one.
type ErrAmbiguousID struct {
	ID     string
	Agents []string
}

func (e *ErrAmbiguousID) Error() string {
	return fmt.Sprintf("scheduler: id %q is registered for multiple agents (%s); pass --agent to disambiguate",
		e.ID, strings.Join(e.Agents, ", "))
}

// Scheduler owns the live set of scheduled entries and persists them to
// ~/.pogo/schedules.json. Safe for concurrent use.
type Scheduler struct {
	store     *store
	deliverer Deliverer

	// liveness, when set, lets Tick garbage-collect mail-check-* schedules
	// whose target agent has disappeared. nil disables GC (most unit tests).
	liveness AgentLiveness

	// SkipWindow is how recent a fire must be (relative to "now") to fire
	// under ReplaySkip. Defaults to 2 × tick interval — wide enough to cover
	// normal scheduling jitter, tight enough to drop fires from a long sleep.
	// Configurable so tests can pin it.
	SkipWindow time.Duration

	mu      sync.Mutex
	entries map[entryKey]*Entry
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
		entries:   make(map[entryKey]*Entry, len(loaded)),
	}
	for _, e := range loaded {
		entry := e
		entry.applyDefaults()
		s.entries[entryKey{Agent: entry.Agent, ID: entry.ID}] = &entry
	}
	return s, nil
}

// SetLiveness installs the agent-liveness checker used to garbage-collect stale
// mail-check schedules. Call once at startup before the heartbeat drives Tick.
func (s *Scheduler) SetLiveness(l AgentLiveness) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveness = l
}

// Add inserts (or replaces) an entry, persists, and returns the canonical
// stored copy. If entry.ID is empty a slug is generated. If entry.NextFire is
// zero for a recurring entry, it is computed from the cron expression relative
// to now.
//
// Replacement is keyed on (agent, id), not id alone — two agents may register
// the same id without colliding (e.g. multiple PMs each registering
// "mail-check"). Re-adding with the same (agent, id) is idempotent.
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
	key := entryKey{Agent: entry.Agent, ID: entry.ID}
	prev, hadPrev := s.entries[key]
	stored := entry
	s.entries[key] = &stored
	if err := s.persistLocked(); err != nil {
		if hadPrev {
			s.entries[key] = prev
		} else {
			emitSchedulerRemovalEvent("rollback_persist_failure", stored, now, err)
			delete(s.entries, key)
		}
		return Entry{}, err
	}
	return stored.Clone(), nil
}

// Remove deletes the entry uniquely identified by (agent, id). Returns false
// if no matching entry exists. To remove by id alone (e.g. from a CLI that
// doesn't know the agent), use RemoveByID.
func (s *Scheduler) Remove(agent, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := entryKey{Agent: agent, ID: id}
	saved, ok := s.entries[key]
	if !ok {
		return false, nil
	}
	delete(s.entries, key)
	if err := s.persistLocked(); err != nil {
		s.entries[key] = saved
		return false, err
	}
	emitSchedulerRemovalEvent("explicit_rm", *saved, time.Now(), nil)
	return true, nil
}

// RemoveByID deletes the entry with the given id when it is unambiguous (i.e.
// only one agent owns an entry with that id). Returns false if no entry
// matches; returns *ErrAmbiguousID if more than one agent owns the id —
// callers must then call Remove(agent, id) with a specific agent.
func (s *Scheduler) RemoveByID(id string) (bool, error) {
	s.mu.Lock()
	matches := s.findByIDLocked(id)
	if len(matches) == 0 {
		s.mu.Unlock()
		return false, nil
	}
	if len(matches) > 1 {
		agents := make([]string, 0, len(matches))
		for _, e := range matches {
			agents = append(agents, e.Agent)
		}
		sort.Strings(agents)
		s.mu.Unlock()
		return false, &ErrAmbiguousID{ID: id, Agents: agents}
	}
	key := entryKey{Agent: matches[0].Agent, ID: matches[0].ID}
	saved := s.entries[key]
	delete(s.entries, key)
	if err := s.persistLocked(); err != nil {
		s.entries[key] = saved
		s.mu.Unlock()
		return false, err
	}
	s.mu.Unlock()
	emitSchedulerRemovalEvent("explicit_rm_by_id", *saved, time.Now(), nil)
	return true, nil
}

// Get returns a copy of the entry uniquely identified by (agent, id), or
// zero + false. Use GetByID when only the id is known.
func (s *Scheduler) Get(agent, id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[entryKey{Agent: agent, ID: id}]
	if !ok {
		return Entry{}, false
	}
	return e.Clone(), true
}

// GetByID returns the entry with the given id when unambiguous. Returns
// zero + false if no entry matches; *ErrAmbiguousID if multiple agents own
// the id.
func (s *Scheduler) GetByID(id string) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	matches := s.findByIDLocked(id)
	if len(matches) == 0 {
		return Entry{}, false, nil
	}
	if len(matches) > 1 {
		agents := make([]string, 0, len(matches))
		for _, e := range matches {
			agents = append(agents, e.Agent)
		}
		sort.Strings(agents)
		return Entry{}, false, &ErrAmbiguousID{ID: id, Agents: agents}
	}
	return matches[0].Clone(), true, nil
}

// findByIDLocked returns clones of every entry whose ID matches. Caller must
// hold s.mu.
func (s *Scheduler) findByIDLocked(id string) []Entry {
	var out []Entry
	for k, e := range s.entries {
		if k.ID == id {
			out = append(out, e.Clone())
		}
	}
	return out
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
	// Reap mail-check schedules for vanished agents before computing what's due,
	// so a stale entry is removed instead of firing into a scheduler_fire_failed
	// event. This is the backstop for the exit path no in-process hook can see —
	// pogod restarting kills its children without firing onExit (gh #15).
	s.GCStaleMailChecks(now)

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
	for _, key := range due {
		s.mu.Lock()
		entry, ok := s.entries[key]
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
		entry, ok = s.entries[key]
		if !ok {
			// Deleted concurrently — leave it gone.
			s.mu.Unlock()
			results = append(results, res)
			continue
		}
		if fire.OneShot {
			emitSchedulerRemovalEvent("one_shot_complete", *entry, now, nil)
			delete(s.entries, key)
			changed = true
		} else {
			c, err := ParseCron(entry.Cron)
			if err != nil {
				log.Printf("scheduler: cron %q now unparseable, removing entry %s/%s: %v", entry.Cron, key.Agent, key.ID, err)
				emitSchedulerRemovalEvent("cron_unparseable", *entry, now, err)
				delete(s.entries, key)
				changed = true
			} else {
				entry.LastFire = now
				if fire.ReplayPolicy == ReplayCount {
					entry.MissedFires += missed
				}
				entry.NextFire = c.Next(now)
				if entry.NextFire.IsZero() {
					log.Printf("scheduler: cron %q has no future fire, removing entry %s/%s", entry.Cron, key.Agent, key.ID)
					emitSchedulerRemovalEvent("no_future_fire", *entry, now, nil)
					delete(s.entries, key)
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

// dueLocked returns keys of entries whose NextFire is at or before now,
// ordered by NextFire ascending. Caller must hold s.mu.
func (s *Scheduler) dueLocked(now time.Time) []entryKey {
	type pair struct {
		key  entryKey
		when time.Time
	}
	var pairs []pair
	for k, e := range s.entries {
		if !e.NextFire.IsZero() && (e.NextFire.Before(now) || e.NextFire.Equal(now)) {
			pairs = append(pairs, pair{key: k, when: e.NextFire})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].when.Before(pairs[j].when) })
	out := make([]entryKey, len(pairs))
	for i, p := range pairs {
		out[i] = p.key
	}
	return out
}

// GCStaleMailChecks sweeps every mail-check-* schedule whose target agent is no
// longer alive (per the configured AgentLiveness) and removes it, emitting a
// schedule_removed event (reason "agent_gone") for each. Returns the number
// reaped. Called from Tick on every heartbeat, so a vanished agent's schedule
// is collected within one tick — well inside its next fire interval, which is
// what keeps scheduler_fire_failed events from accumulating. No-op when no
// liveness checker is installed. See gh drellem2/macguffin #15.
func (s *Scheduler) GCStaleMailChecks(now time.Time) int {
	s.mu.Lock()
	live := s.liveness
	s.mu.Unlock()
	if live == nil {
		return 0
	}
	return s.reapMailChecks(now, func(agent string) bool { return !live.IsAlive(agent) })
}

// RemoveMailChecksForAgent eagerly reaps mail-check-* schedules addressed to any
// of the given agent-identity aliases (a bare name and/or its cat-/crew- event
// identity). pogod calls this from its onExit hook so a stopped or crashed
// agent's mail-check loop is cleaned up immediately rather than waiting for the
// next Tick sweep. Returns the number reaped.
func (s *Scheduler) RemoveMailChecksForAgent(now time.Time, aliases ...string) int {
	set := make(map[string]struct{}, len(aliases))
	for _, a := range aliases {
		if a != "" {
			set[a] = struct{}{}
		}
	}
	if len(set) == 0 {
		return 0
	}
	return s.reapMailChecks(now, func(agent string) bool {
		_, gone := set[agent]
		return gone
	})
}

// reapMailChecks removes every mail-check-* entry for which gone(entry.Agent)
// reports true, persists, and emits a schedule_removed event (reason
// "agent_gone") per removal. On a persist failure it rolls the deletions back
// (keeping memory and disk consistent, matching Add/Remove) and returns 0 — the
// next sweep retries. Caller must NOT hold s.mu.
func (s *Scheduler) reapMailChecks(now time.Time, gone func(agent string) bool) int {
	s.mu.Lock()
	var staleKeys []entryKey
	for k, e := range s.entries {
		if strings.HasPrefix(e.ID, MailCheckIDPrefix) && gone(e.Agent) {
			staleKeys = append(staleKeys, k)
		}
	}
	if len(staleKeys) == 0 {
		s.mu.Unlock()
		return 0
	}
	saved := make([]*Entry, len(staleKeys))
	for i, k := range staleKeys {
		saved[i] = s.entries[k]
		delete(s.entries, k)
	}
	if err := s.persistLocked(); err != nil {
		for i, k := range staleKeys {
			s.entries[k] = saved[i]
		}
		s.mu.Unlock()
		log.Printf("scheduler: mail-check GC of %d entr(ies) failed to persist, rolled back: %v", len(staleKeys), err)
		return 0
	}
	removed := make([]Entry, len(saved))
	for i, e := range saved {
		removed[i] = e.Clone()
	}
	s.mu.Unlock()

	for _, e := range removed {
		emitSchedulerRemovalEvent("agent_gone", e, now, nil)
	}
	return len(removed)
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

// emitSchedulerRemovalEvent writes a schedule_removed event tagged with the
// reason an entry left the live set. Emitted at every delete site so an
// operator can answer "why did this schedule disappear?" from events.log alone
// — see mg-8e5d for the silent-purge incident this guards against.
func emitSchedulerRemovalEvent(reason string, e Entry, removedAt time.Time, err error) {
	details := map[string]any{
		"schedule_id":   e.ID,
		"to":            e.Agent,
		"delivery":      string(e.Delivery),
		"removed_at":    removedAt.Format(time.RFC3339),
		"replay_policy": string(e.ReplayPolicy),
		"one_shot":      e.OneShot,
		"reason":        reason,
	}
	if e.Cron != "" {
		details["cron"] = e.Cron
	}
	if !e.NextFire.IsZero() {
		details["next_fire"] = e.NextFire.Format(time.RFC3339)
	}
	if err != nil {
		details["error"] = err.Error()
	}
	events.Emit(context.Background(), events.Event{
		EventType: "schedule_removed",
		Agent:     "pogod",
		Details:   details,
	})
}
