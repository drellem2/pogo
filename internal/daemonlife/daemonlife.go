// Package daemonlife records pogod's own lifecycle: a boot event on every
// start and a shutdown event on every exit path that can report one (mg-32f5).
//
// # Why this exists
//
// On 2026-09-08 ~18:55 the healthy pogod died and stayed down for 18 days.
// events.log recorded nothing about it: the last line before the hole was a
// stall_watch_fired at 17:55:17Z, and the schema had no pogod shutdown or exit
// event at all — only agent_stopped, which is about agents. What killed pogod
// was postmortem UNRESOLVED (b), and it stayed unresolved because the one
// process that knew was the one that died without saying.
//
// # The two halves
//
// pogod cannot record every death. SIGKILL, a panic on an unguarded goroutine,
// a kernel OOM kill and a host crash all end the process without running a
// single instruction of ours. So the design is two halves that make the
// unreportable deaths VISIBLE instead of silent:
//
//   - pogod_shutdown is emitted by the dying daemon on every path it CAN
//     observe: a catchable signal (named), a fatal error (with the error).
//   - pogod_boot is emitted by every starting daemon and names the previous
//     daemon's exit, read from a small state file (pogod.lifecycle.json under
//     POGO_HOME). When the previous daemon recorded a shutdown the boot repeats
//     it; when it did not, the boot says previous.state="unclean" and carries
//     the previous pid, start time and the mtime of its heartbeat file — the
//     tightest bound the box has on WHEN it died.
//
// An unclean death therefore shows up as a pogod_boot with no matching
// pogod_shutdown, and says so in its own details rather than leaving the reader
// to notice an absence.
package daemonlife

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Event types. Documented in docs/event-log.md ("Daemon lifecycle").
const (
	EventBoot     = "pogod_boot"
	EventShutdown = "pogod_shutdown"
	EventLockLost = "pogod_lock_lost"
	// EventSIGHUPIgnoredAtLaunch: pogod inherited SIGHUP as ignored (nohup, a
	// `trap '' HUP` wrapper) and caught-and-discarded it so its children exec
	// with SIGHUP at default (drellem2/pogo#106).
	EventSIGHUPIgnoredAtLaunch = "pogod_sighup_ignored_at_launch"
)

// Shutdown causes.
const (
	CauseSignal = "signal"
	CauseFatal  = "fatal"
)

// Previous-daemon states reported on pogod_boot.
const (
	// StateClean: the previous daemon recorded a shutdown before it exited.
	StateClean = "clean"
	// StateUnclean: the previous daemon recorded a boot and no shutdown — it
	// died on a path it could not report (SIGKILL, unguarded panic, OOM kill,
	// host crash, power loss) or it was killed mid-write.
	StateUnclean = "unclean"
	// StateAlive: the previous record's pid is still a live process. Normally
	// impossible — the lockfile excludes a second daemon — so this means the
	// lock was lost or bypassed, or the pid was reused by an unrelated process.
	StateAlive = "alive"
	// StateUnknown: no readable record — the first boot under this code, a
	// fresh POGO_HOME, or a corrupt file. Not evidence of anything.
	StateUnknown = "unknown"
)

// FileName is the lifecycle state file's name under POGO_HOME.
const FileName = "pogod.lifecycle.json"

// Record is the on-disk lifecycle record for one daemon run.
type Record struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Revision  string    `json:"revision,omitempty"`
	Shutdown  *Shutdown `json:"shutdown,omitempty"`
}

// Shutdown is how a daemon run ended, as that daemon observed it.
type Shutdown struct {
	At     time.Time `json:"at"`
	Cause  string    `json:"cause"`
	Signal string    `json:"signal,omitempty"`
	Error  string    `json:"error,omitempty"`
}

// Path returns the lifecycle file under pogoHome.
func Path(pogoHome string) string { return filepath.Join(pogoHome, FileName) }

// Read returns the record at path. A missing file is (nil, nil).
func Read(path string) (*Record, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &r, nil
}

