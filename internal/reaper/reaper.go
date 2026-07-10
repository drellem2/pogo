// Package reaper implements pogod's tier-1 heartbeat reaper (mg-d18b).
//
// The reaper watches a declared list of launchd jobs and `launchctl kickstart`s
// any whose HEARTBEAT has gone stale. It exists to implement a specific
// definition of liveness that process supervision cannot:
//
//	launchd's KeepAlive — even on a perfectly healthy launchd — restarts a job
//	only when the job EXITS. It can never detect a job that is alive and not
//	working: a wedged run loop, a closed socket, a timer that never rearmed.
//	The process persists, so launchd sees a healthy job forever.
//
// So liveness here is HEARTBEAT FRESHNESS, never process existence and never
// PID liveness. Each watched job touches a state file at the end of a
// successful loop iteration; the reaper keys on that file's mtime against the
// job's known period. A job at state=running with a stale heartbeat is dead,
// and the reaper says so and acts.
//
// This is not a workaround for launchd's nondemand-spawn wedge on this host
// (mg-50e0). `launchctl kickstart` is a demand spawn, which works regardless;
// but the mechanism is warranted on any host, wedged or pristine, because the
// failure class it catches — alive-but-not-working — is invisible to
// exit-triggered supervision everywhere.
//
// Design hazards this package is built against, each learned the hard way:
//
//   - A reaper that endlessly kickstarts a job that FATALs on every start is a
//     NEW self-concealing failure (the mg-1679 shape: bridget-supervise
//     respawns into a FATAL every 10s while launchctl reports a pid). So
//     kickstarts are BOUNDED (default 3) and, on exhaustion, the reaper STOPS
//     and escalates loudly rather than looping. "Kickstarted N times, heartbeat
//     still stale" is the single most important line this reaper logs.
//
//   - A silent supervisor is the thing that one day conceals the failure
//     (com.pogo.recovery's six-week inertness; pogod's mail-check GC). So every
//     kickstart and every give-up is logged: job, staleness, action, pid.
//
//   - Liveness must not be inferred from a log line: a poller logs only when it
//     delivers, so a quiet mailbox is indistinguishable from a dead poller.
//     That is why this keys on a state-file mtime that ticks every cycle
//     regardless of delivery.
//
// Boundary: a fresh heartbeat proves the process is DOING work — NOT that it is
// running the CURRENT code. A job whose file was patched but whose process was
// never restarted keeps ticking its heartbeat while executing the old loop, and
// the reaper correctly leaves it alone. Detecting alive-but-running-old-code is
// mg-be0c's reconcile step, not this reaper's; the two are complementary, not
// overlapping. See docs/design/reaper-design.md.
package reaper

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultMaxKickstarts is the cap on consecutive kickstarts of a single job
// before the reaper gives up and escalates. Three attempts is enough to ride
// out a transient wedge; a job still stale after three restarts is FATALing on
// start (the mg-1679 shape) and must be surfaced to a human, not looped on.
const DefaultMaxKickstarts = 3

// DefaultInterval is how often the reaper sweeps its job list when the config
// does not specify one.
const DefaultInterval = 60 * time.Second

// Job is one launchd job the reaper supervises by heartbeat freshness.
type Job struct {
	// Label is the launchd label, e.g. "com.pogo.watchdog". kickstart targets
	// gui/$UID/<Label>.
	Label string
	// Heartbeat is the path to the state file whose mtime signals liveness.
	// The job must touch this at the end of every successful loop iteration.
	// A leading ~ is expanded to the user's home directory.
	Heartbeat string
	// Period is the job's known heartbeat cadence. The reaper treats the job as
	// dead once (now - mtime) exceeds Period. It is also the settle/backoff
	// window: after a kickstart, the reaper waits Period for a fresh heartbeat
	// before counting the attempt as failed.
	Period time.Duration
}

// jobState is the reaper's per-job memory across ticks.
type jobState struct {
	consecutive   int       // consecutive kickstarts that did not restore freshness
	lastKickstart time.Time // when we last kickstarted (zero = never)
	lastPID       int       // pid launchd reported after the last kickstart
	escalated     bool      // we have given up and mailed; do not repeat
}

