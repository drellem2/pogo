package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// The coordinator-rename guard.
//
// The coordinator's name is policy — [agents] coordinator, defaulting to
// DefaultCoordinator — and everything load-bearing hangs off it: the agent's mg
// mailbox, its `mail-check-<name>` schedule id, the name the stall watcher arms
// on, the address the refinery mails merge results to, and the name pogod's
// auto-start spawns. Change the resolved name out from under a coordinator that
// is already running and none of that follows: the running process keeps its old
// mailbox while every other component addresses a name nobody answers to, and the
// next daemon restart auto-starts a second, differently-named coordinator in its
// place.
//
// Until now the only thing standing between a deployment and that outcome was a
// config key — the [agents] coordinator pin the default-migration guard writes
// (see migrate.go, mg-7d95). A pin is a fine belt; it is a bad only-belt. Any
// mishap that loses it — a partial config file shadowing the pinned one (the
// mg-cf9e footgun that loadConfigFiles now closes), an operator editing the wrong
// file, a config directory restored from a stale backup — re-arms the v0.4.0
// coordinator flip (mg-ce47) against an install that was explicitly protected
// from it.
//
// So the rename refuses at the source. A coordinator is *running* if the record
// below names it and its pid still answers signal 0; renaming a running
// coordinator is refused outright, whatever config says. That is what makes this
// whole class of config mishap non-fatal rather than merely unlikely: the worst a
// lost pin can now do is leave the wrong name in a file, which the next resolve
// overrides from the live process.
//
// Renaming a coordinator that is NOT running is allowed and always was — it is
// the documented way to rename the role (docs/CONFIGURATION.md). Stop it, edit
// the config, start it under the new name.

// coordinatorRecordName is the file under PogoHome that records which
// coordinator is running. It lives under PogoHome, not the config dir, because
// it is state and not configuration: two daemons on distinct POGO_HOME roots
// have distinct coordinators, and neither may guard the other's rename.
const coordinatorRecordName = "coordinator.json"

// RunningCoordinatorPath returns the path of the running-coordinator record.
func RunningCoordinatorPath() string {
	return filepath.Join(PogoHome(), coordinatorRecordName)
}

// CoordinatorRecord is the on-disk note that a coordinator process was started
// under a given name. It is written when the coordinator spawns and removed when
// it exits, so a record left behind by a pogod that was SIGKILLed (no exit hook
// ran) is still disarmed by the pid check in RunningCoordinator.
type CoordinatorRecord struct {
	// Name is the agent name the coordinator was started under.
	Name string `json:"name"`
	// PID is the coordinator process's pid.
	PID int `json:"pid"`
	// StartedAt is when the process was spawned. Diagnostic only — liveness is
	// decided by the pid, never by age.
	StartedAt time.Time `json:"started_at"`
}

// RecordRunningCoordinator notes that a coordinator named name is running as pid.
// Called by the agent registry on every coordinator spawn and respawn. The write
// is atomic (temp file + rename) so a concurrent reader never sees a half-written
// record and decides no coordinator is running.
func RecordRunningCoordinator(name string, pid int) error {
	if name == "" || pid <= 0 {
		return fmt.Errorf("coordinator record: bad name %q / pid %d", name, pid)
	}
	path := RunningCoordinatorPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(CoordinatorRecord{Name: name, PID: pid, StartedAt: time.Now()})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), coordinatorRecordName+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// ClearRunningCoordinator removes the record, but only when it is the one this
// exact process wrote. Matching on pid as well as name keeps an exiting
// coordinator from clearing the record its own respawn has already written.
func ClearRunningCoordinator(name string, pid int) {
	rec, err := readCoordinatorRecord()
	if err != nil || rec.Name != name || rec.PID != pid {
		return
	}
	os.Remove(RunningCoordinatorPath())
}

// RunningCoordinator returns the record of the coordinator currently running
// under this POGO_HOME, or nil when none is. A record whose pid no longer answers
// signal 0 is treated as absent: the coordinator stopped (or pogod died taking it
// along) and a rename is free to proceed.
func RunningCoordinator() *CoordinatorRecord {
	rec, err := readCoordinatorRecord()
	if err != nil || rec.Name == "" || !pidRunning(rec.PID) {
		return nil
	}
	return rec
}

func readCoordinatorRecord() (*CoordinatorRecord, error) {
	data, err := os.ReadFile(RunningCoordinatorPath())
	if err != nil {
		return nil, err
	}
	var rec CoordinatorRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// pidRunning reports whether pid names a live process, via signal 0 — the same
// probe agent.pidAlive uses on registry entries. A pid can in principle be
// recycled onto an unrelated process, which would make the guard refuse a rename
// it could have allowed. That is the safe direction to be wrong in, and the
// window is small: the record is removed when the coordinator exits.
func pidRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// RenameRefusal describes a coordinator rename the guard refused.
type RenameRefusal struct {
	// Configured is the name config resolved to — the rename that was refused.
	Configured string
	// Running is the name of the coordinator process that is actually running,
	// and the name the guard kept.
	Running string
	// PID is the running coordinator's pid.
	PID int
}

func (r *RenameRefusal) Error() string {
	return fmt.Sprintf("refusing to rename the running coordinator %q (pid %d) to %q; "+
		"stop it first if the rename is intended", r.Running, r.PID, r.Configured)
}

// GuardRunningCoordinator forces cfg's coordinator name back to the name of the
// coordinator that is currently running, when the two differ, and reports the
// refusal. It is a no-op when no coordinator is running, when the names already
// agree, or when cfg carries no coordinator name at all.
//
// It mutates cfg in place and returns it, so a caller can chain it onto Load().
// The stall watcher's agent follows along when — and only when — it was tracking
// the coordinator's pre-guard name; an explicitly configured [stall_watch] agent
// pointing somewhere else is left alone.
//
// Call it at the same seam that resolves role names, BEFORE any consumer reads
// cfg.Agents.Coordinator: both cmd/pogod and cmd/pogo do so in pinAndResolveRoles
// (mg-bc47, mg-cf9e).
func GuardRunningCoordinator(cfg *Config) (*Config, *RenameRefusal) {
	if cfg == nil || cfg.Agents.Coordinator == "" {
		return cfg, nil
	}
	rec := RunningCoordinator()
	if rec == nil || rec.Name == cfg.Agents.Coordinator {
		return cfg, nil
	}
	refusal := &RenameRefusal{Configured: cfg.Agents.Coordinator, Running: rec.Name, PID: rec.PID}
	if cfg.StallWatch.Agent == cfg.Agents.Coordinator {
		cfg.StallWatch.Agent = rec.Name
	}
	cfg.Agents.Coordinator = rec.Name
	return cfg, refusal
}
