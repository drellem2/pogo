package scheduler

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestEmitsScheduleRemovedAtAllPaths exercises every delete site in the
// scheduler and asserts a schedule_removed event with the correct reason
// landed in events.log. Guards mg-8e5d: a silent purge that went undetected
// for ~11h because pogod emitted no removal signal.
func TestEmitsScheduleRemovedAtAllPaths(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		setup  func(t *testing.T) (s *Scheduler, scheduleID, agent string)
	}{
		{
			name:   "rollback_persist_failure",
			reason: "rollback_persist_failure",
			setup: func(t *testing.T) (*Scheduler, string, string) {
				s := newSchedulerForTest(t, nil)
				// Break the store so persistLocked fails inside Add. The path
				// has /dev/null as a non-directory parent, so MkdirAll errors.
				s.store.path = "/dev/null/nope/schedules.json"
				if _, err := s.Add(Entry{Agent: "crew-rollback", Cron: "*/5 * * * *", ID: "rollback-me"}, fixedTime()); err == nil {
					t.Fatal("expected Add to fail under broken store")
				}
				return s, "rollback-me", "crew-rollback"
			},
		},
		{
			name:   "explicit_rm",
			reason: "explicit_rm",
			setup: func(t *testing.T) (*Scheduler, string, string) {
				s := newSchedulerForTest(t, nil)
				if _, err := s.Add(Entry{Agent: "crew-rm", Cron: "*/5 * * * *", ID: "rm-me"}, fixedTime()); err != nil {
					t.Fatal(err)
				}
				ok, err := s.Remove("crew-rm", "rm-me")
				if err != nil || !ok {
					t.Fatalf("Remove: ok=%v err=%v", ok, err)
				}
				return s, "rm-me", "crew-rm"
			},
		},
		{
			name:   "explicit_rm_by_id",
			reason: "explicit_rm_by_id",
			setup: func(t *testing.T) (*Scheduler, string, string) {
				s := newSchedulerForTest(t, nil)
				if _, err := s.Add(Entry{Agent: "crew-rmid", Cron: "*/5 * * * *", ID: "rm-by-id"}, fixedTime()); err != nil {
					t.Fatal(err)
				}
				ok, err := s.RemoveByID("rm-by-id")
				if err != nil || !ok {
					t.Fatalf("RemoveByID: ok=%v err=%v", ok, err)
				}
				return s, "rm-by-id", "crew-rmid"
			},
		},
		{
			name:   "one_shot_complete",
			reason: "one_shot_complete",
			setup: func(t *testing.T) (*Scheduler, string, string) {
				s := newSchedulerForTest(t, nil)
				now := fixedTime()
				if _, err := s.Add(Entry{
					Agent: "cat-oneshot", OneShot: true,
					NextFire: now.Add(time.Minute), ID: "oneshot-me",
				}, now); err != nil {
					t.Fatal(err)
				}
				s.Tick(context.Background(), now.Add(2*time.Minute))
				return s, "oneshot-me", "cat-oneshot"
			},
		},
		{
			name:   "cron_unparseable",
			reason: "cron_unparseable",
			setup: func(t *testing.T) (*Scheduler, string, string) {
				s := newSchedulerForTest(t, nil)
				now := fixedTime()
				if _, err := s.Add(Entry{Agent: "crew-corrupt", Cron: "*/5 * * * *", ID: "corrupt-me"}, now); err != nil {
					t.Fatal(err)
				}
				// Simulate a hand-edited schedules.json that brought in a
				// malformed cron during reload: corrupt the live entry's
				// Cron and force it due.
				key := entryKey{Agent: "crew-corrupt", ID: "corrupt-me"}
				s.mu.Lock()
				s.entries[key].Cron = "not a cron"
				s.entries[key].NextFire = now.Add(-time.Second)
				s.mu.Unlock()
				s.Tick(context.Background(), now)
				return s, "corrupt-me", "crew-corrupt"
			},
		},
		{
			name:   "no_future_fire",
			reason: "no_future_fire",
			setup: func(t *testing.T) (*Scheduler, string, string) {
				s := newSchedulerForTest(t, nil)
				now := fixedTime()
				// "0 0 31 2 *" parses cleanly but Next() never finds a match
				// (Feb 31 doesn't exist). Add would reject it because c.Next
				// returns zero up front, so insert directly into the live map.
				key := entryKey{Agent: "crew-nofuture", ID: "no-future"}
				s.mu.Lock()
				s.entries[key] = &Entry{
					Agent:        "crew-nofuture",
					ID:           "no-future",
					Cron:         "0 0 31 2 *",
					NextFire:     now.Add(-time.Second),
					Delivery:     DeliveryNudge,
					ReplayPolicy: ReplayOnce,
				}
				s.mu.Unlock()
				s.Tick(context.Background(), now)
				return s, "no-future", "crew-nofuture"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each scheduler writes to its OWN root (s.logPath), not a
			// globally-resolved path — mg-e06d. Read the event back from there.
			s, scheduleID, agent := tc.setup(t)
			logPath := s.logPath

			ev := findScheduleRemoved(t, logPath, scheduleID, tc.reason)
			if ev == nil {
				t.Fatalf("no schedule_removed event with reason=%q for schedule_id=%q in %s",
					tc.reason, scheduleID, logPath)
			}
			if ev["event_type"] != "schedule_removed" {
				t.Errorf("event_type: want schedule_removed, got %v", ev["event_type"])
			}
			if ev["agent"] != "pogod" {
				t.Errorf("envelope agent: want pogod, got %v", ev["agent"])
			}
			d := ev["details"].(map[string]any)
			if d["to"] != agent {
				t.Errorf("details.to: want %q, got %v", agent, d["to"])
			}
			if d["reason"] != tc.reason {
				t.Errorf("details.reason: want %q, got %v", tc.reason, d["reason"])
			}
			if d["schedule_id"] != scheduleID {
				t.Errorf("details.schedule_id: want %q, got %v", scheduleID, d["schedule_id"])
			}
			if d["delivery"] != string(DeliveryNudge) {
				t.Errorf("details.delivery: want %q, got %v", DeliveryNudge, d["delivery"])
			}
			if _, ok := d["removed_at"].(string); !ok {
				t.Errorf("details.removed_at missing or wrong type: %v", d["removed_at"])
			}
		})
	}
}

// findScheduleRemoved scans path for a schedule_removed event whose details
// match scheduleID + reason. Returns nil if none is found.
func findScheduleRemoved(t *testing.T, path, scheduleID, reason string) map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open events.log: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["event_type"] != "schedule_removed" {
			continue
		}
		d, _ := m["details"].(map[string]any)
		if d == nil {
			continue
		}
		if d["schedule_id"] == scheduleID && d["reason"] == reason {
			return m
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan events.log: %v", err)
	}
	return nil
}