// write replaces path atomically (temp file + rename), so a daemon killed
// mid-write leaves either the old record or the new one, never half of one.
func write(path string, r *Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Boot reads the previous run's record and replaces it with this run's. It
// returns the previous record (nil when there was none) and any read error
// separately from the write error, because an unreadable previous record must
// still let this run record itself.
func Boot(path string, cur Record) (prev *Record, readErr, writeErr error) {
	prev, readErr = Read(path)
	cur.Shutdown = nil
	writeErr = write(path, &cur)
	return prev, readErr, writeErr
}

// ErrNotOurs is returned by RecordShutdown when the file on disk belongs to a
// different pid — another daemon booted over this one's record, which is only
// possible if the lock was lost.
var ErrNotOurs = errors.New("lifecycle record belongs to another pid")

// RecordShutdown stamps sd onto the record for pid. It refuses (ErrNotOurs) to
// touch a record another pid owns: overwriting it would make a live daemon's
// run read as already shut down to the next boot.
func RecordShutdown(path string, pid int, sd Shutdown) error {
	r, err := Read(path)
	if err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("%s: no boot record", path)
	}
	if r.PID != pid {
		return fmt.Errorf("%w (file pid %d, ours %d)", ErrNotOurs, r.PID, pid)
	}
	r.Shutdown = &sd
	return write(path, r)
}

// PidAlive reports whether pid names a live process. EPERM counts as alive: the
// process exists and belongs to someone else.
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Classify decides the previous run's state. alive is PidAlive(prev.PID),
// injected so the judgement is testable without real processes.
func Classify(prev *Record, alive bool) string {
	switch {
	case prev == nil:
		return StateUnknown
	case prev.Shutdown != nil:
		return StateClean
	case alive:
		return StateAlive
	default:
		return StateUnclean
	}
}

// BootEvent builds the pogod_boot event for this run.
//
// prevState is Classify's answer. lastHeartbeat is the mtime of the previous
// daemon's heartbeat file (zero when absent); it is read BEFORE this run
// writes its own first heartbeat, so for an unclean death it bounds the time of
// death from below to within one heartbeat interval. When the file cannot speak
// for the previous run (absent, or older than that run's start) the event
// carries last_heartbeat: null and a no_heartbeat reason instead — see
// HeartbeatGap. readErr, when non-nil, is
// carried so an unreadable record is not mistaken for a first boot.
func BootEvent(cur Record, prev *Record, prevState string, lastHeartbeat time.Time, readErr error) events.Event {
	p := map[string]any{"state": prevState}
	if prev != nil {
		p["pid"] = prev.PID
		if !prev.StartedAt.IsZero() {
			p["started_at"] = prev.StartedAt.UTC().Format(time.RFC3339Nano)
		}
		if prev.Revision != "" {
			p["revision"] = prev.Revision
		}
		if sd := prev.Shutdown; sd != nil {
			p["exit"] = shutdownDetails(*sd)
		}
	}
	switch note := HeartbeatGap(prev, lastHeartbeat); {
	case note == "" && !lastHeartbeat.IsZero():
		p["last_heartbeat"] = lastHeartbeat.UTC().Format(time.RFC3339Nano)
	case note != "":
		// Present and null, not omitted: an absent field reads as "the schema
		// has no such thing", and this is the one case where its absence IS
		// the finding (mg-e71d).
		p["last_heartbeat"] = nil
		p["no_heartbeat"] = note
	}
	if readErr != nil {
		p["read_error"] = readErr.Error()
	}
	d := map[string]any{
		"pid":      cur.PID,
		"previous": p,
	}
	if cur.Revision != "" {
		d["revision"] = cur.Revision
	}
	return events.Event{
		Timestamp: cur.StartedAt.UTC().Format(time.RFC3339Nano),
		EventType: EventBoot,
		Agent:     "pogod",
		Details:   d,
	}
}

// beatClockSlack is how far a heartbeat mtime may sit BEFORE the run's recorded
// start and still be that run's beat. The boot writes its first beat
// microseconds after StartedAt is taken, but a filesystem stamps mtimes from a
// coarser clock than time.Now (ext4 takes the tick-granular kernel time), so a
// beat can legitimately read a few milliseconds early. A beat older than this
// was written by an EARLIER run.
const beatClockSlack = time.Second