// Reaper sweeps a set of Jobs and kickstarts any whose heartbeat is stale.
// Check is the unit of work and is deterministic given its injected clock,
// stat, and kickstart functions, which makes the whole liveness policy
// testable without launchd. Run drives Check on a ticker.
type Reaper struct {
	jobs          []Job
	maxKickstarts int
	state         map[string]*jobState

	now       func() time.Time
	stat      func(path string) (time.Time, error)
	kickstart func(label string) (pid int, err error)
	mail      func(to, from, subject, body string) error
	logf      func(format string, args ...any)
}

// Options configures a Reaper. Only Jobs and Kickstart are required in
// production; the rest default to real-world implementations.
type Options struct {
	Jobs          []Job
	MaxKickstarts int // <=0 falls back to DefaultMaxKickstarts

	// Kickstart forces a demand-spawn restart of the launchd job and returns
	// the pid launchd assigns. Required.
	Kickstart func(label string) (pid int, err error)
	// Mail escalates on give-up. Defaults to a no-op that logs.
	Mail func(to, from, subject, body string) error
	// Now defaults to time.Now.
	Now func() time.Time
	// Stat returns a file's mtime. Defaults to os.Stat-based mtime.
	Stat func(path string) (time.Time, error)
	// Logf defaults to log.Printf.
	Logf func(format string, args ...any)
}

// New builds a Reaper. Heartbeat paths are tilde-expanded once, here.
func New(o Options) *Reaper {
	r := &Reaper{
		jobs:          make([]Job, 0, len(o.Jobs)),
		maxKickstarts: o.MaxKickstarts,
		state:         make(map[string]*jobState),
		now:           o.Now,
		stat:          o.Stat,
		kickstart:     o.Kickstart,
		mail:          o.Mail,
		logf:          o.Logf,
	}
	if r.maxKickstarts <= 0 {
		r.maxKickstarts = DefaultMaxKickstarts
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.stat == nil {
		r.stat = statMtime
	}
	if r.logf == nil {
		r.logf = log.Printf
	}
	if r.mail == nil {
		r.mail = func(to, from, subject, body string) error {
			r.logf("reaper: (no mailer) would mail %s: %s", to, subject)
			return nil
		}
	}
	for _, j := range o.Jobs {
		j.Heartbeat = expandHome(j.Heartbeat)
		r.jobs = append(r.jobs, j)
		r.state[j.Label] = &jobState{}
	}
	return r
}

// Run sweeps every interval until ctx is cancelled. It logs its configuration
// once at startup — including an idle notice if no jobs are configured — so the
// running daemon's log answers "is the reaper on, and watching what?".
func (r *Reaper) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if len(r.jobs) == 0 {
		r.logf("reaper: enabled but no jobs configured — idle")
	} else {
		labels := make([]string, len(r.jobs))
		for i, j := range r.jobs {
			labels[i] = fmt.Sprintf("%s(period=%s)", j.Label, j.Period)
		}
		r.logf("reaper: watching %d job(s) every %s, max %d kickstart(s) before give-up: %s",
			len(r.jobs), interval, r.maxKickstarts, strings.Join(labels, ", "))
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.Check(now)
		}
	}
}

// Check runs one pass over every job. It is the deterministic core: given the
// injected clock, stat, and kickstart, its behavior is fully reproducible.
func (r *Reaper) Check(now time.Time) {
	for _, j := range r.jobs {
		r.checkJob(now, j)
	}
}

