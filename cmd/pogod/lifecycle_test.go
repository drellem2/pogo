package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/daemonlife"
)

// pogod's own lifecycle on the event spine, end to end through a real binary
// (mg-32f5). The 18-day outage began with a pogod death that left NOTHING in
// events.log; these tests pin that every observable exit now leaves a
// pogod_shutdown, and that an unobservable one (SIGKILL) is named by the next
// pogod_boot instead of being an absence.

type lifeSandbox struct {
	root, state, ws, logPath string
}

func newLifeSandbox(t *testing.T) lifeSandbox {
	t.Helper()
	sb := t.TempDir()
	s := lifeSandbox{root: sb, state: filepath.Join(sb, "state"), ws: filepath.Join(sb, "ws"),
		logPath: filepath.Join(sb, "pogod.log")}
	for _, d := range []string{s.state, s.ws, filepath.Join(sb, ".config")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// start boots bin in the sandbox on port and waits until it is serving (or,
// when wantServe is false, returns right after Start).
func (s lifeSandbox) start(t *testing.T, bin string, port int, wantServe bool) *exec.Cmd {
	t.Helper()
	logFile, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logFile.Close() })
	before := 0
	if data, err := os.ReadFile(s.logPath); err == nil {
		before = strings.Count(string(data), "pogod listening on")
	}
	cmd := exec.Command(bin, "-port", strconv.Itoa(port))
	cmd.Dir = s.ws
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(),
		"HOME="+s.root,
		"XDG_CONFIG_HOME="+filepath.Join(s.root, ".config"),
		"POGO_HOME="+s.state,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting pogod: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	if wantServe {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(s.logPath); err == nil && strings.Count(string(data), "pogod listening on") > before {
				return cmd
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("pogod never started serving\n--- log ---\n%s", readFile(t, s.logPath))
	}
	return cmd
}

// lifeEvents returns the pogod_* lifecycle events in the sandbox's events.log.
func (s lifeSandbox) lifeEvents(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(s.state, "events.log"))
	if err != nil {
		t.Fatalf("events.log: %v\n--- pogod log ---\n%s", err, readFile(t, s.logPath))
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var ev map[string]any
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev["event_type"] {
		case daemonlife.EventBoot, daemonlife.EventShutdown:
			out = append(out, ev)
		}
	}
	return out
}

// heartbeatMtime is pogod's own heartbeat file's mtime (zero when absent).
func (s lifeSandbox) heartbeatMtime(t *testing.T) time.Time {
	t.Helper()
	fi, err := os.Stat(filepath.Join(s.state, "health", "pogod.heartbeat"))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// waitForBeatAfter waits until pogod's heartbeat file carries a beat newer than
// before — i.e. the running daemon has written its own.
func (s lifeSandbox) waitForBeatAfter(t *testing.T, before time.Time) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if m := s.heartbeatMtime(t); m.After(before) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pogod never wrote its own heartbeat (file mtime still %v)\n--- log ---\n%s", before, readFile(t, s.logPath))
}

func parseRFC(v any) time.Time {
	str, _ := v.(string)
	ts, _ := time.Parse(time.RFC3339Nano, str)
	return ts
}

func details(ev map[string]any) map[string]any {
	d, _ := ev["details"].(map[string]any)
	return d
}

