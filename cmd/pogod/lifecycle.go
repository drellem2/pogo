package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/nightlyone/lockfile"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/daemonlife"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/reaper"
	"github.com/drellem2/pogo/internal/version"
)

// pogod's own lifecycle on the event spine: pogod_boot on every start,
// pogod_shutdown on every exit path it can observe, pogod_lock_lost when the
// singleton lockfile stops naming it (mg-32f5). The rationale — and why an
// unreportable death is still made visible — is in internal/daemonlife.

// shutdownRecordBudget bounds how long a dying pogod spends recording its own
// death. `pogo server stop` gives pogod 5s in total before it escalates
// (internal/client.stopDaemon), so the recording must finish well inside that:
// a record that makes the stop miss its deadline turns a SIGTERM into a
// SIGKILL, which is the one death nothing can record.
const shutdownRecordBudget = 2 * time.Second

// lockWatchInterval is how often pogod re-reads its own lockfile.
const lockWatchInterval = time.Minute

// recordedSignals are the catchable signals whose DEFAULT disposition kills
// pogod. Each is recorded and then re-delivered at the default disposition, so
// pogod still dies exactly as it did before — same signal, same exit status,
// deferred functions still skipped, agents still taken down by the PTY hangup
// (see the mg-6b66 note in main). The handler adds a record; it does not add a
// graceful shutdown.
//
// SIGPIPE is deliberately absent. Go only kills the process on a SIGPIPE from a
// write to fd 1 or 2 while SIGPIPE is NOT notified; notifying it would turn that
// death into an EPIPE return — a behaviour change, not a record. mg-a7a1 removed
// the pipe that produced it; a SIGPIPE death still shows up as an unclean boot.
var recordedSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT}

// pogodLife is this run's lifecycle recorder; nil until bootLifecycle runs,
// which it does immediately after the lockfile is acquired.
var pogodLife *lifecycle

type lifecycle struct {
	path string
	cur  daemonlife.Record
	once sync.Once
}

// pogodHeartbeatFile is pogod's OWN heartbeat file (see the heartbeat OnTick in
// main). Shared so the boot record can read the previous run's last beat.
func pogodHeartbeatFile() string {
	return filepath.Join(config.PogoHome(), "health", "pogod.heartbeat")
}

// bootLifecycle replaces the previous run's lifecycle record with this run's
// and emits pogod_boot naming how the previous run ended. It reads the previous
// run's heartbeat and then writes this run's first one, so it must run before
// anything else writes the heartbeat file, or last_heartbeat would be our own.
func bootLifecycle(startedAt time.Time) *lifecycle {
	l := &lifecycle{
		path: daemonlife.Path(config.PogoHome()),
		cur: daemonlife.Record{
			PID:       os.Getpid(),
			StartedAt: startedAt,
			Revision:  version.Get().Commit,
		},
	}
	var lastBeat time.Time
	if fi, err := os.Stat(pogodHeartbeatFile()); err == nil {
		lastBeat = fi.ModTime()
	}
	// This run's FIRST beat, written the moment the previous run's has been
	// read. The heartbeat loop's first tick only seeds its baseline and calls
	// no OnTick, so the loop's first write lands one full interval (~30s) after
	// start: a daemon killed inside that window left no beat, and the next boot
	// either had no bound on its death or — worse — reported an EARLIER run's
	// beat as its last one. On darwin a spurious wake nudge at startup (the
	// `log stream` predicate banner, since filtered by mg-18b3) hid the
	// window; on a Linux runner it was the whole of a SIGKILL test (mg-e71d).
	// The periodic tick (main's hb.OnTick) keeps it fresh from here, and owns
	// the write-failure condition; a failure here is only logged.
	if err := reaper.WriteHeartbeat(pogodHeartbeatFile()); err != nil {
		log.Printf("pogod: cannot write first heartbeat %s: %v", pogodHeartbeatFile(), err)
	}
	prev, readErr, writeErr := daemonlife.Boot(l.path, l.cur)
	if writeErr != nil {
		// Not fatal: pogod runs without the record. Its shutdown event still
		// reaches events.log; only the NEXT boot's `previous` is lost.
		log.Printf("pogod: cannot write lifecycle record %s: %v", l.path, writeErr)
	}
	alive := prev != nil && prev.PID != l.cur.PID && daemonlife.PidAlive(prev.PID)
	state := daemonlife.Classify(prev, alive)
	ev := daemonlife.BootEvent(l.cur, prev, state, lastBeat, readErr)
	events.Emit(context.Background(), ev)
	log.Printf("pogod: boot recorded (pid=%d); previous run: %s", l.cur.PID, describePrevious(prev, state, lastBeat))
	return l
}