func (r *Reaper) checkJob(now time.Time, j Job) {
	st := r.state[j.Label]
	if st == nil {
		st = &jobState{}
		r.state[j.Label] = st
	}

	mtime, err := r.stat(j.Heartbeat)
	fresh := err == nil && now.Sub(mtime) <= j.Period

	if fresh {
		// Recovery: if we had been kickstarting this job, say so loudly and
		// reset. A recovery that goes unlogged is as opaque as a silent failure.
		if st.consecutive > 0 || st.escalated {
			r.logf("reaper: %s RECOVERED — heartbeat fresh (age %s) after %d kickstart(s)",
				j.Label, now.Sub(mtime).Round(time.Second), st.consecutive)
		}
		st.consecutive = 0
		st.escalated = false
		return
	}

	staleness := stalenessString(now, mtime, err)

	// Settle window: a just-kickstarted job needs time to write its first fresh
	// heartbeat. Until Period has elapsed since the last kickstart, we neither
	// re-judge nor re-kickstart — this is the backoff, and it is what keeps the
	// reaper from tight-looping a job that is legitimately still starting.
	if !st.lastKickstart.IsZero() && now.Sub(st.lastKickstart) < j.Period {
		return
	}

	// Bounded: on exhaustion, STOP and escalate — once — rather than loop. This
	// is the mg-1679 defense: a job that FATALs on every start never freshens
	// its heartbeat, so without this cap the reaper would kickstart it forever
	// while launchctl happily reports a fresh pid each time.
	if st.consecutive >= r.maxKickstarts {
		if !st.escalated {
			r.logf("reaper: GIVING UP on %s — kickstarted %d times, heartbeat still stale (%s, period %s). Escalating to mayor+human.",
				j.Label, st.consecutive, staleness, j.Period)
			r.escalate(j, st, staleness)
			st.escalated = true
		}
		return
	}

	// Kickstart. Loud: job, staleness, attempt, resulting pid.
	st.consecutive++
	st.lastKickstart = now
	pid, kerr := r.kickstart(j.Label)
	if kerr != nil {
		r.logf("reaper: %s STALE (%s) — kickstart attempt %d/%d FAILED: %v",
			j.Label, staleness, st.consecutive, r.maxKickstarts, kerr)
		return
	}
	st.lastPID = pid
	r.logf("reaper: %s STALE (%s) — kickstarted (attempt %d/%d), new pid %d",
		j.Label, staleness, st.consecutive, r.maxKickstarts, pid)
}

// escalate mails the mayor and the human. A give-up that only logs is still a
// silent failure to everyone who is not tailing pogod.log.
func (r *Reaper) escalate(j Job, st *jobState, staleness string) {
	subject := fmt.Sprintf("reaper gave up on %s — heartbeat still stale after %d kickstarts", j.Label, st.consecutive)
	body := fmt.Sprintf(
		"pogod's tier-1 reaper kickstarted %s %d times and its heartbeat (%s) is still stale (%s, period %s).\n"+
			"Last pid launchd reported: %d.\n\n"+
			"This is the mg-1679 shape: the job restarts but never does work — most likely FATALing on start.\n"+
			"The reaper has STOPPED kickstarting it to avoid a tight self-concealing loop. Manual investigation needed.",
		j.Label, st.consecutive, j.Heartbeat, staleness, j.Period, st.lastPID)
	for _, to := range []string{"mayor", "human"} {
		if err := r.mail(to, "pogod-reaper", subject, body); err != nil {
			r.logf("reaper: failed to mail %s about %s give-up: %v", to, j.Label, err)
		}
	}
}

// stalenessString renders the staleness for a log/mail line, distinguishing a
// missing heartbeat (job likely never started) from a merely old one.
func stalenessString(now, mtime time.Time, err error) string {
	if err != nil {
		return "heartbeat missing"
	}
	return "heartbeat age " + now.Sub(mtime).Round(time.Second).String()
}

// statMtime is the production Stat: a file's modification time.
func statMtime(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// WriteHeartbeat touches path (creating parent dirs) so its mtime is now. pogod
// uses this to publish its OWN heartbeat: the one liveness the tier-1 reaper
// cannot supervise is pogod itself (a child agent cannot reap its parent, and
// launchd will not — mg-50e0). Surfacing pogod's heartbeat where a human
// already looks is DETECTION, not recovery — the known single point of failure
// this tier explicitly leaves open. See docs/design/reaper-design.md.
func WriteHeartbeat(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return nil
	}
	// File likely does not exist yet — create it.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// expandHome expands a leading ~ to the user's home directory. A bare ~ or
// ~/... only; ~user is left untouched (unsupported).
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
