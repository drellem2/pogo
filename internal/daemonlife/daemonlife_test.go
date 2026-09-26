package daemonlife

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestBootThenShutdownRoundTrip(t *testing.T) {
	path := Path(t.TempDir())
	t0 := time.Date(2026, 9, 8, 17, 0, 0, 0, time.UTC)

	prev, rerr, werr := Boot(path, Record{PID: 100, StartedAt: t0, Revision: "abc"})
	if prev != nil || rerr != nil || werr != nil {
		t.Fatalf("first boot = %v, %v, %v; want nil, nil, nil", prev, rerr, werr)
	}
	if got := Classify(prev, false); got != StateUnknown {
		t.Errorf("Classify(nil) = %s, want unknown", got)
	}
	sd := Shutdown{At: t0.Add(time.Hour), Cause: CauseSignal, Signal: "SIGTERM"}
	if err := RecordShutdown(path, 100, sd); err != nil {
		t.Fatal(err)
	}

	prev, _, _ = Boot(path, Record{PID: 200, StartedAt: t0.Add(2 * time.Hour)})
	if prev == nil || prev.PID != 100 || prev.Shutdown == nil || prev.Shutdown.Signal != "SIGTERM" {
		t.Fatalf("second boot prev = %+v, want pid 100 with its SIGTERM", prev)
	}
	if got := Classify(prev, false); got != StateClean {
		t.Errorf("Classify = %s, want clean", got)
	}

	// Run 200 dies unrecorded; the next boot must see it as unclean.
	prev, _, _ = Boot(path, Record{PID: 300})
	if prev.PID != 200 || prev.Shutdown != nil {
		t.Fatalf("third boot prev = %+v, want pid 200 with no shutdown", prev)
	}
	if got := Classify(prev, false); got != StateUnclean {
		t.Errorf("Classify = %s, want unclean", got)
	}
	if got := Classify(prev, true); got != StateAlive {
		t.Errorf("Classify(alive) = %s, want alive", got)
	}
}

// A daemon whose record was booted over (lock lost) must not stamp its
// shutdown onto the other daemon's run — that would make a live daemon read as
// cleanly shut down to the next boot.
func TestRecordShutdownRefusesAnotherPidsRecord(t *testing.T) {
	path := Path(t.TempDir())
	if _, _, err := Boot(path, Record{PID: 2}); err != nil {
		t.Fatal(err)
	}
	err := RecordShutdown(path, 1, Shutdown{Cause: CauseFatal})
	if !errors.Is(err, ErrNotOurs) {
		t.Fatalf("RecordShutdown by pid 1 over pid 2's record: err = %v, want ErrNotOurs", err)
	}
	r, _ := Read(path)
	if r.Shutdown != nil {
		t.Errorf("record was stamped anyway: %+v", r.Shutdown)
	}
}

func TestCorruptRecordIsReportedNotMistakenForFirstBoot(t *testing.T) {
	path := Path(t.TempDir())
	if err := os.WriteFile(path, []byte("{half"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev, rerr, werr := Boot(path, Record{PID: 5})
	if prev != nil || rerr == nil || werr != nil {
		t.Fatalf("Boot over corrupt record = %v, %v, %v; want nil, read error, nil", prev, rerr, werr)
	}
	ev := BootEvent(Record{PID: 5, StartedAt: time.Now()}, prev, Classify(prev, false), time.Time{}, rerr)
	p := ev.Details["previous"].(map[string]any)
	if p["read_error"] == nil {
		t.Errorf("boot event hides the read error: %v", p)
	}
	if r, _ := Read(path); r == nil || r.PID != 5 {
		t.Errorf("this run was not recorded over the corrupt file: %+v", r)
	}
	if m, _ := filepath.Glob(path + ".tmp.*"); len(m) != 0 {
		t.Errorf("temp files left behind: %v", m)
	}
}

func TestBootEventNamesUncleanDeath(t *testing.T) {
	started := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	beat := time.Date(2026, 9, 8, 18, 55, 0, 0, time.UTC)
	prev := &Record{PID: 42, StartedAt: started, Revision: "r1"}
	ev := BootEvent(Record{PID: 43, StartedAt: beat.Add(18 * 24 * time.Hour), Revision: "r2"},
		prev, StateUnclean, beat, nil)
	if ev.EventType != EventBoot || ev.Agent != "pogod" {
		t.Fatalf("envelope = %s/%s", ev.EventType, ev.Agent)
	}
	p := ev.Details["previous"].(map[string]any)
	if p["state"] != StateUnclean || p["pid"] != 42 || p["revision"] != "r1" ||
		p["last_heartbeat"] != "2026-09-08T18:55:00Z" {
		t.Errorf("previous = %v", p)
	}
	if _, ok := p["exit"]; ok {
		t.Errorf("an unclean death invented an exit: %v", p)
	}
}

func TestShutdownEventCarriesUptime(t *testing.T) {
	start := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	ev := ShutdownEvent(Record{PID: 9, StartedAt: start},
		Shutdown{At: start.Add(90 * time.Second), Cause: CauseFatal, Error: "boom"})
	d := ev.Details
	if d["cause"] != CauseFatal || d["error"] != "boom" || d["uptime_seconds"] != int64(90) || d["pid"] != 9 {
		t.Errorf("details = %v", d)
	}
	if ev.Timestamp != "2026-09-08T10:01:30Z" {
		t.Errorf("timestamp = %s, want the shutdown time", ev.Timestamp)
	}
}

func TestSignalName(t *testing.T) {
	for sig, want := range map[os.Signal]string{
		syscall.SIGTERM: "SIGTERM", syscall.SIGINT: "SIGINT", syscall.SIGHUP: "SIGHUP", syscall.SIGQUIT: "SIGQUIT",
	} {
		if got := SignalName(sig); got != want {
			t.Errorf("SignalName(%v) = %q, want %q", sig, got, want)
		}
	}
}

func TestPidAlive(t *testing.T) {
	if !PidAlive(os.Getpid()) {
		t.Error("own pid reported dead")
	}
	if PidAlive(0) || PidAlive(-1) {
		t.Error("non-positive pid reported alive")
	}
}