func describePrevious(prev *daemonlife.Record, state string, lastBeat time.Time) string {
	if prev == nil {
		return state + " (no lifecycle record)"
	}
	s := fmt.Sprintf("%s (pid=%d", state, prev.PID)
	if sd := prev.Shutdown; sd != nil {
		s += ", cause=" + sd.Cause
		if sd.Signal != "" {
			s += " " + sd.Signal
		}
		if sd.Error != "" {
			s += ": " + sd.Error
		}
	} else if state == daemonlife.StateUnclean {
		s += ", NO shutdown recorded — died on a path it could not report"
		if note := daemonlife.HeartbeatGap(prev, lastBeat); note != "" {
			s += "; " + note
		} else if !lastBeat.IsZero() {
			s += "; last heartbeat " + lastBeat.UTC().Format(time.RFC3339)
		}
	}
	return s + ")"
}

// recordShutdown writes the lifecycle record and emits pogod_shutdown, once per
// run, within shutdownRecordBudget. A second caller (a signal racing a fatal
// error) waits for the first to finish and records nothing.
func (l *lifecycle) recordShutdown(sd daemonlife.Shutdown) {
	if l == nil {
		return
	}
	l.once.Do(func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := daemonlife.RecordShutdown(l.path, l.cur.PID, sd); err != nil {
				log.Printf("pogod: cannot stamp shutdown on lifecycle record: %v", err)
			}
			events.Emit(context.Background(), daemonlife.ShutdownEvent(l.cur, sd))
		}()
		select {
		case <-done:
		case <-time.After(shutdownRecordBudget):
			log.Printf("pogod: shutdown record did not finish within %s; exiting anyway", shutdownRecordBudget)
		}
	})
}

// installSignalRecorder records each of recordedSignals and then dies of it.
// A signal pogod inherited as IGNORED (nohup's SIGHUP, a `&` job's SIGINT) is
// left ignored: notifying it would make pogod killable by a signal it is
// currently immune to.
func (l *lifecycle) installSignalRecorder() {
	var sigs []os.Signal
	for _, s := range recordedSignals {
		if !signal.Ignored(s) {
			sigs = append(sigs, s)
		}
	}
	if len(sigs) == 0 {
		return
	}
	ch := make(chan os.Signal, len(sigs))
	signal.Notify(ch, sigs...)
	go func() {
		sig := <-ch
		name := daemonlife.SignalName(sig)
		log.Printf("pogod: received %s; recording shutdown and exiting (pid=%d)", name, os.Getpid())
		l.recordShutdown(daemonlife.Shutdown{At: time.Now(), Cause: daemonlife.CauseSignal, Signal: name})
		dieOf(sig)
	}()
}

// dieOf re-delivers sig at its default disposition, so the exit status is the
// one the sender would have seen without a handler.
func dieOf(sig os.Signal) {
	signal.Reset(sig)
	s, ok := sig.(syscall.Signal)
	if ok {
		_ = syscall.Kill(os.Getpid(), s)
		// Delivery is asynchronous; give it a moment before falling back.
		time.Sleep(time.Second)
		os.Exit(128 + int(s))
	}
	os.Exit(1)
}

// fatalf is log.Fatalf that records the death first. Use it for every fatal
// exit after the lockfile is held.
func fatalf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Print(msg)
	pogodLife.recordShutdown(daemonlife.Shutdown{At: time.Now(), Cause: daemonlife.CauseFatal, Error: msg})
	os.Exit(1)
}

// exitFatalf keeps the fmt.Printf-to-stdout shape of pogod's early startup
// refusals, and records the death like fatalf.
func exitFatalf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Print(msg)
	pogodLife.recordShutdown(daemonlife.Shutdown{At: time.Now(), Cause: daemonlife.CauseFatal, Error: msg})
	os.Exit(1)
}

// watchLock re-reads the lockfile and emits pogod_lock_lost when it stops
// naming this pid — the file deleted, rewritten, or pointed at a dead pid. The
// lock is a pidfile, so "lost" means a second pogod can now TryLock
// successfully; it does not stop this one. pogod keeps running: exiting on a
// deleted file would let a stray `rm` take the whole fleet down, which is worse
// than the double-daemon risk this reports. One event per transition to lost; a
// log line when it names us again.
func (l *lifecycle) watchLock(lock lockfile.Lockfile, interval time.Duration) {
	agent.GoSafe("pogod.watchLock", func() {
		lost := false
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			owner, err := lock.GetOwner()
			ownerPID := 0
			if owner != nil {
				ownerPID = owner.Pid
			}
			nowLost := err != nil || ownerPID != l.cur.PID
			switch {
			case nowLost && !lost:
				log.Printf("pogod: LOCK LOST — %s no longer names pid %d (owner=%d err=%v); a second pogod could now start",
					string(lock), l.cur.PID, ownerPID, err)
				events.Emit(context.Background(), daemonlife.LockLostEvent(l.cur.PID, ownerPID, string(lock), err, time.Now()))
			case !nowLost && lost:
				log.Printf("pogod: lockfile %s names pid %d again", string(lock), l.cur.PID)
			}
			lost = nowLost
		}
	})
}