// HeartbeatGap returns why lastHeartbeat bounds nothing about prev's death, or
// "" when it does (or when there is no previous run to attribute it to).
//
// Two ways the file can fail to speak for the previous run: it is absent, or
// its mtime predates that run's start — the run died before writing a beat, and
// what the file holds is an older run's last one. Reporting that older mtime
// as last_heartbeat would put the bound on the dead run's death BEFORE the run
// was born. Both happened on a Linux runner, where pogod's first beat used to
// land one full heartbeat interval (~30s) after start (mg-e71d).
func HeartbeatGap(prev *Record, lastHeartbeat time.Time) string {
	if prev == nil {
		return ""
	}
	if lastHeartbeat.IsZero() {
		return "no heartbeat recorded: the heartbeat file does not exist, so nothing bounds this run's death more tightly than started_at"
	}
	if !prev.StartedAt.IsZero() && lastHeartbeat.Before(prev.StartedAt.Add(-beatClockSlack)) {
		return fmt.Sprintf("no heartbeat recorded by this run: the heartbeat file's mtime %s predates the run's start, so an earlier run wrote it; nothing bounds this run's death more tightly than started_at",
			lastHeartbeat.UTC().Format(time.RFC3339Nano))
	}
	return ""
}

// ShutdownEvent builds the pogod_shutdown event for the run cur ending as sd.
func ShutdownEvent(cur Record, sd Shutdown) events.Event {
	d := shutdownDetails(sd)
	d["pid"] = cur.PID
	if !cur.StartedAt.IsZero() {
		d["started_at"] = cur.StartedAt.UTC().Format(time.RFC3339Nano)
		d["uptime_seconds"] = int64(sd.At.Sub(cur.StartedAt).Seconds())
	}
	if cur.Revision != "" {
		d["revision"] = cur.Revision
	}
	return events.Event{
		Timestamp: sd.At.UTC().Format(time.RFC3339Nano),
		EventType: EventShutdown,
		Agent:     "pogod",
		Details:   d,
	}
}

func shutdownDetails(sd Shutdown) map[string]any {
	d := map[string]any{"cause": sd.Cause}
	if !sd.At.IsZero() {
		d["at"] = sd.At.UTC().Format(time.RFC3339Nano)
	}
	if sd.Signal != "" {
		d["signal"] = sd.Signal
	}
	if sd.Error != "" {
		d["error"] = sd.Error
	}
	return d
}

// LockLostEvent builds pogod_lock_lost: the singleton lockfile no longer names
// this daemon. owner is the pid it names now (0 when unreadable), readErr the
// reason it could not be read.
func LockLostEvent(pid, owner int, lockPath string, readErr error, at time.Time) events.Event {
	d := map[string]any{"pid": pid, "lockfile": lockPath}
	if owner > 0 {
		d["owner_pid"] = owner
	}
	if readErr != nil {
		d["error"] = readErr.Error()
	}
	return events.Event{
		Timestamp: at.UTC().Format(time.RFC3339Nano),
		EventType: EventLockLost,
		Agent:     "pogod",
		Details:   d,
	}
}

// SIGHUPIgnoredAtLaunchEvent builds pogod_sighup_ignored_at_launch.
func SIGHUPIgnoredAtLaunchEvent(pid int, at time.Time) events.Event {
	return events.Event{
		Timestamp: at.UTC().Format(time.RFC3339Nano),
		EventType: EventSIGHUPIgnoredAtLaunch,
		Agent:     "pogod",
		Details: map[string]any{
			"pid":                   pid,
			"signal":                "SIGHUP",
			"pogod_disposition":     "caught-and-discarded",
			"child_disposition":     "default",
			"inherited_disposition": "ignored",
		},
	}
}

// SignalName is the conventional SIG* name. syscall.Signal.String() gives the
// strerror-style description ("terminated"), which is not what anyone greps for.
func SignalName(sig os.Signal) string {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return sig.String()
	}
	switch s {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGUSR1:
		return "SIGUSR1"
	case syscall.SIGUSR2:
		return "SIGUSR2"
	case syscall.SIGKILL:
		return "SIGKILL"
	}
	return fmt.Sprintf("signal %d (%s)", int(s), s.String())
}
