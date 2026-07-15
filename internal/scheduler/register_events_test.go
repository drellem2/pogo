package scheduler

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestEventLogPath pins the own-root derivation: events.log is the sibling of
// the schedules.json state file, never a globally-resolved path (mg-e06d).
func TestEventLogPath(t *testing.T) {
	got := EventLogPath("/some/root/schedules.json")
	if want := filepath.Join("/some/root", "events.log"); got != want {
		t.Errorf("EventLogPath = %q, want %q", got, want)
	}
}

// TestEmitScheduleRegisterFailedTo verifies the emitter writes a
// schedule_register_failed event, with the agent/schedule-id/reason fields, to
// the exact own-root log it is handed — the path a scheduler-less pogod
// (scheduler.New failed) still uses to make the startup nil-registrar drop loud
// (mg-6fe0).
func TestEmitScheduleRegisterFailedTo(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")

	EmitScheduleRegisterFailedTo(logPath, "pc-77", MailCheckIDPrefix+"wi-77", "nil_registrar")

	ev := findScheduleRegisterFailed(t, logPath, MailCheckIDPrefix+"wi-77")
	if ev == nil {
		t.Fatalf("no schedule_register_failed event in %s", logPath)
	}
	d, _ := ev["details"].(map[string]any)
	if d == nil {
		t.Fatalf("event has no details: %+v", ev)
	}
	if d["agent"] != "pc-77" {
		t.Errorf("details.agent = %v, want pc-77", d["agent"])
	}
	if d["reason"] != "nil_registrar" {
		t.Errorf("details.reason = %v, want nil_registrar", d["reason"])
	}
	if ev["agent"] != "pogod" {
		t.Errorf("event agent = %v, want pogod", ev["agent"])
	}
}

func findScheduleRegisterFailed(t *testing.T, path, scheduleID string) map[string]any {
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
		if m["event_type"] != "schedule_register_failed" {
			continue
		}
		d, _ := m["details"].(map[string]any)
		if d != nil && d["schedule_id"] == scheduleID {
			return m
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan events.log: %v", err)
	}
	return nil
}