func TestPogodRecordsBootShutdownAndUncleanDeath(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real pogods; skipped under -short")
	}
	bin := buildPogodUnderTest(t)
	s := newLifeSandbox(t)

	// Run 1: SIGTERM, the routine stop.
	c1 := s.start(t, bin, freePort(t), true)
	pid1 := c1.Process.Pid
	_ = c1.Process.Signal(syscall.SIGTERM)
	_ = c1.Wait()
	// The handler RECORDS and then re-delivers: the exit must still be death by
	// SIGTERM, exactly as before there was a handler.
	if ws, ok := c1.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("pogod exit after SIGTERM = %v; want killed by SIGTERM (the recorder must not change how pogod dies)", c1.ProcessState)
	}

	// Run 2: SIGKILL — the death nothing can record. Kill only once run 2's own
	// beat is OBSERVED on disk: the assertion below is about a run that got
	// as far as beating, and a sleep would be a guess at when that is. On a
	// Linux runner the first beat used to land ~30s after start, so this
	// killed a run that had none and read run 1's beat back (mg-e71d).
	beatBefore := s.heartbeatMtime(t)
	c2 := s.start(t, bin, freePort(t), true)
	pid2 := c2.Process.Pid
	s.waitForBeatAfter(t, beatBefore)
	_ = c2.Process.Kill()
	_ = c2.Wait()

	// Run 3: boots and names run 2's unclean death.
	c3 := s.start(t, bin, freePort(t), true)
	pid3 := c3.Process.Pid
	_ = c3.Process.Signal(syscall.SIGINT)
	_ = c3.Wait()

	evs := s.lifeEvents(t)
	var got []string
	for _, ev := range evs {
		got = append(got, ev["event_type"].(string))
	}
	want := []string{"pogod_boot", "pogod_shutdown", "pogod_boot", "pogod_boot", "pogod_shutdown"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lifecycle events = %v, want %v\n--- log ---\n%s", got, want, readFile(t, s.logPath))
	}

	num := func(v any) int { f, _ := v.(float64); return int(f) }
	prev := func(ev map[string]any) map[string]any { p, _ := details(ev)["previous"].(map[string]any); return p }

	if p := prev(evs[0]); p["state"] != daemonlife.StateUnknown {
		t.Errorf("first boot previous.state = %v, want unknown (no record yet)", p["state"])
	}
	if d := details(evs[1]); d["cause"] != "signal" || d["signal"] != "SIGTERM" || num(d["pid"]) != pid1 {
		t.Errorf("run 1 shutdown = %v, want cause=signal signal=SIGTERM pid=%d", d, pid1)
	}
	p := prev(evs[2])
	if p["state"] != daemonlife.StateClean || num(p["pid"]) != pid1 {
		t.Errorf("run 2 boot previous = %v, want clean pid=%d", p, pid1)
	}
	if exit, _ := p["exit"].(map[string]any); exit["signal"] != "SIGTERM" {
		t.Errorf("run 2 boot previous.exit = %v, want the SIGTERM run 1 recorded", p["exit"])
	}
	p = prev(evs[3])
	if p["state"] != daemonlife.StateUnclean || num(p["pid"]) != pid2 {
		t.Errorf("run 3 boot previous = %v, want unclean pid=%d (SIGKILLed)", p, pid2)
	}
	if beat, _ := p["last_heartbeat"].(string); beat == "" {
		t.Errorf("run 3 boot previous carries no last_heartbeat; that is the only bound on when run 2 died: %v", p)
	} else if b, s0 := parseRFC(beat), parseRFC(p["started_at"]); b.IsZero() || s0.IsZero() || b.Before(s0.Add(-time.Second)) {
		// A second of slack, as daemonlife allows: a filesystem stamps mtimes
		// from a coarser clock than time.Now, so a true beat can read early.
		t.Errorf("run 3 boot previous.last_heartbeat %s predates run 2's start %v: it is an earlier run's beat", beat, p["started_at"])
	}
	if d := details(evs[4]); d["signal"] != "SIGINT" || num(d["pid"]) != pid3 {
		t.Errorf("run 3 shutdown = %v, want SIGINT pid=%d", d, pid3)
	}
}

func TestPogodRecordsFatalExit(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real pogod; skipped under -short")
	}
	bin := buildPogodUnderTest(t)
	s := newLifeSandbox(t)

	// Hold the port so pogod's listen fails: a fatal exit after the lock.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	c := s.start(t, bin, port, false)
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("pogod did not exit on a held port\n--- log ---\n%s", readFile(t, s.logPath))
	}
	if c.ProcessState.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1 (same as the log.Fatalf this replaced)", c.ProcessState.ExitCode())
	}
	evs := s.lifeEvents(t)
	if len(evs) != 2 || evs[1]["event_type"] != daemonlife.EventShutdown {
		t.Fatalf("lifecycle events = %v, want boot then shutdown", evs)
	}
	d := details(evs[1])
	if d["cause"] != "fatal" || !strings.Contains(d["error"].(string), "failed to listen") {
		t.Errorf("shutdown details = %v, want cause=fatal naming the listen failure", d)
	}
	rec, err := daemonlife.Read(daemonlife.Path(s.state))
	if err != nil || rec == nil || rec.Shutdown == nil || rec.Shutdown.Cause != "fatal" {
		t.Errorf("lifecycle record = %+v (err %v), want a fatal shutdown stamped for the next boot", rec, err)
	}
}
